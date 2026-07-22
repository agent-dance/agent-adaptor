package agui

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	aguievents "github.com/ag-ui-protocol/ag-ui/sdks/community/go/pkg/core/events"

	agentadaptor "github.com/agent-dance/agent-adaptor"
)

// SubagentMode selects how the Translator maps subagent-scope events
// (subagent.start / subagent.status / subagent.end and subagent-scoped
// text.* / tool_call.*) to AG-UI events. API style mirrors DecisionMode.
type SubagentMode int

const (
	// SubagentAsActivity maps subagent events to AG-UI ACTIVITY_SNAPSHOT /
	// ACTIVITY_DELTA events (activityType="subagent", messageId=SubagentID).
	// This is the default.
	SubagentAsActivity SubagentMode = iota
	// SubagentAsToolCall maps subagent events to TOOL_CALL_* lifecycles so
	// CopilotKit clients can handle them with useCopilotAction.
	SubagentAsToolCall
	// SubagentAsCustom maps subagent events to AG-UI CUSTOM events, preserving
	// the legacy wire shape for hosts that already built custom renderers.
	SubagentAsCustom
)

// WithSubagentMode sets the subagent event mapping strategy. Default is
// SubagentAsActivity. Use SubagentAsCustom for backward compatibility.
func WithSubagentMode(mode SubagentMode) TranslatorOption {
	return func(t *Translator) { t.subagentMode = mode }
}

// SubagentContent is the structured payload for Activity messages that track
// subagent execution. It matches the JSON shape expected by the CopilotKit
// SubagentCard renderer (see workstream-unified-subagent-streaming.md §10.2).
type SubagentContent struct {
	SubagentID       string         `json:"subagentId"`
	RunID            string         `json:"runId,omitempty"`
	ParentToolCallID string         `json:"parentToolCallId,omitempty"`
	AgentKey         string         `json:"agentKey"`
	AgentName        string         `json:"agentName,omitempty"`
	Kind             string         `json:"kind"` // "native" | "delegated"
	Protocol         string         `json:"protocol,omitempty"`
	Status           string         `json:"status"` // "started"|"running"|"completed"|"failed"|"cancelled"|"input_required"
	Description      string         `json:"description,omitempty"`
	Text             string         `json:"text,omitempty"`
	Reasoning        string         `json:"reasoning,omitempty"`
	ToolCalls        []SubagentTC   `json:"toolCalls"`
	Result           map[string]any `json:"result,omitempty"`
	Usage            map[string]any `json:"usage,omitempty"`
	Error            map[string]any `json:"error,omitempty"`
	StartedAt        string         `json:"startedAt,omitempty"`
	UpdatedAt        string         `json:"updatedAt,omitempty"`
}

// SubagentTC is a tool-call record within a subagent scope.
type SubagentTC struct {
	ID     string         `json:"id"`
	Name   string         `json:"name"`
	Status string         `json:"status"` // "running" | "completed" | "failed"
	Args   map[string]any `json:"args,omitempty"`
	Result map[string]any `json:"result,omitempty"`
}

// subagentTracker accumulates subagent state per SubagentID and emits
// Activity events for the Translator's SubagentAsActivity mode.
// The tracker is created lazily; the zero value must not be used directly.
type subagentTracker struct {
	// states keeps the live content for each active SubagentID in order.
	states map[string]*subagentState
	order  []string
	seen   map[string]struct{}
	// tcIndex maps SubagentID → tool-call ID → index in content.ToolCalls.
	tcIndex map[string]map[string]int
}

type subagentState struct {
	content SubagentContent
}

func newSubagentTracker() *subagentTracker {
	return &subagentTracker{
		states:  map[string]*subagentState{},
		seen:    map[string]struct{}{},
		tcIndex: map[string]map[string]int{},
	}
}

// onStart initialises a new subagent scope and returns an ACTIVITY_SNAPSHOT.
func (tr *subagentTracker) onStart(p agentadaptor.StreamPayload) []aguievents.Event {
	ref := subagentRefFromPayload(p)
	if ref == nil {
		return nil
	}
	subID := ref.ID
	if _, exists := tr.seen[subID]; exists {
		return nil
	}
	tr.seen[subID] = struct{}{}
	now := payloadTime(p)
	c := SubagentContent{
		SubagentID:       subID,
		RunID:            p.RunID,
		ParentToolCallID: ref.ToolCallID,
		AgentKey:         ref.Name,
		AgentName:        ref.Name,
		Kind:             ref.Kind,
		Protocol:         ref.Protocol,
		Status:           "started",
		Description:      p.Delta,
		ToolCalls:        []SubagentTC{},
		StartedAt:        now,
		UpdatedAt:        now,
	}
	if c.AgentKey == "" {
		c.AgentKey = p.Name
		c.AgentName = p.Name
	}
	if c.Kind == "" {
		c.Kind = "native"
	}
	st := &subagentState{content: c}
	tr.order = append(tr.order, subID)
	tr.states[subID] = st
	return []aguievents.Event{aguievents.NewActivitySnapshotEvent(subID, activityTypeSubagent, c)}
}

// onStatus updates description/text and returns an ACTIVITY_DELTA.
func (tr *subagentTracker) onStatus(p agentadaptor.StreamPayload) []aguievents.Event {
	ref := subagentRefFromPayload(p)
	if ref == nil {
		return nil
	}
	subID := ref.ID
	st := tr.states[subID]
	if st == nil {
		return nil
	}
	now := payloadTime(p)
	st.content.UpdatedAt = now
	var ops []aguievents.JSONPatchOperation
	if p.Delta != "" {
		st.content.Description = p.Delta
		ops = append(ops, patch("add", "/description", p.Delta))
	}
	if len(p.Result) > 0 {
		st.content.Result = cloneMap(p.Result)
		ops = append(ops, patch("add", "/result", st.content.Result))
	}
	status := resultString(p.Result, "status")
	if status == "" {
		status = rawStr(p.Raw, "status")
	}
	status = normalizedSubagentStatus(status, "running")
	if status != "" && status != st.content.Status {
		st.content.Status = status
		ops = append(ops, patch("replace", "/status", status))
	} else if st.content.Status == "started" {
		st.content.Status = "running"
		ops = append(ops, patch("replace", "/status", "running"))
	}
	ops = append(ops, patch("replace", "/updatedAt", now))
	if len(ops) == 0 {
		return nil
	}
	return []aguievents.Event{aguievents.NewActivityDeltaEvent(subID, activityTypeSubagent, ops)}
}

// onEnd closes the subagent scope and returns an ACTIVITY_DELTA with terminal state.
func (tr *subagentTracker) onEnd(p agentadaptor.StreamPayload) []aguievents.Event {
	ref := subagentRefFromPayload(p)
	if ref == nil {
		return nil
	}
	subID := ref.ID
	st := tr.states[subID]
	if st == nil {
		return nil
	}
	now := payloadTime(p)
	status := resultString(p.Result, "status")
	if status == "" {
		status = rawStr(p.Raw, "status")
	}
	status = normalizedSubagentStatus(status, "completed")
	st.content.Status = status
	st.content.UpdatedAt = now

	ops := []aguievents.JSONPatchOperation{
		patch("replace", "/status", status),
		patch("replace", "/updatedAt", now),
	}
	if len(p.Result) > 0 {
		st.content.Result = cloneMap(p.Result)
		ops = append(ops, patch("add", "/result", st.content.Result))
	}
	if p.Usage != nil {
		st.content.Usage = map[string]any{
			"inputTokens":        p.Usage.InputTokens,
			"outputTokens":       p.Usage.OutputTokens,
			"cachedInputTokens":  p.Usage.CachedInputTokens,
			"estimatedCostMilli": p.Usage.EstimatedCostMilli,
		}
		ops = append(ops, patch("add", "/usage", st.content.Usage))
	}
	if text := resultText(p); text != "" {
		st.content.Text = text
		ops = append(ops, patch("add", "/text", text))
	} else if text := rawStr(p.Raw, "text"); text != "" {
		st.content.Text = text
		ops = append(ops, patch("add", "/text", text))
	}
	if p.Error != nil {
		errInfo := map[string]any{"code": string(p.Error.Code), "message": p.Error.Message}
		st.content.Error = errInfo
		ops = append(ops, patch("add", "/error", errInfo))
	}
	delete(tr.states, subID)
	return []aguievents.Event{aguievents.NewActivityDeltaEvent(subID, activityTypeSubagent, ops)}
}

// onToolCallStart records a new tool call and returns an ACTIVITY_DELTA.
func (tr *subagentTracker) onToolCallStart(subID string, p agentadaptor.StreamPayload) []aguievents.Event {
	st := tr.states[subID]
	if st == nil || p.ToolCallID == "" {
		return nil
	}
	tc := SubagentTC{ID: p.ToolCallID, Name: p.Name, Status: "running", Args: p.Args}
	idx := len(st.content.ToolCalls)
	st.content.ToolCalls = append(st.content.ToolCalls, tc)
	if tr.tcIndex[subID] == nil {
		tr.tcIndex[subID] = map[string]int{}
	}
	tr.tcIndex[subID][p.ToolCallID] = idx
	now := payloadTime(p)
	st.content.UpdatedAt = now
	ops := []aguievents.JSONPatchOperation{
		patch("add", "/toolCalls/-", tc),
		patch("replace", "/updatedAt", now),
	}
	return []aguievents.Event{aguievents.NewActivityDeltaEvent(subID, activityTypeSubagent, ops)}
}

func (tr *subagentTracker) onToolCallArgs(subID string, p agentadaptor.StreamPayload) []aguievents.Event {
	st := tr.states[subID]
	idx, ok := tr.toolCallIndex(subID, p.ToolCallID)
	if st == nil || !ok || p.Delta == "" {
		return nil
	}
	if st.content.ToolCalls[idx].Args == nil {
		st.content.ToolCalls[idx].Args = map[string]any{}
	}
	st.content.ToolCalls[idx].Args["delta"] = stringValue(st.content.ToolCalls[idx].Args["delta"]) + p.Delta
	now := payloadTime(p)
	st.content.UpdatedAt = now
	return []aguievents.Event{aguievents.NewActivityDeltaEvent(subID, activityTypeSubagent, []aguievents.JSONPatchOperation{
		patch("add", fmt.Sprintf("/toolCalls/%d/args", idx), st.content.ToolCalls[idx].Args),
		patch("replace", "/updatedAt", now),
	})}
}

func (tr *subagentTracker) onToolCallEnd(subID string, p agentadaptor.StreamPayload) []aguievents.Event {
	st := tr.states[subID]
	idx, ok := tr.toolCallIndex(subID, p.ToolCallID)
	if st == nil || !ok {
		return nil
	}
	st.content.ToolCalls[idx].Status = "completed"
	now := payloadTime(p)
	st.content.UpdatedAt = now
	return []aguievents.Event{aguievents.NewActivityDeltaEvent(subID, activityTypeSubagent, []aguievents.JSONPatchOperation{
		patch("replace", fmt.Sprintf("/toolCalls/%d/status", idx), "completed"),
		patch("replace", "/updatedAt", now),
	})}
}

// onToolCallResult updates an existing tool call's result and returns an ACTIVITY_DELTA.
func (tr *subagentTracker) onToolCallResult(subID string, p agentadaptor.StreamPayload) []aguievents.Event {
	st := tr.states[subID]
	if st == nil || p.ToolCallID == "" {
		return nil
	}
	idx, ok := tr.toolCallIndex(subID, p.ToolCallID)
	if !ok {
		return nil
	}
	st.content.ToolCalls[idx].Status = "completed"
	st.content.ToolCalls[idx].Result = p.Result
	now := payloadTime(p)
	st.content.UpdatedAt = now
	ops := []aguievents.JSONPatchOperation{
		patch("replace", fmt.Sprintf("/toolCalls/%d/status", idx), "completed"),
		patch("add", fmt.Sprintf("/toolCalls/%d/result", idx), p.Result),
		patch("replace", "/updatedAt", now),
	}
	return []aguievents.Event{aguievents.NewActivityDeltaEvent(subID, activityTypeSubagent, ops)}
}

// onTextDelta appends text to the subagent's accumulated text and returns an ACTIVITY_DELTA.
func (tr *subagentTracker) onTextDelta(subID string, p agentadaptor.StreamPayload) []aguievents.Event {
	st := tr.states[subID]
	if st == nil || p.Delta == "" {
		return nil
	}
	st.content.Text += p.Delta
	now := payloadTime(p)
	st.content.UpdatedAt = now
	ops := []aguievents.JSONPatchOperation{
		patch("add", "/text", st.content.Text),
		patch("replace", "/updatedAt", now),
	}
	return []aguievents.Event{aguievents.NewActivityDeltaEvent(subID, activityTypeSubagent, ops)}
}

func (tr *subagentTracker) onReasoningDelta(subID string, p agentadaptor.StreamPayload) []aguievents.Event {
	st := tr.states[subID]
	if st == nil || p.Delta == "" {
		return nil
	}
	st.content.Reasoning += p.Delta
	now := payloadTime(p)
	st.content.UpdatedAt = now
	return []aguievents.Event{aguievents.NewActivityDeltaEvent(subID, activityTypeSubagent, []aguievents.JSONPatchOperation{
		patch("add", "/reasoning", st.content.Reasoning),
		patch("replace", "/updatedAt", now),
	})}
}

func (tr *subagentTracker) toolCallIndex(subID, toolCallID string) (int, bool) {
	idx, ok := tr.tcIndex[subID][toolCallID]
	if !ok {
		return 0, false
	}
	st := tr.states[subID]
	return idx, st != nil && idx < len(st.content.ToolCalls)
}

// flushSynthetic closes all open subagent scopes with the given terminal status.
// Returns the synthetic Activity delta events.
func (tr *subagentTracker) flushSynthetic(status string, errInfo map[string]any) []aguievents.Event {
	var out []aguievents.Event
	now := time.Now().UTC().Format(time.RFC3339)
	for _, subID := range tr.order {
		st, ok := tr.states[subID]
		if !ok {
			continue
		}
		st.content.Status = status
		st.content.UpdatedAt = now
		ops := []aguievents.JSONPatchOperation{
			patch("replace", "/status", status),
			patch("replace", "/updatedAt", now),
		}
		if errInfo != nil {
			ops = append(ops, patch("add", "/error", errInfo))
		}
		delete(tr.states, subID)
		out = append(out, aguievents.NewActivityDeltaEvent(subID, activityTypeSubagent, ops))
	}
	tr.order = nil
	return out
}

// hasOpen returns true when there are active subagent scopes.
func (tr *subagentTracker) hasOpen() bool {
	return len(tr.states) > 0
}

// activityTypeSubagent is the AG-UI activityType value for subagent messages.
const activityTypeSubagent = "subagent"

// rawStr extracts a string from a StreamPayload.Raw map.
func rawStr(raw map[string]any, key string) string {
	if raw == nil {
		return ""
	}
	v, _ := raw[key].(string)
	return v
}

// subagentRefFromPayload returns the canonical strongly typed scope. The Raw
// fallback supports legacy pre-SubagentRef producers only; new producers must
// populate StreamPayload.Subagent.
func subagentRefFromPayload(p agentadaptor.StreamPayload) *agentadaptor.SubagentRef {
	if p.Subagent != nil && p.Subagent.ID != "" {
		return p.Subagent
	}
	id := rawStr(p.Raw, "subagent_id")
	if id == "" {
		id = rawStr(p.Raw, "delegation_id")
	}
	if id == "" {
		return nil
	}
	return &agentadaptor.SubagentRef{
		ID:         id,
		Name:       firstString(rawStr(p.Raw, "agent_key"), rawStr(p.Raw, "agent_name"), p.Name),
		Kind:       firstString(rawStr(p.Raw, "subagent_kind"), "delegated"),
		Protocol:   firstString(rawStr(p.Raw, "subagent_protocol"), rawStr(p.Raw, "remote_protocol")),
		ToolCallID: firstString(rawStr(p.Raw, "subagent_tool_call_id"), rawStr(p.Raw, "parent_tool_call_id")),
	}
}

func payloadTime(p agentadaptor.StreamPayload) string {
	if p.Timestamp.IsZero() {
		return time.Now().UTC().Format(time.RFC3339)
	}
	return p.Timestamp.UTC().Format(time.RFC3339)
}

func resultString(result map[string]any, key string) string {
	v, _ := result[key].(string)
	return v
}

func resultText(p agentadaptor.StreamPayload) string {
	if text := resultString(p.Result, "text"); text != "" {
		return text
	}
	if text := resultString(p.Result, "summary"); text != "" {
		return text
	}
	return p.Delta
}

func cloneMap(in map[string]any) map[string]any {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]any, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func stringValue(v any) string {
	s, _ := v.(string)
	return s
}

func firstString(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

// patch constructs a RFC 6902 JSON Patch operation.
func patch(op, path string, value any) aguievents.JSONPatchOperation {
	return aguievents.JSONPatchOperation{Op: op, Path: path, Value: value}
}

func normalizedSubagentStatus(raw, fallback string) string {
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

const subagentToolCallPrefix = "subagent:"

type subagentToolCallTracker struct {
	states map[string]*subagentToolCallState
	order  []string
	seen   map[string]struct{}
}

type subagentToolCallState struct {
	ref         agentadaptor.SubagentRef
	toolCallID  string
	status      string
	description string
	updateCount int
}

func newSubagentToolCallTracker() *subagentToolCallTracker {
	return &subagentToolCallTracker{
		states: map[string]*subagentToolCallState{},
		seen:   map[string]struct{}{},
	}
}

func (tr *subagentToolCallTracker) translate(p agentadaptor.StreamPayload) []aguievents.Event {
	ref := subagentRefFromPayload(p)
	if ref == nil {
		return nil
	}
	switch p.Kind {
	case agentadaptor.StreamSubagentStart:
		if _, exists := tr.seen[ref.ID]; exists {
			return nil
		}
		tr.seen[ref.ID] = struct{}{}
		state := &subagentToolCallState{
			ref:         *ref,
			toolCallID:  subagentToolCallPrefix + ref.ID,
			status:      "started",
			description: p.Delta,
		}
		tr.states[ref.ID] = state
		tr.order = append(tr.order, ref.ID)
		args, err := json.Marshal(map[string]any{
			"subagentId":       ref.ID,
			"parentId":         ref.ParentID,
			"parentToolCallId": ref.ToolCallID,
			"name":             ref.Name,
			"kind":             ref.Kind,
			"protocol":         ref.Protocol,
			"description":      p.Delta,
		})
		if err != nil {
			args = []byte("{}")
		}
		// TOOL_CALL_ARGS is a streamed JSON document. Lifecycle status events
		// append objects to updates; subagent.end closes the document.
		args = append(args[:len(args)-1], []byte(`,"updates":[`)...)
		return []aguievents.Event{
			aguievents.NewToolCallStartEvent(state.toolCallID, "subagent"),
			aguievents.NewToolCallArgsEvent(state.toolCallID, string(args)),
		}
	case agentadaptor.StreamSubagentStatus:
		state := tr.states[ref.ID]
		if state == nil {
			return nil
		}
		state.status = firstString(resultString(p.Result, "status"), rawStr(p.Raw, "status"), "running")
		if p.Delta != "" {
			state.description = p.Delta
		}
		update := map[string]any{"status": state.status, "description": p.Delta}
		return []aguievents.Event{
			aguievents.NewToolCallArgsEvent(state.toolCallID, state.nextUpdateChunk(update, false)),
		}
	case agentadaptor.StreamSubagentEnd:
		state := tr.states[ref.ID]
		if state == nil {
			return nil
		}
		delete(tr.states, ref.ID)
		status := firstString(resultString(p.Result, "status"), rawStr(p.Raw, "status"), "completed")
		return subagentToolCallEndEvents(state, status, resultText(p), p.Result, runFailureMap(p.Error))
	default:
		return nil
	}
}

func (tr *subagentToolCallTracker) flushSynthetic(status string, errInfo map[string]any) []aguievents.Event {
	var out []aguievents.Event
	for _, id := range tr.order {
		state, ok := tr.states[id]
		if !ok {
			continue
		}
		delete(tr.states, id)
		out = append(out, subagentToolCallEndEvents(state, status, "", nil, errInfo)...)
	}
	tr.order = nil
	return out
}

func subagentToolCallEndEvents(
	state *subagentToolCallState,
	status string,
	text string,
	result map[string]any,
	errInfo map[string]any,
) []aguievents.Event {
	terminalArgs := state.nextUpdateChunk(map[string]any{
		"status":      status,
		"description": state.description,
	}, true)
	body, err := json.Marshal(map[string]any{
		"subagentId":  state.ref.ID,
		"status":      status,
		"description": state.description,
		"text":        text,
		"result":      result,
		"error":       errInfo,
	})
	if err != nil {
		body = []byte("{}")
	}
	return []aguievents.Event{
		aguievents.NewToolCallArgsEvent(state.toolCallID, terminalArgs),
		aguievents.NewToolCallEndEvent(state.toolCallID),
		aguievents.NewToolCallResultEvent(state.toolCallID+":result", state.toolCallID, string(body)),
	}
}

func (state *subagentToolCallState) nextUpdateChunk(update map[string]any, closeDocument bool) string {
	body, err := json.Marshal(update)
	if err != nil {
		body = []byte("{}")
	}
	prefix := ""
	if state.updateCount > 0 {
		prefix = ","
	}
	state.updateCount++
	suffix := ""
	if closeDocument {
		suffix = "]}"
	}
	return prefix + string(body) + suffix
}

func runFailureMap(failure *agentadaptor.RunFailure) map[string]any {
	if failure == nil {
		return nil
	}
	return map[string]any{"code": string(failure.Code), "message": failure.Message}
}
