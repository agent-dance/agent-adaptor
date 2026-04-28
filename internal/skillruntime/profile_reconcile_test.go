package skillruntime

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	agentadaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/internal/profilestate"
)

func TestReconcileProfileSkillsWritesManagedManifestWithoutSourceLeak(t *testing.T) {
	profileDir := t.TempDir()
	sourceRoot := t.TempDir()
	source := createProfileSkillDir(t, sourceRoot, "secret-skill-source", "top-secret-inline-body")

	_, err := ReconcileProfileSkills(context.Background(), ProfileSkillReconcileOptions{
		ProfileDir:   profileDir,
		SkillsHome:   filepath.Join(profileDir, "skills"),
		Payload:      agentadaptor.ResolvedSkills{Entries: []agentadaptor.ResolvedSkill{{Key: "team/main", RuntimeName: "main", SourcePath: source}}},
		Selected:     []string{"team/main"},
		ConflictMode: ProfileSkillConflictPreserve,
		PruneMode:    ProfileSkillPruneManaged,
	})
	if err != nil {
		t.Fatalf("reconcile skills: %v", err)
	}

	manifest, err := profilestate.LoadManifest(profileDir)
	if err != nil {
		t.Fatalf("load manifest: %v", err)
	}
	entry, ok := manifest.Entry(profileSkillManifestKind, "team/main")
	if !ok {
		t.Fatalf("expected managed manifest entry, got %#v", manifest.Entries)
	}
	if entry.SourcePath != "" {
		t.Fatalf("expected source path to stay out of manifest, got %#v", entry)
	}
	if entry.Metadata[manifestSourceHashKey] == "" {
		t.Fatalf("expected hashed source metadata, got %#v", entry)
	}
	if filepath.Clean(entry.Path) != filepath.Clean(filepath.Join(profileDir, "skills", "main")) {
		t.Fatalf("unexpected manifest path: %#v", entry)
	}

	raw, err := os.ReadFile(filepath.Join(profileDir, profilestate.ManifestName))
	if err != nil {
		t.Fatalf("read manifest file: %v", err)
	}
	text := string(raw)
	if strings.Contains(text, source) || strings.Contains(text, "secret-skill-source") || strings.Contains(text, "top-secret-inline-body") {
		t.Fatalf("manifest leaked source details: %s", text)
	}
}

func TestReconcileProfileSkillsPrunesDeselectedManagedSkillOutsideManagedRoots(t *testing.T) {
	profileDir := t.TempDir()
	skillsHome := filepath.Join(profileDir, "skills")
	source := createProfileSkillDir(t, t.TempDir(), "analysis", "analysis")

	if _, err := ReconcileProfileSkills(context.Background(), ProfileSkillReconcileOptions{
		ProfileDir:   profileDir,
		SkillsHome:   skillsHome,
		Payload:      agentadaptor.ResolvedSkills{Entries: []agentadaptor.ResolvedSkill{{Key: "team/analysis", RuntimeName: "analysis", SourcePath: source}}},
		Selected:     []string{"team/analysis"},
		ConflictMode: ProfileSkillConflictPreserve,
		PruneMode:    ProfileSkillPruneManaged,
	}); err != nil {
		t.Fatalf("initial reconcile: %v", err)
	}
	if _, err := os.Stat(filepath.Join(skillsHome, "analysis")); err != nil {
		t.Fatalf("expected managed skill installed: %v", err)
	}

	result, err := ReconcileProfileSkills(context.Background(), ProfileSkillReconcileOptions{
		ProfileDir:   profileDir,
		SkillsHome:   skillsHome,
		Payload:      agentadaptor.ResolvedSkills{},
		Selected:     []string{},
		ConflictMode: ProfileSkillConflictPreserve,
		PruneMode:    ProfileSkillPruneManaged,
	})
	if err != nil {
		t.Fatalf("prune reconcile: %v", err)
	}
	if _, err := os.Stat(filepath.Join(skillsHome, "analysis")); !os.IsNotExist(err) {
		t.Fatalf("expected deselected managed skill pruned, err=%v", err)
	}
	if len(result.Changes) != 1 || result.Changes[0].Action != "removed" || result.Changes[0].RuntimeName != "analysis" {
		t.Fatalf("unexpected prune result: %#v", result.Changes)
	}

	manifest, err := profilestate.LoadManifest(profileDir)
	if err != nil {
		t.Fatalf("load manifest: %v", err)
	}
	if _, ok := manifest.Entry(profileSkillManifestKind, "team/analysis"); ok {
		t.Fatalf("expected pruned skill removed from manifest, got %#v", manifest.Entries)
	}
}

func TestReconcileProfileSkillsPreservesExternalConflict(t *testing.T) {
	profileDir := t.TempDir()
	skillsHome := filepath.Join(profileDir, "skills")
	managedSource := createProfileSkillDir(t, t.TempDir(), "managed-main", "managed")
	externalSource := createProfileSkillDir(t, t.TempDir(), "external-main", "external")
	if err := os.MkdirAll(skillsHome, 0o755); err != nil {
		t.Fatalf("mkdir skills home: %v", err)
	}
	if err := os.Symlink(externalSource, filepath.Join(skillsHome, "main")); err != nil {
		t.Fatalf("seed external link: %v", err)
	}

	if _, err := ReconcileProfileSkills(context.Background(), ProfileSkillReconcileOptions{
		ProfileDir:   profileDir,
		SkillsHome:   skillsHome,
		Payload:      agentadaptor.ResolvedSkills{Entries: []agentadaptor.ResolvedSkill{{Key: "team/main", RuntimeName: "main", SourcePath: managedSource}}},
		Selected:     []string{"team/main"},
		ConflictMode: ProfileSkillConflictPreserve,
		PruneMode:    ProfileSkillPruneManaged,
	}); err != nil {
		t.Fatalf("reconcile preserve conflict: %v", err)
	}

	resolved, err := filepath.EvalSymlinks(filepath.Join(skillsHome, "main"))
	if err != nil {
		t.Fatalf("resolve external target: %v", err)
	}
	if filepath.Clean(resolved) != filepath.Clean(externalSource) {
		t.Fatalf("expected external target preserved, got %s", resolved)
	}
	manifest, err := profilestate.LoadManifest(profileDir)
	if err != nil {
		t.Fatalf("load manifest: %v", err)
	}
	if _, ok := manifest.Entry(profileSkillManifestKind, "team/main"); ok {
		t.Fatalf("expected conflicting external skill to stay unmanaged, got %#v", manifest.Entries)
	}
}

func TestReconcileProfileSkillsRejectsExternalConflictWhenRequested(t *testing.T) {
	profileDir := t.TempDir()
	skillsHome := filepath.Join(profileDir, "skills")
	managedSource := createProfileSkillDir(t, t.TempDir(), "managed-review", "managed")
	externalSource := createProfileSkillDir(t, t.TempDir(), "external-review", "external")
	if err := os.MkdirAll(skillsHome, 0o755); err != nil {
		t.Fatalf("mkdir skills home: %v", err)
	}
	if err := os.Symlink(externalSource, filepath.Join(skillsHome, "review")); err != nil {
		t.Fatalf("seed external link: %v", err)
	}

	_, err := ReconcileProfileSkills(context.Background(), ProfileSkillReconcileOptions{
		ProfileDir:   profileDir,
		SkillsHome:   skillsHome,
		Payload:      agentadaptor.ResolvedSkills{Entries: []agentadaptor.ResolvedSkill{{Key: "team/review", RuntimeName: "review", SourcePath: managedSource}}},
		Selected:     []string{"team/review"},
		ConflictMode: ProfileSkillConflictError,
		PruneMode:    ProfileSkillPruneManaged,
	})
	if err == nil {
		t.Fatal("expected external conflict error")
	}

	resolved, err := filepath.EvalSymlinks(filepath.Join(skillsHome, "review"))
	if err != nil {
		t.Fatalf("resolve external target: %v", err)
	}
	if filepath.Clean(resolved) != filepath.Clean(externalSource) {
		t.Fatalf("expected external target preserved, got %s", resolved)
	}
}

func createProfileSkillDir(t *testing.T, root, name, body string) string {
	t.Helper()
	skillDir := filepath.Join(root, name)
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("mkdir skill dir: %v", err)
	}
	content := "---\nname: " + name + "\n---\n" + body + "\n"
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatalf("write skill: %v", err)
	}
	return skillDir
}
