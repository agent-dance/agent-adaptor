package cursor

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/skillruntime"
)

func resolveCursorSkillsHome(bindings []driver.EnvBinding) string {
	return filepath.Join(resolveCursorHome(bindings), "skills")
}

func cursorSkillsLocationLabel(bindings []driver.EnvBinding) string {
	if skillruntime.ResolveBinding(bindings, "CURSOR_HOME") != "" || strings.TrimSpace(os.Getenv("CURSOR_HOME")) != "" {
		return resolveCursorSkillsHome(bindings)
	}
	return "~/.cursor/skills"
}

func listCursorSkills(payload driver.ResolvedSkills, selected []string, resolved []driver.Skill, bindings []driver.EnvBinding) (driver.SkillSnapshot, error) {
	skillsHome := resolveCursorSkillsHome(bindings)
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
		LocationLabel:          cursorSkillsLocationLabel(bindings),
		InstalledDetail:        "Installed by agent-adaptor in the Cursor skills home.",
		MissingDetail:          "Configured but not currently linked into the Cursor skills home.",
		ExternalConflictDetail: "Skill name is occupied by an external installation.",
		ExternalDetail:         "Installed outside agent-adaptor management.",
	}), nil
}

func syncCursorSkills(ctx context.Context, payload driver.ResolvedSkills, selected []string, resolved []driver.Skill, bindings []driver.EnvBinding, sink driver.EventSink) (driver.SkillSnapshot, error) {
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
		return driver.SkillSnapshot{}, err
	}
	for _, change := range result.Changes {
		if sink == nil {
			continue
		}
		switch change.Action {
		case "removed":
			_ = sink.Emit(driver.RunEvent{
				Type: driver.RunEventLifecycle,
				Text: fmt.Sprintf("removed stale Cursor skill %q from %s", change.RuntimeName, skillsHome),
			})
		case "created", "repaired":
			_ = sink.Emit(driver.RunEvent{
				Type: driver.RunEventLifecycle,
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

func (noopCursorSink) Emit(driver.RunEvent) error            { return nil }
func (noopCursorSink) EmitStream(driver.StreamPayload) error { return nil }
