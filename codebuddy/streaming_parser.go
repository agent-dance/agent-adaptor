package codebuddy

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/agent-dance/agent-adaptor/driver"
)

// streamingState maps CodeBuddy stream-json stream_event frames (Anthropic
// Messages API-shaped) into StreamPayload. Its tool attribution and message
// usage rules belong to CodeBuddy's formal protocol.
type streamingState struct {
	sink   driver.EventSink
	runID  string
	parser *parser

	messageID   string
	textStarted map[int]bool
	blockKind   map[int]string
	toolBlocks  map[int]codeBuddyToolBlock
	toolResults map[[32]byte]struct{}
	thinkingID  map[int]string
	signatures  map[int]string

	runStarted      bool
	finishedEmitted bool
	apiRetryHits    int
	lastRetryWas5xx bool
	streamUsage     *driver.Usage
	usageMessageID  string
	usageByMessage  map[string]*driver.Usage
	stopReason      string
	numTurns        int
	terminalPayload map[string]any
}

type codeBuddyToolBlock struct {
	id, name           string
	initialInput       map[string]any
	deltas             *strings.Builder
	observationBlocked bool
}

func newStreamingState(sink driver.EventSink, runID string, p *parser) *streamingState {
	return &streamingState{
		sink:           sink,
		runID:          runID,
		parser:         p,
		textStarted:    make(map[int]bool),
		blockKind:      make(map[int]string),
		toolBlocks:     make(map[int]codeBuddyToolBlock),
		thinkingID:     make(map[int]string),
		signatures:     make(map[int]string),
		usageByMessage: make(map[string]*driver.Usage),
	}
}

func (s *streamingState) basePayload() driver.StreamPayload {
	return driver.StreamPayload{RunID: s.runID, ThreadID: s.parser.sessionID}
}

func (s *streamingState) emitStream(pl driver.StreamPayload) {
	if s.sink == nil || s.finishedEmitted {
		return
	}
	if pl.RunID == "" {
		pl.RunID = s.runID
	}
	if pl.ThreadID == "" {
		pl.ThreadID = s.parser.sessionID
	}
	_ = s.sink.EmitStream(pl)
	if pl.Kind == driver.StreamRunFinished || pl.Kind == driver.StreamRunError {
		s.finishedEmitted = true
	}
}

func (s *streamingState) markRunStarted() {
	if s.runStarted {
		return
	}
	s.runStarted = true
	pl := s.basePayload()
	pl.Kind = driver.StreamRunStarted
	pl.ThreadID = s.parser.sessionID
	s.emitStream(pl)
}

func (s *streamingState) handleSystemInit(_ map[string]any) {
	s.markRunStarted()
}

func (s *streamingState) handleAPIRetry(payload map[string]any) {
	s.emitStream(driver.StreamPayload{Name: "system.api_retry", Raw: payload})

	status, ok := httpStatusFromPayload(payload)
	is5xx := ok && status >= 500 && status < 600
	if is5xx {
		if s.lastRetryWas5xx {
			s.apiRetryHits++
		} else {
			s.apiRetryHits = 1
		}
		s.lastRetryWas5xx = true
	} else {
		s.lastRetryWas5xx = false
	}

	willRetry := true
	if v, ok := payload["will_retry"].(bool); ok {
		willRetry = v
	}

	if s.apiRetryHits >= 3 || (!willRetry && is5xx) {
		msg := topString(payload, "message", "error", "detail")
		if msg == "" {
			msg = "API retry exhausted"
		}
		s.emitErrorTerminal(&driver.RunFailure{Message: msg, Code: "api_retry"}, payload)
	}
}

func httpStatusFromPayload(payload map[string]any) (int, bool) {
	if v, ok := payload["error_status"].(float64); ok {
		return int(v), true
	}
	if v, ok := payload["error_status"].(int); ok {
		return v, true
	}
	if nested, ok := payload["error"].(map[string]any); ok {
		if v, ok := nested["status"].(float64); ok {
			return int(v), true
		}
		if code, ok := nested["code"].(string); ok {
			if n, err := strconv.Atoi(strings.TrimSpace(code)); err == nil {
				return n, true
			}
		}
	}
	return 0, false
}

func (s *streamingState) handleStreamEvent(rawLine string, outer map[string]any) {
	s.markRunStarted()
	if s.parser.observation != nil {
		s.parser.observation.suppressed = observationParentUnproved(outer)
		if s.parser.observation.suppressed {
			s.parser.observationNotice("observation_parent_unavailable")
		}
		defer func() { s.parser.observation.suppressed = false }()
	}
	eventAny, ok := outer["event"]
	if !ok {
		s.emitStream(driver.StreamPayload{Name: "stream_event", Raw: outer})
		return
	}
	eventObj, ok := eventAny.(map[string]any)
	if !ok {
		s.emitStream(driver.StreamPayload{Name: "stream_event", Raw: outer})
		return
	}

	switch strings.ToLower(asString(eventObj["type"])) {
	case "message_start":
		s.handleMessageStart(eventObj)
	case "content_block_start":
		s.handleContentBlockStart(eventObj)
	case "content_block_delta":
		s.handleContentBlockDelta(eventObj)
	case "content_block_stop":
		s.handleContentBlockStop(eventObj)
	case "message_delta":
		s.handleMessageDelta(eventObj)
	case "message_stop":
		s.usageMessageID = ""
	default:
		cp := cloneMapShallow(outer)
		cp["_stream_raw_line"] = rawLine
		s.emitStream(driver.StreamPayload{Name: strings.ToLower(asString(eventObj["type"])), Raw: cp})
	}
}

func (s *streamingState) handleMessageStart(event map[string]any) {
	s.stopReason = ""
	msg := topObject(event, "message")
	s.usageMessageID = topString(msg, "id")
	if id := topString(msg, "id"); id != "" {
		s.messageID = id
	}
	if msg != nil {
		if usage := topObject(msg, "usage"); usage != nil {
			s.mergeUsageMap(usage)
		}
	}
}

func (s *streamingState) handleContentBlockStart(event map[string]any) {
	idx := intFromAny(event["index"])
	block := topObject(event, "content_block")
	if block == nil {
		return
	}
	bt := strings.ToLower(topString(block, "type"))
	s.blockKind[idx] = bt

	switch bt {
	case "tool_use":
		id := exactString(block, "id")
		name := exactString(block, "name")
		tool := codeBuddyToolBlock{id: id, name: name, initialInput: topObject(block, "input")}
		if o := s.parser.observation; o != nil {
			tool.observationBlocked = o.suppressed
			if o.suppressed {
				s.parser.observeToolUse(name, id, nil)
			}
		}
		s.toolBlocks[idx] = tool
		pl := s.basePayload()
		pl.Kind = driver.StreamToolCallStart
		pl.ToolCallID = id
		pl.Name = name
		// The formal empty object opens an incremental argument stream; it is
		// not a completed argument snapshot. Preserve subsequent deltas only.
		input := block["input"]
		object, isObject := input.(map[string]any)
		if input != nil && !(isObject && len(object) == 0) {
			pl.Args = map[string]any{"input": input}
		}
		s.emitStream(pl)
	case "thinking":
		thID := fmt.Sprintf("thinking-%d", idx)
		s.thinkingID[idx] = thID
		pl := s.basePayload()
		pl.Kind = driver.StreamReasoningStart
		pl.MessageID = thID
		s.emitStream(pl)
	case "text":
		// Deferred StreamTextStart until first text_delta.
	}
}

func (s *streamingState) handleContentBlockDelta(event map[string]any) {
	idx := intFromAny(event["index"])
	delta, _ := event["delta"].(map[string]any)
	if delta == nil {
		return
	}
	switch strings.ToLower(asString(delta["type"])) {
	case "text_delta":
		text := exactString(delta, "text")
		if text == "" {
			return
		}
		if s.messageID == "" {
			s.messageID = "msg"
		}
		if !s.textStarted[idx] {
			s.textStarted[idx] = true
			pl := s.basePayload()
			pl.Kind = driver.StreamTextStart
			pl.MessageID = s.messageID
			s.emitStream(pl)
		}
		s.parser.appendTextDelta(s.messageID, text)
		pl := s.basePayload()
		pl.Kind = driver.StreamTextContent
		pl.MessageID = s.messageID
		pl.Delta = text
		s.emitStream(pl)

	case "input_json_delta":
		tool := s.toolBlocks[idx]
		if o := s.parser.observation; o != nil && o.suppressed {
			// An unproved wrapper cannot supply arguments to the root block.
			// Retain the original delta below, but invalidate its attribution
			// through the stop and any later full wrapper for this call ID.
			tool.observationBlocked = true
			s.parser.observeToolUse(tool.name, tool.id, nil)
		}
		raw := exactString(delta, "partial_json")
		if tool.deltas == nil {
			tool.deltas = &strings.Builder{}
		}
		tool.deltas.WriteString(raw)
		s.toolBlocks[idx] = tool
		tid := tool.id
		if tid == "" {
			tid = fmt.Sprintf("idx-%d", idx)
		}
		pl := s.basePayload()
		pl.Kind = driver.StreamToolCallArgs
		pl.ToolCallID = tid
		pl.Delta = raw
		s.emitStream(pl)

	case "thinking_delta":
		thinking := exactString(delta, "thinking")
		if thinking == "" {
			return
		}
		thID := s.thinkingID[idx]
		if thID == "" {
			thID = fmt.Sprintf("thinking-%d", idx)
			s.thinkingID[idx] = thID
		}
		pl := s.basePayload()
		pl.Kind = driver.StreamReasoningContent
		pl.MessageID = thID
		pl.Delta = thinking
		s.emitStream(pl)

	case "signature_delta":
		if sig := exactString(delta, "signature"); sig != "" {
			s.signatures[idx] += sig
		}
	default:
		s.emitStream(driver.StreamPayload{Name: strings.ToLower(asString(delta["type"])), Raw: cloneMapShallow(delta)})
	}
}

func (s *streamingState) handleContentBlockStop(event map[string]any) {
	idx := intFromAny(event["index"])
	bt := strings.ToLower(s.blockKind[idx])

	switch bt {
	case "text":
		if s.textStarted[idx] && s.messageID != "" {
			pl := s.basePayload()
			pl.Kind = driver.StreamTextEnd
			pl.MessageID = s.messageID
			s.emitStream(pl)
		}
	case "tool_use":
		tool := s.toolBlocks[idx]
		input := tool.initialInput
		if buf := tool.deltas; buf != nil {
			input = nil
			if err := json.Unmarshal([]byte(buf.String()), &input); err != nil {
				s.parser.observationNotice("observation_input_invalid")
			}
		}
		if !tool.observationBlocked {
			s.parser.observeToolUse(tool.name, tool.id, input)
		}
		if tid := tool.id; tid != "" {
			pl := s.basePayload()
			pl.Kind = driver.StreamToolCallEnd
			pl.ToolCallID = tid
			s.emitStream(pl)
		}
	case "thinking":
		if thID := s.thinkingID[idx]; thID != "" {
			pl := s.basePayload()
			pl.Kind = driver.StreamReasoningEnd
			pl.MessageID = thID
			s.emitStream(pl)
		}
	default:
		if bt != "" {
			s.emitStream(driver.StreamPayload{Name: "content_block_stop", Raw: map[string]any{"index": idx, "kind": bt}})
		}
	}

	delete(s.blockKind, idx)
	delete(s.textStarted, idx)
	delete(s.toolBlocks, idx)
	delete(s.thinkingID, idx)
	delete(s.signatures, idx)
}

func (s *streamingState) handleMessageDelta(event map[string]any) {
	if d, ok := event["delta"].(map[string]any); ok {
		if sr := topString(d, "stop_reason"); sr != "" {
			s.stopReason = sr
		}
	}
	if u := topObject(event, "usage"); u != nil {
		s.mergeUsageMap(u)
	}
	if md, ok := event["delta"].(map[string]any); ok {
		if u := topObject(md, "usage"); u != nil {
			s.mergeUsageMap(u)
		}
	}
}

func (s *streamingState) mergeUsageMap(u map[string]any) {
	// Usage updates are cumulative within one formal message, while distinct
	// message IDs contribute independently to this turn. Without a message ID
	// there is no reliable scope for attributing or deduplicating a counter.
	if s.usageMessageID == "" {
		return
	}
	input, inputOK := codeBuddyUsageCounter(u, "input_tokens")
	cached, cachedOK := codeBuddyUsageCounter(u, "cache_read_input_tokens", "cached_input_tokens")
	output, outputOK := codeBuddyUsageCounter(u, "output_tokens")
	if !inputOK && !cachedOK && !outputOK {
		return
	}
	if s.streamUsage == nil {
		s.streamUsage = &driver.Usage{}
	}
	message := s.usageByMessage[s.usageMessageID]
	if message == nil {
		message = &driver.Usage{}
		s.usageByMessage[s.usageMessageID] = message
	}
	if inputOK && input > message.InputTokens {
		s.streamUsage.InputTokens += input - message.InputTokens
		message.InputTokens = input
	}
	if cachedOK && cached > message.CachedInputTokens {
		s.streamUsage.CachedInputTokens += cached - message.CachedInputTokens
		message.CachedInputTokens = cached
	}
	if outputOK && output > message.OutputTokens {
		s.streamUsage.OutputTokens += output - message.OutputTokens
		message.OutputTokens = output
	}
}

func codeBuddyUsageCounter(usage map[string]any, fields ...string) (int, bool) {
	for _, field := range fields {
		value, ok := topInt(usage, field)
		if !ok {
			continue
		}
		if value < 0 {
			return 0, false
		}
		// As with topInt, the first numeric field is authoritative, including
		// zero. Aliases represent the same counter and are never added together.
		switch original := usage[field].(type) {
		case float64:
			return value, float64(value) == original
		case int64:
			return value, int64(value) == original
		default:
			return value, true
		}
	}
	return 0, false
}

func (s *streamingState) handleUserToolResult(block, wrapper map[string]any) {
	// Only identical official wrappers/blocks with a real ID are replays.
	// The full wrapper includes its parent field, so an unproved or different
	// scope cannot suppress a root result. Raw and Transcript keep both copies.
	if exactString(block, "tool_use_id") != "" {
		if raw, err := json.Marshal([]any{wrapper, block}); err == nil {
			key := sha256.Sum256(raw)
			if _, seen := s.toolResults[key]; seen {
				return
			}
			if s.toolResults == nil {
				s.toolResults = make(map[[32]byte]struct{})
			}
			s.toolResults[key] = struct{}{}
		}
	}
	id := topString(block, "tool_use_id")
	text := resultText(block["content"])
	isError := false
	if v, ok := block["is_error"].(bool); ok {
		isError = v
	}
	pl := s.basePayload()
	pl.Kind = driver.StreamToolCallResult
	pl.ToolCallID = id
	pl.Result = map[string]any{"text": text, "is_error": isError, "tool_use_id": id}
	s.emitStream(pl)
}

func (s *streamingState) handleResultTerminal(payload map[string]any) {
	s.markRunStarted()
	if s.finishedEmitted {
		return
	}
	// Stage the official terminal until parser.finalize has consumed the whole
	// stdout stream. A malformed or post-terminal frame must turn both the
	// Driver response and the typed stream into failure; emitting RunFinished
	// eagerly here would make those two public views disagree.
	s.terminalPayload = cloneMapShallow(payload)
}

func (s *streamingState) finishResultTerminal(payload map[string]any) {
	s.closeOpenLifecycles()
	if s.parser.protocolMalformed || !s.parser.terminalSuccess {
		msg := s.parser.errorMessage
		if msg == "" {
			if s.parser.protocolMalformed {
				msg = "codebuddy protocol was malformed"
			} else {
				msg = "codebuddy terminal result did not report success"
			}
		}
		s.emitErrorTerminal(&driver.RunFailure{Message: msg, Code: driver.FailureAgentError}, payload)
		return
	}

	pl := s.basePayload()
	pl.Kind = driver.StreamRunFinished

	if s.parser.usage != nil {
		u := *s.parser.usage
		pl.Usage = &u
	} else if s.streamUsage != nil {
		u := *s.streamUsage
		pl.Usage = &u
	}

	raw := map[string]any{"stop_reason": s.stopReason}
	if v, ok := topFloat(payload, "total_cost_usd"); ok {
		raw["total_cost_usd"] = v
	}
	if subtype := topString(payload, "subtype"); subtype != "" {
		raw["subtype"] = subtype
	}
	if v, ok := payload["num_turns"].(float64); ok {
		raw["num_turns"] = int(v)
		s.numTurns = int(v)
	}
	pl.Raw = raw

	s.emitStream(pl)
}

func (s *streamingState) handleErrorTerminal(payload map[string]any) {
	msg := topString(payload, "message", "error")
	code := topString(payload, "code")
	if msg == "" {
		msg = "codebuddy stream error"
	}
	s.emitErrorTerminal(&driver.RunFailure{Message: msg, Code: driver.FailureCode(code)}, payload)
}

func (s *streamingState) closeOpenLifecycles() {
	pending := make([]int, 0, len(s.blockKind))
	for idx := range s.blockKind {
		pending = append(pending, idx)
	}
	for _, idx := range pending {
		switch strings.ToLower(s.blockKind[idx]) {
		case "text":
			if s.textStarted[idx] && s.messageID != "" {
				pl := s.basePayload()
				pl.Kind = driver.StreamTextEnd
				pl.MessageID = s.messageID
				s.emitStream(pl)
			}
		case "tool_use":
			if tid := s.toolBlocks[idx].id; tid != "" {
				pl := s.basePayload()
				pl.Kind = driver.StreamToolCallEnd
				pl.ToolCallID = tid
				s.emitStream(pl)
			}
		case "thinking":
			if thID := s.thinkingID[idx]; thID != "" {
				pl := s.basePayload()
				pl.Kind = driver.StreamReasoningEnd
				pl.MessageID = thID
				s.emitStream(pl)
			}
		}
	}
	s.blockKind = nil
	s.textStarted = nil
	// Keep the buffer map writable for unbound deltas drained after a retry
	// terminal, while forgetting every completed block's identity and input.
	clear(s.toolBlocks)
	s.thinkingID = nil
	s.signatures = nil
}

func (s *streamingState) emitErrorTerminal(failure *driver.RunFailure, raw map[string]any) {
	if s == nil || s.sink == nil || s.finishedEmitted {
		return
	}
	s.markRunStarted()
	// Close observed invocations before the terminal seals the core sink.
	// Tracker.Close is idempotent when parser completion reaches it again.
	s.parser.closeObservations()
	s.closeOpenLifecycles()
	s.emitStream(driver.StreamPayload{Kind: driver.StreamRunError, Error: failure, Raw: raw})
}

func (s *streamingState) complete(failure *driver.RunFailure, exitCode int, signal string, timedOut bool) {
	if s == nil || s.sink == nil || s.finishedEmitted {
		return
	}
	s.markRunStarted()
	if failure == nil {
		switch {
		case timedOut:
			failure = &driver.RunFailure{Code: driver.FailureAgentError, Message: "codebuddy process timed out"}
		case signal != "":
			failure = &driver.RunFailure{Code: driver.FailureAgentError, Message: "codebuddy process exited after signal " + signal}
		case exitCode != 0:
			failure = &driver.RunFailure{Code: driver.FailureAgentError, Message: fmt.Sprintf("codebuddy process exited with code %d", exitCode)}
		}
	}
	if failure != nil {
		raw := s.terminalPayload
		if raw == nil {
			raw = map[string]any{"reason": "missing_or_invalid_terminal"}
		}
		s.emitErrorTerminal(failure, raw)
		return
	}
	if s.terminalPayload != nil {
		s.finishResultTerminal(s.terminalPayload)
		return
	}
	s.closeOpenLifecycles()

	s.emitErrorTerminal(&driver.RunFailure{
		Code:    driver.FailureAgentError,
		Message: "codebuddy protocol ended without a terminal result",
	}, map[string]any{"reason": "missing_terminal"})
}

func asString(v any) string {
	if t, ok := v.(string); ok {
		return t
	}
	return ""
}

func intFromAny(v any) int {
	switch t := v.(type) {
	case float64:
		return int(t)
	case int:
		return t
	case int64:
		return int(t)
	case json.Number:
		n, _ := t.Int64()
		return int(n)
	default:
		return 0
	}
}

func exactString(payload map[string]any, keys ...string) string {
	for _, key := range keys {
		raw, ok := payload[key]
		if !ok {
			continue
		}
		if value, ok := raw.(string); ok {
			return value
		}
	}
	return ""
}

func cloneMapShallow(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
