package codex

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	agentadaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/internal/skillruntime"
)

var codexCopiedSharedFiles = []string{"config.json", "config.toml", "instructions.md"}
var codexSymlinkedSharedFiles = []string{"auth.json"}

func listCodexSkills(payload agentadaptor.ResolvedSkills, selected []string, resolved []agentadaptor.Skill) agentadaptor.SkillSnapshot {
	return skillruntime.BuildEphemeralSnapshot(skillruntime.EphemeralSnapshotOptions{
		DriverType:       DriverType,
		Payload:          payload,
		Selected:         selected,
		Resolved:         resolved,
		ConfiguredDetail: "Will be linked into the effective CODEX_HOME/skills directory on the next run.",
		MissingDetail:    "agent-adaptor cannot find this skill in the runtime skill catalog.",
	})
}

func syncCodexSkills(payload agentadaptor.ResolvedSkills, selected []string, resolved []agentadaptor.Skill) agentadaptor.SkillSnapshot {
	return listCodexSkills(payload, selected, resolved)
}

func effectiveCodexBindings(config agentadaptor.CommonConfig, selection *agentadaptor.ProfileSelection, agent agentadaptor.AgentIdentity) ([]agentadaptor.EnvBinding, error) {
	profile, err := resolveCodexProfile(config, selection, agent)
	if err != nil {
		return nil, err
	}
	return skillruntime.WithBinding(config.Env, "CODEX_HOME", profile.Dir), nil
}

func resolveCodexProfile(config agentadaptor.CommonConfig, selection *agentadaptor.ProfileSelection, agent agentadaptor.AgentIdentity) (agentadaptor.AgentProfile, error) {
	if selection == nil && skillruntime.ResolveBinding(config.Env, "CODEX_HOME") == "" && strings.TrimSpace(os.Getenv("CODEX_HOME")) == "" {
		bindings := config.Env
		if home := skillruntime.ResolveBinding(bindings, "HOME"); strings.TrimSpace(home) != "" {
			sharedHome := resolveSharedCodexHome(bindings)
			if _, err := os.Stat(sharedHome); err == nil {
				return agentadaptor.AgentProfile{DriverType: DriverType, Supported: true, Dir: sharedHome, EnvVar: "CODEX_HOME", Source: agentadaptor.AgentProfileSourceDefault}, nil
			}
		}
		dir := resolveManagedCodexHome(agent)
		return agentadaptor.AgentProfile{DriverType: DriverType, Supported: true, Dir: dir, EnvVar: "CODEX_HOME", Source: agentadaptor.AgentProfileSourceManaged, Managed: true}, nil
	}
	resolution, err := skillruntime.ResolveProfile(skillruntime.ProfileResolveOptions{
		Bindings:         config.Env,
		Selection:        selection,
		EnvVar:           "CODEX_HOME",
		DefaultDir:       filepath.Join(skillruntime.ResolveHome(config.Env), ".codex"),
		NativeSharedDir:  resolveSharedCodexHome(config.Env),
		DedicatedSubdirs: []string{"skills"},
		SettingsFiles:    []string{"config.json", "config.toml", "instructions.md"},
		MCPFiles:         []string{"config.json", "config.toml"},
		SkillsDirs:       []string{"skills"},
		AuthFiles:        []string{"auth.json"},
	})
	if err != nil {
		return agentadaptor.AgentProfile{}, err
	}
	profile := resolution.Profile
	profile.DriverType = DriverType
	return profile, nil
}

func codexProfile(config agentadaptor.CommonConfig, selection *agentadaptor.ProfileSelection, agent agentadaptor.AgentIdentity) agentadaptor.AgentProfile {
	profile, err := resolveCodexProfile(config, selection, agent)
	if err != nil {
		return agentadaptor.AgentProfile{DriverType: DriverType, Supported: true, EnvVar: "CODEX_HOME", Error: err.Error()}
	}
	return profile
}

func resolveSharedCodexHome(bindings []agentadaptor.EnvBinding) string {
	if configured := skillruntime.ResolveBinding(bindings, "CODEX_HOME"); configured != "" {
		return filepath.Clean(configured)
	}
	return filepath.Join(skillruntime.ResolveHome(bindings), ".codex")
}

func resolveManagedCodexHome(agent agentadaptor.AgentIdentity) string {
	root, err := os.UserConfigDir()
	if err != nil || strings.TrimSpace(root) == "" {
		root = skillruntime.ResolveHome(nil)
	}
	tenant := safeNamespace(agent.TenantID)
	return filepath.Join(root, "agent-adaptor", "codex-home", tenant)
}

func safeNamespace(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "default"
	}
	builder := strings.Builder{}
	for _, r := range trimmed {
		switch {
		case r >= 'a' && r <= 'z':
			builder.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			builder.WriteRune(r + ('a' - 'A'))
		case r >= '0' && r <= '9':
			builder.WriteRune(r)
		default:
			builder.WriteRune('-')
		}
	}
	return strings.Trim(builder.String(), "-")
}

func prepareManagedCodexHome(bindings []agentadaptor.EnvBinding, agent agentadaptor.AgentIdentity) (string, error) {
	targetHome := resolveManagedCodexHome(agent)
	sourceHome := resolveSharedCodexHome(bindings)
	if filepath.Clean(sourceHome) == filepath.Clean(targetHome) {
		return targetHome, nil
	}
	if err := os.MkdirAll(targetHome, 0o755); err != nil {
		return "", err
	}
	for _, name := range codexSymlinkedSharedFiles {
		source := filepath.Join(sourceHome, name)
		if _, err := os.Stat(source); err != nil {
			continue
		}
		if err := ensureCodexSymlink(source, filepath.Join(targetHome, name)); err != nil {
			return "", err
		}
	}
	for _, name := range codexCopiedSharedFiles {
		source := filepath.Join(sourceHome, name)
		if _, err := os.Stat(source); err != nil {
			continue
		}
		target := filepath.Join(targetHome, name)
		if _, err := os.Stat(target); err == nil {
			continue
		}
		if err := copyFile(source, target); err != nil {
			return "", err
		}
	}
	return targetHome, nil
}

func ensureCodexSymlink(source, target string) error {
	info, err := os.Lstat(target)
	if err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			linkedPath, readErr := os.Readlink(target)
			if readErr == nil {
				resolved := linkedPath
				if !filepath.IsAbs(resolved) {
					resolved = filepath.Join(filepath.Dir(target), resolved)
				}
				if filepath.Clean(resolved) == filepath.Clean(source) {
					return nil
				}
			}
			if err := os.Remove(target); err != nil {
				return err
			}
		} else {
			return nil
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	if err := os.Symlink(source, target); err == nil {
		return nil
	}
	return copyFile(source, target)
}

func copyFile(source, target string) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	data, err := os.ReadFile(source)
	if err != nil {
		return err
	}
	return os.WriteFile(target, data, 0o644)
}

func injectCodexSkills(_ context.Context, skills agentadaptor.ResolvedSkills, codexHome string, sink agentadaptor.EventSink) error {
	entries := skills.Entries
	if len(entries) == 0 {
		return nil
	}
	skillsHome := filepath.Join(codexHome, "skills")
	if err := os.MkdirAll(skillsHome, 0o755); err != nil {
		return err
	}
	managedRoots := []string{skillruntime.ManagedSkillCacheRoot()}
	allowedRuntimeNames := make([]string, 0, len(entries))
	for _, entry := range entries {
		if strings.TrimSpace(entry.SourcePath) == "" {
			continue
		}
		result, err := skillruntime.EnsureSkillTarget(entry.SourcePath, filepath.Join(skillsHome, entry.RuntimeName), managedRoots)
		if err != nil {
			if sink != nil {
				_ = sink.Emit(agentadaptor.RunEvent{
					Type: agentadaptor.RunEventLifecycle,
					Text: fmt.Sprintf("failed to inject Codex skill %q: %v", entry.Key, err),
				})
			}
			continue
		}
		allowedRuntimeNames = append(allowedRuntimeNames, entry.RuntimeName)
		if result == "skipped" {
			continue
		}
		if sink != nil {
			_ = sink.Emit(agentadaptor.RunEvent{
				Type: agentadaptor.RunEventLifecycle,
				Text: fmt.Sprintf("%s Codex skill %q into %s", capitalize(result), entry.RuntimeName, skillsHome),
			})
		}
	}
	removed, err := skillruntime.PruneBrokenManagedSkillTargets(skillsHome, allowedRuntimeNames, managedRoots)
	if err != nil {
		return err
	}
	for _, name := range removed {
		if sink != nil {
			_ = sink.Emit(agentadaptor.RunEvent{
				Type: agentadaptor.RunEventLifecycle,
				Text: fmt.Sprintf("removed stale Codex skill %q from %s", name, skillsHome),
			})
		}
	}
	return nil
}

func capitalize(value string) string {
	if value == "" {
		return value
	}
	return strings.ToUpper(value[:1]) + value[1:]
}
