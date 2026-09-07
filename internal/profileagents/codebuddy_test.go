package profileagents

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/engine"
)

func TestAlignmentCodeBuddyAgentNativeMarkdown(t *testing.T) {
	root := t.TempDir()
	spec := driver.AgentSpec{Key: "catalog/reviewer", RuntimeName: "named-reviewer", Description: "Check \"quoted\" paths\nand 中文.", Instructions: "Exact body: \"quoted\"\n中文", Model: "fixture-model", ReasoningEffort: "high", PermissionMode: "plan", ToolPolicy: &driver.AgentToolPolicy{Allow: []string{"Read", "Grep"}, Deny: []string{"Write"}}, MCPServers: []string{"known"}, Skills: []string{"review"}}
	snapshot, err := Sync(context.Background(), "codebuddy", root, driver.AgentPayload{Agents: []driver.AgentSpec{spec}, Fingerprint: "catalog-v1"})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(root, "agents", "named-reviewer.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`name: "named-reviewer"`, `description: "Check \"quoted\" paths\nand 中文."`, `model: "fixture-model"`, `effort: "high"`, `permissionMode: "plan"`, `tools: ["Read", "Grep"]`, `disallowedTools: ["Write"]`, "mcpServers:\n  - \"known\"", "skills:\n  - \"review\"", "---\n\n" + spec.Instructions + "\n"} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("native field missing: %q", want)
		}
	}
	if snapshot.Materialization != engine.ProfileResourceMaterializationNativeManaged || len(snapshot.Managed) != 1 || snapshot.Managed[0] != spec.Key {
		t.Fatal("materialization did not retain canonical key")
	}
	if _, err := Sync(context.Background(), "codebuddy", root, driver.AgentPayload{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "agents", "named-reviewer.md")); !os.IsNotExist(err) {
		t.Fatal("explicit clear did not prune managed agent")
	}
}

func TestAlignmentCodeBuddyAgentUnsupportedPreservesFile(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*driver.AgentSpec)
	}{
		{"sandbox", func(s *driver.AgentSpec) { s.SandboxMode = "workspace-write" }},
		{"hooks", func(s *driver.AgentSpec) { s.Hooks = []driver.HookSpec{{Key: "local"}} }},
		{"native", func(s *driver.AgentSpec) { s.Native = map[string]any{"experimental": true} }},
		{"effort", func(s *driver.AgentSpec) { s.ReasoningEffort = "unknown" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			spec := driver.AgentSpec{Key: "reviewer", Instructions: "original"}
			payload := driver.AgentPayload{Agents: []driver.AgentSpec{spec}}
			if _, err := Sync(context.Background(), "codebuddy", root, payload); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(root, "agents", "reviewer.md")
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			tc.change(&spec)
			payload.Agents = []driver.AgentSpec{spec}
			if _, err := Sync(context.Background(), "codebuddy", root, payload); err == nil {
				t.Fatal("unsupported declared field silently accepted")
			}
			after, err := os.ReadFile(path)
			if err != nil || string(after) != string(before) {
				t.Fatal("invalid update changed healthy resource")
			}
		})
	}
}

func TestAlignmentCodeBuddyAgentSourceAndConflict(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(t.TempDir(), "native.md")
	text := "---\nname: source-agent\ndescription: Source fixture\n---\nNative content\n"
	if err := os.WriteFile(source, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	spec := driver.AgentSpec{Key: "source", RuntimeName: "source-agent", SourcePath: source, SourceFingerprint: "native-v1"}
	if _, err := Sync(context.Background(), "codebuddy", root, driver.AgentPayload{Agents: []driver.AgentSpec{spec}}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(root, "agents", "source-agent.md"))
	if err != nil || string(raw) != text {
		t.Fatal("SourcePath bytes changed")
	}
	external := filepath.Join(root, "agents", "external.md")
	if err := os.WriteFile(external, []byte("external"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Sync(context.Background(), "codebuddy", root, driver.AgentPayload{Agents: []driver.AgentSpec{{Key: "external", Instructions: "replacement"}}}); err == nil {
		t.Fatal("external file silently replaced")
	}
	raw, err = os.ReadFile(external)
	if err != nil || string(raw) != "external" {
		t.Fatal("external conflict changed bytes")
	}
}

func TestAlignmentCodeBuddyAgentNameAndFileSeparation(t *testing.T) {
	root := t.TempDir()
	names := []string{"reviewer", "ReviewAgent", "reviewagent", "审查Agent", "catalog/default", "reviewer.json", "../escape", "con", strings.Repeat("a", 300), fmt.Sprintf("agent~%x", sha256.Sum256([]byte("ReviewAgent")))}
	specs := make([]driver.AgentSpec, 0, len(names))
	for _, name := range names {
		specs = append(specs, driver.AgentSpec{Key: name, RuntimeName: name, Instructions: "exact role"})
	}
	if _, err := Sync(context.Background(), "codebuddy", root, driver.AgentPayload{Agents: specs}); err != nil {
		t.Fatal(err)
	}
	paths, err := filepath.Glob(filepath.Join(root, "agents", "*.md"))
	if err != nil || len(paths) != len(names) {
		t.Fatalf("native files collided or extension changed: %v %v", paths, err)
	}
	seen := map[string]bool{}
	for _, path := range paths {
		if len(filepath.Base(path)) > 128 {
			t.Fatal("unsafe long native filename")
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(string(raw), "\n") {
			if strings.HasPrefix(line, "name: ") {
				var name string
				if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "name: ")), &name); err != nil {
					t.Fatal(err)
				}
				seen[name] = true
			}
		}
	}
	for _, name := range names {
		if !seen[name] {
			t.Fatalf("exact catalog name changed: %q", name)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "agents", "reviewer.md")); err != nil {
		t.Fatal("simple native filename changed")
	}
}
