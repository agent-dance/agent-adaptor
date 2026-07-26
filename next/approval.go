package adaptor

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/agent-dance/agent-adaptor/driver"
)

// ============ Approvals (P1.3, design doc §2.6): the request answers itself ============
//
// The legacy surface — requestID bookkeeping, ResolveDecision round-trips,
// kind-mismatch runtime errors, and 3×2 typed handler options — collapses to
// two consumption forms of one type:
//
//	Form A, callback:  adaptor.OnApproval(func(ctx, req *ApprovalRequest) error { ... })
//	Form B, event:     case *adaptor.ApprovalRequest: on the Stream — store it,
//	                   answer later from any goroutine.
//
// The responder lives on the request (Approve / Deny / Answer), so a kind
// mismatch is impossible below the method level, and timeout / retry /
// fallback semantics live in Policy.Approvals (the legacy HumanDecisionPolicy
// semantics, unchanged).

// ApprovalKind labels the semantic category of an approval request. The
// values are the legacy HumanDecisionKind taxonomy.
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
// HumanDecisionPolicy — the semantics are the legacy ones, unchanged:
// zero-valued fields inherit the SDK defaults (Permission/PlanReview ask,
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

// ApprovalsAutoDeny is the ApprovalPolicy preset that denies every approval
// kind without asking — the unattended-worker setting (scenario S4).
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
// synthesized answer (same rule as the legacy autonomous preset).
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
)

// ApprovalRequest is a human-in-the-loop request that carries its own
// responder. It arrives either through the OnApproval callback (form A) or
// as a *ApprovalRequest event on the Stream (form B); in both forms exactly
// one of Approve / Deny / Answer resolves it. Late or duplicate responses
// return ErrApprovalResolved; if nobody responds before Deadline, the
// ApprovalPolicy timeout fallback applies.
type ApprovalRequest struct {
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

	// CreatedAt / Deadline bound the response window; after Deadline the
	// ApprovalPolicy OnTimeout fallback applies.
	CreatedAt time.Time
	Deadline  time.Time

	// Attempt is the zero-based retry attempt (FallbackRetry re-asks).
	Attempt int

	mu       sync.Mutex
	resolved bool
	reply    chan driver.DecisionResponse
}

// newApprovalRequest wraps a normalized DecisionRequest with a responder.
func newApprovalRequest(req driver.DecisionRequest) *ApprovalRequest {
	return &ApprovalRequest{
		ID:         req.RequestID,
		RunID:      req.RunID,
		Kind:       ApprovalKind(req.Kind),
		Title:      req.Prompt,
		Source:     req.Source,
		ToolCallID: req.ToolCallID,
		Choices:    append([]Choice(nil), req.Choices...),
		Details:    req.Payload,
		CreatedAt:  req.CreatedAt,
		Deadline:   req.Deadline,
		Attempt:    req.RetryAttempt,
		reply:      make(chan driver.DecisionResponse, 1),
	}
}

// Approve resolves a Permission or PlanReview request positively. Calling
// it on a Question returns ErrApprovalKindMismatch; calling it after the
// request was resolved returns ErrApprovalResolved.
func (r *ApprovalRequest) Approve(_ context.Context) error {
	if r.Kind == ApprovalQuestion {
		return ErrApprovalKindMismatch
	}
	return r.settle(driver.DecisionResponse{Result: driver.DecisionApproved})
}

// Deny resolves any request kind negatively with a reason. What happens
// next is the ApprovalPolicy OnReject fallback (abort by default).
func (r *ApprovalRequest) Deny(_ context.Context, reason string) error {
	return r.settle(driver.DecisionResponse{Result: driver.DecisionRejected, Text: reason})
}

// Answer resolves a Question request with the chosen option: one of the
// Choices keys, or free text for open questions. Calling it on a
// Permission / PlanReview request returns ErrApprovalKindMismatch.
func (r *ApprovalRequest) Answer(_ context.Context, option string) error {
	if r.Kind != ApprovalQuestion {
		return ErrApprovalKindMismatch
	}
	resp := driver.DecisionResponse{Result: driver.DecisionAnswered, Text: option}
	for _, c := range r.Choices {
		if c.Key == option {
			resp.Choice = option
			break
		}
	}
	return r.settle(resp)
}

// settle records the consumer response exactly once.
func (r *ApprovalRequest) settle(resp driver.DecisionResponse) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.resolved {
		return ErrApprovalResolved
	}
	r.resolved = true
	resp.RequestID = r.ID
	r.reply <- resp // buffered(1): never blocks
	return nil
}

// expire marks the request resolved from the SDK side (timeout, run end,
// abort) so late responses fail with ErrApprovalResolved. When a consumer
// response won the race under the same lock, expire recovers and returns
// it so the answer is honored instead of dropped.
func (r *ApprovalRequest) expire() (driver.DecisionResponse, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.resolved {
		select {
		case resp := <-r.reply:
			return resp, true
		default:
			return driver.DecisionResponse{}, false
		}
	}
	r.resolved = true
	return driver.DecisionResponse{}, false
}

// takeResponse retrieves the settled response without blocking (callback
// form: the handler returned nil, the response must be in the buffer).
func (r *ApprovalRequest) takeResponse() (driver.DecisionResponse, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.resolved {
		return driver.DecisionResponse{}, false
	}
	select {
	case resp := <-r.reply:
		return resp, true
	default:
		return driver.DecisionResponse{}, false
	}
}
