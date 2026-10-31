package pi

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/profile"
)

func TestAgentSourceCaptureCloneAndClear(t *testing.T) {
	ctx := context.Background()
	source := filepath.Join(t.TempDir(), "reviewer.md")
	content := "---\nname: original\ndescription: Reviews changes\nmodel: test/child\ntools: [read, grep]\n---\nPRIVATE_ROLE\n"
	if err := os.WriteFile(source, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "profile")
	spec := profile.SubAgent{Key: "host/reviewer", RuntimeName: "reviewer", SourcePath: source, SourceFingerprint: "host-supplied"}
	a := adaptor.New(Driver(Config{}), adaptor.WithProfile(profile.Dedicated(root)), adaptor.WithProfileResources(profile.Resources{Agents: []profile.SubAgent{spec}}))
	defer a.Close(ctx)
	state, err := a.SyncProfile(ctx)
	if err != nil || !reflect.DeepEqual(agentRow(t, state).Managed, []string{spec.Key}) {
		t.Fatalf("sync: %v %+v", err, state)
	}
	if err := os.Remove(source); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(root, agentProfileFile))
	if err != nil || strings.Contains(string(raw), `"SourcePath":"`+source) {
		t.Fatal("source path retained")
	}
	target := filepath.Join(t.TempDir(), "clone")
	fresh := adaptor.New(Driver(Config{}), adaptor.WithProfile(profile.CloneFrom(root, target, profile.CopySettings())))
	defer fresh.Close(ctx)
	state, err = fresh.SyncProfile(ctx)
	if err != nil || !reflect.DeepEqual(agentRow(t, state).Managed, []string{spec.Key}) {
		t.Fatalf("fresh clone: %v %+v", err, state)
	}
	payload, err := resolveProfileAgents(driver.Request{}, target)
	if err != nil || len(payload.Agents) != 1 {
		t.Fatalf("load: %v", err)
	}
	got := payload.Agents[0]
	if got.RuntimeName != "reviewer" || got.SourcePath != "" || got.Instructions != "PRIVATE_ROLE" || got.Model != "test/child" || !reflect.DeepEqual(got.ToolPolicy.Allow, []string{"grep", "read"}) {
		t.Fatalf("import: %+v", got)
	}
	clear := adaptor.New(Driver(Config{}), adaptor.WithProfile(profile.Dedicated(target)), adaptor.WithProfileResources(profile.Resources{Agents: []profile.SubAgent{}}))
	defer clear.Close(ctx)
	if _, err := clear.SyncProfile(ctx); err != nil {
		t.Fatal(err)
	}
	payload, err = resolveProfileAgents(driver.Request{}, target)
	if err != nil || len(payload.Agents) != 0 {
		t.Fatal("imported agent not cleared")
	}
}

func TestAgentSourceThreadContentDrift(t *testing.T) {
	source := filepath.Join(t.TempDir(), "agent.md")
	write := func(body string) {
		t.Helper()
		if err := os.WriteFile(source, []byte("---\nname: original\ndescription: example\ntools: read, grep\n---\n"+body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("first")
	spec := profile.SubAgent{Key: "worker", SourcePath: source, SourceFingerprint: "constant-host-token"}
	f := persistentRPCFixture(t, adaptor.WithProfileResources(profile.Resources{Agents: []profile.SubAgent{spec}}))
	f.run(t)
	f.run(t)
	if len(f.pids(t)) != 1 {
		t.Fatal("unchanged source did not reuse process")
	}
	write("second")
	f.run(t)
	if len(f.pids(t)) != 2 {
		t.Fatal("changed source reused old process")
	}
	if err := os.Remove(source); err != nil {
		t.Fatal(err)
	}
	if _, err := f.thread.Run(context.Background(), "hello"); err == nil {
		t.Fatal("missing source ran")
	}
	if len(f.pids(t)) != 2 {
		t.Fatal("invalid source launched a process")
	}
}

func TestAgentSourceRejectsInvalidBeforeProfileInit(t *testing.T) {
	for i, content := range []string{"---\nname: worker\n---\nbody", "---\nname: worker\ndescription: x\ntools: [read, 3]\n---", "---\nname: worker\ndescription: x\ntools: [read, null]\n---", "---\nname: worker\ndescription: x\ntools: [unknown]\n---", "---\nname: worker\ndescription: x\npermissionMode: bypassPermissions\n---"} {
		t.Run(string(rune('a'+i)), func(t *testing.T) {
			source := filepath.Join(t.TempDir(), "agent.md")
			if err := os.WriteFile(source, []byte(content), 0600); err != nil {
				t.Fatal(err)
			}
			root := filepath.Join(t.TempDir(), "uninitialized")
			a := adaptor.New(Driver(Config{}), adaptor.WithProfile(profile.Dedicated(root)), adaptor.WithProfileResources(profile.Resources{Agents: []profile.SubAgent{{Key: "worker", SourcePath: source}}}))
			defer a.Close(context.Background())
			if _, err := a.SyncProfile(context.Background()); err == nil {
				t.Fatal("invalid source synchronized")
			}
			if _, err := os.Stat(root); !os.IsNotExist(err) {
				t.Fatal("invalid source initialized profile")
			}
		})
	}
}

func TestAgentSourceFrozenPayload(t *testing.T) {
	source := filepath.Join(t.TempDir(), "agent.md")
	if err := os.WriteFile(source, []byte("---\nname: original\ndescription: x\n---\nfrozen"), 0600); err != nil {
		t.Fatal(err)
	}
	payload, err := prepareAgentPayload(driver.AgentPayload{Agents: []driver.AgentSpec{{Key: "worker", SourcePath: source}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(source); err != nil {
		t.Fatal(err)
	}
	entries, err := agentProfileEntries(payload)
	if err != nil {
		t.Fatal(err)
	}
	var got driver.AgentSpec
	if err := json.Unmarshal(entries["worker"], &got); err != nil || got.Instructions != "frozen" {
		t.Fatal("capture re-read source")
	}
}

func TestAgentSourceRejectsInlineOverrides(t *testing.T) {
	for _, spec := range []driver.AgentSpec{{Model: "test/model"}, {Description: "role"}, {ToolPolicy: &driver.AgentToolPolicy{}}, {Native: map[string]any{"field": true}}, {Skills: []string{"skill"}}, {PermissionMode: "default"}} {
		spec.Key, spec.SourcePath = "worker", "unread.md"
		if _, err := normalizeAgent(spec); err == nil || !strings.Contains(err.Error(), "inline agent fields") {
			t.Fatalf("ambiguous declaration: %v", err)
		}
	}
}
