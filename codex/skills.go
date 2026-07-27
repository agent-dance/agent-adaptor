package codex

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/skillruntime"
)

func listCodexSkills(payload driver.ResolvedSkills, selected []string, resolved []driver.Skill, codexHome string) (driver.SkillSnapshot, error) {
	skillsHome := filepath.Join(codexHome, "skills")
	installed, err := skillruntime.ReadInstalledSkillTargets(skillsHome)
	if err != nil {
		return driver.SkillSnapshot{}, err
	}
	return skillruntime.BuildPersistentSnapshot(skillruntime.PersistentSnapshotOptions{
		DriverType:             DriverType,
		Payload:                payload,
		Selected:               selected,
		Resolved:               resolved,
		Installed:              installed,
		SkillsHome:             skillsHome,
		LocationLabel:          skillsHome,
		InstalledDetail:        "Installed by agent-adaptor in the effective CODEX_HOME skills directory.",
		MissingDetail:          "Configured but not currently linked into the effective CODEX_HOME skills directory.",
		ExternalConflictDetail: "Skill name is occupied by an external installation.",
		ExternalDetail:         "Installed outside agent-adaptor management.",
	}), nil
}

func syncCodexSkills(ctx context.Context, payload driver.ResolvedSkills, selected []string, resolved []driver.Skill, codexHome string, sink driver.EventSink) (driver.SkillSnapshot, error) {
	if err := injectCodexSkillsSelected(ctx, payload, selected, codexHome, sink); err != nil {
		return driver.SkillSnapshot{}, err
	}
	return listCodexSkills(payload, selected, resolved, codexHome)
}

func effectiveCodexBindings(config driver.CommonConfig, selection *driver.ProfileSelection, agent driver.AgentIdentity) ([]driver.EnvBinding, error) {
	profile, err := resolveCodexProfileWithOptions(config, selection, agent, false)
	if err != nil {
		return nil, err
	}
	return skillruntime.WithBinding(config.Env, "CODEX_HOME", profile.Dir), nil
}

func resolveCodexProfile(config driver.CommonConfig, selection *driver.ProfileSelection, agent driver.AgentIdentity) (driver.AgentProfile, error) {
	return resolveCodexProfileWithOptions(config, selection, agent, false)
}

func effectiveCodexBindingsNoInitialize(config driver.CommonConfig, selection *driver.ProfileSelection, agent driver.AgentIdentity) ([]driver.EnvBinding, error) {
	profile, err := resolveCodexProfileWithOptions(config, selection, agent, true)
	if err != nil {
		return nil, err
	}
	return skillruntime.WithBinding(config.Env, "CODEX_HOME", profile.Dir), nil
}

func resolveCodexProfileWithOptions(config driver.CommonConfig, selection *driver.ProfileSelection, agent driver.AgentIdentity, skipInitialize bool) (driver.AgentProfile, error) {
	if selection == nil && skillruntime.ResolveBinding(config.Env, "CODEX_HOME") == "" && strings.TrimSpace(os.Getenv("CODEX_HOME")) == "" {
		bindings := config.Env
		if home := skillruntime.ResolveBinding(bindings, "HOME"); strings.TrimSpace(home) != "" {
			sharedHome := resolveSharedCodexHome(bindings)
			if _, err := os.Stat(sharedHome); err == nil {
				return driver.AgentProfile{DriverType: DriverType, Supported: true, Dir: sharedHome, EnvVar: "CODEX_HOME", Source: driver.AgentProfileSourceDefault}, nil
			}
		}
		dir := resolveManagedCodexHome(agent)
		return driver.AgentProfile{DriverType: DriverType, Supported: true, Dir: dir, EnvVar: "CODEX_HOME", Source: driver.AgentProfileSourceManaged, Managed: true}, nil
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
		SkipInitialize:   skipInitialize,
	})
	if err != nil {
		return driver.AgentProfile{}, err
	}
	profile := resolution.Profile
	profile.DriverType = DriverType
	return profile, nil
}

func codexProfile(config driver.CommonConfig, selection *driver.ProfileSelection, agent driver.AgentIdentity) driver.AgentProfile {
	profile, err := resolveCodexProfile(config, selection, agent)
	if err != nil {
		return driver.AgentProfile{DriverType: DriverType, Supported: true, EnvVar: "CODEX_HOME", Error: err.Error()}
	}
	return profile
}

func resolveSharedCodexHome(bindings []driver.EnvBinding) string {
	if configured := skillruntime.ResolveBinding(bindings, "CODEX_HOME"); configured != "" {
		return filepath.Clean(configured)
	}
	return filepath.Join(skillruntime.ResolveHome(bindings), ".codex")
}

func resolveManagedCodexHome(agent driver.AgentIdentity) string {
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

func injectCodexSkills(ctx context.Context, skills driver.ResolvedSkills, codexHome string, sink driver.EventSink) error {
	return injectCodexSkillsSelected(ctx, skills, nil, codexHome, sink)
}

func injectCodexSkillsSelected(ctx context.Context, skills driver.ResolvedSkills, selected []string, codexHome string, sink driver.EventSink) error {
	if len(skills.Entries) == 0 {
		return nil
	}
	skillsHome := filepath.Join(codexHome, "skills")
	result, err := skillruntime.ReconcileProfileSkills(ctx, skillruntime.ProfileSkillReconcileOptions{
		ProfileDir:   codexHome,
		SkillsHome:   skillsHome,
		Payload:      skills,
		Selected:     selected,
		ManagedRoots: []string{skillruntime.ManagedSkillCacheRoot()},
		ConflictMode: skillruntime.ProfileSkillConflictPreserve,
		PruneMode:    skillruntime.ProfileSkillPruneBrokenManaged,
	})
	if err != nil {
		return err
	}
	for _, change := range result.Changes {
		if sink == nil {
			continue
		}
		switch change.Action {
		case "removed":
			_ = sink.Emit(driver.RunEvent{
				Type: driver.RunEventLifecycle,
				Text: fmt.Sprintf("removed stale Codex skill %q from %s", change.RuntimeName, skillsHome),
			})
		case "created", "repaired":
			_ = sink.Emit(driver.RunEvent{
				Type: driver.RunEventLifecycle,
				Text: fmt.Sprintf("%s Codex skill %q into %s", capitalize(change.Action), change.RuntimeName, skillsHome),
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
