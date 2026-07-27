package codebuddy

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	agentadaptor "github.com/agent-dance/agent-adaptor"
)

// streamingState maps CodeBuddy stream-json stream_event frames (Anthropic
// Messages API-shaped) into StreamPayload. It is a copy of the Claude
// streaming mapper with the interactive HITL hooks removed.
type streamingState struct {
	sink   agentadaptor.EventSink
	runID  string
	parser *parser

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

func newStreamingState(sink agentadaptor.EventSink, runID string, p *parser) *streamingState {
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
	return agentadaptor.StreamPayload{RunID: s.runID, ThreadID: s.parser.sessionID}
}

func (s *streamingState) emitStream(pl agentadaptor.StreamPayload) {
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
	if pl.Kind == agentadaptor.StreamRunFinished || pl.Kind == agentadaptor.StreamRunError {
		s.finishedEmitted = true
	}
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
	s.emitStream(agentadaptor.StreamPayload{Name: "system.api_retry", Raw: payload})

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
	} else if v, ok := payload["willRetry"].(bool); ok {
		willRetry = v
	}

	if s.apiRetryHits >= 3 || (!willRetry && is5xx) {
		msg := topString(payload, "message", "error", "detail")
		if msg == "" {
			msg = "API retry exhausted"
		}
		s.emitErrorTerminal(&agentadaptor.RunFailure{Message: msg, Code: "api_retry"}, payload)
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
		// no interactive stdin to close in headless mode
	default:
		cp := cloneMapShallow(outer)
		cp["_stream_raw_line"] = rawLine
		s.emitStream(agentadaptor.StreamPayload{Name: strings.ToLower(asString(eventObj["type"])), Raw: cp})
	}
}

func (s *streamingState) handleMessageStart(event map[string]any) {
	s.stopReason = ""
	msg := topObject(event, "message")
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
		id := topString(block, "id")
		name := topString(block, "name")
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
	case "thinking":
		thID := fmt.Sprintf("thinking-%d", idx)
		s.thinkingID[idx] = thID
		pl := s.basePayload()
		pl.Kind = agentadaptor.StreamReasoningStart
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
		raw := exactString(delta, "partial_json")
		tid := s.toolCallID[idx]
		if tid == "" {
			tid = fmt.Sprintf("idx-%d", idx)
		}
		pl := s.basePayload()
		pl.Kind = agentadaptor.StreamToolCallArgs
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
		pl.Kind = agentadaptor.StreamReasoningContent
		pl.MessageID = thID
		pl.Delta = thinking
		s.emitStream(pl)

	case "signature_delta":
		if sig := exactString(delta, "signature"); sig != "" {
			s.signatures[idx] += sig
		}
	default:
		s.emitStream(agentadaptor.StreamPayload{Name: strings.ToLower(asString(delta["type"])), Raw: cloneMapShallow(delta)})
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
	if s.streamUsage == nil {
		s.streamUsage = &agentadaptor.Usage{}
	}
	if v, ok := topInt(u, "input_tokens"); ok && v > s.streamUsage.InputTokens {
		s.streamUsage.InputTokens = v
	}
	if v, ok := topInt(u, "cache_read_input_tokens", "cached_input_tokens"); ok && v > s.streamUsage.CachedInputTokens {
		s.streamUsage.CachedInputTokens = v
	}
	if v, ok := topInt(u, "output_tokens"); ok && v > s.streamUsage.OutputTokens {
		s.streamUsage.OutputTokens = v
	}
}

func (s *streamingState) handleUserToolResult(block map[string]any) {
	id := topString(block, "tool_use_id")
	text := resultText(block["content"])
	isError := false
	if v, ok := block["is_error"].(bool); ok {
		isError = v
	}
	pl := s.basePayload()
	pl.Kind = agentadaptor.StreamToolCallResult
	pl.ToolCallID = id
	pl.Result = map[string]any{"text": text, "is_error": isError, "tool_use_id": id}
	s.emitStream(pl)
}

func (s *streamingState) handleResultTerminal(payload map[string]any) {
	s.markRunStarted()
	if s.finishedEmitted {
		return
	}
	s.closeOpenLifecycles()
	if !s.parser.terminalSuccess {
		msg := s.parser.errorMessage
		if msg == "" {
			msg = "codebuddy terminal result did not report success"
		}
		s.emitErrorTerminal(&agentadaptor.RunFailure{Message: msg, Code: agentadaptor.FailureAgentError}, payload)
		return
	}

	pl := s.basePayload()
	pl.Kind = agentadaptor.StreamRunFinished

	if s.parser.usage != nil {
		u := *s.parser.usage
		pl.Usage = &u
	} else if s.streamUsage != nil {
		u := *s.streamUsage
		pl.Usage = &u
	}

	raw := map[string]any{"stop_reason": s.stopReason}
	if v, ok := topFloat(payload, "total_cost_usd", "cost_usd"); ok {
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
	s.emitErrorTerminal(&agentadaptor.RunFailure{Message: msg, Code: agentadaptor.FailureCode(code)}, payload)
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
}

func (s *streamingState) emitErrorTerminal(failure *agentadaptor.RunFailure, raw map[string]any) {
	if s == nil || s.sink == nil || s.finishedEmitted {
		return
	}
	s.markRunStarted()
	s.closeOpenLifecycles()
	s.emitStream(agentadaptor.StreamPayload{Kind: agentadaptor.StreamRunError, Error: failure, Raw: raw})
}

func (s *streamingState) finalize() {
	if s == nil || s.sink == nil || s.finishedEmitted {
		return
	}
	s.markRunStarted()
	s.closeOpenLifecycles()

	s.emitErrorTerminal(&agentadaptor.RunFailure{
		Code:    agentadaptor.FailureAgentError,
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
