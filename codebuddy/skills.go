package codebuddy

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/engine"
	"github.com/agent-dance/agent-adaptor/internal/skillruntime"
)

func resolveSkillsHome(bindings []driver.EnvBinding) string {
	return filepath.Join(resolveConfigDir(bindings), "skills")
}

func skillsLocationLabel(bindings []driver.EnvBinding) string {
	if skillruntime.ResolveBinding(bindings, configEnvVar) != "" || strings.TrimSpace(os.Getenv(configEnvVar)) != "" {
		return resolveSkillsHome(bindings)
	}
	return "~/.codebuddy/skills"
}

func listSkills(payload driver.ResolvedSkills, selected []string, resolved []driver.Skill, bindings []driver.EnvBinding) (driver.SkillSnapshot, error) {
	skillsHome := resolveSkillsHome(bindings)
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
		LocationLabel:          skillsLocationLabel(bindings),
		InstalledDetail:        "Installed by agent-adaptor in the CodeBuddy skills home.",
		MissingDetail:          "Configured but not currently linked into the CodeBuddy skills home.",
		ExternalConflictDetail: "Skill name is occupied by an external installation.",
		ExternalDetail:         "Installed outside agent-adaptor management in the CodeBuddy skills home.",
	}), nil
}

func syncSkills(ctx context.Context, payload driver.ResolvedSkills, selected []string, resolved []driver.Skill, bindings []driver.EnvBinding, kind engine.ProfileKind) (driver.SkillSnapshot, error) {
	skillsHome := resolveSkillsHome(bindings)
	pruneMode := skillruntime.ProfileSkillPruneBrokenManaged
	if kind == engine.ProfileKindHostManaged {
		pruneMode = skillruntime.ProfileSkillPruneManaged
	}
	if _, err := skillruntime.ReconcileProfileSkills(ctx, skillruntime.ProfileSkillReconcileOptions{
		ProfileDir:   resolveConfigDir(bindings),
		SkillsHome:   skillsHome,
		Payload:      payload,
		Selected:     selected,
		ManagedRoots: []string{skillruntime.ManagedSkillCacheRoot()},
		ConflictMode: skillruntime.ProfileSkillConflictError,
		PruneMode:    pruneMode,
	}); err != nil {
		return driver.SkillSnapshot{}, err
	}
	return listSkills(payload, selected, resolved, bindings)
}
