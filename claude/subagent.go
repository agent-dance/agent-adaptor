package claude

import (
	agentadaptor "github.com/agent-dance/agent-adaptor"
)

// claudeSubagentScope tracks an open child-agent scope within a parent run.
// Keyed in claudeParser.subagentScopes by SubagentID.
type claudeSubagentScope struct {
	ID         string // stable within the parent RunID
	Name       string // subagent_type / display name
	ToolCallID string // parent_tool_use_id that spawned this scope
	ended      bool   // true once StreamSubagentEnd has been emitted
}

func (s *claudeSubagentScope) subagentRef() *agentadaptor.SubagentRef {
	return &agentadaptor.SubagentRef{
		ID:         s.ID,
		Name:       s.Name,
		Kind:       "native",
		ToolCallID: s.ToolCallID,
	}
}

// handleSubagentTaskStarted processes a system{subtype:task_started} event.
// It creates a new subagent scope and emits StreamSubagentStart.
func (p *claudeParser) handleSubagentTaskStarted(payload map[string]any) {
	if p.stream == nil {
		return
	}
	taskID := claudeTopLevelString(payload, "task_id", "taskId")
	agentID := claudeTopLevelString(payload, "agent_id", "agentId")
	subagentType := claudeTopLevelString(payload, "subagent_type", "subagentType")
	parentToolUseID := claudeTopLevelString(payload, "parent_tool_use_id", "parentToolUseId")

	// Prefer task_id, then agent_id, then derive from parent_tool_use_id.
	id := taskID
	if id == "" {
		id = agentID
	}
	if id == "" && parentToolUseID != "" {
		id = "subagent-" + parentToolUseID
	}
	if id == "" {
		return
	}

	if p.subagentScopes == nil {
		p.subagentScopes = map[string]*claudeSubagentScope{}
	}
	if p.toolCallToSubagentID == nil {
		p.toolCallToSubagentID = map[string]string{}
	}

	// Idempotent: skip if a scope for this ID (or tool_use_id) was already opened.
	if _, exists := p.subagentScopes[id]; exists {
		return
	}
	if parentToolUseID != "" {
		if _, exists := p.toolCallToSubagentID[parentToolUseID]; exists {
			return
		}
	}

	scope := &claudeSubagentScope{
		ID:         id,
		Name:       subagentType,
		ToolCallID: parentToolUseID,
	}
	p.subagentScopes[id] = scope
	if parentToolUseID != "" {
		p.toolCallToSubagentID[parentToolUseID] = id
	}

	p.emitSubagentStart(scope)
}

// handleSubagentTaskProgress processes a system{subtype:task_progress} event
// and emits StreamSubagentStatus.
func (p *claudeParser) handleSubagentTaskProgress(payload map[string]any) {
	if p.stream == nil {
		return
	}
	taskID := claudeTopLevelString(payload, "task_id", "taskId")
	parentToolUseID := claudeTopLevelString(payload, "parent_tool_use_id", "parentToolUseId")

	scope := p.lookupSubagentScope(taskID, parentToolUseID)
	if scope == nil || scope.ended {
		return
	}

	description := claudeTopLevelString(payload, "description")
	lastToolName := claudeTopLevelString(payload, "last_tool_name", "lastToolName")

	status := description
	if status == "" {
		status = lastToolName
	}

	raw := map[string]any{}
	if description != "" {
		raw["description"] = description
	}
	if lastToolName != "" {
		raw["last_tool_name"] = lastToolName
	}

	pl := p.stream.basePayload()
	pl.Kind = agentadaptor.StreamSubagentStatus
	pl.Subagent = scope.subagentRef()
	pl.Delta = status
	if len(raw) > 0 {
		pl.Raw = raw
	}
	p.stream.emitStream(pl)
}

// handleSubagentTaskNotification processes a system{subtype:task_notification}
// event. Terminal statuses (completed/failed/cancelled/input_required) close
// the scope and emit StreamSubagentEnd exactly once.
func (p *claudeParser) handleSubagentTaskNotification(payload map[string]any) {
	if p.stream == nil {
		return
	}
	status := claudeTopLevelString(payload, "status")
	taskID := claudeTopLevelString(payload, "task_id", "taskId")
	parentToolUseID := claudeTopLevelString(payload, "parent_tool_use_id", "parentToolUseId")
	summary := claudeTopLevelString(payload, "summary", "message")

	scope := p.lookupSubagentScope(taskID, parentToolUseID)
	if scope == nil || scope.ended {
		return
	}

	switch status {
	case "completed", "failed", "cancelled", "input_required":
	default:
		// Not a terminal notification; emit status instead.
		if status != "" {
			p.handleSubagentTaskProgress(payload)
		}
		return
	}

	result := map[string]any{"status": status}
	if summary != "" {
		result["summary"] = summary
	}
	p.emitSubagentEnd(scope, result)
}

// ensureSubagentScope returns or creates an open scope for the given
// parentToolUseID. Used when child events arrive before task_started.
func (p *claudeParser) ensureSubagentScope(parentToolUseID, taskID, name string) *claudeSubagentScope {
	if parentToolUseID != "" && p.toolCallToSubagentID != nil {
		if id, ok := p.toolCallToSubagentID[parentToolUseID]; ok {
			return p.subagentScopes[id]
		}
	}
	if taskID != "" && p.subagentScopes != nil {
		if scope, ok := p.subagentScopes[taskID]; ok {
			return scope
		}
	}

	id := taskID
	if id == "" && parentToolUseID != "" {
		id = "subagent-" + parentToolUseID
	}
	if id == "" {
		return nil
	}

	if p.subagentScopes == nil {
		p.subagentScopes = map[string]*claudeSubagentScope{}
	}
	if p.toolCallToSubagentID == nil {
		p.toolCallToSubagentID = map[string]string{}
	}

	scope := &claudeSubagentScope{
		ID:         id,
		Name:       name,
		ToolCallID: parentToolUseID,
	}
	p.subagentScopes[id] = scope
	if parentToolUseID != "" {
		p.toolCallToSubagentID[parentToolUseID] = id
	}

	p.emitSubagentStart(scope)
	return scope
}

// lookupSubagentScope finds an open scope by task ID or parent_tool_use_id.
func (p *claudeParser) lookupSubagentScope(taskID, parentToolUseID string) *claudeSubagentScope {
	if p.subagentScopes == nil {
		return nil
	}
	if taskID != "" {
		if scope, ok := p.subagentScopes[taskID]; ok {
			return scope
		}
	}
	if parentToolUseID != "" && p.toolCallToSubagentID != nil {
		if id, ok := p.toolCallToSubagentID[parentToolUseID]; ok {
			return p.subagentScopes[id]
		}
	}
	return nil
}

func (p *claudeParser) emitSubagentStart(scope *claudeSubagentScope) {
	if p.stream == nil {
		return
	}
	pl := p.stream.basePayload()
	pl.Kind = agentadaptor.StreamSubagentStart
	pl.Subagent = scope.subagentRef()
	p.stream.emitStream(pl)
}

func (p *claudeParser) emitSubagentEnd(scope *claudeSubagentScope, result map[string]any) {
	if p.stream == nil || scope.ended {
		return
	}
	scope.ended = true
	pl := p.stream.basePayload()
	pl.Kind = agentadaptor.StreamSubagentEnd
	pl.Subagent = scope.subagentRef()
	if len(result) > 0 {
		pl.Result = result
	}
	p.stream.emitStream(pl)
}

// flushOpenSubagents closes any still-open subagent scopes with a synthetic
// failed end event. Callers must invoke it before emitting run.finished or
// run.error so no child event can follow the parent terminal event.
func (p *claudeParser) flushOpenSubagents() {
	if p.subagentScopes == nil {
		return
	}
	for _, scope := range p.subagentScopes {
		if !scope.ended {
			p.emitSubagentEnd(scope, map[string]any{
				"status":    "failed",
				"synthetic": true,
			})
		}
	}
}
