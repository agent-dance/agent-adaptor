package claude

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/agent-dance/agent-adaptor/driver"
)

// streamingState maps Claude stream-json stream_event frames (Anthropic Messages
// API-shaped) into StreamPayload. It does not participate in checkpoint
// construction; session capture stays in the main parser.
type streamingState struct {
	sink        driver.EventSink
	runID       string
	parser      *claudeParser
	messageID   string
	scope       toolScope
	messageIDs  map[string]string
	blockTools  map[claudeBlockKey]claudeToolBlock
	textStarted map[claudeBlockKey]string // message ID fixed at each text start
	blockKind   map[claudeBlockKey]string
	thinkingID  map[claudeBlockKey]string
	signatures  map[claudeBlockKey]string

	runStarted      bool
	finishedEmitted bool
	streamUsage     *driver.Usage
	messageUsage    map[claudeUsageKey]*driver.Usage
	activeUsage     map[string]claudeUsageKey
	anonymousUsage  uint64
	stopReason      string
	terminalPayload map[string]any
}

type claudeBlockKey struct {
	scope string
	index int
}

type claudeToolBlock struct {
	call   *observedTool
	replay bool
}

func (s *streamingState) blockIndex(event map[string]any) claudeBlockKey {
	return claudeBlockKey{s.scope.id, intFromAny(event["index"])}
}

type claudeUsageKey struct {
	parent    string
	id        string
	anonymous uint64
}

func newStreamingState(sink driver.EventSink, runID string, p *claudeParser) *streamingState {
	return &streamingState{
		sink:         sink,
		messageIDs:   map[string]string{},
		blockTools:   map[claudeBlockKey]claudeToolBlock{},
		runID:        runID,
		parser:       p,
		messageUsage: make(map[claudeUsageKey]*driver.Usage),
		activeUsage:  make(map[string]claudeUsageKey),
		textStarted:  make(map[claudeBlockKey]string),
		blockKind:    make(map[claudeBlockKey]string),
		thinkingID:   make(map[claudeBlockKey]string),
		signatures:   make(map[claudeBlockKey]string),
	}
}

func (s *streamingState) basePayload() driver.StreamPayload {
	p := driver.StreamPayload{
		RunID:    s.runID,
		ThreadID: s.parser.sessionID,
	}
	return p
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
	s.emitStream(driver.StreamPayload{
		Name: "system.api_retry",
		Raw:  payload,
	})
}

func (s *streamingState) handleStreamEvent(rawLine string, outer map[string]any) {
	s.markRunStarted()
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

	// Nested subagent messages can end while the root is awaiting a tool
	// response. Only root message state may close the run's stdin.
	parent, validParent := claudeParentID(outer)
	if !validParent {
		s.parser.observationNotice("parent_unresolved")
		return
	}
	scope, resolved := s.parser.wrapperScope(outer)
	s.scope = scope
	s.messageID = s.messageIDs[scope.id]
	rootMessage := parent == ""
	evType := strings.ToLower(asString(eventObj["type"]))
	switch evType {
	case "message_start":
		if rootMessage {
			s.stopReason = ""
		}
		s.handleMessageStart(eventObj, parent, resolved)
	case "content_block_start":
		if !resolved {
			return
		}
		s.handleContentBlockStart(eventObj)
	case "content_block_delta":
		if !resolved {
			return
		}
		s.handleContentBlockDelta(eventObj)
	case "content_block_stop":
		if !resolved {
			return
		}
		s.handleContentBlockStop(eventObj)
	case "message_delta":
		if rootMessage {
			if delta := claudeTopLevelObject(eventObj, "delta"); delta != nil {
				if reason := claudeTopLevelString(delta, "stop_reason"); reason != "" {
					s.stopReason = reason
				}
			}
		}
		s.handleMessageDelta(eventObj, parent)
	case "message_stop":
		// See onAssistantMessageStop: close interactive stdin after a
		// terminal model turn so the CLI can exit and unblock the host.
		if rootMessage && s.parser != nil {
			s.parser.onAssistantMessageStop(s.stopReason)
		}
	default:
		cp := cloneMapShallow(outer)
		cp["_stream_raw_line"] = rawLine
		s.emitStream(driver.StreamPayload{Name: evType, Raw: cp})
	}
}

func (s *streamingState) handleMessageStart(event map[string]any, parent string, resolved bool) {
	msg := claudeTopLevelObject(event, "message")
	id := claudeTopLevelString(msg, "id")
	key := s.usageKey(id, parent)
	s.activeUsage[parent] = key
	if id != "" && resolved {
		s.messageID = id
		s.messageIDs[s.scope.id] = id
	}
	if msg != nil {
		if usage := claudeTopLevelObject(msg, "usage"); usage != nil {
			s.mergeUsageMap(key, usage)
		}
	}
}

func (s *streamingState) handleContentBlockStart(event map[string]any) {
	idx := s.blockIndex(event)
	block := claudeTopLevelObject(event, "content_block")
	if block == nil {
		return
	}
	bt := strings.ToLower(claudeTopLevelString(block, "type"))
	s.blockKind[idx] = bt

	switch bt {
	case "tool_use":
		id, name := claudeExactString(block, "id"), claudeExactString(block, "name")
		input, _ := block["input"].(map[string]any)
		call, fresh := s.parser.startTool(s.scope, id, name, input, len(input) == 0)
		s.blockTools[idx] = claudeToolBlock{call: call, replay: !fresh}
		if fresh && s.scope.id == "" {
			s.parser.interactiveOnToolUseStart(idx.index, name, id)
		}
	case "thinking":
		thID := fmt.Sprintf("thinking-%s-%d", idx.scope, idx.index)
		s.thinkingID[idx] = thID
		pl := s.basePayload()
		pl.Kind = driver.StreamReasoningStart
		pl.MessageID = thID
		s.emitStream(pl)
	case "text":
		// Deferred StreamTextStart until first text_delta.
	default:
		// Unknown block: already in blockKind; stop handler may emit opaque ends.
	}
}

func (s *streamingState) handleContentBlockDelta(event map[string]any) {
	idx := s.blockIndex(event)
	delta, _ := event["delta"].(map[string]any)
	if delta == nil {
		return
	}
	dt := strings.ToLower(asString(delta["type"]))
	switch dt {
	case "text_delta":
		text := claudeExactString(delta, "text")
		if text == "" {
			return
		}
		if s.messageID == "" {
			s.messageID = "msg"
		}
		if s.textStarted[idx] == "" {
			s.textStarted[idx] = s.messageID
			pl := s.basePayload()
			pl.Kind = driver.StreamTextStart
			pl.MessageID = s.messageID
			s.emitStream(pl)
		}
		pl := s.basePayload()
		pl.Kind = driver.StreamTextContent
		pl.MessageID = s.textStarted[idx]
		pl.Delta = text
		s.emitStream(pl)

	case "input_json_delta":
		raw := claudeExactString(delta, "partial_json")
		if block := s.blockTools[idx]; !block.replay {
			s.parser.appendToolArgs(block.call, raw)
		}
		if s.scope.id == "" {
			s.parser.interactiveOnToolUseDelta(idx.index, raw)
		}

	case "thinking_delta":
		thinking := claudeExactString(delta, "thinking")
		if thinking == "" {
			return
		}
		thID := s.thinkingID[idx]
		if thID == "" {
			thID = fmt.Sprintf("thinking-%s-%d", idx.scope, idx.index)
			s.thinkingID[idx] = thID
		}
		pl := s.basePayload()
		pl.Kind = driver.StreamReasoningContent
		pl.MessageID = thID
		pl.Delta = thinking
		s.emitStream(pl)

	case "signature_delta":
		sig := claudeExactString(delta, "signature")
		if sig != "" {
			s.signatures[idx] += sig
		}
	default:
		s.emitStream(driver.StreamPayload{Name: dt, Raw: cloneMapShallow(delta)})
	}
}

func (s *streamingState) handleContentBlockStop(event map[string]any) {
	idx := s.blockIndex(event)
	bt := strings.ToLower(s.blockKind[idx])

	switch bt {
	case "text":
		if messageID := s.textStarted[idx]; messageID != "" {
			pl := s.basePayload()
			pl.Kind = driver.StreamTextEnd
			pl.MessageID = messageID
			s.emitStream(pl)
		}
	case "tool_use":
		if block := s.blockTools[idx]; !block.replay {
			s.parser.endTool(block.call)
		}
		if s.scope.id == "" {
			s.parser.interactiveOnToolUseStop(idx.index)
		}
		delete(s.blockTools, idx)
	case "thinking":
		thID := s.thinkingID[idx]
		if thID != "" {
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
	delete(s.thinkingID, idx)
	delete(s.signatures, idx)
}

func (s *streamingState) handleMessageDelta(event map[string]any, parent string) {
	key, ok := s.activeUsage[parent]
	if !ok {
		key = s.usageKey("", parent)
		s.activeUsage[parent] = key
	}
	if u := claudeTopLevelObject(event, "usage"); u != nil {
		s.mergeUsageMap(key, u)
	}
	if md, ok := event["delta"].(map[string]any); ok {
		if u := claudeTopLevelObject(md, "usage"); u != nil {
			s.mergeUsageMap(key, u)
		}
	}
}

func (s *streamingState) usageKey(id, parent string) claudeUsageKey {
	if id != "" {
		return claudeUsageKey{id: id, parent: parent}
	}
	s.anonymousUsage++
	return claudeUsageKey{anonymous: s.anonymousUsage, parent: parent}
}

func (s *streamingState) mergeAssistantUsage(message map[string]any, parent string) {
	u := claudeTopLevelObject(message, "usage")
	if u == nil {
		return
	}
	id := claudeTopLevelString(message, "id")
	key, ok := s.activeUsage[parent]
	if id != "" || !ok {
		key = s.usageKey(id, parent)
	}
	s.mergeUsageMap(key, u)
}

func (s *streamingState) mergeUsageMap(key claudeUsageKey, u map[string]any) {
	input, okInput := claudeUsageCounter(u, "input_tokens")
	cached, okCached := claudeUsageCounter(u, "cache_read_input_tokens")
	output, okOutput := claudeUsageCounter(u, "output_tokens")
	if !okInput && !okCached && !okOutput {
		return
	}
	if s.streamUsage == nil {
		s.streamUsage = &driver.Usage{}
	}
	message := s.messageUsage[key]
	if message == nil {
		message = &driver.Usage{}
		s.messageUsage[key] = message
	}
	// Message deltas and the eventual assistant snapshot repeat cumulative
	// counters. Add only that message's increase to the run aggregate.
	if okInput {
		if input > message.InputTokens {
			s.streamUsage.InputTokens += input - message.InputTokens
			message.InputTokens = input
		}
	}
	if okCached {
		if cached > message.CachedInputTokens {
			s.streamUsage.CachedInputTokens += cached - message.CachedInputTokens
			message.CachedInputTokens = cached
		}
	}
	if okOutput {
		// message_delta.usage.output_tokens is cumulative along the message.
		if output > message.OutputTokens {
			s.streamUsage.OutputTokens += output - message.OutputTokens
			message.OutputTokens = output
		}
	}
}

func (s *streamingState) handleResultTerminal(payload map[string]any) {
	// A result frame is provider evidence, not the final SDK outcome. More
	// stdout, process failure, HITL failure, fork validation, or structured
	// output validation may still invalidate it. Driver.Run calls complete
	// exactly once after all of those gates have frozen the outcome.
	s.terminalPayload = cloneMapShallow(payload)
}

func (s *streamingState) handleErrorTerminal(payload map[string]any) {
	s.terminalPayload = cloneMapShallow(payload)
}

func (s *streamingState) closeOpenLifecycles() {
	pending := make([]claudeBlockKey, 0, len(s.blockKind))
	for idx := range s.blockKind {
		pending = append(pending, idx)
	}
	sort.Slice(pending, func(i, j int) bool {
		if pending[i].scope == pending[j].scope {
			return pending[i].index < pending[j].index
		}
		return pending[i].scope < pending[j].scope
	})
	for _, idx := range pending {
		bt := strings.ToLower(s.blockKind[idx])

		switch bt {
		case "text":
			if messageID := s.textStarted[idx]; messageID != "" {
				pl := s.basePayload()
				pl.Kind = driver.StreamTextEnd
				pl.MessageID = messageID
				s.emitStream(pl)
			}
		case "tool_use":
			// Parser closes tools in their accepted start order across all scopes.
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
	s.thinkingID = nil
	s.signatures = nil
}

func (s *streamingState) emitErrorTerminal(failure *driver.RunFailure, raw map[string]any) {
	if s == nil || s.sink == nil || s.finishedEmitted {
		return
	}
	s.markRunStarted()
	s.closeOpenLifecycles()
	s.emitStream(driver.StreamPayload{Kind: driver.StreamRunError, Error: failure, Raw: raw})
}

func (s *streamingState) complete(failure *driver.RunFailure, exitCode int, signal string, timedOut bool) {
	if s == nil || s.sink == nil || s.finishedEmitted {
		return
	}
	s.markRunStarted()
	s.closeOpenLifecycles()

	if failure == nil {
		switch {
		case timedOut:
			failure = &driver.RunFailure{Code: driver.FailureAgentError, Message: "claude process timed out"}
		case signal != "":
			failure = &driver.RunFailure{Code: driver.FailureAgentError, Message: "claude process exited after signal " + signal}
		case exitCode != 0:
			failure = &driver.RunFailure{Code: driver.FailureAgentError, Message: fmt.Sprintf("claude process exited with code %d", exitCode)}
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

	pl := s.basePayload()
	pl.Kind = driver.StreamRunFinished
	if s.parser.usage != nil {
		u := *s.parser.usage
		pl.Usage = &u
	} else if s.streamUsage != nil {
		u := *s.streamUsage
		pl.Usage = &u
	}
	pl.Raw = s.terminalPayload
	s.emitStream(pl)
}

func asString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	default:
		return ""
	}
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

// claudeExactString reads a string field without trimming — whitespace-only
// deltas must survive for faithful streaming reconstruction.
func claudeExactString(payload map[string]any, keys ...string) string {
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
