package codex

import (
	"bytes"
	"encoding/json"
	"strings"
	"sync"
	"time"

	"github.com/agent-dance/agent-adaptor/driver"
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

	sink driver.EventSink

	stdoutLine bytes.Buffer
	stderrLine bytes.Buffer

	transcript    []driver.TranscriptItem
	assistantText []string

	usage              *driver.Usage
	hasUsage           bool
	terminal           *driver.TerminalPayload
	errorMessage       string
	checkpointThreadID string
	threadStarted      bool
	turnCompleted      bool
	terminalFailed     bool
	protocolMalformed  bool
}

func newCodexParser(sink driver.EventSink) *codexParser {
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
		p.emit(driver.TranscriptItem{
			Kind: driver.TranscriptStderr,
			Text: text,
		})
		return
	}

	if !strings.HasPrefix(trimmed, "{") {
		// Codex CLI may print non-JSON stdout banners; treat them as raw
		// stdout transcript entries rather than guessing JSON shape.
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

func (p *codexParser) handlePayload(raw string, payload map[string]any) {
	if p.turnCompleted || p.terminalFailed {
		p.protocolMalformed = true
		return
	}
	kind := codexEventKind(payload)
	switch kind {
	case "thread.started":
		if p.threadStarted {
			p.protocolMalformed = true
			return
		}
		if threadID := codexTopLevelString(payload, "thread_id"); threadID != "" {
			p.threadStarted = true
			p.checkpointThreadID = threadID
			p.emit(driver.TranscriptItem{
				Kind:      driver.TranscriptInit,
				SessionID: threadID,
			})
		} else {
			p.protocolMalformed = true
		}
	case "item.started":
		if !p.threadStarted {
			p.protocolMalformed = true
		}
		item := codexTopLevelObject(payload, "item")
		p.handleItem(item, codexItemStarted)
	case "item.completed":
		if !p.threadStarted {
			p.protocolMalformed = true
		}
		item := codexTopLevelObject(payload, "item")
		p.handleItem(item, codexItemCompleted)
	case "turn.completed":
		if !p.threadStarted {
			p.protocolMalformed = true
		}
		p.turnCompleted = true
		p.handleTerminal(raw, payload, kind)
	case "turn.failed":
		p.terminalFailed = true
		p.handleTerminal(raw, payload, kind)
	case "error":
		p.terminalFailed = true
		p.terminal = &driver.TerminalPayload{Event: kind, JSON: append(json.RawMessage(nil), raw...)}
		message := codexTopLevelString(payload, "message")
		if message == "" {
			message = "codex provider error"
		}
		p.errorMessage = message
		p.emit(driver.TranscriptItem{
			Kind:     driver.TranscriptFailure,
			Text:     message,
			Metadata: map[string]string{"code": "error"},
		})
	default:
		p.emit(driver.TranscriptItem{
			Kind: driver.TranscriptSystem,
			Text: raw,
			Data: map[string]any{"payload": payload},
		})
	}
}

type codexItemPhase uint8

const (
	codexItemStarted codexItemPhase = iota + 1
	codexItemCompleted
)

func (p *codexParser) handleItem(item map[string]any, phase codexItemPhase) {
	if len(item) == 0 {
		p.protocolMalformed = true
		return
	}
	switch codexEventKind(item) {
	case "agent_message":
		// codex exec --json exposes the final assistant value on the completed
		// item. item.started is lifecycle metadata, not a text delta.
		if phase != codexItemCompleted {
			return
		}
		text, ok := item["text"].(string)
		if !ok {
			p.protocolMalformed = true
			return
		}
		p.assistantText = append(p.assistantText, text)
		p.emit(driver.TranscriptItem{
			Kind: driver.TranscriptAssistant,
			Text: text,
		})
	case "reasoning":
		if phase != codexItemCompleted {
			return
		}
		text := codexTopLevelString(item, "text")
		if text == "" {
			return
		}
		p.emit(driver.TranscriptItem{
			Kind: driver.TranscriptThinking,
			Text: text,
		})
	case "command_execution":
		p.handleCommandExecution(item, phase)
	case "file_change":
		p.handleFileChange(item, phase)
	case "mcp_tool_call":
		p.handleMCPToolCall(item, phase)
	case "web_search":
		p.handleWebSearch(item, phase)
	case "dynamic_tool_call":
		p.handleDynamicToolCall(item, phase)
	}
}

func (p *codexParser) handleCommandExecution(item map[string]any, phase codexItemPhase) {
	id := p.codexToolItemID(item)
	if id == "" {
		return
	}
	if phase == codexItemStarted {
		p.emit(driver.TranscriptItem{
			Kind:      driver.TranscriptToolCall,
			ToolUseID: id,
			ToolName:  "shell",
			Input:     codexSelectedValues(item, "command", "cwd"),
		})
		return
	}
	p.emit(driver.TranscriptItem{
		Kind:      driver.TranscriptToolResult,
		ToolUseID: id,
		Text:      codexTopLevelRawString(item, "aggregated_output"),
		IsError:   codexToolFailed(item),
		Data:      codexSelectedValues(item, "status", "exit_code", "duration_ms"),
	})
}

func (p *codexParser) handleFileChange(item map[string]any, phase codexItemPhase) {
	id := p.codexToolItemID(item)
	if id == "" {
		return
	}
	if phase == codexItemStarted {
		p.emit(driver.TranscriptItem{
			Kind:      driver.TranscriptToolCall,
			ToolUseID: id,
			ToolName:  "apply_patch",
			Input:     codexSelectedValues(item, "changes"),
		})
		return
	}
	p.emit(driver.TranscriptItem{
		Kind:      driver.TranscriptToolResult,
		ToolUseID: id,
		IsError:   codexToolFailed(item),
		Data:      codexSelectedValues(item, "status", "changes"),
	})
}

func (p *codexParser) handleMCPToolCall(item map[string]any, phase codexItemPhase) {
	id := p.codexToolItemID(item)
	if id == "" {
		return
	}
	server := codexTopLevelString(item, "server")
	tool := codexTopLevelString(item, "tool")
	name := "mcp"
	if tool != "" {
		name = tool
		if server != "" {
			name = server + "/" + tool
		}
	}
	if phase == codexItemStarted {
		p.emit(driver.TranscriptItem{
			Kind:      driver.TranscriptToolCall,
			ToolUseID: id,
			ToolName:  name,
			Input:     item["arguments"],
		})
		return
	}
	p.emit(driver.TranscriptItem{
		Kind:      driver.TranscriptToolResult,
		ToolUseID: id,
		Text:      codexJSONText(item["result"]),
		IsError:   codexToolFailed(item) || codexHasValue(item["error"]),
		Data:      codexSelectedValues(item, "status", "result", "error", "duration_ms"),
	})
}

func (p *codexParser) handleWebSearch(item map[string]any, phase codexItemPhase) {
	id := p.codexToolItemID(item)
	if id == "" {
		return
	}
	if phase == codexItemStarted {
		p.emit(driver.TranscriptItem{
			Kind:      driver.TranscriptToolCall,
			ToolUseID: id,
			ToolName:  "web_search",
			Input:     codexSelectedValues(item, "query", "action"),
		})
		return
	}
	p.emit(driver.TranscriptItem{
		Kind:      driver.TranscriptToolResult,
		ToolUseID: id,
		Text:      codexJSONText(item["result"]),
		IsError:   codexToolFailed(item) || codexHasValue(item["error"]),
		Data:      codexSelectedValues(item, "status", "action", "result", "error"),
	})
}

func (p *codexParser) handleDynamicToolCall(item map[string]any, phase codexItemPhase) {
	id := p.codexToolItemID(item)
	if id == "" {
		return
	}
	name := codexTopLevelString(item, "tool")
	if name == "" {
		name = "dynamic"
	}
	if phase == codexItemStarted {
		p.emit(driver.TranscriptItem{
			Kind:      driver.TranscriptToolCall,
			ToolUseID: id,
			ToolName:  name,
			Input:     item["arguments"],
		})
		return
	}
	p.emit(driver.TranscriptItem{
		Kind:      driver.TranscriptToolResult,
		ToolUseID: id,
		IsError:   codexToolFailed(item) || codexExplicitFalse(item["success"]),
		Data:      codexSelectedValues(item, "status", "success", "duration_ms"),
	})
}

func (p *codexParser) codexToolItemID(item map[string]any) string {
	id := codexTopLevelString(item, "id")
	if id == "" {
		p.protocolMalformed = true
	}
	return id
}

func codexToolFailed(item map[string]any) bool {
	switch strings.ToLower(codexTopLevelString(item, "status")) {
	case "failed", "error", "declined", "interrupted", "cancelled", "canceled":
		return true
	}
	if exitCode, ok := codexTopLevelInt(item, "exit_code"); ok {
		return exitCode != 0
	}
	return false
}

func codexSelectedValues(item map[string]any, keys ...string) map[string]any {
	selected := make(map[string]any, len(keys))
	for _, key := range keys {
		if value, ok := item[key]; ok && codexHasValue(value) {
			selected[key] = value
		}
	}
	if len(selected) == 0 {
		return nil
	}
	return selected
}

func codexJSONText(value any) string {
	if !codexHasValue(value) {
		return ""
	}
	if text, ok := value.(string); ok {
		return text
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	return string(raw)
}

func codexHasValue(value any) bool {
	switch v := value.(type) {
	case nil:
		return false
	case string:
		return strings.TrimSpace(v) != ""
	case []any:
		return len(v) > 0
	case map[string]any:
		return len(v) > 0
	default:
		return true
	}
}

func codexExplicitFalse(value any) bool {
	v, ok := value.(bool)
	return ok && !v
}

func (p *codexParser) handleTerminal(raw string, payload map[string]any, kind string) {
	p.terminal = &driver.TerminalPayload{
		Event: kind,
		JSON:  append(json.RawMessage(nil), raw...),
	}

	usage := codexTopLevelObject(payload, "usage")
	if usage != nil {
		input, okInput := codexTopLevelInt(usage, "input_tokens")
		cached, okCached := codexTopLevelInt(usage, "cached_input_tokens")
		output, okOutput := codexTopLevelInt(usage, "output_tokens")
		if okInput || okCached || okOutput {
			p.hasUsage = true
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
	p.emit(driver.TranscriptItem{
		Kind:    driver.TranscriptResult,
		Subtype: kind,
		IsError: isError,
		Usage:   p.usage,
		Data:    map[string]any{"payload": payload},
	})
}

func (p *codexParser) emit(item driver.TranscriptItem) {
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

// finalSummary is empty because the current codex exec JSONL terminal does not
// define a bounded summary field. Assistant output is never reused as Summary
// because it may be arbitrarily large.
func (p *codexParser) finalSummary() string {
	return ""
}

// buildOutput returns only the last officially completed agent_message. Codex
// may emit intermediate agent_message items while it works; those remain in
// Transcript, but Result.Text is the provider's final assistant-facing value.
func (p *codexParser) buildOutput() string {
	if len(p.assistantText) == 0 {
		return ""
	}
	return p.assistantText[len(p.assistantText)-1]
}

func (p *codexParser) checkpoint(exitCode int) *driver.Checkpoint {
	if exitCode != 0 || p.protocolMalformed || p.terminalFailed || !p.threadStarted || !p.turnCompleted || p.checkpointThreadID == "" {
		return nil
	}
	return &driver.Checkpoint{
		State: &driver.SessionState{
			ResumeID:  p.checkpointThreadID,
			DisplayID: p.checkpointThreadID,
		},
		Valid: true,
	}
}

func (p *codexParser) checkpointForOutcome(exitCode int, signal string, timedOut bool, failure *driver.RunFailure) *driver.Checkpoint {
	if signal != "" || timedOut || failure != nil {
		return nil
	}
	return p.checkpoint(exitCode)
}

// nativeStructuredOutputForOutcome returns the last officially completed
// agent_message as the Codex native structured-output candidate. The parser
// deliberately does not claim schema validity: the core invocation pipeline
// canonicalizes and validates RawJSON against the host's requested schema.
// A candidate is exposed only after the official turn.completed terminal and
// a clean process/business outcome.
func (p *codexParser) nativeStructuredOutputForOutcome(exitCode int, signal string, timedOut bool, failure *driver.RunFailure) *driver.StructuredOutput {
	if exitCode != 0 || signal != "" || timedOut || failure != nil || p.protocolMalformed || p.terminalFailed || !p.turnCompleted || p.terminal == nil || p.terminal.Event != "turn.completed" || len(p.assistantText) == 0 {
		return nil
	}
	text := p.assistantText[len(p.assistantText)-1]
	if strings.TrimSpace(text) == "" {
		return nil
	}
	return &driver.StructuredOutput{
		Format:  driver.OutputFormatJSONSchema,
		Source:  driver.StructuredOutputSourceNative,
		RawJSON: append(json.RawMessage(nil), text...),
	}
}

// snapshotCodexStdout feeds a complete stdout string through the parser and
// returns its final state. It lets batch consumers and unit tests operate on a
// complete stdout dump without giving up the streaming parser as the single
// source of truth.
func snapshotCodexStdout(stdout string) *codexParser {
	p := newCodexParser(nil)
	_ = p.onChunk("stdout", []byte(stdout), time.Now().UTC())
	p.finalize()
	return p
}

func codexEventKind(payload map[string]any) string {
	return codexTopLevelString(payload, "type")
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

func codexTopLevelRawString(payload map[string]any, key string) string {
	value, _ := payload[key].(string)
	return value
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
