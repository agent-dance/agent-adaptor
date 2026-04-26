package agentadaptor

import (
	"os"
	"path/filepath"
	"strings"
)

// ProfileMode describes how a built-in adapter should choose its local
// provider profile directory for auth, config, MCP, and skill state.
type ProfileMode string

const (
	// ProfileModeUnset means "use the adapter default behavior".
	ProfileModeUnset ProfileMode = ""
	// ProfileModeNative uses the provider's native profile/home resolution.
	ProfileModeNative ProfileMode = "native"
	// ProfileModeDedicated uses Dir as the provider home/profile directory.
	ProfileModeDedicated ProfileMode = "dedicated"
	// ProfileModeClone creates or refreshes a managed profile copied from From.
	ProfileModeClone ProfileMode = "clone"
)

// CloneProfileOptions controls which parts of a source provider profile are
// copied when WithCloneProfile or WithCloneProfileFrom is used.
type CloneProfileOptions struct {
	IncludeSettings bool
	IncludeMCP      bool
	IncludeSkills   bool
	IncludeAuth     bool
}

// ProfileSelection is the normalized binding-level profile request. Hosts
// usually construct it through WithNativeProfile, WithDedicatedProfile,
// WithCloneProfile, or WithCloneProfileFrom rather than setting fields
// directly.
type ProfileSelection struct {
	Mode  ProfileMode
	Dir   string
	From  string
	Clone *CloneProfileOptions
}

func cloneProfileSelection(selection *ProfileSelection) *ProfileSelection {
	if selection == nil {
		return nil
	}
	copySelection := *selection
	if selection.Clone != nil {
		copyClone := *selection.Clone
		copySelection.Clone = &copyClone
	}
	return &copySelection
}

// NormalizeProfileDir expands ~, resolves relative paths, and cleans the
// result. It is exported for hosts that want to validate profile paths before
// building an SDK.
func NormalizeProfileDir(dir string) (string, error) {
	trimmed := strings.TrimSpace(dir)
	if trimmed == "" {
		return "", nil
	}
	if trimmed == "~" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		trimmed = home
	} else if strings.HasPrefix(trimmed, "~/") || strings.HasPrefix(trimmed, `~\`) {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		trimmed = filepath.Join(home, trimmed[2:])
	}
	absolute, err := filepath.Abs(trimmed)
	if err != nil {
		return "", err
	}
	return filepath.Clean(absolute), nil
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
