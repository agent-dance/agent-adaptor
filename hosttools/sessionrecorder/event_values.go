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
func cloneRecord(r EventRecord) EventRecord {
	r.Event = cloneRecordedEvent(r.Event)
	return r
}

func cloneRecordedEvent(ev adaptor.Event) adaptor.Event {
	if ev == nil {
		return nil
	}
	if req, ok := ev.(*adaptor.ApprovalRequest); ok {
		if req == nil {
			return ev
		}
		// Construct only public descriptive fields; even the memory backend is a
		// historical record, never a second route to the live responder. The
		// final WithEventMeta call copies both metadata and description once.
		descriptive := &adaptor.ApprovalRequest{
			ID: req.ID, RunID: req.RunID, Kind: req.Kind, Title: req.Title,
			Source: req.Source, ToolCallID: req.ToolCallID,
			Choices:   req.Choices,
			Details:   req.Details,
			CreatedAt: req.CreatedAt, Deadline: req.Deadline, Attempt: req.Attempt,
		}
		return adaptor.WithEventMeta(descriptive, req.Meta())
	}
	return adaptor.WithEventMeta(ev, ev.Meta())
}
