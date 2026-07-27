package skillruntime

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	agentadaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/internal/engine"
)

type ProfileResolveOptions struct {
	Bindings         []agentadaptor.EnvBinding
	Selection        *agentadaptor.ProfileSelection
	EnvVar           string
	DefaultDir       string
	NativeSharedDir  string
	DedicatedSubdirs []string
	SettingsFiles    []string
	MCPFiles         []string
	SkillsDirs       []string
	AuthFiles        []string
	SkipInitialize   bool
}

type ProfileResolution struct {
	Bindings []agentadaptor.EnvBinding
	Profile  agentadaptor.AgentProfile
}

func ResolveProfile(opts ProfileResolveOptions) (ProfileResolution, error) {
	bindings := append([]agentadaptor.EnvBinding(nil), opts.Bindings...)
	if configured := ResolveBinding(bindings, opts.EnvVar); configured != "" {
		dir, err := engine.NormalizeProfileDir(configured)
		if err != nil {
			return ProfileResolution{}, err
		}
		return ProfileResolution{Bindings: WithBinding(bindings, opts.EnvVar, dir), Profile: agentadaptor.AgentProfile{Supported: true, Dir: dir, EnvVar: opts.EnvVar, Source: agentadaptor.AgentProfileSourceBindingEnv}}, nil
	}

	selection := opts.Selection
	if selection != nil {
		switch selection.Mode {
		case agentadaptor.ProfileModeNative:
			dir, err := nativeProfileDir(bindings, opts)
			if err != nil {
				return ProfileResolution{}, err
			}
			return ProfileResolution{Bindings: WithBinding(bindings, opts.EnvVar, dir), Profile: agentadaptor.AgentProfile{Supported: true, Dir: dir, EnvVar: opts.EnvVar, Source: agentadaptor.AgentProfileSourceProcessEnv}}, nil
		case agentadaptor.ProfileModeDedicated:
			dir, err := dedicatedProfileDir(selection.Dir)
			if err != nil {
				return ProfileResolution{}, err
			}
			if !opts.SkipInitialize {
				if err := ensureDedicatedProfile(dir, opts.DedicatedSubdirs); err != nil {
					return ProfileResolution{}, err
				}
			}
			return ProfileResolution{Bindings: WithBinding(bindings, opts.EnvVar, dir), Profile: agentadaptor.AgentProfile{Supported: true, Dir: dir, EnvVar: opts.EnvVar, Source: agentadaptor.AgentProfileSourceProfileOption}}, nil
		case agentadaptor.ProfileModeClone:
			dir, err := dedicatedProfileDir(selection.Dir)
			if err != nil {
				return ProfileResolution{}, err
			}
			from, err := cloneSourceDir(bindings, selection.From, opts)
			if err != nil {
				return ProfileResolution{}, err
			}
			if !opts.SkipInitialize {
				cloneOpts := agentadaptor.CloneProfileOptions{}
				if selection.Clone != nil {
					cloneOpts = *selection.Clone
				}
				if err := reconcileCloneProfile(from, dir, opts, cloneOpts); err != nil {
					return ProfileResolution{}, err
				}
			}
			return ProfileResolution{Bindings: WithBinding(bindings, opts.EnvVar, dir), Profile: agentadaptor.AgentProfile{Supported: true, Dir: dir, EnvVar: opts.EnvVar, Source: agentadaptor.AgentProfileSourceProfileOption}}, nil
		case agentadaptor.ProfileModeUnset:
		default:
			return ProfileResolution{}, fmt.Errorf("unsupported profile mode %q", selection.Mode)
		}
	}

	if configured := strings.TrimSpace(os.Getenv(opts.EnvVar)); configured != "" {
		dir, err := engine.NormalizeProfileDir(configured)
		if err != nil {
			return ProfileResolution{}, err
		}
		return ProfileResolution{Bindings: WithBinding(bindings, opts.EnvVar, dir), Profile: agentadaptor.AgentProfile{Supported: true, Dir: dir, EnvVar: opts.EnvVar, Source: agentadaptor.AgentProfileSourceProcessEnv}}, nil
	}

	dir := strings.TrimSpace(opts.DefaultDir)
	if dir == "" {
		dir = opts.NativeSharedDir
	}
	normalized, err := engine.NormalizeProfileDir(dir)
	if err != nil {
		return ProfileResolution{}, err
	}
	return ProfileResolution{Bindings: WithBinding(bindings, opts.EnvVar, normalized), Profile: agentadaptor.AgentProfile{Supported: true, Dir: normalized, EnvVar: opts.EnvVar, Source: agentadaptor.AgentProfileSourceDefault}}, nil
}

func nativeProfileDir(bindings []agentadaptor.EnvBinding, opts ProfileResolveOptions) (string, error) {
	if configured := strings.TrimSpace(os.Getenv(opts.EnvVar)); configured != "" {
		return engine.NormalizeProfileDir(configured)
	}
	return engine.NormalizeProfileDir(opts.NativeSharedDir)
}

func dedicatedProfileDir(dir string) (string, error) {
	normalized, err := engine.NormalizeProfileDir(dir)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(normalized) == "" {
		return "", fmt.Errorf("profile directory is required")
	}
	return normalized, nil
}

func cloneSourceDir(bindings []agentadaptor.EnvBinding, from string, opts ProfileResolveOptions) (string, error) {
	if strings.TrimSpace(from) != "" {
		return engine.NormalizeProfileDir(from)
	}
	return nativeProfileDir(bindings, opts)
}

func ensureDedicatedProfile(dir string, subdirs []string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for _, subdir := range subdirs {
		if strings.TrimSpace(subdir) == "" {
			continue
		}
		if err := os.MkdirAll(filepath.Join(dir, subdir), 0o755); err != nil {
			return err
		}
	}
	return nil
}

func reconcileCloneProfile(source, target string, opts ProfileResolveOptions, cloneOpts agentadaptor.CloneProfileOptions) error {
	if _, err := os.Stat(target); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := ensureDedicatedProfile(target, opts.DedicatedSubdirs); err != nil {
		return err
	}
	if cloneOpts.IncludeSettings {
		if err := copyNamedEntriesIfMissing(source, target, opts.SettingsFiles); err != nil {
			return err
		}
	}
	if cloneOpts.IncludeMCP {
		if err := copyNamedEntriesIfMissing(source, target, opts.MCPFiles); err != nil {
			return err
		}
	}
	if cloneOpts.IncludeSkills {
		if err := copyNamedEntriesIfMissing(source, target, opts.SkillsDirs); err != nil {
			return err
		}
	}
	switch cloneAuthMode(cloneOpts) {
	case agentadaptor.CloneProfileAuthNone:
	case agentadaptor.CloneProfileAuthCopy:
		if err := copyNamedEntriesIfMissing(source, target, opts.AuthFiles); err != nil {
			return err
		}
	case agentadaptor.CloneProfileAuthLink:
		if err := linkNamedEntries(source, target, opts.AuthFiles); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unsupported clone auth mode %q", cloneOpts.AuthMode)
	}
	return nil
}

func cloneAuthMode(opts agentadaptor.CloneProfileOptions) agentadaptor.CloneProfileAuthMode {
	if opts.AuthMode != "" {
		return opts.AuthMode
	}
	if opts.IncludeAuth {
		return agentadaptor.CloneProfileAuthCopy
	}
	return agentadaptor.CloneProfileAuthNone
}

func copyNamedEntriesIfMissing(sourceRoot, targetRoot string, names []string) error {
	for _, name := range names {
		if strings.TrimSpace(name) == "" {
			continue
		}
		source := filepath.Join(sourceRoot, name)
		if _, err := os.Stat(source); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return err
		}
		if err := copyPathIfMissing(source, filepath.Join(targetRoot, name)); err != nil {
			return err
		}
	}
	return nil
}

func copyPathIfMissing(source, target string) error {
	info, err := os.Stat(source)
	if err != nil {
		return err
	}
	if info.IsDir() {
		entries, err := os.ReadDir(source)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(target, 0o755); err != nil {
			return err
		}
		for _, entry := range entries {
			if err := copyPathIfMissing(filepath.Join(source, entry.Name()), filepath.Join(target, entry.Name())); err != nil {
				return err
			}
		}
		return nil
	}
	if _, err := os.Lstat(target); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	data, err := os.ReadFile(source)
	if err != nil {
		return err
	}
	return os.WriteFile(target, data, info.Mode().Perm())
}

func linkNamedEntries(sourceRoot, targetRoot string, names []string) error {
	for _, name := range names {
		if strings.TrimSpace(name) == "" {
			continue
		}
		source := filepath.Join(sourceRoot, name)
		info, err := os.Stat(source)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return err
		}
		if info.IsDir() {
			return fmt.Errorf("clone auth link requires file auth entry %q", source)
		}
		if err := ensureSharedFileLink(source, filepath.Join(targetRoot, name)); err != nil {
			return err
		}
	}
	return nil
}

func ensureSharedFileLink(source, target string) error {
	source = filepath.Clean(source)
	target = filepath.Clean(target)
	if sameFile(source, target) {
		return nil
	}

	info, err := os.Lstat(target)
	if err == nil {
		if info.IsDir() {
			return fmt.Errorf("auth target %q is a directory", target)
		}
		if err := os.Remove(target); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	if err := os.Symlink(source, target); err == nil {
		return nil
	} else {
		symlinkErr := err
		if err := os.Link(source, target); err == nil {
			return nil
		} else {
			return fmt.Errorf("share auth file %q with %q: symlink failed: %v; hardlink failed: %w", source, target, symlinkErr, err)
		}
	}
}

func sameFile(left, right string) bool {
	leftInfo, leftErr := os.Stat(left)
	rightInfo, rightErr := os.Stat(right)
	return leftErr == nil && rightErr == nil && os.SameFile(leftInfo, rightInfo)
}
