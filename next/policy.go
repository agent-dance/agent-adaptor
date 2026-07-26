package adaptor

import "github.com/agent-dance/agent-adaptor/driver"

// SandboxLevel controls filesystem / process boundary strength. It is the
// v1 name for the legacy IsolationLevel and aliases the driver SPI type so
// policies flow to drivers without conversion.
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

	// Approvals (HITL timeout / retry / fallback policy — the legacy
	// HumanDecisionPolicy semantics) joins in P1.3 together with
	// ApprovalRequest and the ApprovalsAutoDeny / ApproveAll / DenyAll
	// presets.
}

// Presets mapped from the legacy run_policy.go vocabulary. The legacy
// HITL-bearing presets (PolicyHostReview / PolicyReadOnlyReview /
// PolicyAutonomous) are not carried over by name: their approval halves
// return in P1.3 as Policy.Approvals + ApproveAll()/DenyAll() handlers.
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
		Isolation: p.Sandbox,
		WebSearch: p.WebSearch,
		Browser:   p.Browser,
		// HumanDecision: wired in P1.3 via Policy.Approvals.
	}
}
