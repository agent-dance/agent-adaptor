package codex

import (
	"bytes"
	"encoding/json"
	"strings"
	"sync"
	"time"

	agentadaptor "github.com/agent-dance/agent-adaptor"
)

// codexParser consumes raw stdout/stderr chunks from a Codex CLI run and
// produces the normalized outputs required by the adapter contract:
// transcript items, assistant output, summary, terminal result payload,
// usage, and the resume checkpoint.
//
// The parser is line-oriented and buffers partial lines across chunk
// boundaries so that both realtime streaming consumers and snapshot-based
// callers see the same semantics.
type codexParser struct {
	mu sync.Mutex

	sink agentadaptor.EventSink

	stdoutLine bytes.Buffer
	stderrLine bytes.Buffer

	transcript    []agentadaptor.TranscriptItem
	assistantText []string

	sessionID          string
	displayID          string
	summary            string // last assistant text; used only as summary fallback
	terminalSummary    string // authoritative summary from terminal result events
	usage              *agentadaptor.Usage
	cost               *float64
	hasUsage           bool
	resultFinal        map[string]any
	structuredOutput   *agentadaptor.StructuredOutput
	errorMessage       string
	checkpointThreadID string
	threadStarted      bool
	turnCompleted      bool
	terminalFailed     bool
	protocolMalformed  bool
}

func newCodexParser(sink agentadaptor.EventSink) *codexParser {
	return &codexParser{sink: sink}
}

func (p *codexParser) onChunk(stream string, chunk []byte, ts time.Time) error {
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

// finalize flushes any trailing partial line so its content is not lost.
func (p *codexParser) finalize() {
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

func (p *codexParser) processLine(stream string, line []byte, _ time.Time) {
	text := strings.TrimRight(string(line), "\r")
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return
	}

	if stream == "stderr" {
		p.emit(agentadaptor.TranscriptItem{
			Kind: agentadaptor.TranscriptStderr,
			Text: text,
		})
		return
	}

	if !strings.HasPrefix(trimmed, "{") {
		// Codex CLI may print non-JSON stdout banners; treat them as raw
		// stdout transcript entries rather than guessing JSON shape.
		p.emit(agentadaptor.TranscriptItem{
			Kind: agentadaptor.TranscriptStdout,
			Text: text,
		})
		return
	}

	var payload map[string]any
	if err := json.Unmarshal([]byte(trimmed), &payload); err != nil {
		p.protocolMalformed = true
		p.emit(agentadaptor.TranscriptItem{
			Kind: agentadaptor.TranscriptStdout,
			Text: text,
		})
		return
	}

	p.handlePayload(trimmed, payload)
}

func (p *codexParser) handlePayload(raw string, payload map[string]any) {
	if p.turnCompleted || p.terminalFailed {
		p.protocolMalformed = true
		return
	}
	kind := codexEventKind(payload)
	switch kind {
	case "thread.started":
		if threadID := codexTopLevelString(payload, "thread_id", "threadId"); threadID != "" {
			p.threadStarted = true
			p.checkpointThreadID = threadID
			p.sessionID = threadID
			p.displayID = threadID
			p.emit(agentadaptor.TranscriptItem{
				Kind:      agentadaptor.TranscriptInit,
				SessionID: threadID,
			})
		}
	case "item.started":
		item := codexTopLevelObject(payload, "item")
		p.handleItem(item, true)
	case "item.completed":
		item := codexTopLevelObject(payload, "item")
		p.handleItem(item, false)
	case "turn.completed":
		p.turnCompleted = true
		p.handleTerminal(raw, payload, kind)
	case "turn.failed":
		p.terminalFailed = true
		p.handleTerminal(raw, payload, kind)
	case "result":
		// Historical provider wrappers used result, but Codex CLI checkpoint
		// validity is proved only by thread.started + turn.completed.
		p.handleTerminal(raw, payload, kind)
	case "error":
		p.terminalFailed = true
		message := codexTopLevelString(payload, "message")
		if message == "" {
			message = "codex provider error"
		}
		p.errorMessage = message
		p.emit(agentadaptor.TranscriptItem{
			Kind:     agentadaptor.TranscriptFailure,
			Text:     message,
			Metadata: map[string]string{"code": "error"},
		})
	case "session", "session.updated":
		if id := codexTopLevelString(payload, "session_id", "sessionId", "sessionID"); id != "" {
			p.sessionID = id
			if display := codexTopLevelString(payload, "display_id", "displayId"); display != "" {
				p.displayID = display
			} else if p.displayID == "" {
				p.displayID = id
			}
			p.emit(agentadaptor.TranscriptItem{
				Kind:      agentadaptor.TranscriptInit,
				SessionID: id,
			})
		}
	default:
		if p.tryAbsorbCheckpointOnlyPayload(payload) {
			return
		}
		p.emit(agentadaptor.TranscriptItem{
			Kind: agentadaptor.TranscriptSystem,
			Text: raw,
			Data: map[string]any{"payload": payload},
		})
	}
}

func (p *codexParser) tryAbsorbCheckpointOnlyPayload(payload map[string]any) bool {
	if !isCheckpointPayload(payload) {
		return false
	}
	sessionID := codexTopLevelString(payload, "session_id", "sessionId", "sessionID", "thread_id", "threadId")
	if sessionID == "" {
		return false
	}
	p.sessionID = sessionID
	if display := codexTopLevelString(payload, "display_id", "displayId"); display != "" {
		p.displayID = display
	} else {
		p.displayID = sessionID
	}
	p.emit(agentadaptor.TranscriptItem{
		Kind:      agentadaptor.TranscriptInit,
		SessionID: sessionID,
	})
	return true
}

func (p *codexParser) handleItem(item map[string]any, delta bool) {
	if len(item) == 0 {
		return
	}
	switch codexEventKind(item) {
	case "agent_message":
		text := codexTopLevelString(item, "text", "message")
		if text == "" {
			return
		}
		if !delta {
			p.assistantText = append(p.assistantText, text)
			p.summary = text
		}
		p.emit(agentadaptor.TranscriptItem{
			Kind:  agentadaptor.TranscriptAssistant,
			Text:  text,
			Delta: delta,
		})
	case "reasoning", "thinking":
		text := codexTopLevelString(item, "text", "message")
		if text == "" {
			return
		}
		p.emit(agentadaptor.TranscriptItem{
			Kind:  agentadaptor.TranscriptThinking,
			Text:  text,
			Delta: delta,
		})
	case "command_execution", "tool_call", "function_call":
		name := codexTopLevelString(item, "name", "tool_name", "command")
		id := codexTopLevelString(item, "id", "call_id", "tool_use_id")
		p.emit(agentadaptor.TranscriptItem{
			Kind:      agentadaptor.TranscriptToolCall,
			ToolName:  name,
			ToolUseID: id,
			Input:     item["input"],
		})
	case "tool_result", "command_output", "file_change":
		id := codexTopLevelString(item, "id", "call_id", "tool_use_id")
		text := codexTopLevelString(item, "text", "output", "message")
		p.emit(agentadaptor.TranscriptItem{
			Kind:      agentadaptor.TranscriptToolResult,
			ToolUseID: id,
			Text:      text,
		})
	}
}

func (p *codexParser) handleTerminal(raw string, payload map[string]any, kind string) {
	var decoded map[string]any
	if err := json.Unmarshal([]byte(raw), &decoded); err == nil {
		p.resultFinal = decoded
	}

	usage := codexTopLevelObject(payload, "usage")
	if usage != nil {
		input, okInput := codexTopLevelInt(usage, "input_tokens")
		cached, okCached := codexTopLevelInt(usage, "cached_input_tokens")
		output, okOutput := codexTopLevelInt(usage, "output_tokens")
		if okInput || okCached || okOutput {
			p.hasUsage = true
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
	if cost, ok := codexTopLevelFloat(payload, "cost_usd", "costUSD", "cost"); ok {
		c := cost
		p.cost = &c
	}

	isError := kind == "turn.failed"
	if isError {
		if errPayload := codexTopLevelObject(payload, "error"); errPayload != nil {
			if message := codexTopLevelString(errPayload, "message"); message != "" {
				p.errorMessage = message
			}
		}
		if p.errorMessage == "" {
			p.errorMessage = "codex terminal event reported failure"
		}
	}
	if summary := codexTopLevelString(payload, "summary", "result", "text"); summary != "" {
		p.terminalSummary = summary
	}
	if raw, ok := codexStructuredJSONFromTerminal(payload); ok {
		p.structuredOutput = &agentadaptor.StructuredOutput{
			Format:  agentadaptor.OutputFormatJSONSchema,
			Source:  agentadaptor.StructuredOutputSourceNative,
			RawJSON: raw,
			Valid:   true,
		}
	}

	p.emit(agentadaptor.TranscriptItem{
		Kind:    agentadaptor.TranscriptResult,
		Subtype: kind,
		IsError: isError,
		Usage:   p.usage,
		CostUSD: p.cost,
		Data:    map[string]any{"payload": payload},
	})
}

func codexStructuredJSONFromTerminal(payload map[string]any) (json.RawMessage, bool) {
	for _, key := range []string{"structured_output", "structuredOutput", "output", "result", "text"} {
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

func (p *codexParser) emit(item agentadaptor.TranscriptItem) {
	p.transcript = append(p.transcript, item)
	if p.sink == nil {
		return
	}
	clone := item
	_ = p.sink.Emit(agentadaptor.RunEvent{
		Type:      agentadaptor.RunEventItem,
		Timestamp: time.Now().UTC(),
		Item:      &clone,
	})
}

// finalSummary implements the Summary precedence rule: terminal result text
// wins; the last assistant text only serves as a fallback.
func (p *codexParser) finalSummary() string {
	if strings.TrimSpace(p.terminalSummary) != "" {
		return p.terminalSummary
	}
	return p.summary
}

// buildOutput concatenates assistant-kind transcript entries with the
// "\n\n" separator required by the output contract.
func (p *codexParser) buildOutput() string {
	nonEmpty := make([]string, 0, len(p.assistantText))
	for _, text := range p.assistantText {
		if strings.TrimSpace(text) != "" {
			nonEmpty = append(nonEmpty, text)
		}
	}
	return strings.Join(nonEmpty, "\n\n")
}

func (p *codexParser) checkpoint(exitCode int) *agentadaptor.DriverCheckpoint {
	if exitCode != 0 || p.protocolMalformed || p.terminalFailed || !p.threadStarted || !p.turnCompleted || p.checkpointThreadID == "" {
		return nil
	}
	display := p.displayID
	if display == "" {
		display = p.checkpointThreadID
	}
	return &agentadaptor.DriverCheckpoint{
		State: &agentadaptor.DriverSessionState{
			ResumeID:  p.checkpointThreadID,
			DisplayID: display,
		},
		Valid: true,
	}
}

func (p *codexParser) checkpointForOutcome(exitCode int, signal string, timedOut bool, failure *agentadaptor.RunFailure) *agentadaptor.DriverCheckpoint {
	if signal != "" || timedOut || failure != nil {
		return nil
	}
	return p.checkpoint(exitCode)
}

// snapshotCodexStdout feeds a complete stdout string through the parser and
// returns its final state. It exists so legacy callers and unit tests can
// operate on a complete stdout dump without giving up the streaming parser as
// the single source of truth.
func snapshotCodexStdout(stdout string) *codexParser {
	p := newCodexParser(nil)
	_ = p.onChunk("stdout", []byte(stdout), time.Now().UTC())
	p.finalize()
	return p
}

func codexEventKind(payload map[string]any) string {
	return strings.ToLower(codexTopLevelString(payload, "event", "type", "kind"))
}

func codexTopLevelString(payload map[string]any, keys ...string) string {
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

func codexTopLevelObject(payload map[string]any, key string) map[string]any {
	raw, ok := payload[key]
	if !ok {
		return nil
	}
	value, _ := raw.(map[string]any)
	return value
}

func codexTopLevelInt(payload map[string]any, keys ...string) (int, bool) {
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

func codexTopLevelFloat(payload map[string]any, keys ...string) (float64, bool) {
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

func isCheckpointPayload(payload map[string]any) bool {
	for key, value := range payload {
		switch value.(type) {
		case nil, string, bool, float64:
		default:
			return false
		}

		switch key {
		case "session_id", "sessionId", "sessionID", "thread_id", "threadId", "display_id", "displayId", "event", "type", "kind", "timestamp", "ts", "created_at", "createdAt":
		default:
			return false
		}
	}
	return true
}
