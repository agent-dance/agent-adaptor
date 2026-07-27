package engine

import "github.com/agent-dance/agent-adaptor/driver"

// Well-known session parameter keys used by the built-in adapters. The truth
// moved to the driver package in P5.2 (the keys are part of the driver SPI);
// these stay as constant aliases so the engine call sites are unchanged.
const (
	// SessionParamCWD records the workspace directory captured in a session.
	SessionParamCWD = driver.SessionParamCWD
	// SessionParamWorkspaceID records the SDK workspace lease identifier.
	SessionParamWorkspaceID = driver.SessionParamWorkspaceID
	// SessionParamPromptBundleKey records the prompt/skill bundle fingerprint
	// used as a resume guard by skill-aware adapters.
	//
	// Deprecated: built-in adapters now use SessionParamProfileFingerprint so
	// MCP, skills, agents, hooks, instructions, and config share one guard.
	SessionParamPromptBundleKey = driver.SessionParamPromptBundleKey
	// SessionParamProfileFingerprint records the provider-visible effective
	// profile resource fingerprint captured by a resumable session.
	SessionParamProfileFingerprint = driver.SessionParamProfileFingerprint
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
