package agui

import (
	"context"
	"errors"
	"strings"
	"time"

	aguievents "github.com/ag-ui-protocol/ag-ui/sdks/community/go/pkg/core/events"

	adaptor "github.com/agent-dance/agent-adaptor"
)

const subagentActivityType = "subagent"

// subagentActivity is the stable AG-UI activity payload projected from one
// adaptor.SubagentUpdate lifecycle. CopilotKit keeps one activity message per
// SubagentID and applies subsequent ACTIVITY_DELTA patches to it.
type subagentActivity struct {
	SubagentID       string             `json:"subagentId"`
	RunID            string             `json:"runId,omitempty"`
	ParentToolCallID string             `json:"parentToolCallId,omitempty"`
	AgentKey         string             `json:"agentKey"`
	AgentName        string             `json:"agentName,omitempty"`
	Kind             string             `json:"kind"`
	Protocol         string             `json:"protocol,omitempty"`
	Status           string             `json:"status"`
	Description      string             `json:"description,omitempty"`
	Text             string             `json:"text,omitempty"`
	Reasoning        string             `json:"reasoning,omitempty"`
	ToolCalls        []subagentToolCall `json:"toolCalls"`
	Result           any                `json:"result,omitempty"`
	Error            any                `json:"error,omitempty"`
	StartedAt        string             `json:"startedAt,omitempty"`
	UpdatedAt        string             `json:"updatedAt,omitempty"`
	DurationMS       int64              `json:"durationMs,omitempty"`
}

// subagentToolCall is one tool lifecycle nested inside a subagent activity.
type subagentToolCall struct {
	ID     string         `json:"id"`
	Name   string         `json:"name"`
	Status string         `json:"status"`
	Args   map[string]any `json:"args,omitempty"`
	Result any            `json:"result,omitempty"`
}

type subagentTracker struct {
	states map[string]*subagentActivityState
	order  []string
}

type subagentActivityState struct {
	content   subagentActivity
	toolIndex map[string]int
	started   time.Time
	terminal  bool
}

func newSubagentTracker() *subagentTracker {
	return &subagentTracker{states: map[string]*subagentActivityState{}}
}

func (t *subagentTracker) translate(update adaptor.SubagentUpdate) []aguievents.Event {
	id := subagentID(update)
	if id == "" {
		return nil
	}
	state := t.states[id]
	var out []aguievents.Event
	if state == nil {
		state = t.start(id, update)
		out = append(out, aguievents.NewActivitySnapshotEvent(id, subagentActivityType, state.content))
		if update.Kind == adaptor.SubagentStarted {
			return out
		}
	}
	if state.terminal || update.Kind == adaptor.SubagentStarted {
		return out
	}

	ops := state.apply(update)
	if len(ops) > 0 {
		out = append(out, aguievents.NewActivityDeltaEvent(id, subagentActivityType, ops))
	}
	return out
}

func (t *subagentTracker) start(id string, update adaptor.SubagentUpdate) *subagentActivityState {
	now := subagentEventTime(update)
	agentName := subagentString(update.Data, "agent_name")
	if agentName == "" {
		agentName = update.Agent
	}
	state := &subagentActivityState{
		content: subagentActivity{
			SubagentID:       id,
			RunID:            update.Meta().RunID,
			ParentToolCallID: subagentString(update.Data, "parent_tool_call_id"),
			AgentKey:         update.Agent,
			AgentName:        agentName,
			Kind:             "delegated",
			Protocol:         subagentString(update.Data, "remote_protocol"),
			Status:           "started",
			Description:      update.Delta,
			ToolCalls:        []subagentToolCall{},
			StartedAt:        now.Format(time.RFC3339Nano),
			UpdatedAt:        now.Format(time.RFC3339Nano),
		},
		toolIndex: map[string]int{},
		started:   now,
	}
	t.states[id] = state
	t.order = append(t.order, id)
	return state
}

func (s *subagentActivityState) apply(update adaptor.SubagentUpdate) []aguievents.JSONPatchOperation {
	now := subagentEventTime(update)
	kind := subagentString(update.Data, "kind")
	var ops []aguievents.JSONPatchOperation
	add := func(path string, value any) {
		ops = append(ops, aguievents.JSONPatchOperation{Op: "add", Path: path, Value: value})
	}

	switch kind {
	case "subagent.text.delta":
		if update.Delta != "" {
			s.content.Text += update.Delta
			add("/text", s.content.Text)
		}
	case "subagent.reasoning.delta":
		if update.Delta != "" {
			s.content.Reasoning += update.Delta
			add("/reasoning", s.content.Reasoning)
		}
	case "subagent.tool_call.start":
		id := subagentString(update.Data, "remote_tool_call_id")
		if id != "" {
			call := subagentToolCall{
				ID: id, Name: subagentString(update.Data, "tool_name"), Status: "running",
				Args: subagentMap(update.Data["args"]),
			}
			s.toolIndex[id] = len(s.content.ToolCalls)
			s.content.ToolCalls = append(s.content.ToolCalls, call)
			add("/toolCalls", s.content.ToolCalls)
		}
	case "subagent.tool_call.args":
		if call := s.toolCall(update); call != nil {
			call.Args = subagentMap(update.Data["args"])
			if call.Args == nil && update.Delta != "" {
				call.Args = map[string]any{"delta": update.Delta}
			}
			add("/toolCalls", s.content.ToolCalls)
		}
	case "subagent.tool_call.result":
		if call := s.toolCall(update); call != nil {
			call.Result = update.Data["result"]
			call.Status = "completed"
			add("/toolCalls", s.content.ToolCalls)
		}
	case "subagent.tool_call.end":
		if call := s.toolCall(update); call != nil {
			call.Status = "completed"
			add("/toolCalls", s.content.ToolCalls)
		}
	case "subagent.status", "subagent.custom", "subagent.stream.dropped", "subagent.artifact":
		if update.Delta != "" {
			s.content.Description = update.Delta
			add("/description", s.content.Description)
		}
	}

	if update.Kind == adaptor.SubagentFinished {
		s.content.Status = terminalSubagentStatus(kind, subagentString(update.Data, "status"))
		s.terminal = true
		add("/status", s.content.Status)
		if text := subagentString(update.Data, "text"); text != "" {
			s.content.Text = text
			add("/text", text)
		}
		if result, ok := update.Data["result"]; ok {
			s.content.Result = result
			add("/result", result)
		}
		if failure, ok := update.Data["error"]; ok {
			s.content.Error = failure
			add("/error", failure)
		}
		s.content.DurationMS = max(now.Sub(s.started).Milliseconds(), 0)
		add("/durationMs", s.content.DurationMS)
	} else if status := runningSubagentStatus(subagentString(update.Data, "status")); status != "" && status != s.content.Status {
		s.content.Status = status
		add("/status", status)
	} else if s.content.Status == "started" {
		s.content.Status = "running"
		add("/status", "running")
	}

	s.content.UpdatedAt = now.Format(time.RFC3339Nano)
	add("/updatedAt", s.content.UpdatedAt)
	return ops
}

func (s *subagentActivityState) toolCall(update adaptor.SubagentUpdate) *subagentToolCall {
	id := subagentString(update.Data, "remote_tool_call_id")
	index, ok := s.toolIndex[id]
	if !ok || index < 0 || index >= len(s.content.ToolCalls) {
		return nil
	}
	return &s.content.ToolCalls[index]
}

func (t *subagentTracker) flush(err error) []aguievents.Event {
	var out []aguievents.Event
	for _, id := range t.order {
		state := t.states[id]
		if state == nil || state.terminal {
			continue
		}
		status := "failed"
		code := "subagent_terminal_missing"
		message := "parent run ended before the subagent reported a terminal event"
		if errors.Is(err, context.Canceled) {
			status = "cancelled"
			code = "parent_cancelled"
			message = "parent run was cancelled"
		} else if err != nil {
			message = err.Error()
		}
		now := time.Now().UTC()
		state.terminal = true
		state.content.Status = status
		state.content.Error = map[string]any{"code": code, "message": message}
		state.content.UpdatedAt = now.Format(time.RFC3339Nano)
		state.content.DurationMS = max(now.Sub(state.started).Milliseconds(), 0)
		out = append(out, aguievents.NewActivityDeltaEvent(id, subagentActivityType, []aguievents.JSONPatchOperation{
			{Op: "add", Path: "/status", Value: state.content.Status},
			{Op: "add", Path: "/error", Value: state.content.Error},
			{Op: "add", Path: "/updatedAt", Value: state.content.UpdatedAt},
			{Op: "add", Path: "/durationMs", Value: state.content.DurationMS},
		}))
	}
	return out
}

func subagentID(update adaptor.SubagentUpdate) string {
	if id := subagentString(update.Data, "delegation_id"); id != "" {
		return id
	}
	if update.Agent == "" {
		return ""
	}
	if runID := update.Meta().RunID; runID != "" {
		return runID + ":subagent:" + update.Agent
	}
	return "subagent:" + update.Agent
}

func subagentEventTime(update adaptor.SubagentUpdate) time.Time {
	if raw, ok := update.Data["time"]; ok {
		switch value := raw.(type) {
		case time.Time:
			if !value.IsZero() {
				return value.UTC()
			}
		case string:
			if parsed, err := time.Parse(time.RFC3339Nano, value); err == nil {
				return parsed.UTC()
			}
		}
	}
	if observed := update.Meta().Time; !observed.IsZero() {
		return observed.UTC()
	}
	return time.Now().UTC()
}

func subagentString(data map[string]any, key string) string {
	if data == nil {
		return ""
	}
	value, _ := data[key].(string)
	return strings.TrimSpace(value)
}

func subagentMap(value any) map[string]any {
	if value == nil {
		return nil
	}
	if typed, ok := value.(map[string]any); ok {
		return typed
	}
	return map[string]any{"value": value}
}

func runningSubagentStatus(status string) string {
	switch strings.ToLower(status) {
	case "started", "submitted", "working", "running":
		return "running"
	case "input-required", "input_required":
		return "input_required"
	}
	return ""
}

func terminalSubagentStatus(kind, status string) string {
	switch kind {
	case "subagent.failed":
		return "failed"
	case "subagent.cancelled":
		return "cancelled"
	case "subagent.input_required":
		return "input_required"
	}
	switch strings.ToLower(status) {
	case "failed", "error":
		return "failed"
	case "cancelled", "canceled":
		return "cancelled"
	case "input-required", "input_required":
		return "input_required"
	case "completed", "succeeded", "success":
		return "completed"
	}
	return "completed"
}
