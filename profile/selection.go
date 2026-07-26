package profile

import "github.com/agent-dance/agent-adaptor/driver"

// Selection is the normalized profile request consumed by built-in drivers.
// It is an alias for [driver.ProfileSelection]; the constructors below are
// the v1 spelling of the historical root options and produce byte-identical
// values.
type Selection = driver.ProfileSelection

// Mode describes how a driver chooses its local provider profile directory.
// It is an alias for [driver.ProfileMode].
type Mode = driver.ProfileMode

const (
	// ModeUnset means "use the driver default behavior" (see Default).
	ModeUnset = driver.ProfileModeUnset
	// ModeNative uses the provider's native profile/home resolution.
	ModeNative = driver.ProfileModeNative
	// ModeDedicated uses Selection.Dir as the provider home/profile directory.
	ModeDedicated = driver.ProfileModeDedicated
	// ModeClone creates or refreshes a managed profile copied from a source.
	ModeClone = driver.ProfileModeClone
)

// CloneOptions controls which parts of a source provider profile are copied
// by CloneNative and CloneFrom. It is an alias for
// [driver.CloneProfileOptions]; hosts normally build it through CloneOption
// values instead of setting fields directly.
type CloneOptions = driver.CloneProfileOptions

// AuthMode controls how clone constructors seed auth files from the source
// profile. It is an alias for [driver.CloneProfileAuthMode].
type AuthMode = driver.CloneProfileAuthMode

const (
	// AuthNone leaves auth files out of the cloned profile.
	AuthNone = driver.CloneProfileAuthNone
	// AuthCopy copies auth files into the cloned profile (see CopyAuth).
	AuthCopy = driver.CloneProfileAuthCopy
	// AuthLink shares auth files with the source profile (see LinkAuth).
	AuthLink = driver.CloneProfileAuthLink
)

// Default requests the driver's default profile behavior. It is the zero
// Selection (ModeUnset) — the same state a binding has when no profile
// option is applied at all. Most hosts want Native or an isolated mode
// instead; Default exists so the "unset" form of the historical API keeps an
// explicit v1 name.
func Default() Selection {
	return Selection{}
}

// Native uses the provider's normal native profile/home lookup (~/.claude,
// ~/.codex, ...). This is appropriate for local CLI-style integrations.
//
// It is equivalent to the historical root option WithNativeProfile.
func Native() Selection {
	return Selection{Mode: driver.ProfileModeNative}
}

// Dedicated pins the agent to a specific profile/home directory. Use it when
// a host manages isolated operator profiles itself.
//
// It is equivalent to the historical root option WithDedicatedProfile(dir).
func Dedicated(dir string) Selection {
	return Selection{Mode: driver.ProfileModeDedicated, Dir: dir}
}

// CloneNative creates or refreshes a managed profile at dir by cloning the
// driver's default/native source profile. With no options only the managed
// profile skeleton is ensured; add CopySettings, CopyMCP, CopySkills, and
// CopyAuth/LinkAuth to seed state from the source.
//
// It is equivalent to the historical root option WithCloneProfile(dir, opts).
func CloneNative(dir string, opts ...CloneOption) Selection {
	return Selection{Mode: driver.ProfileModeClone, Dir: dir, Clone: applyCloneOptions(opts)}
}

// CloneFrom creates or refreshes a managed profile at dst by cloning from
// src. It is useful for service hosts that seed disposable profiles from an
// operator-approved template.
//
// It is equivalent to the historical root option
// WithCloneProfileFrom(src, dst, opts).
func CloneFrom(src, dst string, opts ...CloneOption) Selection {
	return Selection{Mode: driver.ProfileModeClone, From: src, Dir: dst, Clone: applyCloneOptions(opts)}
}

// CloneOption adjusts the CloneOptions carried by CloneNative and CloneFrom.
type CloneOption func(*CloneOptions)

// CopySettings copies the provider settings files into the cloned profile
// when they are missing there (CloneOptions.IncludeSettings).
func CopySettings() CloneOption {
	return func(opts *CloneOptions) { opts.IncludeSettings = true }
}

// CopyMCP copies the provider MCP configuration files into the cloned
// profile when they are missing there (CloneOptions.IncludeMCP).
func CopyMCP() CloneOption {
	return func(opts *CloneOptions) { opts.IncludeMCP = true }
}

// CopySkills copies the provider skills directories into the cloned profile
// when they are missing there (CloneOptions.IncludeSkills).
func CopySkills() CloneOption {
	return func(opts *CloneOptions) { opts.IncludeSkills = true }
}

// CopyAuth copies auth files into the cloned profile when they are missing
// there (AuthMode = AuthCopy). This is suitable for static API-key style
// auth, but can duplicate OAuth refresh-token state for CLIs that rotate
// tokens in place; prefer LinkAuth for OAuth-backed CLIs.
//
// It has the same effect as the legacy CloneOptions.IncludeAuth field, which
// resolves to AuthCopy when AuthMode is unset.
func CopyAuth() CloneOption {
	return func(opts *CloneOptions) { opts.AuthMode = driver.CloneProfileAuthCopy }
}

// LinkAuth shares auth files with the source profile by symlink, falling
// back to a hardlink when symlinks are unavailable (AuthMode = AuthLink).
// The clone therefore reuses the machine's OAuth login state instead of
// duplicating token files, and it fails rather than silently copying if
// neither shared-file strategy works.
func LinkAuth() CloneOption {
	return func(opts *CloneOptions) { opts.AuthMode = driver.CloneProfileAuthLink }
}

// WithOptions replaces the accumulated CloneOptions with a pre-built struct.
// It is the escape hatch for hosts migrating a stored
// [driver.CloneProfileOptions] value (including the legacy IncludeAuth
// field) without translating it field by field; CloneOption values applied
// after it still take effect on top.
func WithOptions(opts CloneOptions) CloneOption {
	return func(target *CloneOptions) { *target = opts }
}

func applyCloneOptions(opts []CloneOption) *CloneOptions {
	out := &CloneOptions{}
	for _, opt := range opts {
		if opt != nil {
			opt(out)
		}
	}
	return out
}
