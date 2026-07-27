package profileagents

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/engine"
)

func TestSyncCodexAgentMaterializesTOML(t *testing.T) {
	root := t.TempDir()
	payload := driver.AgentPayload{
		Agents: []driver.AgentSpec{{
			Key:             "reviewer",
			Description:     "Reviews risky changes.",
			Instructions:    "Focus on correctness.",
			Model:           "gpt-5.4",
			ReasoningEffort: "high",
		}},
		Fingerprint: "agents-fp",
	}
	snapshot, err := Sync(context.Background(), "codex", root, payload)
	if err != nil {
		t.Fatalf("sync agents: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(root, "agents", "reviewer.toml"))
	if err != nil {
		t.Fatalf("read agent: %v", err)
	}
	text := string(raw)
	for _, want := range []string{`name = 'reviewer'`, `description = 'Reviews risky changes.'`, `developer_instructions = 'Focus on correctness.'`, `model = 'gpt-5.4'`} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected %q in %s", want, text)
		}
	}
	if snapshot.Materialization != engine.ProfileResourceMaterializationNativeManaged {
		t.Fatalf("unexpected snapshot: %#v", snapshot)
	}
}

func TestSyncEmptyAgentsPrunesManagedFiles(t *testing.T) {
	root := t.TempDir()
	payload := driver.AgentPayload{
		Agents: []driver.AgentSpec{{
			Key:          "reviewer",
			Description:  "Reviews risky changes.",
			Instructions: "Focus on correctness.",
		}},
		Fingerprint: "agents-fp",
	}
	if _, err := Sync(context.Background(), "codex", root, payload); err != nil {
		t.Fatalf("initial sync agents: %v", err)
	}
	target := filepath.Join(root, "agents", "reviewer.toml")
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("expected materialized agent: %v", err)
	}

	snapshot, err := Sync(context.Background(), "codex", root, driver.AgentPayload{Fingerprint: "empty-agents-fp"})
	if err != nil {
		t.Fatalf("sync empty agents: %v", err)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("expected managed agent to be pruned, stat err=%v", err)
	}
	if len(snapshot.Managed) != 0 {
		t.Fatalf("expected no managed agents after prune, got %#v", snapshot.Managed)
	}
}

func TestSyncClaudeAgentMaterializesMarkdown(t *testing.T) {
	root := t.TempDir()
	payload := driver.AgentPayload{
		Agents: []driver.AgentSpec{{
			Key:          "api-developer",
			Description:  "Implements APIs.",
			Instructions: "Follow API conventions.",
			ToolPolicy:   &driver.AgentToolPolicy{Allow: []string{"Read", "Grep"}, Deny: []string{"Write"}},
			Skills:       []string{"api-conventions"},
		}},
		Fingerprint: "agents-fp",
	}
	if _, err := Sync(context.Background(), "claude", root, payload); err != nil {
		t.Fatalf("sync agents: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(root, "agents", "api-developer.md"))
	if err != nil {
		t.Fatalf("read agent: %v", err)
	}
	text := string(raw)
	for _, want := range []string{`name: "api-developer"`, `tools: ["Read", "Grep"]`, `disallowedTools: ["Write"]`, `skills:`, `Follow API conventions.`} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected %q in %s", want, text)
		}
	}
}

func TestSnapshotClaudeAgentLocalHooksWarnsUnsupported(t *testing.T) {
	payload := driver.AgentPayload{
		Agents: []driver.AgentSpec{{
			Key:          "api-developer",
			Instructions: "Follow API conventions.",
			Hooks: []driver.HookSpec{{
				Key:     "pre-shell",
				Event:   driver.HookEventPreShell,
				Handler: driver.HookHandler{Type: driver.HookHandlerCommand, Command: "true"},
			}},
		}},
		Fingerprint: "agents-fp",
	}

	snapshot := Snapshot("claude", t.TempDir(), payload, true)
	if snapshot.Support != engine.ProfileResourceSupportPortableExtended {
		t.Fatalf("expected extended support warning, got %#v", snapshot)
	}
	if !strings.Contains(strings.Join(snapshot.Warnings, "\n"), "agent-local hooks are not mapped for Claude agents") {
		t.Fatalf("expected agent-local hooks warning, got %#v", snapshot.Warnings)
	}
}

func TestSyncAgentRejectsExternalConflict(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "agents"), 0o755); err != nil {
		t.Fatalf("mkdir agents: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "agents", "reviewer.toml"), []byte("external"), 0o644); err != nil {
		t.Fatalf("write external: %v", err)
	}
	payload := driver.AgentPayload{
		Agents:      []driver.AgentSpec{{Key: "reviewer", Instructions: "Review."}},
		Fingerprint: "agents-fp",
	}
	if _, err := Sync(context.Background(), "codex", root, payload); err == nil || !strings.Contains(err.Error(), "external entry") {
		t.Fatalf("expected external conflict, got %v", err)
	}
}
