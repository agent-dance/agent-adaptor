package adaptor

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"sync"
	"time"

	"github.com/agent-dance/agent-adaptor/driver"
)

// Approval requests support two consumption forms:
//
//	Callback: adaptor.OnApproval(func(ctx, req *ApprovalRequest) error { ... })
//	Event:    case *adaptor.ApprovalRequest: on the Stream; answer it from any goroutine.
//
// The responder lives on the request (Approve / Deny / Answer), so a kind
// mismatch is rejected by the response methods. Timeout, retry, and fallback
// behavior is controlled exclusively by Policy.Approvals.

// ApprovalKind labels the semantic category of an approval request.
type ApprovalKind string

const (
	// ApprovalPermission covers tool, command, file, or permission gates.
	ApprovalPermission ApprovalKind = ApprovalKind(driver.HumanDecisionPermission)
	// ApprovalPlanReview covers plan-mode approval before execution.
	ApprovalPlanReview ApprovalKind = ApprovalKind(driver.HumanDecisionPlanReview)
	// ApprovalQuestion covers structured clarification questions.
	ApprovalQuestion ApprovalKind = ApprovalKind(driver.HumanDecisionQuestion)
)

// Choice is a single renderable option attached to an ApprovalQuestion
// request. It aliases the driver SPI type.
type Choice = driver.DecisionChoice

// ApprovalPolicy carries the timeout / fallback / retry knobs for approval
// requests, plus the per-kind routing modes. It aliases the driver SPI
// HumanDecisionPolicy. Zero-valued fields inherit the package defaults
// (Permission/PlanReview ask,
// Question auto-deny, 30s timeout, abort on timeout/reject, 3 max retries).
type ApprovalPolicy = driver.HumanDecisionPolicy

// ApprovalMode routes one binary approval kind (Permission / PlanReview).
type ApprovalMode = driver.HumanDecisionMode

const (
	// ApprovalInherit falls back to the SDK default (ask).
	ApprovalInherit ApprovalMode = driver.HumanDecisionUnset
	// ApprovalAsk routes the request to the host (callback or event form).
	ApprovalAsk ApprovalMode = driver.HumanDecisionAsk
	// ApprovalAutoApprove approves without asking (driver bypass path).
	ApprovalAutoApprove ApprovalMode = driver.HumanDecisionAutoApprove
	// ApprovalAutoDeny denies without asking.
	ApprovalAutoDeny ApprovalMode = driver.HumanDecisionAutoReject
)

// QuestionMode routes the Question kind. Auto-approve is intentionally
// absent: a question has no legitimate synthesized answer.
type QuestionMode = driver.QuestionMode

const (
	// QuestionInherit falls back to the SDK default (auto-deny).
	QuestionInherit QuestionMode = driver.QuestionUnset
	// QuestionAsk routes the question to the host.
	QuestionAsk QuestionMode = driver.QuestionAsk
	// QuestionAutoDeny denies questions without asking.
	QuestionAutoDeny QuestionMode = driver.QuestionAutoReject
)

// FallbackAction selects what happens when an approval times out
// (ApprovalPolicy.OnTimeout) or is denied (ApprovalPolicy.OnReject).
type FallbackAction = driver.FailureAction

const (
	// FallbackInherit falls back to the SDK default (abort).
	FallbackInherit FallbackAction = driver.FailureActionUnset
	// FallbackAbort terminates the run with a business failure.
	FallbackAbort FallbackAction = driver.FailureAbort
	// FallbackContinue forwards the outcome to the agent so the run can
	// progress.
	FallbackContinue FallbackAction = driver.FailureContinue
	// FallbackRetry re-asks the same decision, bounded by MaxRetries;
	// drivers without retry support degrade to abort with a warning
	// Notice on the stream.
	FallbackRetry FallbackAction = driver.FailureRetry
)

// ApprovalsAutoDeny is the ApprovalPolicy preset that explicitly denies every
// approval kind without asking. The bound Driver must advertise AutoReject
// for all three kinds; use a zero ApprovalPolicy for the portable conservative
// defaults when that capability is unavailable.
var ApprovalsAutoDeny = ApprovalPolicy{
	Permission: driver.HumanDecisionAutoReject,
	PlanReview: driver.HumanDecisionAutoReject,
	Question:   driver.QuestionAutoReject,
}

// ApprovalHandler is the callback form (form A) of approval consumption,
// installed with OnApproval. The handler must resolve the request — call
// Approve, Deny, or Answer — and return nil, or return an error to abort
// the run.
type ApprovalHandler func(ctx context.Context, req *ApprovalRequest) error

// ApproveAll returns a ready-made handler that approves every Permission
// and PlanReview request. Questions are denied: they have no legitimate
// synthesized answer.
func ApproveAll() ApprovalHandler {
	return func(ctx context.Context, req *ApprovalRequest) error {
		if req.Kind == ApprovalQuestion {
			return req.Deny(ctx, "questions cannot be auto-approved")
		}
		return req.Approve(ctx)
	}
}

// DenyAll returns a ready-made handler that denies every request with the
// given reason.
func DenyAll(reason string) ApprovalHandler {
	return func(ctx context.Context, req *ApprovalRequest) error {
		return req.Deny(ctx, reason)
	}
}

// Approval response errors.
var (
	// ErrApprovalResolved is returned by Approve / Deny / Answer when the
	// request was already resolved — by an earlier response, by the
	// timeout fallback, or because the run ended.
	ErrApprovalResolved = errors.New("adaptor: approval request already resolved")
	// ErrApprovalKindMismatch is returned when the response method does
	// not fit the request kind: Approve on a Question, or Answer on a
	// Permission / PlanReview request.
	ErrApprovalKindMismatch = errors.New("adaptor: response does not match approval kind")
	// ErrApprovalUnavailable is returned by a nil, zero-valued, or otherwise
	// unbound request. Such values have no run-owned responder and therefore
	// can never be answered.
	ErrApprovalUnavailable = errors.New("adaptor: approval responder unavailable")
	// ErrApprovalExpired identifies a request whose response window or owning
	// run ended. It wraps ErrApprovalResolved so callers can treat every late
	// response as an already-resolved request while still detecting expiry.
	ErrApprovalExpired = fmt.Errorf("%w: request expired", ErrApprovalResolved)
)

// ApprovalRequest is a human-in-the-loop request that carries its own
// responder. It arrives either through the OnApproval callback (form A) or
// as a *ApprovalRequest event on the Stream (form B); in both forms exactly
// one of Approve / Deny / Answer resolves it. Late or duplicate responses
// return ErrApprovalResolved; if nobody responds before Deadline, the
// ApprovalPolicy timeout fallback applies.
type ApprovalRequest struct {
	eventMetaCarrier
	// ID identifies this request (unique per run, fresh per retry).
	ID string
	// RunID is the SDK execution identifier of the owning run.
	RunID string
	// Kind is the request category; it gates which responder methods apply.
	Kind ApprovalKind
	// Title is the human-readable prompt to display.
	Title string
	// Source names the requesting surface (tool name, plan stage, ...).
	Source string

	// ToolCallID correlates a Permission request with the tool call that
	// triggered it (permission field group).
	ToolCallID string

	// Choices are the renderable options of a Question request (question
	// field group). Answer accepts one of the choice keys or free text.
	Choices []Choice

	// Details carries driver-specific structured request data.
	Details map[string]any

	// CreatedAt is when the current approval attempt was created.
	CreatedAt time.Time
	// Deadline bounds the response window; after it the ApprovalPolicy
	// OnTimeout fallback applies.
	Deadline time.Time

	// Attempt is the zero-based retry attempt (FallbackRetry re-asks).
	Attempt int

	responder *approvalResponder
}

type approvalState uint8

const (
	approvalPending approvalState = iota
	approvalResponded
	approvalExpired
)

// approvalResponder is the run-owned exactly-once state. ApprovalRequest is
// intentionally copyable: all value and pointer copies retain this pointer,
// so a copied request cannot manufacture a second response channel or lock.
type approvalResponder struct {
	mu    sync.Mutex
	state approvalState
	id    string
	kind  ApprovalKind
	resp  driver.DecisionResponse
	ready chan struct{}
}

// newApprovalRequest wraps a normalized DecisionRequest with a responder.
func newApprovalRequest(req driver.DecisionRequest) *ApprovalRequest {
	responder := &approvalResponder{
		id:    req.RequestID,
		kind:  ApprovalKind(req.Kind),
		ready: make(chan struct{}),
	}
	return &ApprovalRequest{
		ID:         req.RequestID,
		RunID:      req.RunID,
		Kind:       ApprovalKind(req.Kind),
		Title:      req.Prompt,
		Source:     req.Source,
		ToolCallID: req.ToolCallID,
		Choices:    append([]Choice(nil), req.Choices...),
		Details:    maps.Clone(req.Payload),
		CreatedAt:  req.CreatedAt,
		Deadline:   req.Deadline,
		Attempt:    req.RetryAttempt,
		responder:  responder,
	}
}

// Approve resolves a Permission or PlanReview request positively. Calling
// it on a Question returns ErrApprovalKindMismatch; calling it after the
// request was resolved returns ErrApprovalResolved.
func (r *ApprovalRequest) Approve(_ context.Context) error {
	responder, err := r.boundResponder()
	if err != nil {
		return err
	}
	if responder.kind == ApprovalQuestion {
		return ErrApprovalKindMismatch
	}
	return responder.settle(driver.DecisionResponse{Result: driver.DecisionApproved})
}

// Deny resolves any request kind negatively with a reason. What happens
// next is the ApprovalPolicy OnReject fallback (abort by default).
func (r *ApprovalRequest) Deny(_ context.Context, reason string) error {
	responder, err := r.boundResponder()
	if err != nil {
		return err
	}
	return responder.settle(driver.DecisionResponse{Result: driver.DecisionRejected, Text: reason})
}

// Answer resolves a Question request with the chosen option: one of the
// Choices keys, or free text for open questions. Calling it on a
// Permission / PlanReview request returns ErrApprovalKindMismatch.
func (r *ApprovalRequest) Answer(_ context.Context, option string) error {
	responder, err := r.boundResponder()
	if err != nil {
		return err
	}
	if responder.kind != ApprovalQuestion {
		return ErrApprovalKindMismatch
	}
	resp := driver.DecisionResponse{Result: driver.DecisionAnswered, Text: option}
	for _, c := range r.Choices {
		if c.Key == option {
			resp.Choice = option
			break
		}
	}
	return responder.settle(resp)
}

func (r *ApprovalRequest) boundResponder() (*approvalResponder, error) {
	if r == nil || r.responder == nil || r.responder.ready == nil {
		return nil, ErrApprovalUnavailable
	}
	return r.responder, nil
}

// settle records the consumer response exactly once and wakes every waiter
// by closing a readiness channel. It never sends to a possibly nil channel.
func (r *approvalResponder) settle(resp driver.DecisionResponse) error {
	if r == nil || r.ready == nil {
		return ErrApprovalUnavailable
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	switch r.state {
	case approvalResponded:
		return ErrApprovalResolved
	case approvalExpired:
		return ErrApprovalExpired
	}
	r.state = approvalResponded
	resp.RequestID = r.id
	r.resp = resp
	close(r.ready)
	return nil
}

// expire marks the request resolved from the SDK side (timeout, run end,
// abort) so late responses fail with ErrApprovalResolved. When a consumer
// response won the race under the same lock, expire recovers and returns
// it so the answer is honored instead of dropped.
func (r *ApprovalRequest) expire() (driver.DecisionResponse, bool) {
	responder, err := r.boundResponder()
	if err != nil {
		return driver.DecisionResponse{}, false
	}
	return responder.expire()
}

func (r *approvalResponder) expire() (driver.DecisionResponse, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.state == approvalResponded {
		return r.resp, true
	}
	if r.state == approvalPending {
		r.state = approvalExpired
		close(r.ready)
	}
	return driver.DecisionResponse{}, false
}

// takeResponse retrieves the settled response without blocking (callback
// form: the handler returned nil, the response must be in the buffer).
func (r *ApprovalRequest) takeResponse() (driver.DecisionResponse, bool) {
	responder, err := r.boundResponder()
	if err != nil {
		return driver.DecisionResponse{}, false
	}
	return responder.takeResponse()
}

func (r *approvalResponder) takeResponse() (driver.DecisionResponse, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.state != approvalResponded {
		return driver.DecisionResponse{}, false
	}
	return r.resp, true
}

func (r *ApprovalRequest) ready() <-chan struct{} {
	if r == nil || r.responder == nil {
		return nil
	}
	return r.responder.ready
}
