package cursor

import (
	"bytes"
	"encoding/json"
	"strings"
	"sync"
	"time"

	"github.com/agent-dance/agent-adaptor/driver"
)

// cursorParser consumes raw stdout/stderr chunks from a Cursor Agent CLI run
// (stream-json) and produces the normalized outputs required by the adapter
// contract.
type cursorParser struct {
	mu sync.Mutex

	sink driver.EventSink

	stdoutLine bytes.Buffer
	stderrLine bytes.Buffer

	transcript    []driver.TranscriptItem
	assistantText []string

	// deltaBuffer accumulates consecutive assistant.delta fragments so that
	// a pure-delta stream still produces a final Output and Summary
	// fallback even when the CLI never emits a non-delta assistant event.
	deltaBuffer strings.Builder

	sessionID         string
	displayID         string
	summary           string // last assistant text retained for parser diagnostics
	terminalSummary   string // authoritative summary from terminal result events
	usage             *driver.Usage
	cost              *float64
	terminal          *driver.TerminalPayload
	errorMessage      string
	terminalSeen      bool
	terminalSuccess   bool
	protocolMalformed bool
}

func newCursorParser(sink driver.EventSink) *cursorParser {
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
		p.emit(driver.TranscriptItem{
			Kind: driver.TranscriptStderr,
			Text: text,
		})
		return
	}

	if !strings.HasPrefix(trimmed, "{") {
		p.emit(driver.TranscriptItem{
			Kind: driver.TranscriptStdout,
			Text: text,
		})
		return
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(trimmed), &payload); err != nil {
		p.protocolMalformed = true
		p.emit(driver.TranscriptItem{
			Kind: driver.TranscriptStdout,
			Text: text,
		})
		return
	}

	p.handlePayload(trimmed, payload)
}

func (p *cursorParser) handlePayload(raw string, payload map[string]any) {
	if p.terminalSeen {
		p.protocolMalformed = true
		return
	}
	eventType := strings.ToLower(cursorTopLevelString(payload, "type", "event", "kind"))

	if cursorFormalEvent(eventType) {
		p.maybeCaptureSession(payload)
	}

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
		p.emit(driver.TranscriptItem{
			Kind: driver.TranscriptThinking,
			Text: text,
		})
	case "tool_call", "tool.call":
		name := cursorTopLevelString(payload, "name", "tool_name")
		id := cursorTopLevelString(payload, "id", "call_id", "tool_use_id")
		p.emit(driver.TranscriptItem{
			Kind:      driver.TranscriptToolCall,
			ToolName:  name,
			ToolUseID: id,
			Input:     payload["input"],
		})
	case "tool_result", "tool.result":
		id := cursorTopLevelString(payload, "id", "call_id", "tool_use_id")
		text := cursorTopLevelString(payload, "text", "output", "result")
		p.emit(driver.TranscriptItem{
			Kind:      driver.TranscriptToolResult,
			ToolUseID: id,
			Text:      text,
		})
	case "init", "session", "session.updated", "system":
		model := cursorTopLevelString(payload, "model")
		session := cursorTopLevelString(payload, "session_id", "sessionId", "sessionID")
		p.emit(driver.TranscriptItem{
			Kind:      driver.TranscriptInit,
			Model:     model,
			SessionID: session,
		})
	case "result", "run.completed", "completion":
		p.flushDeltaLocked()
		p.handleResult(raw, payload, eventType)
	case "error", "run.failed":
		p.terminalSeen = true
		p.terminalSuccess = false
		p.terminal = &driver.TerminalPayload{Event: eventType, JSON: append(json.RawMessage(nil), raw...)}
		message := cursorTopLevelString(payload, "message", "error")
		if message == "" {
			message = "cursor provider error"
		}
		p.errorMessage = message
		p.emit(driver.TranscriptItem{
			Kind:     driver.TranscriptFailure,
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
		p.emit(driver.TranscriptItem{
			Kind: driver.TranscriptSystem,
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
				p.emit(driver.TranscriptItem{
					Kind: driver.TranscriptAssistant,
					Text: text,
				})
			case "thinking":
				text := cursorTopLevelString(block, "text")
				if text == "" {
					continue
				}
				p.emit(driver.TranscriptItem{
					Kind: driver.TranscriptThinking,
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
	p.emit(driver.TranscriptItem{
		Kind: driver.TranscriptAssistant,
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
	p.emit(driver.TranscriptItem{
		Kind:  driver.TranscriptAssistant,
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
	p.terminalSeen = true
	isError, _ := payload["is_error"].(bool)
	p.terminalSuccess = (subtype == "result" || subtype == "run.completed" || subtype == "completion") && !isError
	if !p.terminalSuccess && p.errorMessage == "" {
		p.errorMessage = "cursor terminal result did not report success"
	}

	p.terminal = &driver.TerminalPayload{
		Event: subtype,
		JSON:  append(json.RawMessage(nil), raw...),
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
	if cost, ok := cursorTopLevelFloat(payload, "cost_usd", "costUSD", "total_cost_usd"); ok {
		c := cost
		p.cost = &c
	}

	p.emit(driver.TranscriptItem{
		Kind:    driver.TranscriptResult,
		Subtype: subtype,
		Usage:   p.usage,
		CostUSD: p.cost,
		Text:    cursorTopLevelString(payload, "result", "summary"),
		Data:    map[string]any{"payload": payload},
	})
}

func cursorFormalEvent(eventType string) bool {
	switch eventType {
	case "assistant", "assistant_message", "assistant.message", "message", "assistant.delta", "delta",
		"thinking", "reasoning", "tool_call", "tool.call", "tool_result", "tool.result",
		"init", "session", "session.updated", "system", "result", "run.completed", "completion", "error", "run.failed":
		return true
	default:
		return false
	}
}

func (p *cursorParser) emit(item driver.TranscriptItem) {
	p.transcript = append(p.transcript, item)
	if p.sink == nil {
		return
	}
	clone := item
	_ = p.sink.Emit(driver.RunEvent{
		Type:      driver.RunEventItem,
		Timestamp: time.Now().UTC(),
		Item:      &clone,
	})
}

// finalSummary returns only a bounded provider terminal summary. Assistant
// output is never reused as Summary because it may be arbitrarily large.
func (p *cursorParser) finalSummary() string {
	return strings.TrimSpace(p.terminalSummary)
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

// checkpoint promotes a Cursor session only after a clean process exit and an
// official successful result/completion event with a top-level session_id.
// Session/init frames and partial assistant output are not terminal proof.
func (p *cursorParser) checkpoint(exitCode int) *driver.Checkpoint {
	if exitCode != 0 || p.protocolMalformed || !p.terminalSeen || !p.terminalSuccess || p.sessionID == "" {
		return nil
	}
	display := p.displayID
	if display == "" {
		display = p.sessionID
	}
	return &driver.Checkpoint{
		State: &driver.SessionState{
			ResumeID:  p.sessionID,
			DisplayID: display,
		},
		Valid: true,
	}
}

func (p *cursorParser) checkpointForOutcome(exitCode int, signal string, timedOut bool, failure *driver.RunFailure) *driver.Checkpoint {
	if signal != "" || timedOut || failure != nil {
		return nil
	}
	return p.checkpoint(exitCode)
}

func snapshotCursorStdout(stdout string) *cursorParser {
	p := newCursorParser(nil)
	_ = p.onChunk("stdout", []byte(stdout), time.Now().UTC())
	p.finalize()
	return p
}

// parseCheckpoint is the snapshot compatibility entry point and shares the
// live parser's formal-terminal safety gate.
func parseCheckpoint(stdout string, exitCode int) *driver.Checkpoint {
	return snapshotCursorStdout(stdout).checkpoint(exitCode)
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
