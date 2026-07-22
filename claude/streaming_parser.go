package claude

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	agentadaptor "github.com/agent-dance/agent-adaptor"
)

// streamingState maps Claude stream-json stream_event frames (Anthropic Messages
// API-shaped) into StreamPayload. It does not participate in checkpoint
// construction; session capture stays in the main parser.
type streamingState struct {
	sink        agentadaptor.EventSink
	runID       string
	parser      *claudeParser
	messageID   string
	textStarted map[int]bool
	blockKind   map[int]string
	toolCallID  map[int]string
	toolName    map[int]string
	thinkingID  map[int]string
	signatures  map[int]string

	runStarted      bool
	finishedEmitted bool
	apiRetryHits    int
	lastRetryWas5xx bool
	streamUsage     *agentadaptor.Usage
	stopReason      string
	numTurns        int
}

func newStreamingState(sink agentadaptor.EventSink, runID string, p *claudeParser) *streamingState {
	return &streamingState{
		sink:        sink,
		runID:       runID,
		parser:      p,
		textStarted: make(map[int]bool),
		blockKind:   make(map[int]string),
		toolCallID:  make(map[int]string),
		toolName:    make(map[int]string),
		thinkingID:  make(map[int]string),
		signatures:  make(map[int]string),
	}
}

func (s *streamingState) basePayload() agentadaptor.StreamPayload {
	p := agentadaptor.StreamPayload{
		RunID:    s.runID,
		ThreadID: s.parser.sessionID,
	}
	return p
}

func (s *streamingState) emitStream(pl agentadaptor.StreamPayload) {
	if s.sink == nil {
		return
	}
	if pl.RunID == "" {
		pl.RunID = s.runID
	}
	if pl.ThreadID == "" {
		pl.ThreadID = s.parser.sessionID
	}
	_ = s.sink.EmitStream(pl)
}

func (s *streamingState) markRunStarted() {
	if s.runStarted {
		return
	}
	s.runStarted = true
	pl := s.basePayload()
	pl.Kind = agentadaptor.StreamRunStarted
	pl.ThreadID = s.parser.sessionID
	s.emitStream(pl)
}

func (s *streamingState) handleSystemInit(_ map[string]any) {
	s.markRunStarted()
}

func (s *streamingState) handleAPIRetry(payload map[string]any) {
	s.emitStream(agentadaptor.StreamPayload{
		Name: "system.api_retry",
		Raw:  payload,
	})

	status, ok := claudeHTTPStatusFromPayload(payload)
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
	} else if v, ok := payload["willRetry"].(bool); ok {
		willRetry = v
	}

	if s.apiRetryHits >= 3 || (!willRetry && is5xx) {
		msg := claudeTopLevelString(payload, "message", "error", "detail")
		if msg == "" {
			msg = "API retry exhausted"
		}
		if s.parser != nil {
			s.parser.flushOpenSubagents()
		}
		s.emitStream(agentadaptor.StreamPayload{
			Kind: agentadaptor.StreamRunError,
			Error: &agentadaptor.RunFailure{
				Message: msg,
				Code:    "api_retry",
			},
			Raw: payload,
		})
	}
}

func claudeHTTPStatusFromPayload(payload map[string]any) (int, bool) {
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
	eventAny, ok := outer["event"]
	if !ok {
		s.emitStream(agentadaptor.StreamPayload{Name: "stream_event", Raw: outer})
		return
	}
	eventObj, ok := eventAny.(map[string]any)
	if !ok {
		s.emitStream(agentadaptor.StreamPayload{Name: "stream_event", Raw: outer})
		return
	}

	evType := strings.ToLower(asString(eventObj["type"]))
	switch evType {
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
		// See onAssistantMessageStop: close interactive stdin after a
		// terminal model turn so the CLI can exit and unblock the host.
		if s.parser != nil {
			s.parser.onAssistantMessageStop(s.stopReason)
		}
	default:
		cp := cloneMapShallow(outer)
		cp["_stream_raw_line"] = rawLine
		s.emitStream(agentadaptor.StreamPayload{Name: evType, Raw: cp})
	}
}

func (s *streamingState) handleMessageStart(event map[string]any) {
	s.stopReason = ""
	msg := claudeTopLevelObject(event, "message")
	id := claudeTopLevelString(msg, "id")
	if id != "" {
		s.messageID = id
	}
	if msg != nil {
		if usage := claudeTopLevelObject(msg, "usage"); usage != nil {
			s.mergeUsageMap(usage)
		}
	}
}

func (s *streamingState) handleContentBlockStart(event map[string]any) {
	idx := intFromAny(event["index"])
	block := claudeTopLevelObject(event, "content_block")
	if block == nil {
		return
	}
	bt := strings.ToLower(claudeTopLevelString(block, "type"))
	s.blockKind[idx] = bt

	switch bt {
	case "tool_use":
		id := claudeTopLevelString(block, "id")
		name := claudeTopLevelString(block, "name")
		s.toolCallID[idx] = id
		s.toolName[idx] = name
		pl := s.basePayload()
		pl.Kind = agentadaptor.StreamToolCallStart
		pl.ToolCallID = id
		pl.Name = name
		if input := block["input"]; input != nil {
			pl.Args = map[string]any{"input": input}
		}
		s.emitStream(pl)
		// Interactive mode: register this tool_use with the parent parser
		// so handleContentBlockDelta can accumulate input_json_delta and
		// handleContentBlockStop can drive the HITL flow.
		if s.parser != nil {
			s.parser.interactiveOnToolUseStart(idx, name, id)
		}
	case "thinking":
		thID := fmt.Sprintf("thinking-%d", idx)
		s.thinkingID[idx] = thID
		pl := s.basePayload()
		pl.Kind = agentadaptor.StreamReasoningStart
		pl.MessageID = thID
		s.emitStream(pl)
	case "text":
		// Deferred StreamTextStart until first text_delta.
	default:
		// Unknown block: already in blockKind; stop handler may emit opaque ends.
	}
}

func (s *streamingState) handleContentBlockDelta(event map[string]any) {
	idx := intFromAny(event["index"])
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
		if !s.textStarted[idx] {
			s.textStarted[idx] = true
			pl := s.basePayload()
			pl.Kind = agentadaptor.StreamTextStart
			pl.MessageID = s.messageID
			s.emitStream(pl)
		}
		s.parser.appendTextDelta(s.messageID, text)
		pl := s.basePayload()
		pl.Kind = agentadaptor.StreamTextContent
		pl.MessageID = s.messageID
		pl.Delta = text
		s.emitStream(pl)

	case "input_json_delta":
		raw := claudeExactString(delta, "partial_json")
		tid := s.toolCallID[idx]
		if tid == "" {
			tid = fmt.Sprintf("idx-%d", idx)
		}
		pl := s.basePayload()
		pl.Kind = agentadaptor.StreamToolCallArgs
		pl.ToolCallID = tid
		pl.Delta = raw
		s.emitStream(pl)
		// Interactive mode: feed the partial_json into the accumulator so
		// we have the complete tool_use input once content_block_stop hits.
		if s.parser != nil {
			s.parser.interactiveOnToolUseDelta(idx, raw)
		}

	case "thinking_delta":
		thinking := claudeExactString(delta, "thinking")
		if thinking == "" {
			return
		}
		thID := s.thinkingID[idx]
		if thID == "" {
			thID = fmt.Sprintf("thinking-%d", idx)
			s.thinkingID[idx] = thID
		}
		pl := s.basePayload()
		pl.Kind = agentadaptor.StreamReasoningContent
		pl.MessageID = thID
		pl.Delta = thinking
		s.emitStream(pl)

	case "signature_delta":
		sig := claudeExactString(delta, "signature")
		if sig != "" {
			s.signatures[idx] += sig
		}
	default:
		s.emitStream(agentadaptor.StreamPayload{Name: dt, Raw: cloneMapShallow(delta)})
	}
}

func (s *streamingState) handleContentBlockStop(event map[string]any) {
	idx := intFromAny(event["index"])
	bt := strings.ToLower(s.blockKind[idx])

	switch bt {
	case "text":
		if s.textStarted[idx] && s.messageID != "" {
			pl := s.basePayload()
			pl.Kind = agentadaptor.StreamTextEnd
			pl.MessageID = s.messageID
			s.emitStream(pl)
		}
	case "tool_use":
		tid := s.toolCallID[idx]
		if tid != "" {
			pl := s.basePayload()
			pl.Kind = agentadaptor.StreamToolCallEnd
			pl.ToolCallID = tid
			s.emitStream(pl)
		}
		// Interactive mode: tool_use input is now complete. Hand off to
		// the parser so it can trigger RequestDecision + inject a user
		// tool_result via stdin.
		if s.parser != nil {
			s.parser.interactiveOnToolUseStop(idx)
		}
	case "thinking":
		thID := s.thinkingID[idx]
		if thID != "" {
			pl := s.basePayload()
			pl.Kind = agentadaptor.StreamReasoningEnd
			pl.MessageID = thID
			s.emitStream(pl)
		}
	default:
		if bt != "" {
			s.emitStream(agentadaptor.StreamPayload{Name: "content_block_stop", Raw: map[string]any{"index": idx, "kind": bt}})
		}
	}

	delete(s.blockKind, idx)
	delete(s.textStarted, idx)
	delete(s.toolCallID, idx)
	delete(s.toolName, idx)
	delete(s.thinkingID, idx)
	delete(s.signatures, idx)
}

func (s *streamingState) handleMessageDelta(event map[string]any) {
	if d, ok := event["delta"].(map[string]any); ok {
		if sr := claudeTopLevelString(d, "stop_reason"); sr != "" {
			s.stopReason = sr
		}
	}
	if u := claudeTopLevelObject(event, "usage"); u != nil {
		s.mergeUsageMap(u)
	}
	if md, ok := event["delta"].(map[string]any); ok {
		if u := claudeTopLevelObject(md, "usage"); u != nil {
			s.mergeUsageMap(u)
		}
	}
}

func (s *streamingState) mergeUsageMap(u map[string]any) {
	if s.streamUsage == nil {
		s.streamUsage = &agentadaptor.Usage{}
	}
	if v, ok := claudeTopLevelInt(u, "input_tokens"); ok {
		if v > s.streamUsage.InputTokens {
			s.streamUsage.InputTokens = v
		}
	}
	if v, ok := claudeTopLevelInt(u, "cache_read_input_tokens", "cached_input_tokens"); ok {
		if v > s.streamUsage.CachedInputTokens {
			s.streamUsage.CachedInputTokens = v
		}
	}
	if v, ok := claudeTopLevelInt(u, "output_tokens"); ok {
		// message_delta.usage.output_tokens is cumulative along the message.
		if v > s.streamUsage.OutputTokens {
			s.streamUsage.OutputTokens = v
		}
	}
}

func (s *streamingState) handleUserToolResult(block map[string]any) {
	id := claudeTopLevelString(block, "tool_use_id")
	text := claudeResultText(block["content"])
	isError := false
	if v, ok := block["is_error"].(bool); ok {
		isError = v
	}
	pl := s.basePayload()
	pl.Kind = agentadaptor.StreamToolCallResult
	pl.ToolCallID = id
	pl.Result = map[string]any{
		"text":        text,
		"is_error":    isError,
		"tool_use_id": id,
	}
	s.emitStream(pl)
}

func (s *streamingState) handleResultTerminal(payload map[string]any) {
	// Pair finish with a start when the CLI omits system.init (should be rare).
	s.markRunStarted()
	if s.finishedEmitted {
		return
	}
	// A terminal parent result can arrive while a child scope is still open
	// (for example when task_notification was omitted). Close every such scope
	// before publishing run.finished.
	if s.parser != nil {
		s.parser.flushOpenSubagents()
	}
	s.finishedEmitted = true

	pl := s.basePayload()
	pl.Kind = agentadaptor.StreamRunFinished

	if s.parser.usage != nil {
		u := *s.parser.usage
		pl.Usage = &u
	} else if s.streamUsage != nil {
		u := *s.streamUsage
		pl.Usage = &u
	}

	raw := map[string]any{
		"stop_reason": s.stopReason,
	}
	if v, ok := claudeTopLevelFloat(payload, "total_cost_usd", "cost_usd"); ok {
		raw["total_cost_usd"] = v
	}
	if subtype := claudeTopLevelString(payload, "subtype"); subtype != "" {
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
	if s.finishedEmitted {
		return
	}
	// Preserve the lifecycle invariant that no child event follows a terminal
	// parent event, including provider error terminals.
	if s.parser != nil {
		s.parser.flushOpenSubagents()
	}
	msg := claudeTopLevelString(payload, "message", "error")
	code := claudeTopLevelString(payload, "code")
	if msg == "" {
		msg = "claude stream error"
	}
	s.emitStream(agentadaptor.StreamPayload{
		Kind: agentadaptor.StreamRunError,
		Error: &agentadaptor.RunFailure{
			Message: msg,
			Code:    agentadaptor.FailureCode(code),
		},
		Raw: payload,
	})
	s.finishedEmitted = true
}

func (s *streamingState) finalize() {
	if s == nil || s.sink == nil {
		return
	}
	// Close any blocks still marked open (truncated stream).
	pending := make([]int, 0, len(s.blockKind))
	for idx := range s.blockKind {
		pending = append(pending, idx)
	}
	for _, idx := range pending {
		bt := strings.ToLower(s.blockKind[idx])

		switch bt {
		case "text":
			if s.textStarted[idx] && s.messageID != "" {
				pl := s.basePayload()
				pl.Kind = agentadaptor.StreamTextEnd
				pl.MessageID = s.messageID
				s.emitStream(pl)
			}
		case "tool_use":
			if tid := s.toolCallID[idx]; tid != "" {
				pl := s.basePayload()
				pl.Kind = agentadaptor.StreamToolCallEnd
				pl.ToolCallID = tid
				s.emitStream(pl)
			}
		case "thinking":
			if thID := s.thinkingID[idx]; thID != "" {
				pl := s.basePayload()
				pl.Kind = agentadaptor.StreamReasoningEnd
				pl.MessageID = thID
				s.emitStream(pl)
			}
		}
	}
	s.blockKind = nil
	s.textStarted = nil
	s.toolCallID = nil
	s.toolName = nil
	s.thinkingID = nil
	s.signatures = nil

	if !s.finishedEmitted && s.runStarted {
		pl := s.basePayload()
		pl.Kind = agentadaptor.StreamRunFinished
		if s.parser.usage != nil {
			u := *s.parser.usage
			pl.Usage = &u
		} else if s.streamUsage != nil {
			u := *s.streamUsage
			pl.Usage = &u
		}
		pl.Raw = map[string]any{
			"stop_reason": s.stopReason,
			"ephemeral":   true,
		}
		s.emitStream(pl)
		s.finishedEmitted = true
	}
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
