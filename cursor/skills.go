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

func syncCursorSkills(ctx context.Context, payload agentadaptor.ResolvedSkills, selected []string, resolved []agentadaptor.Skill, bindings []agentadaptor.EnvBinding, sink agentadaptor.EventSink) (agentadaptor.SkillSnapshot, error) {
	skillsHome := resolveCursorSkillsHome(bindings)
	result, err := skillruntime.ReconcileProfileSkills(ctx, skillruntime.ProfileSkillReconcileOptions{
		ProfileDir:              resolveCursorHome(bindings),
		SkillsHome:              skillsHome,
		Payload:                 payload,
		Selected:                selected,
		ManagedRoots:            []string{skillruntime.ManagedSkillCacheRoot()},
		ConflictMode:            skillruntime.ProfileSkillConflictPreserve,
		PruneMode:               skillruntime.ProfileSkillPruneManaged,
		PruneMatchingUnselected: true,
	})
	if err != nil {
		return agentadaptor.SkillSnapshot{}, err
	}
	for _, change := range result.Changes {
		if sink == nil {
			continue
		}
		switch change.Action {
		case "removed":
			_ = sink.Emit(agentadaptor.RunEvent{
				Type: agentadaptor.RunEventLifecycle,
				Text: fmt.Sprintf("removed stale Cursor skill %q from %s", change.RuntimeName, skillsHome),
			})
		case "created", "repaired":
			_ = sink.Emit(agentadaptor.RunEvent{
				Type: agentadaptor.RunEventLifecycle,
				Text: fmt.Sprintf("%s Cursor skill %q into %s", capitalize(change.Action), change.RuntimeName, skillsHome),
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
