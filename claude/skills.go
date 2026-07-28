package claude

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/engine"
	"github.com/agent-dance/agent-adaptor/internal/skillruntime"
)

func resolveClaudeSkillsHome(bindings []driver.EnvBinding) string {
	return filepath.Join(resolveClaudeConfigDir(bindings), "skills")
}

func claudeSkillsLocationLabel(bindings []driver.EnvBinding) string {
	if skillruntime.ResolveBinding(bindings, "CLAUDE_CONFIG_DIR") != "" || strings.TrimSpace(os.Getenv("CLAUDE_CONFIG_DIR")) != "" {
		return resolveClaudeSkillsHome(bindings)
	}
	return "~/.claude/skills"
}

func listClaudeSkills(payload driver.ResolvedSkills, selected []string, resolved []driver.Skill, bindings []driver.EnvBinding) (driver.SkillSnapshot, error) {
	skillsHome := resolveClaudeSkillsHome(bindings)
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
		LocationLabel:          claudeSkillsLocationLabel(bindings),
		InstalledDetail:        "Installed by agent-adaptor in the Claude skills home.",
		MissingDetail:          "Configured but not currently linked into the Claude skills home.",
		ExternalConflictDetail: "Skill name is occupied by an external installation.",
		ExternalDetail:         "Installed outside agent-adaptor management in the Claude skills home.",
	}), nil
}

func syncClaudeSkills(ctx context.Context, payload driver.ResolvedSkills, selected []string, resolved []driver.Skill, bindings []driver.EnvBinding, kind engine.ProfileKind) (driver.SkillSnapshot, error) {
	skillsHome := resolveClaudeSkillsHome(bindings)
	pruneMode := skillruntime.ProfileSkillPruneBrokenManaged
	if kind == engine.ProfileKindHostManaged {
		pruneMode = skillruntime.ProfileSkillPruneManaged
	}
	if _, err := skillruntime.ReconcileProfileSkills(ctx, skillruntime.ProfileSkillReconcileOptions{
		ProfileDir:   resolveClaudeConfigDir(bindings),
		SkillsHome:   skillsHome,
		Payload:      payload,
		Selected:     selected,
		ManagedRoots: []string{skillruntime.ManagedSkillCacheRoot()},
		ConflictMode: skillruntime.ProfileSkillConflictError,
		PruneMode:    pruneMode,
	}); err != nil {
		return driver.SkillSnapshot{}, err
	}
	return listClaudeSkills(payload, selected, resolved, bindings)
}
