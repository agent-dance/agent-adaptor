package adaptor

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"sync"
	"sync/atomic"
	"time"

	"github.com/agent-dance/agent-adaptor/capability"
	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/activebudget"
	"github.com/agent-dance/agent-adaptor/internal/capabilityobs"
	"github.com/agent-dance/agent-adaptor/internal/todoobs"
)

// defaultEventBuffer is the unified event channel buffer when
// WithEventBuffer is not used.
const defaultEventBuffer = 1024

// errApprovalAbort is the sentinel RequestDecision returns to the driver
// when an approval fallback aborts the run. The failure context is already
// recorded on the sink; conforming drivers stop their protocol loop and
// return without overlaying their own failure. finalizeRun recognizes the
// sentinel so lazy drivers that return it verbatim still take the business
// failure path.
var errApprovalAbort = errors.New("adaptor: approval aborted the run")

// eventSink implements driver.EventSink: it translates RunEvents
// and StreamPayloads into typed Events on one channel, applies the
// backpressure strategy, and implements driver.DecisionCapableSink so
// approval requests route through the unified stream / OnApproval callback.
type eventSink struct {
	events <-chan Event
	broker *eventBroker

	runID   string
	policy  ApprovalPolicy // effective (defaults materialized)
	handler ApprovalHandler
	caps    driver.RunPolicyCapabilities

	// retryWarned dedupes the retry-degradation warning per kind.
	retryMu     sync.Mutex
	retryWarned map[driver.HumanDecisionKind]struct{}

	// Each approval has its own token and responder; no lock spans human wait.
	decSeq   atomic.Uint64
	budget   *activebudget.Controller
	terminal invocationTerminal

	// outstanding tracks unanswered event-form requests so close() can
	// expire them (a response after run end fails fast).
	outstandingMu sync.Mutex
	outstanding   map[string]*ApprovalRequest

	failMu  sync.Mutex
	failure *driver.RunFailure

	// Core owns every admitted run envelope, including Driver-only execution.
	// The final Go outcome, after teardown, determines the one public terminal.
	lifecycleMu      sync.Mutex
	lifecycleActive  bool
	lifecycleEnded   bool
	providerRunID    string
	providerThreadID string
	driverTerminal   *RunFinished
	terminalSource   *EventSourceMeta
	observerCtx      context.Context
	observerInfo     RunEventInfo
	observers        []runObserverState
	observerDeadline atomic.Int64 // earliest cleanup deadline, independent of publication
}

type eventSinkConfig struct {
	runID     string
	threadKey string
	buffer    int
	blocking  bool
	policy    ApprovalPolicy
	handler   ApprovalHandler
	caps      driver.RunPolicyCapabilities
}

func newEventSink(cfg eventSinkConfig) *eventSink {
	buf := cfg.buffer
	if buf <= 0 {
		buf = defaultEventBuffer
	}
	broker := newEventBroker(cfg.runID, cfg.threadKey, buf, cfg.blocking)
	return &eventSink{
		events:      broker.events,
		broker:      broker,
		runID:       cfg.runID,
		policy:      effectiveApprovalPolicy(cfg.policy),
		handler:     cfg.handler,
		caps:        cfg.caps,
		retryWarned: map[driver.HumanDecisionKind]struct{}{},
		outstanding: map[string]*ApprovalRequest{},
	}
}

// effectiveApprovalPolicy materializes the package defaults for unset fields.
func effectiveApprovalPolicy(p ApprovalPolicy) ApprovalPolicy {
	if p.Permission == driver.HumanDecisionUnset {
		p.Permission = driver.HumanDecisionAsk
	}
	if p.PlanReview == driver.HumanDecisionUnset {
		p.PlanReview = driver.HumanDecisionAsk
	}
	if p.Question == driver.QuestionUnset {
		p.Question = driver.QuestionAutoReject
	}
	if p.Timeout == 0 {
		p.Timeout = driver.DefaultHumanDecisionTimeout
	}
	if p.OnTimeout == driver.FailureActionUnset {
		p.OnTimeout = driver.FailureAbort
	}
	if p.OnReject == driver.FailureActionUnset {
		p.OnReject = driver.FailureAbort
	}
	if p.MaxRetries == 0 {
		p.MaxRetries = driver.DefaultHumanDecisionMaxRetries
	}
	return p
}

// ---------------------------------------------------------------------------
// EventSink: translation + backpressure
// ---------------------------------------------------------------------------

func (s *eventSink) Emit(ev driver.RunEvent) error {
	var source *EventSourceMeta
	if ev.Seq != 0 || !ev.Timestamp.IsZero() {
		source = &EventSourceMeta{Sequence: ev.Seq, Timestamp: ev.Timestamp}
	}
	s.pushWithSource(eventFromRunEvent(ev), source)
	return nil
}

func (s *eventSink) EmitStream(p driver.StreamPayload) error {
	if err := validateObservationPayload(p); err != nil {
		return err
	}
	source := sourceMetaFromStreamPayload(p)
	if s.captureDriverLifecycle(p, source) {
		return nil
	}
	event := eventFromStreamPayload(p)
	if p.Kind == driver.StreamRunFinished || p.Kind == driver.StreamRunError {
		// In a Driver-only stream the provider owns the lifecycle envelope.
		// Its terminal is still a hard public boundary: use the broker's
		// reserved terminal slot and atomically seal out any later payloads.
		s.broker.publishTerminal(event, source)
		return nil
	}
	s.pushWithSource(event, source)
	return nil
}

func sourceMetaFromStreamPayload(p driver.StreamPayload) *EventSourceMeta {
	sequence := p.Sequence
	if sequence == 0 {
		sequence = p.Seq
	}
	if p.RunID == "" && p.ThreadID == "" && p.TurnID == "" && sequence == 0 && p.Timestamp.IsZero() {
		return nil
	}
	return &EventSourceMeta{
		RunID: p.RunID, ThreadID: p.ThreadID, TurnID: p.TurnID,
		Sequence: sequence, Timestamp: p.Timestamp,
	}
}

// enableAuthoritativeLifecycle starts the core-owned public run envelope
// before resource acquisition or Driver dispatch.
func (s *eventSink) enableAuthoritativeLifecycle() {
	if s == nil || s.broker == nil {
		return
	}
	s.lifecycleMu.Lock()
	if s.lifecycleActive {
		s.lifecycleMu.Unlock()
		return
	}
	s.lifecycleActive = true
	s.lifecycleMu.Unlock()
	s.push(RunStarted{RunID: s.runID})
}

// captureDriverLifecycle suppresses duplicate Driver run-envelope events
// while the core owns the merged lifecycle. Non-lifecycle payloads continue
// through the broker immediately. The terminal is retained as a source of
// provider coordinates/usage, but the final Go outcome remains authoritative.
func (s *eventSink) captureDriverLifecycle(p driver.StreamPayload, source *EventSourceMeta) bool {
	if s == nil {
		return false
	}
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	if !s.lifecycleActive {
		return false
	}
	switch p.Kind {
	case driver.StreamRunStarted:
		if p.RunID != "" {
			s.providerRunID = p.RunID
		}
		if p.ThreadID != "" {
			s.providerThreadID = p.ThreadID
		}
		return true
	case driver.StreamRunFinished, driver.StreamRunError:
		if s.driverTerminal == nil {
			terminal, _ := eventFromStreamPayload(p).(RunFinished)
			if terminal.Usage != nil {
				usage := *terminal.Usage
				terminal.Usage = &usage
			}
			s.driverTerminal = &terminal
			s.terminalSource = cloneEventSourceMeta(source)
		}
		return true
	default:
		return s.driverTerminal != nil
	}
}

// completeAuthoritativeLifecycle publishes the merged stream's unique
// terminal event. It is called after host event pumps and run-scoped
// resources have completed teardown, immediately before broker close.
func (s *eventSink) completeAuthoritativeLifecycle(res *Result, runErr error) {
	if s == nil || s.broker == nil {
		return
	}
	s.lifecycleMu.Lock()
	if !s.lifecycleActive || s.lifecycleEnded {
		s.lifecycleMu.Unlock()
		return
	}
	s.lifecycleEnded = true
	terminal := RunFinished{RunID: s.providerRunID, ThreadID: s.providerThreadID}
	if s.driverTerminal != nil {
		terminal = *s.driverTerminal
		if terminal.Usage != nil {
			usage := *terminal.Usage
			terminal.Usage = &usage
		}
	}
	if terminal.RunID == "" {
		terminal.RunID = s.runID
	}
	if terminal.ThreadID == "" {
		terminal.ThreadID = s.providerThreadID
	}
	source := cloneEventSourceMeta(s.terminalSource)
	s.lifecycleMu.Unlock()

	if terminal.Usage == nil && res != nil && res.Usage != nil {
		usage := *res.Usage
		terminal.Usage = &usage
	}
	terminal.Failed = runErr != nil
	terminal.Reason = ""
	terminal.Message = ""
	if runErr != nil {
		var business *RunError
		outcome := s.terminalSnapshot()
		switch {
		case errors.As(runErr, &business):
			terminal.Reason = business.Reason
			terminal.Message = business.Message
		case outcome.reason != "":
			terminal.Reason, terminal.Message = outcome.reason, outcome.message
		case errors.Is(runErr, ErrActiveExecutionTimeout):
			terminal.Reason = ReasonActiveExecutionTimeout
			terminal.Message = "active execution budget exhausted"
		case errors.Is(runErr, context.DeadlineExceeded):
			terminal.Reason = ReasonDeadlineExceeded
			terminal.Message = "run deadline exceeded"
		case errors.Is(runErr, context.Canceled):
			terminal.Reason = ReasonCancelled
			terminal.Message = runErr.Error()
		default:
			terminal.Reason = ReasonInfrastructure
			terminal.Message = "run infrastructure failed"
		}
	}
	s.broker.publishTerminal(terminal, source)
}

// push delivers one event under the configured backpressure strategy.
//
// Drop mode (default): non-blocking send; overflow increments a counter
// that is flushed as one aggregated Dropped{Count} marker before the next
// event that fits. The marker is delivered before that event and is never
// duplicated.
//
// Blocking mode (WithBlockingEvents): the send waits for the consumer;
// close() releases pending senders. Emitting on a closed sink is a no-op,
// never a panic.
func (s *eventSink) push(ev Event) {
	s.pushWithSource(ev, sourceMetaFromEvent(ev))
}

func (s *eventSink) pushWithSource(ev Event, source *EventSourceMeta) {
	if s == nil || s.broker == nil {
		return
	}
	s.broker.publish(ev, source)
}

// close seals the sink: releases blocked senders, expires unanswered
// approval requests, flushes the final drop marker, and closes the event
// channel. Idempotent.
func (s *eventSink) close() {
	if s == nil {
		return
	}
	s.expireOutstanding()
	s.broker.close()
}

func (s *eventSink) abort() {
	if s == nil {
		return
	}
	s.expireOutstanding()
	s.broker.abort()
}

func (s *eventSink) expireOutstanding() {
	s.outstandingMu.Lock()
	outstanding := s.outstanding
	s.outstanding = map[string]*ApprovalRequest{}
	s.outstandingMu.Unlock()
	for _, r := range outstanding {
		r.expire()
	}
}

func sourceMetaFromEvent(ev Event) *EventSourceMeta {
	if ev == nil {
		return nil
	}
	meta := ev.Meta()
	if meta.Source != nil {
		return cloneEventSourceMeta(meta.Source)
	}
	if meta.RunID == "" && meta.ThreadKey == "" && meta.TurnID == "" && meta.Sequence == 0 && meta.Time.IsZero() {
		return nil
	}
	return &EventSourceMeta{
		RunID: meta.RunID, ThreadID: meta.ThreadKey, TurnID: meta.TurnID,
		Sequence: meta.Sequence, Timestamp: meta.Time,
	}
}

func (s *eventSink) setPendingFailure(f *driver.RunFailure) {
	s.failMu.Lock()
	if s.failure == nil {
		s.failure = f
	}
	s.failMu.Unlock()
	s.recordOutcome(nil, f, nil, nil)
	if s.budget != nil {
		s.budget.Cancel(errApprovalAbort)
	}
}

func (s *eventSink) pendingFailure() *driver.RunFailure {
	s.failMu.Lock()
	defer s.failMu.Unlock()
	return s.failure
}

// ---------------------------------------------------------------------------
// DecisionCapableSink: the approval dispatcher
// ---------------------------------------------------------------------------

// RequestDecision routes one approval request: normalize, apply automatic
// policy modes, dispatch the callback or event form, then apply OnReject or
// OnTimeout with bounded, capability-gated retry.
//
// Driver SPI return contract:
//   - (resp, nil): the driver proceeds using resp.Result — Approved,
//     Answered, or Rejected/TimedOut when the fallback is Continue.
//   - (_, err): the run must end; the failure context is already recorded
//     and is overlaid onto the driver response by the stream pipeline.
func (s *eventSink) RequestDecision(ctx context.Context, req driver.DecisionRequest) (driver.DecisionResponse, error) {
	req = s.normalizeRequest(req)
	kind := req.Kind

	// Policy short-circuit: auto modes resolve without asking anyone.
	if resp, decided := s.tryAutoResolve(req); decided {
		s.pushRequestedNotice(req)
		out, err := s.applyAutoResolve(req, resp)
		s.pushResolvedNotice(req, resp, time.Now().UTC())
		return out, err
	}

	var attempts int
	for {
		attempts++
		req.RetryAttempt = attempts - 1

		resp, decision, runErr := s.dispatchOnce(ctx, req)

		switch decision {
		case driver.DecisionApproved, driver.DecisionAnswered:
			s.pushResolvedNotice(req, resp, time.Now().UTC())
			return resp, nil

		case driver.DecisionRejected:
			out, abortErr := s.applyFailureAction(req, resp, attempts, kind, s.policy.OnReject, driver.FailureReject, driver.DecisionRejected)
			s.pushResolvedNotice(req, resp, time.Now().UTC())
			if abortErr == nil && out.retry {
				req = s.renewForRetry(req)
				continue
			}
			return out.resp, abortErr

		case driver.DecisionTimedOut:
			timedOut := driver.DecisionResponse{RequestID: req.RequestID, Result: driver.DecisionTimedOut}
			out, abortErr := s.applyFailureAction(req, timedOut, attempts, kind, s.policy.OnTimeout, driver.FailureTimeout, driver.DecisionTimedOut)
			s.pushResolvedNotice(req, resp, time.Now().UTC())
			if abortErr == nil && out.retry {
				req = s.renewForRetry(req)
				continue
			}
			return out.resp, abortErr

		default: // DecisionAborted (ctx cancelled, handler error/panic, run end)
			// Preserve the original handler/context cause. Finalization adds
			// the one partial Result carrier; panic/unresolved handlers have
			// already registered their more specific failure.
			if runErr == nil {
				runErr = errApprovalAbort
			}
			s.recordOutcome(ctx, nil, runErr, nil)
			s.pushResolvedNotice(req, resp, time.Now().UTC())
			return resp, runErr
		}
	}
}

// tryAutoResolve synthesizes the response for AutoApprove / AutoDeny modes.
func (s *eventSink) tryAutoResolve(req driver.DecisionRequest) (driver.DecisionResponse, bool) {
	switch req.Kind {
	case driver.HumanDecisionPermission:
		switch s.policy.Permission {
		case driver.HumanDecisionAutoApprove:
			return driver.DecisionResponse{RequestID: req.RequestID, Result: driver.DecisionApproved}, true
		case driver.HumanDecisionAutoReject:
			return driver.DecisionResponse{RequestID: req.RequestID, Result: driver.DecisionRejected}, true
		}
	case driver.HumanDecisionPlanReview:
		switch s.policy.PlanReview {
		case driver.HumanDecisionAutoApprove:
			return driver.DecisionResponse{RequestID: req.RequestID, Result: driver.DecisionApproved}, true
		case driver.HumanDecisionAutoReject:
			return driver.DecisionResponse{RequestID: req.RequestID, Result: driver.DecisionRejected}, true
		}
	case driver.HumanDecisionQuestion:
		if s.policy.Question == driver.QuestionAutoReject {
			return driver.DecisionResponse{RequestID: req.RequestID, Result: driver.DecisionRejected}, true
		}
	}
	return driver.DecisionResponse{}, false
}

// applyAutoResolve finishes the auto path: approvals return immediately,
// denials route through OnReject. Retrying a deterministic auto-denial
// would loop forever, so FallbackRetry degrades to abort.
func (s *eventSink) applyAutoResolve(req driver.DecisionRequest, resp driver.DecisionResponse) (driver.DecisionResponse, error) {
	if resp.Result == driver.DecisionApproved {
		return resp, nil
	}
	out, err := s.applyFailureAction(req, resp, 1, req.Kind, s.policy.OnReject, driver.FailureReject, driver.DecisionRejected)
	if err != nil {
		return out.resp, err
	}
	if out.retry {
		s.setPendingFailure(&driver.RunFailure{
			Code:    driver.FailureReject,
			Message: "auto-denied approval cannot be retried; aborting",
			HumanDecision: &driver.HumanDecisionFailure{
				Kind:     req.Kind,
				Source:   req.Source,
				Decision: driver.DecisionRejected,
				Request:  cloneDecisionRequest(req),
				Attempts: 1,
			},
		})
		return out.resp, errApprovalAbort
	}
	return out.resp, nil
}

// dispatchOnce runs one ask attempt under the request deadline: callback
// form when OnApproval is installed, event form otherwise.
var errApprovalDeadline = errors.New("adaptor: approval attempt deadline")

func (s *eventSink) dispatchOnce(ctx context.Context, req driver.DecisionRequest) (driver.DecisionResponse, driver.DecisionResult, error) {
	release := s.budget.Pause()
	defer release()
	if ctx.Err() != nil {
		return driver.DecisionResponse{RequestID: req.RequestID, Result: driver.DecisionAborted}, driver.DecisionAborted, errors.Join(ctx.Err(), context.Cause(ctx))
	}
	dctx, cancel := withDecisionDeadline(ctx, req.Deadline)
	defer cancel()
	var resp driver.DecisionResponse
	var result driver.DecisionResult
	var err error
	if s.handler != nil {
		resp, result, err = s.runHandler(dctx, req)
	} else {
		resp, result, err = s.runEventDispatch(dctx, req)
	}
	// Only this attempt's own wall-clock timer can produce DecisionTimedOut.
	// Parent deadline/custom cause/active exhaustion retain their real identity.
	if result != driver.DecisionApproved && result != driver.DecisionAnswered && ctx.Err() != nil {
		s.recordContext(ctx)
		return driver.DecisionResponse{RequestID: req.RequestID, Result: driver.DecisionAborted}, driver.DecisionAborted, errors.Join(err, ctx.Err(), context.Cause(ctx))
	}
	if result == driver.DecisionAborted && context.Cause(dctx) == errApprovalDeadline {
		return driver.DecisionResponse{RequestID: req.RequestID, Result: driver.DecisionTimedOut}, driver.DecisionTimedOut, nil
	}
	return resp, result, err
}

// runHandler is form A: invoke the OnApproval callback with a live request.
// The handler resolves the request and returns nil; a handler error aborts
// the run; a panic or an unresolved return is an agent error.
func (s *eventSink) runHandler(ctx context.Context, req driver.DecisionRequest) (driver.DecisionResponse, driver.DecisionResult, error) {
	ar := newApprovalRequest(req)
	if !s.pushRequestedNoticeContext(ctx, req) {
		ar.expire()
		if context.Cause(ctx) == errApprovalDeadline {
			return driver.DecisionResponse{RequestID: req.RequestID, Result: driver.DecisionTimedOut}, driver.DecisionTimedOut, nil
		}
		return driver.DecisionResponse{RequestID: req.RequestID, Result: driver.DecisionAborted}, driver.DecisionAborted, errors.Join(ctx.Err(), context.Cause(ctx))
	}

	outcome := make(chan error, 1)
	panicked := make(chan string, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				panicked <- fmt.Sprintf("%v", r)
			}
		}()
		outcome <- s.handler(ctx, ar)
	}()

	select {
	case msg := <-panicked:
		if ctx.Err() != nil {
			return decisionContextEnded(ctx, req, ar)
		}
		ar.expire()
		err := fmt.Errorf("approval handler panic: %s", msg)
		s.setPendingFailure(&driver.RunFailure{Code: driver.FailureAgentError, Message: err.Error()})
		return driver.DecisionResponse{RequestID: req.RequestID, Result: driver.DecisionAborted}, driver.DecisionAborted, err

	case err := <-outcome:
		if ctx.Err() != nil {
			return decisionContextEnded(ctx, req, ar)
		}
		if err != nil {
			ar.expire()
			return driver.DecisionResponse{RequestID: req.RequestID, Result: driver.DecisionAborted}, driver.DecisionAborted, err
		}
		if resp, ok := ar.takeResponse(); ok {
			resp.RequestID = req.RequestID
			return resp, resp.Result, nil
		}
		ar.expire()
		rerr := errors.New("approval handler returned without resolving the request")
		s.setPendingFailure(&driver.RunFailure{Code: driver.FailureAgentError, Message: rerr.Error()})
		return driver.DecisionResponse{RequestID: req.RequestID, Result: driver.DecisionAborted}, driver.DecisionAborted, rerr

	case <-ctx.Done():
		// The handler goroutine may still settle; expire recovers a
		// response that won the race so it is honored.
		if resp, ok := ar.expire(); ok {
			resp.RequestID = req.RequestID
			return resp, resp.Result, nil
		}
		if context.Cause(ctx) == errApprovalDeadline {
			return driver.DecisionResponse{RequestID: req.RequestID, Result: driver.DecisionTimedOut}, driver.DecisionTimedOut, nil
		}
		return driver.DecisionResponse{RequestID: req.RequestID, Result: driver.DecisionAborted}, driver.DecisionAborted, ctx.Err()
	}
}

func decisionContextEnded(ctx context.Context, req driver.DecisionRequest, ar *ApprovalRequest) (driver.DecisionResponse, driver.DecisionResult, error) {
	if resp, ok := ar.expire(); ok {
		resp.RequestID = req.RequestID
		return resp, resp.Result, nil
	}
	if context.Cause(ctx) == errApprovalDeadline {
		return driver.DecisionResponse{RequestID: req.RequestID, Result: driver.DecisionTimedOut}, driver.DecisionTimedOut, nil
	}
	return driver.DecisionResponse{RequestID: req.RequestID, Result: driver.DecisionAborted}, driver.DecisionAborted, errors.Join(ctx.Err(), context.Cause(ctx))
}

// runEventDispatch is form B: enqueue the live *ApprovalRequest on the
// event stream and wait for a responder call. The enqueue is exempt from
// the drop strategy — it blocks until the consumer has room, bounded by
// the same deadline that guards an unconsumed request (timeout → policy
// fallback).
func (s *eventSink) runEventDispatch(ctx context.Context, req driver.DecisionRequest) (driver.DecisionResponse, driver.DecisionResult, error) {
	ar := newApprovalRequest(req)

	s.outstandingMu.Lock()
	if _, duplicate := s.outstanding[ar.ID]; duplicate {
		s.outstandingMu.Unlock()
		ar.expire()
		return driver.DecisionResponse{RequestID: req.RequestID, Result: driver.DecisionAborted}, driver.DecisionAborted, errors.New("adaptor: duplicate outstanding approval request ID")
	}
	s.outstanding[ar.ID] = ar
	s.outstandingMu.Unlock()
	defer func() {
		s.outstandingMu.Lock()
		delete(s.outstanding, ar.ID)
		s.outstandingMu.Unlock()
	}()

	enqueued := s.broker.publishContext(ctx, ar, nil)

	if !enqueued {
		if resp, ok := ar.expire(); ok {
			resp.RequestID = req.RequestID
			return resp, resp.Result, nil
		}
		if ctx.Err() != nil && context.Cause(ctx) == errApprovalDeadline {
			return driver.DecisionResponse{RequestID: req.RequestID, Result: driver.DecisionTimedOut}, driver.DecisionTimedOut, nil
		}
		if ctx.Err() != nil {
			return driver.DecisionResponse{RequestID: req.RequestID, Result: driver.DecisionAborted}, driver.DecisionAborted, ctx.Err()
		}
		return driver.DecisionResponse{RequestID: req.RequestID, Result: driver.DecisionAborted}, driver.DecisionAborted, context.Canceled
	}

	select {
	case <-ar.ready():
		if resp, ok := ar.takeResponse(); ok {
			resp.RequestID = req.RequestID
			return resp, resp.Result, nil
		}
		if context.Cause(ctx) == errApprovalDeadline {
			return driver.DecisionResponse{RequestID: req.RequestID, Result: driver.DecisionTimedOut}, driver.DecisionTimedOut, nil
		}
		if ctx.Err() != nil {
			return driver.DecisionResponse{RequestID: req.RequestID, Result: driver.DecisionAborted}, driver.DecisionAborted, ctx.Err()
		}
		return driver.DecisionResponse{RequestID: req.RequestID, Result: driver.DecisionAborted}, driver.DecisionAborted, context.Canceled
	case <-s.broker.abortCh:
		if resp, ok := ar.expire(); ok {
			resp.RequestID = req.RequestID
			return resp, resp.Result, nil
		}
		return driver.DecisionResponse{RequestID: req.RequestID, Result: driver.DecisionAborted}, driver.DecisionAborted, context.Canceled
	case <-ctx.Done():
		if resp, ok := ar.expire(); ok {
			resp.RequestID = req.RequestID
			return resp, resp.Result, nil
		}
		if context.Cause(ctx) == errApprovalDeadline {
			return driver.DecisionResponse{RequestID: req.RequestID, Result: driver.DecisionTimedOut}, driver.DecisionTimedOut, nil
		}
		return driver.DecisionResponse{RequestID: req.RequestID, Result: driver.DecisionAborted}, driver.DecisionAborted, ctx.Err()
	}
}

// retryOutcome carries either a retry instruction or the response to return.
type retryOutcome struct {
	retry bool
	resp  driver.DecisionResponse
}

// applyFailureAction centralizes OnReject and OnTimeout handling:
//
//	Continue            → (resp, nil), no failure recorded
//	Retry, unsupported  → one-time warning Notice + abort with failure
//	Retry, exhausted    → abort with "exhausted retries" failure
//	Retry               → renew request, ask again
//	Abort / unset       → abort with failure
func (s *eventSink) applyFailureAction(req driver.DecisionRequest, resp driver.DecisionResponse, attempts int, kind driver.HumanDecisionKind, action driver.FailureAction, code driver.FailureCode, decision driver.DecisionResult) (retryOutcome, error) {
	switch action {
	case driver.FailureContinue:
		return retryOutcome{resp: resp}, nil

	case driver.FailureRetry:
		if !s.retrySupported(kind) {
			s.pushRetryUnsupportedWarning(kind, action)
			s.setPendingFailure(&driver.RunFailure{
				Code:    code,
				Message: decisionFailureMessage(decision, kind, req.Source),
				HumanDecision: &driver.HumanDecisionFailure{
					Kind:     kind,
					Source:   req.Source,
					Decision: decision,
					Request:  cloneDecisionRequest(req),
					Attempts: attempts,
				},
			})
			return retryOutcome{resp: resp}, errApprovalAbort
		}
		if attempts > s.policy.MaxRetries {
			s.setPendingFailure(&driver.RunFailure{
				Code:    code,
				Message: fmt.Sprintf("approval %s exhausted retries (%d attempts)", decision, attempts),
				HumanDecision: &driver.HumanDecisionFailure{
					Kind:     kind,
					Source:   req.Source,
					Decision: decision,
					Request:  cloneDecisionRequest(req),
					Attempts: attempts,
				},
			})
			return retryOutcome{resp: resp}, errApprovalAbort
		}
		return retryOutcome{retry: true, resp: resp}, nil

	default: // FailureAbort, FailureActionUnset, unknown
		s.setPendingFailure(&driver.RunFailure{
			Code:    code,
			Message: decisionFailureMessage(decision, kind, req.Source),
			HumanDecision: &driver.HumanDecisionFailure{
				Kind:     kind,
				Source:   req.Source,
				Decision: decision,
				Request:  cloneDecisionRequest(req),
				Attempts: attempts,
			},
		})
		return retryOutcome{resp: resp}, errApprovalAbort
	}
}

func (s *eventSink) retrySupported(kind driver.HumanDecisionKind) bool {
	switch kind {
	case driver.HumanDecisionPermission:
		return s.caps.Permission.Retry
	case driver.HumanDecisionPlanReview:
		return s.caps.PlanReview.Retry
	case driver.HumanDecisionQuestion:
		return s.caps.Question.Retry
	default:
		return false
	}
}

// pushRetryUnsupportedWarning emits the retry-degradation warning Notice at
// most once per kind.
func (s *eventSink) pushRetryUnsupportedWarning(kind driver.HumanDecisionKind, action driver.FailureAction) {
	s.retryMu.Lock()
	if _, dup := s.retryWarned[kind]; dup {
		s.retryMu.Unlock()
		return
	}
	s.retryWarned[kind] = struct{}{}
	s.retryMu.Unlock()

	s.push(Notice{
		Kind: NoticeLifecycle,
		Text: fmt.Sprintf("approval %s does not support %s; degrading to abort", kind, action),
		Data: map[string]any{
			"kind":    string(kind),
			"action":  string(action),
			"warning": "human_decision_retry_unsupported",
		},
	})
}

func (s *eventSink) renewForRetry(req driver.DecisionRequest) driver.DecisionRequest {
	next := req
	next.RequestID = s.nextDecisionID()
	next.CreatedAt = time.Now().UTC()
	next.Deadline = next.CreatedAt.Add(s.effectiveTimeout())
	next.RetryAttempt = req.RetryAttempt + 1
	return next
}

func (s *eventSink) normalizeRequest(req driver.DecisionRequest) driver.DecisionRequest {
	if req.RequestID == "" {
		req.RequestID = s.nextDecisionID()
	}
	if req.RunID == "" {
		req.RunID = s.runID
	}
	if req.CreatedAt.IsZero() {
		req.CreatedAt = time.Now().UTC()
	}
	if req.Deadline.IsZero() {
		req.Deadline = req.CreatedAt.Add(s.effectiveTimeout())
	}
	return req
}

func (s *eventSink) effectiveTimeout() time.Duration {
	d := s.policy.Timeout
	if d < 0 {
		// Negative means "never time out"; use a far-future deadline.
		return 100 * 365 * 24 * time.Hour
	}
	if d == 0 {
		return driver.DefaultHumanDecisionTimeout
	}
	return d
}

func (s *eventSink) nextDecisionID() string {
	return fmt.Sprintf("%s-dec-%d", s.runID, s.decSeq.Add(1))
}

func withDecisionDeadline(ctx context.Context, deadline time.Time) (context.Context, context.CancelFunc) {
	if deadline.IsZero() {
		return context.WithCancel(ctx)
	}
	return context.WithDeadlineCause(ctx, deadline, errApprovalDeadline)
}

// pushRequestedNotice broadcasts an approval request that does NOT appear
// as a *ApprovalRequest event (callback form and auto-resolved policy
// paths). In event form the *ApprovalRequest event itself is the request
// signal.
func (s *eventSink) pushRequestedNotice(req driver.DecisionRequest) {
	s.push(requestedNotice(req))
}
func (s *eventSink) pushRequestedNoticeContext(ctx context.Context, req driver.DecisionRequest) bool {
	return s.broker.publishContext(ctx, requestedNotice(req), nil)
}
func requestedNotice(req driver.DecisionRequest) Notice {
	return Notice{
		Kind: NoticeApprovalRequested,
		Text: req.Prompt,
		Data: map[string]any{
			"request_id":     req.RequestID,
			"kind":           string(req.Kind),
			"source":         req.Source,
			"tool_call_id":   req.ToolCallID,
			"payload":        maps.Clone(req.Payload),
			"choices":        append([]driver.DecisionChoice(nil), req.Choices...),
			"created_at":     req.CreatedAt,
			"deadline":       req.Deadline,
			"default_result": string(req.DefaultDecision),
			"attempt":        req.RetryAttempt,
		},
	}
}

// pushResolvedNotice broadcasts exactly one outcome for every approval
// attempt.
func (s *eventSink) pushResolvedNotice(req driver.DecisionRequest, resp driver.DecisionResponse, at time.Time) {
	var latency time.Duration
	if !req.CreatedAt.IsZero() {
		latency = at.Sub(req.CreatedAt)
	}
	s.push(Notice{
		Kind: NoticeApprovalResolved,
		Text: resp.Text,
		Data: map[string]any{
			"request_id":   req.RequestID,
			"kind":         string(req.Kind),
			"source":       req.Source,
			"tool_call_id": req.ToolCallID,
			"payload":      maps.Clone(req.Payload),
			"choices":      append([]driver.DecisionChoice(nil), req.Choices...),
			"created_at":   req.CreatedAt,
			"deadline":     req.Deadline,
			"result":       string(resp.Result),
			"choice":       resp.Choice,
			"answer":       maps.Clone(resp.Answer),
			"text":         resp.Text,
			"attempt":      req.RetryAttempt,
			"resolved_at":  at,
			"latency":      latency,
		},
	})
}

func decisionFailureMessage(decision driver.DecisionResult, kind driver.HumanDecisionKind, source string) string {
	src := source
	if src == "" {
		src = string(kind)
	}
	switch decision {
	case driver.DecisionRejected:
		return fmt.Sprintf("approval rejected: kind=%s source=%s", kind, src)
	case driver.DecisionTimedOut:
		return fmt.Sprintf("approval timed out: kind=%s source=%s", kind, src)
	default:
		return fmt.Sprintf("approval failed (%s): kind=%s source=%s", decision, kind, src)
	}
}

func cloneDecisionRequest(req driver.DecisionRequest) *driver.DecisionRequest {
	out := req
	if req.Payload != nil {
		out.Payload = make(map[string]any, len(req.Payload))
		for k, v := range req.Payload {
			out.Payload[k] = v
		}
	}
	if len(req.Choices) > 0 {
		out.Choices = append([]driver.DecisionChoice(nil), req.Choices...)
	}
	return &out
}

const observationTimeout = 100 * time.Millisecond

type observerContextKey struct{}
type runObserverState struct {
	callback RunEventObserver
	disabled bool
}

func invalidRunEvent() error { return fmt.Errorf("adaptor: invalid run event") }
func validSourceChain(source *EventSourceMeta) bool {
	seen := map[*EventSourceMeta]bool{}
	for source != nil {
		if seen[source] || len(seen) >= 8 {
			return false
		}
		seen[source] = true
		for _, s := range []string{source.RunID, source.ThreadID, source.TurnID, source.ScopeID, source.ToolCallID, source.InvocationID, source.DelegationID} {
			if !capabilityobs.ValidText(s, 2048, false) {
				return false
			}
		}
		source = source.Upstream
	}
	return true
}
func validObservationEvent(ev Event) bool {
	if ev == nil || !validSourceChain(ev.Meta().Source) {
		return false
	}
	switch e := ev.(type) {
	case CapabilityInvocation:
		return capabilityobs.Validate(e.Invocation) == nil
	case TodoUpdated:
		return todoobs.Validate(e.Snapshot) == nil
	default:
		return true
	}
}
func validHostEvent(ev Event) bool {
	ev = cloneEventValue(ev)
	if !validObservationEvent(ev) {
		return false
	}
	switch e := ev.(type) {
	case CapabilityInvocation:
		return e.Invocation.Source == capability.Host || e.Invocation.Source == capability.Relay
	case TodoUpdated, SubagentUpdate, Notice, Dropped:
		return true
	default:
		return false
	}
}
func validateObservationPayload(p driver.StreamPayload) error {
	if p.Kind != driver.StreamCapabilityInvocation && p.Kind != driver.StreamTodoUpdated {
		if p.Capability != nil || p.Todo != nil {
			return invalidRunEvent()
		}
		return nil
	}
	if p.Args != nil || p.Result != nil || p.Raw != nil || p.HITLRequested != nil || p.HITLResolved != nil || p.Role != "" || p.Delta != "" || p.Name != "" || p.Error != nil || p.Usage != nil || p.MessageID != "" || p.ToolCallID != "" || p.ScopeID != "" || p.ParentScopeID != "" || p.ParentToolCallID != "" {
		return invalidRunEvent()
	}
	switch p.Kind {
	case driver.StreamCapabilityInvocation:
		if p.Capability == nil || p.Todo != nil || capabilityobs.Validate(*p.Capability) != nil {
			return invalidRunEvent()
		}
	case driver.StreamTodoUpdated:
		if p.Todo == nil || p.Capability != nil || todoobs.Validate(*p.Todo) != nil {
			return invalidRunEvent()
		}
	}
	return nil
}
func (s *eventSink) installObservers(ctx context.Context, info RunEventInfo, observers []RunEventObserver) {
	s.broker.mu.Lock()
	defer s.broker.mu.Unlock()
	s.observerCtx = ctx
	s.observerInfo = info
	s.observers = make([]runObserverState, len(observers))
	for i, callback := range observers {
		s.observers[i].callback = callback
	}
	s.broker.observe = s.observeEvent
}
func (s *eventSink) stopObservers() {
	s.broker.mu.Lock()
	defer s.broker.mu.Unlock()
	s.broker.observe = nil
	s.observers = nil
}

// limitObserverCleanup installs the earliest total cleanup deadline without
// taking the publication gate; later callbacks cannot renew that budget.
func (s *eventSink) limitObserverCleanup(deadline time.Time) {
	next := deadline.UnixNano()
	for {
		current := s.observerDeadline.Load()
		if current != 0 && current <= next {
			return
		}
		if s.observerDeadline.CompareAndSwap(current, next) {
			return
		}
	}
}
func (s *eventSink) observeEvent(ev Event) []Event {
	switch ev.(type) {
	case CapabilityInvocation, TodoUpdated:
	default:
		return nil
	}
	var notices []Event
	for i := range s.observers {
		state := &s.observers[i]
		if state.callback == nil || state.disabled {
			continue
		}
		parent := s.observerCtx
		if parent.Err() != nil {
			s.limitObserverCleanup(time.Now().Add(runResourceCleanupTimeout))
			parent = context.WithoutCancel(parent)
		}
		deadline := time.Now().Add(observationTimeout)
		if cleanup := s.observerDeadline.Load(); cleanup != 0 && cleanup < deadline.UnixNano() {
			deadline = time.Unix(0, cleanup)
		}
		if !time.Now().Before(deadline) {
			state.disabled = true
			notices = append(notices, Notice{Kind: NoticeRuntime, Data: map[string]any{"code": "observation_disabled", "reason": "timeout", "observer_index": i}})
			continue
		}
		ctx, cancel := context.WithDeadline(context.WithValue(parent, observerContextKey{}, s), deadline)
		type outcome struct {
			err      error
			panicked bool
		}
		done := make(chan outcome, 1)
		callback := state.callback
		private := WithEventMeta(ev, ev.Meta())
		info := s.observerInfo
		go func() {
			out := outcome{}
			defer func() {
				if recover() != nil {
					out.panicked = true
				}
				done <- out
			}()
			out.err = callback(ctx, info, private)
		}()
		reason := ""
		select {
		case out := <-done:
			// A callback may return nil precisely when its context expires.
			// Completion cannot win over the authoritative cancellation fence.
			if ctx.Err() != nil || !time.Now().Before(deadline) {
				reason = "timeout"
			} else if out.panicked {
				reason = "panic"
			} else if out.err != nil {
				reason = "error"
			}
		case <-ctx.Done():
			reason = "timeout"
		}
		cancel()
		if reason != "" {
			state.disabled = true
			notices = append(notices, Notice{Kind: NoticeRuntime, Data: map[string]any{"code": "observation_disabled", "reason": reason, "observer_index": i}})
		}
	}
	return notices
}

// invocationTerminal is the sole first-terminal record. Its mutex covers only
// candidate comparison and copying, never Driver code, events, callbacks or IO.
type invocationTerminal struct {
	mu      sync.Mutex
	ctx     context.Context
	parent  context.Context
	expired *ActiveExecutionTimeoutError
	reason  FailureReason
	message string
	details map[string]any
	cause   error
	ended   bool
}
type terminalOutcome struct {
	reason  FailureReason
	message string
	details map[string]any
	cause   error
}

func (s *eventSink) bindBudget(ctx context.Context, budget *activebudget.Controller, expired *ActiveExecutionTimeoutError) {
	s.budget = budget
	s.terminal.mu.Lock()
	s.terminal.parent = s.terminal.ctx
	s.terminal.ctx = ctx
	s.terminal.expired = expired
	s.terminal.mu.Unlock()
	context.AfterFunc(ctx, func() { s.recordContext(ctx); s.abort() })
}
func (s *eventSink) recordContext(ctx context.Context) {
	if ctx == nil || ctx.Err() == nil {
		return
	}
	s.recordOutcome(ctx, nil, nil, nil)
}
func (s *eventSink) recordCancellation(ctx context.Context) {
	if ctx.Err() != nil {
		s.recordContext(ctx)
		return
	}
	s.recordOutcome(nil, nil, context.Canceled, nil)
}
func (s *eventSink) recordOutcome(ctx context.Context, failure *driver.RunFailure, err, coordination error) {
	t := &s.terminal
	t.mu.Lock()
	defer t.mu.Unlock()
	s.recordOutcomeLocked(ctx, failure, err, coordination)
}
func (s *eventSink) recordOutcomeLocked(ctx context.Context, failure *driver.RunFailure, err, coordination error) {
	t := &s.terminal
	if t.ended {
		return
	}
	if ctx == nil {
		ctx = t.ctx
	}
	var contextErr, errorCause error
	if ctx != nil && ctx.Err() != nil {
		contextErr = ctx.Err()
		errorCause = context.Cause(ctx)
	}
	t.cause = errors.Join(t.cause, err, coordination, contextErr, errorCause)
	// Cause identity distinguishes this controller's selected expiry from an
	// inherited active-looking parent cause. Its cancellation may precede the
	// core AfterFunc notification, including when the parent cancels afterwards.
	ownExpiry := t.expired != nil && t.ctx != nil && context.Cause(t.ctx) == t.expired
	if ownExpiry {
		contextErr, errorCause = t.ctx.Err(), t.expired
		t.cause = errors.Join(t.cause, contextErr, errorCause)
	} else if t.parent != nil && t.parent.Err() != nil {
		contextErr, errorCause = t.parent.Err(), context.Cause(t.parent)
		t.cause = errors.Join(t.cause, contextErr, errorCause)
	}
	if t.reason != "" {
		return
	}
	switch {
	case contextErr != nil:
		switch {
		case ownExpiry:
			t.reason, t.message = ReasonActiveExecutionTimeout, "active execution budget exhausted"
		case contextErr == context.DeadlineExceeded:
			t.reason, t.message = ReasonDeadlineExceeded, "execution deadline exceeded"
		default:
			t.reason, t.message = ReasonCancelled, "execution cancelled"
		}
	case failure != nil:
		t.reason, t.message, t.details = failureReason(failure.Code), failure.Message, maps.Clone(failure.Metadata)
	case coordination != nil:
		t.reason, t.message = ReasonInfrastructure, "Thread coordination failed"
	case err != nil:
		switch {
		case errors.Is(err, ErrActiveExecutionTimeout):
			t.reason, t.message = ReasonActiveExecutionTimeout, "active execution budget exhausted"
		case errors.Is(err, context.DeadlineExceeded):
			t.reason, t.message = ReasonDeadlineExceeded, "execution deadline exceeded"
		case errors.Is(err, context.Canceled):
			t.reason, t.message = ReasonCancelled, "execution cancelled"
		default:
			t.reason, t.message = ReasonInfrastructure, "execution failed"
		}
	}
}
func (s *eventSink) finishTerminal() {
	s.terminal.mu.Lock()
	s.terminal.ended = true
	s.terminal.mu.Unlock()
}
func (s *eventSink) terminalSnapshot() terminalOutcome {
	s.terminal.mu.Lock()
	defer s.terminal.mu.Unlock()
	return terminalOutcome{s.terminal.reason, s.terminal.message, maps.Clone(s.terminal.details), s.terminal.cause}
}

// finishExecution orders the health seal against every concrete terminal
// candidate. The budget mutex never calls back here, so the lock order is one-way.
func (s *eventSink) finishExecution(ctx context.Context) error {
	s.terminal.mu.Lock()
	defer s.terminal.mu.Unlock()
	s.recordOutcomeLocked(ctx, nil, nil, nil)
	if s.terminal.reason != "" {
		return errors.Join(errApprovalAbort, s.terminal.cause)
	}
	err := s.budget.FinishExecution()
	if err != nil {
		s.recordOutcomeLocked(ctx, nil, err, nil)
	}
	return err
}
