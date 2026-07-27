package driver

// Well-known SessionParams.Values keys used by the built-in drivers.
//
// Hosts should prefer SessionCodec over direct map access, but these constants
// define the stable meanings for the SDK's built-in drivers and examples, and
// they let out-of-tree drivers populate the same keys without importing the
// facade package.
const (
	// SessionParamCWD records the workspace directory captured in a session.
	SessionParamCWD = "cwd"
	// SessionParamWorkspaceID records the SDK workspace lease identifier.
	SessionParamWorkspaceID = "workspace_id"
	// SessionParamPromptBundleKey records the prompt/skill bundle fingerprint
	// used as a resume guard by skill-aware drivers.
	//
	// Deprecated: built-in drivers now use SessionParamProfileFingerprint so
	// MCP, skills, agents, hooks, instructions, and config share one guard.
	SessionParamPromptBundleKey = "prompt_bundle_key"
	// SessionParamProfileFingerprint records the provider-visible effective
	// profile resource fingerprint captured by a resumable session.
	SessionParamProfileFingerprint = "profile_fingerprint"
)

// SessionParams is the structured host-facing view of one driver session.
//
// ResumeID is the engine-owned token needed to continue the session. DisplayID
// is the user-facing label. Values stores driver-specific session parameters
// such as cwd or profile fingerprints used for resume guards.
type SessionParams struct {
	ResumeID  string
	DisplayID string
	Values    map[string]string
}

// SessionCodec formalizes how one driver maps SessionState to stable,
// host-readable session parameters and how it derives a resume-guard
// fingerprint.
//
// The codec does not introduce a second session model. Instead it gives hosts,
// tests, and drivers a stable way to normalize SessionState and inspect
// driver-specific parameters without guessing map keys.
//
// Built-in drivers hash the ProfilePayload.Fingerprint into the session
// params so that GuardFingerprint changes whenever MCP, skills, agents,
// hooks, instructions, or structured config changes. A Run invocation that
// supplies a resume ID whose GuardFingerprint no longer matches the current
// profile payload SHOULD be rejected by the driver with a dedicated error.
// This keeps provider-visible profile resources consistent with the session
// they were captured for.
type SessionCodec interface {
	Name() string
	ToParams(state *SessionState) SessionParams
	FromParams(params SessionParams) *SessionState
	GuardFingerprint(params SessionParams) string
}
