package subagentstream

// Merge folds delegation-bus events into an adaptor.Stream as
// adaptor.SubagentUpdate events, so a leader's consumer sees team progress on
// the unified event channel:
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
// The bridge shares its projection with a2adelegation so direct host merging
// and delegation service injection produce the same event vocabulary.

import (
	"context"
	"sync"
	"time"

	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/hosttools/a2adelegation"
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
	if ctx == nil {
		ctx = context.Background()
	}
	out := make(chan adaptor.Event, 64)
	mergeCtx, cancelMerge := context.WithCancel(ctx)
	merged := &mergedStream{parent: stream, events: out, done: make(chan struct{}), cancelMerge: cancelMerge}

	go func() {
		defer close(out)
		defer close(merged.done)
		parent := stream.Events()
		subCtx, cancelSubagents := context.WithCancel(mergeCtx)
		defer cancelSubagents()
		subagents := bus.SubscribeRun(subCtx, stream.RunID())

		tracker := newDelegationTracker()
		var sequence uint64
		var threadKey string
		var parentTerminal adaptor.Event
		stamp := func(ev adaptor.Event, source *adaptor.EventSourceMeta, observed time.Time) adaptor.Event {
			sequence++
			meta := ev.Meta()
			if meta.RunID == "" {
				meta.RunID = stream.RunID()
			}
			if meta.ThreadKey != "" {
				threadKey = meta.ThreadKey
			} else {
				meta.ThreadKey = threadKey
			}
			if meta.Time.IsZero() {
				if observed.IsZero() {
					meta.Time = time.Now().UTC()
				} else {
					meta.Time = observed
				}
			}
			meta.Sequence = sequence
			if source != nil {
				meta.Source = source
			}
			return adaptor.WithEventMeta(ev, meta)
		}
		send := func(ev adaptor.Event) bool {
			select {
			case <-mergeCtx.Done():
				return false
			case out <- ev:
				return true
			}
		}
		sendSubagent := func(ev a2adelegation.DelegationEvent) bool {
			tracker.Track(ev)
			source := &adaptor.EventSourceMeta{
				RunID: ev.RunID, ThreadID: ev.RemoteContextID,
				TurnID: ev.DelegationID, Sequence: ev.Sequence, Timestamp: ev.Time,
			}
			return send(stamp(SubagentEvent(ev), source, ev.Time))
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
			case <-mergeCtx.Done():
				tracker.FlushSynthetic(
					a2adelegation.DelegationCancelled,
					"cancelled",
					&a2adelegation.DelegationError{Code: "parent_cancelled", Message: "parent run context cancelled"},
					func(ev a2adelegation.DelegationEvent) bool {
						tracker.Track(ev)
						projected := stamp(SubagentEvent(ev), &adaptor.EventSourceMeta{
							RunID: ev.RunID, ThreadID: ev.RemoteContextID, TurnID: ev.DelegationID,
							Sequence: ev.Sequence, Timestamp: ev.Time,
						}, ev.Time)
						select {
						case out <- projected:
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
					if parentTerminal != nil && !send(stamp(parentTerminal, nil, time.Time{})) {
						return
					}
					continue
				}
				if _, terminal := ev.(adaptor.RunFinished); terminal {
					parentTerminal = ev
					continue
				}
				if !send(stamp(ev, nil, time.Time{})) {
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
	parent      adaptor.Stream
	events      chan adaptor.Event
	done        chan struct{}
	cancelMerge context.CancelFunc
	cancelOnce  sync.Once
}

var _ adaptor.Stream = (*mergedStream)(nil)

func (s *mergedStream) Events() <-chan adaptor.Event { return s.events }
func (s *mergedStream) Result() (*adaptor.Result, error) {
	<-s.done
	return s.parent.Result()
}
func (s *mergedStream) RunID() string { return s.parent.RunID() }
func (s *mergedStream) Cancel() {
	s.cancelOnce.Do(func() {
		s.cancelMerge()
		s.parent.Cancel()
	})
}

// SubagentEvent projects one DelegationEvent onto the adaptor event
// vocabulary. The 19 DelegationEventKinds collapse onto the three
// SubagentUpdate kinds (started / delta / finished); the full detail —
// original kind, status, remote identifiers, tool payloads, errors — is
// preserved in Data so nothing is lost in the projection.
//
// The canonical projection lives in a2adelegation so host-side merging and
// delegation service injection produce identical SubagentUpdate values.
func SubagentEvent(ev a2adelegation.DelegationEvent) adaptor.SubagentUpdate {
	return a2adelegation.SubagentEvent(ev)
}
