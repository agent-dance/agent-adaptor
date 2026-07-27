package adaptor

import (
	"context"
	"errors"
	"fmt"
	"maps"

	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/engine"
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
	st, eff, runCtx, ok := a.openStream(ctx, opts)
	if !ok {
		return st
	}

	go func() {
		defer st.cancel()
		// Environment acquisition (workspace lease, runtime services,
		// run-service provider attachments) happens first: it needs the
		// run ID, and its endpoints feed the MCP payload resolution
		// below. An attach failure is a pre-launch failure — the driver
		// never starts, and everything already acquired is unwound.
		res, acquireErr := a.acquireRun(runCtx, st.runID, &eff, st.sink)
		if acquireErr != nil {
			st.err = fmt.Errorf("adaptor: run %s: %w", st.runID, acquireErr)
			st.sink.close()
			close(st.done)
			return
		}
		// Resolution (skills, MCP, profile payload, structured output
		// negotiation) runs inside the goroutine so Stream returns
		// immediately even when a provider fetch or materialization is
		// slow. Failures here are pre-launch: the driver never starts and
		// the error surfaces through Result() with the engine sentinel
		// chain intact.
		rr, resolveErr := a.resolveRun(runCtx, st.runID, prompt, &eff, res)
		if resolveErr != nil {
			st.err = fmt.Errorf("adaptor: run %s: %w", st.runID, resolveErr)
			res.finish(runCtx, st.sink)
			close(st.done)
			return
		}
		rr.req.Streaming = true
		resp, runErr := a.driver.Run(runCtx, rr.req, st.sink)
		if runErr == nil {
			// Post-run structured output contract (engine truth):
			// suppress unrequested output, prompt-validate raw text,
			// escalate invalid output per OnInvalid.
			resp.StructuredOutput, resp.Failure = engine.FinalizeStructuredOutput(
				rr.schema, rr.source, resp.Output, resp.StructuredOutput, resp.Failure)
		}
		// Order matters for the close-timing contract: the outcome is
		// stored before the event channel closes, and done closes last,
		// so a consumer that drained Events() gets Result() without
		// further waiting. finish() drains the provider event sources
		// before closing the channel and detaches them after, so a
		// terminal SubagentUpdate is never clipped and a returned
		// Result() implies the run's services are released.
		st.res, st.err = finalizeRun(st.runID, st.sink, resp, runErr)
		backfillRunServices(res, st.res, st.err)
		res.finish(runCtx, st.sink)
		close(st.done)
	}()
	return st
}

// openStream is the shared Stream prologue for the stateless Agent path and
// the Thread path: merge per-call options over the agent defaults, apply the
// timeout and identity to the context, mint the run ID, and prime the sink
// and runStream. When run-ID minting fails the returned stream is already
// sealed (closed Events channel, Result() error) and ok is false — callers
// return it as-is.
func (a *Agent) openStream(ctx context.Context, opts []CallOption) (st *runStream, eff RunSettings, runCtx context.Context, ok bool) {
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
		runID:    runID,
		buffer:   a.defaults.eventBuffer,
		blocking: a.defaults.blockingEvents,
		policy:   approvals,
		handler:  eff.approval,
		caps:     a.driver.Descriptor().RunPolicyCaps,
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

// finalizeRun translates the driver outcome into the D1 contract, overlaying
// the approval failure recorded by the sink (legacy pendingFailure overlay):
//
//   - outer-context cancellation / deadline → plain wrapped error, even when
//     an approval was in flight (a bare cancellation is infrastructure);
//   - recorded approval failure → *RunError built from it (it supersedes the
//     driver's own Failure classification);
//   - driver error → plain wrapped error (crash, protocol breakage);
//   - Response.Failure → *RunError; otherwise success.
func finalizeRun(runID string, sink *eventSink, resp driver.Response, err error) (*Result, error) {
	pending := sink.pendingFailure()
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, fmt.Errorf("adaptor: run %s: %w", runID, err)
		}
		if pending != nil {
			return nil, runErrorFromFailure(pending, resultFromResponse(runID, resp))
		}
		if errors.Is(err, errApprovalAbort) {
			// Sentinel without recorded context — a driver returned it
			// verbatim outside the dispatcher flow. Classify as agent
			// error rather than leaking the internal sentinel.
			return nil, &RunError{
				Reason:  ReasonAgentError,
				Message: "approval aborted the run",
				Result:  resultFromResponse(runID, resp),
			}
		}
		return nil, fmt.Errorf("adaptor: run %s: %w", runID, err)
	}

	res := resultFromResponse(runID, resp)
	failure := resp.Failure
	if pending != nil {
		failure = pending
	}
	if failure != nil {
		return nil, runErrorFromFailure(failure, res)
	}
	return res, nil
}

func runErrorFromFailure(f *driver.RunFailure, res *Result) *RunError {
	return &RunError{
		Reason:  failureReason(f.Code),
		Message: f.Message,
		Details: maps.Clone(f.Metadata),
		Result:  res,
	}
}
