package codebuddy

import (
	"bytes"
	"encoding/json"
	"sort"
	"strings"
	"sync"
	"time"

	agentadaptor "github.com/agent-dance/agent-adaptor"
)

// parser consumes raw stdout/stderr chunks from a CodeBuddy CLI stream-json
// run and produces the normalized outputs required by the adapter contract.
type parser struct {
	mu sync.Mutex

	sink agentadaptor.EventSink

	stdoutLine bytes.Buffer
	stderrLine bytes.Buffer

	transcript    []agentadaptor.TranscriptItem
	assistantText []string

	sessionID         string
	displayID         string
	summary           string
	terminalSummary   string
	usage             *agentadaptor.Usage
	cost              *float64
	resultFinal       map[string]any
	structuredOutput  *agentadaptor.StructuredOutput
	errorMessage      string
	terminalSeen      bool
	terminalSuccess   bool
	protocolMalformed bool

	stream       *streamingState
	deltaBuffers map[string]*strings.Builder
	control      *controlState

	pendingFailure *agentadaptor.RunFailure

	runID string
}

var checkpointEvents = map[string]struct{}{
	"completion":      {},
	"result":          {},
	"session":         {},
	"session.updated": {},
	"turn.completed":  {},
}

func newParser(sink agentadaptor.EventSink) *parser {
	return &parser{sink: sink}
}

func (p *parser) enableStreaming(runID string) {
	if p.sink == nil {
		return
	}
	p.runID = runID
	p.stream = newStreamingState(p.sink, runID, p)
}

func (p *parser) appendTextDelta(messageID, delta string) {
	if messageID == "" || delta == "" {
		return
	}
	if p.deltaBuffers == nil {
		p.deltaBuffers = make(map[string]*strings.Builder)
	}
	buf := p.deltaBuffers[messageID]
	if buf == nil {
		var b strings.Builder
		buf = &b
		p.deltaBuffers[messageID] = buf
	}
	buf.WriteString(delta)
}

func (p *parser) onChunk(stream string, chunk []byte, ts time.Time) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	buf := &p.stdoutLine
	if stream == "stderr" {
		buf = &p.stderrLine
	}
	buf.Write(chunk)
	for {
		idx := bytes.IndexByte(buf.Bytes(), '\n')
		if idx < 0 {
			break
		}
		line := make([]byte, idx)
		copy(line, buf.Bytes()[:idx])
		buf.Next(idx + 1)
		p.processLine(stream, line, ts)
	}
	return nil
}

func (p *parser) finalize() {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.stdoutLine.Len() > 0 {
		remaining := append([]byte(nil), p.stdoutLine.Bytes()...)
		p.stdoutLine.Reset()
		p.processLine("stdout", remaining, time.Now().UTC())
	}
	if p.stderrLine.Len() > 0 {
		remaining := append([]byte(nil), p.stderrLine.Bytes()...)
		p.stderrLine.Reset()
		p.processLine("stderr", remaining, time.Now().UTC())
	}
	if p.stream != nil {
		p.stream.finalize()
	}
}

func (p *parser) processLine(stream string, line []byte, _ time.Time) {
	text := strings.TrimRight(string(line), "\r")
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return
	}

	if stream == "stderr" {
		p.emit(agentadaptor.TranscriptItem{Kind: agentadaptor.TranscriptStderr, Text: text})
		return
	}

	if !strings.HasPrefix(trimmed, "{") {
		p.emit(agentadaptor.TranscriptItem{Kind: agentadaptor.TranscriptStdout, Text: text})
		return
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(trimmed), &payload); err != nil {
		p.protocolMalformed = true
		p.emit(agentadaptor.TranscriptItem{Kind: agentadaptor.TranscriptStdout, Text: text})
		return
	}

	p.handlePayload(trimmed, payload)
}

func (p *parser) handlePayload(raw string, payload map[string]any) {
	if p.terminalSeen {
		p.protocolMalformed = true
		return
	}
	if p.handleControlPayload(payload) {
		return
	}
	eventType := strings.ToLower(topString(payload, "type", "event", "kind"))
	subtype := strings.ToLower(topString(payload, "subtype"))

	if codeBuddyFormalEvent(eventType) {
		p.maybeCaptureSession(payload)
	}

	switch eventType {
	case "system":
		if subtype == "init" {
			model := topString(payload, "model")
			session := topString(payload, "session_id", "sessionId")
			p.emit(agentadaptor.TranscriptItem{Kind: agentadaptor.TranscriptInit, Model: model, SessionID: session})
			if p.stream != nil {
				p.stream.handleSystemInit(payload)
			}
			return
		}
		if p.stream != nil && subtype == "api_retry" {
			p.stream.handleAPIRetry(payload)
		}
		p.emit(agentadaptor.TranscriptItem{Kind: agentadaptor.TranscriptSystem, Text: raw, Subtype: subtype})
	case "stream_event":
		if p.stream != nil {
			p.stream.handleStreamEvent(raw, payload)
		}
		return
	case "assistant":
		p.handleAssistantMessage(topObject(payload, "message"))
	case "user":
		p.handleUserMessage(topObject(payload, "message"))
	case "result":
		p.handleResult(raw, payload, subtype)
	case "error":
		p.terminalSeen = true
		p.terminalSuccess = false
		message := topString(payload, "message", "error")
		if message == "" {
			message = "codebuddy provider error"
		}
		p.errorMessage = message
		if p.stream != nil {
			p.stream.handleErrorTerminal(payload)
		}
		p.emit(agentadaptor.TranscriptItem{Kind: agentadaptor.TranscriptFailure, Text: message, Metadata: map[string]string{"code": "error"}})
	default:
		if eventType != "" {
			if _, ok := checkpointEvents[eventType]; ok {
				p.emit(agentadaptor.TranscriptItem{Kind: agentadaptor.TranscriptResult, Subtype: eventType, Data: map[string]any{"payload": payload}})
				return
			}
		}
		if p.stream != nil {
			op := cloneMap(payload)
			op["_codebuddy_wrapper_type"] = eventType
			_ = p.sink.EmitStream(agentadaptor.StreamPayload{RunID: p.stream.runID, ThreadID: p.sessionID, Name: eventType, Raw: op})
		}
		p.emit(agentadaptor.TranscriptItem{Kind: agentadaptor.TranscriptSystem, Text: raw, Data: map[string]any{"payload": payload}})
	}
}

func cloneMap(payload map[string]any) map[string]any {
	if payload == nil {
		return map[string]any{}
	}
	out := make(map[string]any, len(payload))
	for k, v := range payload {
		out[k] = v
	}
	return out
}

func (p *parser) maybeCaptureSession(payload map[string]any) {
	session := topString(payload, "session_id", "sessionId", "sessionID")
	if session == "" {
		return
	}
	p.sessionID = session
	if display := topString(payload, "display_id", "displayId"); display != "" {
		p.displayID = display
	} else if p.displayID == "" {
		p.displayID = session
	}
}

func (p *parser) handleAssistantMessage(message map[string]any) {
	if len(message) == 0 {
		return
	}
	model := topString(message, "model")
	content, ok := message["content"].([]any)
	if !ok {
		return
	}
	for _, raw := range content {
		block, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		switch strings.ToLower(topString(block, "type")) {
		case "text":
			text := topString(block, "text")
			if text == "" {
				continue
			}
			p.assistantText = append(p.assistantText, text)
			p.summary = text
			p.emit(agentadaptor.TranscriptItem{Kind: agentadaptor.TranscriptAssistant, Text: text, Model: model})
		case "thinking":
			text := topString(block, "text", "thinking")
			if text == "" {
				continue
			}
			p.emit(agentadaptor.TranscriptItem{Kind: agentadaptor.TranscriptThinking, Text: text, Model: model})
		case "tool_use":
			toolName := topString(block, "name")
			input, _ := block["input"].(map[string]any)
			p.captureControlPlan(toolName, input)
			p.emit(agentadaptor.TranscriptItem{
				Kind:      agentadaptor.TranscriptToolCall,
				ToolName:  toolName,
				ToolUseID: topString(block, "id", "tool_use_id"),
				Input:     block["input"],
			})
		}
	}
}

func (p *parser) handleUserMessage(message map[string]any) {
	content, ok := message["content"].([]any)
	if !ok {
		return
	}
	for _, raw := range content {
		block, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if strings.ToLower(topString(block, "type")) == "tool_result" {
			id := topString(block, "tool_use_id")
			text := resultText(block["content"])
			isError := false
			if v, ok := block["is_error"].(bool); ok {
				isError = v
			}
			if p.stream != nil {
				p.stream.handleUserToolResult(block)
			}
			p.emit(agentadaptor.TranscriptItem{Kind: agentadaptor.TranscriptToolResult, ToolUseID: id, Text: text, IsError: isError})
			continue
		}
		if text := topString(block, "text"); text != "" {
			p.emit(agentadaptor.TranscriptItem{Kind: agentadaptor.TranscriptUser, Text: text})
		}
	}
}

func resultText(raw any) string {
	switch value := raw.(type) {
	case string:
		return value
	case []any:
		parts := make([]string, 0, len(value))
		for _, entry := range value {
			block, ok := entry.(map[string]any)
			if !ok {
				continue
			}
			if text := topString(block, "text"); text != "" {
				parts = append(parts, text)
			}
		}
		return strings.Join(parts, "\n")
	}
	return ""
}

func (p *parser) handleResult(raw string, payload map[string]any, subtype string) {
	p.terminalSeen = true
	isErrorFlag, _ := payload["is_error"].(bool)
	p.terminalSuccess = subtype == "success" && !isErrorFlag

	if p.control != nil {
		// Control sessions retain stdin for host responses. Once a terminal
		// result arrives, closing it lets the CLI terminate cleanly.
		_ = p.control.stdin.Close()
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(raw), &decoded); err == nil {
		p.resultFinal = decoded
	}

	if resultText := topString(payload, "result", "summary"); resultText != "" {
		p.terminalSummary = resultText
	}
	if rawJSON, ok := structuredJSONFromResult(payload); ok {
		p.structuredOutput = &agentadaptor.StructuredOutput{
			Format:  agentadaptor.OutputFormatJSONSchema,
			Source:  agentadaptor.StructuredOutputSourceNative,
			RawJSON: rawJSON,
			Valid:   true,
		}
	}

	if usage := topObject(payload, "usage"); usage != nil {
		input, okInput := topInt(usage, "input_tokens")
		cached, okCached := topInt(usage, "cache_read_input_tokens", "cached_input_tokens")
		output, okOutput := topInt(usage, "output_tokens")
		if okInput || okCached || okOutput {
			if p.usage == nil {
				p.usage = &agentadaptor.Usage{}
			}
			if okInput {
				p.usage.InputTokens = input
			}
			if okCached {
				p.usage.CachedInputTokens = cached
			}
			if okOutput {
				p.usage.OutputTokens = output
			}
		}
	}
	if cost, ok := topFloat(payload, "total_cost_usd", "cost_usd", "costUSD"); ok {
		c := cost
		p.cost = &c
	}

	isError := !p.terminalSuccess
	if isError {
		if message := topString(payload, "message", "result"); message != "" {
			p.errorMessage = message
		} else if p.errorMessage == "" {
			p.errorMessage = "codebuddy terminal result did not report success"
		}
	}

	p.emit(agentadaptor.TranscriptItem{
		Kind:    agentadaptor.TranscriptResult,
		Subtype: subtype,
		IsError: isError,
		Usage:   p.usage,
		CostUSD: p.cost,
		Text:    topString(payload, "result"),
		Data:    map[string]any{"payload": payload},
	})
	if p.stream != nil {
		p.stream.handleResultTerminal(payload)
	}
}

func codeBuddyFormalEvent(eventType string) bool {
	switch eventType {
	case "system", "stream_event", "assistant", "user", "result", "error":
		return true
	default:
		return false
	}
}

func structuredJSONFromResult(payload map[string]any) (json.RawMessage, bool) {
	for _, key := range []string{"structured_output", "structuredOutput"} {
		raw, ok := payload[key]
		if !ok {
			continue
		}
		if msg, ok := jsonRawMessageFromValue(raw); ok {
			return msg, true
		}
	}
	return nil, false
}

func jsonRawMessageFromValue(raw any) (json.RawMessage, bool) {
	switch value := raw.(type) {
	case nil:
		return nil, false
	case string:
		var decoded any
		if err := json.Unmarshal([]byte(value), &decoded); err != nil {
			return nil, false
		}
		msg, err := json.Marshal(decoded)
		if err != nil {
			return nil, false
		}
		return msg, true
	default:
		msg, err := json.Marshal(value)
		if err != nil {
			return nil, false
		}
		var decoded any
		if err := json.Unmarshal(msg, &decoded); err != nil {
			return nil, false
		}
		return msg, true
	}
}

func (p *parser) emit(item agentadaptor.TranscriptItem) {
	p.transcript = append(p.transcript, item)
	if p.sink == nil {
		return
	}
	clone := item
	_ = p.sink.Emit(agentadaptor.RunEvent{Type: agentadaptor.RunEventItem, Timestamp: time.Now().UTC(), Item: &clone})
}

func (p *parser) finalSummary() string {
	if strings.TrimSpace(p.terminalSummary) != "" {
		return p.terminalSummary
	}
	return p.summary
}

func (p *parser) buildOutput() string {
	nonEmpty := make([]string, 0, len(p.assistantText))
	for _, text := range p.assistantText {
		if strings.TrimSpace(text) != "" {
			nonEmpty = append(nonEmpty, text)
		}
	}
	if len(nonEmpty) > 0 {
		return strings.Join(nonEmpty, "\n\n")
	}
	if len(p.deltaBuffers) == 0 {
		return ""
	}
	keys := make([]string, 0, len(p.deltaBuffers))
	for id := range p.deltaBuffers {
		keys = append(keys, id)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, id := range keys {
		if b := p.deltaBuffers[id]; b != nil {
			if s := strings.TrimSpace(b.String()); s != "" {
				parts = append(parts, s)
			}
		}
	}
	return strings.Join(parts, "\n\n")
}

func (p *parser) outputMetadata() map[string]string {
	out := make([]string, 0, len(p.assistantText))
	for _, text := range p.assistantText {
		if strings.TrimSpace(text) != "" {
			out = append(out, text)
		}
	}
	if len(out) > 0 || len(p.deltaBuffers) == 0 {
		return nil
	}
	hasDelta := false
	for _, b := range p.deltaBuffers {
		if b != nil && strings.TrimSpace(b.String()) != "" {
			hasDelta = true
			break
		}
	}
	if !hasDelta {
		return nil
	}
	return map[string]string{"output_source": "reconstructed_from_deltas"}
}

func (p *parser) checkpoint(exitCode int) *agentadaptor.DriverCheckpoint {
	if exitCode != 0 || p.protocolMalformed || !p.terminalSeen || !p.terminalSuccess || p.sessionID == "" {
		return nil
	}
	display := p.displayID
	if display == "" {
		display = p.sessionID
	}
	return &agentadaptor.DriverCheckpoint{
		State: &agentadaptor.DriverSessionState{ResumeID: p.sessionID, DisplayID: display},
		Valid: true,
	}
}

func (p *parser) checkpointForOutcome(exitCode int, signal string, timedOut bool, failure *agentadaptor.RunFailure) *agentadaptor.DriverCheckpoint {
	if signal != "" || timedOut || failure != nil {
		return nil
	}
	return p.checkpoint(exitCode)
}

func topString(payload map[string]any, keys ...string) string {
	for _, key := range keys {
		raw, ok := payload[key]
		if !ok {
			continue
		}
		if value, ok := raw.(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func topObject(payload map[string]any, key string) map[string]any {
	raw, ok := payload[key]
	if !ok {
		return nil
	}
	value, _ := raw.(map[string]any)
	return value
}

func topInt(payload map[string]any, keys ...string) (int, bool) {
	for _, key := range keys {
		raw, ok := payload[key]
		if !ok {
			continue
		}
		switch value := raw.(type) {
		case float64:
			return int(value), true
		case int:
			return value, true
		case int64:
			return int(value), true
		}
	}
	return 0, false
}

func topFloat(payload map[string]any, keys ...string) (float64, bool) {
	for _, key := range keys {
		raw, ok := payload[key]
		if !ok {
			continue
		}
		switch value := raw.(type) {
		case float64:
			return value, true
		case int:
			return float64(value), true
		case int64:
			return float64(value), true
		}
	}
	return 0, false
}
