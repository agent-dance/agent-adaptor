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

func listCursorSkills(payload agentadaptor.SkillPayload, bindings []agentadaptor.EnvBinding) (agentadaptor.SkillSnapshot, error) {
	skillsHome := resolveCursorSkillsHome(bindings)
	installed, err := skillruntime.ReadInstalledSkillTargets(skillsHome)
	if err != nil {
		return agentadaptor.SkillSnapshot{}, err
	}
	return skillruntime.BuildPersistentSnapshot(skillruntime.PersistentSnapshotOptions{
		DriverType:             DriverType,
		Payload:                payload,
		Installed:              installed,
		SkillsHome:             skillsHome,
		LocationLabel:          cursorSkillsLocationLabel(bindings),
		InstalledDetail:        "Installed by agent-adaptor in the Cursor skills home.",
		MissingDetail:          "Configured but not currently linked into the Cursor skills home.",
		ExternalConflictDetail: "Skill name is occupied by an external installation.",
		ExternalDetail:         "Installed outside agent-adaptor management.",
	}), nil
}

func syncCursorSkills(_ context.Context, payload agentadaptor.SkillPayload, bindings []agentadaptor.EnvBinding, sink agentadaptor.EventSink) (agentadaptor.SkillSnapshot, error) {
	skillsHome := resolveCursorSkillsHome(bindings)
	if err := os.MkdirAll(skillsHome, 0o755); err != nil {
		return agentadaptor.SkillSnapshot{}, err
	}
	selected := skillruntime.SelectedRuntimeEntries(payload)
	installed, err := skillruntime.ReadInstalledSkillTargets(skillsHome)
	if err != nil {
		return agentadaptor.SkillSnapshot{}, err
	}
	availableByRuntime := map[string]agentadaptor.SkillRuntimeEntry{}
	desiredKeys := map[string]struct{}{}
	for _, key := range payload.Requested {
		desiredKeys[key] = struct{}{}
	}
	for _, entry := range payload.RuntimeEntries {
		availableByRuntime[entry.RuntimeName] = entry
	}
	for name, installedEntry := range installed {
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
		_ = sink.Emit(agentadaptor.RunEvent{
			Type: agentadaptor.RunEventLifecycle,
			Text: fmt.Sprintf("removed stale Cursor skill %q from %s", name, skillsHome),
		})
	}
	managedRoots := []string{skillruntime.ManagedSkillCacheRoot()}
	for _, entry := range selected {
		result, err := skillruntime.EnsureSkillTarget(entry.SourcePath, filepath.Join(skillsHome, entry.RuntimeName), managedRoots)
		if err != nil {
			return agentadaptor.SkillSnapshot{}, err
		}
		if result == "skipped" {
			continue
		}
		_ = sink.Emit(agentadaptor.RunEvent{
			Type: agentadaptor.RunEventLifecycle,
			Text: fmt.Sprintf("%s Cursor skill %q into %s", capitalize(result), entry.RuntimeName, skillsHome),
		})
	}
	return listCursorSkills(payload, bindings)
}

func runtimeNames(entries []agentadaptor.SkillRuntimeEntry) []string {
	out := make([]string, 0, len(entries))
	for _, entry := range entries {
		out = append(out, entry.RuntimeName)
	}
	return out
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
