package adaptor

import (
	"errors"

	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/engine"
)

// Decision D1: business failures are typed errors. A run that completed but
// failed at the business level returns a *RunError carrying the full Result;
// infrastructure failures (context cancellation, process crash, protocol
// breakage) travel the same err path as plain wrapped errors. Hosts have one
// verdict point:
//
//	res, err := agent.Run(ctx, prompt)
//	if err != nil {
//	    var runErr *adaptor.RunError
//	    if errors.As(err, &runErr) {
//	        // completed-but-failed: runErr.Reason, runErr.Result
//	    }
//	    return err // cancellation / crash / breakage: same path
//	}

// FailureReason classifies a business-level run failure.
type FailureReason string

const (
	// ReasonApprovalDenied: a human decision was rejected (including
	// auto-deny synthesis). Legacy code: decision_rejected.
	ReasonApprovalDenied FailureReason = "approval_denied"
	// ReasonApprovalTimeout: a human decision deadline elapsed.
	// Legacy code: decision_timeout.
	ReasonApprovalTimeout FailureReason = "approval_timeout"
	// ReasonAgentError: the driver classified an agent-level failure
	// (bad protocol, non-zero exit, handler panic, ...).
	ReasonAgentError FailureReason = "agent_error"
	// ReasonCancelled: the run was cancelled after producing a classified
	// business failure (as opposed to a bare context cancellation, which
	// surfaces as a plain error wrapping ctx.Err()).
	ReasonCancelled FailureReason = "cancelled"
	// ReasonPolicyViolation: policy validation failed. Legacy code:
	// policy_error.
	ReasonPolicyViolation FailureReason = "policy_violation"
)

// Sentinels for errors.Is matching. Each RunError unwraps to the sentinel
// matching its Reason.
var (
	// ErrApprovalDenied matches runs that failed because a human decision
	// was rejected.
	ErrApprovalDenied = errors.New("adaptor: approval denied")
	// ErrApprovalTimeout matches runs that failed because a human decision
	// timed out.
	ErrApprovalTimeout = errors.New("adaptor: approval timed out")
	// ErrAgentFailed matches driver-classified agent failures.
	ErrAgentFailed = errors.New("adaptor: agent failed")
	// ErrRunCancelled matches driver-classified cancellation failures.
	ErrRunCancelled = errors.New("adaptor: run cancelled")
	// ErrPolicyViolation matches policy validation failures.
	ErrPolicyViolation = errors.New("adaptor: policy violation")
)

// RunError is the typed error for a run that completed but failed at the
// business level (approval denied / timed out, policy violation, agent
// error). It follows the *exec.ExitError convention: the error carries the
// full execution Result, so partial output, usage, and the transcript stay
// accessible on the failure path.
type RunError struct {
	// Reason classifies the failure.
	Reason FailureReason
	// Message is the human-readable failure message from the driver/SDK.
	Message string
	// Details carries driver-specific structured failure metadata.
	Details map[string]any
	// Result is the full result of the completed-but-failed run. It is
	// always non-nil when the SDK returns a *RunError.
	Result *Result
}

// Error implements the error interface.
func (e *RunError) Error() string {
	msg := "adaptor: run failed"
	if e == nil {
		return msg
	}
	if e.Reason != "" {
		msg += ": " + string(e.Reason)
	}
	if e.Message != "" {
		msg += ": " + e.Message
	}
	return msg
}

// Unwrap returns the sentinel matching Reason so that
// errors.Is(err, adaptor.ErrApprovalDenied) and friends hold.
func (e *RunError) Unwrap() error {
	if e == nil {
		return nil
	}
	switch e.Reason {
	case ReasonApprovalDenied:
		return ErrApprovalDenied
	case ReasonApprovalTimeout:
		return ErrApprovalTimeout
	case ReasonAgentError:
		return ErrAgentFailed
	case ReasonCancelled:
		return ErrRunCancelled
	case ReasonPolicyViolation:
		return ErrPolicyViolation
	default:
		return nil
	}
}

// ============ Pre-launch failure vocabulary (P3) ============
//
// Skill, MCP, and structured-output resolution failures happen before the
// driver launches and surface as plain wrapped errors (decision D1: they
// are configuration problems, not business outcomes). The sentinels and
// typed errors are the engine's single truth, re-exported here so hosts
// match them without importing internal packages.

var (
	// ErrSkillNotFound: a bare skill key was requested (WithSkills /
	// SelectSkills) but the SkillProvider did not return it.
	ErrSkillNotFound = engine.ErrSkillNotFound
	// ErrSkillKeyConflict: two skill candidates share a key but differ
	// structurally. Unwrap to *SkillKeyConflictError for the sources.
	ErrSkillKeyConflict = engine.ErrSkillKeyConflict
	// ErrSkillMaterializationFailed: staging a skill's source to disk
	// failed. Unwrap to *SkillMaterializationError for the key.
	ErrSkillMaterializationFailed = engine.ErrSkillMaterializationFailed
	// ErrSkillSourceMissing: a skill declares no usable source.
	ErrSkillSourceMissing = engine.ErrSkillSourceMissing
	// ErrInvalidMCPConfig: an MCP server spec is malformed (missing key,
	// transport/field mismatch, duplicate key).
	ErrInvalidMCPConfig = engine.ErrInvalidMCPConfig
	// ErrMCPUnsupported: the driver does not support MCP servers at all.
	ErrMCPUnsupported = engine.ErrMCPUnsupported
	// ErrMCPTransportUnsupported: the driver supports MCP but not this
	// server's transport.
	ErrMCPTransportUnsupported = engine.ErrMCPTransportUnsupported
	// ErrInvalidOutputSchema: the structured-output schema is invalid or
	// could not be derived. Unwrap to *InvalidOutputSchemaError.
	ErrInvalidOutputSchema = engine.ErrInvalidOutputSchema
	// ErrStructuredOutputUnsupported: the driver's capability matrix
	// cannot honor the requested structured-output mode. Unwrap to
	// *StructuredOutputUnsupportedError for the adapter and mode.
	ErrStructuredOutputUnsupported = engine.ErrStructuredOutputUnsupported
)

// Typed error aliases for errors.As matching.
type (
	// SkillKeyConflictError reports conflicting duplicate skill keys.
	SkillKeyConflictError = engine.SkillKeyConflictError
	// SkillMaterializationError reports a failed skill staging.
	SkillMaterializationError = engine.SkillMaterializationError
	// InvalidOutputSchemaError reports an invalid or underivable schema.
	InvalidOutputSchemaError = engine.InvalidOutputSchemaError
	// StructuredOutputUnsupportedError reports a capability-matrix miss.
	StructuredOutputUnsupportedError = engine.StructuredOutputUnsupportedError
)

// failureReason maps the driver SPI failure code onto the consumer
// vocabulary. Unknown codes pass through verbatim so drivers can extend the
// space without silently losing information.
func failureReason(code driver.FailureCode) FailureReason {
	switch code {
	case driver.FailureReject:
		return ReasonApprovalDenied
	case driver.FailureTimeout:
		return ReasonApprovalTimeout
	case driver.FailureAgentError, "":
		return ReasonAgentError
	case driver.FailureCancelled:
		return ReasonCancelled
	case driver.FailurePolicyError:
		return ReasonPolicyViolation
	default:
		return FailureReason(code)
	}
}
