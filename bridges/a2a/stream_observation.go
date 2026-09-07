package a2a

import (
	"context"
	"time"

	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/capability"
	"github.com/agent-dance/agent-adaptor/todo"
)

func encodeCapability(v capability.Invocation) *AdapterCapabilityInvocationV1 {
	out := &AdapterCapabilityInvocationV1{
		InvocationID: v.InvocationID, Kind: string(v.Ref.Kind), Key: v.Ref.Key, Operation: v.Ref.Operation,
		Phase: string(v.Phase), Evidence: string(v.Evidence), Source: string(v.Source),
		ScopeID: v.ScopeID, ParentScopeID: v.ParentScopeID, ParentToolCallID: v.ParentToolCallID,
		OccurredAt: v.OccurredAt.UTC().Format(time.RFC3339Nano), ErrorCode: string(v.ErrorCode),
	}
	if v.Duration != nil {
		n := int64(*v.Duration)
		out.DurationNS = &n
	}
	return out
}
func decodeCapability(v *AdapterCapabilityInvocationV1) capability.Invocation {
	out := capability.Invocation{
		InvocationID: v.InvocationID, Ref: capability.Ref{Kind: capability.Kind(v.Kind), Key: v.Key, Operation: v.Operation},
		Phase: capability.Phase(v.Phase), Evidence: capability.Evidence(v.Evidence), Source: capability.Source(v.Source),
		ScopeID: v.ScopeID, ParentScopeID: v.ParentScopeID, ParentToolCallID: v.ParentToolCallID, ErrorCode: capability.ErrorCode(v.ErrorCode),
	}
	out.OccurredAt, _ = time.Parse(time.RFC3339Nano, v.OccurredAt)
	if v.DurationNS != nil {
		d := time.Duration(*v.DurationNS)
		out.Duration = &d
	}
	return out
}
func encodeTodo(v todo.Snapshot) *AdapterTodoSnapshotV1 {
	out := &AdapterTodoSnapshotV1{
		Items: make([]AdapterTodoItemV1, len(v.Items)), Source: string(v.Source), Revision: v.Revision,
		ScopeID: v.ScopeID, ParentScopeID: v.ParentScopeID, ParentToolCallID: v.ParentToolCallID,
		OccurredAt: v.OccurredAt.UTC().Format(time.RFC3339Nano),
	}
	for i, item := range v.Items {
		out.Items[i] = AdapterTodoItemV1{ID: item.ID, Content: item.Content, Status: string(item.Status), SyntheticID: item.SyntheticID}
	}
	return out
}
func decodeTodo(v *AdapterTodoSnapshotV1) todo.Snapshot {
	out := todo.Snapshot{
		Items: make([]todo.Item, len(v.Items)), Source: todo.Source(v.Source), Revision: v.Revision,
		ScopeID: v.ScopeID, ParentScopeID: v.ParentScopeID, ParentToolCallID: v.ParentToolCallID,
	}
	out.OccurredAt, _ = time.Parse(time.RFC3339Nano, v.OccurredAt)
	for i, item := range v.Items {
		out.Items[i] = todo.Item{ID: item.ID, Content: item.Content, Status: todo.Status(item.Status), SyntheticID: item.SyntheticID}
	}
	return out
}

func encodeSource(source *adaptor.EventSourceMeta) *AdapterEventSourceMetaV1 {
	var out *AdapterEventSourceMetaV1
	tail := &out
	seen := map[*adaptor.EventSourceMeta]bool{}
	for s := source; s != nil; s = s.Upstream {
		// Preserve invalid depth as a bounded ninth node. Validation turns it into
		// a safe loss marker, rather than silently truncating a source chain.
		if seen[s] || len(seen) >= 8 {
			for i := len(seen); i < 9; i++ {
				*tail = &AdapterEventSourceMetaV1{}
				tail = &(*tail).Upstream
			}
			break
		}
		seen[s] = true
		next := &AdapterEventSourceMetaV1{RunID: s.RunID, ThreadID: s.ThreadID, TurnID: s.TurnID, Sequence: s.Sequence,
			ScopeID: s.ScopeID, ToolCallID: s.ToolCallID, InvocationID: s.InvocationID, DelegationID: s.DelegationID}
		if !s.Timestamp.IsZero() {
			next.Timestamp = s.Timestamp.UTC().Format(time.RFC3339Nano)
		}
		*tail = next
		tail = &next.Upstream
	}
	return out
}
func decodeSource(source *AdapterEventSourceMetaV1) *adaptor.EventSourceMeta {
	var out *adaptor.EventSourceMeta
	tail := &out
	for s := source; s != nil; s = s.Upstream {
		next := &adaptor.EventSourceMeta{RunID: s.RunID, ThreadID: s.ThreadID, TurnID: s.TurnID, Sequence: s.Sequence,
			ScopeID: s.ScopeID, ToolCallID: s.ToolCallID, InvocationID: s.InvocationID, DelegationID: s.DelegationID}
		if s.Timestamp != "" {
			next.Timestamp, _ = time.Parse(time.RFC3339Nano, s.Timestamp)
		}
		*tail = next
		tail = &next.Upstream
	}
	return out
}

// This attachment requests observed facts without introducing a Store, event
// channel, callback, or a second choice of provider transport.
type observationDemandProvider struct{ demand adaptor.ObservationDemand }

func (p observationDemandProvider) AttachRun(context.Context, string) (adaptor.RunAttachment, error) {
	return adaptor.RunAttachment{Observation: p.demand}, nil
}
func (observationDemandProvider) DetachRun(context.Context, string) error { return nil }
func observationOptions(exposure ExposurePolicy) []adaptor.CallOption {
	if !exposure.IncludeCapabilityInvocations && !exposure.IncludeTodos {
		return nil
	}
	return []adaptor.CallOption{adaptor.WithRunServices(observationDemandProvider{demand: adaptor.ObservationDemand{
		CapabilityInvocations: exposure.IncludeCapabilityInvocations, Todos: exposure.IncludeTodos,
	}})}
}
