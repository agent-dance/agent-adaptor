package codex

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	agentadaptor "github.com/agent-dance/agent-adaptor"
)

type eventSink struct {
	events []agentadaptor.RunEvent
}

func (s *eventSink) Emit(event agentadaptor.RunEvent) error {
	s.events = append(s.events, event)
	return nil
}

func (s *eventSink) EmitStream(agentadaptor.StreamPayload) error { return nil }

func createSkillDir(t *testing.T, root, name string) string {
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

func TestInjectCodexSkillsRepairsManagedSymlink(t *testing.T) {
	testRoot := filepath.Join(t.TempDir(), "codex-test", t.Name())
	t.Setenv("AGENT_ADAPTOR_SKILL_CACHE_ROOT", filepath.Dir(filepath.Dir(testRoot)))
	oldSource := createSkillDir(t, testRoot, "paperclip-old")
	currentSource := createSkillDir(t, testRoot, "paperclip-current")
	codexHome := t.TempDir()
	skillsHome := filepath.Join(codexHome, "skills")
	if err := os.MkdirAll(skillsHome, 0o755); err != nil {
		t.Fatalf("mkdir skills home: %v", err)
	}
	if err := os.Symlink(oldSource, filepath.Join(skillsHome, "paperclip")); err != nil {
		t.Fatalf("seed symlink: %v", err)
	}

	payload := agentadaptor.ResolvedSkills{
		Entries: []agentadaptor.ResolvedSkill{
			{Key: "team/paperclip", RuntimeName: "paperclip", SourcePath: currentSource},
		},
	}
	sink := &eventSink{}
	if err := injectCodexSkills(context.Background(), payload, codexHome, sink); err != nil {
		t.Fatalf("inject: %v", err)
	}

	resolved, err := filepath.EvalSymlinks(filepath.Join(skillsHome, "paperclip"))
	if err != nil {
		t.Fatalf("resolve symlink: %v", err)
	}
	expected, err := filepath.EvalSymlinks(currentSource)
	if err != nil {
		t.Fatalf("resolve expected source: %v", err)
	}
	if filepath.Clean(resolved) != filepath.Clean(expected) {
		t.Fatalf("expected repaired symlink to %s, got %s", expected, resolved)
	}
}

func TestInjectCodexSkillsPreservesExternalSymlink(t *testing.T) {
	testRoot := filepath.Join(t.TempDir(), "codex-test", t.Name())
	t.Setenv("AGENT_ADAPTOR_SKILL_CACHE_ROOT", filepath.Dir(filepath.Dir(testRoot)))
	currentSource := createSkillDir(t, testRoot, "paperclip-current")
	externalRoot := t.TempDir()
	externalSource := createSkillDir(t, externalRoot, "paperclip-custom")
	codexHome := t.TempDir()
	skillsHome := filepath.Join(codexHome, "skills")
	if err := os.MkdirAll(skillsHome, 0o755); err != nil {
		t.Fatalf("mkdir skills home: %v", err)
	}
	if err := os.Symlink(externalSource, filepath.Join(skillsHome, "paperclip")); err != nil {
		t.Fatalf("seed external symlink: %v", err)
	}

	payload := agentadaptor.ResolvedSkills{
		Entries: []agentadaptor.ResolvedSkill{
			{Key: "team/paperclip", RuntimeName: "paperclip", SourcePath: currentSource},
		},
	}
	if err := injectCodexSkills(context.Background(), payload, codexHome, &eventSink{}); err != nil {
		t.Fatalf("inject: %v", err)
	}

	resolved, err := filepath.EvalSymlinks(filepath.Join(skillsHome, "paperclip"))
	if err != nil {
		t.Fatalf("resolve external symlink: %v", err)
	}
	expected, err := filepath.EvalSymlinks(externalSource)
	if err != nil {
		t.Fatalf("resolve expected source: %v", err)
	}
	if filepath.Clean(resolved) != filepath.Clean(expected) {
		t.Fatalf("expected external symlink to be preserved, got %s", resolved)
	}
}
