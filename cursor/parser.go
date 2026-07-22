package cursor

import (
	"bytes"
	"encoding/json"
	"sort"
	"strings"
	"sync"
	"time"

	agentadaptor "github.com/agent-dance/agent-adaptor"
)

// cursorPendingSubagent tracks a taskToolCall that has been started but not
// yet completed. It is keyed by call_id in cursorParser.pendingSubagents.
type cursorPendingSubagent struct {
	callID      string
	argsAgentID string // agentId from args (may differ from result.success.agentId)
	description string
}

// cursorParser consumes raw stdout/stderr chunks from a Cursor Agent CLI run
// (stream-json) and produces the normalized outputs required by the adapter
// contract.
type cursorParser struct {
	mu sync.Mutex

	sink  agentadaptor.EventSink
	runID string

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

	// pendingSubagents tracks taskToolCall invocations between "started" and
	// "completed" subtype events. Key is call_id.
	pendingSubagents   map[string]cursorPendingSubagent
	closedSubagents    map[string]struct{}
	runTerminalEmitted bool
}

var cursorCheckpointEvents = map[string]struct{}{
	"result":          {},
	"run.completed":   {},
	"session":         {},
	"session.updated": {},
}

func newCursorParser(sink agentadaptor.EventSink) *cursorParser {
	return newCursorParserWithRunID(sink, "")
}

func newCursorParserWithRunID(sink agentadaptor.EventSink, runID string) *cursorParser {
	return &cursorParser{
		sink:             sink,
		runID:            runID,
		pendingSubagents: make(map[string]cursorPendingSubagent),
		closedSubagents:  make(map[string]struct{}),
	}
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
	p.flushPendingSubagentsLocked()
}

// finishRun emits a terminal stream event when the CLI exits without a
// provider terminal frame. finalize must run first so pending subagents close
// before the parent run terminal.
func (p *cursorParser) finishRun(exitCode int) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.flushPendingSubagentsLocked()
	if exitCode == 0 {
		p.emitRunTerminalLocked(agentadaptor.StreamRunFinished)
		return
	}
	p.emitRunTerminalLocked(agentadaptor.StreamRunError)
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
		if nested, ok := payload["tool_call"].(map[string]any); ok && len(nested) > 0 {
			// Discriminated tool_call with nested union: e.g. {"taskToolCall": {...}}
			subtype := strings.ToLower(cursorTopLevelString(payload, "subtype"))
			callID := cursorTopLevelString(payload, "call_id", "id")
			p.handleDiscriminatedToolCall(subtype, callID, nested)
			return
		}
		// Flat tool_call (legacy/other shapes): name and input at top level.
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
		p.flushPendingSubagentsLocked()
		p.handleResult(raw, payload, eventType)
		p.emitRunTerminalLocked(agentadaptor.StreamRunFinished)
	case "error", "run.failed":
		p.flushPendingSubagentsLocked()
		message := cursorTopLevelString(payload, "message", "error")
		if message != "" {
			p.errorMessage = message
		}
		p.emit(agentadaptor.TranscriptItem{
			Kind:     agentadaptor.TranscriptFailure,
			Text:     message,
			Metadata: map[string]string{"code": "error"},
		})
		p.emitRunTerminalLocked(agentadaptor.StreamRunError)
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

// handleDiscriminatedToolCall routes nested discriminated tool_call events
// such as {"taskToolCall": {...}} to their specific handlers. Callers must
// hold p.mu.
func (p *cursorParser) handleDiscriminatedToolCall(subtype, callID string, nested map[string]any) {
	if tc, ok := nested["taskToolCall"].(map[string]any); ok {
		p.handleTaskToolCall(subtype, callID, tc)
		return
	}
	// Unknown discriminated variant: emit a system transcript entry so the
	// event is not silently dropped.
	p.emit(agentadaptor.TranscriptItem{
		Kind: agentadaptor.TranscriptSystem,
		Data: map[string]any{"tool_call": nested, "subtype": subtype, "call_id": callID},
	})
}

// handleTaskToolCall processes the Cursor-native taskToolCall discriminated
// tool event (parent stream-json subtype="started" or "completed").
//
// Mapping (§8.4.2):
//   - started  → TranscriptToolCall + StreamToolCallStart + StreamSubagentStart
//   - completed → TranscriptToolResult + StreamSubagentEnd + StreamToolCallEnd
//   - StreamToolCallResult
//
// Scope-ID stability: args.agentId is used as the SubagentRef.ID anchor for
// both start and end so that the scope ID never changes mid-lifecycle even when
// result.success.agentId differs. If args.agentId is absent, call_id serves as
// the fallback anchor.
func (p *cursorParser) handleTaskToolCall(subtype, callID string, tc map[string]any) {
	switch subtype {
	case "started":
		p.handleTaskToolCallStarted(callID, tc)
	case "completed":
		p.handleTaskToolCallCompleted(callID, tc)
	default:
		// Defensive: unknown subtype — record as system event.
		p.emit(agentadaptor.TranscriptItem{
			Kind: agentadaptor.TranscriptSystem,
			Data: map[string]any{"taskToolCall": tc, "subtype": subtype, "call_id": callID},
		})
	}
}

func (p *cursorParser) handleTaskToolCallStarted(callID string, tc map[string]any) {
	if _, closed := p.closedSubagents[callID]; closed {
		return
	}
	if _, pending := p.pendingSubagents[callID]; pending {
		return
	}

	args, _ := tc["args"].(map[string]any)
	argsAgentID := cursorTopLevelString(args, "agentId")
	description := cursorTopLevelString(args, "description")

	scopeID := argsAgentID
	if scopeID == "" {
		scopeID = callID
	}

	p.pendingSubagents[callID] = cursorPendingSubagent{
		callID:      callID,
		argsAgentID: scopeID,
		description: description,
	}

	// Transcript: parent tool call boundary.
	p.emit(agentadaptor.TranscriptItem{
		Kind:      agentadaptor.TranscriptToolCall,
		ToolName:  "Task",
		ToolUseID: callID,
		Input:     args,
	})

	// Streaming: parent tool_call.start + subagent.start.
	p.emitStream(agentadaptor.StreamPayload{
		Kind:       agentadaptor.StreamToolCallStart,
		ToolCallID: callID,
		Name:       "Task",
		Args:       cursorAnyToStringMap(args),
	})
	p.emitStream(agentadaptor.StreamPayload{
		Kind: agentadaptor.StreamSubagentStart,
		Subagent: &agentadaptor.SubagentRef{
			ID:         scopeID,
			Name:       description,
			Kind:       "native",
			ToolCallID: callID,
		},
	})
}

func (p *cursorParser) handleTaskToolCallCompleted(callID string, tc map[string]any) {
	if _, closed := p.closedSubagents[callID]; closed {
		return
	}

	pending, hasPending := p.pendingSubagents[callID]
	if hasPending {
		delete(p.pendingSubagents, callID)
	}

	args, _ := tc["args"].(map[string]any)
	result, _ := tc["result"].(map[string]any)
	success, _ := result["success"].(map[string]any)

	var resultAgentID, durationMs string
	var isBackground bool
	var conversationText string

	if success != nil {
		resultAgentID = cursorTopLevelString(success, "agentId")
		durationMs = cursorTopLevelString(success, "durationMs")
		isBackground, _ = success["isBackground"].(bool)
		conversationText = cursorJoinConversationSteps(success["conversationSteps"])
	}

	scopeID := ""
	description := cursorTopLevelString(args, "description")
	if hasPending {
		scopeID = pending.argsAgentID
		description = pending.description
	} else {
		scopeID = resultAgentID
		if scopeID == "" {
			scopeID = cursorTopLevelString(args, "agentId")
		}
	}
	if scopeID == "" {
		scopeID = callID
	}
	p.closedSubagents[callID] = struct{}{}

	// A completed-only frame is a valid degraded Cursor stream shape. Rebuild
	// the missing parent/subagent starts before emitting either end so every
	// observed scope still satisfies start-before-end.
	if !hasPending && scopeID != "" {
		p.emit(agentadaptor.TranscriptItem{
			Kind:      agentadaptor.TranscriptToolCall,
			ToolName:  "Task",
			ToolUseID: callID,
			Input:     args,
		})
		p.emitStream(agentadaptor.StreamPayload{
			Kind:       agentadaptor.StreamToolCallStart,
			ToolCallID: callID,
			Name:       "Task",
			Args:       cursorAnyToStringMap(args),
		})
		p.emitStream(agentadaptor.StreamPayload{
			Kind: agentadaptor.StreamSubagentStart,
			Subagent: &agentadaptor.SubagentRef{
				ID:         scopeID,
				Name:       description,
				Kind:       "native",
				ToolCallID: callID,
			},
		})
	}

	// Transcript: parent tool result carrying final subagent text.
	p.emit(agentadaptor.TranscriptItem{
		Kind:      agentadaptor.TranscriptToolResult,
		ToolUseID: callID,
		Text:      conversationText,
	})

	// Streaming: subagent.end + parent tool_call.result.
	raw := map[string]any{
		"result_agent_id": resultAgentID,
		"duration_ms":     durationMs,
		"is_background":   isBackground,
	}
	if scopeID != "" {
		p.emitStream(agentadaptor.StreamPayload{
			Kind: agentadaptor.StreamSubagentEnd,
			Subagent: &agentadaptor.SubagentRef{
				ID:         scopeID,
				Name:       description,
				Kind:       "native",
				ToolCallID: callID,
			},
			Result: map[string]any{"text": conversationText, "status": "completed"},
			Raw:    raw,
		})
	}
	p.emitStream(agentadaptor.StreamPayload{
		Kind:       agentadaptor.StreamToolCallEnd,
		ToolCallID: callID,
	})
	p.emitStream(agentadaptor.StreamPayload{
		Kind:       agentadaptor.StreamToolCallResult,
		ToolCallID: callID,
		Result:     map[string]any{"text": conversationText},
	})
}

// emitStream forwards a StreamPayload through the EventSink. It sets RunID
// and ThreadID when they are not already populated. Callers must hold p.mu.
func (p *cursorParser) emitStream(pl agentadaptor.StreamPayload) {
	if p.sink == nil {
		return
	}
	if pl.RunID == "" {
		pl.RunID = p.runID
	}
	if pl.ThreadID == "" {
		pl.ThreadID = p.sessionID
	}
	_ = p.sink.EmitStream(pl)
}

// flushPendingSubagentsLocked closes every started taskToolCall that did not
// receive a completed frame. Deleting before emission makes repeated finalize
// calls idempotent. Callers must hold p.mu.
func (p *cursorParser) flushPendingSubagentsLocked() {
	if len(p.pendingSubagents) == 0 {
		return
	}

	callIDs := make([]string, 0, len(p.pendingSubagents))
	for callID := range p.pendingSubagents {
		callIDs = append(callIDs, callID)
	}
	sort.Strings(callIDs)

	for _, callID := range callIDs {
		pending := p.pendingSubagents[callID]
		delete(p.pendingSubagents, callID)
		p.closedSubagents[callID] = struct{}{}
		p.emitStream(agentadaptor.StreamPayload{
			Kind: agentadaptor.StreamSubagentEnd,
			Subagent: &agentadaptor.SubagentRef{
				ID:         pending.argsAgentID,
				Name:       pending.description,
				Kind:       "native",
				ToolCallID: callID,
			},
			Result: map[string]any{
				"status":    "failed",
				"synthetic": true,
			},
		})
	}
}

// emitRunTerminalLocked emits at most one parent run terminal payload. Pending
// subagents must be flushed by the caller before invoking this method.
func (p *cursorParser) emitRunTerminalLocked(kind agentadaptor.StreamKind) {
	if p.runTerminalEmitted {
		return
	}
	p.runTerminalEmitted = true

	payload := agentadaptor.StreamPayload{
		Kind:  kind,
		Usage: p.usage,
	}
	if kind == agentadaptor.StreamRunError {
		message := p.errorMessage
		if message == "" {
			message = "cursor agent exited before emitting a terminal result"
		}
		payload.Error = &agentadaptor.RunFailure{
			Code:    agentadaptor.FailureAgentError,
			Message: message,
		}
	}
	p.emitStream(payload)
}

// cursorJoinConversationSteps extracts and concatenates assistantMessage.text
// values from a Cursor conversationSteps array. Non-assistant steps and
// unknown shapes are skipped.
func cursorJoinConversationSteps(steps any) string {
	arr, _ := steps.([]any)
	if len(arr) == 0 {
		return ""
	}
	texts := make([]string, 0, len(arr))
	for _, raw := range arr {
		step, _ := raw.(map[string]any)
		if step == nil {
			continue
		}
		if msg, _ := step["assistantMessage"].(map[string]any); msg != nil {
			if text := cursorTopLevelString(msg, "text"); text != "" {
				texts = append(texts, text)
			}
		}
	}
	return strings.Join(texts, "\n")
}

// cursorAnyToStringMap converts a map[string]any to map[string]any safely,
// returning nil when the input is nil.
func cursorAnyToStringMap(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
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
