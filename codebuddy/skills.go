package codebuddy

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	agentadaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/internal/skillruntime"
)

func resolveSkillsHome(bindings []agentadaptor.EnvBinding) string {
	return filepath.Join(resolveConfigDir(bindings), "skills")
}

func skillsLocationLabel(bindings []agentadaptor.EnvBinding) string {
	if skillruntime.ResolveBinding(bindings, configEnvVar) != "" || strings.TrimSpace(os.Getenv(configEnvVar)) != "" {
		return resolveSkillsHome(bindings)
	}
	return "~/.codebuddy/skills"
}

func listSkills(payload agentadaptor.ResolvedSkills, selected []string, resolved []agentadaptor.Skill, bindings []agentadaptor.EnvBinding) (agentadaptor.SkillSnapshot, error) {
	skillsHome := resolveSkillsHome(bindings)
	installed, err := skillruntime.ReadInstalledSkillTargets(skillsHome)
	if err != nil {
		return agentadaptor.SkillSnapshot{}, err
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

func syncSkills(ctx context.Context, payload agentadaptor.ResolvedSkills, selected []string, resolved []agentadaptor.Skill, bindings []agentadaptor.EnvBinding, kind agentadaptor.ProfileKind) (agentadaptor.SkillSnapshot, error) {
	skillsHome := resolveSkillsHome(bindings)
	pruneMode := skillruntime.ProfileSkillPruneBrokenManaged
	if kind == agentadaptor.ProfileKindHostManaged {
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
		return agentadaptor.SkillSnapshot{}, err
	}
	return listSkills(payload, selected, resolved, bindings)
}
