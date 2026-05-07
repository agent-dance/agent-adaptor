package cursor

import (
	"bytes"
	"encoding/json"
	"strings"
	"sync"
	"time"

	agentadaptor "github.com/agent-dance/agent-adaptor"
)

// cursorParser consumes raw stdout/stderr chunks from a Cursor Agent CLI run
// (stream-json) and produces the normalized outputs required by the adapter
// contract.
type cursorParser struct {
	mu sync.Mutex

	sink agentadaptor.EventSink

	stdoutLine bytes.Buffer
	stderrLine bytes.Buffer

	transcript    []agentadaptor.TranscriptItem
	assistantText []string

	// deltaBuffer accumulates consecutive assistant.delta fragments so that
	// a pure-delta stream still produces a final Output and Summary
	// fallback even when the CLI never emits a non-delta assistant event.
	deltaBuffer strings.Builder

	sessionID       string
	displayID       string
	summary         string // last assistant text; used only as summary fallback
	terminalSummary string // authoritative summary from terminal result events
	usage           *agentadaptor.Usage
	cost            *float64
	resultFinal     map[string]any
	errorMessage    string
}

var cursorCheckpointEvents = map[string]struct{}{
	"result":          {},
	"run.completed":   {},
	"session":         {},
	"session.updated": {},
}

func newCursorParser(sink agentadaptor.EventSink) *cursorParser {
	return &cursorParser{sink: sink}
}

func (p *cursorParser) onChunk(stream string, chunk []byte, ts time.Time) error {
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

func (p *cursorParser) finalize() {
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
	p.flushDeltaLocked()
}

// flushDeltaLocked promotes any buffered assistant.delta fragments into a
// finalized assistant block so they participate in Output and the summary
// fallback. Callers must hold p.mu.
func (p *cursorParser) flushDeltaLocked() {
	if p.deltaBuffer.Len() == 0 {
		return
	}
	merged := p.deltaBuffer.String()
	p.deltaBuffer.Reset()
	if strings.TrimSpace(merged) == "" {
		return
	}
	p.assistantText = append(p.assistantText, merged)
	p.summary = merged
}

func (p *cursorParser) processLine(stream string, line []byte, _ time.Time) {
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
		p.emit(agentadaptor.TranscriptItem{
			Kind: agentadaptor.TranscriptStdout,
			Text: text,
		})
		return
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(trimmed), &payload); err != nil {
		p.emit(agentadaptor.TranscriptItem{
			Kind: agentadaptor.TranscriptStdout,
			Text: text,
		})
		return
	}

	p.handlePayload(trimmed, payload)
}

func (p *cursorParser) handlePayload(raw string, payload map[string]any) {
	eventType := strings.ToLower(cursorTopLevelString(payload, "type", "event", "kind"))

	p.maybeCaptureSession(payload)

	switch eventType {
	case "assistant", "assistant_message", "assistant.message", "message":
		p.flushDeltaLocked()
		p.handleAssistant(payload)
	case "assistant.delta", "delta":
		p.handleAssistantDelta(payload)
	case "thinking", "reasoning":
		text := cursorTopLevelString(payload, "text", "message")
		if text == "" {
			return
		}
		p.emit(agentadaptor.TranscriptItem{
			Kind: agentadaptor.TranscriptThinking,
			Text: text,
		})
	case "tool_call", "tool.call":
		name := cursorTopLevelString(payload, "name", "tool_name")
		id := cursorTopLevelString(payload, "id", "call_id", "tool_use_id")
		p.emit(agentadaptor.TranscriptItem{
			Kind:      agentadaptor.TranscriptToolCall,
			ToolName:  name,
			ToolUseID: id,
			Input:     payload["input"],
		})
	case "tool_result", "tool.result":
		id := cursorTopLevelString(payload, "id", "call_id", "tool_use_id")
		text := cursorTopLevelString(payload, "text", "output", "result")
		p.emit(agentadaptor.TranscriptItem{
			Kind:      agentadaptor.TranscriptToolResult,
			ToolUseID: id,
			Text:      text,
		})
	case "init", "session", "session.updated", "system":
		model := cursorTopLevelString(payload, "model")
		session := cursorTopLevelString(payload, "session_id", "sessionId", "sessionID")
		p.emit(agentadaptor.TranscriptItem{
			Kind:      agentadaptor.TranscriptInit,
			Model:     model,
			SessionID: session,
		})
	case "result", "run.completed", "completion":
		p.flushDeltaLocked()
		p.handleResult(raw, payload, eventType)
	case "error", "run.failed":
		message := cursorTopLevelString(payload, "message", "error")
		if message != "" {
			p.errorMessage = message
		}
		p.emit(agentadaptor.TranscriptItem{
			Kind:     agentadaptor.TranscriptFailure,
			Text:     message,
			Metadata: map[string]string{"code": "error"},
		})
	default:
		if eventType == "" && isCursorScalarOnlyPayload(payload) {
			// A bare session-only payload: treat as init but do not emit
			// a system frame redundantly; it was already captured by
			// maybeCaptureSession.
			return
		}
		p.emit(agentadaptor.TranscriptItem{
			Kind: agentadaptor.TranscriptSystem,
			Text: raw,
			Data: map[string]any{"payload": payload},
		})
	}
}

func (p *cursorParser) maybeCaptureSession(payload map[string]any) {
	session := cursorTopLevelString(payload, "session_id", "sessionId", "sessionID")
	if session == "" {
		return
	}
	p.sessionID = session
	if display := cursorTopLevelString(payload, "display_id", "displayId"); display != "" {
		p.displayID = display
	} else if p.displayID == "" {
		p.displayID = session
	}
}

func (p *cursorParser) handleAssistant(payload map[string]any) {
	// Cursor may emit either a plain text message or a structured content
	// array similar to Claude.
	if content, ok := payload["content"].([]any); ok {
		for _, entry := range content {
			block, ok := entry.(map[string]any)
			if !ok {
				continue
			}
			kind := strings.ToLower(cursorTopLevelString(block, "type"))
			switch kind {
			case "text", "":
				text := cursorTopLevelString(block, "text", "message")
				if text == "" {
					continue
				}
				p.assistantText = append(p.assistantText, text)
				p.summary = text
				p.emit(agentadaptor.TranscriptItem{
					Kind: agentadaptor.TranscriptAssistant,
					Text: text,
				})
			case "thinking":
				text := cursorTopLevelString(block, "text")
				if text == "" {
					continue
				}
				p.emit(agentadaptor.TranscriptItem{
					Kind: agentadaptor.TranscriptThinking,
					Text: text,
				})
			}
		}
		return
	}
	text := cursorTopLevelString(payload, "text", "message")
	if text == "" {
		return
	}
	p.assistantText = append(p.assistantText, text)
	p.summary = text
	p.emit(agentadaptor.TranscriptItem{
		Kind: agentadaptor.TranscriptAssistant,
		Text: text,
	})
}

func (p *cursorParser) handleAssistantDelta(payload map[string]any) {
	// Delta tokens can legitimately include leading/trailing whitespace
	// (for example " " between words), so we must preserve the raw string
	// rather than going through cursorTopLevelString which trims.
	text := cursorRawString(payload, "delta", "text", "message")
	if text == "" {
		return
	}
	p.deltaBuffer.WriteString(text)
	p.emit(agentadaptor.TranscriptItem{
		Kind:  agentadaptor.TranscriptAssistant,
		Text:  text,
		Delta: true,
	})
}

// cursorRawString returns the first matching string value verbatim, without
// trimming whitespace. It is used for streaming fragments (like
// assistant.delta tokens) where whitespace is part of the payload.
func cursorRawString(payload map[string]any, keys ...string) string {
	for _, key := range keys {
		raw, ok := payload[key]
		if !ok {
			continue
		}
		if value, ok := raw.(string); ok && value != "" {
			return value
		}
	}
	return ""
}

func (p *cursorParser) handleResult(raw string, payload map[string]any, subtype string) {
	var decoded map[string]any
	if err := json.Unmarshal([]byte(raw), &decoded); err == nil {
		p.resultFinal = decoded
	}
	if result := cursorTopLevelString(payload, "result", "summary", "text"); result != "" {
		p.terminalSummary = result
	}

	usage := cursorTopLevelObject(payload, "usage")
	if usage != nil {
		input, okInput := cursorTopLevelInt(usage, "input_tokens")
		cached, okCached := cursorTopLevelInt(usage, "cached_input_tokens", "cache_read_input_tokens")
		output, okOutput := cursorTopLevelInt(usage, "output_tokens")
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
	if cost, ok := cursorTopLevelFloat(payload, "cost_usd", "costUSD", "total_cost_usd"); ok {
		c := cost
		p.cost = &c
	}

	p.emit(agentadaptor.TranscriptItem{
		Kind:    agentadaptor.TranscriptResult,
		Subtype: subtype,
		Usage:   p.usage,
		CostUSD: p.cost,
		Text:    cursorTopLevelString(payload, "result", "summary"),
		Data:    map[string]any{"payload": payload},
	})
}

func (p *cursorParser) emit(item agentadaptor.TranscriptItem) {
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
func (p *cursorParser) finalSummary() string {
	if strings.TrimSpace(p.terminalSummary) != "" {
		return p.terminalSummary
	}
	return p.summary
}

func (p *cursorParser) buildOutput() string {
	nonEmpty := make([]string, 0, len(p.assistantText))
	for _, text := range p.assistantText {
		if strings.TrimSpace(text) != "" {
			nonEmpty = append(nonEmpty, text)
		}
	}
	return strings.Join(nonEmpty, "\n\n")
}

// checkpoint promotes a captured Cursor session id into a persistable
// DriverCheckpoint. The session id is minted server-side on the first event,
// so it remains resumable independent of how the local subprocess terminated.
// We gate solely on a non-empty session_id; abnormal CLI exits do not by
// themselves invalidate the session — if the session is unusable the upstream
// API will surface an actionable error on the next resume attempt.
func (p *cursorParser) checkpoint(exitCode int) *agentadaptor.DriverCheckpoint {
	_ = exitCode // retained in the signature for call-site symmetry; see GoDoc.
	if p.sessionID == "" {
		return nil
	}
	display := p.displayID
	if display == "" {
		display = p.sessionID
	}
	return &agentadaptor.DriverCheckpoint{
		State: &agentadaptor.DriverSessionState{
			ResumeID:  p.sessionID,
			DisplayID: display,
		},
		Valid: true,
	}
}

func snapshotCursorStdout(stdout string) *cursorParser {
	p := newCursorParser(nil)
	_ = p.onChunk("stdout", []byte(stdout), time.Now().UTC())
	p.finalize()
	return p
}

// parseCheckpoint preserves the historical snapshot semantics for legacy unit
// tests: only recognized terminal events or scalar-only session payloads may
// promote the session id into a checkpoint.
//
// Like (*cursorParser).checkpoint, this function no longer gates on
// exitCode: a recognized session_id remains resumable even if the CLI
// exited abnormally. exitCode is kept in the signature for call-site
// symmetry with the streaming path.
func parseCheckpoint(stdout string, exitCode int) *agentadaptor.DriverCheckpoint {
	_ = exitCode
	var last *agentadaptor.DriverCheckpoint
	for _, line := range strings.Split(stdout, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "{") {
			continue
		}
		var payload map[string]any
		if err := json.Unmarshal([]byte(trimmed), &payload); err != nil {
			continue
		}
		sessionID := cursorTopLevelString(payload, "session_id", "sessionId", "sessionID")
		if sessionID == "" {
			continue
		}
		eventKind := strings.ToLower(cursorTopLevelString(payload, "event", "type", "kind"))
		if eventKind != "" {
			if _, ok := cursorCheckpointEvents[eventKind]; !ok {
				continue
			}
		} else if !isCursorScalarOnlyPayload(payload) {
			continue
		}
		display := cursorTopLevelString(payload, "display_id", "displayId")
		if display == "" {
			display = sessionID
		}
		last = &agentadaptor.DriverCheckpoint{
			State: &agentadaptor.DriverSessionState{
				ResumeID:  sessionID,
				DisplayID: display,
			},
			Valid: true,
		}
	}
	return last
}

func isCursorScalarOnlyPayload(payload map[string]any) bool {
	for key, value := range payload {
		switch value.(type) {
		case nil, string, bool, float64:
		default:
			return false
		}

		switch key {
		case "session_id", "sessionId", "sessionID", "display_id", "displayId", "event", "type", "kind", "timestamp", "ts", "created_at", "createdAt":
		default:
			return false
		}
	}
	return true
}

func cursorTopLevelString(payload map[string]any, keys ...string) string {
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

func cursorTopLevelObject(payload map[string]any, key string) map[string]any {
	raw, ok := payload[key]
	if !ok {
		return nil
	}
	value, _ := raw.(map[string]any)
	return value
}

func cursorTopLevelInt(payload map[string]any, keys ...string) (int, bool) {
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

func cursorTopLevelFloat(payload map[string]any, keys ...string) (float64, bool) {
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
