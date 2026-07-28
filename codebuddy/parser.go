package codebuddy

import (
	"bytes"
	"encoding/json"
	"strings"
	"sync"
	"time"

	"github.com/agent-dance/agent-adaptor/driver"
)

// parser consumes raw stdout/stderr chunks from a CodeBuddy CLI stream-json
// run and produces the normalized outputs required by the adapter contract.
type parser struct {
	mu sync.Mutex

	sink driver.EventSink

	stdoutLine bytes.Buffer
	stderrLine bytes.Buffer

	transcript        []driver.TranscriptItem
	assistantText     []string
	terminalResult    string
	terminalResultSet bool
	terminalSessionID string

	sessionID         string
	usage             *driver.Usage
	cost              *float64
	terminal          *driver.TerminalPayload
	structuredOutput  *driver.StructuredOutput
	errorMessage      string
	terminalSeen      bool
	terminalSuccess   bool
	protocolMalformed bool

	stream       *streamingState
	deltaBuffers map[string]*strings.Builder
	deltaOrder   []string
	control      *controlState

	pendingFailure *driver.RunFailure

	runID string
}

func newParser(sink driver.EventSink) *parser {
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
		p.deltaOrder = append(p.deltaOrder, messageID)
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
}

func (p *parser) completeStream(failure *driver.RunFailure, exitCode int, signal string, timedOut bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.stream != nil {
		p.stream.complete(failure, exitCode, signal, timedOut)
	}
}

func (p *parser) processLine(stream string, line []byte, _ time.Time) {
	text := strings.TrimRight(string(line), "\r")
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return
	}

	if stream == "stderr" {
		p.emit(driver.TranscriptItem{Kind: driver.TranscriptStderr, Text: text})
		return
	}

	if !strings.HasPrefix(trimmed, "{") {
		p.emit(driver.TranscriptItem{Kind: driver.TranscriptStdout, Text: text})
		return
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(trimmed), &payload); err != nil {
		p.protocolMalformed = true
		p.emit(driver.TranscriptItem{Kind: driver.TranscriptStdout, Text: text})
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
	eventType := strings.ToLower(topString(payload, "type"))
	subtype := strings.ToLower(topString(payload, "subtype"))

	if codeBuddyFormalEvent(eventType) {
		p.maybeCaptureSession(payload)
	}

	switch eventType {
	case "system":
		if subtype == "init" {
			model := topString(payload, "model")
			session := topString(payload, "session_id")
			p.emit(driver.TranscriptItem{Kind: driver.TranscriptInit, Model: model, SessionID: session})
			if p.stream != nil {
				p.stream.handleSystemInit(payload)
			}
			return
		}
		if p.stream != nil && subtype == "api_retry" {
			p.stream.handleAPIRetry(payload)
		}
		p.emit(driver.TranscriptItem{Kind: driver.TranscriptSystem, Text: raw, Subtype: subtype})
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
		p.terminal = &driver.TerminalPayload{Event: eventType, JSON: append(json.RawMessage(nil), raw...)}
		message := topString(payload, "message", "error")
		if message == "" {
			message = "codebuddy provider error"
		}
		p.errorMessage = message
		if p.stream != nil {
			p.stream.handleErrorTerminal(payload)
		}
		p.emit(driver.TranscriptItem{Kind: driver.TranscriptFailure, Text: message, Metadata: map[string]string{"code": "error"}})
	default:
		if p.stream != nil {
			op := cloneMap(payload)
			op["_codebuddy_wrapper_type"] = eventType
			_ = p.sink.EmitStream(driver.StreamPayload{RunID: p.stream.runID, ThreadID: p.sessionID, Name: eventType, Raw: op})
		}
		p.emit(driver.TranscriptItem{Kind: driver.TranscriptSystem, Text: raw, Data: map[string]any{"payload": payload}})
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
	session := topString(payload, "session_id")
	if session == "" {
		return
	}
	p.sessionID = session
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
			p.emit(driver.TranscriptItem{Kind: driver.TranscriptAssistant, Text: text, Model: model})
		case "thinking":
			text := topString(block, "thinking")
			if text == "" {
				continue
			}
			p.emit(driver.TranscriptItem{Kind: driver.TranscriptThinking, Text: text, Model: model})
		case "tool_use":
			toolName := topString(block, "name")
			input, _ := block["input"].(map[string]any)
			p.captureControlPlan(toolName, input)
			p.emit(driver.TranscriptItem{
				Kind:      driver.TranscriptToolCall,
				ToolName:  toolName,
				ToolUseID: topString(block, "id"),
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
			p.emit(driver.TranscriptItem{Kind: driver.TranscriptToolResult, ToolUseID: id, Text: text, IsError: isError})
			continue
		}
		if text := topString(block, "text"); text != "" {
			p.emit(driver.TranscriptItem{Kind: driver.TranscriptUser, Text: text})
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
	isErrorFlag, hasErrorFlag := payload["is_error"].(bool)
	result, hasResult := payload["result"].(string)
	p.terminalSessionID = topString(payload, "session_id")
	if p.terminalSessionID == "" || !hasErrorFlag || (subtype == "success" && !hasResult) {
		p.protocolMalformed = true
	}
	p.terminalSuccess = subtype == "success" && hasErrorFlag && !isErrorFlag && hasResult && p.terminalSessionID != ""
	if subtype == "success" && hasResult {
		p.terminalResult = result
		p.terminalResultSet = true
	}

	if p.control != nil {
		// Control sessions retain stdin for host responses. Once a terminal
		// result arrives, closing it lets the CLI terminate cleanly.
		_ = p.control.stdin.Close()
	}
	p.terminal = &driver.TerminalPayload{
		Event: "result",
		JSON:  append(json.RawMessage(nil), raw...),
	}

	if rawJSON, ok := structuredJSONFromResult(payload); ok {
		p.structuredOutput = &driver.StructuredOutput{
			Format:  driver.OutputFormatJSONSchema,
			Source:  driver.StructuredOutputSourceNative,
			RawJSON: rawJSON,
		}
	}

	if usage := topObject(payload, "usage"); usage != nil {
		input, okInput := topInt(usage, "input_tokens")
		cached, okCached := topInt(usage, "cache_read_input_tokens")
		output, okOutput := topInt(usage, "output_tokens")
		if okInput || okCached || okOutput {
			if p.usage == nil {
				p.usage = &driver.Usage{}
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
	if cost, ok := topFloat(payload, "total_cost_usd"); ok {
		c := cost
		p.cost = &c
	}

	isError := !p.terminalSuccess
	if isError {
		if p.terminalSessionID == "" && subtype == "success" {
			p.errorMessage = "codebuddy successful terminal result is missing required session_id"
		} else if !hasErrorFlag && subtype == "success" {
			p.errorMessage = "codebuddy successful terminal result is missing required is_error"
		} else if !hasResult && subtype == "success" {
			p.errorMessage = "codebuddy successful terminal result is missing required result"
		} else if message := codeBuddyResultErrorMessage(payload); message != "" {
			p.errorMessage = message
		} else if p.errorMessage == "" {
			p.errorMessage = "codebuddy terminal result did not report success"
		}
	}

	transcriptText := p.terminalResult
	if isError {
		transcriptText = p.errorMessage
	}
	p.emit(driver.TranscriptItem{
		Kind:    driver.TranscriptResult,
		Subtype: subtype,
		IsError: isError,
		Usage:   p.usage,
		CostUSD: p.cost,
		Text:    transcriptText,
		Data:    map[string]any{"payload": payload},
	})
	if p.stream != nil {
		p.stream.handleResultTerminal(payload)
	}
}

func codeBuddyResultErrorMessage(payload map[string]any) string {
	raw, ok := payload["errors"].([]any)
	if !ok {
		return ""
	}
	messages := make([]string, 0, len(raw))
	for _, value := range raw {
		message, ok := value.(string)
		if ok && strings.TrimSpace(message) != "" {
			messages = append(messages, strings.TrimSpace(message))
		}
	}
	return strings.Join(messages, "\n")
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
	raw, ok := payload["structured_output"]
	if !ok {
		return nil, false
	}
	if msg, ok := jsonRawMessageFromValue(raw); ok {
		return msg, true
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

func (p *parser) emit(item driver.TranscriptItem) {
	p.transcript = append(p.transcript, item)
	if p.sink == nil {
		return
	}
	clone := item
	_ = p.sink.Emit(driver.RunEvent{Type: driver.RunEventItem, Timestamp: time.Now().UTC(), Item: &clone})
}

func (p *parser) finalSummary() string {
	// CodeBuddy documents result as the final text response. It does not
	// publish a distinct bounded run-summary field, so keep Summary empty and
	// preserve the exact terminal object in RawStreams.Terminal.
	return ""
}

func (p *parser) buildOutput() string {
	// CodeBuddy's official ResultMessage.result is the final assistant-facing
	// response. It is authoritative when present; streamed assistant messages
	// are intermediate protocol frames and must never be concatenated into a
	// synthetic final answer.
	if p.terminalResultSet {
		return p.terminalResult
	}
	for i := len(p.assistantText) - 1; i >= 0; i-- {
		if strings.TrimSpace(p.assistantText[i]) != "" {
			return p.assistantText[i]
		}
	}
	for i := len(p.deltaOrder) - 1; i >= 0; i-- {
		id := p.deltaOrder[i]
		if b := p.deltaBuffers[id]; b != nil {
			if strings.TrimSpace(b.String()) != "" {
				return b.String()
			}
		}
	}
	return ""
}

func (p *parser) outputMetadata() map[string]string {
	if strings.TrimSpace(p.terminalResult) != "" || len(p.assistantText) != 0 {
		return nil
	}
	for _, id := range p.deltaOrder {
		b := p.deltaBuffers[id]
		if b != nil && strings.TrimSpace(b.String()) != "" {
			return map[string]string{"output_source": "reconstructed_from_deltas"}
		}
	}
	return nil
}

// failureForOutcome classifies provider-protocol failures without stealing
// bare non-zero/signal/timeout process outcomes from the common invocation
// classifier. A zero exit is successful only after one clean official
// ResultMessage with subtype=success, is_error=false, and its own session_id.
func (p *parser) failureForOutcome(exitCode int) *driver.RunFailure {
	if p.pendingFailure != nil {
		return p.pendingFailure
	}
	if strings.TrimSpace(p.errorMessage) != "" {
		return &driver.RunFailure{Code: driver.FailureAgentError, Message: p.errorMessage}
	}
	if exitCode != 0 {
		return nil
	}
	if p.protocolMalformed {
		return &driver.RunFailure{Code: driver.FailureAgentError, Message: "codebuddy protocol was malformed"}
	}
	if !p.terminalSeen {
		return &driver.RunFailure{Code: driver.FailureAgentError, Message: "codebuddy protocol ended without a terminal result"}
	}
	if !p.terminalSuccess {
		return &driver.RunFailure{Code: driver.FailureAgentError, Message: "codebuddy terminal result did not report success"}
	}
	return nil
}

// nativeStructuredOutputForOutcome exposes a provider-native candidate only
// for a clean successful terminal. The parser recognizes the official
// structured_output field but deliberately does not claim schema validity;
// Driver.Run validates it through the shared core validator first.
func (p *parser) nativeStructuredOutputForOutcome(exitCode int, signal string, timedOut bool, failure *driver.RunFailure) *driver.StructuredOutput {
	if exitCode != 0 || signal != "" || timedOut || failure != nil || p.protocolMalformed || !p.terminalSuccess || p.terminalSessionID == "" || p.structuredOutput == nil {
		return nil
	}
	out := *p.structuredOutput
	out.RawJSON = append(json.RawMessage(nil), p.structuredOutput.RawJSON...)
	out.Valid = false
	out.Value = nil
	out.ValidationErrors = nil
	out.SchemaHash = ""
	return &out
}

func (p *parser) checkpoint(exitCode int) *driver.Checkpoint {
	if exitCode != 0 || p.protocolMalformed || !p.terminalSeen || !p.terminalSuccess || p.terminalSessionID == "" {
		return nil
	}
	return &driver.Checkpoint{
		State: &driver.SessionState{ResumeID: p.terminalSessionID, DisplayID: p.terminalSessionID},
		Valid: true,
	}
}

func (p *parser) checkpointForOutcome(exitCode int, signal string, timedOut bool, failure *driver.RunFailure) *driver.Checkpoint {
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
