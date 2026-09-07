package adaptor

import (
	"context"
	"errors"

	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/mcp"
	"github.com/agent-dance/agent-adaptor/skill"
)

// FailureReason classifies the primary reason an execution failed. Additional
// causes may match errors.Is without changing the primary Reason.
type FailureReason string

const (
	// ReasonApprovalDenied means an approval was denied, including an
	// automatically denied request.
	ReasonApprovalDenied FailureReason = "approval_denied"
	// ReasonApprovalTimeout means an approval deadline elapsed.
	ReasonApprovalTimeout FailureReason = "approval_timeout"
	// ReasonAgentError: the driver classified an agent-level failure
	// (bad protocol, non-zero exit, handler panic, ...).
	ReasonAgentError FailureReason = "agent_error"
	// ReasonCancelled means an execution was cancelled, including an explicit
	// Cancel after Driver.Run was entered.
	ReasonCancelled FailureReason = "cancelled"
	// ReasonPolicyViolation means policy validation failed.
	ReasonPolicyViolation FailureReason = "policy_violation"
	// ReasonInfrastructure means execution, Thread coordination, or cleanup
	// failed without a more specific primary reason. Cause preserves the error.
	ReasonInfrastructure FailureReason = "infrastructure_error"
	// ReasonDeadlineExceeded means the invocation's outer deadline elapsed
	// after Driver.Run was entered. It matches context.DeadlineExceeded.
	ReasonDeadlineExceeded FailureReason = "deadline_exceeded"
)

// Sentinels for errors.Is matching. Each RunError unwraps to the sentinel
// matching its Reason.
var (
	// ErrAgentClosed is returned by an Agent or any Thread derived from it
	// after Agent.Close has begun.
	ErrAgentClosed = errors.New("adaptor: agent closed")
	// ErrApprovalDenied matches runs that failed because a human decision
	// was rejected.
	ErrApprovalDenied = errors.New("adaptor: approval denied")
	// ErrApprovalTimeout matches runs that failed because a human decision
	// timed out.
	ErrApprovalTimeout = errors.New("adaptor: approval timed out")
	// ErrAgentFailed matches driver-classified agent failures.
	ErrAgentFailed = errors.New("adaptor: agent failed")
	// ErrRunCancelled matches cancelled executions, including explicit Cancel.
	ErrRunCancelled = errors.New("adaptor: run cancelled")
	// ErrPolicyViolation matches policy validation failures.
	ErrPolicyViolation = errors.New("adaptor: policy violation")
)

// RunError carries every failed execution's available Result once Driver.Run
// has been entered, including cancellation, transport, Thread coordination,
// and cleanup errors. Failures before Driver.Run retain their original wrapped
// error types and do not create a RunError or a provider Result.
//
// Run and Stream.Result always return nil, error on failure. Use errors.As to
// retrieve RunError.Result, and errors.Is/As to inspect Cause. Reason is the
// authoritative primary classification; Cause may contain secondary errors.
// A cleanup failure may follow an already committed healthy Thread checkpoint;
// it neither rolls back that commit nor permits an unhealthy checkpoint.
// The SDK stops modifying the error and Result before Events closes.
type RunError struct {
	// Reason classifies the failure.
	Reason FailureReason
	// Message is the human-readable failure message from the driver/SDK.
	Message string
	// Details carries driver-specific structured failure metadata.
	Details map[string]any
	// Result contains all available execution output and audit information.
	// It is always non-nil when the SDK returns a *RunError.
	Result *Result
	// Cause preserves original and secondary errors, possibly joined with
	// errors.Join. It may be nil when only a classified failure was observed.
	Cause error
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

// Unwrap joins the sentinel matching Reason with Cause, preserving both the
// primary classification and original errors.Is/As identities. It returns nil
// for a nil receiver or when neither a reason sentinel nor Cause is present.
func (e *RunError) Unwrap() error {
	if e == nil {
		return nil
	}
	var sentinel error
	switch e.Reason {
	case ReasonApprovalDenied:
		sentinel = ErrApprovalDenied
	case ReasonApprovalTimeout:
		sentinel = ErrApprovalTimeout
	case ReasonAgentError:
		sentinel = ErrAgentFailed
	case ReasonCancelled:
		sentinel = ErrRunCancelled
	case ReasonPolicyViolation:
		sentinel = ErrPolicyViolation
	case ReasonDeadlineExceeded:
		sentinel = context.DeadlineExceeded
	}
	return errors.Join(sentinel, e.Cause)
}

// Skill, MCP, and structured-output resolution failures happen before the
// driver launches and surface as plain wrapped errors. They are configuration
// problems, not business outcomes. Their leaf vocabulary
// packages own the canonical identities; the root only re-exports them.

var (
	// ErrSkillNotFound: a bare skill key was requested (WithSkills /
	// SelectSkills) but the SkillProvider did not return it.
	ErrSkillNotFound = skill.ErrSkillNotFound
	// ErrSkillKeyConflict: two skill candidates share a key but differ
	// structurally. Unwrap to *SkillKeyConflictError for the sources.
	ErrSkillKeyConflict = skill.ErrSkillKeyConflict
	// ErrSkillMaterializationFailed: staging a skill's source to disk
	// failed. Unwrap to *SkillMaterializationError for the key.
	ErrSkillMaterializationFailed = skill.ErrSkillMaterializationFailed
	// ErrSkillSourceMissing: a skill declares no usable source.
	ErrSkillSourceMissing = skill.ErrSkillSourceMissing
	// ErrSkillKeyMissing: a concrete skill declaration has an empty key.
	ErrSkillKeyMissing = skill.ErrSkillKeyMissing
	// ErrInvalidMCPConfig: an MCP server spec is malformed (missing key,
	// transport/field mismatch, duplicate key).
	ErrInvalidMCPConfig = mcp.ErrInvalidConfig
	// ErrMCPUnsupported: the driver does not support MCP servers at all.
	ErrMCPUnsupported = mcp.ErrUnsupported
	// ErrMCPTransportUnsupported: the driver supports MCP but not this
	// server's transport.
	ErrMCPTransportUnsupported = mcp.ErrTransportUnsupported
	// ErrInvalidOutputSchema: the structured-output schema is invalid or
	// could not be derived. Unwrap to *InvalidOutputSchemaError.
	ErrInvalidOutputSchema = driver.ErrInvalidOutputSchema
	// ErrStructuredOutputUnsupported: the driver's capability matrix
	// cannot honor structured output through either supported mechanism.
	// Unwrap to *StructuredOutputUnsupportedError for Driver diagnostics.
	ErrStructuredOutputUnsupported = driver.ErrStructuredOutputUnsupported
	// ErrInvalidDriverConfig: Driver.ValidateConfig rejected the Driver's
	// captured construction-time configuration before launch.
	ErrInvalidDriverConfig = driver.ErrInvalidDriverConfig
	// ErrInvalidPolicy: a Policy field contains an out-of-domain value.
	ErrInvalidPolicy = driver.ErrInvalidPolicy
	// ErrPolicyCapabilityUnsupported: a valid, explicitly selected Sandbox,
	// WebSearch, or Browser value is unsupported by the Driver.
	ErrPolicyCapabilityUnsupported = driver.ErrPolicyCapabilityUnsupported
	// ErrHumanDecisionModeUnsupported: an explicitly selected approval mode
	// is not advertised by the Driver's RunPolicyCaps.
	ErrHumanDecisionModeUnsupported = driver.ErrHumanDecisionModeUnsupported
)

// Typed error aliases for errors.As matching.
type (
	// SkillKeyConflictError reports conflicting duplicate skill keys.
	SkillKeyConflictError = skill.SkillKeyConflictError
	// SkillMaterializationError reports a failed skill staging.
	SkillMaterializationError = skill.SkillMaterializationError
	// InvalidOutputSchemaError reports an invalid or underivable schema.
	InvalidOutputSchemaError = driver.InvalidOutputSchemaError
	// StructuredOutputUnsupportedError reports a capability-matrix miss.
	StructuredOutputUnsupportedError = driver.StructuredOutputUnsupportedError
	// InvalidDriverConfigError reports a rejected captured Driver config.
	InvalidDriverConfigError = driver.InvalidDriverConfigError
	// InvalidPolicyError reports one out-of-domain Policy field.
	InvalidPolicyError = driver.InvalidPolicyError
	// PolicyCapabilityUnsupportedError reports the rejected dimension, value,
	// and Driver.
	PolicyCapabilityUnsupportedError = driver.PolicyCapabilityUnsupportedError
	// HumanDecisionModeUnsupportedError reports the rejected kind, mode, and
	// Driver capability matrix.
	HumanDecisionModeUnsupportedError = driver.HumanDecisionModeUnsupportedError
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
