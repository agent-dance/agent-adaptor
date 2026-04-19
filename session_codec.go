package agentadaptor

// Well-known session parameter keys used by the built-in adapters.
//
// Hosts should prefer SessionCodec over direct map access, but these constants
// define the stable meanings for the SDK's built-in adapters and examples.
const (
	SessionParamCWD             = "cwd"
	SessionParamWorkspaceID     = "workspace_id"
	SessionParamPromptBundleKey = "prompt_bundle_key"
)

// SessionParams is the structured host-facing view of one adapter session.
//
// ResumeID is the engine-owned token needed to continue the session. DisplayID
// is the user-facing label. Values stores adapter-specific session parameters
// such as cwd or prompt bundle fingerprints used for resume guards.
type SessionParams struct {
	ResumeID  string
	DisplayID string
	Values    map[string]string
}

// SessionCodec formalizes how one adapter maps DriverSessionState to stable,
// host-readable session parameters and how it derives a resume-guard fingerprint.
//
// The codec does not introduce a second session model. Instead it gives hosts,
// tests, and adapters a stable way to normalize DriverSessionState and inspect
// adapter-specific parameters without guessing map keys.
type SessionCodec interface {
	Name() string
	ToParams(state *DriverSessionState) SessionParams
	FromParams(params SessionParams) *DriverSessionState
	GuardFingerprint(params SessionParams) string
}

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
