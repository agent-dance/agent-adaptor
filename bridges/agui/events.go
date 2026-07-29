package agui

// This file maps the unified adaptor.Event family onto the AG-UI state
// machine.
//
// Typical host use:
//
//	stream := agent.Stream(ctx, prompt)
//	for ev := range agui.Events(stream) {
//	    // ev is an AG-UI events.Event; forward it via SSE / WebSocket.
//	}
//
// Protocol invariants:
//   - the output always starts with RUN_STARTED (pre-start events buffer);
//   - lifecycle START/CONTENT/END markers are idempotent and synthesized
//     when the producer skipped the opening marker;
//   - all open lifecycles close before the terminal event;
//   - when a Driver publishes no assistant text events, a non-empty final
//     Result.Text is projected once as a complete assistant message before
//     the terminal event;
//   - after CloseRun or CloseResult emits RUN_FINISHED / RUN_ERROR the
//     translator suppresses all traffic;
//   - approvals project as "dec.<kind>.<source>" tool-call lifecycles
//     (DecisionAsToolCall) or CUSTOM events (DecisionAsCustom);
//   - capability degradation stays visible: the run-policy retry warning
//     (Notice kind "lifecycle", Data.warning=human_decision_retry_unsupported)
//     reaches the wire as a CUSTOM event.

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	aguievents "github.com/ag-ui-protocol/ag-ui/sdks/community/go/pkg/core/events"

	adaptor "github.com/agent-dance/agent-adaptor"
)

// EventTranslator is the stateful mapper from adaptor.Event to AG-UI
// events. Create one per run via NewEventTranslator (or let Events do it) so
// state never leaks between runs.
type EventTranslator struct {
	mu sync.Mutex

	threadID  string
	runID     string
	runStart  bool // RUN_STARTED already emitted
	runFinish bool // RUN_FINISHED or RUN_ERROR already emitted

	// pending buffers events produced before the first RunStarted arrives.
	pending []aguievents.Event

	// Active lifecycles for idempotent START / CONTENT / END handling.
	activeText      map[string]bool
	activeReason    map[string]bool
	activeToolStart map[string]bool
	assistantText   bool // non-empty assistant TEXT_MESSAGE_CONTENT was emitted
	subagents       *subagentTracker

	decisionMode DecisionMode
}

// EventTranslatorOption configures an EventTranslator / Events call.
type EventTranslatorOption func(*EventTranslator)

// WithEventDecisionMode selects the approval projection strategy for the
// v1 translator. DecisionAsToolCall is the default; DecisionAsCustom selects
// an explicit CustomEvent mapping.
func WithEventDecisionMode(mode DecisionMode) EventTranslatorOption {
	return func(t *EventTranslator) { t.decisionMode = mode }
}

// NewEventTranslator returns a fresh translator with the default
// DecisionAsToolCall approval mapping.
func NewEventTranslator(opts ...EventTranslatorOption) *EventTranslator {
	t := &EventTranslator{
		activeText:      map[string]bool{},
		activeReason:    map[string]bool{},
		activeToolStart: map[string]bool{},
		subagents:       newSubagentTracker(),
		decisionMode:    DecisionAsToolCall,
	}
	for _, o := range opts {
		o(t)
	}
	return t
}

// Events subscribes to the stream's unified event channel, translates each
// event into AG-UI protocol events, and returns a channel the caller can
// range over. When the run ends (Events() closes), the bridge consults
// stream.Result() and emits a closing RUN_FINISHED / RUN_ERROR if the
// producer did not, then closes the channel.
//
// Events uses a background context for compatibility. Request-scoped bridges
// should use EventsContext so a disconnected consumer cancels the stream and
// cannot leave this fan-out goroutine blocked on its output channel.
func Events(stream adaptor.Stream, opts ...EventTranslatorOption) <-chan aguievents.Event {
	return EventsContext(context.Background(), stream, opts...)
}

// EventsContext is Events with explicit consumer lifetime. It cancels the
// underlying stream and exits promptly when ctx ends; every output send is
// cancellation-aware.
func EventsContext(ctx context.Context, stream adaptor.Stream, opts ...EventTranslatorOption) <-chan aguievents.Event {
	out := make(chan aguievents.Event, 32)
	translator := NewEventTranslator(opts...)
	if ctx == nil {
		ctx = context.Background()
	}

	go func() {
		defer close(out)
		send := func(event aguievents.Event) bool {
			select {
			case <-ctx.Done():
				stream.Cancel()
				return false
			case out <- event:
				return true
			}
		}
		for {
			select {
			case <-ctx.Done():
				stream.Cancel()
				return
			case ev, ok := <-stream.Events():
				if !ok {
					goto drained
				}
				for _, event := range translator.Translate(ev) {
					if !send(event) {
						return
					}
				}
			}
		}
	drained:
		result, err := stream.Result()
		translator.fillRunID(stream.RunID())
		for _, event := range translator.CloseResult(result, err) {
			if !send(event) {
				return
			}
		}
	}()

	return out
}

// Translate maps one adaptor.Event to zero or more AG-UI events. Stateful:
// it synthesizes missing opening lifecycle markers, buffers pre-RUN_STARTED
// output and dedupes lifecycle markers. RunFinished only supplies identity;
// CloseResult latches the authoritative terminal after Stream.Result().
func (t *EventTranslator) Translate(ev adaptor.Event) []aguievents.Event {
	t.mu.Lock()
	defer t.mu.Unlock()

	// Post-terminal suppression (CopilotKit verifyEvents rejects traffic
	// after RUN_FINISHED / RUN_ERROR).
	if t.runFinish {
		return nil
	}
	if approval, ok := ev.(*adaptor.ApprovalRequest); ok && approval == nil {
		return nil
	}

	meta := ev.Meta()
	if meta.RunID != "" {
		t.runID = meta.RunID
	}
	if t.threadID == "" && meta.ThreadKey != "" {
		t.threadID = meta.ThreadKey
	}

	switch e := ev.(type) {
	case adaptor.RunStarted:
		if e.ThreadID != "" {
			t.threadID = e.ThreadID
		}
		if meta.RunID == "" && e.RunID != "" {
			t.runID = e.RunID
		}
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

	case adaptor.RunFinished:
		if e.ThreadID != "" {
			t.threadID = e.ThreadID
		}
		if meta.RunID == "" && e.RunID != "" {
			t.runID = e.RunID
		}
		// Informational only. Stream.Result() is the sole terminal authority;
		// Events calls CloseResult after the event channel is fully drained.
		return nil
	}

	translated := t.translateNonTerminalLocked(ev)
	if len(translated) == 0 {
		return nil
	}
	if !t.runStart {
		t.pending = append(t.pending, translated...)
		return nil
	}
	return translated
}

// CloseRun emits the terminal AG-UI event for callers that do not have a
// Result to project. Hosts draining an adaptor.Stream should prefer
// CloseResult so a non-streaming Driver's final assistant text is not lost.
func (t *EventTranslator) CloseRun(err error) []aguievents.Event {
	return t.CloseResult(nil, err)
}

// CloseResult emits the terminal AG-UI event for the run based on the
// stream.Result() outcome, exactly once. When no non-empty assistant text was
// previously translated, it first projects Result.Text as one complete
// assistant message. On a business failure, the partial assistant-facing text
// comes from RunError.Result; the terminal remains RUN_ERROR.
//
//   - err == nil → RUN_FINISHED;
//   - errors.Is(err, context.Canceled) → RUN_ERROR code "run.cancelled";
//   - *adaptor.RunError → RUN_ERROR code = string(RunError.Reason);
//   - any other error → RUN_ERROR code "run.error".
//
// Idempotent: once the translator emitted a terminal event, all
// further CloseRun / CloseResult / Translate calls return nil.
func (t *EventTranslator) CloseResult(result *adaptor.Result, err error) []aguievents.Event {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.runFinish {
		return nil
	}
	t.runFinish = true

	out := t.ensureRunStartedLocked()
	out = append(out, t.subagents.flush(err)...)
	out = append(out, t.closeAllOpenLifecyclesLocked()...)
	if !t.assistantText {
		if text := resultText(result, err); strings.TrimSpace(text) != "" {
			messageID := t.runOrDefault() + ":result"
			out = append(out,
				aguievents.NewTextMessageStartEvent(messageID, aguievents.WithRole("assistant")),
				aguievents.NewTextMessageContentEvent(messageID, text),
				aguievents.NewTextMessageEndEvent(messageID),
			)
			t.assistantText = true
		}
	}

	if err == nil {
		return append(out, aguievents.NewRunFinishedEvent(t.threadOrDefault(), t.runOrDefault()))
	}

	code := "run.error"
	msg := err.Error()
	var runErr *adaptor.RunError
	switch {
	case errors.Is(err, context.Canceled):
		code = "run.cancelled"
	case errors.As(err, &runErr):
		code = string(runErr.Reason)
		msg = defaultString(runErr.Message, msg)
	}
	opts := []aguievents.RunErrorOption{aguievents.WithErrorCode(code)}
	if t.runID != "" {
		opts = append(opts, aguievents.WithRunID(t.runID))
	}
	return append(out, aguievents.NewRunErrorEvent(msg, opts...))
}

// fillRunID backfills the run identifier from the authoritative stream when
// the producer never emitted RunStarted.
func (t *EventTranslator) fillRunID(runID string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.runID == "" {
		t.runID = runID
	}
}

// translateNonTerminalLocked handles every event except the run terminal
// markers. The caller holds t.mu.
func (t *EventTranslator) translateNonTerminalLocked(ev adaptor.Event) []aguievents.Event {
	switch e := ev.(type) {
	case adaptor.TextDelta:
		return t.textDeltaLocked(e)
	case adaptor.Thinking:
		return t.thinkingLocked(e)
	case adaptor.ToolCall:
		return t.toolCallLocked(e)
	case adaptor.ToolResult:
		if e.ID == "" {
			return nil
		}
		content := toolResultContent(e.Result)
		return []aguievents.Event{aguievents.NewToolCallResultEvent(e.ID+":result", e.ID, content)}
	case adaptor.Notice:
		return t.noticeLocked(e)
	case adaptor.Dropped:
		return []aguievents.Event{customEvent("stream.dropped", droppedValue(e))}
	case adaptor.SubagentUpdate:
		return t.subagents.translate(e)
	case *adaptor.ApprovalRequest:
		return t.approvalRequestLocked(e)
	case adaptor.ProcessInfo:
		// Process chunks have no AG-UI protocol projection.
		return nil
	}
	return nil
}

func (t *EventTranslator) textDeltaLocked(e adaptor.TextDelta) []aguievents.Event {
	if e.MessageID == "" {
		return nil
	}
	switch e.Phase {
	case adaptor.PhaseStart:
		if t.activeText[e.MessageID] {
			return nil
		}
		t.activeText[e.MessageID] = true
		return []aguievents.Event{aguievents.NewTextMessageStartEvent(e.MessageID, textRoleOpt(e.Role))}
	case adaptor.PhaseEnd:
		if !t.activeText[e.MessageID] {
			return nil
		}
		delete(t.activeText, e.MessageID)
		return []aguievents.Event{aguievents.NewTextMessageEndEvent(e.MessageID)}
	default: // PhaseContent
		if e.Text == "" {
			return nil
		}
		if e.Role != adaptor.RoleUser && strings.TrimSpace(e.Text) != "" {
			t.assistantText = true
		}
		out := []aguievents.Event{}
		if !t.activeText[e.MessageID] {
			t.activeText[e.MessageID] = true
			out = append(out, aguievents.NewTextMessageStartEvent(e.MessageID, textRoleOpt(e.Role)))
		}
		return append(out, aguievents.NewTextMessageContentEvent(e.MessageID, e.Text))
	}
}

// resultText returns only the assistant-facing result layer. In particular it
// never guesses from Summary, raw process output, transcript entries, or an
// error message. Business failures carry their partial Result on RunError.
func resultText(result *adaptor.Result, err error) string {
	if result != nil {
		return result.Text
	}
	var runErr *adaptor.RunError
	if errors.As(err, &runErr) && runErr != nil && runErr.Result != nil {
		return runErr.Result.Text
	}
	return ""
}

func (t *EventTranslator) thinkingLocked(e adaptor.Thinking) []aguievents.Event {
	if e.MessageID == "" {
		return nil
	}
	switch e.Phase {
	case adaptor.PhaseStart:
		if t.activeReason[e.MessageID] {
			return nil
		}
		t.activeReason[e.MessageID] = true
		return []aguievents.Event{aguievents.NewReasoningMessageStartEvent(e.MessageID, ReasoningRole)}
	case adaptor.PhaseEnd:
		if !t.activeReason[e.MessageID] {
			return nil
		}
		delete(t.activeReason, e.MessageID)
		return []aguievents.Event{aguievents.NewReasoningMessageEndEvent(e.MessageID)}
	default:
		if e.Text == "" {
			return nil
		}
		out := []aguievents.Event{}
		if !t.activeReason[e.MessageID] {
			t.activeReason[e.MessageID] = true
			out = append(out, aguievents.NewReasoningMessageStartEvent(e.MessageID, ReasoningRole))
		}
		return append(out, aguievents.NewReasoningMessageContentEvent(e.MessageID, e.Text))
	}
}

func (t *EventTranslator) toolCallLocked(e adaptor.ToolCall) []aguievents.Event {
	if e.ID == "" {
		return nil
	}
	switch e.Phase {
	case adaptor.PhaseStart:
		if t.activeToolStart[e.ID] {
			return nil
		}
		t.activeToolStart[e.ID] = true
		out := []aguievents.Event{aguievents.NewToolCallStartEvent(e.ID, defaultString(e.Name, "tool"))}
		if e.Args != nil {
			if encoded, err := json.Marshal(e.Args); err == nil {
				out = append(out, aguievents.NewToolCallArgsEvent(e.ID, string(encoded)))
			}
		}
		return out
	case adaptor.PhaseEnd:
		out := []aguievents.Event{}
		if !t.activeToolStart[e.ID] {
			// An end-only snapshot is still meaningful when it carries the
			// provider's complete result. Synthesize its missing opening edge.
			if e.Result == nil {
				return nil
			}
			out = append(out, aguievents.NewToolCallStartEvent(e.ID, defaultString(e.Name, "tool")))
		}
		delete(t.activeToolStart, e.ID)
		out = append(out, aguievents.NewToolCallEndEvent(e.ID))
		if e.Result != nil {
			out = append(out, aguievents.NewToolCallResultEvent(e.ID+":result", e.ID, toolResultContent(e.Result)))
		}
		return out
	default: // args delta
		if e.ArgsDelta == "" {
			return nil
		}
		out := []aguievents.Event{}
		if !t.activeToolStart[e.ID] {
			t.activeToolStart[e.ID] = true
			out = append(out, aguievents.NewToolCallStartEvent(e.ID, defaultString(e.Name, "tool")))
		}
		return append(out, aguievents.NewToolCallArgsEvent(e.ID, e.ArgsDelta))
	}
}

// noticeLocked projects the Notice space:
//   - step notices → STEP_STARTED / STEP_FINISHED;
//   - approval notices → decision tool-call lifecycles (or CUSTOM);
//   - everything else (lifecycle warnings, runtime, invocation, transcript
//     items, driver pass-through kinds) → CUSTOM named by the notice kind,
//     so hosts keep degradation warnings and provider extras visible.
func (t *EventTranslator) noticeLocked(e adaptor.Notice) []aguievents.Event {
	switch e.Kind {
	case adaptor.NoticeStep:
		phase, _ := e.Data["phase"].(string)
		switch phase {
		case "started":
			return []aguievents.Event{aguievents.NewStepStartedEvent(e.Text)}
		case "finished":
			return []aguievents.Event{aguievents.NewStepFinishedEvent(e.Text)}
		}
		return []aguievents.Event{customEvent(e.Kind, noticeValue(e))}

	case adaptor.NoticeApprovalRequested:
		if t.decisionMode == DecisionAsCustom {
			return []aguievents.Event{customEvent("hitl.requested", noticeValue(e))}
		}
		return t.approvalNoticeRequestedLocked(e)

	case adaptor.NoticeApprovalResolved:
		if t.decisionMode == DecisionAsCustom {
			return []aguievents.Event{customEvent("hitl.resolved", noticeValue(e))}
		}
		return t.approvalNoticeResolvedLocked(e)
	}
	return []aguievents.Event{customEvent(defaultString(e.Kind, "codex.event"), noticeValue(e))}
}

// approvalRequestLocked projects a live *ApprovalRequest event (form B
// consumption) with full fidelity — the request carries payload, choices,
// deadline, and the agent-native tool call id, so the wire shape matches
// the stable approval wire projection key for key.
func (t *EventTranslator) approvalRequestLocked(req *adaptor.ApprovalRequest) []aguievents.Event {
	if req == nil || req.ID == "" {
		return nil
	}
	payload := map[string]any{
		"request_id":    req.ID,
		"kind":          string(req.Kind),
		"source":        req.Source,
		"prompt":        req.Title,
		"payload":       req.Details,
		"choices":       choicesToJSON(req.Choices),
		"tool_call_id":  req.ToolCallID,
		"deadline":      req.Deadline,
		"retry_attempt": req.Attempt,
	}
	if t.decisionMode == DecisionAsCustom {
		return []aguievents.Event{customEvent("hitl.requested", payload)}
	}
	toolCallID := decisionToolPrefix + req.ID
	if t.activeToolStart[toolCallID] {
		return nil
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil
	}
	t.activeToolStart[toolCallID] = true
	return []aguievents.Event{
		aguievents.NewToolCallStartEvent(toolCallID, hitlToolName(string(req.Kind), req.Source)),
		aguievents.NewToolCallArgsEvent(toolCallID, string(body)),
	}
}

// approvalNoticeRequestedLocked projects the requested Notice emitted on
// the callback / auto-resolve paths. The notice carries a reduced field
// set (request_id, kind, source, attempt, prompt); payload / choices /
// deadline are only available on the *ApprovalRequest event form.
func (t *EventTranslator) approvalNoticeRequestedLocked(e adaptor.Notice) []aguievents.Event {
	requestID, _ := e.Data["request_id"].(string)
	if requestID == "" {
		return nil
	}
	toolCallID := decisionToolPrefix + requestID
	if t.activeToolStart[toolCallID] {
		return nil
	}
	kind, _ := e.Data["kind"].(string)
	source, _ := e.Data["source"].(string)
	payload := map[string]any{
		"request_id":    requestID,
		"kind":          kind,
		"source":        source,
		"prompt":        e.Text,
		"retry_attempt": intFromAny(e.Data["attempt"]),
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil
	}
	t.activeToolStart[toolCallID] = true
	return []aguievents.Event{
		aguievents.NewToolCallStartEvent(toolCallID, hitlToolName(kind, source)),
		aguievents.NewToolCallArgsEvent(toolCallID, string(body)),
	}
}

func (t *EventTranslator) approvalNoticeResolvedLocked(e adaptor.Notice) []aguievents.Event {
	requestID, _ := e.Data["request_id"].(string)
	if requestID == "" {
		return nil
	}
	toolCallID := decisionToolPrefix + requestID
	tail := hitlResolvedTail(toolCallID, e.Data)
	if !t.activeToolStart[toolCallID] {
		// Out-of-order resolved — synthesize a start so the AG-UI stream
		// stays well-formed.
		kind, _ := e.Data["kind"].(string)
		source, _ := e.Data["source"].(string)
		start := aguievents.NewToolCallStartEvent(toolCallID, hitlToolName(kind, source))
		return append([]aguievents.Event{start}, tail...)
	}
	delete(t.activeToolStart, toolCallID)
	return tail
}

func hitlResolvedTail(toolCallID string, data map[string]any) []aguievents.Event {
	result, _ := data["result"].(string)
	choice, _ := data["choice"].(string)
	var latencyMs int64
	if d, ok := data["latency"].(time.Duration); ok {
		latencyMs = d.Milliseconds()
	}
	payload := map[string]any{
		"result": result,
		"choice": choice,
		// Preserve an answer when the resolved notice carries one and encode
		// JSON null when it does not.
		"answer":        data["answer"],
		"retry_attempt": intFromAny(data["attempt"]),
		"latency_ms":    latencyMs,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		body = []byte("{}")
	}
	return []aguievents.Event{
		aguievents.NewToolCallEndEvent(toolCallID),
		aguievents.NewToolCallResultEvent(toolCallID+":result", toolCallID, string(body)),
	}
}

// ---------------------------------------------------------------------------
// Shared translation helpers.
// ---------------------------------------------------------------------------

func (t *EventTranslator) threadOrDefault() string {
	if t.threadID != "" {
		return t.threadID
	}
	return fallbackThreadID
}

func (t *EventTranslator) runOrDefault() string {
	if t.runID != "" {
		return t.runID
	}
	return fallbackRunID
}

func (t *EventTranslator) ensureRunStartedLocked() []aguievents.Event {
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

func (t *EventTranslator) closeAllOpenLifecyclesLocked() []aguievents.Event {
	out := []aguievents.Event{}
	for _, id := range sortedActiveIDs(t.activeText) {
		out = append(out, aguievents.NewTextMessageEndEvent(id))
	}
	t.activeText = map[string]bool{}
	for _, id := range sortedActiveIDs(t.activeReason) {
		out = append(out, aguievents.NewReasoningMessageEndEvent(id))
	}
	t.activeReason = map[string]bool{}
	for _, id := range sortedActiveIDs(t.activeToolStart) {
		out = append(out, aguievents.NewToolCallEndEvent(id))
	}
	t.activeToolStart = map[string]bool{}
	return out
}

func sortedActiveIDs(active map[string]bool) []string {
	ids := make([]string, 0, len(active))
	for id := range active {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// textRoleOpt maps the public Role onto the AG-UI role option.
func textRoleOpt(r adaptor.Role) aguievents.TextMessageStartOption {
	if r == adaptor.RoleUser {
		return aguievents.WithRole("user")
	}
	return aguievents.WithRole("assistant")
}

// toolResultContent inlines common tool-result payload shapes using a
// stable preference order.
func toolResultContent(result map[string]any) string {
	if result == nil {
		return "{}"
	}
	if text, ok := result["text"].(string); ok && text != "" {
		return text
	}
	if content, ok := result["content"].(string); ok && content != "" {
		return content
	}
	if out, ok := result["output"].(string); ok && out != "" {
		if exit, ok := result["exitCode"].(int); ok {
			return out + "\n[exit=" + strconv.Itoa(exit) + "]"
		}
		return out
	}
	if status, ok := result["status"].(string); ok && status != "" {
		return status
	}
	if raw, err := json.Marshal(result); err == nil && len(raw) > 0 && string(raw) != "{}" {
		return string(raw)
	}
	return "{}"
}

func droppedValue(e adaptor.Dropped) map[string]any {
	value := map[string]any{"dropped_count": e.Count}
	if len(e.ByKind) > 0 {
		value["by_kind"] = e.ByKind
	}
	if e.FirstSequence != 0 {
		value["first_sequence"] = e.FirstSequence
	}
	if e.LastSequence != 0 {
		value["last_sequence"] = e.LastSequence
	}
	if e.Reason != "" {
		value["reason"] = e.Reason
	}
	if e.Source != "" {
		value["source"] = e.Source
	}
	if len(e.Details) > 0 {
		value["details"] = e.Details
	}
	return value
}

func noticeValue(e adaptor.Notice) map[string]any {
	value := map[string]any{}
	for k, v := range e.Data {
		value[k] = v
	}
	if e.Text != "" {
		if _, taken := value["text"]; !taken {
			value["text"] = e.Text
		}
	}
	return value
}

func customEvent(name string, value map[string]any) aguievents.Event {
	if name == "" {
		name = "codex.event"
	}
	opts := []aguievents.CustomEventOption{}
	if len(value) > 0 {
		opts = append(opts, aguievents.WithValue(value))
	}
	return aguievents.NewCustomEvent(name, opts...)
}

func intFromAny(v any) int {
	switch n := v.(type) {
	case int:
		return n
	case int32:
		return int(n)
	case int64:
		return int(n)
	case uint64:
		return int(n)
	case float64:
		return int(n)
	}
	return 0
}
