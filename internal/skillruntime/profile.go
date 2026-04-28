package skillruntime

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	agentadaptor "github.com/agent-dance/agent-adaptor"
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
		dir, err := agentadaptor.NormalizeProfileDir(configured)
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
				if err := cloneProfileIfMissing(from, dir, opts, cloneOpts); err != nil {
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
		dir, err := agentadaptor.NormalizeProfileDir(configured)
		if err != nil {
			return ProfileResolution{}, err
		}
		return ProfileResolution{Bindings: WithBinding(bindings, opts.EnvVar, dir), Profile: agentadaptor.AgentProfile{Supported: true, Dir: dir, EnvVar: opts.EnvVar, Source: agentadaptor.AgentProfileSourceProcessEnv}}, nil
	}

	dir := strings.TrimSpace(opts.DefaultDir)
	if dir == "" {
		dir = opts.NativeSharedDir
	}
	normalized, err := agentadaptor.NormalizeProfileDir(dir)
	if err != nil {
		return ProfileResolution{}, err
	}
	return ProfileResolution{Bindings: WithBinding(bindings, opts.EnvVar, normalized), Profile: agentadaptor.AgentProfile{Supported: true, Dir: normalized, EnvVar: opts.EnvVar, Source: agentadaptor.AgentProfileSourceDefault}}, nil
}

func nativeProfileDir(bindings []agentadaptor.EnvBinding, opts ProfileResolveOptions) (string, error) {
	if configured := strings.TrimSpace(os.Getenv(opts.EnvVar)); configured != "" {
		return agentadaptor.NormalizeProfileDir(configured)
	}
	return agentadaptor.NormalizeProfileDir(opts.NativeSharedDir)
}

func dedicatedProfileDir(dir string) (string, error) {
	normalized, err := agentadaptor.NormalizeProfileDir(dir)
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
		return agentadaptor.NormalizeProfileDir(from)
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

func cloneProfileIfMissing(source, target string, opts ProfileResolveOptions, cloneOpts agentadaptor.CloneProfileOptions) error {
	if _, err := os.Stat(target); err == nil {
		return ensureDedicatedProfile(target, opts.DedicatedSubdirs)
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := ensureDedicatedProfile(target, opts.DedicatedSubdirs); err != nil {
		return err
	}
	if cloneOpts.IncludeSettings {
		if err := copyNamedEntries(source, target, opts.SettingsFiles); err != nil {
			return err
		}
	}
	if cloneOpts.IncludeMCP {
		if err := copyNamedEntries(source, target, opts.MCPFiles); err != nil {
			return err
		}
	}
	if cloneOpts.IncludeSkills {
		if err := copyNamedEntries(source, target, opts.SkillsDirs); err != nil {
			return err
		}
	}
	if cloneOpts.IncludeAuth {
		if err := copyNamedEntries(source, target, opts.AuthFiles); err != nil {
			return err
		}
	}
	return nil
}

func copyNamedEntries(sourceRoot, targetRoot string, names []string) error {
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
		if err := copyPath(source, filepath.Join(targetRoot, name)); err != nil {
			return err
		}
	}
	return nil
}

func copyPath(source, target string) error {
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
			if err := copyPath(filepath.Join(source, entry.Name()), filepath.Join(target, entry.Name())); err != nil {
				return err
			}
		}
		return nil
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
