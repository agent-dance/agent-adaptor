package subagentstream

// Merge (P4.6/P4.7 fallback bridge): fold delegation-bus events into a
// next-gen adaptor.Stream as adaptor.SubagentUpdate events, so a leader's
// consumer sees team progress on the one unified channel (design doc §9.7):
//
//	stream := leader.Stream(ctx, prompt)
//	merged := subagentstream.Merge(ctx, stream, team.Bus())
//	for ev := range merged.Events() {
//	    switch e := ev.(type) {
//	    case adaptor.TextDelta:      // leader's own output
//	    case adaptor.SubagentUpdate: // team member progress
//	    }
//	}
//	res, err := merged.Result()
//
// This is the host-side fallback for engine-level SubagentUpdate injection
// (team.Option()), which lands in a later wave; the projection is shared so
// the engine path can reuse SubagentEvent verbatim.

import (
	"context"

	"github.com/agent-dance/agent-adaptor/hosttools/a2adelegation"
	adaptor "github.com/agent-dance/agent-adaptor/next"
)

// Merge returns a Stream that carries every event of stream plus one
// adaptor.SubagentUpdate per delegation event published for stream.RunID()
// on bus. Per-source ordering is preserved (parent events in stream order,
// delegation events in bus order); the two sources interleave as they
// arrive. RunID, Result, and Cancel delegate to the parent stream, and the
// merged Events channel closes after the parent stream ends (terminal
// SubagentUpdate markers are synthesized for delegations that never
// reported one). A nil bus returns stream unchanged.
//
// ctx bounds the bus subscription; cancel it (or the parent run) to detach.
// Like the parent stream, the merged channel must be drained.
func Merge(ctx context.Context, stream adaptor.Stream, bus EventBus) adaptor.Stream {
	if stream == nil || bus == nil {
		return stream
	}
	out := make(chan adaptor.Event, 64)
	merged := &mergedStream{parent: stream, events: out}

	go func() {
		defer close(out)
		parent := stream.Events()
		subCtx, cancelSubagents := context.WithCancel(ctx)
		defer cancelSubagents()
		subagents := bus.SubscribeRun(subCtx, stream.RunID())

		tracker := newDelegationTracker()
		send := func(ev adaptor.Event) bool {
			select {
			case <-ctx.Done():
				return false
			case out <- ev:
				return true
			}
		}
		sendSubagent := func(ev a2adelegation.DelegationEvent) bool {
			tracker.Track(ev)
			return send(SubagentEvent(ev))
		}
		// drainSubagents forwards every already-delivered delegation event
		// without blocking. Delegation terminals are published before the
		// parent driver returns, so draining after the parent channel closes
		// deterministically flushes the tail.
		drainSubagents := func() bool {
			for subagents != nil {
				select {
				case ev, ok := <-subagents:
					if !ok {
						subagents = nil
						return true
					}
					if !sendSubagent(ev) {
						return false
					}
				default:
					return true
				}
			}
			return true
		}
		stopSubagents := func() {
			cancelSubagents()
			subagents = nil
		}

		for parent != nil || subagents != nil {
			select {
			case <-ctx.Done():
				tracker.FlushSynthetic(
					a2adelegation.DelegationCancelled,
					"cancelled",
					&a2adelegation.DelegationError{Code: "parent_cancelled", Message: "parent run context cancelled"},
					func(ev a2adelegation.DelegationEvent) bool {
						select {
						case out <- SubagentEvent(ev):
							return true
						default:
							return false
						}
					},
				)
				stopSubagents()
				stream.Cancel()
				return
			case ev, ok := <-parent:
				if !ok {
					parent = nil
					if !drainSubagents() {
						return
					}
					if !tracker.FlushSynthetic(a2adelegation.DelegationFailed, "failed", parentFinishedError(), sendSubagent) {
						return
					}
					stopSubagents()
					continue
				}
				if !send(ev) {
					return
				}
			case ev, ok := <-subagents:
				if !ok {
					subagents = nil
					continue
				}
				if !sendSubagent(ev) {
					return
				}
			}
		}
	}()
	return merged
}

// mergedStream decorates the parent stream with the merged event channel.
type mergedStream struct {
	parent adaptor.Stream
	events chan adaptor.Event
}

var _ adaptor.Stream = (*mergedStream)(nil)

func (s *mergedStream) Events() <-chan adaptor.Event { return s.events }
func (s *mergedStream) Result() (*adaptor.Result, error) {
	return s.parent.Result()
}
func (s *mergedStream) RunID() string { return s.parent.RunID() }
func (s *mergedStream) Cancel()       { s.parent.Cancel() }

// SubagentEvent projects one DelegationEvent onto the next-gen event
// vocabulary. The 19 DelegationEventKinds collapse onto the three
// SubagentUpdate kinds (started / delta / finished); the full detail —
// original kind, status, remote identifiers, tool payloads, errors — is
// preserved in Data so nothing is lost in the projection.
func SubagentEvent(ev a2adelegation.DelegationEvent) adaptor.SubagentUpdate {
	update := adaptor.SubagentUpdate{
		Agent: ev.AgentKey,
		Kind:  adaptor.SubagentDelta,
		Delta: ev.Delta,
	}
	switch {
	case ev.Kind == a2adelegation.DelegationStarted:
		update.Kind = adaptor.SubagentStarted
	case isTerminalDelegation(ev.Kind):
		update.Kind = adaptor.SubagentFinished
	}

	data := map[string]any{
		"kind":                string(ev.Kind),
		"status":              ev.Status,
		"agent_name":          ev.AgentName,
		"delegation_id":       ev.DelegationID,
		"parent_tool_call_id": ev.ParentToolCallID,
		"remote_protocol":     ev.Protocol,
		"remote_task_id":      ev.RemoteTaskID,
		"remote_context_id":   ev.RemoteContextID,
		"remote_message_id":   ev.RemoteMessageID,
		"remote_artifact_id":  ev.RemoteArtifactID,
		"remote_tool_call_id": ev.RemoteToolCallID,
		"tool_name":           ev.ToolName,
		"name":                ev.Name,
		"role":                ev.Role,
		"text":                ev.Text,
		"args":                ev.Args,
		"result":              ev.Result,
	}
	if ev.Sequence != 0 {
		data["sequence"] = ev.Sequence
	}
	if ev.Artifact != nil {
		data["artifact"] = ev.Artifact
	}
	if ev.Error != nil {
		data["error"] = ev.Error
	}
	if !ev.Time.IsZero() {
		data["time"] = ev.Time
	}
	for key, val := range data {
		if val == "" || val == nil {
			delete(data, key)
		}
	}
	update.Data = data
	return update
}
