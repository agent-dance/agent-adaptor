package adaptor

import (
	"context"
	"fmt"
)

// Stream is the live view of one running invocation (decision D4: a small
// interface, not a struct). One event channel carries everything —
// text/thinking deltas, tool calls, process output, notices, approval
// requests — and Result() is the single close-out.
//
// Consumption contract:
//
//	stream := agent.Stream(ctx, prompt)
//	for ev := range stream.Events() {
//	    switch e := ev.(type) {
//	    case adaptor.TextDelta:        // render e.Text
//	    case adaptor.ToolCall:         // show e.Name
//	    case *adaptor.ApprovalRequest: // e.Approve / e.Deny / e.Answer
//	    }
//	}
//	res, err := stream.Result()
//
// Events() closes when the run ends; Result() then returns immediately with
// the same Result / *RunError / infrastructure-error contract as Run.
// Consumers must continuously drain Events. The default backpressure mode
// may discard only high-frequency deltas; approvals, lifecycle, terminal,
// transcript and drop-report events remain reliable and can therefore apply
// backpressure. A consumer which abandons the loop must call Cancel first.
type Stream interface {
	// Events returns the unified typed event channel. It is closed when
	// the run ends (after the final events, including the terminal
	// Dropped marker when events were dropped, have been delivered).
	Events() <-chan Event
	// Result blocks until the run ends and returns the final outcome —
	// exactly Run's contract: (*Result, nil) on success, (nil, *RunError)
	// on business failure, (nil, error) on infrastructure failure.
	// Result may be called multiple times and from any goroutine.
	Result() (*Result, error)
	// RunID returns the SDK-assigned execution identifier, available
	// immediately (before the first event).
	RunID() string
	// Cancel aborts the run and immediately releases blocked event publishers
	// and approval waiters. It is idempotent. Buffered Events may still be
	// drained before reading Result().
	Cancel()
}

// runStream is the concrete Stream. res/err are written exactly once,
// before done is closed; every reader waits on done.
type runStream struct {
	runID  string
	sink   *eventSink
	cancel context.CancelFunc
	done   chan struct{}
	res    *Result
	err    error
}

var _ Stream = (*runStream)(nil)

func (s *runStream) Events() <-chan Event { return s.sink.events }
func (s *runStream) RunID() string        { return s.runID }
func (s *runStream) Cancel() {
	if s == nil {
		return
	}
	if s.sink != nil {
		s.sink.abort()
	}
	if s.cancel != nil {
		s.cancel()
	}
}

func (s *runStream) Result() (*Result, error) {
	<-s.done
	return s.res, s.err
}

// Stream starts one prompt and returns the live Stream immediately. Options
// merge exactly like Run ("nearer scope wins; skills append, everything
// else replaces"); the agent defaults are never mutated.
//
// Stream never returns an error: startup failures surface through the
// normal contract (closed Events channel + Result() error).
func (a *Agent) Stream(ctx context.Context, prompt string, opts ...CallOption) Stream {
	return a.startInvocation(ctx, prompt, opts, nil)
}

// openStream is the shared Stream prologue for the stateless Agent path and
// the Thread path: merge per-call options over the agent defaults, apply the
// timeout and identity to the context, mint the run ID, and prime the sink
// and runStream. When run-ID minting fails the returned stream is already
// sealed (closed Events channel, Result() error) and ok is false — callers
// return it as-is.
func (a *Agent) openStream(ctx context.Context, opts []CallOption, threadKey string) (st *runStream, eff RunSettings, runCtx context.Context, ok bool) {
	eff = a.defaults.RunSettings.clone()
	for _, o := range opts {
		if o == nil {
			continue
		}
		o.ApplyRun(&eff)
	}

	var cancel context.CancelFunc
	if eff.timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, eff.timeout)
	} else {
		ctx, cancel = context.WithCancel(ctx)
	}
	if eff.identity != nil {
		ctx = contextWithIdentity(ctx, *eff.identity)
	}

	runID, idErr := newRunID()

	var approvals ApprovalPolicy
	if eff.policy != nil {
		approvals = eff.policy.Approvals
	}
	sink := newEventSink(eventSinkConfig{
		runID:     runID,
		threadKey: threadKey,
		buffer:    a.defaults.eventBuffer,
		blocking:  a.defaults.blockingEvents,
		policy:    approvals,
		handler:   eff.approval,
		caps:      a.driver.Descriptor().RunPolicyCaps,
	})

	st = &runStream{
		runID:  runID,
		sink:   sink,
		cancel: cancel,
		done:   make(chan struct{}),
	}

	if idErr != nil {
		st.err = fmt.Errorf("adaptor: generate run id: %w", idErr)
		cancel()
		sink.close()
		close(st.done)
		return st, eff, ctx, false
	}
	// Parent cancellation has the same unblocking guarantees as Cancel().
	// A successful normal close ends the watcher through broker.done.
	go func() {
		select {
		case <-ctx.Done():
			sink.abort()
		case <-sink.broker.done:
		}
	}()
	return st, eff, ctx, true
}
