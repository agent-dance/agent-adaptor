package cursor

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	agentadaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/internal/skillruntime"
)

func resolveCursorSkillsHome(bindings []agentadaptor.EnvBinding) string {
	return filepath.Join(resolveCursorHome(bindings), "skills")
}

func cursorSkillsLocationLabel(bindings []agentadaptor.EnvBinding) string {
	if skillruntime.ResolveBinding(bindings, "CURSOR_HOME") != "" || strings.TrimSpace(os.Getenv("CURSOR_HOME")) != "" {
		return resolveCursorSkillsHome(bindings)
	}
	return "~/.cursor/skills"
}

func listCursorSkills(payload agentadaptor.ResolvedSkills, selected []string, resolved []agentadaptor.Skill, bindings []agentadaptor.EnvBinding) (agentadaptor.SkillSnapshot, error) {
	skillsHome := resolveCursorSkillsHome(bindings)
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
		LocationLabel:          cursorSkillsLocationLabel(bindings),
		InstalledDetail:        "Installed by agent-adaptor in the Cursor skills home.",
		MissingDetail:          "Configured but not currently linked into the Cursor skills home.",
		ExternalConflictDetail: "Skill name is occupied by an external installation.",
		ExternalDetail:         "Installed outside agent-adaptor management.",
	}), nil
}

func syncCursorSkills(_ context.Context, payload agentadaptor.ResolvedSkills, selected []string, resolved []agentadaptor.Skill, bindings []agentadaptor.EnvBinding, sink agentadaptor.EventSink) (agentadaptor.SkillSnapshot, error) {
	skillsHome := resolveCursorSkillsHome(bindings)
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

	// Compute the allowed runtime-name set: only entries whose Key is
	// still desired stay on disk. We use payload.Entries (the subset
	// that successfully materialised) to derive the Key → RuntimeName
	// mapping; entries that failed to materialise fall through to the
	// "selected but missing" branch of listCursorSkills below.
	allowedRuntimeNames := make([]string, 0, len(payload.Entries))
	availableByRuntime := make(map[string]agentadaptor.ResolvedSkill, len(payload.Entries))
	for _, entry := range payload.Entries {
		availableByRuntime[entry.RuntimeName] = entry
		if _, desired := desiredKeys[entry.Key]; !desired {
			continue
		}
		allowedRuntimeNames = append(allowedRuntimeNames, entry.RuntimeName)
	}

	// Step A: remove every cache-root managed entry (symlink into the
	// SDK cache, or marker-tagged directory with a cache-root target)
	// that is no longer on the allow-list. This covers both
	// "deselected skill still in catalog" and "skill dropped from
	// catalog entirely" provided the managed target lives in the
	// materialiser's cache root.
	removed, err := skillruntime.RemoveManagedSkillTargets(skillsHome, allowedRuntimeNames, cacheRoots)
	if err != nil {
		return agentadaptor.SkillSnapshot{}, err
	}

	// Step B: handle entries whose underlying source is host-supplied
	// (SkillFromPath) and therefore lives outside the SDK cache root.
	// We only touch them when the current catalog still knows the
	// runtime name AND its source matches the installed symlink target
	// — i.e. we are sure we created this link in an earlier sync and
	// the host has now deselected the skill. Symlinks pointing to
	// arbitrary user paths we never recorded are intentionally left
	// alone.
	removedSet := map[string]struct{}{}
	for _, name := range removed {
		removedSet[name] = struct{}{}
	}
	for name, installedEntry := range installed {
		if _, already := removedSet[name]; already {
			continue
		}
		available, ok := availableByRuntime[name]
		if !ok {
			continue
		}
		if _, desired := desiredKeys[available.Key]; desired {
			continue
		}
		if filepath.Clean(installedEntry.TargetPath) != filepath.Clean(available.SourcePath) {
			continue
		}
		if err := os.RemoveAll(filepath.Join(skillsHome, name)); err != nil {
			return agentadaptor.SkillSnapshot{}, err
		}
		removed = append(removed, name)
	}

	for _, name := range removed {
		if sink != nil {
			_ = sink.Emit(agentadaptor.RunEvent{
				Type: agentadaptor.RunEventLifecycle,
				Text: fmt.Sprintf("removed stale Cursor skill %q from %s", name, skillsHome),
			})
		}
	}

	for _, entry := range payload.Entries {
		if _, desired := desiredKeys[entry.Key]; !desired {
			continue
		}
		if strings.TrimSpace(entry.SourcePath) == "" {
			continue
		}
		result, err := skillruntime.EnsureSkillTarget(entry.SourcePath, filepath.Join(skillsHome, entry.RuntimeName), cacheRoots)
		if err != nil {
			return agentadaptor.SkillSnapshot{}, err
		}
		if result == "skipped" {
			continue
		}
		if sink != nil {
			_ = sink.Emit(agentadaptor.RunEvent{
				Type: agentadaptor.RunEventLifecycle,
				Text: fmt.Sprintf("%s Cursor skill %q into %s", capitalize(result), entry.RuntimeName, skillsHome),
			})
		}
	}
	return listCursorSkills(payload, selected, resolved, bindings)
}

func capitalize(value string) string {
	if value == "" {
		return value
	}
	return strings.ToUpper(value[:1]) + value[1:]
}

type noopCursorSink struct{}

func (noopCursorSink) Emit(agentadaptor.RunEvent) error            { return nil }
func (noopCursorSink) EmitStream(agentadaptor.StreamPayload) error { return nil }
