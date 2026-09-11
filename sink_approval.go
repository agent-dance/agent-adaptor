package adaptor

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"time"

	"github.com/agent-dance/agent-adaptor/driver"
)

// errApprovalAbort is the sentinel RequestDecision returns to the driver
// when an approval fallback aborts the run. The failure context is already
// recorded on the sink; conforming drivers stop their protocol loop and
// return without overlaying their own failure. finalizeRun recognizes the
// sentinel so lazy drivers that return it verbatim still take the business
// failure path.
var errApprovalAbort = errors.New("adaptor: approval aborted the run")

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
		err := fmt.Errorf("approval handler panic: %s", msg)
		if ctx.Err() != nil {
			return s.decisionContextEnded(ctx, req, ar, err)
		}
		ar.expire()
		s.setPendingFailure(&driver.RunFailure{Code: driver.FailureAgentError, Message: err.Error()})
		return driver.DecisionResponse{RequestID: req.RequestID, Result: driver.DecisionAborted}, driver.DecisionAborted, err

	case err := <-outcome:
		if ctx.Err() != nil {
			return s.decisionContextEnded(ctx, req, ar, err)
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

func (s *eventSink) decisionContextEnded(ctx context.Context, req driver.DecisionRequest, ar *ApprovalRequest, observed error) (driver.DecisionResponse, driver.DecisionResult, error) {
	// The receiver already observed the handler outcome. Retain its cause
	// without turning an existing response or a continuing timeout into a
	// failure. A handler that settles after this receiver exits is not awaited.
	if observed != nil {
		s.terminal.mu.Lock()
		if !s.terminal.ended {
			s.terminal.cause = errors.Join(s.terminal.cause, observed)
		}
		s.terminal.mu.Unlock()
	}
	if resp, ok := ar.expire(); ok {
		resp.RequestID = req.RequestID
		return resp, resp.Result, nil
	}
	if context.Cause(ctx) == errApprovalDeadline {
		return driver.DecisionResponse{RequestID: req.RequestID, Result: driver.DecisionTimedOut}, driver.DecisionTimedOut, nil
	}
	return driver.DecisionResponse{RequestID: req.RequestID, Result: driver.DecisionAborted}, driver.DecisionAborted, errors.Join(ctx.Err(), context.Cause(ctx), observed)
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
