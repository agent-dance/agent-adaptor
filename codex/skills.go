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

func listCodexSkills(payload agentadaptor.SkillPayload) agentadaptor.SkillSnapshot {
	return skillruntime.BuildEphemeralSnapshot(skillruntime.EphemeralSnapshotOptions{
		DriverType:       DriverType,
		Payload:          payload,
		ConfiguredDetail: "Will be linked into the effective CODEX_HOME/skills directory on the next run.",
		MissingDetail:    "agent-adaptor cannot find this skill in the runtime skill catalog.",
	})
}

func syncCodexSkills(payload agentadaptor.SkillPayload) agentadaptor.SkillSnapshot {
	return listCodexSkills(payload)
}

func effectiveCodexBindings(config agentadaptor.CommonConfig) []agentadaptor.EnvBinding {
	return skillruntime.ApplyProfileBinding(config.Env, config.AgentProfileDir, "CODEX_HOME")
}

func codexProfile(config agentadaptor.CommonConfig, agent agentadaptor.AgentIdentity) agentadaptor.AgentProfile {
	if configured := skillruntime.ResolveBinding(config.Env, "CODEX_HOME"); configured != "" {
		return agentadaptor.AgentProfile{
			DriverType: DriverType,
			Supported:  true,
			Dir:        filepath.Clean(configured),
			EnvVar:     "CODEX_HOME",
			Source:     agentadaptor.AgentProfileSourceBindingEnv,
		}
	}
	if strings.TrimSpace(config.AgentProfileDir) != "" {
		return agentadaptor.AgentProfile{
			DriverType: DriverType,
			Supported:  true,
			Dir:        filepath.Clean(config.AgentProfileDir),
			EnvVar:     "CODEX_HOME",
			Source:     agentadaptor.AgentProfileSourceAgentProfileDir,
		}
	}
	return agentadaptor.AgentProfile{
		DriverType: DriverType,
		Supported:  true,
		Dir:        resolveManagedCodexHome(agent),
		EnvVar:     "CODEX_HOME",
		Source:     agentadaptor.AgentProfileSourceManaged,
		Managed:    true,
	}
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

func injectCodexSkills(_ context.Context, skills agentadaptor.SkillPayload, codexHome string, sink agentadaptor.EventSink) error {
	selected := skillruntime.SelectedRuntimeEntries(skills)
	if len(selected) == 0 {
		return nil
	}
	skillsHome := filepath.Join(codexHome, "skills")
	if err := os.MkdirAll(skillsHome, 0o755); err != nil {
		return err
	}
	managedRoots := []string{skillruntime.ManagedSkillCacheRoot()}
	allowedRuntimeNames := make([]string, 0, len(selected))
	for _, entry := range selected {
		result, err := skillruntime.EnsureSkillTarget(entry.SourcePath, filepath.Join(skillsHome, entry.RuntimeName), managedRoots)
		if err != nil {
			_ = sink.Emit(agentadaptor.RunEvent{
				Type: agentadaptor.RunEventLifecycle,
				Text: fmt.Sprintf("failed to inject Codex skill %q: %v", entry.Key, err),
			})
			continue
		}
		allowedRuntimeNames = append(allowedRuntimeNames, entry.RuntimeName)
		if result == "skipped" {
			continue
		}
		_ = sink.Emit(agentadaptor.RunEvent{
			Type: agentadaptor.RunEventLifecycle,
			Text: fmt.Sprintf("%s Codex skill %q into %s", capitalize(result), entry.RuntimeName, skillsHome),
		})
	}
	removed, err := skillruntime.PruneBrokenManagedSkillTargets(skillsHome, allowedRuntimeNames, managedRoots)
	if err != nil {
		return err
	}
	for _, name := range removed {
		_ = sink.Emit(agentadaptor.RunEvent{
			Type: agentadaptor.RunEventLifecycle,
			Text: fmt.Sprintf("removed stale Codex skill %q from %s", name, skillsHome),
		})
	}
	return nil
}

func capitalize(value string) string {
	if value == "" {
		return value
	}
	return strings.ToUpper(value[:1]) + value[1:]
}
