package subagentstream

import (
	"fmt"
	"strings"
	"time"

	aguievents "github.com/ag-ui-protocol/ag-ui/sdks/community/go/pkg/core/events"

	"github.com/agent-dance/agent-adaptor/pkg/bridges/agui"
	"github.com/agent-dance/agent-adaptor/pkg/hosttools/a2adelegation"
)

// activityAggregator converts A2A DelegationEvents into AG-UI ACTIVITY_SNAPSHOT /
// ACTIVITY_DELTA events, keyed by DelegationID (= SubagentID).
//
// One aggregator instance is owned by each Wrap/WrapAGUI call so state never
// leaks between runs.
type activityAggregator struct {
	states  map[string]*delegationActivity
	order   []string
	seen    map[string]struct{}
	tcIndex map[string]map[string]int // delegationID → toolCallID → slice index
}

type delegationActivity struct {
	content agui.SubagentContent
}

func newActivityAggregator() *activityAggregator {
	return &activityAggregator{
		states:  map[string]*delegationActivity{},
		seen:    map[string]struct{}{},
		tcIndex: map[string]map[string]int{},
	}
}

// Process converts one DelegationEvent into zero or more Activity AG-UI events.
func (a *activityAggregator) Process(ev a2adelegation.DelegationEvent) []aguievents.Event {
	switch ev.Kind {
	case a2adelegation.DelegationStarted:
		return a.onStarted(ev)
	case a2adelegation.DelegationStatus:
		return a.onStatus(ev)
	case a2adelegation.DelegationTextStart:
		return nil // no-op; text is accumulated via DelegationTextDelta
	case a2adelegation.DelegationTextDelta:
		return a.onTextDelta(ev)
	case a2adelegation.DelegationTextEnd:
		return nil // no-op; text is already accumulated
	case a2adelegation.DelegationReasoningStart, a2adelegation.DelegationReasoningEnd:
		return nil
	case a2adelegation.DelegationReasoningDelta:
		return a.onReasoningDelta(ev)
	case a2adelegation.DelegationToolCallStart:
		return a.onToolCallStart(ev)
	case a2adelegation.DelegationToolCallArgs:
		return a.onToolCallArgs(ev)
	case a2adelegation.DelegationToolCallResult:
		return a.onToolCallResult(ev)
	case a2adelegation.DelegationToolCallEnd:
		return nil
	case a2adelegation.DelegationArtifactCreated:
		return a.onArtifact(ev)
	case a2adelegation.DelegationFinished:
		return a.onTerminal(ev, "completed", nil)
	case a2adelegation.DelegationFailed:
		return a.onTerminal(ev, "failed", delegationErrInfo(ev.Error))
	case a2adelegation.DelegationCancelled:
		return a.onTerminal(ev, "cancelled", delegationErrInfo(ev.Error))
	case a2adelegation.DelegationInputRequired:
		return a.onTerminal(ev, "input_required", delegationErrInfo(ev.Error))
	default:
		return nil
	}
}

// FlushSynthetic generates terminal Activity delta events for all open
// (non-terminated) subagent scopes. Call before emitting the parent terminal
// event.
func (a *activityAggregator) FlushSynthetic(status string, err *a2adelegation.DelegationError) []aguievents.Event {
	var out []aguievents.Event
	now := time.Now().UTC().Format(time.RFC3339)
	errInfo := delegationErrInfo(err)
	for _, id := range a.order {
		st, ok := a.states[id]
		if !ok {
			continue
		}
		st.content.Status = status
		st.content.UpdatedAt = now
		ops := []aguievents.JSONPatchOperation{
			apatch("replace", "/status", status),
			apatch("replace", "/updatedAt", now),
		}
		if errInfo != nil {
			ops = append(ops, apatch("add", "/error", errInfo))
		}
		delete(a.states, id)
		out = append(out, aguievents.NewActivityDeltaEvent(id, activityType, ops))
	}
	a.order = nil
	return out
}

// hasOpen reports whether there are any active (non-terminal) subagent scopes.
func (a *activityAggregator) hasOpen() bool {
	return len(a.states) > 0
}

// ---------------------------------------------------------------------------
// internal event handlers
// ---------------------------------------------------------------------------

func (a *activityAggregator) onStarted(ev a2adelegation.DelegationEvent) []aguievents.Event {
	if ev.DelegationID == "" {
		return nil
	}
	if _, exists := a.seen[ev.DelegationID]; exists {
		return nil
	}
	a.seen[ev.DelegationID] = struct{}{}
	now := nowRFC3339(ev.Time)
	c := agui.SubagentContent{
		SubagentID:       ev.DelegationID,
		RunID:            ev.RunID,
		ParentToolCallID: ev.ParentToolCallID,
		AgentKey:         ev.AgentKey,
		AgentName:        defaultStr(ev.AgentName, ev.AgentKey),
		Kind:             "delegated",
		Protocol:         ev.Protocol,
		Status:           "started",
		ToolCalls:        []agui.SubagentTC{},
		StartedAt:        now,
		UpdatedAt:        now,
	}
	a.order = append(a.order, ev.DelegationID)
	a.states[ev.DelegationID] = &delegationActivity{content: c}
	return []aguievents.Event{aguievents.NewActivitySnapshotEvent(ev.DelegationID, activityType, c)}
}

func (a *activityAggregator) onStatus(ev a2adelegation.DelegationEvent) []aguievents.Event {
	st := a.states[ev.DelegationID]
	if st == nil {
		return nil
	}
	now := nowRFC3339(ev.Time)
	st.content.UpdatedAt = now
	ops := []aguievents.JSONPatchOperation{apatch("replace", "/updatedAt", now)}
	if ev.Delta != "" {
		st.content.Description = ev.Delta
		ops = append(ops, apatch("add", "/description", ev.Delta))
	}
	if ev.Status != "" {
		status := activityStatus(ev.Status, "running")
		if status != st.content.Status {
			st.content.Status = status
			ops = append(ops, apatch("replace", "/status", status))
		}
	} else if st.content.Status == "started" {
		st.content.Status = "running"
		ops = append(ops, apatch("replace", "/status", "running"))
	}
	return []aguievents.Event{aguievents.NewActivityDeltaEvent(ev.DelegationID, activityType, ops)}
}

func (a *activityAggregator) onTextDelta(ev a2adelegation.DelegationEvent) []aguievents.Event {
	st := a.states[ev.DelegationID]
	if st == nil || ev.Delta == "" {
		return nil
	}
	now := nowRFC3339(ev.Time)
	st.content.Text += ev.Delta
	st.content.UpdatedAt = now
	ops := []aguievents.JSONPatchOperation{
		apatch("add", "/text", st.content.Text),
		apatch("replace", "/updatedAt", now),
	}
	return []aguievents.Event{aguievents.NewActivityDeltaEvent(ev.DelegationID, activityType, ops)}
}

func (a *activityAggregator) onReasoningDelta(ev a2adelegation.DelegationEvent) []aguievents.Event {
	st := a.states[ev.DelegationID]
	if st == nil || ev.Delta == "" {
		return nil
	}
	now := nowRFC3339(ev.Time)
	st.content.Reasoning += ev.Delta
	st.content.UpdatedAt = now
	return []aguievents.Event{aguievents.NewActivityDeltaEvent(ev.DelegationID, activityType, []aguievents.JSONPatchOperation{
		apatch("add", "/reasoning", st.content.Reasoning),
		apatch("replace", "/updatedAt", now),
	})}
}

func (a *activityAggregator) onToolCallStart(ev a2adelegation.DelegationEvent) []aguievents.Event {
	st := a.states[ev.DelegationID]
	if st == nil || ev.RemoteToolCallID == "" {
		return nil
	}
	now := nowRFC3339(ev.Time)
	st.content.UpdatedAt = now
	args, _ := ev.Args.(map[string]any)
	tc := agui.SubagentTC{
		ID:     ev.RemoteToolCallID,
		Name:   ev.ToolName,
		Status: "running",
		Args:   args,
	}
	idx := len(st.content.ToolCalls)
	st.content.ToolCalls = append(st.content.ToolCalls, tc)
	if a.tcIndex[ev.DelegationID] == nil {
		a.tcIndex[ev.DelegationID] = map[string]int{}
	}
	a.tcIndex[ev.DelegationID][ev.RemoteToolCallID] = idx
	ops := []aguievents.JSONPatchOperation{
		apatch("add", "/toolCalls/-", tc),
		apatch("replace", "/updatedAt", now),
	}
	return []aguievents.Event{aguievents.NewActivityDeltaEvent(ev.DelegationID, activityType, ops)}
}

func (a *activityAggregator) onToolCallArgs(ev a2adelegation.DelegationEvent) []aguievents.Event {
	st := a.states[ev.DelegationID]
	if st == nil || ev.RemoteToolCallID == "" || ev.Delta == "" {
		return nil
	}
	idxMap := a.tcIndex[ev.DelegationID]
	idx, ok := idxMap[ev.RemoteToolCallID]
	if !ok || idx >= len(st.content.ToolCalls) {
		return nil
	}
	now := nowRFC3339(ev.Time)
	st.content.UpdatedAt = now
	if st.content.ToolCalls[idx].Args == nil {
		st.content.ToolCalls[idx].Args = map[string]any{}
	}
	current, _ := st.content.ToolCalls[idx].Args["delta"].(string)
	st.content.ToolCalls[idx].Args["delta"] = current + ev.Delta
	ops := []aguievents.JSONPatchOperation{
		apatch("add", fmt.Sprintf("/toolCalls/%d/args", idx), st.content.ToolCalls[idx].Args),
		apatch("replace", "/updatedAt", now),
	}
	return []aguievents.Event{aguievents.NewActivityDeltaEvent(ev.DelegationID, activityType, ops)}
}

func (a *activityAggregator) onToolCallResult(ev a2adelegation.DelegationEvent) []aguievents.Event {
	st := a.states[ev.DelegationID]
	if st == nil || ev.RemoteToolCallID == "" {
		return nil
	}
	idxMap := a.tcIndex[ev.DelegationID]
	idx, ok := idxMap[ev.RemoteToolCallID]
	if !ok || idx >= len(st.content.ToolCalls) {
		return nil
	}
	now := nowRFC3339(ev.Time)
	st.content.UpdatedAt = now
	result, _ := ev.Result.(map[string]any)
	st.content.ToolCalls[idx].Status = "completed"
	st.content.ToolCalls[idx].Result = result
	ops := []aguievents.JSONPatchOperation{
		apatch("replace", fmt.Sprintf("/toolCalls/%d/status", idx), "completed"),
		apatch("add", fmt.Sprintf("/toolCalls/%d/result", idx), result),
		apatch("replace", "/updatedAt", now),
	}
	return []aguievents.Event{aguievents.NewActivityDeltaEvent(ev.DelegationID, activityType, ops)}
}

func (a *activityAggregator) onArtifact(ev a2adelegation.DelegationEvent) []aguievents.Event {
	st := a.states[ev.DelegationID]
	if st == nil {
		return nil
	}
	now := nowRFC3339(ev.Time)
	st.content.UpdatedAt = now
	status := "running"
	if ev.Status != "" {
		status = activityStatus(ev.Status, "running")
	}
	if st.content.Status == "started" || status != st.content.Status {
		st.content.Status = status
	}
	ops := []aguievents.JSONPatchOperation{
		apatch("replace", "/status", st.content.Status),
		apatch("replace", "/updatedAt", now),
	}
	return []aguievents.Event{aguievents.NewActivityDeltaEvent(ev.DelegationID, activityType, ops)}
}

func (a *activityAggregator) onTerminal(ev a2adelegation.DelegationEvent, status string, errInfo map[string]any) []aguievents.Event {
	st := a.states[ev.DelegationID]
	if st == nil {
		return nil
	}
	now := nowRFC3339(ev.Time)
	st.content.Status = status
	st.content.UpdatedAt = now
	ops := []aguievents.JSONPatchOperation{
		apatch("replace", "/status", status),
		apatch("replace", "/updatedAt", now),
	}
	st.content.Result = map[string]any{"status": status}
	if ev.Text != "" {
		st.content.Result["text"] = ev.Text
	}
	ops = append(ops, apatch("add", "/result", st.content.Result))
	if ev.Text != "" {
		st.content.Text = ev.Text
		ops = append(ops, apatch("add", "/text", ev.Text))
	}
	if errInfo != nil {
		st.content.Error = errInfo
		ops = append(ops, apatch("add", "/error", errInfo))
	}
	delete(a.states, ev.DelegationID)
	return []aguievents.Event{aguievents.NewActivityDeltaEvent(ev.DelegationID, activityType, ops)}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

const activityType = "subagent"

func apatch(op, path string, value any) aguievents.JSONPatchOperation {
	return aguievents.JSONPatchOperation{Op: op, Path: path, Value: value}
}

func activityStatus(raw, fallback string) string {
	switch strings.ToUpper(strings.TrimSpace(raw)) {
	case "STARTED", "PENDING", "PENDINGINIT", "TASK_STATE_SUBMITTED":
		return "started"
	case "RUNNING", "WORKING", "INPROGRESS", "IN_PROGRESS", "INTERACTED", "TASK_STATE_WORKING":
		return "running"
	case "COMPLETED", "TASK_STATE_COMPLETED":
		return "completed"
	case "FAILED", "ERRORED", "REJECTED", "TASK_STATE_FAILED", "TASK_STATE_REJECTED":
		return "failed"
	case "CANCELLED", "CANCELED", "INTERRUPTED", "SHUTDOWN", "TASK_STATE_CANCELED":
		return "cancelled"
	case "INPUT_REQUIRED", "AUTH_REQUIRED", "TASK_STATE_INPUT_REQUIRED", "TASK_STATE_AUTH_REQUIRED":
		return "input_required"
	default:
		return fallback
	}
}

func nowRFC3339(t time.Time) string {
	if t.IsZero() {
		t = time.Now().UTC()
	}
	return t.UTC().Format(time.RFC3339)
}

func defaultStr(s, fallback string) string {
	if s != "" {
		return s
	}
	return fallback
}

func delegationErrInfo(err *a2adelegation.DelegationError) map[string]any {
	if err == nil {
		return nil
	}
	return map[string]any{"code": err.Code, "message": err.Message}
}
