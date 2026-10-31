package skillruntime

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/agent-dance/agent-adaptor/driver"
)

func TestProfileSkillsSharedOwnershipAndMissingTarget(t *testing.T) {
	ctx := context.Background()
	profile, source := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "SKILL.md"), []byte("proof"), 0600); err != nil {
		t.Fatal(err)
	}
	payload := driver.ResolvedSkills{Entries: []driver.ResolvedSkill{{Key: "team/proof", RuntimeName: "proof", SourcePath: source}}}
	if _, err := SyncProfileSkills(ctx, "test", profile, driver.ProfileKindShared, payload, payload.Keys(), nil); err != nil {
		t.Fatal(err)
	}
	snapshot, err := SyncProfileSkills(ctx, "test", profile, driver.ProfileKindShared, driver.ResolvedSkills{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Entries) != 1 || snapshot.Entries[0].Key != "team/proof" || !snapshot.Entries[0].Managed || snapshot.Entries[0].State != driver.SkillStateInstalled {
		t.Fatalf("shared profile discarded managed ownership: %+v", snapshot)
	}
	// Deleting the native SKILL.md can leave a valid ownership marker or a
	// link whose source no longer has a skill. Neither proves installation.
	if err := os.Remove(filepath.Join(profile, "skills", "proof", "SKILL.md")); err != nil {
		t.Fatal(err)
	}
	snapshot, err = ListProfileSkills("test", profile, payload, payload.Keys(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Entries) != 1 || snapshot.Entries[0].State != driver.SkillStateMissing {
		t.Fatalf("missing SKILL.md reported installed: %+v", snapshot)
	}
}
