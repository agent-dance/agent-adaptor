package sse

import (
	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/capability"
	"github.com/agent-dance/agent-adaptor/todo"
	"time"
)

// Observation wire values are local translations of the public typed events.
// They deliberately do not depend on another bridge's DTO or provider parser.
type capabilityWire struct {
	InvocationID     string               `json:"invocation_id"`
	Kind             capability.Kind      `json:"kind"`
	Key              string               `json:"key"`
	Operation        string               `json:"operation"`
	Phase            capability.Phase     `json:"phase"`
	Evidence         capability.Evidence  `json:"evidence"`
	Source           capability.Source    `json:"source"`
	ScopeID          string               `json:"scope_id,omitempty"`
	ParentScopeID    string               `json:"parent_scope_id,omitempty"`
	ParentToolCallID string               `json:"parent_tool_call_id,omitempty"`
	OccurredAt       time.Time            `json:"occurred_at"`
	DurationNS       *int64               `json:"duration_ns,omitempty"`
	ErrorCode        capability.ErrorCode `json:"error_code,omitempty"`
}
type todoItemWire struct {
	ID          string      `json:"id"`
	Content     string      `json:"content"`
	Status      todo.Status `json:"status"`
	SyntheticID bool        `json:"synthetic_id"`
}
type todoWire struct {
	Items            []todoItemWire `json:"items"`
	Source           todo.Source    `json:"source"`
	ScopeID          string         `json:"scope_id,omitempty"`
	ParentScopeID    string         `json:"parent_scope_id,omitempty"`
	ParentToolCallID string         `json:"parent_tool_call_id,omitempty"`
	Revision         uint64         `json:"revision"`
	OccurredAt       time.Time      `json:"occurred_at"`
}

func capabilityValue(value capability.Invocation) capabilityWire {
	out := capabilityWire{InvocationID: value.InvocationID, Kind: value.Ref.Kind, Key: value.Ref.Key, Operation: value.Ref.Operation, Phase: value.Phase, Evidence: value.Evidence, Source: value.Source, ScopeID: value.ScopeID, ParentScopeID: value.ParentScopeID, ParentToolCallID: value.ParentToolCallID, OccurredAt: value.OccurredAt, ErrorCode: value.ErrorCode}
	if value.Duration != nil {
		n := int64(*value.Duration)
		out.DurationNS = &n
	}
	return out
}
func todoValue(value todo.Snapshot) todoWire {
	out := todoWire{Items: make([]todoItemWire, len(value.Items)), Source: value.Source, ScopeID: value.ScopeID, ParentScopeID: value.ParentScopeID, ParentToolCallID: value.ParentToolCallID, Revision: value.Revision, OccurredAt: value.OccurredAt}
	for i, item := range value.Items {
		out.Items[i] = todoItemWire{ID: item.ID, Content: item.Content, Status: item.Status, SyntheticID: item.SyntheticID}
	}
	return out
}
func sourceValue(source *adaptor.EventSourceMeta) map[string]any {
	out := map[string]any{"run_id": source.RunID, "thread_id": source.ThreadID, "turn_id": source.TurnID, "sequence": source.Sequence, "timestamp": source.Timestamp}
	for key, value := range map[string]string{"scope_id": source.ScopeID, "tool_call_id": source.ToolCallID, "invocation_id": source.InvocationID, "delegation_id": source.DelegationID} {
		if value != "" {
			out[key] = value
		}
	}
	if source.Upstream != nil {
		out["upstream"] = sourceValue(source.Upstream)
	}
	return out
}

// A source chain received outside a live sink still must not be truncated or
// recursively followed forever. Invalid depth is a visible degradation.
func sourceDepthValid(source *adaptor.EventSourceMeta) bool {
	for depth := 0; source != nil; depth++ {
		if depth >= 8 {
			return false
		}
		source = source.Upstream
	}
	return true
}
