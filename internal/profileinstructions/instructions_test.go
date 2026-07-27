package profileinstructions

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	agentadaptor "github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/engine"
)

func TestSyncMaterializesInlineInstructions(t *testing.T) {
	profileDir := t.TempDir()
	ref := &agentadaptor.InstructionsBundleRef{ID: "team", Content: "# Team\n\nBe concise.", Scope: agentadaptor.InstructionScopeProject}

	snapshot, path, err := Sync(context.Background(), "cursor", profileDir, ref)
	if err != nil {
		t.Fatalf("sync instructions: %v", err)
	}
	if snapshot.Support != engine.ProfileResourceSupportFallback || snapshot.Materialization != engine.ProfileResourceMaterializationPromptInjected {
		t.Fatalf("unexpected snapshot: %#v", snapshot)
	}
	if path == "" || !strings.HasPrefix(path, filepath.Join(profileDir, ".agent-adaptor", "instructions")) {
		t.Fatalf("unexpected managed path: %q", path)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read managed file: %v", err)
	}
	if string(raw) != ref.Content {
		t.Fatalf("unexpected managed content: %q", string(raw))
	}
}

func TestPrepareForRunCopiesSourceAndBuildsPromptPrefix(t *testing.T) {
	profileDir := t.TempDir()
	source := filepath.Join(t.TempDir(), "instructions.md")
	if err := os.WriteFile(source, []byte("Prefer tests."), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}

	prepared, err := PrepareForRun(context.Background(), "cursor", profileDir, "", &agentadaptor.InstructionsBundleRef{ID: "run", Path: source})
	if err != nil {
		t.Fatalf("prepare run instructions: %v", err)
	}
	prefix := PromptPrefix(prepared, agentadaptor.InstructionModeAdditive)
	if !strings.Contains(prefix, "Prefer tests.") || !strings.Contains(prefix, prepared.Path) {
		t.Fatalf("unexpected prompt prefix: %q", prefix)
	}
}

func TestSyncPrunesPreviousManagedInstructions(t *testing.T) {
	profileDir := t.TempDir()
	if _, _, err := Sync(context.Background(), "cursor", profileDir, &agentadaptor.InstructionsBundleRef{ID: "old", Content: "old"}); err != nil {
		t.Fatalf("sync old: %v", err)
	}
	if _, _, err := Sync(context.Background(), "cursor", profileDir, &agentadaptor.InstructionsBundleRef{ID: "new", Content: "new"}); err != nil {
		t.Fatalf("sync new: %v", err)
	}
	oldPath := filepath.Join(profileDir, ".agent-adaptor", "instructions", "old.md")
	if _, err := os.Stat(oldPath); !os.IsNotExist(err) {
		t.Fatalf("expected old managed instructions pruned, got %v", err)
	}
}

func TestSyncMaterializesCodexNativeInstructions(t *testing.T) {
	profileDir := t.TempDir()
	ref := &agentadaptor.InstructionsBundleRef{ID: "team", Content: "# Team\n\nUse native guidance."}

	snapshot, path, err := Sync(context.Background(), "codex", profileDir, ref)
	if err != nil {
		t.Fatalf("sync codex instructions: %v", err)
	}
	if snapshot.Support != engine.ProfileResourceSupportPortableCore || snapshot.Materialization != engine.ProfileResourceMaterializationNativeManaged {
		t.Fatalf("unexpected snapshot: %#v", snapshot)
	}
	if path != filepath.Join(profileDir, "AGENTS.md") {
		t.Fatalf("unexpected native path: %q", path)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read native file: %v", err)
	}
	if string(raw) != ref.Content {
		t.Fatalf("unexpected native content: %q", string(raw))
	}
	prepared, err := PrepareForRun(context.Background(), "codex", profileDir, t.TempDir(), ref)
	if err != nil {
		t.Fatalf("prepare codex instructions: %v", err)
	}
	if prefix := PromptPrefix(prepared, agentadaptor.InstructionModeAdditive); prefix != "" {
		t.Fatalf("native instructions should not be prompt-injected, got %q", prefix)
	}
}

func TestSyncPrunesOldNativeInstructionWhenTargetChanges(t *testing.T) {
	profileDir := t.TempDir()
	additive := &agentadaptor.InstructionsBundleRef{ID: "team", Content: "Add guidance."}
	if _, path, err := Sync(context.Background(), "codex", profileDir, additive); err != nil {
		t.Fatalf("sync additive instructions: %v", err)
	} else if path != filepath.Join(profileDir, "AGENTS.md") {
		t.Fatalf("unexpected additive path: %q", path)
	}

	replace := &agentadaptor.InstructionsBundleRef{ID: "team", Content: "Replace guidance.", Mode: agentadaptor.InstructionModeReplace}
	if _, path, err := Sync(context.Background(), "codex", profileDir, replace); err != nil {
		t.Fatalf("sync replacement instructions: %v", err)
	} else if path != filepath.Join(profileDir, "AGENTS.override.md") {
		t.Fatalf("unexpected replacement path: %q", path)
	}
	if _, err := os.Stat(filepath.Join(profileDir, "AGENTS.md")); !os.IsNotExist(err) {
		t.Fatalf("expected old additive instructions pruned, got %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(profileDir, "AGENTS.override.md"))
	if err != nil {
		t.Fatalf("read replacement instructions: %v", err)
	}
	if string(raw) != replace.Content {
		t.Fatalf("unexpected replacement content: %q", string(raw))
	}
}

func TestSyncMaterializesClaudeNativeInstructions(t *testing.T) {
	profileDir := t.TempDir()
	ref := &agentadaptor.InstructionsBundleRef{ID: "team", Content: "Prefer small patches."}

	snapshot, path, err := Sync(context.Background(), "claude", profileDir, ref)
	if err != nil {
		t.Fatalf("sync claude instructions: %v", err)
	}
	if snapshot.Support != engine.ProfileResourceSupportPortableCore || snapshot.Materialization != engine.ProfileResourceMaterializationNativeManaged {
		t.Fatalf("unexpected snapshot: %#v", snapshot)
	}
	if path != filepath.Join(profileDir, "CLAUDE.md") {
		t.Fatalf("unexpected native path: %q", path)
	}
}

func TestPrepareForRunMaterializesCursorProjectRule(t *testing.T) {
	profileDir := t.TempDir()
	workspaceDir := t.TempDir()
	ref := &agentadaptor.InstructionsBundleRef{ID: "team", Content: "Use project rules.", Scope: agentadaptor.InstructionScopeProject}

	prepared, err := PrepareForRun(context.Background(), "cursor", profileDir, workspaceDir, ref)
	if err != nil {
		t.Fatalf("prepare cursor instructions: %v", err)
	}
	wantPath := filepath.Join(workspaceDir, ".cursor", "rules", "team.mdc")
	if prepared.Path != wantPath {
		t.Fatalf("unexpected cursor rule path: %q", prepared.Path)
	}
	if prepared.Snapshot.Support != engine.ProfileResourceSupportPortableCore || prepared.Snapshot.Materialization != engine.ProfileResourceMaterializationNativeManaged {
		t.Fatalf("unexpected cursor snapshot: %#v", prepared.Snapshot)
	}
	raw, err := os.ReadFile(wantPath)
	if err != nil {
		t.Fatalf("read cursor rule: %v", err)
	}
	text := string(raw)
	if !strings.Contains(text, "alwaysApply: true") || !strings.Contains(text, "Use project rules.") {
		t.Fatalf("unexpected cursor rule content: %q", text)
	}
}

func TestPrepareForRunPrunesStaleCursorProjectRule(t *testing.T) {
	profileDir := t.TempDir()
	workspaceDir := t.TempDir()
	ref := &agentadaptor.InstructionsBundleRef{ID: "team", Content: "Use project rules.", Scope: agentadaptor.InstructionScopeProject}
	if _, err := PrepareForRun(context.Background(), "cursor", profileDir, workspaceDir, ref); err != nil {
		t.Fatalf("prepare cursor project instructions: %v", err)
	}
	rulePath := filepath.Join(workspaceDir, ".cursor", "rules", "team.mdc")
	if _, err := os.Stat(rulePath); err != nil {
		t.Fatalf("expected cursor rule: %v", err)
	}
	fallbackRef := &agentadaptor.InstructionsBundleRef{ID: "team", Content: "Use fallback rules.", Scope: agentadaptor.InstructionScopeUser}
	if _, err := PrepareForRun(context.Background(), "cursor", profileDir, workspaceDir, fallbackRef); err != nil {
		t.Fatalf("prepare cursor fallback instructions: %v", err)
	}
	if _, err := os.Stat(rulePath); !os.IsNotExist(err) {
		t.Fatalf("expected stale cursor rule pruned, got %v", err)
	}
}
