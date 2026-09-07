package a2adelegation

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"time"
	"unicode"
	"unicode/utf8"

	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/capability"
)

// Domain keys use a reversible tuple. Truncation or hashing would conceal a
// loss of identity; an unrepresentable mapped event becomes a safe drop.
func relayTuple(parts ...string) string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(parts)
	return "aa1:" + base64.RawURLEncoding.EncodeToString(bytes.TrimSuffix(b.Bytes(), []byte{'\n'}))
}
func cloneSource(in *adaptor.EventSourceMeta) *adaptor.EventSourceMeta {
	seen := map[*adaptor.EventSourceMeta]*adaptor.EventSourceMeta{}
	var copy func(*adaptor.EventSourceMeta) *adaptor.EventSourceMeta
	copy = func(s *adaptor.EventSourceMeta) *adaptor.EventSourceMeta {
		if s == nil {
			return nil
		}
		if v := seen[s]; v != nil {
			return v
		}
		v := *s
		seen[s] = &v
		v.Upstream = copy(s.Upstream)
		return &v
	}
	return copy(in)
}
func coreDelegationEvent(ev DelegationEvent) adaptor.Event {
	ev = cloneDelegationEvent(ev)
	var result adaptor.Event = SubagentEvent(ev)
	if ev.Capability != nil {
		result = adaptor.CapabilityInvocation{Invocation: *ev.Capability}
	}
	if ev.Todo != nil {
		result = adaptor.TodoUpdated{Snapshot: *ev.Todo}
	}
	return adaptor.WithEventMeta(result, adaptor.EventMeta{Source: ev.Source})
}
func relayIDValid(s string) bool {
	if len(s) > 2048 || !utf8.ValidString(s) {
		return false
	}
	for _, c := range s {
		if unicode.IsControl(c) {
			return false
		}
	}
	return true
}
func relayLoss(base DelegationEvent, reason string) DelegationEvent {
	ev := base
	ev.Kind = DelegationStreamDropped
	ev.Capability = nil
	ev.Todo = nil
	ev.Source = nil
	ev.Args = nil
	ev.Result = nil
	ev.Delta = ""
	ev.Text = ""
	ev.StatusParts = nil
	ev.Raw = map[string]any{"reason": reason, "source": "a2a", "dropped_count": 1}
	return ev
}
func mapRelay(ev, base DelegationEvent) DelegationEvent {
	if ev.Source == nil { // A host decoder has no claimed formal source.
		ev.ScopeID = base.ScopeID
		ev.ParentScopeID = base.ParentScopeID
		ev.ParentToolCallID = base.ParentToolCallID
		return ev
	}
	remote := ev.Source.RunID
	scope, parentScope, parent := ev.ScopeID, ev.ParentScopeID, ev.ParentToolCallID
	ev.Source.ScopeID = scope
	ev.Source.ToolCallID = ev.RemoteToolCallID
	if ev.Capability != nil {
		ev.Source.InvocationID = ev.Capability.InvocationID
	}
	// DelegationID refers to the immediate upstream delegation when present;
	// the current delegation is already represented in this event's domain.
	ev.ScopeID = relayTuple("scope", base.DelegationID, remote, scope)
	if parent != "" {
		ev.ParentScopeID = relayTuple("scope", base.DelegationID, remote, parentScope)
		ev.ParentToolCallID = relayTuple("tool", base.DelegationID, remote, parentScope, parent)
	} else {
		ev.ParentScopeID = base.ParentScopeID
		ev.ParentToolCallID = base.ParentToolCallID
	}
	if ev.RemoteToolCallID != "" {
		ev.RemoteToolCallID = relayTuple("tool", base.DelegationID, remote, scope, ev.RemoteToolCallID)
	}
	if ev.Capability != nil {
		v := ev.Capability
		v.InvocationID = relayTuple("invocation", base.DelegationID, remote, scope, v.InvocationID)
		v.ScopeID = ev.ScopeID
		v.ParentScopeID = ev.ParentScopeID
		v.ParentToolCallID = ev.ParentToolCallID
		v.Source = capability.Relay
		v.Evidence = capability.Relayed
	}
	if ev.Todo != nil {
		v := ev.Todo
		v.ScopeID = ev.ScopeID
		v.ParentScopeID = ev.ParentScopeID
		v.ParentToolCallID = ev.ParentToolCallID
	}
	seen := map[*adaptor.EventSourceMeta]bool{}
	for s := ev.Source; s != nil; s = s.Upstream {
		if seen[s] || len(seen) >= 8 {
			return relayLoss(base, "relay_depth_exceeded")
		}
		seen[s] = true
	}
	ids := []string{ev.ScopeID, ev.ParentScopeID, ev.ParentToolCallID, ev.RemoteToolCallID}
	if ev.Capability != nil {
		ids = append(ids, ev.Capability.InvocationID)
	}
	for _, id := range ids {
		if !relayIDValid(id) {
			return relayLoss(base, "invalid_payload")
		}
	}
	return ev
}

// encodeLocalFact preserves typed strings and integer precision until the
// accepted A2A decoder validates the closed schema. A JSON round trip here
// would silently repair malformed UTF-8 before validation.
func encodeLocalFact(ev adaptor.Event) (map[string]any, bool) {
	frame := map[string]any{}
	switch e := ev.(type) {
	case adaptor.CapabilityInvocation:
		v := e.Invocation
		c := map[string]any{"invocation_id": v.InvocationID, "kind": string(v.Ref.Kind), "key": v.Ref.Key, "operation": v.Ref.Operation, "phase": string(v.Phase), "evidence": string(v.Evidence), "source": string(v.Source), "scope_id": v.ScopeID, "parent_scope_id": v.ParentScopeID, "parent_tool_call_id": v.ParentToolCallID, "occurred_at": v.OccurredAt.UTC().Format(time.RFC3339Nano)}
		if v.ErrorCode != "" {
			c["error_code"] = string(v.ErrorCode)
		}
		if v.Duration != nil {
			c["duration_ns"] = int64(*v.Duration)
		}
		frame["kind"] = "capability.invocation"
		frame["capability"] = c
	case adaptor.TodoUpdated:
		v := e.Snapshot
		items := make([]any, len(v.Items))
		for i, item := range v.Items {
			items[i] = map[string]any{"id": item.ID, "content": item.Content, "status": string(item.Status), "synthetic_id": item.SyntheticID}
		}
		frame["kind"] = "todo.updated"
		frame["todo"] = map[string]any{"items": items, "source": string(v.Source), "scope_id": v.ScopeID, "parent_scope_id": v.ParentScopeID, "parent_tool_call_id": v.ParentToolCallID, "revision": v.Revision, "occurred_at": v.OccurredAt.UTC().Format(time.RFC3339Nano)}
	default:
		return nil, false
	}
	setLocalMeta(frame, ev.Meta())
	return frame, true
}
func setLocalMeta(frame map[string]any, meta adaptor.EventMeta) {
	if meta.RunID == "" && meta.Sequence == 0 && meta.Time.IsZero() {
		return
	}
	m := map[string]any{"run_id": meta.RunID, "sequence": meta.Sequence, "time": meta.Time.UTC().Format(time.RFC3339Nano)}
	if meta.ThreadKey != "" {
		m["thread_key"] = meta.ThreadKey
	}
	if meta.TurnID != "" {
		m["turn_id"] = meta.TurnID
	}
	if meta.Source != nil {
		m["source"] = localSourceWire(meta.Source, map[*adaptor.EventSourceMeta]bool{})
	}
	frame["meta"] = m
	frame["sequence"] = meta.Sequence
}
func localSourceWire(s *adaptor.EventSourceMeta, seen map[*adaptor.EventSourceMeta]bool) map[string]any {
	if s == nil {
		return nil
	}
	if seen[s] || len(seen) >= 8 {
		return map[string]any{"upstream": map[string]any{}}
	}
	seen[s] = true
	m := map[string]any{"run_id": s.RunID, "thread_id": s.ThreadID, "turn_id": s.TurnID, "sequence": s.Sequence, "scope_id": s.ScopeID, "tool_call_id": s.ToolCallID, "invocation_id": s.InvocationID, "delegation_id": s.DelegationID}
	if !s.Timestamp.IsZero() {
		m["timestamp"] = s.Timestamp.UTC().Format(time.RFC3339Nano)
	}
	if s.Upstream != nil {
		m["upstream"] = localSourceWire(s.Upstream, seen)
	}
	return m
}
