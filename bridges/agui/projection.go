package agui

import (
	"encoding/json"
	"math"
	"math/big"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	events "github.com/ag-ui-protocol/ag-ui/sdks/community/go/pkg/core/events"
)

// ProjectionHook redacts, drops, or expands projected content and diagnostics.
// Run boundaries (RUN_STARTED, RUN_FINISHED and RUN_ERROR) bypass the hook so
// it cannot remove or rewrite the run envelope. Hosts must preserve content
// lifecycles in replacement events. Hooks should be deterministic for replay
// and must not call back into their translator.
type ProjectionHook func(events.Event) []events.Event

// WithProjectionHook installs the host's redaction/extension policy.
func WithProjectionHook(h ProjectionHook) EventTranslatorOption {
	return func(t *EventTranslator) { t.projectionHook = h }
}

// WithRunIdentity separates public request identity from SDK/provider IDs.
// Without it, low-level callers retain SDK/provider identity fallback behavior.
func WithRunIdentity(threadID, runID, parentRunID string) EventTranslatorOption {
	return func(t *EventTranslator) {
		t.publicIdentity = true
		t.threadID = threadID
		t.runID = runID
		t.parentRunID = parentRunID
	}
}

// WithInputDiagnostics emits warnings immediately after RUN_STARTED.
func WithInputDiagnostics(d []Diagnostic) EventTranslatorOption {
	return func(t *EventTranslator) { t.diagnostics = append([]Diagnostic(nil), d...) }
}

// WithToolContentLimit bounds tool-result content in bytes. Non-positive values
// select the default 64 KiB limit. Truncation never splits a UTF-8 code point.
func WithToolContentLimit(n int) EventTranslatorOption {
	return func(t *EventTranslator) {
		t.toolContentLimit = 64 << 10
		if n > 0 {
			t.toolContentLimit = n
		}
	}
}

// RunStartedEvent adds the mandatory AG-UI 1.0 version and optional parent ID
// absent from the upstream Go SDK's run-start shape.
type RunStartedEvent struct {
	// RunStartedEvent supplies the standard SDK run-start fields.
	*events.RunStartedEvent
	// ProtocolVersion is the bridge's producer protocol version, always "1.0".
	ProtocolVersion string `json:"protocolVersion"`
	// ParentRunID identifies the public parent run when provided by the caller.
	ParentRunID string `json:"parentRunId,omitempty"`
	// Input optionally echoes recorded input messages. Clients add only IDs they
	// do not already own; this seeds faithful history without duplicating text.
	Input *RunAgentInput `json:"input,omitempty"`
}

// ToJSON serializes the standard fields together with version and parent identity.
func (e *RunStartedEvent) ToJSON() ([]byte, error) {
	return json.Marshal(e)
}

func (t *EventTranslator) startedEvent() events.Event {
	// Freeze even fallback identities: later metadata is audit information,
	// never a reason to change the already-published run envelope.
	t.threadID = t.threadOrDefault()
	t.runID = t.runOrDefault()
	e := &RunStartedEvent{
		RunStartedEvent: events.NewRunStartedEvent(t.threadOrDefault(), t.runOrDefault()),
		ProtocolVersion: "1.0",
		ParentRunID:     t.parentRunID,
	}
	t.boundaryMetadata(e)
	return e
}

func (t *EventTranslator) boundaryMetadata(e events.Event) {
	m := map[string]any{"sdkRunId": t.sdkRunID}
	if t.providerThreadID != "" {
		m["providerThreadId"] = t.providerThreadID
	}
	if t.threadKey != "" {
		m["threadKey"] = t.threadKey
	}
	if e.Type() == events.EventTypeRunError {
		m["threadId"] = t.threadOrDefault()
		m["runId"] = t.runOrDefault()
	}
	e.GetBaseEvent().Metadata = map[string]any{"agentAdaptor": m}
}

func (t *EventTranslator) warningEvents() []events.Event {
	var out []events.Event
	for _, d := range t.diagnostics {
		out = append(out, customEvent("agentAdaptor.input.warning", map[string]any{
			"code": d.Code, "message": d.Message, "path": d.Path,
		}))
	}
	t.diagnostics = nil
	return out
}

func (t *EventTranslator) project(in []events.Event) []events.Event {
	for _, e := range in {
		if t.eventClock != nil {
			e.GetBaseEvent().SetTimestamp(t.eventClock().UnixMilli())
		}
		switch tool := e.(type) {
		case *events.ToolCallArgsEvent:
			tool.SubagentRunID = t.toolSubagent(tool.ToolCallID)
		case *events.ToolCallEndEvent:
			tool.SubagentRunID = t.toolSubagent(tool.ToolCallID)
		}
	}
	if t.projectionHook == nil {
		return in
	}
	var out []events.Event
	for _, e := range in {
		switch e.Type() {
		case events.EventTypeRunStarted, events.EventTypeRunFinished, events.EventTypeRunError:
			out = append(out, e)
		default:
			out = append(out, t.projectionHook(e)...)
		}
	}
	return out
}

func exitCode(m map[string]any) (int64, bool) {
	v, ok := m["exitCode"]
	if !ok {
		v = m["exit_code"]
	}
	switch n := v.(type) {
	case int:
		return int64(n), true
	case int64:
		return n, true
	case float64:
		if math.IsNaN(n) || math.IsInf(n, 0) || math.Trunc(n) != n {
			return 0, false
		}
		// Stay within JSON's exact integer range for replayed floating-point values.
		if n >= -9007199254740991 && n <= 9007199254740991 {
			return int64(n), true
		}
	case json.Number:
		if !json.Valid([]byte(n)) {
			return 0, false
		}
		i, err := n.Int64()
		if err == nil {
			return i, true
		}
		// Parse decimal/exponent forms exactly. Float64 would round a
		// fractional exit code such as 1.00000000000000000001 to 1.
		// Bound exponent expansion before constructing an exact rational.
		// Outside this range a nonzero value cannot be an int64 integer.
		mantissa, exponent, hasExponent := strings.Cut(strings.ToLower(string(n)), "e")
		if hasExponent {
			e, err := strconv.ParseInt(exponent, 10, 64)
			if err != nil || e > int64(len(mantissa)+19) || e < -int64(len(mantissa)) {
				return 0, false
			}
		}
		if exact, ok := new(big.Rat).SetString(string(n)); ok && exact.IsInt() && exact.Num().IsInt64() {
			return exact.Num().Int64(), true
		}
	}
	return 0, false
}

// toolResultContent inlines known text payloads in a stable preference order.
func toolResultContent(m map[string]any) string {
	for _, k := range []string{"text", "content", "output"} {
		if text, ok := m[k].(string); ok && (text != "" || k == "output") {
			if k == "output" {
				if n, ok := exitCode(m); ok {
					text += "\n[exit=" + strconv.FormatInt(n, 10) + "]"
				}
			}
			return text
		}
	}
	if s, ok := m["status"].(string); ok && s != "" {
		return s
	}
	b, _ := json.Marshal(m)
	if len(b) == 0 || string(b) == "null" {
		return "{}"
	}
	return string(b)
}

func toolMetadata(m map[string]any) map[string]any {
	status := "unknown"
	s, _ := m["status"].(string)
	switch strings.ToLower(s) {
	case "completed", "complete", "succeeded", "success", "successful", "done", "approved", "answered":
		status = "succeeded"
	case "failed", "failure", "error", "declined", "denied":
		status = "failed"
	case "cancelled", "canceled", "interrupted":
		status = "cancelled"
	}
	out := map[string]any{}
	if n, ok := exitCode(m); ok {
		out["exitCode"] = n
		if n != 0 {
			status = "failed"
		} else if status == "unknown" {
			status = "succeeded"
		}
	}
	if m["is_error"] == true || m["isError"] == true || m["success"] == false {
		status = "failed"
	}
	if err, ok := m["error"].(string); ok && err != "" {
		out["error"] = err
		if status != "cancelled" {
			status = "failed"
		}
	}
	out["status"] = status
	return out
}

func (t *EventTranslator) toolResultEvent(id string, m map[string]any) events.Event {
	text := toolResultContent(m)
	meta := toolMetadata(m)
	if len(text) > t.toolContentLimit {
		meta["truncated"] = true
		meta["originalBytes"] = len(text)
		// Move the cut back to a rune boundary instead of splitting UTF-8.
		n := t.toolContentLimit
		for n > 0 && !utf8.RuneStart(text[n]) {
			n--
		}
		text = text[:n]
	}
	e := events.NewToolCallResultEvent(id+":result", id, text)
	e.Metadata = map[string]any{"agentAdaptor": meta}
	e.SubagentRunID = t.toolSubagent(id)
	return e
}

func (t *EventTranslator) toolStartEvent(id, name string) events.Event {
	e := events.NewToolCallStartEvent(id, name)
	subagent := t.toolSubagent(id)
	t.toolOwners[id] = subagent
	// TextDelta belongs to the parent agent. A child tool cannot claim that
	// message as its parent: the client enforces matching subagent ownership.
	if t.lastAssistant != "" && subagent == "" {
		parent := t.lastAssistant
		e.ParentMessageID = &parent
	}
	e.SubagentRunID = subagent
	return e
}

func (t *EventTranslator) toolSubagent(id string) string {
	if owner, started := t.toolOwners[id]; started {
		return owner
	}
	for _, key := range t.subagents.order {
		s := t.subagents.states[key]
		if _, ok := s.toolIndex[id]; ok {
			return s.content.SubagentID
		}
	}
	return ""
}

// WithEventClock supplies event timestamps before the projection hook, and the
// time used to close unfinished subagent activities. Recorded projections use
// the current record time so hooks and activity content replay deterministically.
func WithEventClock(clock func() time.Time) EventTranslatorOption {
	return func(t *EventTranslator) {
		if clock != nil {
			t.eventClock = clock
			t.subagents.clock = clock
		}
	}
}
