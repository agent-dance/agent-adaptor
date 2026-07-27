package claude

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	agentadaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/driver"
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

	sessionID         string
	displayID         string
	summary           string // last assistant text; used only as summary fallback
	terminalSummary   string // authoritative summary from terminal result events
	usage             *agentadaptor.Usage
	cost              *float64
	resultFinal       map[string]any
	structuredOutput  *agentadaptor.StructuredOutput
	errorMessage      string
	terminalSeen      bool
	terminalSuccess   bool
	protocolMalformed bool

	stream       *streamingState
	deltaBuffers map[string]*strings.Builder // messageID -> streamed text (cancel/crash fallback)

	runID  string
	policy agentadaptor.HumanDecisionPolicy

	// pendingHITL tracks tool_use frames that belong to the claudeInteractiveTools
	// whitelist so tool_result can be resolved into HITL decisions. Keyed by
	// tool_use_id.
	pendingHITL map[string]*pendingHITL

	// decisionSeq allocates RequestID suffixes for HITL events the parser
	// synthesizes locally. A separate sink-level allocator (dualSink.decSeq)
	// owns the canonical counter when adapters call sink.RequestDecision —
	// this one is only used when the parser recognizes a historical
	// locally-resolved tool_use (§5.1 Phase 1).
	decisionSeq int

	// pendingFailure is the HITL-originated failure recorded when the CLI
	// reports a rejected plan. The driver layer reads it in Run() to set
	// DriverRunResult.Failure.
	pendingFailure *agentadaptor.RunFailure

	// -------------------------------------------------------------------
	// Phase 3 interactive mode.
	// -------------------------------------------------------------------
	//
	// When interactive == true the driver has started the CLI in
	// --input-format stream-json mode with a long-lived stdin. Decisions are
	// driven by Claude's native control_request(can_use_tool) frames, not by
	// synthesizing user tool_result messages.
	interactive bool
	// interactiveCtx and interactiveSink are snapshots captured by
	// setInteractive so the per-chunk parser callback can issue blocking
	// sink.RequestDecision calls without needing the driver to thread a
	// context through every event handler.
	interactiveCtx  context.Context
	interactiveSink agentadaptor.DecisionCapableSink
	stdin           InteractiveStdin

	// interactiveTools captures tool_use frames so streaming mode can still
	// expose arg deltas / tool IDs while interactive mode is active. Claude's
	// actual decision prompt arrives later as control_request.
	interactiveTools map[int]*interactiveToolUse
	// interactiveControl tracks tool_use IDs already handled through
	// control_request so any fallback logic can avoid double-resolving them.
	interactiveControl map[string]struct{}
}

// InteractiveStdin is the API the parser uses to inject control_response
// frames back to the CLI when in Phase 3 interactive mode, and to signal
// "no more host input" when a model turn is complete. It is satisfied by
// clihelper.StdinController.
type InteractiveStdin interface {
	Write(frame []byte) error
	// Close signals no further frames (EOF on the subprocess stdin). The
	// trpc stream-json driver often keeps the child alive waiting for
	// another NDJSON line; closing is required after the last assistant
	// message of a one-shot run so the CLI can emit the terminal
	// type:result and exit, which unblocks the host HTTP /agent request.
	Close() error
}

// interactiveToolUse is the parser-local state for a tool_use frame while
// its argument deltas are still arriving.
type interactiveToolUse struct {
	ToolName  string
	ToolUseID string
	StartedAt time.Time
	// argsBuf accumulates input_json_delta.partial_json fragments so
	// content_block_stop sees the complete JSON input.
	argsBuf strings.Builder
}

// pendingHITL captures a whitelisted tool_use frame while its tool_result is
// still in flight.
type pendingHITL struct {
	Kind      agentadaptor.HumanDecisionKind
	Source    string
	ToolUseID string
	StartedAt time.Time
	Input     map[string]any

	// Derived fields populated from input when available.
	Plan    string
	Prompt  string
	Schema  map[string]any
	Choices []agentadaptor.DecisionChoice
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
		p.protocolMalformed = true
		p.emit(agentadaptor.TranscriptItem{
			Kind: agentadaptor.TranscriptStdout,
			Text: text,
		})
		return
	}

	p.handlePayload(trimmed, payload)
}

func (p *claudeParser) handlePayload(raw string, payload map[string]any) {
	if p.terminalSeen {
		p.protocolMalformed = true
		return
	}
	eventType := strings.ToLower(claudeTopLevelString(payload, "type", "event", "kind"))
	subtype := strings.ToLower(claudeTopLevelString(payload, "subtype"))

	// Phase 3 interactive mode: the CLI echoes every user frame we inject
	// back with isReplay:true as an ack signal (via --replay-user-messages).
	// We must not re-process these as real user messages or they would
	// pollute the transcript with duplicate tool_result entries.
	if isReplay, _ := payload["isReplay"].(bool); isReplay {
		p.maybeCaptureSession(payload)
		return
	}

	if claudeFormalEvent(eventType) {
		p.maybeCaptureSession(payload)
	}

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
	case "control_request":
		if p.interactive && p.handleInteractiveControlRequest(payload) {
			return
		}
		p.emit(agentadaptor.TranscriptItem{
			Kind:    agentadaptor.TranscriptSystem,
			Text:    raw,
			Subtype: eventType,
			Data:    map[string]any{"payload": payload},
		})
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
		p.terminalSeen = true
		p.terminalSuccess = false
		message := claudeTopLevelString(payload, "message", "error")
		if message == "" {
			message = "claude provider error"
		}
		p.errorMessage = message
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
			if kind, interactive := claudeInteractiveTools[name]; interactive && id != "" {
				p.registerPendingHITL(id, name, kind, block["input"])
			}
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
			// Resolve any pending HITL tool_use_id against this tool_result.
			p.resolveHITLOnToolResult(id, text, isError)
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
	p.terminalSeen = true
	isErrorFlag, _ := payload["is_error"].(bool)
	p.terminalSuccess = subtype == "success" && !isErrorFlag

	var decoded map[string]any
	if err := json.Unmarshal([]byte(raw), &decoded); err == nil {
		p.resultFinal = decoded
	}

	if resultText := claudeTopLevelString(payload, "result", "summary"); resultText != "" {
		p.terminalSummary = resultText
	}
	if raw, ok := claudeStructuredJSONFromResult(payload); ok {
		p.structuredOutput = &agentadaptor.StructuredOutput{
			Format:  agentadaptor.OutputFormatJSONSchema,
			Source:  agentadaptor.StructuredOutputSourceNative,
			RawJSON: raw,
			Valid:   true,
		}
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

	isError := !p.terminalSuccess
	if isError {
		if message := claudeTopLevelString(payload, "message", "result"); message != "" {
			p.errorMessage = message
		} else if p.errorMessage == "" {
			p.errorMessage = "claude terminal result did not report success"
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

func claudeFormalEvent(eventType string) bool {
	switch eventType {
	case "system", "stream_event", "control_request", "assistant", "user", "result", "error", "permission_request":
		return true
	default:
		return false
	}
}

func claudeStructuredJSONFromResult(payload map[string]any) (json.RawMessage, bool) {
	for _, key := range []string{"structured_output", "structuredOutput"} {
		raw, ok := payload[key]
		if !ok {
			continue
		}
		if msg, ok := claudeJSONRawMessageFromValue(raw); ok {
			return msg, true
		}
	}
	return nil, false
}

func claudeJSONRawMessageFromValue(raw any) (json.RawMessage, bool) {
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

// checkpoint promotes a Claude session only after both layers of the
// checkpoint safety contract are satisfied: the process exited cleanly and
// the official stream-json protocol delivered a successful terminal result
// carrying a top-level session_id. An init frame alone is never sufficient.
func (p *claudeParser) checkpoint(exitCode int) *agentadaptor.DriverCheckpoint {
	if exitCode != 0 || p.protocolMalformed || !p.terminalSeen || !p.terminalSuccess || p.sessionID == "" {
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

// checkpointForOutcome adds the process signal/timeout and structured
// provider-failure gates that are only known by the Driver after the process
// helper returns.
func (p *claudeParser) checkpointForOutcome(exitCode int, signal string, timedOut bool, failure *agentadaptor.RunFailure) *agentadaptor.DriverCheckpoint {
	if signal != "" || timedOut || failure != nil {
		return nil
	}
	return p.checkpoint(exitCode)
}

func snapshotClaudeStdout(stdout string) *claudeParser {
	p := newClaudeParser(nil)
	_ = p.onChunk("stdout", []byte(stdout), time.Now().UTC())
	p.finalize()
	return p
}

// parseCheckpoint is the snapshot compatibility entry point. It deliberately
// delegates to the same formal-protocol parser and safety gate as live runs.
func parseCheckpoint(stdout string, exitCode int) *agentadaptor.DriverCheckpoint {
	return snapshotClaudeStdout(stdout).checkpoint(exitCode)
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

// -----------------------------------------------------------------------------
// HITL v2 hooks (see docs/workstream-hitl-v2.md §5.1).
// -----------------------------------------------------------------------------

// setHITLContext wires the policy + run id the streaming driver resolved for
// this run. Called by the driver before the CLI pipeline starts so pending
// HITL events carry authoritative Deadline / Source values.
func (p *claudeParser) setHITLContext(runID string, policy agentadaptor.HumanDecisionPolicy) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.runID = runID
	p.policy = policy
}

// enableInteractive puts the parser into Phase 3 stream-json bidirectional
// mode. The driver must have started the CLI with --input-format stream-json
// and provide a writable stdin. sink MUST implement DecisionCapableSink
// (otherwise the parser cannot call RequestDecision and interactive mode is
// meaningless).
//
// After enableInteractive, the parser's observation of tool_use frames
// switches from "wait for tool_result and emit observational HITL" (Phase 1)
// to "block on RequestDecision, answer control_request via stdin" (Phase 3).
func (p *claudeParser) enableInteractive(ctx context.Context, sink agentadaptor.DecisionCapableSink, stdin InteractiveStdin) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.interactive = true
	p.interactiveCtx = ctx
	p.interactiveSink = sink
	p.stdin = stdin
	p.interactiveTools = map[int]*interactiveToolUse{}
	p.interactiveControl = map[string]struct{}{}
}

// onAssistantMessageStop is invoked from the streaming state when the
// Anthropic stream_event sequence emits message_stop, after the last
// message_delta (which set stop_reason). In Phase 3 interactive mode the
// CLI will otherwise block reading stdin: closing stdin after a non
// tool_use turn lets the process flush type:result and exit so the
// host's /agent run can end and the UI can leave the busy state.
func (p *claudeParser) onAssistantMessageStop(stopReason string) {
	if !p.interactive || p.stdin == nil {
		return
	}
	// A tool_use stop means the model is waiting for a control_request
	// response; keep stdin open.
	if stopReason == "" || stopReason == "tool_use" {
		return
	}
	stdin := p.stdin
	_ = stdin.Close()
	// One-shot per run: avoid a duplicate message_stop re-closing.
	p.stdin = nil
}

// registerPendingHITL records an interactive tool_use frame so the parser can
// pair it with a subsequent tool_result. Must be called from within
// handleAssistantMessage / handleContentBlockStart where p.mu is already held.
func (p *claudeParser) registerPendingHITL(toolUseID, toolName string, kind agentadaptor.HumanDecisionKind, input any) {
	// Phase 3 interactive mode handles tool_use via interactiveOnToolUseStart
	// on the streaming path; the batch handleAssistantMessage path would
	// double-register and pollute pendingHITL. Skip.
	if p.interactive {
		return
	}
	if p.pendingHITL == nil {
		p.pendingHITL = map[string]*pendingHITL{}
	}
	pending := &pendingHITL{
		Kind:      kind,
		Source:    sourceForTool(toolName),
		ToolUseID: toolUseID,
		StartedAt: time.Now().UTC(),
	}
	if m, ok := input.(map[string]any); ok {
		pending.Input = m
		if plan, ok := m["plan"].(string); ok {
			pending.Plan = plan
		}
		if prompt, ok := m["prompt"].(string); ok {
			pending.Prompt = prompt
		} else if question, ok := m["question"].(string); ok {
			pending.Prompt = question
		}
		if schema, ok := m["schema"].(map[string]any); ok {
			pending.Schema = schema
		}
		if choices, ok := m["choices"].([]any); ok {
			pending.Choices = decodeChoices(choices)
		}
	}
	p.pendingHITL[toolUseID] = pending
}

// resolveHITLOnToolResult pairs a tool_result frame with its pending HITL
// tool_use. Must be called with p.mu held.
//
// Not every `is_error:true` tool_result is a HITL rejection. Claude's CLI
// also returns `is_error:true` + <tool_use_error> when it rejects the
// model's tool call on schema / validation grounds (e.g. AskUserQuestion
// with more than 4 options per question). Those are model-authored bugs
// that the CLI surfaces before any UI is displayed; routing them through
// the HITL channel as "user rejected" would mis-attribute the failure and
// pollute the audit trail. The interpretation step distinguishes these from
// real decisions and, when the result is a pure tool error, drops the HITL
// emission entirely. The normal tool_call.result stream still carries the
// error so the host can render it as a plain tool_call card.
func (p *claudeParser) resolveHITLOnToolResult(toolUseID, content string, isError bool) {
	if p.pendingHITL == nil {
		return
	}
	// In interactive mode the parser has already resolved whitelisted
	// tool_use via sink.RequestDecision + synthesized tool_result. Skip the
	// observational Phase 1 path entirely so we don't double-emit HITL
	// events or record a spurious pendingFailure.
	if p.interactive {
		return
	}
	pending, ok := p.pendingHITL[toolUseID]
	if !ok {
		return
	}
	delete(p.pendingHITL, toolUseID)

	decision, isDecision := interpretClaudeToolResult(content, isError)
	if !isDecision {
		// CLI validation error, not a human decision. Do not emit
		// StreamHITLRequested/Resolved and do not record a pendingFailure.
		return
	}

	p.decisionSeq++
	effective := driver.EffectiveHumanDecisionPolicy(p.policy)
	createdAt := pending.StartedAt
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	deadline := createdAt.Add(effective.Timeout)
	requestID := fmt.Sprintf("%s-dec-claude-%d", p.runID, p.decisionSeq)

	payloadMap := pending.payloadAsMap()

	requestedAt := time.Now().UTC()
	if p.sink != nil {
		requested := agentadaptor.StreamPayload{
			Kind:     agentadaptor.StreamHITLRequested,
			RunID:    p.runID,
			ThreadID: p.sessionID,
			Name:     pending.Source,
			HITLRequested: &agentadaptor.HITLRequestedPayload{
				RequestID:    requestID,
				Kind:         pending.Kind,
				Source:       pending.Source,
				ToolCallID:   pending.ToolUseID,
				Prompt:       pending.Prompt,
				Payload:      payloadMap,
				Choices:      append([]agentadaptor.DecisionChoice(nil), pending.Choices...),
				CreatedAt:    createdAt,
				Deadline:     deadline,
				RetryAttempt: 0,
			},
		}
		_ = p.sink.EmitStream(requested)
	}

	if p.sink != nil {
		resolvedAt := time.Now().UTC()
		resolved := agentadaptor.StreamPayload{
			Kind:     agentadaptor.StreamHITLResolved,
			RunID:    p.runID,
			ThreadID: p.sessionID,
			Name:     pending.Source,
			HITLResolved: &agentadaptor.HITLResolvedPayload{
				RequestID:    requestID,
				Kind:         pending.Kind,
				Source:       pending.Source,
				RetryAttempt: 0,
				Result:       decision,
				ResolvedAt:   resolvedAt,
				Latency:      resolvedAt.Sub(requestedAt),
			},
		}
		_ = p.sink.EmitStream(resolved)
	}

	if decision == agentadaptor.DecisionRejected {
		if effective.OnReject == agentadaptor.FailureAbort || effective.OnReject == agentadaptor.FailureActionUnset {
			// Record a structured failure so the driver surfaces it on
			// DriverRunResult.Failure. The run continues locally because the
			// CLI already produced a reject tool_result, but the host sees
			// the failure via RunResult.Failure.
			snapshot := &agentadaptor.DecisionRequest{
				RequestID:  requestID,
				RunID:      p.runID,
				ThreadID:   p.sessionID,
				Kind:       pending.Kind,
				Source:     pending.Source,
				ToolCallID: pending.ToolUseID,
				Prompt:     pending.Prompt,
				Payload:    payloadMap,
				Choices:    append([]agentadaptor.DecisionChoice(nil), pending.Choices...),
				CreatedAt:  createdAt,
				Deadline:   deadline,
			}
			msg := hitlFailureMessage(pending.Kind)
			p.pendingFailure = &agentadaptor.RunFailure{
				Code:    agentadaptor.FailureReject,
				Message: msg,
				HumanDecision: &agentadaptor.HumanDecisionFailure{
					Kind:     pending.Kind,
					Source:   pending.Source,
					Decision: agentadaptor.DecisionRejected,
					Request:  snapshot,
					Attempts: 1,
				},
			}
		}
	}
}

// payloadAsMap flattens the pending HITL payload into the canonical
// map shape consumed by bridges (see §6.1 schema).
func (p *pendingHITL) payloadAsMap() map[string]any {
	if len(p.Input) == 0 && p.Plan == "" && p.Prompt == "" && p.Schema == nil && len(p.Choices) == 0 {
		return nil
	}
	out := map[string]any{}
	for k, v := range p.Input {
		out[k] = v
	}
	if p.Plan != "" {
		out["plan"] = p.Plan
	}
	if p.Prompt != "" && out["prompt"] == nil {
		out["prompt"] = p.Prompt
	}
	if p.Schema != nil {
		out["schema"] = p.Schema
	}
	return out
}

func decodeChoices(raw []any) []agentadaptor.DecisionChoice {
	if len(raw) == 0 {
		return nil
	}
	out := make([]agentadaptor.DecisionChoice, 0, len(raw))
	for _, entry := range raw {
		m, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		choice := agentadaptor.DecisionChoice{}
		if v, ok := m["key"].(string); ok {
			choice.Key = v
		} else if v, ok := m["value"].(string); ok {
			choice.Key = v
		}
		if v, ok := m["label"].(string); ok {
			choice.Label = v
		}
		if v, ok := m["description"].(string); ok {
			choice.Description = v
		}
		out = append(out, choice)
	}
	return out
}

func hitlFailureMessage(kind agentadaptor.HumanDecisionKind) string {
	switch kind {
	case agentadaptor.HumanDecisionPlanReview:
		return "Claude Plan Mode was not approved; no file changes applied."
	case agentadaptor.HumanDecisionPermission:
		return "Claude tool permission was denied by the user."
	case agentadaptor.HumanDecisionQuestion:
		return "Claude asked the user a question and the request was rejected."
	}
	return "Claude human-in-the-loop decision was rejected."
}

// -----------------------------------------------------------------------------
// Phase 3 interactive state machine.
// -----------------------------------------------------------------------------

// interactiveOnToolUseStart registers a tool_use block so streaming mode can
// correlate later arg deltas with the tool ID. Decisions themselves are
// resolved from control_request, not from the tool_use stop.
func (p *claudeParser) interactiveOnToolUseStart(idx int, name, toolUseID string) {
	if !p.interactive || toolUseID == "" {
		return
	}
	if p.interactiveTools == nil {
		p.interactiveTools = map[int]*interactiveToolUse{}
	}
	p.interactiveTools[idx] = &interactiveToolUse{
		ToolName:  name,
		ToolUseID: toolUseID,
		StartedAt: time.Now().UTC(),
	}
}

// interactiveOnToolUseDelta accumulates input_json_delta.partial_json
// fragments.
func (p *claudeParser) interactiveOnToolUseDelta(idx int, partial string) {
	if !p.interactive {
		return
	}
	tool, ok := p.interactiveTools[idx]
	if !ok || partial == "" {
		return
	}
	tool.argsBuf.WriteString(partial)
}

// interactiveOnToolUseStop finalizes local bookkeeping for a streamed tool_use
// block. In Claude's stream-json protocol the actual decision prompt is
// delivered separately as control_request(can_use_tool), so there is
// intentionally no stdin write here.
func (p *claudeParser) interactiveOnToolUseStop(idx int) {
	if !p.interactive {
		return
	}
	tool, ok := p.interactiveTools[idx]
	if !ok {
		return
	}
	delete(p.interactiveTools, idx)
	if p.interactiveControl != nil {
		delete(p.interactiveControl, tool.ToolUseID)
	}
}

func (p *claudeParser) buildInteractiveDecisionRequestFromControl(requestID, toolName, toolUseID string, input map[string]any) agentadaptor.DecisionRequest {
	kind, ok := claudeInteractiveTools[toolName]
	if !ok {
		kind = agentadaptor.HumanDecisionPermission
	}
	prompt, choices, payload := extractInteractivePayload(kind, input)
	if kind == agentadaptor.HumanDecisionPermission {
		if prompt == "" {
			if s, ok := payload["prompt"].(string); ok && s != "" {
				prompt = s
			}
		}
	}
	return agentadaptor.DecisionRequest{
		RequestID:  requestID,
		RunID:      p.runID,
		ThreadID:   p.sessionID,
		Kind:       kind,
		Source:     sourceForTool(toolName),
		ToolCallID: toolUseID,
		Prompt:     prompt,
		Payload:    payload,
		Choices:    choices,
		CreatedAt:  time.Now().UTC(),
	}
}

// extractInteractivePayload lifts the human-readable prompt, the list of
// choices (for select-type questions) and the structured payload from the
// vendor tool_use input. Each Kind has its own schema:
//
//   - PlanReview (ExitPlanMode):
//     { "plan": "<markdown>" }
//     → prompt = "" (there's no per-question prompt; the UI uses
//     payload.plan as the body), payload keeps "plan"
//
//   - Question (AskUserQuestion):
//     { "questions": [
//     {"type":"freeText","question":"…"}  |
//     {"type":"multipleChoice","question":"…","options":["a","b"]}
//     ] }
//     → prompt = first question.question, choices = its options (if any),
//     payload gets { "questions": [...], "question_type": "freeText"|... }
//
// The function tolerates unknown shapes: anything it doesn't recognise is
// copied verbatim into payload so hosts can still render it.
func extractInteractivePayload(kind agentadaptor.HumanDecisionKind, input map[string]any) (prompt string, choices []agentadaptor.DecisionChoice, payload map[string]any) {
	payload = map[string]any{}
	for k, v := range input {
		payload[k] = v
	}

	switch kind {
	case agentadaptor.HumanDecisionPlanReview:
		if s, ok := input["plan"].(string); ok && s != "" {
			payload["plan"] = s
		}
		return "", nil, payload

	case agentadaptor.HumanDecisionQuestion:
		// Claude's AskUserQuestion packs the actual question(s) inside a
		// "questions" array. Extract the first entry's prompt + options.
		questions, _ := input["questions"].([]any)
		if len(questions) == 0 {
			// Fallback for older claude versions / different shapes.
			if s, ok := input["prompt"].(string); ok {
				prompt = s
			} else if s, ok := input["question"].(string); ok {
				prompt = s
			}
			if arr, ok := input["choices"].([]any); ok {
				choices = decodeChoices(arr)
			}
			return prompt, choices, payload
		}

		first, _ := questions[0].(map[string]any)
		if first == nil {
			return "", nil, payload
		}
		if s, ok := first["question"].(string); ok {
			prompt = s
		}
		if s, ok := first["type"].(string); ok {
			payload["question_type"] = s
		}
		if opts, ok := first["options"].([]any); ok && len(opts) > 0 {
			choices = decodeAskUserQuestionOptions(opts)
		}
		return prompt, choices, payload
	}

	// Permission / unknown: fall back to generic prompt extraction.
	if s, ok := input["prompt"].(string); ok {
		prompt = s
	}
	if arr, ok := input["choices"].([]any); ok {
		choices = decodeChoices(arr)
	}
	return prompt, choices, payload
}

// decodeAskUserQuestionOptions converts claude's AskUserQuestion options —
// which are usually plain strings or objects — into the SDK's DecisionChoice
// shape. Strings become {Key: s, Label: s}.
func decodeAskUserQuestionOptions(raw []any) []agentadaptor.DecisionChoice {
	out := make([]agentadaptor.DecisionChoice, 0, len(raw))
	for _, entry := range raw {
		switch v := entry.(type) {
		case string:
			out = append(out, agentadaptor.DecisionChoice{Key: v, Label: v})
		case map[string]any:
			c := agentadaptor.DecisionChoice{}
			if s, ok := v["key"].(string); ok {
				c.Key = s
			} else if s, ok := v["value"].(string); ok {
				c.Key = s
			} else if s, ok := v["id"].(string); ok {
				c.Key = s
			}
			if s, ok := v["label"].(string); ok {
				c.Label = s
			} else if s, ok := v["text"].(string); ok {
				c.Label = s
			}
			if c.Label == "" {
				c.Label = c.Key
			}
			if c.Key == "" {
				c.Key = c.Label
			}
			if s, ok := v["description"].(string); ok {
				c.Description = s
			}
			if c.Key != "" || c.Label != "" {
				out = append(out, c)
			}
		}
	}
	return out
}

func (p *claudeParser) handleInteractiveControlRequest(payload map[string]any) bool {
	requestID := claudeTopLevelString(payload, "request_id", "requestId")
	request := claudeTopLevelObject(payload, "request")
	if requestID == "" || request == nil {
		return false
	}
	if strings.ToLower(claudeTopLevelString(request, "subtype")) != "can_use_tool" {
		return false
	}

	toolName := claudeTopLevelString(request, "tool_name", "toolName")
	input := claudeTopLevelObject(request, "input")
	toolUseID := claudeTopLevelString(request, "tool_use_id", "toolUseID")
	req := p.buildInteractiveDecisionRequestFromControl(requestID, toolName, toolUseID, input)
	if toolUseID != "" {
		if p.interactiveControl == nil {
			p.interactiveControl = map[string]struct{}{}
		}
		p.interactiveControl[toolUseID] = struct{}{}
	}

	if _, ok := claudeInteractiveTools[toolName]; ok {
		resp, err := p.interactiveSink.RequestDecision(p.interactiveCtx, req)
		if err != nil {
			response := buildInteractiveControlResponse(req, agentadaptor.DecisionResponse{
				RequestID: req.RequestID,
				Result:    agentadaptor.DecisionRejected,
				Text:      "User decision aborted.",
			})
			response = p.decorateInteractiveControlResponse(req, agentadaptor.DecisionResponse{
				RequestID: req.RequestID,
				Result:    agentadaptor.DecisionRejected,
				Text:      "User decision aborted.",
			}, response)
			_ = p.writeInteractiveControlResponse(requestID, response)
			p.trackInteractiveInterrupt(requestID, response)
			return true
		}
		response := buildInteractiveControlResponse(req, resp)
		response = p.decorateInteractiveControlResponse(req, resp, response)
		_ = p.writeInteractiveControlResponse(requestID, response)
		p.trackInteractiveInterrupt(requestID, response)
		p.recordInteractiveRejectFailure(req, resp)
		return true
	}

	effective := driver.EffectiveHumanDecisionPolicy(p.policy)
	switch effective.Permission {
	case agentadaptor.HumanDecisionAutoApprove:
		_ = p.writeInteractiveControlResponse(requestID, buildInteractiveControlResponse(req, agentadaptor.DecisionResponse{
			RequestID: req.RequestID,
			Result:    agentadaptor.DecisionApproved,
		}))
		return true
	case agentadaptor.HumanDecisionAutoReject:
		resp := agentadaptor.DecisionResponse{RequestID: requestID, Result: agentadaptor.DecisionRejected}
		response := buildInteractiveControlResponse(req, resp)
		response = p.decorateInteractiveControlResponse(req, resp, response)
		_ = p.writeInteractiveControlResponse(requestID, response)
		p.trackInteractiveInterrupt(requestID, response)
		p.recordInteractiveRejectFailure(req, resp)
		return true
	default:
		return false
	}
}

// writeInteractiveToolResult formats and writes a user tool_result frame to
// the CLI's stdin. The frame shape mirrors Anthropic's messages API user
// message with a tool_result content block.
func (p *claudeParser) writeInteractiveToolResult(toolUseID, content string, isError bool) error {
	if p.stdin == nil {
		return nil
	}
	frame := map[string]any{
		"type": "user",
		"message": map[string]any{
			"role": "user",
			"content": []map[string]any{
				{
					"type":        "tool_result",
					"tool_use_id": toolUseID,
					"content":     content,
					"is_error":    isError,
				},
			},
		},
	}
	raw, err := json.Marshal(frame)
	if err != nil {
		return err
	}
	// CLI reads NDJSON — every frame ends with a newline.
	raw = append(raw, '\n')
	return p.stdin.Write(raw)
}

func (p *claudeParser) writeInteractiveControlResponse(requestID string, response map[string]any) error {
	if p.stdin == nil {
		return nil
	}
	frame := map[string]any{
		"type": "control_response",
		"response": map[string]any{
			"subtype":    "success",
			"request_id": requestID,
			"response":   response,
		},
	}
	raw, err := jsonMarshalInteractive(frame)
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	return p.stdin.Write(raw)
}

// parseToolUseInput turns the accumulated partial_json into a decoded map.
// Returns nil on parse error so callers render a safe default.
func parseToolUseInput(raw string) map[string]any {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(trimmed), &out); err != nil {
		return nil
	}
	return out
}

// renderInteractiveToolResult converts a DecisionResponse into the
// (content, is_error) pair the CLI expects in a user tool_result. Question
// answers intentionally follow Claude's native AskUserQuestion tool_result
// summary shape: `"question"="answer"` with optional annotations.
//
// For AskUserQuestion we produce a plain-text body the model can read —
// the tool_result content in claude's CLI is stringly-typed, not structured,
// so we write human-readable prose that captures the question → answer
// mapping. This mirrors Claude's own AskUserQuestionTool source.
func renderInteractiveToolResult(req agentadaptor.DecisionRequest, resp agentadaptor.DecisionResponse) (string, bool) {
	switch req.Kind {
	case agentadaptor.HumanDecisionPlanReview:
		switch resp.Result {
		case agentadaptor.DecisionApproved:
			return "User approved the plan. Proceed with implementation.", false
		case agentadaptor.DecisionRejected:
			if resp.Text != "" {
				return "User rejected the plan. Correction hint: " + resp.Text, true
			}
			return "User rejected the plan.", true
		}
	case agentadaptor.HumanDecisionQuestion:
		switch resp.Result {
		case agentadaptor.DecisionAnswered:
			return formatQuestionAnswer(req, resp), false
		case agentadaptor.DecisionRejected:
			return "User declined to answer.", true
		}
	case agentadaptor.HumanDecisionPermission:
		switch resp.Result {
		case agentadaptor.DecisionApproved:
			return "Permission granted by user.", false
		case agentadaptor.DecisionRejected:
			return "Permission denied by user.", true
		}
	}
	return "Decision aborted.", true
}

// formatQuestionAnswer produces the human-readable answer the CLI hands back
// to the model. We first try to reconstruct Claude's native
// `"question"="answer"` summary, then fall back to generic prose only when
// the host did not provide enough structure.
func formatQuestionAnswer(req agentadaptor.DecisionRequest, resp agentadaptor.DecisionResponse) string {
	if structured := formatStructuredQuestionAnswer(req, resp); structured != "" {
		return structured
	}
	if s := resolveSingleQuestionAnswer(req, resp); s != "" {
		return "User answered: " + s
	}
	if len(resp.Answer) > 0 {
		if b, err := json.Marshal(resp.Answer); err == nil {
			return "User answered (structured): " + string(b)
		}
	}
	return "User answered."
}

func buildInteractiveControlResponse(req agentadaptor.DecisionRequest, resp agentadaptor.DecisionResponse) map[string]any {
	toolUseID := strings.TrimSpace(req.ToolCallID)
	allow := func(updatedInput map[string]any) map[string]any {
		out := map[string]any{
			"behavior":     "allow",
			"updatedInput": updatedInput,
		}
		if toolUseID != "" {
			out["toolUseID"] = toolUseID
		}
		return out
	}
	deny := func(message string) map[string]any {
		out := map[string]any{
			"behavior": "deny",
			"message":  message,
		}
		if toolUseID != "" {
			out["toolUseID"] = toolUseID
		}
		return out
	}
	switch req.Kind {
	case agentadaptor.HumanDecisionPlanReview:
		if resp.Result == agentadaptor.DecisionApproved {
			return allow(cloneInteractivePayload(req.Payload))
		}
		message := "User rejected the plan."
		if hint := strings.TrimSpace(resp.Text); hint != "" {
			message += " Correction hint: " + hint
		}
		return deny(message)
	case agentadaptor.HumanDecisionQuestion:
		if resp.Result == agentadaptor.DecisionAnswered {
			return allow(buildQuestionUpdatedInput(req, resp))
		}
		return deny("User declined to answer.")
	case agentadaptor.HumanDecisionPermission:
		if resp.Result == agentadaptor.DecisionApproved {
			return allow(cloneInteractivePayload(req.Payload))
		}
		return deny("Permission denied by user.")
	default:
		return deny("User decision aborted.")
	}
}

func (p *claudeParser) decorateInteractiveControlResponse(req agentadaptor.DecisionRequest, resp agentadaptor.DecisionResponse, response map[string]any) map[string]any {
	if response == nil {
		return nil
	}
	if behavior, _ := response["behavior"].(string); behavior != "deny" {
		return response
	}
	effective := driver.EffectiveHumanDecisionPolicy(p.policy)
	shouldInterrupt := false
	if resp.Result == agentadaptor.DecisionRejected {
		shouldInterrupt = effective.OnReject == agentadaptor.FailureAbort || effective.OnReject == agentadaptor.FailureActionUnset
	}
	if shouldInterrupt {
		response["interrupt"] = true
	}
	return response
}

func (p *claudeParser) trackInteractiveInterrupt(requestID string, response map[string]any) {
	if interrupt, _ := response["interrupt"].(bool); !interrupt {
		return
	}
	if p.stdin != nil {
		_ = p.stdin.Close()
		p.stdin = nil
	}
}

type questionAnswerEntry struct {
	Question string
	Answer   string
	Preview  string
	Notes    string
}

type questionAnnotation struct {
	Preview string
	Notes   string
}

func formatStructuredQuestionAnswer(req agentadaptor.DecisionRequest, resp agentadaptor.DecisionResponse) string {
	entries := collectQuestionAnswerEntries(req, resp)
	if len(entries) == 0 {
		return ""
	}
	parts := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.Question == "" || entry.Answer == "" {
			continue
		}
		part := quoteQuestionAnswerValue(entry.Question) + "=" + quoteQuestionAnswerValue(entry.Answer)
		if entry.Preview != "" {
			part += " selected preview:\n" + entry.Preview
		}
		if entry.Notes != "" {
			part += " user notes: " + entry.Notes
		}
		parts = append(parts, part)
	}
	if len(parts) == 0 {
		return ""
	}
	return "User has answered your questions: " + strings.Join(parts, ", ") + ". You can now continue with the user's answers in mind."
}

func collectQuestionAnswerEntries(req agentadaptor.DecisionRequest, resp agentadaptor.DecisionResponse) []questionAnswerEntry {
	questions := extractQuestionTexts(req)
	annotations := extractQuestionAnnotations(resp.Answer)

	if answers := extractAnswerStringMap(resp.Answer["answers"]); len(answers) > 0 {
		return buildQuestionAnswerEntries(questions, answers, annotations)
	}

	if answers := extractDirectQuestionAnswerMap(resp.Answer, questions); len(answers) > 0 {
		return buildQuestionAnswerEntries(questions, answers, annotations)
	}

	if answer := resolveSingleQuestionAnswer(req, resp); answer != "" {
		question := firstQuestionText(questions)
		if question == "" {
			return nil
		}
		entry := questionAnswerEntry{
			Question: question,
			Answer:   answer,
		}
		if ann, ok := annotations[question]; ok {
			entry.Preview = ann.Preview
			entry.Notes = ann.Notes
		}
		return []questionAnswerEntry{entry}
	}

	return nil
}

func buildQuestionUpdatedInput(req agentadaptor.DecisionRequest, resp agentadaptor.DecisionResponse) map[string]any {
	updated := cloneInteractivePayload(req.Payload)
	delete(updated, "question_type")

	entries := collectQuestionAnswerEntries(req, resp)
	if len(entries) == 0 {
		return updated
	}

	answers := make(map[string]any, len(entries))
	annotations := map[string]any{}
	for _, entry := range entries {
		if entry.Question == "" || entry.Answer == "" {
			continue
		}
		answers[entry.Question] = entry.Answer
		annotation := map[string]any{}
		if entry.Preview != "" {
			annotation["preview"] = entry.Preview
		}
		if entry.Notes != "" {
			annotation["notes"] = entry.Notes
		}
		if len(annotation) > 0 {
			annotations[entry.Question] = annotation
		}
	}
	if len(answers) > 0 {
		updated["answers"] = answers
	}
	if len(annotations) > 0 {
		updated["annotations"] = annotations
	}
	return updated
}

func buildQuestionAnswerEntries(questions []string, answers map[string]string, annotations map[string]questionAnnotation) []questionAnswerEntry {
	if len(answers) == 0 {
		return nil
	}

	out := make([]questionAnswerEntry, 0, len(answers))
	seen := make(map[string]struct{}, len(answers))
	for _, question := range questions {
		answer := strings.TrimSpace(answers[question])
		if answer == "" {
			continue
		}
		entry := questionAnswerEntry{
			Question: question,
			Answer:   answer,
		}
		if ann, ok := annotations[question]; ok {
			entry.Preview = ann.Preview
			entry.Notes = ann.Notes
		}
		out = append(out, entry)
		seen[question] = struct{}{}
	}

	extras := make([]string, 0, len(answers))
	for question, answer := range answers {
		if strings.TrimSpace(answer) == "" {
			continue
		}
		if _, ok := seen[question]; ok {
			continue
		}
		extras = append(extras, question)
	}
	sort.Strings(extras)
	for _, question := range extras {
		entry := questionAnswerEntry{
			Question: question,
			Answer:   strings.TrimSpace(answers[question]),
		}
		if ann, ok := annotations[question]; ok {
			entry.Preview = ann.Preview
			entry.Notes = ann.Notes
		}
		out = append(out, entry)
	}

	return out
}

func extractQuestionTexts(req agentadaptor.DecisionRequest) []string {
	out := make([]string, 0, 4)
	seen := make(map[string]struct{}, 4)
	appendQuestion := func(raw string) {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			return
		}
		if _, ok := seen[raw]; ok {
			return
		}
		seen[raw] = struct{}{}
		out = append(out, raw)
	}

	if req.Payload != nil {
		switch questions := req.Payload["questions"].(type) {
		case []any:
			for _, raw := range questions {
				if m, ok := raw.(map[string]any); ok {
					if question, ok := m["question"].(string); ok {
						appendQuestion(question)
					}
				}
			}
		case []map[string]any:
			for _, m := range questions {
				if question, ok := m["question"].(string); ok {
					appendQuestion(question)
				}
			}
		}
		if question, ok := req.Payload["question"].(string); ok {
			appendQuestion(question)
		}
	}

	appendQuestion(req.Prompt)
	return out
}

func firstQuestionText(questions []string) string {
	if len(questions) == 0 {
		return ""
	}
	return questions[0]
}

func extractAnswerStringMap(raw any) map[string]string {
	switch v := raw.(type) {
	case map[string]string:
		out := make(map[string]string, len(v))
		for key, value := range v {
			if value = strings.TrimSpace(value); value != "" {
				out[key] = value
			}
		}
		return out
	case map[string]any:
		out := make(map[string]string, len(v))
		for key, value := range v {
			if answer := stringifyQuestionAnswerValue(value); answer != "" {
				out[key] = answer
			}
		}
		return out
	default:
		return nil
	}
}

func extractDirectQuestionAnswerMap(answer map[string]any, questions []string) map[string]string {
	if len(answer) == 0 || len(questions) == 0 {
		return nil
	}
	out := make(map[string]string)
	for _, question := range questions {
		if value := stringifyQuestionAnswerValue(answer[question]); value != "" {
			out[question] = value
		}
	}
	return out
}

func extractQuestionAnnotations(answer map[string]any) map[string]questionAnnotation {
	if len(answer) == 0 {
		return nil
	}
	rawAnnotations, ok := answer["annotations"]
	if !ok {
		return nil
	}
	entries, ok := rawAnnotations.(map[string]any)
	if !ok {
		return nil
	}
	out := make(map[string]questionAnnotation, len(entries))
	for question, raw := range entries {
		entryMap, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		annotation := questionAnnotation{}
		if preview, ok := entryMap["preview"].(string); ok {
			annotation.Preview = strings.TrimSpace(preview)
		}
		if notes, ok := entryMap["notes"].(string); ok {
			annotation.Notes = strings.TrimSpace(notes)
		}
		if annotation.Preview != "" || annotation.Notes != "" {
			out[question] = annotation
		}
	}
	return out
}

func resolveSingleQuestionAnswer(req agentadaptor.DecisionRequest, resp agentadaptor.DecisionResponse) string {
	if s := strings.TrimSpace(resp.Text); s != "" {
		return s
	}
	for _, key := range []string{"label", "text", "answer", "response", "value"} {
		if s := stringifyQuestionAnswerValue(resp.Answer[key]); s != "" {
			return s
		}
	}
	if s := choiceDisplayValue(req.Choices, strings.TrimSpace(resp.Choice)); s != "" {
		return s
	}
	if s := choiceDisplayValue(req.Choices, stringifyQuestionAnswerValue(resp.Answer["choice"])); s != "" {
		return s
	}
	return ""
}

func stringifyQuestionAnswerValue(raw any) string {
	switch v := raw.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(v)
	case []string:
		parts := make([]string, 0, len(v))
		for _, item := range v {
			item = strings.TrimSpace(item)
			if item != "" {
				parts = append(parts, item)
			}
		}
		return strings.Join(parts, ", ")
	case []any:
		parts := make([]string, 0, len(v))
		for _, item := range v {
			if s := stringifyQuestionAnswerValue(item); s != "" {
				parts = append(parts, s)
			}
		}
		return strings.Join(parts, ", ")
	default:
		return strings.TrimSpace(fmt.Sprint(v))
	}
}

func choiceDisplayValue(choices []agentadaptor.DecisionChoice, raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	for _, choice := range choices {
		if raw == choice.Key {
			if label := strings.TrimSpace(choice.Label); label != "" {
				return label
			}
			return raw
		}
		if raw == choice.Label {
			return raw
		}
	}
	return raw
}

func quoteQuestionAnswerValue(raw string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `"`, `\"`)
	return `"` + replacer.Replace(raw) + `"`
}

func (p *claudeParser) recordInteractiveRejectFailure(req agentadaptor.DecisionRequest, resp agentadaptor.DecisionResponse) {
	if resp.Result != agentadaptor.DecisionRejected {
		return
	}
	effective := driver.EffectiveHumanDecisionPolicy(p.policy)
	if effective.OnReject != agentadaptor.FailureAbort && effective.OnReject != agentadaptor.FailureActionUnset {
		return
	}
	p.pendingFailure = &agentadaptor.RunFailure{
		Code:    agentadaptor.FailureReject,
		Message: hitlFailureMessage(req.Kind),
		HumanDecision: &agentadaptor.HumanDecisionFailure{
			Kind:     req.Kind,
			Source:   req.Source,
			Decision: agentadaptor.DecisionRejected,
			Request:  cloneInteractiveRequest(req),
			Attempts: 1,
		},
	}
}

func cloneInteractivePayload(payload map[string]any) map[string]any {
	if payload == nil {
		return map[string]any{}
	}
	out := make(map[string]any, len(payload))
	for key, value := range payload {
		out[key] = value
	}
	return out
}

// cloneInteractiveRequest makes a small-scoped copy for Failure snapshot
// recording. We want RunResult.Failure to survive after the parser state
// is cleared.
func cloneInteractiveRequest(req agentadaptor.DecisionRequest) *agentadaptor.DecisionRequest {
	out := req
	if req.Payload != nil {
		p := make(map[string]any, len(req.Payload))
		for k, v := range req.Payload {
			p[k] = v
		}
		out.Payload = p
	}
	if len(req.Choices) > 0 {
		out.Choices = append([]agentadaptor.DecisionChoice(nil), req.Choices...)
	}
	return &out
}
