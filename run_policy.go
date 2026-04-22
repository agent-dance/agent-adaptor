package agentadaptor

// RunPolicy is the only host-facing contract for execution guardrails. Values
// are not CLI flag names: each adapter maps them to provider-specific controls.
// Use empty fields (…Inherit) to mean "use binding default for this run".
type RunPolicy struct {
	Approvals ApprovalLevel
	Isolation IsolationLevel
	WebSearch FeatureLevel
	Browser   FeatureLevel
	Trust     TrustLevel
}

// Field-level "inherit" uses the zero value of each type (empty string).

// ApprovalLevel controls human approval for sensitive tool/execution steps.
type ApprovalLevel string

const (
	ApprovalInherit ApprovalLevel = ""
	ApprovalAsk     ApprovalLevel = "ask"
	ApprovalAuto    ApprovalLevel = "auto"
	// ApprovalOff disables human approval prompts (each adapter maps to its
	// vendor "bypass" or "never" where available).
	ApprovalOff ApprovalLevel = "off"
)

// IsolationLevel controls filesystem / process boundary strength.
type IsolationLevel string

const (
	IsolationInherit        IsolationLevel = ""
	IsolationReadOnly       IsolationLevel = "read_only"
	IsolationWorkspaceWrite IsolationLevel = "workspace_write"
	// IsolationUnrestricted maps to each agent's "full access" / danger sandbox
	// (or the closest available behavior).
	IsolationUnrestricted IsolationLevel = "unrestricted"
)

// FeatureLevel is used for optional capabilities (search, browser tooling).
type FeatureLevel string

const (
	FeatureInherit FeatureLevel = ""
	FeatureAllow   FeatureLevel = "allow"
	FeatureDeny    FeatureLevel = "deny"
)

// TrustLevel is honored by agents that support delegated trust (e.g. Cursor).
type TrustLevel string

const (
	TrustInherit TrustLevel = ""
	TrustAsk     TrustLevel = "ask"
	TrustAuto    TrustLevel = "auto"
	TrustDeny    TrustLevel = "deny"
)

// Presets: hosts may use these instead of ad-hoc field combinations.
var (
	// RunPolicyInteractive is conservative: ask for approvals, workspace write.
	RunPolicyInteractive = RunPolicy{Approvals: ApprovalAsk, Isolation: IsolationWorkspaceWrite}
	// RunPolicyReadOnly asks for approvals and restricts the workspace to read-only.
	RunPolicyReadOnly = RunPolicy{Approvals: ApprovalAsk, Isolation: IsolationReadOnly}
	// RunPolicyTrusted disables approval prompts and strongest isolation. It maps
	// to each vendor’s bypass / danger-mode flags where the adapter supports them.
	RunPolicyTrusted = RunPolicy{Approvals: ApprovalOff, Isolation: IsolationUnrestricted}
)

// mergeRunPolicy layers per-call runPolicy on top of binding defaults. Empty
// fields in override mean "keep default for that field".
func mergeRunPolicy(base, override *RunPolicy) RunPolicy {
	var out RunPolicy
	if base != nil {
		out = *base
	}
	if override == nil {
		return out
	}
	ov := *override
	if ov.Approvals != ApprovalInherit {
		out.Approvals = ov.Approvals
	}
	if ov.Isolation != IsolationInherit {
		out.Isolation = ov.Isolation
	}
	if ov.WebSearch != FeatureInherit {
		out.WebSearch = ov.WebSearch
	}
	if ov.Browser != FeatureInherit {
		out.Browser = ov.Browser
	}
	if ov.Trust != TrustInherit {
		out.Trust = ov.Trust
	}
	return out
}

func cloneRunPolicy(p *RunPolicy) *RunPolicy {
	if p == nil {
		return nil
	}
	c := *p
	return &c
}

// RunPolicyCapabilities lists which RunPolicy fields an adapter can apply.
// False means the dimension is ignored or not modeled for that driver.
type RunPolicyCapabilities struct {
	Approvals  bool
	Isolation  bool
	WebSearch  bool
	Browser    bool
	Trust      bool
}
