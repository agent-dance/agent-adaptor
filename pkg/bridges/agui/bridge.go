// Package agui bridges the agent-adaptor SDK's streaming surface
// (RunHandle.StreamEvents) onto the AG-UI protocol event stream.
//
// The bridge is intentionally thin: it consumes StreamPayload values on the
// SDK side and emits *github.com/ag-ui-protocol/ag-ui/sdks/community/go/pkg/core/events.Event*
// values on the host side. It is adapter-agnostic; codex / claude / cursor
// are expected to populate StreamPayload uniformly so the same Wrap call
// works for any driver.
//
// Typical use from a host:
//
//	handle, _ := sdk.Start(ctx, prompt, agentadaptor.WithStreaming())
//	for ev := range agui.Wrap(handle) {
//	    // ev is an events.Event; forward it via SSE / WebSocket / any
//	    // transport the host prefers.
//	}
//
// The bridge does not emit STATE_SNAPSHOT / STATE_DELTA events; AG-UI
// regards state as the host's domain responsibility. Hosts that want to
// synchronize business state can compose this bridge with their own state
// maintainer.
package agui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"

	aguievents "github.com/ag-ui-protocol/ag-ui/sdks/community/go/pkg/core/events"

	agentadaptor "github.com/agent-dance/agent-adaptor"
)

// DecisionMode selects how the bridge translates HITL events
// (StreamHITLRequested / StreamHITLResolved). DecisionAsToolCall is the new
// default: HITL events are mapped onto AG-UI tool-call lifecycles so
// CopilotKit-style clients can use `useCopilotAction` to render approval UI.
// DecisionAsCustom preserves the legacy behaviour that sent them as
// CustomEvent payloads and is retained for backward compatibility.
type DecisionMode int

const (
	// DecisionAsToolCall maps HITL request/resolve events onto AG-UI tool-call
	// lifecycles so frontends can render them with standard action handlers.
	DecisionAsToolCall DecisionMode = iota
	// DecisionAsCustom preserves the legacy mapping to AG-UI CustomEvent
	// payloads for hosts that already built their own decision renderer.
	DecisionAsCustom
)

// decisionToolPrefix is prepended to DecisionRequest.RequestID to avoid
// collisions with agent-native ToolCallID values on the wire.
const decisionToolPrefix = "dec-"

// All AG-UI literal string values consumed by this file are defined in
// literals.go and shared with tests and future callers.

// Translator is a stateful mapper from StreamPayload to AG-UI events. The
// zero value is usable; Wrap() creates one per run so state never leaks
// between runs.
type Translator struct {
	mu sync.Mutex

	threadID  string
	runID     string
	runStart  bool // RUN_STARTED already emitted
	runFinish bool // RUN_FINISHED or RUN_ERROR already emitted

	// pending buffers events produced before the first RUN_STARTED arrives.
	pending []aguievents.Event

	// Active message lifecycles for idempotent START / CONTENT / END handling.
	activeText      map[string]bool
	activeReason    map[string]bool
	activeToolStart map[string]bool
	toolStartArgs   map[string]string

	// decisionMode selects HITL event translation. Default: DecisionAsToolCall.
	decisionMode DecisionMode
}

// NewTranslator returns a fresh translator with the default DecisionAsToolCall
// HITL mapping.
func NewTranslator(opts ...TranslatorOption) *Translator {
	t := &Translator{
		activeText:      map[string]bool{},
		activeReason:    map[string]bool{},
		activeToolStart: map[string]bool{},
		toolStartArgs:   map[string]string{},
		decisionMode:    DecisionAsToolCall,
	}
	for _, o := range opts {
		o(t)
	}
	return t
}

// TranslatorOption configures a Translator / Wrap call.
type TranslatorOption func(*Translator)

// WithDecisionMode selects the HITL mapping strategy. DecisionAsToolCall is
// the default; DecisionAsCustom retains the legacy CustomEvent mapping.
func WithDecisionMode(mode DecisionMode) TranslatorOption {
	return func(t *Translator) { t.decisionMode = mode }
}

// Translate maps one StreamPayload to zero or more AG-UI events. It is
// stateful: it adds opening REASONING / TEXT / TOOL lifecycle markers when
// missing, buffers any events produced before the first RUN_STARTED so the
// output always starts with RUN_STARTED, and guards against duplicate
// lifecycle markers.
func (t *Translator) Translate(p agentadaptor.StreamPayload) []aguievents.Event {
	t.mu.Lock()
	defer t.mu.Unlock()

	if p.ThreadID != "" {
		t.threadID = p.ThreadID
	}
	if p.RunID != "" {
		t.runID = p.RunID
	}

	// Once RUN_FINISHED / RUN_ERROR has been emitted no further events may
	// reach the wire — CopilotKit's verifyEvents raises "The run has
	// already finished/errored" otherwise. Suppress post-terminal traffic
	// silently (adapters can still trigger late events in edge cases like
	// background-drain notifications).
	if t.runFinish {
		return nil
	}

	// Terminal events need the pending queue flushed and a synthesized
	// RUN_STARTED when the adapter never emitted one.
	switch p.Kind {
	case agentadaptor.StreamRunStarted:
		if t.runStart {
			return nil
		}
		t.runStart = true
		out := []aguievents.Event{
			aguievents.NewRunStartedEvent(t.threadOrDefault(), t.runOrDefault()),
		}
		if len(t.pending) > 0 {
			out = append(out, t.pending...)
			t.pending = nil
		}
		return out

	case agentadaptor.StreamRunFinished:
		if t.runFinish {
			return nil
		}
		t.runFinish = true
		out := t.ensureRunStartedLocked()
		out = append(out, t.closeAllOpenLifecyclesLocked()...)
		out = append(out, aguievents.NewRunFinishedEvent(t.threadOrDefault(), t.runOrDefault()))
		return out

	case agentadaptor.StreamRunError:
		if t.runFinish {
			return nil
		}
		t.runFinish = true
		out := t.ensureRunStartedLocked()
		out = append(out, t.closeAllOpenLifecyclesLocked()...)
		msg, code := errorDetails(p)
		opts := []aguievents.RunErrorOption{}
		if code != "" {
			opts = append(opts, aguievents.WithErrorCode(code))
		}
		if t.runID != "" {
			opts = append(opts, aguievents.WithRunID(t.runID))
		}
		out = append(out, aguievents.NewRunErrorEvent(msg, opts...))
		return out
	}

	// Non-terminal events: translate normally, then gate on runStart. Any
	// events produced before the first RUN_STARTED go into pending so the
	// caller-visible stream always begins with RUN_STARTED.
	translated := t.translateNonTerminalLocked(p)
	if len(translated) == 0 {
		return nil
	}
	if !t.runStart {
		t.pending = append(t.pending, translated...)
		return nil
	}
	return translated
}

// CloseRun emits the terminal event for the run as a single operation based
// on waitErr. It is intended to replace the common host idiom of calling
// Translate(StreamRunFinished) and then conditionally Translate(StreamRunError)
// — which silently drops the error because the first call latches
// runFinish=true (see Translate).
//
// Semantics:
//   - waitErr == nil emits RUN_FINISHED.
//   - errors.Is(waitErr, context.Canceled) emits RUN_ERROR with
//     FailureCancelled.
//   - any other error emits RUN_ERROR with FailureAgentError.
//
// CloseRun is idempotent: subsequent calls (and any subsequent
// Translate(StreamRunFinished|StreamRunError)) return nil, matching the
// translator's terminal-latch invariant.
//
// Preferred pattern:
//
//	err := handle.Wait()
//	if evts := translator.CloseRun(err); len(evts) > 0 {
//	    writer.Write(evts...)
//	}
func (t *Translator) CloseRun(waitErr error) []aguievents.Event {
	if waitErr == nil {
		return t.Translate(agentadaptor.StreamPayload{Kind: agentadaptor.StreamRunFinished})
	}
	code := agentadaptor.FailureAgentError
	if errors.Is(waitErr, context.Canceled) {
		code = agentadaptor.FailureCancelled
	}
	return t.Translate(agentadaptor.StreamPayload{
		Kind: agentadaptor.StreamRunError,
		Error: &agentadaptor.RunFailure{
			Code:    code,
			Message: waitErr.Error(),
		},
	})
}

// translateNonTerminalLocked handles every payload kind except the run
// lifecycle markers (started / finished / error). The caller holds t.mu.
func (t *Translator) translateNonTerminalLocked(p agentadaptor.StreamPayload) []aguievents.Event {
	switch p.Kind {
	case agentadaptor.StreamStepStarted:
		return []aguievents.Event{aguievents.NewStepStartedEvent(p.Name)}
	case agentadaptor.StreamStepFinished:
		return []aguievents.Event{aguievents.NewStepFinishedEvent(p.Name)}

	case agentadaptor.StreamTextStart:
		if p.MessageID == "" {
			return nil
		}
		if t.activeText[p.MessageID] {
			return nil
		}
		t.activeText[p.MessageID] = true
		return []aguievents.Event{aguievents.NewTextMessageStartEvent(p.MessageID, textRoleOpt(p.Role))}

	case agentadaptor.StreamTextContent:
		if p.MessageID == "" || p.Delta == "" {
			return nil
		}
		out := []aguievents.Event{}
		if !t.activeText[p.MessageID] {
			t.activeText[p.MessageID] = true
			out = append(out, aguievents.NewTextMessageStartEvent(p.MessageID, textRoleOpt(p.Role)))
		}
		out = append(out, aguievents.NewTextMessageContentEvent(p.MessageID, p.Delta))
		return out

	case agentadaptor.StreamTextEnd:
		if p.MessageID == "" {
			return nil
		}
		if !t.activeText[p.MessageID] {
			return nil
		}
		delete(t.activeText, p.MessageID)
		return []aguievents.Event{aguievents.NewTextMessageEndEvent(p.MessageID)}

	case agentadaptor.StreamReasoningStart:
		if p.MessageID == "" {
			return nil
		}
		if t.activeReason[p.MessageID] {
			return nil
		}
		t.activeReason[p.MessageID] = true
		return []aguievents.Event{aguievents.NewReasoningMessageStartEvent(p.MessageID, ReasoningRole)}

	case agentadaptor.StreamReasoningContent:
		if p.MessageID == "" || p.Delta == "" {
			return nil
		}
		out := []aguievents.Event{}
		if !t.activeReason[p.MessageID] {
			t.activeReason[p.MessageID] = true
			out = append(out, aguievents.NewReasoningMessageStartEvent(p.MessageID, ReasoningRole))
		}
		out = append(out, aguievents.NewReasoningMessageContentEvent(p.MessageID, p.Delta))
		return out

	case agentadaptor.StreamReasoningEnd:
		if p.MessageID == "" {
			return nil
		}
		if !t.activeReason[p.MessageID] {
			return nil
		}
		delete(t.activeReason, p.MessageID)
		return []aguievents.Event{aguievents.NewReasoningMessageEndEvent(p.MessageID)}

	case agentadaptor.StreamToolCallStart:
		if p.ToolCallID == "" {
			return nil
		}
		if t.activeToolStart[p.ToolCallID] {
			return nil
		}
		t.activeToolStart[p.ToolCallID] = true
		if len(p.Args) > 0 {
			if raw, err := json.Marshal(p.Args); err == nil {
				t.toolStartArgs[p.ToolCallID] = string(raw)
			}
		}
		return []aguievents.Event{aguievents.NewToolCallStartEvent(p.ToolCallID, defaultString(p.Name, "tool"))}

	case agentadaptor.StreamToolCallArgs:
		if p.ToolCallID == "" || p.Delta == "" {
			return nil
		}
		// Some adapters (e.g. codex) may stream output before the item/started
		// path has emitted StreamToolCallStart. Synthesize TOOL_CALL_START so
		// the AG-UI sequence remains well-formed.
		out := []aguievents.Event{}
		if !t.activeToolStart[p.ToolCallID] {
			t.activeToolStart[p.ToolCallID] = true
			out = append(out, aguievents.NewToolCallStartEvent(p.ToolCallID, defaultString(p.Name, "tool")))
		}
		// StreamToolCallArgs also carries command output for adapters such as
		// Codex. Preserve that existing delta stream and suppress the complete
		// start snapshot; concatenating both would produce invalid AG-UI args.
		delete(t.toolStartArgs, p.ToolCallID)
		out = append(out, aguievents.NewToolCallArgsEvent(p.ToolCallID, p.Delta))
		return out

	case agentadaptor.StreamToolCallEnd:
		if p.ToolCallID == "" {
			return nil
		}
		if !t.activeToolStart[p.ToolCallID] {
			delete(t.toolStartArgs, p.ToolCallID)
			return nil
		}
		delete(t.activeToolStart, p.ToolCallID)
		out := []aguievents.Event{}
		if args := t.toolStartArgs[p.ToolCallID]; args != "" {
			out = append(out, aguievents.NewToolCallArgsEvent(p.ToolCallID, args))
		}
		delete(t.toolStartArgs, p.ToolCallID)
		out = append(out, aguievents.NewToolCallEndEvent(p.ToolCallID))
		return out

	case agentadaptor.StreamToolCallResult:
		if p.ToolCallID == "" {
			return nil
		}
		content := toolResultContent(p)
		// AG-UI requires a message id; reuse the tool call id when the
		// adapter did not provide one.
		msgID := p.MessageID
		if msgID == "" {
			msgID = p.ToolCallID + ":result"
		}
		return []aguievents.Event{aguievents.NewToolCallResultEvent(msgID, p.ToolCallID, content)}

	case agentadaptor.StreamHITLRequested:
		if t.decisionMode == DecisionAsCustom {
			return []aguievents.Event{customFromPayload(string(p.Kind), p)}
		}
		return t.hitlRequestedAsToolCall(p)
	case agentadaptor.StreamHITLResolved:
		if t.decisionMode == DecisionAsCustom {
			return []aguievents.Event{customFromPayload(string(p.Kind), p)}
		}
		return t.hitlResolvedAsToolCall(p)
	case agentadaptor.StreamDropped:
		return []aguievents.Event{customFromPayload(string(p.Kind), p)}

	case "":
		// Unclassified pass-through. Codex adapter uses this for e.g.
		// thread/tokenUsage/updated. Convey it as a CUSTOM event so hosts
		// can render or ignore as they see fit.
		return []aguievents.Event{customFromPayload(p.Name, p)}
	}

	return nil
}

// ensureRunStartedLocked returns the events needed to make sure the output
// stream starts with RUN_STARTED before any other event is emitted. It is
// a no-op when RUN_STARTED has already been forwarded. Callers must hold
// t.mu.
func (t *Translator) ensureRunStartedLocked() []aguievents.Event {
	if t.runStart {
		return nil
	}
	t.runStart = true
	out := []aguievents.Event{
		aguievents.NewRunStartedEvent(t.threadOrDefault(), t.runOrDefault()),
	}
	if len(t.pending) > 0 {
		out = append(out, t.pending...)
		t.pending = nil
	}
	return out
}

// closeAllOpenLifecyclesLocked emits closing markers for any dangling
// TEXT_MESSAGE / REASONING_MESSAGE / TOOL_CALL lifecycles so the final
// stream is well-formed. Callers must hold t.mu.
func (t *Translator) closeAllOpenLifecyclesLocked() []aguievents.Event {
	out := []aguievents.Event{}
	for id := range t.activeText {
		out = append(out, aguievents.NewTextMessageEndEvent(id))
	}
	t.activeText = map[string]bool{}
	for id := range t.activeReason {
		out = append(out, aguievents.NewReasoningMessageEndEvent(id))
	}
	t.activeReason = map[string]bool{}
	for id := range t.activeToolStart {
		if args := t.toolStartArgs[id]; args != "" {
			out = append(out, aguievents.NewToolCallArgsEvent(id, args))
		}
		out = append(out, aguievents.NewToolCallEndEvent(id))
	}
	t.activeToolStart = map[string]bool{}
	t.toolStartArgs = map[string]string{}
	return out
}

// Wrap subscribes to the run's StreamEvents, translates each payload to a
// sequence of AG-UI events, and returns a channel that the caller can range
// over. When the run finishes (Wait returns), the bridge emits a closing
// RUN_FINISHED / RUN_ERROR if the adapter did not, then closes the channel.
//
// Wrap launches exactly one goroutine. Cancelling the context passed to
// Wait() via handle.Cancel() propagates through the SDK and terminates the
// goroutine cleanly.
func Wrap(handle agentadaptor.RunHandle) <-chan aguievents.Event {
	return WrapWithContext(context.Background(), handle)
}

// WrapWithContext is the context-aware variant of Wrap. The context is
// consulted when waiting for Wait() to return; cancelling it short-circuits
// the loop (after best-effort handle.Cancel()) and drains the stream
// channel before closing the output.
func WrapWithContext(ctx context.Context, handle agentadaptor.RunHandle) <-chan aguievents.Event {
	out := make(chan aguievents.Event, 32)
	translator := NewTranslator()

	go func() {
		defer close(out)

		// Drain StreamPayloads first. Wait() may race with StreamEvents()
		// closing; we always prefer forwarding what we already saw.
		waitDone := make(chan struct{})
		var waitResult agentadaptor.RunResult
		var waitErr error
		go func() {
			defer close(waitDone)
			waitResult, waitErr = handle.Wait(ctx)
		}()

	loop:
		for {
			select {
			case <-ctx.Done():
				_ = handle.Cancel(ctx)
				// keep looping; we still want to drain pending payloads
			case p, ok := <-handle.StreamEvents():
				if !ok {
					break loop
				}
				for _, ev := range translator.Translate(p) {
					out <- ev
				}
			}
		}

		<-waitDone

		// Synthesize a closing RUN_FINISHED / RUN_ERROR if the adapter did
		// not already emit one. ensureRunStartedLocked flushes any pending
		// pre-start events and backfills RUN_STARTED when needed so the
		// final stream always satisfies the AG-UI spec.
		translator.mu.Lock()
		alreadyFinished := translator.runFinish
		if !alreadyFinished && waitErr != nil {
			translator.runFinish = true
			code := "run.error"
			if errors.Is(waitErr, context.Canceled) {
				code = "run.cancelled"
			}
			msg := waitErr.Error()
			opts := []aguievents.RunErrorOption{aguievents.WithErrorCode(code)}
			if translator.runID != "" {
				opts = append(opts, aguievents.WithRunID(translator.runID))
			}
			closing := translator.ensureRunStartedLocked()
			closing = append(closing, translator.closeAllOpenLifecyclesLocked()...)
			closing = append(closing, aguievents.NewRunErrorEvent(msg, opts...))
			translator.mu.Unlock()
			for _, ev := range closing {
				out <- ev
			}
			return
		}
		if !alreadyFinished {
			translator.runFinish = true
			// Fill missing identifiers from the authoritative result.
			if translator.threadID == "" {
				translator.threadID = resultThreadID(waitResult)
			}
			if translator.runID == "" {
				translator.runID = waitResult.RunID
			}
			closing := translator.ensureRunStartedLocked()
			closing = append(closing, translator.closeAllOpenLifecyclesLocked()...)
			closing = append(closing, aguievents.NewRunFinishedEvent(translator.threadOrDefault(), translator.runOrDefault()))
			translator.mu.Unlock()
			for _, ev := range closing {
				out <- ev
			}
			return
		}
		translator.mu.Unlock()
	}()

	return out
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func (t *Translator) threadOrDefault() string {
	if t.threadID != "" {
		return t.threadID
	}
	return fallbackThreadID
}

func (t *Translator) runOrDefault() string {
	if t.runID != "" {
		return t.runID
	}
	return fallbackRunID
}

// textRoleOpt maps a StreamPayload Role onto the AG-UI WithRole option
// used by NewTextMessageStartEvent. Zero value (RoleAssistant) emits the
// AG-UI "assistant" role explicitly so user-role and assistant-role
// events have schema-identical shapes on the wire.
func textRoleOpt(r agentadaptor.Role) aguievents.TextMessageStartOption {
	switch r {
	case agentadaptor.RoleUser:
		return aguievents.WithRole("user")
	default:
		return aguievents.WithRole("assistant")
	}
}

func defaultString(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}

func errorDetails(p agentadaptor.StreamPayload) (msg, code string) {
	if p.Error != nil {
		msg = p.Error.Message
		code = string(p.Error.Code)
	}
	if msg == "" {
		msg = "stream run error"
	}
	return msg, code
}

func toolResultContent(p agentadaptor.StreamPayload) string {
	if p.Result == nil {
		// Avoid emitting TOOL_CALL_RESULT with empty content (AG-UI validation).
		return "{}"
	}
	// Inline the most common payload shapes. Hosts get the full map via
	// their own StreamEvents subscription when they need the raw fields.
	if text, ok := p.Result["text"].(string); ok && text != "" {
		return text
	}
	if content, ok := p.Result["content"].(string); ok && content != "" {
		return content
	}
	if out, ok := p.Result["output"].(string); ok && out != "" {
		if exit, ok := p.Result["exitCode"].(int); ok {
			return out + "\n[exit=" + strconv.Itoa(exit) + "]"
		}
		return out
	}
	if status, ok := p.Result["status"].(string); ok && status != "" {
		return status
	}
	// As a last resort, serialize the raw map so we never emit an invalid
	// empty TOOL_CALL_RESULT event for adapters with provider-specific shapes.
	if raw, err := json.Marshal(p.Result); err == nil && len(raw) > 0 && string(raw) != "{}" {
		return string(raw)
	}
	return ""
}

func customFromPayload(name string, p agentadaptor.StreamPayload) aguievents.Event {
	value := map[string]any{}
	for k, v := range p.Raw {
		value[k] = v
	}
	if name == "" {
		name = "codex.event"
	}
	opts := []aguievents.CustomEventOption{}
	if len(value) > 0 {
		opts = append(opts, aguievents.WithValue(value))
	}
	return aguievents.NewCustomEvent(name, opts...)
}

func resultThreadID(r agentadaptor.RunResult) string {
	if r.Session == nil {
		return ""
	}
	return r.Session.ID
}

// hitlRequestedAsToolCall converts a StreamHITLRequested into
// ToolCallStart + ToolCallArgs (§6.1 schema). The tool_call_id receives a
// "dec-" prefix to prevent collisions with agent-native tool_call_ids.
func (t *Translator) hitlRequestedAsToolCall(p agentadaptor.StreamPayload) []aguievents.Event {
	req := p.HITLRequested
	if req == nil || req.RequestID == "" {
		return nil
	}
	toolCallID := decisionToolPrefix + req.RequestID
	toolName := hitlToolName(req.Kind, req.Source)

	if t.activeToolStart[toolCallID] {
		return nil
	}
	t.activeToolStart[toolCallID] = true

	payload := map[string]any{
		"request_id":    req.RequestID,
		"kind":          string(req.Kind),
		"source":        req.Source,
		"prompt":        req.Prompt,
		"payload":       req.Payload,
		"choices":       choicesToJSON(req.Choices),
		"tool_call_id":  req.ToolCallID,
		"deadline":      req.Deadline,
		"retry_attempt": req.RetryAttempt,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil
	}
	return []aguievents.Event{
		aguievents.NewToolCallStartEvent(toolCallID, toolName),
		aguievents.NewToolCallArgsEvent(toolCallID, string(body)),
	}
}

// hitlResolvedAsToolCall converts a StreamHITLResolved into ToolCallEnd +
// ToolCallResult.
func (t *Translator) hitlResolvedAsToolCall(p agentadaptor.StreamPayload) []aguievents.Event {
	res := p.HITLResolved
	if res == nil || res.RequestID == "" {
		return nil
	}
	toolCallID := decisionToolPrefix + res.RequestID
	if !t.activeToolStart[toolCallID] {
		// Out-of-order resolved — still emit for observers but do not pair.
		// Creating a synthetic start before the end keeps the AG-UI stream
		// well-formed.
		toolName := hitlToolName(res.Kind, res.Source)
		t.activeToolStart[toolCallID] = true
		events := []aguievents.Event{aguievents.NewToolCallStartEvent(toolCallID, toolName)}
		delete(t.activeToolStart, toolCallID)
		return append(events, hitlResolvedTail(toolCallID, res)...)
	}
	delete(t.activeToolStart, toolCallID)
	return hitlResolvedTail(toolCallID, res)
}

func hitlResolvedTail(toolCallID string, res *agentadaptor.HITLResolvedPayload) []aguievents.Event {
	payload := map[string]any{
		"result":        string(res.Result),
		"choice":        res.Choice,
		"answer":        res.Answer,
		"retry_attempt": res.RetryAttempt,
		"latency_ms":    res.Latency.Milliseconds(),
	}
	body, err := json.Marshal(payload)
	if err != nil {
		body = []byte("{}")
	}
	msgID := toolCallID + ":result"
	return []aguievents.Event{
		aguievents.NewToolCallEndEvent(toolCallID),
		aguievents.NewToolCallResultEvent(msgID, toolCallID, string(body)),
	}
}

// hitlToolName composes the canonical "dec.<kind>.<source>" name.
func hitlToolName(kind agentadaptor.HumanDecisionKind, source string) string {
	parts := []string{"dec", string(kind)}
	if src := strings.TrimSpace(source); src != "" {
		parts = append(parts, src)
	}
	return strings.Join(parts, ".")
}

func choicesToJSON(choices []agentadaptor.DecisionChoice) []map[string]string {
	if len(choices) == 0 {
		return nil
	}
	out := make([]map[string]string, 0, len(choices))
	for _, c := range choices {
		out = append(out, map[string]string{
			"key":         c.Key,
			"label":       c.Label,
			"description": c.Description,
		})
	}
	return out
}

// ResolveDecision routes a host-supplied AG-UI-style decision response into
// the underlying RunHandle. It strips the decisionToolPrefix from the
// AG-UI-visible tool_call_id before forwarding to handle.ResolveDecision, so
// callers can pass the ToolCallID they see on the wire.
//
// Errors from handle.ResolveDecision (ErrDecisionRequestExpired,
// ErrDecisionResultKindMismatch, ErrRunEnded) are propagated verbatim.
func ResolveDecision(handle agentadaptor.RunHandle, toolCallID string, resp agentadaptor.DecisionResponse) error {
	if handle == nil {
		return agentadaptor.ErrRunEnded
	}
	requestID := strings.TrimPrefix(toolCallID, decisionToolPrefix)
	if requestID == "" {
		return fmt.Errorf("agui: empty tool_call_id")
	}
	resp.RequestID = requestID
	return handle.ResolveDecision(requestID, resp)
}
