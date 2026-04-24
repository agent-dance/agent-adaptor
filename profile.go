package agentadaptor

import (
	"os"
	"path/filepath"
	"strings"
)

type ProfileMode string

const (
	ProfileModeUnset     ProfileMode = ""
	ProfileModeNative    ProfileMode = "native"
	ProfileModeDedicated ProfileMode = "dedicated"
	ProfileModeClone     ProfileMode = "clone"
)

type CloneProfileOptions struct {
	IncludeSettings bool
	IncludeMCP      bool
	IncludeSkills   bool
	IncludeAuth     bool
}

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

func WithNativeProfile() AgentOption {
	return func(defaults *AgentDefaults) {
		defaults.Profile = &ProfileSelection{Mode: ProfileModeNative}
	}
}

func WithDedicatedProfile(dir string) AgentOption {
	return func(defaults *AgentDefaults) {
		defaults.Profile = &ProfileSelection{Mode: ProfileModeDedicated, Dir: dir}
	}
}

func WithCloneProfile(dir string, opts CloneProfileOptions) AgentOption {
	return func(defaults *AgentDefaults) {
		copyOpts := opts
		defaults.Profile = &ProfileSelection{Mode: ProfileModeClone, Dir: dir, Clone: &copyOpts}
	}
}

func WithCloneProfileFrom(src, dst string, opts CloneProfileOptions) AgentOption {
	return func(defaults *AgentDefaults) {
		copyOpts := opts
		defaults.Profile = &ProfileSelection{Mode: ProfileModeClone, From: src, Dir: dst, Clone: &copyOpts}
	}
}
