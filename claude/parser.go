package claude

import (
	"bytes"
	"encoding/json"
	"sort"
	"strings"
	"sync"
	"time"

	agentadaptor "github.com/agent-dance/agent-adaptor"
)

// claudeParser consumes raw stdout/stderr chunks from a Claude CLI run
// (stream-json mode) and produces the normalized outputs required by the
// adapter contract.
type claudeParser struct {
	mu sync.Mutex

	sink agentadaptor.EventSink

	stdoutLine bytes.Buffer
	stderrLine bytes.Buffer

	transcript    []agentadaptor.TranscriptItem
	assistantText []string

	sessionID       string
	displayID       string
	summary         string // last assistant text; used only as summary fallback
	terminalSummary string // authoritative summary from terminal result events
	usage           *agentadaptor.Usage
	cost            *float64
	resultFinal     map[string]any
	errorMessage    string

	stream       *streamingState
	deltaBuffers map[string]*strings.Builder // messageID -> streamed text (cancel/crash fallback)
}

var claudeCheckpointEvents = map[string]struct{}{
	"completion":      {},
	"result":          {},
	"session":         {},
	"session.updated": {},
	"turn.completed":  {},
}

func newClaudeParser(sink agentadaptor.EventSink) *claudeParser {
	return &claudeParser{sink: sink}
}

func (p *claudeParser) enableStreaming(runID string) {
	if p.sink == nil {
		return
	}
	p.stream = newStreamingState(p.sink, runID, p)
}

func (p *claudeParser) appendTextDelta(messageID, delta string) {
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

func (p *claudeParser) onChunk(stream string, chunk []byte, ts time.Time) error {
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

func (p *claudeParser) finalize() {
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

func (p *claudeParser) processLine(stream string, line []byte, _ time.Time) {
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

func (p *claudeParser) handlePayload(raw string, payload map[string]any) {
	eventType := strings.ToLower(claudeTopLevelString(payload, "type", "event", "kind"))
	subtype := strings.ToLower(claudeTopLevelString(payload, "subtype"))

	p.maybeCaptureSession(payload)

	switch eventType {
	case "system":
		if subtype == "init" {
			model := claudeTopLevelString(payload, "model")
			session := claudeTopLevelString(payload, "session_id", "sessionId")
			p.emit(agentadaptor.TranscriptItem{
				Kind:      agentadaptor.TranscriptInit,
				Model:     model,
				SessionID: session,
			})
			if p.stream != nil {
				p.stream.handleSystemInit(payload)
			}
			return
		}
		if p.stream != nil && subtype == "api_retry" {
			p.stream.handleAPIRetry(payload)
		}
		p.emit(agentadaptor.TranscriptItem{
			Kind:    agentadaptor.TranscriptSystem,
			Text:    raw,
			Subtype: subtype,
		})
	case "stream_event":
		if p.stream != nil {
			p.stream.handleStreamEvent(raw, payload)
			return
		}
		// Without --include-partial-messages stream_event frames should not appear;
		// ignore defensively so batch transcript stays clean.
		return
	case "assistant":
		message := claudeTopLevelObject(payload, "message")
		p.handleAssistantMessage(message)
	case "user":
		message := claudeTopLevelObject(payload, "message")
		p.handleUserMessage(message)
	case "result":
		p.handleResult(raw, payload, subtype)
	case "error":
		message := claudeTopLevelString(payload, "message", "error")
		if message != "" {
			p.errorMessage = message
		}
		if p.stream != nil {
			p.stream.handleErrorTerminal(payload)
		}
		p.emit(agentadaptor.TranscriptItem{
			Kind:     agentadaptor.TranscriptFailure,
			Text:     message,
			Metadata: map[string]string{"code": "error"},
		})
	case "permission_request":
		if p.stream != nil {
			_ = p.sink.EmitStream(agentadaptor.StreamPayload{
				Kind:     agentadaptor.StreamHITLRequested,
				Name:     "permission_request",
				RunID:    p.stream.runID,
				ThreadID: p.sessionID,
				Raw:      payload,
			})
		}
		p.emit(agentadaptor.TranscriptItem{
			Kind:    agentadaptor.TranscriptSystem,
			Text:    raw,
			Subtype: eventType,
			Data:    map[string]any{"payload": payload},
		})
	default:
		// Some CLI versions emit terminal events with top-level event/kind
		// names like "turn.completed" with session_id at the top level.
		if eventType != "" {
			if _, ok := claudeCheckpointEvents[eventType]; ok {
				p.emit(agentadaptor.TranscriptItem{
					Kind:    agentadaptor.TranscriptResult,
					Subtype: eventType,
					Data:    map[string]any{"payload": payload},
				})
				return
			}
		}
		if p.stream != nil {
			op := cloneUnknownPayload(payload)
			op["_claude_wrapper_type"] = eventType
			_ = p.sink.EmitStream(agentadaptor.StreamPayload{
				RunID:    p.stream.runID,
				ThreadID: p.sessionID,
				Name:     eventType,
				Raw:      op,
			})
		}
		p.emit(agentadaptor.TranscriptItem{
			Kind: agentadaptor.TranscriptSystem,
			Text: raw,
			Data: map[string]any{"payload": payload},
		})
	}
}

func cloneUnknownPayload(payload map[string]any) map[string]any {
	if payload == nil {
		return map[string]any{}
	}
	out := make(map[string]any, len(payload))
	for k, v := range payload {
		out[k] = v
	}
	return out
}

func (p *claudeParser) maybeCaptureSession(payload map[string]any) {
	session := claudeTopLevelString(payload, "session_id", "sessionId", "sessionID")
	if session == "" {
		return
	}
	p.sessionID = session
	if display := claudeTopLevelString(payload, "display_id", "displayId"); display != "" {
		p.displayID = display
	} else if p.displayID == "" {
		p.displayID = session
	}
}

func (p *claudeParser) handleAssistantMessage(message map[string]any) {
	if len(message) == 0 {
		return
	}
	model := claudeTopLevelString(message, "model")
	content, ok := message["content"].([]any)
	if !ok {
		return
	}
	for _, raw := range content {
		block, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		kind := strings.ToLower(claudeTopLevelString(block, "type"))
		switch kind {
		case "text":
			text := claudeTopLevelString(block, "text")
			if text == "" {
				continue
			}
			p.assistantText = append(p.assistantText, text)
			p.summary = text
			p.emit(agentadaptor.TranscriptItem{
				Kind:  agentadaptor.TranscriptAssistant,
				Text:  text,
				Model: model,
			})
		case "thinking":
			text := claudeTopLevelString(block, "text", "thinking")
			if text == "" {
				continue
			}
			p.emit(agentadaptor.TranscriptItem{
				Kind:  agentadaptor.TranscriptThinking,
				Text:  text,
				Model: model,
			})
		case "tool_use":
			name := claudeTopLevelString(block, "name")
			id := claudeTopLevelString(block, "id", "tool_use_id")
			p.emit(agentadaptor.TranscriptItem{
				Kind:      agentadaptor.TranscriptToolCall,
				ToolName:  name,
				ToolUseID: id,
				Input:     block["input"],
			})
		}
	}
}

func (p *claudeParser) handleUserMessage(message map[string]any) {
	content, ok := message["content"].([]any)
	if !ok {
		return
	}
	for _, raw := range content {
		block, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		kind := strings.ToLower(claudeTopLevelString(block, "type"))
		if kind == "tool_result" {
			id := claudeTopLevelString(block, "tool_use_id")
			text := claudeResultText(block["content"])
			isError := false
			if v, ok := block["is_error"].(bool); ok {
				isError = v
			}
			if p.stream != nil {
				p.stream.handleUserToolResult(block)
			}
			p.emit(agentadaptor.TranscriptItem{
				Kind:      agentadaptor.TranscriptToolResult,
				ToolUseID: id,
				Text:      text,
				IsError:   isError,
			})
			continue
		}
		text := claudeTopLevelString(block, "text")
		if text != "" {
			p.emit(agentadaptor.TranscriptItem{
				Kind: agentadaptor.TranscriptUser,
				Text: text,
			})
		}
	}
}

func claudeResultText(raw any) string {
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
			if text := claudeTopLevelString(block, "text"); text != "" {
				parts = append(parts, text)
			}
		}
		return strings.Join(parts, "\n")
	}
	return ""
}

func (p *claudeParser) handleResult(raw string, payload map[string]any, subtype string) {
	var decoded map[string]any
	if err := json.Unmarshal([]byte(raw), &decoded); err == nil {
		p.resultFinal = decoded
	}

	if resultText := claudeTopLevelString(payload, "result", "summary"); resultText != "" {
		p.terminalSummary = resultText
	}

	usage := claudeTopLevelObject(payload, "usage")
	if usage != nil {
		input, okInput := claudeTopLevelInt(usage, "input_tokens")
		cached, okCached := claudeTopLevelInt(usage, "cache_read_input_tokens", "cached_input_tokens")
		output, okOutput := claudeTopLevelInt(usage, "output_tokens")
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
	if cost, ok := claudeTopLevelFloat(payload, "total_cost_usd", "cost_usd", "costUSD"); ok {
		c := cost
		p.cost = &c
	}

	isError := subtype == "error" || subtype == "error_during_execution" || strings.HasPrefix(subtype, "error")
	if isError {
		if message := claudeTopLevelString(payload, "message", "result"); message != "" {
			p.errorMessage = message
		}
	}

	p.emit(agentadaptor.TranscriptItem{
		Kind:    agentadaptor.TranscriptResult,
		Subtype: subtype,
		IsError: isError,
		Usage:   p.usage,
		CostUSD: p.cost,
		Text:    claudeTopLevelString(payload, "result"),
		Data:    map[string]any{"payload": payload},
	})
	if p.stream != nil {
		p.stream.handleResultTerminal(payload)
	}
}

func (p *claudeParser) emit(item agentadaptor.TranscriptItem) {
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
func (p *claudeParser) finalSummary() string {
	if strings.TrimSpace(p.terminalSummary) != "" {
		return p.terminalSummary
	}
	return p.summary
}

func (p *claudeParser) buildOutput() string {
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
	// Prefer stable order by message id for deterministic tests.
	keys := make([]string, 0, len(p.deltaBuffers))
	for id := range p.deltaBuffers {
		keys = append(keys, id)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, id := range keys {
		if b := p.deltaBuffers[id]; b != nil {
			s := strings.TrimSpace(b.String())
			if s != "" {
				parts = append(parts, s)
			}
		}
	}
	return strings.Join(parts, "\n\n")
}

// outputMetadata reports when Output was reconstructed from streamed deltas
// because no assistant full text blocks were parsed (e.g. ctx cancel).
func (p *claudeParser) outputMetadata() map[string]string {
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

// checkpoint honors the historical Claude checkpoint recognition: a valid
// session id can come from a recognized terminal event or from a bare
// session-only payload with only simple scalar fields.
func (p *claudeParser) checkpoint(exitCode int) *agentadaptor.DriverCheckpoint {
	if exitCode != 0 || p.sessionID == "" {
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

func snapshotClaudeStdout(stdout string) *claudeParser {
	p := newClaudeParser(nil)
	_ = p.onChunk("stdout", []byte(stdout), time.Now().UTC())
	p.finalize()
	return p
}

// parseCheckpoint keeps the historical snapshot-based recognition used by
// legacy unit tests. It now runs the same streaming parser on the full stdout
// buffer and enforces the "event must be recognized, else require scalar-only
// session payload" rule before promoting the session id into a checkpoint.
func parseCheckpoint(stdout string, exitCode int) *agentadaptor.DriverCheckpoint {
	if exitCode != 0 {
		return nil
	}
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
		sessionID := claudeTopLevelString(payload, "session_id", "sessionId", "sessionID")
		if sessionID == "" {
			continue
		}
		eventKind := strings.ToLower(claudeTopLevelString(payload, "event", "type", "kind"))
		if eventKind != "" {
			if _, ok := claudeCheckpointEvents[eventKind]; !ok {
				continue
			}
		} else if !isClaudeScalarOnlyPayload(payload) {
			continue
		}
		display := claudeTopLevelString(payload, "display_id", "displayId")
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

func isClaudeScalarOnlyPayload(payload map[string]any) bool {
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

func claudeTopLevelString(payload map[string]any, keys ...string) string {
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

func claudeTopLevelObject(payload map[string]any, key string) map[string]any {
	raw, ok := payload[key]
	if !ok {
		return nil
	}
	value, _ := raw.(map[string]any)
	return value
}

func claudeTopLevelInt(payload map[string]any, keys ...string) (int, bool) {
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

func claudeTopLevelFloat(payload map[string]any, keys ...string) (float64, bool) {
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
