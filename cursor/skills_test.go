package cursor

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	agentadaptor "github.com/agent-dance/agent-adaptor"
)

func createCursorSkillDir(t *testing.T, root, name string) string {
	t.Helper()
	skillDir := filepath.Join(root, name)
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("mkdir skill dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: "+name+"\n---\n"), 0o644); err != nil {
		t.Fatalf("write skill: %v", err)
	}
	return skillDir
}

func TestSyncCursorSkillsInstallsDesiredAndRemovesStaleManaged(t *testing.T) {
	t.Setenv("CURSOR_HOME", "")
	home := t.TempDir()
	sourceRoot := t.TempDir()
	mainSource := createCursorSkillDir(t, sourceRoot, "main")
	oldSource := createCursorSkillDir(t, sourceRoot, "old")
	skillsHome := filepath.Join(home, ".cursor", "skills")
	if err := os.MkdirAll(skillsHome, 0o755); err != nil {
		t.Fatalf("mkdir skills home: %v", err)
	}
	if err := os.Symlink(oldSource, filepath.Join(skillsHome, "old")); err != nil {
		t.Fatalf("seed stale symlink: %v", err)
	}

	payload := agentadaptor.SkillPayload{
		Requested: []string{"team/main"},
		RuntimeEntries: []agentadaptor.SkillRuntimeEntry{
			{Key: "team/main", RuntimeName: "main", SourcePath: mainSource},
			{Key: "team/old", RuntimeName: "old", SourcePath: oldSource},
		},
	}

	snapshot, err := syncCursorSkills(context.Background(), payload, []agentadaptor.EnvBinding{{Name: "HOME", Value: home}}, noopCursorSink{})
	if err != nil {
		t.Fatalf("sync skills: %v", err)
	}
	if _, err := os.Stat(filepath.Join(skillsHome, "main")); err != nil {
		t.Fatalf("expected desired skill to be installed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(skillsHome, "old")); !os.IsNotExist(err) {
		t.Fatalf("expected stale managed skill to be removed, got err=%v", err)
	}
	for _, entry := range snapshot.Entries {
		if entry.Key == "team/main" && entry.State != agentadaptor.SkillStateInstalled {
			t.Fatalf("expected main skill to be installed, got %#v", entry)
		}
		if entry.Key == "team/old" && entry.State != agentadaptor.SkillStateAvailable {
			t.Fatalf("expected old skill to become available after removal, got %#v", entry)
		}
	}
}

func TestSyncCursorSkillsPreservesExternalConflict(t *testing.T) {
	t.Setenv("CURSOR_HOME", "")
	home := t.TempDir()
	sourceRoot := t.TempDir()
	managedSource := createCursorSkillDir(t, sourceRoot, "main")
	externalRoot := t.TempDir()
	externalSource := createCursorSkillDir(t, externalRoot, "main")
	skillsHome := filepath.Join(home, ".cursor", "skills")
	if err := os.MkdirAll(skillsHome, 0o755); err != nil {
		t.Fatalf("mkdir skills home: %v", err)
	}
	if err := os.Symlink(externalSource, filepath.Join(skillsHome, "main")); err != nil {
		t.Fatalf("seed external symlink: %v", err)
	}

	payload := agentadaptor.SkillPayload{
		Requested: []string{"team/main"},
		RuntimeEntries: []agentadaptor.SkillRuntimeEntry{
			{Key: "team/main", RuntimeName: "main", SourcePath: managedSource},
		},
	}

	snapshot, err := syncCursorSkills(context.Background(), payload, []agentadaptor.EnvBinding{{Name: "HOME", Value: home}}, noopCursorSink{})
	if err != nil {
		t.Fatalf("sync skills: %v", err)
	}
	resolved, err := filepath.EvalSymlinks(filepath.Join(skillsHome, "main"))
	if err != nil {
		t.Fatalf("resolve external symlink: %v", err)
	}
	if filepath.Clean(resolved) != filepath.Clean(externalSource) {
		t.Fatalf("expected external symlink to remain, got %s", resolved)
	}
	if len(snapshot.Entries) != 1 || snapshot.Entries[0].State != agentadaptor.SkillStateExternal {
		t.Fatalf("expected external conflict snapshot, got %#v", snapshot.Entries)
	}
}

func TestSyncCursorSkillsUsesCursorHomeWhenProvided(t *testing.T) {
	cursorHome := t.TempDir()
	sourceRoot := t.TempDir()
	mainSource := createCursorSkillDir(t, sourceRoot, "main")
	payload := agentadaptor.SkillPayload{
		Requested: []string{"team/main"},
		RuntimeEntries: []agentadaptor.SkillRuntimeEntry{
			{Key: "team/main", RuntimeName: "main", SourcePath: mainSource},
		},
	}

	snapshot, err := syncCursorSkills(context.Background(), payload, []agentadaptor.EnvBinding{{Name: "CURSOR_HOME", Value: cursorHome}}, noopCursorSink{})
	if err != nil {
		t.Fatalf("sync skills: %v", err)
	}
	if _, err := os.Stat(filepath.Join(cursorHome, "skills", "main")); err != nil {
		t.Fatalf("expected desired skill to be installed in cursor home: %v", err)
	}
	if len(snapshot.Entries) != 1 || filepath.Clean(snapshot.Entries[0].TargetPath) != filepath.Clean(filepath.Join(cursorHome, "skills", "main")) {
		t.Fatalf("unexpected snapshot: %#v", snapshot.Entries)
	}
}
