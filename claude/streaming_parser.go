package claude

import (
	"encoding/json"
	"fmt"
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
	textStarted map[int]bool
	blockKind   map[int]string
	toolCallID  map[int]string
	toolName    map[int]string
	thinkingID  map[int]string
	signatures  map[int]string

	runStarted      bool
	finishedEmitted bool
	streamUsage     *driver.Usage
	stopReason      string
	terminalPayload map[string]any
}

func newStreamingState(sink driver.EventSink, runID string, p *claudeParser) *streamingState {
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
		s.emitStream(driver.StreamPayload{Name: evType, Raw: cp})
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
		pl.Kind = driver.StreamToolCallStart
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
			pl.Kind = driver.StreamTextStart
			pl.MessageID = s.messageID
			s.emitStream(pl)
		}
		pl := s.basePayload()
		pl.Kind = driver.StreamTextContent
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
		pl.Kind = driver.StreamToolCallArgs
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
		tid := s.toolCallID[idx]
		if tid != "" {
			pl := s.basePayload()
			pl.Kind = driver.StreamToolCallEnd
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
		s.streamUsage = &driver.Usage{}
	}
	if v, ok := claudeTopLevelInt(u, "input_tokens"); ok {
		if v > s.streamUsage.InputTokens {
			s.streamUsage.InputTokens = v
		}
	}
	if v, ok := claudeTopLevelInt(u, "cache_read_input_tokens"); ok {
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
	pl.Kind = driver.StreamToolCallResult
	pl.ToolCallID = id
	pl.Result = map[string]any{
		"text":        text,
		"is_error":    isError,
		"tool_use_id": id,
	}
	s.emitStream(pl)
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
				pl.Kind = driver.StreamTextEnd
				pl.MessageID = s.messageID
				s.emitStream(pl)
			}
		case "tool_use":
			if tid := s.toolCallID[idx]; tid != "" {
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
	s.toolCallID = nil
	s.toolName = nil
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
