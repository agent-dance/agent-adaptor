package adaptor

import (
	"context"
	"fmt"

	"github.com/agent-dance/agent-adaptor/v2/driver"
)

// Stream represents one execution. Send starts it; Send on this handle adds
// supplemental messages. Events ends with the authoritative Done, after all
// checkpoint persistence, validation and resource cleanup. Result returns the
// same Result pointer and error as Done and can be called concurrently.
//
// Events may be read after Result. Critical events are retained even without a
// reader; the default backpressure policy can aggregate incremental deltas into
// Dropped. WithBlockingEvents applies backpressure to ordinary events while
// Events is consumed; a Result-only caller retains critical events and a drop
// summary. A subscriber which abandons
// Events should cancel the execution and drain its remaining critical events.
type Stream interface {
	// Send admits supplemental input into this execution. Delivery is reported
	// by MessageUpdate and Result.Messages. Only message options are accepted.
	// Prelaunch admission uses the Driver's declared capabilities; resolution
	// may fail queued messages before delivery and returns their original text
	// through MessageUpdate and Result.Undelivered.
	Send(ctx context.Context, text string, opts ...MessageOption) error
	// Events returns the unified typed event channel. It is closed when
	// the run ends (after the final events, including the terminal
	// Dropped marker when events were dropped, have been delivered).
	// Call Events before consuming ordinary traffic. If execution finishes
	// before Events is called, the closed log retains only lifecycle, message,
	// turn, approval and notice events, plus a Dropped summary. Ordinary events
	// are discarded; Result's raw streams and transcript remain available.
	Events() <-chan Event
	// Result blocks until the run ends and returns the final outcome —
	// using the single error contract: (*Result, nil) on success, (nil, *RunError)
	// on business failure, (nil, error) on infrastructure failure.
	// Result may be called multiple times and from any goroutine. If Events
	// has never been called, Result releases ordinary blocking producers and
	// discards ordinary events, retaining critical events and one Dropped
	// summary for a later Events call. Call Events first when both consumption
	// forms are needed with complete ordinary-event delivery.
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
	hub    *messageHub
	runID  string
	sink   *eventSink
	cancel context.CancelFunc
	done   chan struct{}
	res    *Result
	err    error
}

var _ Stream = (*runStream)(nil)

func (s *runStream) Events() <-chan Event {
	s.sink.eventsCalled.Store(true)
	return s.sink.broker.subscribe()
}
func (s *runStream) Send(ctx context.Context, text string, opts ...MessageOption) error {
	return s.hub.send(ctx, text, opts...)
}
func (s *runStream) RunID() string { return s.runID }
func (s *runStream) Cancel() {
	if s == nil {
		return
	}
	// Establish cancellation before releasing approval or event waiters: any
	// transport failures they report while unwinding belong to this cancel.
	if s.cancel != nil {
		s.cancel()
	}
	if s.hub != nil {
		s.hub.cancelPending()
	}
	if s.sink != nil {
		s.sink.abort()
	}
}

func (s *runStream) Result() (*Result, error) {
	s.sink.broker.awaitResult()
	<-s.done
	return s.res, s.err
}

// Send starts an independent stateless execution and returns immediately. Options
// merge ("nearer scope wins; skills append, everything
// else replaces"); the agent defaults are never mutated.
//
// Send never returns a separate error: startup failures surface through the
// normal contract (closed Events channel + Result() error).
func (a *Agent) Send(ctx context.Context, prompt string, opts ...CallOption) Stream {
	return a.startInvocation(ctx, prompt, opts, nil)
}

// openStream is the shared Stream prologue for the stateless Agent path and
// the Thread path: merge per-call options over the agent defaults, apply the
// timeout and identity to the context, mint the run ID, and prime the sink
// and runStream. When run-ID minting fails the returned stream is already
// sealed (closed Events channel, Result() error) and ok is false — callers
// return it as-is. admit registers inputs on the new hub before any preflight
// failure can settle the messages and publish Done.
func (a *Agent) openStream(ctx context.Context, opts []CallOption, threadKey string, admit func(*runStream, RunSettings)) (st *runStream, eff RunSettings, runCtx context.Context, ok bool) {
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

	desc := a.driver.Descriptor()
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
		caps:      desc.RunPolicyCaps,
	})

	sink.broker.spool = true
	st = &runStream{
		runID:  runID,
		sink:   sink,
		cancel: cancel,
		done:   make(chan struct{}),
	}
	st.hub = newMessageHub(ctx, sink, effectiveMessagePolicy(eff.policy), desc.Messages, desc.Sessions.SupportsResume)
	sink.hub = st.hub
	admit(st, eff)
	if openErr := a.ensureOpen(); openErr != nil {
		st.err = openErr
		cancel()
		st.hub.finish(MessageProviderRejected)
		sink.broker.publishTerminal(Done{Result: st.res, Err: st.err}, nil)
		sink.close()
		close(st.done)
		return st, eff, ctx, false
	}

	if idErr != nil {
		st.err = fmt.Errorf("adaptor: generate run id: %w", idErr)
		cancel()
		st.hub.finish(MessageProviderRejected)
		sink.broker.publishTerminal(Done{Result: st.res, Err: st.err}, nil)
		sink.close()
		close(st.done)
		return st, eff, ctx, false
	}
	if a.toolConfigErr != nil {
		st.err = fmt.Errorf("adaptor: run %s: %w", runID, a.toolConfigErr)
		cancel()
		st.hub.finish(MessageProviderRejected)
		sink.broker.publishTerminal(Done{Result: st.res, Err: st.err}, nil)
		sink.close()
		close(st.done)
		return st, eff, ctx, false
	}
	if configErr := a.driver.ValidateConfig(nil); configErr != nil {
		st.err = fmt.Errorf("adaptor: run %s: %w", runID, &driver.InvalidDriverConfigError{
			Driver: desc.Type,
			Cause:  configErr,
		})
		cancel()
		st.hub.finish(MessageProviderRejected)
		sink.broker.publishTerminal(Done{Result: st.res, Err: st.err}, nil)
		sink.close()
		close(st.done)
		return st, eff, ctx, false
	}
	if toolsErr := a.validateHostedToolsPreflight(&eff, desc.MCP); toolsErr != nil {
		st.err = fmt.Errorf("adaptor: run %s: %w", runID, toolsErr)
		cancel()
		st.hub.finish(MessageProviderRejected)
		sink.broker.publishTerminal(Done{Result: st.res, Err: st.err}, nil)
		sink.close()
		close(st.done)
		return st, eff, ctx, false
	}
	if policyErr := validatePolicy(desc, eff.policy); policyErr != nil {
		st.err = fmt.Errorf("adaptor: run %s: %w", runID, policyErr)
		cancel()
		st.hub.finish(MessageProviderRejected)
		sink.broker.publishTerminal(Done{Result: st.res, Err: st.err}, nil)
		sink.close()
		close(st.done)
		return st, eff, ctx, false
	}
	if openErr := a.registerRun(runID, cancel); openErr != nil {
		st.err = openErr
		cancel()
		st.hub.finish(MessageProviderRejected)
		sink.broker.publishTerminal(Done{Result: st.res, Err: st.err}, nil)
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
			st.hub.cancelPending()
		case <-sink.broker.done:
		}
	}()
	return st, eff, ctx, true
}
