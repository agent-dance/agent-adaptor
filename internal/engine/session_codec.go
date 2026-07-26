package engine

// Well-known session parameter keys used by the built-in adapters.
//
// Hosts should prefer SessionCodec over direct map access, but these constants
// define the stable meanings for the SDK's built-in adapters and examples.
const (
	// SessionParamCWD records the workspace directory captured in a session.
	SessionParamCWD = "cwd"
	// SessionParamWorkspaceID records the SDK workspace lease identifier.
	SessionParamWorkspaceID = "workspace_id"
	// SessionParamPromptBundleKey records the prompt/skill bundle fingerprint
	// used as a resume guard by skill-aware adapters.
	//
	// Deprecated: built-in adapters now use SessionParamProfileFingerprint so
	// MCP, skills, agents, hooks, instructions, and config share one guard.
	SessionParamPromptBundleKey = "prompt_bundle_key"
	// SessionParamProfileFingerprint records the provider-visible effective
	// profile resource fingerprint captured by a resumable session.
	SessionParamProfileFingerprint = "profile_fingerprint"
)

type passthroughSessionCodec struct{}

func (passthroughSessionCodec) Name() string { return "passthrough" }

func (passthroughSessionCodec) ToParams(state *DriverSessionState) SessionParams {
	if state == nil {
		return SessionParams{}
	}
	displayID := state.DisplayID
	if displayID == "" {
		displayID = state.ResumeID
	}
	return SessionParams{
		ResumeID:  state.ResumeID,
		DisplayID: displayID,
		Values:    cloneStringMap(state.Data),
	}
}

func (passthroughSessionCodec) FromParams(params SessionParams) *DriverSessionState {
	if params.ResumeID == "" && params.DisplayID == "" && len(params.Values) == 0 {
		return nil
	}
	displayID := params.DisplayID
	if displayID == "" {
		displayID = params.ResumeID
	}
	return &DriverSessionState{
		ResumeID:  params.ResumeID,
		DisplayID: displayID,
		Data:      cloneStringMap(params.Values),
	}
}

func (passthroughSessionCodec) GuardFingerprint(params SessionParams) string {
	if params.ResumeID == "" && params.DisplayID == "" && len(params.Values) == 0 {
		return ""
	}
	return stableHash(params.ResumeID, params.DisplayID, params.Values)
}

// SessionCodecFor returns the adapter's explicit session codec when available,
// otherwise it falls back to a passthrough codec that simply round-trips
// DriverSessionState fields.
func SessionCodecFor(driver DriverAdapter) SessionCodec {
	if aware, ok := driver.(SessionCodecAwareDriver); ok {
		if codec := aware.SessionCodec(); codec != nil {
			return codec
		}
	}
	return passthroughSessionCodec{}
}

func normalizeSessionState(driver DriverAdapter, state *DriverSessionState) *DriverSessionState {
	codec := SessionCodecFor(driver)
	return codec.FromParams(codec.ToParams(state))
}

func sessionDisplayID(driver DriverAdapter, state *DriverSessionState) string {
	params := SessionCodecFor(driver).ToParams(state)
	if params.DisplayID != "" {
		return params.DisplayID
	}
	return params.ResumeID
}
