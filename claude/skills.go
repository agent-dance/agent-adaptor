package claude

import (
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

func syncClaudeSkills(payload agentadaptor.ResolvedSkills, selected []string, resolved []agentadaptor.Skill, bindings []agentadaptor.EnvBinding, kind agentadaptor.ProfileKind) (agentadaptor.SkillSnapshot, error) {
	skillsHome := resolveClaudeSkillsHome(bindings)
	if err := os.MkdirAll(skillsHome, 0o755); err != nil {
		return agentadaptor.SkillSnapshot{}, err
	}
	installed, err := skillruntime.ReadInstalledSkillTargets(skillsHome)
	if err != nil {
		return agentadaptor.SkillSnapshot{}, err
	}
	desiredKeys := map[string]struct{}{}
	for _, key := range selected {
		desiredKeys[key] = struct{}{}
	}
	cacheRoots := []string{skillruntime.ManagedSkillCacheRoot()}
	allowedRuntimeNames := make([]string, 0, len(payload.Entries))
	for _, entry := range payload.Entries {
		if _, desired := desiredKeys[entry.Key]; !desired {
			continue
		}
		allowedRuntimeNames = append(allowedRuntimeNames, entry.RuntimeName)
		if strings.TrimSpace(entry.SourcePath) == "" {
			continue
		}
		if existing, ok := installed[entry.RuntimeName]; ok && filepath.Clean(existing.TargetPath) != filepath.Clean(entry.SourcePath) && !installedSkillTargetWithinRoots(existing, cacheRoots) {
			return agentadaptor.SkillSnapshot{}, fmt.Errorf("materialize Claude skill %q: runtime name %q is occupied by external installation %q", entry.Key, entry.RuntimeName, existing.TargetPath)
		}
		if _, err := skillruntime.EnsureSkillTarget(entry.SourcePath, filepath.Join(skillsHome, entry.RuntimeName), cacheRoots); err != nil {
			return agentadaptor.SkillSnapshot{}, fmt.Errorf("materialize Claude skill %q: %w", entry.Key, err)
		}
	}
	var removed []string
	if kind == agentadaptor.ProfileKindHostManaged {
		removed, err = skillruntime.RemoveManagedSkillTargets(skillsHome, allowedRuntimeNames, cacheRoots)
	} else {
		removed, err = skillruntime.PruneBrokenManagedSkillTargets(skillsHome, allowedRuntimeNames, cacheRoots)
	}
	if err != nil {
		return agentadaptor.SkillSnapshot{}, err
	}
	_ = removed
	return listClaudeSkills(payload, selected, resolved, bindings)
}

func installedSkillTargetWithinRoots(target skillruntime.InstalledSkillTarget, roots []string) bool {
	targetPath := strings.TrimSpace(target.TargetPath)
	if targetPath == "" {
		return false
	}
	targetPath = filepath.Clean(targetPath)
	for _, root := range roots {
		root = strings.TrimSpace(root)
		if root == "" {
			continue
		}
		root = filepath.Clean(root)
		if targetPath == root || strings.HasPrefix(targetPath, root+string(filepath.Separator)) {
			return true
		}
	}
	return false
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
