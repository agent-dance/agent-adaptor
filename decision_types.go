package agentadaptor

import (
	"context"
	"time"
)

// HumanDecisionKind labels the semantic category of a human-in-the-loop (HITL)
// decision event. See docs/workstream-hitl-v2.md §3.1 for the taxonomy.
type HumanDecisionKind string

const (
	HumanDecisionPermission HumanDecisionKind = "permission"
	HumanDecisionPlanReview HumanDecisionKind = "plan_review"
	HumanDecisionQuestion   HumanDecisionKind = "question"
)

// HumanDecisionMode expresses the host's intent for a Permission / PlanReview
// decision (binary-approval classes).
//
// Semantic layering:
//   - Ask         → route the request to a HITL channel (handler or channel).
//   - AutoApprove → defer to the agent / CLI bypass / auto path.
//   - AutoReject  → synthesize a rejection locally and emit a Failure.
//   - Unset ("")  → fall back to the SDK default (see docs/workstream-hitl-v2.md §3.7).
//
// Question uses the narrower QuestionMode because the values are not
// interchangeable: a Question result is structured (Answered), so "auto
// approve" has no legitimate synthesized value.
type HumanDecisionMode string

const (
	HumanDecisionUnset       HumanDecisionMode = ""
	HumanDecisionAsk         HumanDecisionMode = "ask"
	HumanDecisionAutoApprove HumanDecisionMode = "auto_approve"
	HumanDecisionAutoReject  HumanDecisionMode = "auto_reject"
)

// QuestionMode expresses the host's intent for the Question class. It is a
// strict subset of HumanDecisionMode: AutoApprove is intentionally absent.
type QuestionMode string

const (
	QuestionUnset      QuestionMode = ""
	QuestionAsk        QuestionMode = "ask"
	QuestionAutoReject QuestionMode = "auto_reject"
)

// FailureAction is the value type for HumanDecisionPolicy.OnTimeout and
// HumanDecisionPolicy.OnReject. It describes what the SDK should do next
// when a decision surface produces a failure signal.
type FailureAction string

const (
	FailureActionUnset FailureAction = ""
	// FailureAbort terminates the run and emits RunResult.Failure with the
	// matching code (FailureReject or FailureTimeout). This is the default.
	FailureAbort FailureAction = "abort"
	// FailureContinue lets the adapter forward the reject / timeout to the
	// agent as a tool_result so the run can progress.
	FailureContinue FailureAction = "continue"
	// FailureRetry re-triggers the same decision (bounded by MaxRetries).
	// When the adapter cannot truly re-ask, the runner warns and degrades to
	// FailureAbort.
	FailureRetry FailureAction = "retry"
)

// FailureCode is the enumeration carried on RunResult.Failure.Code.
type FailureCode string

const (
	// FailureReject indicates that a HITL decision was rejected (including
	// AutoReject synthesis) and OnReject resolved to FailureAbort.
	FailureReject FailureCode = "decision_rejected"
	// FailureTimeout indicates that a HITL decision Deadline elapsed and
	// OnTimeout resolved to FailureAbort.
	FailureTimeout FailureCode = "decision_timeout"
	// FailureAgentError reports an adapter-level failure (bad protocol,
	// non-zero exit, handler panic, …).
	FailureAgentError FailureCode = "agent_error"
	// FailureCancelled indicates that the run was cancelled (ctx.Cancel,
	// handler returned error, etc.).
	FailureCancelled FailureCode = "cancelled"
	// FailurePolicyError reports a policy validation error at start time.
	FailurePolicyError FailureCode = "policy_error"
)

// HumanDecisionPolicy is the RunPolicy-facing sub-struct that carries all
// HITL knobs. Zero-valued fields inherit the SDK defaults declared in
// docs/workstream-hitl-v2.md §3.7.
type HumanDecisionPolicy struct {
	Permission HumanDecisionMode
	PlanReview HumanDecisionMode
	Question   QuestionMode

	// Timeout is the maximum time the SDK waits for a host to resolve a
	// decision when the field value is Ask. 0 means "SDK default" (30s);
	// negative values mean "never time out".
	Timeout time.Duration

	// OnTimeout selects the SDK action when a decision times out.
	OnTimeout FailureAction

	// OnReject selects the SDK action when a decision resolves to a rejection
	// (handler return value or AutoReject synthesis).
	OnReject FailureAction

	// MaxRetries caps the FailureRetry action. 0 means "SDK default" (3).
	// Negative values are rejected by mergeRunPolicy.
	MaxRetries int
}

// HumanDecisionSupport describes Permission / PlanReview support on a given
// adapter. Fields set to false reject the matching host-facing setting at
// Start() time.
type HumanDecisionSupport struct {
	Ask         bool
	AutoApprove bool
	AutoReject  bool
	Retry       bool
}

// QuestionSupport describes Question support on a given adapter. AutoApprove
// is intentionally absent—QuestionMode has no such value.
type QuestionSupport struct {
	Ask        bool
	AutoReject bool
	Retry      bool
}

// DecisionChoice is a single renderable option returned by an adapter.
type DecisionChoice struct {
	Key         string
	Label       string
	Description string
}

// DecisionResult is the cross-class decision outcome used in
// DecisionResponse, channel mode, and HumanDecisionFailure.Decision.
type DecisionResult string

const (
	DecisionApproved DecisionResult = "approved"
	DecisionRejected DecisionResult = "rejected"
	DecisionAnswered DecisionResult = "answered"
	DecisionTimedOut DecisionResult = "timed_out"
	DecisionAborted  DecisionResult = "aborted"
)

// DecisionRequest is the cross-class request envelope adapters hand to the
// SDK. The SDK normalizes RequestID / CreatedAt / Deadline / RetryAttempt
// before routing and before emitting StreamHITLRequested.
type DecisionRequest struct {
	RequestID string
	RunID     string
	ThreadID  string
	Kind      HumanDecisionKind
	Source    string
	ToolCallID string

	Prompt  string
	Payload map[string]any
	Choices []DecisionChoice

	DefaultDecision DecisionResult
	CreatedAt       time.Time
	Deadline        time.Time
	RetryAttempt    int
}

// DecisionResponse is the channel-mode response. Typed handlers use the
// per-Kind Response types (PermissionResponse / PlanReviewResponse /
// QuestionResponse) and are converted to DecisionResponse internally before
// the adapter sees them.
type DecisionResponse struct {
	RequestID string
	Result    DecisionResult
	Choice    string
	Answer    map[string]any
	Text      string
}

// decisionRequestBase is the common field set embedded in the per-Kind
// typed request structs.
type decisionRequestBase struct {
	RequestID    string
	RunID        string
	ThreadID     string
	Source       string
	ToolCallID   string
	CreatedAt    time.Time
	Deadline     time.Time
	RetryAttempt int
}

// PermissionRequest is the Permission class typed request (binary-approval
// tool / command gate).
type PermissionRequest struct {
	decisionRequestBase
	Tool   string
	Prompt string
	Args   map[string]any
}

// PlanReviewRequest is the PlanReview class typed request (plan-mode
// approval).
type PlanReviewRequest struct {
	decisionRequestBase
	Prompt string
	Plan   string
	Extra  map[string]any
}

// QuestionRequest is the Question class typed request (structured
// clarification).
type QuestionRequest struct {
	decisionRequestBase
	Prompt  string
	Schema  map[string]any
	Choices []DecisionChoice
}

// ApprovalResult narrows Permission / PlanReview handler responses to the
// legal binary-approval values.
type ApprovalResult string

const (
	ApprovalApproved ApprovalResult = "approved"
	ApprovalRejected ApprovalResult = "rejected"
)

// QuestionResult narrows Question handler responses to the legal
// structured-question values.
type QuestionResult string

const (
	QuestionAnswered QuestionResult = "answered"
	QuestionRejected QuestionResult = "rejected"
)

// PermissionResponse is the typed Response returned by PermissionHandler.
type PermissionResponse struct {
	RequestID string
	Result    ApprovalResult
	Text      string
}

// PlanReviewResponse is the typed Response returned by PlanReviewHandler.
type PlanReviewResponse struct {
	RequestID string
	Result    ApprovalResult
	Text      string
}

// QuestionResponse is the typed Response returned by QuestionHandler.
type QuestionResponse struct {
	RequestID string
	Result    QuestionResult
	Choice    string
	Answer    map[string]any
	Text      string
}

// PermissionHandler processes Permission-class decisions synchronously.
type PermissionHandler func(ctx context.Context, req PermissionRequest) (PermissionResponse, error)

// PlanReviewHandler processes PlanReview-class decisions synchronously.
type PlanReviewHandler func(ctx context.Context, req PlanReviewRequest) (PlanReviewResponse, error)

// QuestionHandler processes Question-class decisions synchronously.
type QuestionHandler func(ctx context.Context, req QuestionRequest) (QuestionResponse, error)

// HumanDecisionFailure is the structured attribution attached to
// RunResult.Failure.HumanDecision when a HITL decision causes a run to
// terminate.
type HumanDecisionFailure struct {
	Kind     HumanDecisionKind
	Source   string
	Decision DecisionResult
	Request  *DecisionRequest
	Attempts int
}

// DecisionCapableSink is an optional extension of EventSink. Adapters call
// RequestDecision to block on a HITL decision. The built-in dualSink
// implements this interface; custom or observer-only sinks do not need to.
type DecisionCapableSink interface {
	EventSink
	// RequestDecision blocks until the host resolves the decision, the
	// policy Deadline elapses, or ctx is cancelled. The returned error is
	// non-nil when the decision was aborted (cancellation or adapter-visible
	// abort); DecisionResponse carries the outcome otherwise.
	RequestDecision(ctx context.Context, req DecisionRequest) (DecisionResponse, error)
}

// HITLRequestedPayload is the structured body of a StreamHITLRequested
// StreamPayload. It is attached at StreamPayload.HITLRequested.
type HITLRequestedPayload struct {
	RequestID    string
	Kind         HumanDecisionKind
	Source       string
	ToolCallID   string
	Prompt       string
	Payload      map[string]any
	Choices      []DecisionChoice
	CreatedAt    time.Time
	Deadline     time.Time
	RetryAttempt int
}

// HITLResolvedPayload is the structured body of a StreamHITLResolved
// StreamPayload.
type HITLResolvedPayload struct {
	RequestID    string
	Kind         HumanDecisionKind
	Source       string
	RetryAttempt int
	Result       DecisionResult
	Choice       string
	Answer       map[string]any
	ResolvedAt   time.Time
	Latency      time.Duration
}
