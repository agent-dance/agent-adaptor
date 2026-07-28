package adaptor

import (
	"strconv"

	"github.com/agent-dance/agent-adaptor/driver"
)

// SandboxLevel controls filesystem and process boundary strength. It aliases
// the driver SPI type so policies flow to Drivers without conversion.
type SandboxLevel = driver.IsolationLevel

const (
	// SandboxInherit leaves the sandbox to the agent default or the
	// driver's own fallback.
	SandboxInherit SandboxLevel = driver.IsolationInherit
	// ReadOnly requests a read-only workspace.
	ReadOnly SandboxLevel = driver.IsolationReadOnly
	// WorkspaceWrite allows writes inside the resolved workspace.
	WorkspaceWrite SandboxLevel = driver.IsolationWorkspaceWrite
	// Unrestricted maps to each agent's "full access" / danger sandbox
	// (or the closest available behavior).
	Unrestricted SandboxLevel = driver.IsolationUnrestricted
)

// FeatureLevel gates optional capabilities (web search, browser tooling).
type FeatureLevel = driver.FeatureLevel

const (
	// FeatureInherit leaves the capability to the agent default or the
	// driver's own fallback.
	FeatureInherit FeatureLevel = driver.FeatureInherit
	// FeatureAllow explicitly enables the capability when supported.
	FeatureAllow FeatureLevel = driver.FeatureAllow
	// FeatureDeny explicitly disables the capability.
	FeatureDeny FeatureLevel = driver.FeatureDeny
)

// Policy is the execution guardrail contract set via WithPolicy. Values are
// not CLI flag names: each driver maps them to provider-specific controls.
//
// As an option value, Policy replaces as a whole ("nearer scope wins;
// everything but skills replaces"): a call-site WithPolicy substitutes the
// agent-default Policy entirely. Zero fields mean "inherit" at the driver
// boundary, so an all-zero Policy defers every dimension to the driver.
type Policy struct {
	// Sandbox is the filesystem / process boundary strength.
	Sandbox SandboxLevel
	// WebSearch gates the provider's web-search capability.
	WebSearch FeatureLevel
	// Browser gates the provider's browser tooling.
	Browser FeatureLevel

	// Approvals routes and bounds human-in-the-loop requests: per-kind
	// modes (ask / auto-approve / auto-deny), the response Timeout, the
	// OnTimeout / OnReject fallbacks, and MaxRetries. Zero-valued fields
	// inherit the package defaults (Permission/PlanReview ask, Question
	// auto-deny, 30s timeout, abort on timeout/reject, 3 retries). See ApprovalPolicy
	// and the ApprovalsAutoDeny preset.
	Approvals ApprovalPolicy
}

// Common sandbox presets. Configure human-in-the-loop behavior independently
// through Policy.Approvals or an OnApproval handler.
var (
	// PolicyReadOnly is a read-only workspace policy (reviewers, planners).
	PolicyReadOnly = Policy{Sandbox: ReadOnly}
	// PolicyWorkspaceWrite allows writes inside the resolved workspace.
	PolicyWorkspaceWrite = Policy{Sandbox: WorkspaceWrite}
	// PolicyUnrestricted requests the driver's full-access sandbox.
	PolicyUnrestricted = Policy{Sandbox: Unrestricted}
)

// driverPolicy maps the consumer Policy onto the driver SPI contract.
func (p Policy) driverPolicy() driver.RunPolicy {
	return driver.RunPolicy{
		Isolation:     p.Sandbox,
		WebSearch:     p.WebSearch,
		Browser:       p.Browser,
		HumanDecision: p.Approvals,
	}
}

// validatePolicy rejects malformed enum values and capability misses before
// any host resource is acquired or Driver.Run is called. Capability checks
// intentionally inspect only non-zero modes: Unset means "use the portable
// SDK default" and must not make an otherwise valid Driver unusable merely
// because its descriptor does not advertise Ask.
func validatePolicy(desc driver.Descriptor, p *Policy) error {
	if p == nil {
		return nil
	}
	invalid := func(field, value string) error {
		return &driver.InvalidPolicyError{Driver: desc.Type, Field: field, Value: value}
	}

	switch p.Sandbox {
	case driver.IsolationInherit, driver.IsolationReadOnly, driver.IsolationWorkspaceWrite, driver.IsolationUnrestricted:
	default:
		return invalid("Policy.Sandbox", string(p.Sandbox))
	}
	validateFeature := func(field string, value driver.FeatureLevel) error {
		switch value {
		case driver.FeatureInherit, driver.FeatureAllow, driver.FeatureDeny:
			return nil
		default:
			return invalid(field, string(value))
		}
	}
	if err := validateFeature("Policy.WebSearch", p.WebSearch); err != nil {
		return err
	}
	if err := validateFeature("Policy.Browser", p.Browser); err != nil {
		return err
	}

	approvals := p.Approvals
	validateBinaryMode := func(field string, mode driver.HumanDecisionMode) error {
		switch mode {
		case driver.HumanDecisionUnset, driver.HumanDecisionAsk, driver.HumanDecisionAutoApprove, driver.HumanDecisionAutoReject:
			return nil
		default:
			return invalid(field, string(mode))
		}
	}
	if err := validateBinaryMode("Policy.Approvals.Permission", approvals.Permission); err != nil {
		return err
	}
	if err := validateBinaryMode("Policy.Approvals.PlanReview", approvals.PlanReview); err != nil {
		return err
	}
	switch approvals.Question {
	case driver.QuestionUnset, driver.QuestionAsk, driver.QuestionAutoReject:
	default:
		return invalid("Policy.Approvals.Question", string(approvals.Question))
	}
	validateAction := func(field string, action driver.FailureAction) error {
		switch action {
		case driver.FailureActionUnset, driver.FailureAbort, driver.FailureContinue, driver.FailureRetry:
			return nil
		default:
			return invalid(field, string(action))
		}
	}
	if err := validateAction("Policy.Approvals.OnTimeout", approvals.OnTimeout); err != nil {
		return err
	}
	if err := validateAction("Policy.Approvals.OnReject", approvals.OnReject); err != nil {
		return err
	}
	if approvals.MaxRetries < 0 {
		return invalid("Policy.Approvals.MaxRetries", strconv.Itoa(approvals.MaxRetries))
	}

	unsupportedDimension := func(dimension, value string) error {
		return &driver.PolicyCapabilityUnsupportedError{
			Driver:    desc.Type,
			Dimension: dimension,
			Value:     value,
		}
	}
	if p.Sandbox != driver.IsolationInherit && !desc.RunPolicyCaps.Isolation {
		return unsupportedDimension("sandbox", string(p.Sandbox))
	}
	if p.WebSearch != driver.FeatureInherit && !desc.RunPolicyCaps.WebSearch {
		return unsupportedDimension("web_search", string(p.WebSearch))
	}
	if p.Browser != driver.FeatureInherit && !desc.RunPolicyCaps.Browser {
		return unsupportedDimension("browser", string(p.Browser))
	}

	unsupported := func(kind driver.HumanDecisionKind, mode string) error {
		return &driver.HumanDecisionModeUnsupportedError{
			Driver: desc.Type,
			Kind:   kind,
			Mode:   mode,
		}
	}
	validateBinarySupport := func(kind driver.HumanDecisionKind, mode driver.HumanDecisionMode, support driver.HumanDecisionSupport) error {
		switch mode {
		case driver.HumanDecisionUnset:
			return nil
		case driver.HumanDecisionAsk:
			if support.Ask {
				return nil
			}
		case driver.HumanDecisionAutoApprove:
			if support.AutoApprove {
				return nil
			}
		case driver.HumanDecisionAutoReject:
			if support.AutoReject {
				return nil
			}
		}
		return unsupported(kind, string(mode))
	}
	if err := validateBinarySupport(driver.HumanDecisionPermission, approvals.Permission, desc.RunPolicyCaps.Permission); err != nil {
		return err
	}
	if err := validateBinarySupport(driver.HumanDecisionPlanReview, approvals.PlanReview, desc.RunPolicyCaps.PlanReview); err != nil {
		return err
	}
	switch approvals.Question {
	case driver.QuestionUnset:
		return nil
	case driver.QuestionAsk:
		if desc.RunPolicyCaps.Question.Ask {
			return nil
		}
	case driver.QuestionAutoReject:
		if desc.RunPolicyCaps.Question.AutoReject {
			return nil
		}
	}
	return unsupported(driver.HumanDecisionQuestion, string(approvals.Question))
}
