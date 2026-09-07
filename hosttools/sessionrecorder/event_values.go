package sessionrecorder

import (
	adaptor "github.com/agent-dance/agent-adaptor"
)

func sourceToWire(s *adaptor.EventSourceMeta) *eventSourceMetaWire {
	if s == nil {
		return nil
	}
	return &eventSourceMetaWire{RunID: s.RunID, ThreadID: s.ThreadID, TurnID: s.TurnID, Sequence: s.Sequence, Timestamp: s.Timestamp, ScopeID: s.ScopeID, ToolCallID: s.ToolCallID, InvocationID: s.InvocationID, DelegationID: s.DelegationID, Upstream: sourceToWire(s.Upstream)}
}
func sourceFromWire(s *eventSourceMetaWire) *adaptor.EventSourceMeta {
	if s == nil {
		return nil
	}
	return &adaptor.EventSourceMeta{RunID: s.RunID, ThreadID: s.ThreadID, TurnID: s.TurnID, Sequence: s.Sequence, Timestamp: s.Timestamp, ScopeID: s.ScopeID, ToolCallID: s.ToolCallID, InvocationID: s.InvocationID, DelegationID: s.DelegationID, Upstream: sourceFromWire(s.Upstream)}
}
func cloneRecord(r EventRecord) EventRecord { r.Event = cloneRecordedEvent(r.Event); return r }
func cloneRecordedEvent(ev adaptor.Event) adaptor.Event {
	if req, ok := ev.(*adaptor.ApprovalRequest); ok && req == nil {
		return ev
	}
	if ev == nil {
		return nil
	}
	cloned := adaptor.WithEventMeta(ev, ev.Meta())
	if req, ok := cloned.(*adaptor.ApprovalRequest); ok && req != nil {
		// Construct only public descriptive fields; even the memory backend is a
		// historical record, never a second route to the live responder.
		descriptive := &adaptor.ApprovalRequest{
			ID: req.ID, RunID: req.RunID, Kind: req.Kind, Title: req.Title,
			Source: req.Source, ToolCallID: req.ToolCallID,
			Choices:   append([]adaptor.Choice(nil), req.Choices...),
			Details:   approvalDetailsSnapshot(req.Details),
			CreatedAt: req.CreatedAt, Deadline: req.Deadline, Attempt: req.Attempt,
		}
		return adaptor.WithEventMeta(descriptive, req.Meta())
	}
	return cloned
}

// Give each approval description its own nested map snapshot using the public
// map-carrying Event copy contract. This intermediate value is never published
// and carries no responder.
func approvalDetailsSnapshot(details map[string]any) map[string]any {
	return adaptor.WithEventMeta(adaptor.Notice{Data: details}, adaptor.EventMeta{}).(adaptor.Notice).Data
}
