package profilereconcile

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/agent-dance/agent-adaptor/internal/profilestate"
)

func TestReconcileDirectoryWritesUpdatesAndPrunesManagedFiles(t *testing.T) {
	root := t.TempDir()
	manifest := mustManifest(t, root)

	snapshot, err := ReconcileDirectory(DirectoryOptions{
		Root:     root,
		Kind:     "agents",
		Manifest: &manifest,
		Entries: []DirectoryEntry{
			{Key: "reviewer", RuntimeName: "reviewer.md", Content: "review v1"},
			{Key: "planner", RuntimeName: "planner.md", Content: "plan"},
		},
		AllowPrune: true,
	})
	if err != nil {
		t.Fatalf("initial reconcile: %v", err)
	}
	if !sameStrings(snapshot.Managed, []string{"planner", "reviewer"}) {
		t.Fatalf("unexpected managed entries: %#v", snapshot)
	}
	if raw := readFile(t, filepath.Join(root, "reviewer.md")); raw != "review v1" {
		t.Fatalf("unexpected file content: %q", raw)
	}

	snapshot, err = ReconcileDirectory(DirectoryOptions{
		Root:       root,
		Kind:       "agents",
		Manifest:   &manifest,
		Entries:    []DirectoryEntry{{Key: "reviewer", RuntimeName: "reviewer.md", Content: "review v2"}},
		AllowPrune: true,
	})
	if err != nil {
		t.Fatalf("second reconcile: %v", err)
	}
	if !sameStrings(snapshot.Managed, []string{"reviewer"}) {
		t.Fatalf("unexpected managed entries after prune: %#v", snapshot)
	}
	if raw := readFile(t, filepath.Join(root, "reviewer.md")); raw != "review v2" {
		t.Fatalf("unexpected updated content: %q", raw)
	}
	if _, err := os.Stat(filepath.Join(root, "planner.md")); !os.IsNotExist(err) {
		t.Fatalf("expected stale managed file pruned, got %v", err)
	}
}

func TestReconcileDirectoryPrunesOldPathWhenRuntimeNameChanges(t *testing.T) {
	root := t.TempDir()
	manifest := mustManifest(t, root)

	if _, err := ReconcileDirectory(DirectoryOptions{
		Root:       root,
		Kind:       "agents",
		Manifest:   &manifest,
		Entries:    []DirectoryEntry{{Key: "reviewer", RuntimeName: "reviewer.md", Content: "review"}},
		AllowPrune: true,
	}); err != nil {
		t.Fatalf("initial reconcile: %v", err)
	}

	snapshot, err := ReconcileDirectory(DirectoryOptions{
		Root:       root,
		Kind:       "agents",
		Manifest:   &manifest,
		Entries:    []DirectoryEntry{{Key: "reviewer", RuntimeName: "security.md", Content: "security"}},
		AllowPrune: true,
	})
	if err != nil {
		t.Fatalf("rename reconcile: %v", err)
	}
	if !sameStrings(snapshot.Managed, []string{"reviewer"}) {
		t.Fatalf("unexpected managed entries after rename: %#v", snapshot)
	}
	if _, err := os.Stat(filepath.Join(root, "reviewer.md")); !os.IsNotExist(err) {
		t.Fatalf("expected old runtime path pruned, got %v", err)
	}
	if raw := readFile(t, filepath.Join(root, "security.md")); raw != "security" {
		t.Fatalf("unexpected renamed content: %q", raw)
	}
}

func TestReconcileDirectoryRejectsExternalConflict(t *testing.T) {
	root := t.TempDir()
	manifest := mustManifest(t, root)
	if err := os.WriteFile(filepath.Join(root, "reviewer.md"), []byte("external"), 0o644); err != nil {
		t.Fatalf("write external: %v", err)
	}

	_, err := ReconcileDirectory(DirectoryOptions{
		Root:     root,
		Kind:     "agents",
		Manifest: &manifest,
		Entries:  []DirectoryEntry{{Key: "reviewer", RuntimeName: "reviewer.md", Content: "managed"}},
	})
	if err == nil {
		t.Fatal("expected external conflict")
	}
}

func TestReconcileDirectoryCopiesSourceFileAndReportsExternalEntries(t *testing.T) {
	root := t.TempDir()
	sourceRoot := t.TempDir()
	source := filepath.Join(sourceRoot, "security.md")
	if err := os.WriteFile(source, []byte("security"), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "external.md"), []byte("external"), 0o644); err != nil {
		t.Fatalf("write external: %v", err)
	}
	manifest := mustManifest(t, root)

	snapshot, err := ReconcileDirectory(DirectoryOptions{
		Root:     root,
		Kind:     "agents",
		Manifest: &manifest,
		Entries:  []DirectoryEntry{{Key: "security", RuntimeName: "security.md", SourcePath: source}},
	})
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if !sameStrings(snapshot.Managed, []string{"security"}) || !sameStrings(snapshot.External, []string{"external.md"}) {
		t.Fatalf("unexpected snapshot: %#v", snapshot)
	}
	if raw := readFile(t, filepath.Join(root, "security.md")); raw != "security" {
		t.Fatalf("unexpected copied content: %q", raw)
	}
}

func TestReconcileDirectoryCopiesSourceDirectory(t *testing.T) {
	root := t.TempDir()
	sourceRoot := t.TempDir()
	source := filepath.Join(sourceRoot, "agent")
	if err := os.MkdirAll(filepath.Join(source, "nested"), 0o755); err != nil {
		t.Fatalf("mkdir source: %v", err)
	}
	if err := os.WriteFile(filepath.Join(source, "one.md"), []byte("one"), 0o644); err != nil {
		t.Fatalf("write source one: %v", err)
	}
	if err := os.WriteFile(filepath.Join(source, "nested", "two.md"), []byte("two"), 0o644); err != nil {
		t.Fatalf("write source two: %v", err)
	}
	manifest := mustManifest(t, root)

	if _, err := ReconcileDirectory(DirectoryOptions{
		Root:     root,
		Kind:     "agents",
		Manifest: &manifest,
		Entries:  []DirectoryEntry{{Key: "directory-agent", RuntimeName: "directory-agent", SourcePath: source}},
	}); err != nil {
		t.Fatalf("reconcile source directory: %v", err)
	}
	if raw := readFile(t, filepath.Join(root, "directory-agent", "one.md")); raw != "one" {
		t.Fatalf("unexpected copied root file: %q", raw)
	}
	if raw := readFile(t, filepath.Join(root, "directory-agent", "nested", "two.md")); raw != "two" {
		t.Fatalf("unexpected copied nested file: %q", raw)
	}
}

func mustManifest(t *testing.T, root string) profilestate.Manifest {
	t.Helper()
	manifest, err := profilestate.LoadManifest(root)
	if err != nil {
		t.Fatalf("load manifest: %v", err)
	}
	return manifest
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(raw)
}

func sameStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
