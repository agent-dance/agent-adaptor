package driver

import "time"

// RunPolicy is the only host-facing contract for execution guardrails. Values
// are not CLI flag names: each driver maps them to provider-specific
// controls. Empty fields (…Inherit) leave the dimension unspecified so the
// resolved Agent default or driver fallback applies. HumanDecision contains
// the complete HITL policy. See docs/run-policy.md for the public contract.
type RunPolicy struct {
	Isolation     IsolationLevel
	WebSearch     FeatureLevel
	Browser       FeatureLevel
	HumanDecision HumanDecisionPolicy
}

// IsolationLevel controls filesystem / process boundary strength.
type IsolationLevel string

const (
	// IsolationInherit leaves isolation to the Agent default or driver fallback.
	IsolationInherit IsolationLevel = ""
	// IsolationReadOnly requests a read-only workspace.
	IsolationReadOnly IsolationLevel = "read_only"
	// IsolationWorkspaceWrite allows writes inside the resolved workspace.
	IsolationWorkspaceWrite IsolationLevel = "workspace_write"
	// IsolationUnrestricted maps to each agent's "full access" / danger
	// sandbox (or the closest available behavior).
	IsolationUnrestricted IsolationLevel = "unrestricted"
)

// FeatureLevel is used for optional capabilities (search, browser tooling).
type FeatureLevel string

const (
	// FeatureInherit leaves the capability to the Agent default or driver fallback.
	FeatureInherit FeatureLevel = ""
	// FeatureAllow explicitly enables the optional capability when supported.
	FeatureAllow FeatureLevel = "allow"
	// FeatureDeny explicitly disables the optional capability.
	FeatureDeny FeatureLevel = "deny"
)

// Defaults declared in docs/run-policy.md §1.3. Exposed as package
// constants so runner and driver tests can reference them without drift.
const (
	// DefaultHumanDecisionTimeout is the timeout used for Ask decisions when
	// the host does not set HumanDecisionPolicy.Timeout.
	DefaultHumanDecisionTimeout = 30 * time.Second
	// DefaultHumanDecisionMaxRetries is the retry cap used when the host
	// requests FailureRetry without setting MaxRetries.
	DefaultHumanDecisionMaxRetries = 3
)

// RunPolicyCapabilities lists which RunPolicy dimensions a Driver can honor.
// False means unsupported: the root runner rejects an explicitly selected
// non-zero value before Driver.Run instead of silently ignoring host intent.
// Zero/inherit values remain portable and are not rejected. Permission /
// PlanReview / Question declare per-mode support via HumanDecisionSupport /
// QuestionSupport under the same explicit-value rule.
type RunPolicyCapabilities struct {
	Isolation bool
	WebSearch bool
	Browser   bool

	Permission HumanDecisionSupport
	PlanReview HumanDecisionSupport
	Question   QuestionSupport
}
