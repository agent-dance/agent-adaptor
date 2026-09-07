package subagentstream

import (
	"context"
	"errors"
	"sync"

	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/hosttools/a2adelegation"
)

// ErrEventInjectionUnsupported means the bus cannot prove that this run used
// a run service attachment to publish delegation facts through the core sink.
var ErrEventInjectionUnsupported = errors.New("subagentstream: event injection requires a run service attachment")

// runEventsBinding is structural so Merge does not depend on a concrete bus.
// The proof is monotonic, nonblocking, and compared against the opaque run ID.
type runEventsBinding interface{ RunEventsBound(runID string) bool }

// Merge transparently forwards the parent Stream, preserving every EventMeta
// and its terminal order. A nil bus returns stream unchanged. Merge never
// subscribes to the bus, injects events, assigns sequences, or invents terminals.
// Install the delegation Service.Option before running and consume that run's
// Stream directly; the attachment publishes facts through its unique core sink.
//
// After draining the parent, Merge checks the optional RunEventsBound(string)
// bool proof on bus. Missing/false proof adds ErrEventInjectionUnsupported to a
// RunError retaining the complete parent Result and existing primary failure.
// Its cause preserves the entire original error graph, including outer wrappers
// and joined siblings. A bare deadline keeps ReasonDeadlineExceeded; an existing
// RunError reason always takes precedence over secondary context errors.
// Waiting until completion permits asynchronous attachment binding. Result is
// cached for concurrent/repeated callers; callers must still drain Events.
// Cancel and ctx cancellation cancel the parent and unblock outward sends;
// Merge then drains the cancelled parent to retain its available audit output.
func Merge(ctx context.Context, stream adaptor.Stream, bus EventBus) adaptor.Stream {
	if stream == nil || bus == nil {
		return stream
	}
	if ctx == nil {
		ctx = context.Background()
	}
	mergeCtx, cancel := context.WithCancel(ctx)
	merged := &mergedStream{parent: stream, events: make(chan adaptor.Event, 64), done: make(chan struct{}), cancelMerge: cancel}
	go func() {
		defer close(merged.events)
		defer close(merged.done)
		defer cancel()
		forwarding := true
		for {
			if !forwarding {
				for range stream.Events() {
				}
				break
			}
			select {
			case <-mergeCtx.Done():
				merged.Cancel()
				forwarding = false
			case ev, ok := <-stream.Events():
				if !ok {
					forwarding = false
					goto drained
				}
				select {
				case <-mergeCtx.Done():
					merged.Cancel()
					forwarding = false
				case merged.events <- ev:
				}
			}
		}
	drained:
		merged.result, merged.err = stream.Result()
		proof, ok := bus.(runEventsBinding)
		if !ok || !proof.RunEventsBound(stream.RunID()) {
			merged.result, merged.err = injectionFailure(stream.RunID(), merged.result, merged.err)
		}
	}()
	return merged
}

func injectionFailure(runID string, result *adaptor.Result, parentErr error) (*adaptor.Result, error) {
	var runErr *adaptor.RunError
	if errors.As(parentErr, &runErr) && runErr != nil {
		copied := *runErr
		copied.Details = adaptor.WithEventMeta(adaptor.Notice{Data: runErr.Details}, adaptor.EventMeta{}).(adaptor.Notice).Data
		// Keep the whole original error graph, including outer wrappers and
		// siblings of the selected RunError. parentErr points to the immutable
		// parent, never to copied, so a direct RunError cannot create a cycle.
		copied.Cause = errors.Join(parentErr, ErrEventInjectionUnsupported)
		if copied.Result == nil {
			copied.Result = result
		}
		if copied.Result == nil {
			copied.Result = &adaptor.Result{RunID: runID}
		}
		return nil, &copied
	}
	if result == nil {
		result = &adaptor.Result{RunID: runID}
	}
	reason := adaptor.ReasonInfrastructure
	if errors.Is(parentErr, context.Canceled) {
		reason = adaptor.ReasonCancelled
	} else if errors.Is(parentErr, context.DeadlineExceeded) {
		reason = adaptor.ReasonDeadlineExceeded
	}
	return nil, &adaptor.RunError{Reason: reason, Message: ErrEventInjectionUnsupported.Error(), Cause: errors.Join(parentErr, ErrEventInjectionUnsupported), Result: result}
}

type mergedStream struct {
	parent      adaptor.Stream
	events      chan adaptor.Event
	done        chan struct{}
	cancelMerge context.CancelFunc
	cancelOnce  sync.Once
	result      *adaptor.Result
	err         error
}

var _ adaptor.Stream = (*mergedStream)(nil)

func (s *mergedStream) Events() <-chan adaptor.Event     { return s.events }
func (s *mergedStream) Result() (*adaptor.Result, error) { <-s.done; return s.result, s.err }
func (s *mergedStream) RunID() string                    { return s.parent.RunID() }
func (s *mergedStream) Cancel()                          { s.cancelOnce.Do(func() { s.cancelMerge(); s.parent.Cancel() }) }

// SubagentEvent projects the delegation bus event for observational UI use.
// Its canonical projection belongs to a2adelegation; do not feed this mirror
// back into the core when the Service already published the same fact.
func SubagentEvent(ev a2adelegation.DelegationEvent) adaptor.SubagentUpdate {
	return a2adelegation.SubagentEvent(ev)
}
