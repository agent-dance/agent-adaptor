package cursor

import (
	"bytes"
	"encoding/json"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/agent-dance/agent-adaptor/driver"
)

// cursorParser consumes the official Cursor Agent CLI stream-json protocol.
// Provider semantics are recognized only from the documented top-level event
// type/subtype and nested message/tool_call shapes. Unknown events remain
// available as system transcript items without being guessed into output,
// tool results, terminal state, or checkpoints.
type cursorParser struct {
	mu sync.Mutex

	sink driver.EventSink

	stdoutLine bytes.Buffer
	stderrLine bytes.Buffer

	transcript      []driver.TranscriptItem
	assistantDeltas []string
	terminalResult  string

	sessionID         string
	terminalSession   string
	displayID         string
	usage             *driver.Usage
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
		line := append([]byte(nil), buf.Bytes()[:idx]...)
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
}

func (p *cursorParser) processLine(stream string, line []byte, _ time.Time) {
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

func (p *cursorParser) handlePayload(raw string, payload map[string]any) {
	if p.terminalSeen {
		// The documented stream terminates with result.success. Any payload
		// after it makes the protocol unsuitable as checkpoint proof.
		p.protocolMalformed = true
		return
	}

	eventType := cursorString(payload, "type")
	subtype := cursorString(payload, "subtype")
	switch eventType {
	case "system":
		if subtype != "init" {
			p.emitUnknown(raw, payload)
			return
		}
		p.captureSession(payload)
		p.emit(driver.TranscriptItem{
			Kind:      driver.TranscriptInit,
			Model:     cursorString(payload, "model"),
			SessionID: cursorString(payload, "session_id"),
		})
	case "user":
		p.captureSession(payload)
		p.handleMessage(payload, driver.TranscriptUser, false)
	case "assistant":
		p.captureSession(payload)
		p.handleMessage(payload, driver.TranscriptAssistant, true)
	case "tool_call":
		p.captureSession(payload)
		if !p.handleToolCall(payload, subtype) {
			p.emitUnknown(raw, payload)
		}
	case "result":
		if subtype != "success" {
			p.emitUnknown(raw, payload)
			return
		}
		p.handleSuccessResult(raw, payload)
	default:
		p.emitUnknown(raw, payload)
	}
}

func (p *cursorParser) captureSession(payload map[string]any) {
	session := cursorString(payload, "session_id")
	if session == "" {
		// session_id is required on every documented Cursor stream-json frame.
		// Treat omission as a protocol error even for stateless runs; otherwise
		// an exit-zero result could be reported as successful while being
		// unusable for Thread persistence.
		p.protocolMalformed = true
		return
	}
	if p.sessionID != "" && p.sessionID != session {
		p.protocolMalformed = true
	}
	p.sessionID = session
	if p.displayID == "" {
		p.displayID = session
	}
}

func (p *cursorParser) handleMessage(payload map[string]any, kind driver.TranscriptKind, delta bool) {
	message := cursorObject(payload, "message")
	if message == nil {
		p.protocolMalformed = true
		return
	}
	content, ok := message["content"].([]any)
	if !ok {
		p.protocolMalformed = true
		return
	}
	for _, entry := range content {
		block, ok := entry.(map[string]any)
		if !ok || cursorString(block, "type") != "text" {
			continue
		}
		text, ok := block["text"].(string)
		if !ok || text == "" {
			continue
		}
		if kind == driver.TranscriptAssistant {
			p.assistantDeltas = append(p.assistantDeltas, text)
		}
		p.emit(driver.TranscriptItem{Kind: kind, Text: text, Delta: delta})
	}
}

func (p *cursorParser) handleToolCall(payload map[string]any, subtype string) bool {
	if subtype != "started" && subtype != "completed" {
		return false
	}
	callID := cursorString(payload, "call_id")
	name, call, ok := cursorNestedToolCall(payload)
	if !ok || callID == "" {
		p.protocolMalformed = true
		return false
	}

	args := call["args"]
	if subtype == "started" {
		p.emit(driver.TranscriptItem{
			Kind:      driver.TranscriptToolCall,
			ToolUseID: callID,
			ToolName:  name,
			Input:     args,
		})
		return true
	}

	result, exists := call["result"]
	if !exists {
		p.protocolMalformed = true
		return false
	}
	item := driver.TranscriptItem{
		Kind:      driver.TranscriptToolResult,
		ToolUseID: callID,
		ToolName:  name,
		Text:      cursorToolResultText(result),
		IsError:   cursorToolResultIsError(result),
		Data:      map[string]any{"result": result},
	}
	p.emit(item)
	return true
}

// cursorNestedToolCall extracts the single documented tool-call variant, for
// example tool_call.readToolCall. Sorting makes malformed multi-variant input
// deterministic; such input is rejected instead of depending on map order.
func cursorNestedToolCall(payload map[string]any) (string, map[string]any, bool) {
	toolCall := cursorObject(payload, "tool_call")
	if len(toolCall) == 0 {
		return "", nil, false
	}
	keys := make([]string, 0, len(toolCall))
	for key := range toolCall {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var name string
	var body map[string]any
	for _, key := range keys {
		candidate, ok := toolCall[key].(map[string]any)
		if !ok {
			continue
		}
		_, hasArgs := candidate["args"]
		_, hasResult := candidate["result"]
		if !hasArgs && !hasResult {
			continue
		}
		if body != nil {
			return "", nil, false
		}
		name, body = key, candidate
	}
	return name, body, body != nil
}

func cursorToolResultText(result any) string {
	if envelope, ok := result.(map[string]any); ok {
		for _, key := range []string{"success", "failure", "error"} {
			value, exists := envelope[key]
			if !exists {
				continue
			}
			if object, ok := value.(map[string]any); ok {
				if content, ok := object["content"].(string); ok {
					return content
				}
			}
			return cursorCompactJSON(value)
		}
	}
	return cursorCompactJSON(result)
}

func cursorToolResultIsError(result any) bool {
	envelope, ok := result.(map[string]any)
	if !ok {
		return false
	}
	_, failure := envelope["failure"]
	_, errored := envelope["error"]
	return failure || errored
}

func cursorCompactJSON(value any) string {
	raw, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	return string(raw)
}

func (p *cursorParser) handleSuccessResult(raw string, payload map[string]any) {
	p.captureSession(payload)
	p.terminalSeen = true
	isError, hasErrorFlag := payload["is_error"].(bool)
	result, hasResult := payload["result"].(string)
	p.terminalSession = cursorString(payload, "session_id")
	if !hasErrorFlag || !hasResult || p.terminalSession == "" {
		// Cursor's documented result.success envelope requires is_error,
		// result, and session_id. A missing or wrongly typed field is not
		// proof of a healthy provider terminal and must never mint a Thread
		// checkpoint merely because the CLI exited zero.
		p.protocolMalformed = true
	}
	p.terminalSuccess = hasErrorFlag && !isError && hasResult && p.terminalSession != ""
	if isError {
		p.errorMessage = "cursor terminal result reported an error"
	}
	p.terminal = &driver.TerminalPayload{
		Event: "result",
		JSON:  append(json.RawMessage(nil), raw...),
	}
	if hasResult {
		p.terminalResult = result
	}
	p.emit(driver.TranscriptItem{
		Kind:    driver.TranscriptResult,
		Subtype: "success",
		IsError: isError,
		Text:    p.terminalResult,
		Data:    map[string]any{"payload": payload},
	})
}

func (p *cursorParser) emitUnknown(raw string, payload map[string]any) {
	p.emit(driver.TranscriptItem{
		Kind: driver.TranscriptSystem,
		Text: raw,
		Data: map[string]any{"payload": payload},
	})
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

// finalSummary is empty because Cursor's documented result field is the full
// assistant response, not a bounded provider summary.
func (p *cursorParser) finalSummary() string { return "" }

func (p *cursorParser) buildOutput() string {
	if len(p.assistantDeltas) != 0 {
		return strings.Join(p.assistantDeltas, "")
	}
	// A successful terminal result is an official full-text fallback for a
	// truncated/missing delta stream. It remains distinct from Summary.
	return p.terminalResult
}

// checkpoint promotes a Cursor session only after a clean process exit and
// an official result.success event carrying its own top-level session_id.
func (p *cursorParser) checkpoint(exitCode int) *driver.Checkpoint {
	if exitCode != 0 || p.protocolMalformed || !p.terminalSeen || !p.terminalSuccess || p.terminalSession == "" {
		return nil
	}
	display := p.displayID
	if display == "" {
		display = p.terminalSession
	}
	return &driver.Checkpoint{
		State: &driver.SessionState{
			ResumeID:  p.terminalSession,
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

func cursorString(payload map[string]any, key string) string {
	value, _ := payload[key].(string)
	return strings.TrimSpace(value)
}

// cursorTopLevelString reads explicit top-level fields from non-protocol
// Cursor documents such as cli-config.json. Protocol parsing above uses
// cursorString with one exact official field name and does not use aliases.
func cursorTopLevelString(payload map[string]any, keys ...string) string {
	for _, key := range keys {
		if value := cursorString(payload, key); value != "" {
			return value
		}
	}
	return ""
}

func cursorObject(payload map[string]any, key string) map[string]any {
	value, _ := payload[key].(map[string]any)
	return value
}
