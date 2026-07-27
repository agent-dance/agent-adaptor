package driver

import (
	"context"
	"time"
)

// HumanDecisionKind labels the semantic category of a human-in-the-loop (HITL)
// decision event. See docs/run-policy.md for the public taxonomy.
type HumanDecisionKind string

const (
	// HumanDecisionPermission covers tool, command, file, or permission gates.
	HumanDecisionPermission HumanDecisionKind = "permission"
	// HumanDecisionPlanReview covers plan-mode approval before execution.
	HumanDecisionPlanReview HumanDecisionKind = "plan_review"
	// HumanDecisionQuestion covers structured clarification questions.
	HumanDecisionQuestion HumanDecisionKind = "question"
)

// HumanDecisionMode expresses the host's intent for a Permission / PlanReview
// decision (binary-approval classes).
//
// Semantic layering:
//   - Ask         → route the request to a HITL channel (handler or channel).
//   - AutoApprove → defer to the agent / CLI bypass / auto path.
//   - AutoReject  → synthesize a rejection locally and emit a Failure.
//   - Unset ("")  → fall back to the SDK default (see docs/run-policy.md §1.3).
//
// Question uses the narrower QuestionMode because the values are not
// interchangeable: a Question result is structured (Answered), so "auto
// approve" has no legitimate synthesized value.
type HumanDecisionMode string

const (
	// HumanDecisionUnset inherits the SDK/binding default.
	HumanDecisionUnset HumanDecisionMode = ""
	// HumanDecisionAsk routes the decision to a host handler or async channel.
	HumanDecisionAsk HumanDecisionMode = "ask"
	// HumanDecisionAutoApprove lets the driver take its provider-specific
	// automatic/bypass path for the decision class.
	HumanDecisionAutoApprove HumanDecisionMode = "auto_approve"
	// HumanDecisionAutoReject synthesizes a rejection locally.
	HumanDecisionAutoReject HumanDecisionMode = "auto_reject"
)

// QuestionMode expresses the host's intent for the Question class. It is a
// strict subset of HumanDecisionMode: AutoApprove is intentionally absent.
type QuestionMode string

const (
	// QuestionUnset inherits the SDK/binding default.
	QuestionUnset QuestionMode = ""
	// QuestionAsk routes the question to a host handler or async channel.
	QuestionAsk QuestionMode = "ask"
	// QuestionAutoReject rejects questions without asking the host.
	QuestionAutoReject QuestionMode = "auto_reject"
)

// FailureAction is the value type for HumanDecisionPolicy.OnTimeout and
// HumanDecisionPolicy.OnReject. It describes what the SDK should do next
// when a decision surface produces a failure signal.
type FailureAction string

const (
	// FailureActionUnset inherits the SDK default action.
	FailureActionUnset FailureAction = ""
	// FailureAbort terminates the run and emits a business failure with the
	// matching code (FailureReject or FailureTimeout). This is the default.
	FailureAbort FailureAction = "abort"
	// FailureContinue lets the driver forward the reject / timeout to the
	// agent as a tool_result so the run can progress.
	FailureContinue FailureAction = "continue"
	// FailureRetry re-triggers the same decision (bounded by MaxRetries).
	// When the driver cannot truly re-ask, the runner warns and degrades to
	// FailureAbort.
	FailureRetry FailureAction = "retry"
)

// FailureCode is the enumeration carried on RunFailure.Code.
type FailureCode string

const (
	// FailureReject indicates that a HITL decision was rejected (including
	// AutoReject synthesis) and OnReject resolved to FailureAbort.
	FailureReject FailureCode = "decision_rejected"
	// FailureTimeout indicates that a HITL decision Deadline elapsed and
	// OnTimeout resolved to FailureAbort.
	FailureTimeout FailureCode = "decision_timeout"
	// FailureAgentError reports a driver-level failure (bad protocol,
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
// docs/run-policy.md §1.3.
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
	// Negative values are rejected during policy merging.
	MaxRetries int
}

// EffectiveHumanDecisionPolicy materializes SDK defaults for unset fields in
// a HumanDecisionPolicy. Drivers use it when they need to know the actual
// Timeout / OnTimeout / OnReject / MaxRetries values that the runner applies
// so they can surface consistent Deadline timestamps and failure messages.
func EffectiveHumanDecisionPolicy(p HumanDecisionPolicy) HumanDecisionPolicy {
	out := p
	if out.Permission == HumanDecisionUnset {
		out.Permission = HumanDecisionAsk
	}
	if out.PlanReview == HumanDecisionUnset {
		out.PlanReview = HumanDecisionAsk
	}
	if out.Question == QuestionUnset {
		out.Question = QuestionAutoReject
	}
	if out.Timeout == 0 {
		out.Timeout = DefaultHumanDecisionTimeout
	}
	if out.OnTimeout == FailureActionUnset {
		out.OnTimeout = FailureAbort
	}
	if out.OnReject == FailureActionUnset {
		out.OnReject = FailureAbort
	}
	if out.MaxRetries == 0 {
		out.MaxRetries = DefaultHumanDecisionMaxRetries
	}
	return out
}

// HumanDecisionSupport describes Permission / PlanReview support on a given
// driver. Fields set to false reject the matching host-facing setting before
// driver launch.
type HumanDecisionSupport struct {
	Ask         bool
	AutoApprove bool
	AutoReject  bool
	Retry       bool
}

// QuestionSupport describes Question support on a given driver. AutoApprove
// is intentionally absent—QuestionMode has no such value.
type QuestionSupport struct {
	Ask        bool
	AutoReject bool
	Retry      bool
}

// DecisionChoice is a single renderable option returned by a driver.
type DecisionChoice struct {
	Key         string
	Label       string
	Description string
}

// DecisionResult is the cross-class decision outcome used in
// DecisionResponse, channel mode, and HumanDecisionFailure.Decision.
type DecisionResult string

const (
	// DecisionApproved is the normalized positive result for binary decisions.
	DecisionApproved DecisionResult = "approved"
	// DecisionRejected is the normalized negative result for binary decisions.
	DecisionRejected DecisionResult = "rejected"
	// DecisionAnswered carries a structured answer for Question decisions.
	DecisionAnswered DecisionResult = "answered"
	// DecisionTimedOut records that no host answer arrived before Deadline.
	DecisionTimedOut DecisionResult = "timed_out"
	// DecisionAborted records cancellation or another driver-visible abort.
	DecisionAborted DecisionResult = "aborted"
)

// DecisionRequest is the cross-class request envelope drivers hand to the
// SDK. The SDK normalizes RequestID / CreatedAt / Deadline / RetryAttempt
// before routing and before emitting StreamHITLRequested.
type DecisionRequest struct {
	RequestID  string
	RunID      string
	ThreadID   string
	Kind       HumanDecisionKind
	Source     string
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
// per-Kind Response types in the root package (PermissionResponse /
// PlanReviewResponse / QuestionResponse) and are converted to
// DecisionResponse internally before the driver sees them.
type DecisionResponse struct {
	RequestID string
	Result    DecisionResult
	Choice    string
	Answer    map[string]any
	Text      string
}

// HumanDecisionFailure is the structured attribution attached to
// RunFailure.HumanDecision when a HITL decision causes a run to terminate.
type HumanDecisionFailure struct {
	Kind     HumanDecisionKind
	Source   string
	Decision DecisionResult
	Request  *DecisionRequest
	Attempts int
}

// DecisionCapableSink is an optional extension of EventSink. Drivers call
// RequestDecision to block on a HITL decision. The SDK's built-in sink
// implements this interface; custom or observer-only sinks do not need to.
type DecisionCapableSink interface {
	EventSink
	// RequestDecision blocks until the host resolves the decision, the
	// policy Deadline elapses, or ctx is cancelled. The returned error is
	// non-nil when the decision was aborted (cancellation or driver-visible
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
