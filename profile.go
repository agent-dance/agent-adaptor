package agentadaptor

import "github.com/agent-dance/agent-adaptor/internal/engine"

// NormalizeProfileDir expands ~, resolves relative paths, and cleans the
// result. It is exported for hosts that want to validate profile paths before
// building an SDK.
//
// The truth moved to internal/engine in P5.2 so the driver-side profile
// runtime can reach it without importing the facade package; this stays a
// forwarder so the root surface is unchanged.
func NormalizeProfileDir(dir string) (string, error) {
	return engine.NormalizeProfileDir(dir)
}

// WithNativeProfile tells a built-in adapter to use its normal provider-native
// profile/home lookup. This is appropriate for local CLI-style integrations.
func WithNativeProfile() AgentOption {
	return func(defaults *AgentDefaults) {
		defaults.Profile = &ProfileSelection{Mode: ProfileModeNative}
	}
}

// WithDedicatedProfile pins a built-in adapter to a specific profile/home
// directory. Use it when a host manages isolated operator profiles itself.
func WithDedicatedProfile(dir string) AgentOption {
	return func(defaults *AgentDefaults) {
		defaults.Profile = &ProfileSelection{Mode: ProfileModeDedicated, Dir: dir}
	}
}

// WithCloneProfile creates a managed profile at dir by cloning the adapter's
// default/native source profile according to opts.
func WithCloneProfile(dir string, opts CloneProfileOptions) AgentOption {
	return func(defaults *AgentDefaults) {
		copyOpts := opts
		defaults.Profile = &ProfileSelection{Mode: ProfileModeClone, Dir: dir, Clone: &copyOpts}
	}
}

// WithCloneProfileFrom creates a managed profile at dst by cloning from src
// according to opts. It is useful for service hosts that seed disposable
// profiles from an operator-approved template.
func WithCloneProfileFrom(src, dst string, opts CloneProfileOptions) AgentOption {
	return func(defaults *AgentDefaults) {
		copyOpts := opts
		defaults.Profile = &ProfileSelection{Mode: ProfileModeClone, From: src, Dir: dst, Clone: &copyOpts}
	}
}
