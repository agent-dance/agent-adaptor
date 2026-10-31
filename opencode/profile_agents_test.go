package opencode

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/testutil"
	"github.com/agent-dance/agent-adaptor/profile"
)

func agentRow(t *testing.T, snapshot adaptor.ProfileSnapshot) adaptor.ResourceSnapshot {
	t.Helper()
	for _, row := range snapshot.Resources {
		if row.Kind == adaptor.ProfileResourceAgents {
			return row
		}
	}
	t.Fatal("missing agent row")
	return adaptor.ResourceSnapshot{}
}

func TestProfileAgentsDurableOwnershipAndClone(t *testing.T) {
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "profile")
	newAgent := func(opts ...adaptor.Option) *adaptor.Agent {
		a := adaptor.New(Driver(Config{Env: []driver.EnvBinding{{Name: "OPENCODE_CONFIG_DIR", Value: root}}}), opts...)
		t.Cleanup(func() { _ = a.Close(ctx) })
		return a
	}
	spec := profile.SubAgent{Key: "host/reviewer", RuntimeName: "reviewer", Instructions: "Read {file:literal} and {env:literal} literally.", Model: "test/model", ReasoningEffort: "low", ToolPolicy: &profile.ToolPolicy{Allow: []string{"Read", "Grep"}, Deny: []string{"bash"}}, Native: map[string]any{"temperature": 0.2, "steps": 3}}
	a := newAgent(adaptor.WithProfileResources(profile.Resources{Agents: []profile.SubAgent{spec}}))
	before, err := a.ProfileState(ctx)
	if err != nil || len(agentRow(t, before).Managed) != 0 {
		t.Fatalf("before=%+v err=%v", before, err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatal("read-only probe created profile")
	}
	after, err := a.SyncProfile(ctx)
	if err != nil || !reflect.DeepEqual(agentRow(t, after).Managed, []string{"host/reviewer"}) || len(agentRow(t, after).Warnings) != 0 {
		t.Fatalf("after=%+v err=%v", after, err)
	}
	file := filepath.Join(root, "opencode.jsonc")
	raw, err := os.ReadFile(file)
	if err != nil || !strings.Contains(string(raw), `\u007bfile:`) || !strings.Contains(string(raw), `\u007benv:`) {
		t.Fatal("native literals were not escaped")
	}
	var native struct {
		Agent map[string]struct {
			Mode, Model, Variant string
			Permission           map[string]string
		}
	}
	if err := json.Unmarshal(raw, &native); err != nil {
		t.Fatal(err)
	}
	entry := native.Agent["reviewer"]
	if entry.Mode != "subagent" || entry.Model != "test/model" || entry.Variant != "low" || !reflect.DeepEqual(entry.Permission, map[string]string{"*": "deny", "read": "allow", "grep": "allow", "bash": "deny"}) {
		t.Fatalf("wrong native agent mapping: %+v", entry)
	}
	fresh := newAgent()
	observed, err := fresh.SyncProfile(ctx)
	if err != nil || !reflect.DeepEqual(agentRow(t, observed).Managed, []string{"host/reviewer"}) {
		t.Fatalf("undeclared sync: %v %+v", err, observed)
	}
	unchanged, _ := os.ReadFile(file)
	if string(unchanged) != string(raw) {
		t.Fatal("undeclared sync rewrote native agents")
	}
	target := filepath.Join(t.TempDir(), "clone")
	clone := newAgent(adaptor.WithProfile(profile.CloneFrom(root, target, profile.CopySettings())))
	cloned, err := clone.SyncProfile(ctx)
	if err != nil || !reflect.DeepEqual(agentRow(t, cloned).Managed, []string{"host/reviewer"}) {
		t.Fatalf("clone lost agent ownership: %v %+v", err, cloned)
	}
	clearClone := newAgent(adaptor.WithProfile(profile.Dedicated(target)), adaptor.WithProfileResources(profile.Resources{Agents: []profile.SubAgent{}}))
	cleared, err := clearClone.SyncProfile(ctx)
	if err != nil || len(agentRow(t, cleared).Managed) != 0 {
		t.Fatalf("clear cloned agent: %v", err)
	}
	unchanged, _ = os.ReadFile(file)
	if string(unchanged) != string(raw) {
		t.Fatal("clone mutation reached source")
	}
	changed := strings.ReplaceAll(string(raw), "test/model", "test/external")
	if err := os.WriteFile(file, []byte(changed), 0600); err != nil {
		t.Fatal(err)
	}
	observed, err = fresh.ProfileState(ctx)
	if err != nil || len(agentRow(t, observed).Managed) != 0 || !reflect.DeepEqual(agentRow(t, observed).External, []string{"reviewer"}) {
		t.Fatalf("external modification: %v %+v", err, observed)
	}
	if _, err := a.SyncProfile(ctx); err == nil {
		t.Fatal("external agent edit overwritten")
	}
}

func TestProfileAgentsExternalDefinitionsAndInvalidShapes(t *testing.T) {
	for _, filename := range []string{"opencode.json", "agents/reviewer.md", "agent/reviewer.md"} {
		t.Run(filename, func(t *testing.T) {
			root := t.TempDir()
			file := filepath.Join(root, filename)
			if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
				t.Fatal(err)
			}
			content := "---\nmode: subagent\n---\nExternal"
			if filename == "opencode.json" {
				content = `{"agent":{"reviewer":{"prompt":"external"}}}`
			}
			if err := os.WriteFile(file, []byte(content), 0600); err != nil {
				t.Fatal(err)
			}
			a := adaptor.New(Driver(Config{Env: []driver.EnvBinding{{Name: "OPENCODE_CONFIG_DIR", Value: root}}}), adaptor.WithProfileResources(profile.Resources{Agents: []profile.SubAgent{{Key: "reviewer"}}}))
			defer a.Close(context.Background())
			state, err := a.ProfileState(context.Background())
			if err != nil || !reflect.DeepEqual(agentRow(t, state).External, []string{"reviewer"}) {
				t.Fatalf("external state: %v %+v", err, state)
			}
			if _, err := a.SyncProfile(context.Background()); err == nil {
				t.Fatal("external definition overwritten")
			}
			if got, _ := os.ReadFile(file); string(got) != content {
				t.Fatal("external native definition changed")
			}
		})
	}
	for _, spec := range []driver.AgentSpec{
		{Key: "build"}, {Key: "bad/name"}, {Key: "test", Model: "missing-provider"},
		{Key: "test", ReasoningEffort: "high"}, {Key: "test", PermissionMode: "plan"},
		{Key: "test", SourcePath: "source.md"}, {Key: "test", Skills: []string{"skill"}},
		{Key: "test", ToolPolicy: &driver.AgentToolPolicy{Allow: []string{"Write"}}},
		{Key: "test", Native: map[string]any{"steps": 1.5}},
		{Key: "test", Native: map[string]any{"temperature": json.Number("1e9999")}},
		{Key: "test", Native: map[string]any{"permission": "allow"}},
	} {
		t.Run(spec.Key+spec.Model+spec.PermissionMode, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "absent")
			d := Driver(Config{Command: "missing-opencode", Env: []driver.EnvBinding{{Name: "OPENCODE_CONFIG_DIR", Value: root}}})
			payload := driver.ProfilePayload{Declared: driver.ProfileResourceDeclarations{Agents: true}, Agents: driver.AgentPayload{Agents: []driver.AgentSpec{spec}}}
			if _, err := d.Run(context.Background(), driver.Request{ProfilePayload: payload}, &testutil.EventRecorder{}); err == nil {
				t.Fatal("invalid agent accepted")
			}
			if _, err := d.(driver.ProfileResourceDriver).SyncProfileResources(context.Background(), nil, driver.AgentIdentity{}, nil, payload, nil, nil); err == nil {
				t.Fatal("invalid agent synced")
			}
			if _, err := os.Stat(root); !os.IsNotExist(err) {
				t.Fatal("invalid agent initialized profile")
			}
		})
	}
}

func TestProfileAgentsInvocationReuseClearAndOverride(t *testing.T) {
	spec := profile.SubAgent{Key: "host/reviewer", RuntimeName: "reviewer", Instructions: "first {file:literal}"}
	f := persistentServerFixture(t, adaptor.WithProfileResources(profile.Resources{Agents: []profile.SubAgent{spec}}))
	if _, err := f.agent.SyncProfile(context.Background()); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(f.profile, "opencode.jsonc")
	original, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	f.run(t)
	f.run(t)
	if len(f.pids(t)) != 1 {
		t.Fatal("identical agent did not reuse process")
	}
	spec.Instructions = "second"
	f.run(t, adaptor.WithProfileResources(profile.Resources{Agents: []profile.SubAgent{spec}}))
	if len(f.pids(t)) != 2 {
		t.Fatal("agent change reused old process")
	}
	f.run(t, adaptor.WithProfileResources(profile.Resources{Agents: []profile.SubAgent{}}))
	if len(f.pids(t)) != 3 {
		t.Fatal("agent clear reused old process")
	}
	if got, _ := os.ReadFile(file); string(got) != string(original) {
		t.Fatal("invocation rewrote durable agents")
	}
	if err := os.WriteFile(filepath.Join(f.dir, "managed-agents.json"), []byte(`{"reviewer":{"mode":"subagent","prompt":"wrong"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(f.dir, "capture.json")); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	_, err = f.thread.Run(context.Background(), "must not deliver", adaptor.WithSpawn())
	if err == nil || !strings.Contains(err.Error(), "differs from its declared") {
		t.Fatalf("native override not rejected: %v", err)
	}
	if _, err := os.Stat(filepath.Join(f.dir, "capture.json")); !os.IsNotExist(err) {
		t.Fatal("prompt delivered despite wrong native agent")
	}
}

func TestProfileAgentUnsupportedFieldsUseStableError(t *testing.T) {
	_, _, err := agentProfilePlan("validate", driver.AgentPayload{Agents: []driver.AgentSpec{{Key: "test", MCPServers: []string{"server"}}}})
	if !errors.Is(err, errors.ErrUnsupported) {
		t.Fatalf("unsupported error identity: %v", err)
	}
}

func TestProfileAgentExternalLinksAndCycles(t *testing.T) {
	root, target := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(target, "reviewer.md"), []byte("external"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(root, "agents")); err != nil {
		t.Skipf("directory links unavailable: %v", err)
	}
	if err := os.Symlink(root, filepath.Join(target, "cycle")); err != nil {
		t.Skipf("cycle link unavailable: %v", err)
	}
	external, err := externalProfileAgents(root)
	if err != nil || !reflect.DeepEqual(external, map[string]bool{"reviewer": true}) {
		t.Fatalf("linked agent discovery: %v %+v", err, external)
	}
}

func TestProfileAgentVariantAvailabilityAndNormalizedDefaults(t *testing.T) {
	for _, variant := range []string{"", "low", "unavailable"} {
		t.Run(variant, func(t *testing.T) {
			_, entries, err := agentProfilePlan("validate", driver.AgentPayload{Agents: []driver.AgentSpec{{Key: "reviewer", Model: "test/model", ReasoningEffort: variant}}})
			if err != nil {
				t.Fatal(err)
			}
			var entry map[string]any
			if err := json.Unmarshal(entries["reviewer"], &entry); err != nil {
				t.Fatal(err)
			}
			entry["options"], entry["permission"] = map[string]any{}, map[string]any{}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/config":
					_ = json.NewEncoder(w).Encode(map[string]any{"agent": map[string]any{"reviewer": entry}})
				case "/provider":
					_, _ = w.Write([]byte(`{"all":[{"id":"test","models":{"model":{"variants":{"low":{}}}}}]}`))
				default:
					t.Error("unexpected native call")
					w.WriteHeader(400)
				}
			}))
			defer server.Close()
			client := &serverClient{base: server.URL, http: server.Client()}
			err = (agentInvocation{Entries: entries}).verify(context.Background(), client)
			if variant == "unavailable" {
				if !errors.Is(err, errors.ErrUnsupported) {
					t.Fatalf("unavailable variant: %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
}
