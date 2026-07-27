package skill_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agent-dance/agent-adaptor/skill"
)

// makeSkillTree builds a small directory hierarchy with a mix of
// skill subdirectories, hidden entries, and stray files. Used by the
// LocalSkillsFromDir test cases.
func makeSkillTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	type entry struct {
		path  string
		isDir bool
		body  string // empty when dir
	}
	entries := []entry{
		{"code-review/SKILL.md", false, "# code-review"},
		{"code-review/example.md", false, "ex"},
		{"lint/SKILL.md", false, "# lint"},
		{"empty-dir/.placeholder", false, ""},    // missing SKILL.md
		{".hidden/SKILL.md", false, "# hidden"},  // hidden dir; should be skipped
		{"node_modules/SKILL.md", false, "# nm"}, // ignored by default? no — only via WithDirIgnore
		{"loose-file.md", false, "stray"},        // top-level file
	}
	for _, e := range entries {
		full := filepath.Join(root, e.path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir %q: %v", filepath.Dir(full), err)
		}
		if !e.isDir {
			if err := os.WriteFile(full, []byte(e.body), 0o644); err != nil {
				t.Fatalf("write %q: %v", full, err)
			}
		}
	}
	return root
}

func TestLocalSkillsFromDir_Happy(t *testing.T) {
	t.Parallel()
	root := makeSkillTree(t)

	skills, err := skill.LocalSkillsFromDir(root)
	if err != nil {
		t.Fatalf("LocalSkillsFromDir: %v", err)
	}
	keys := keysOfSkills(skills)
	want := []string{"code-review", "lint", "node_modules"}
	if !equalUnorderedStrings(keys, want) {
		t.Fatalf("keys: got %v, want %v", keys, want)
	}
	for _, s := range skills {
		path, ok := s.Source.(skill.PathSource)
		if !ok {
			t.Fatalf("skill %q source: want SkillFromPath, got %T", s.Key, s.Source)
		}
		if !strings.HasPrefix(path.Path, root) {
			t.Errorf("skill %q path %q should be under root %q", s.Key, path.Path, root)
		}
	}
	// Defensive: explicit assertions for entries the scan must skip,
	// alongside the equalUnorderedStrings check above.
	for _, key := range keys {
		if strings.HasPrefix(key, ".") {
			t.Errorf("hidden entry leaked into result: %q", key)
		}
		if key == "empty-dir" {
			t.Errorf("subdir without SKILL.md leaked: %q", key)
		}
	}
}

func TestLocalSkillsFromDir_Prefix(t *testing.T) {
	t.Parallel()
	root := makeSkillTree(t)

	skills, err := skill.LocalSkillsFromDir(root, skill.WithDirSkillKeyPrefix("team"))
	if err != nil {
		t.Fatalf("LocalSkillsFromDir: %v", err)
	}
	for _, s := range skills {
		if !strings.HasPrefix(s.Key, "team/") {
			t.Errorf("expected prefixed key, got %q", s.Key)
		}
	}
}

func TestLocalSkillsFromDir_Ignore(t *testing.T) {
	t.Parallel()
	root := makeSkillTree(t)

	skills, err := skill.LocalSkillsFromDir(root, skill.WithDirIgnore("node_modules"))
	if err != nil {
		t.Fatalf("LocalSkillsFromDir: %v", err)
	}
	for _, s := range skills {
		if s.Key == "node_modules" {
			t.Errorf("ignored entry leaked into result: %#v", s)
		}
	}
}

func TestLocalSkillsFromDir_CustomMarkerFile(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	// Two subdirs: one with SKILL.md, one with AGENT.md.
	if err := os.MkdirAll(filepath.Join(root, "skill-style"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "skill-style", "SKILL.md"), []byte("a"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, "agent-style"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "agent-style", "AGENT.md"), []byte("b"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	skills, err := skill.LocalSkillsFromDir(root, skill.WithDirSkillFile("AGENT.md"))
	if err != nil {
		t.Fatalf("LocalSkillsFromDir: %v", err)
	}
	if len(skills) != 1 || skills[0].Key != "agent-style" {
		t.Fatalf("with custom marker AGENT.md, want [agent-style], got %#v", skills)
	}
}

func TestLocalSkillsFromDir_RootMustExist(t *testing.T) {
	t.Parallel()
	_, err := skill.LocalSkillsFromDir("/this/definitely/does/not/exist")
	if err == nil {
		t.Fatal("expected error for non-existent root, got nil")
	}
}

func TestLocalSkillsFromDir_RootMustBeDir(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	file := filepath.Join(tmp, "not-a-dir.txt")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, err := skill.LocalSkillsFromDir(file)
	if err == nil {
		t.Fatal("expected error when root is a file, got nil")
	}
}

func TestLocalSkillsFromDir_EmptyRoot(t *testing.T) {
	t.Parallel()
	_, err := skill.LocalSkillsFromDir("")
	if err == nil {
		t.Fatal("expected error for empty root, got nil")
	}
	if !strings.Contains(err.Error(), "empty root") {
		t.Errorf("error should mention empty root, got %q", err.Error())
	}
}

func TestSkillsAsRefs_Round(t *testing.T) {
	t.Parallel()
	skills := []skill.Skill{
		{Key: "a", Source: skill.InlineSource{SkillMD: "# a"}},
		{Key: "b", Source: skill.InlineSource{SkillMD: "# b"}},
	}
	refs := skill.SkillsAsRefs(skills)
	if len(refs) != 2 {
		t.Fatalf("want 2 refs, got %d", len(refs))
	}
	for i, ref := range refs {
		s, ok := ref.(skill.Skill)
		if !ok {
			t.Errorf("ref[%d] should be Skill, got %T", i, ref)
		} else if s.Key != skills[i].Key {
			t.Errorf("ref[%d] key: got %q, want %q", i, s.Key, skills[i].Key)
		}
	}
}

func TestSkillsAsRefs_Empty(t *testing.T) {
	t.Parallel()
	if got := skill.SkillsAsRefs(nil); got != nil {
		t.Errorf("SkillsAsRefs(nil) = %v, want nil", got)
	}
	if got := skill.SkillsAsRefs([]skill.Skill{}); got != nil {
		t.Errorf("SkillsAsRefs(empty) = %v, want nil", got)
	}
}

// keysOfSkills extracts skill keys for assertions.
func keysOfSkills(skills []skill.Skill) []string {
	out := make([]string, 0, len(skills))
	for _, s := range skills {
		out = append(out, s.Key)
	}
	return out
}

// equalUnorderedStrings reports whether two string slices contain
// the same elements regardless of order.
func equalUnorderedStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	want := make(map[string]int, len(b))
	for _, s := range b {
		want[s]++
	}
	for _, s := range a {
		want[s]--
		if want[s] < 0 {
			return false
		}
	}
	return true
}
