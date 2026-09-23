package profileagents

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/engine"
	"github.com/agent-dance/agent-adaptor/internal/profilestate"
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
	names := []string{"reviewer", "ReviewAgent", "reviewagent", "审查Agent", "reviewer.json", "reviewer..json", ".reviewer", "review agent", "con", "CON", "default", strings.Repeat("a", 300), fmt.Sprintf("agent~%x", sha256.Sum256([]byte("ReviewAgent")))}
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

func TestAlignmentCodeBuddyUnsafeNativeNamePreservesResources(t *testing.T) {
	for _, name := range []string{"catalog/default", `catalog\default`, "catalog:default", ".", "..", "../escape", "/absolute", "\ufeffreviewer", "reviewer\ufeff", "invalid\x00name", string([]byte{0xff})} {
		t.Run(fmt.Sprintf("%q", name), func(t *testing.T) {
			for _, source := range []bool{false, true} {
				t.Run(fmt.Sprintf("source=%t", source), func(t *testing.T) {
					root := t.TempDir()
					original := driver.AgentPayload{Agents: []driver.AgentSpec{{Key: "healthy", Instructions: "preserve this agent"}}}
					if _, err := Sync(context.Background(), "codebuddy", root, original); err != nil {
						t.Fatal(err)
					}
					before := codeBuddyResourceFiles(t, root)
					spec := driver.AgentSpec{Key: "catalog/invalid", RuntimeName: name, Instructions: "must never be written"}
					if source {
						spec.SourcePath = filepath.Join(t.TempDir(), "source.md")
						spec.Instructions = ""
						if err := os.WriteFile(spec.SourcePath, []byte("---\nname: source-agent\n---\nNative bytes remain caller-owned\n"), 0600); err != nil {
							t.Fatal(err)
						}
					}
					// A valid update is encountered first, but no write or prune may
					// occur until the complete set of native names is accepted.
					payload := driver.AgentPayload{Agents: []driver.AgentSpec{{Key: "new-agent", Instructions: "must not replace healthy"}, spec}}
					_, err := Sync(context.Background(), "codebuddy", root, payload)
					if err == nil || !strings.Contains(err.Error(), "invalid runtime name") {
						t.Errorf("official loader cannot accept native name %q: got %v", name, err)
					}
					if after := codeBuddyResourceFiles(t, root); !reflect.DeepEqual(after, before) {
						t.Error("rejected native name changed healthy agent files or manifest")
					}
				})
			}
		})
	}
}

func codeBuddyResourceFiles(t *testing.T, root string) map[string]string {
	t.Helper()
	files := map[string]string{}
	paths, err := filepath.Glob(filepath.Join(root, "agents", "*"))
	if err != nil {
		t.Fatal(err)
	}
	paths = append(paths, filepath.Join(root, profilestate.ManifestName))
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		files[path] = string(raw)
	}
	return files
}
