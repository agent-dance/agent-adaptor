package claude

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	agentadaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/internal/skillruntime"
)

func resolveClaudeSkillsHome(bindings []agentadaptor.EnvBinding) string {
	return filepath.Join(resolveClaudeConfigDir(bindings), "skills")
}

func claudeSkillsLocationLabel(bindings []agentadaptor.EnvBinding) string {
	if skillruntime.ResolveBinding(bindings, "CLAUDE_CONFIG_DIR") != "" || strings.TrimSpace(os.Getenv("CLAUDE_CONFIG_DIR")) != "" {
		return resolveClaudeSkillsHome(bindings)
	}
	return "~/.claude/skills"
}

func listClaudeSkills(payload agentadaptor.ResolvedSkills, selected []string, resolved []agentadaptor.Skill, bindings []agentadaptor.EnvBinding) (agentadaptor.SkillSnapshot, error) {
	skillsHome := resolveClaudeSkillsHome(bindings)
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
		LocationLabel:          claudeSkillsLocationLabel(bindings),
		InstalledDetail:        "Installed by agent-adaptor in the Claude skills home.",
		MissingDetail:          "Configured but not currently linked into the Claude skills home.",
		ExternalConflictDetail: "Skill name is occupied by an external installation.",
		ExternalDetail:         "Installed outside agent-adaptor management in the Claude skills home.",
	}), nil
}

func syncClaudeSkills(ctx context.Context, payload agentadaptor.ResolvedSkills, selected []string, resolved []agentadaptor.Skill, bindings []agentadaptor.EnvBinding, kind agentadaptor.ProfileKind) (agentadaptor.SkillSnapshot, error) {
	skillsHome := resolveClaudeSkillsHome(bindings)
	pruneMode := skillruntime.ProfileSkillPruneBrokenManaged
	if kind == agentadaptor.ProfileKindHostManaged {
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
		return agentadaptor.SkillSnapshot{}, err
	}
	return listClaudeSkills(payload, selected, resolved, bindings)
}

// prepareClaudePromptBundle materializes the Selected skills into a per-run
// prompt bundle layout that Claude's CLI can discover via --add-dir.
func prepareClaudePromptBundle(agent agentadaptor.AgentIdentity, payload agentadaptor.ResolvedSkills) (string, string, error) {
	if len(payload.Entries) == 0 {
		return "", "", nil
	}
	root, err := os.UserConfigDir()
	if err != nil || strings.TrimSpace(root) == "" {
		root = skillruntime.ResolveHome(nil)
	}
	bundleKey := payload.Fingerprint
	bundleRoot := filepath.Join(root, "agent-adaptor", "claude-prompt-cache", safeNamespace(agent.TenantID), bundleKey)
	skillsHome := filepath.Join(bundleRoot, ".claude", "skills")
	if err := os.MkdirAll(skillsHome, 0o755); err != nil {
		return "", "", err
	}
	managedRoots := []string{skillruntime.ManagedSkillCacheRoot()}
	for _, entry := range payload.Entries {
		if strings.TrimSpace(entry.SourcePath) == "" {
			continue
		}
		if _, err := skillruntime.EnsureSkillTarget(entry.SourcePath, filepath.Join(skillsHome, entry.RuntimeName), managedRoots); err != nil {
			return "", "", fmt.Errorf("materialize Claude skill %q: %w", entry.Key, err)
		}
	}
	return bundleRoot, bundleKey, nil
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
