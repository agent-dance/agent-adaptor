package opencode

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/profile"
)

const orderedAgentSource = "---\nname: original\ndescription: Reviews changes\nmodel: test/child\nmode: subagent\ntemperature: 0.25\nmaxSteps: 4\npermission:\n  'read*': deny\n  '*': allow\n  read:\n    'secret*': deny\n    '*': allow\n---\nRead {file:literal} and {env:literal} literally.\n"

func TestAgentSourceCaptureCloneAndClear(t *testing.T) {
	ctx := context.Background()
	source := filepath.Join(t.TempDir(), "reviewer.md")
	if err := os.WriteFile(source, []byte(orderedAgentSource), 0600); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "profile")
	spec := profile.SubAgent{Key: "host/reviewer", RuntimeName: "reviewer", SourcePath: source}
	a := adaptor.New(Driver(Config{}), adaptor.WithProfile(profile.Dedicated(root)), adaptor.WithProfileResources(profile.Resources{Agents: []profile.SubAgent{spec}}))
	defer a.Close(ctx)
	state, err := a.SyncProfile(ctx)
	if err != nil || !reflect.DeepEqual(agentRow(t, state).Managed, []string{spec.Key}) {
		t.Fatalf("sync: %v %+v", err, state)
	}
	if err := os.Remove(source); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "clone")
	fresh := adaptor.New(Driver(Config{}), adaptor.WithProfile(profile.CloneFrom(root, target, profile.CopySettings())))
	defer fresh.Close(ctx)
	state, err = fresh.SyncProfile(ctx)
	if err != nil || !reflect.DeepEqual(agentRow(t, state).Managed, []string{spec.Key}) {
		t.Fatalf("clone: %v %+v", err, state)
	}
	plan, err := resolveProfileAgents(driver.Request{}, target)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(plan.Entries["reviewer"], &got); err != nil {
		t.Fatal(err)
	}
	if string(got["permission"]) != `{"read*":"deny","*":"allow","read":{"secret*":"deny","*":"allow"}}` || string(got["steps"]) != "4" || got["maxSteps"] != nil || got["name"] != nil {
		t.Fatalf("source mapping changed: %s", plan.Entries["reviewer"])
	}
	if !strings.Contains(string(got["prompt"]), `\u007bfile:`) || !strings.Contains(string(got["prompt"]), `\u007benv:`) {
		t.Fatal("source templates became active")
	}
	// A reordered permission object changes native behavior and is an external
	// edit, even when JSON maps contain exactly the same key/value pairs.
	file := filepath.Join(target, "opencode.jsonc")
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var native map[string]json.RawMessage
	_ = json.Unmarshal(raw, &native)
	var agents map[string]map[string]json.RawMessage
	_ = json.Unmarshal(native["agent"], &agents)
	agents["reviewer"]["permission"] = json.RawMessage(`{"*":"allow","read*":"deny","read":{"secret*":"deny","*":"allow"}}`)
	native["agent"], _ = json.Marshal(agents)
	changed, _ := json.Marshal(native)
	if err := os.WriteFile(file, changed, 0600); err != nil {
		t.Fatal(err)
	}
	state, err = fresh.ProfileState(ctx)
	if err != nil || len(agentRow(t, state).Managed) != 0 || !reflect.DeepEqual(agentRow(t, state).External, []string{"reviewer"}) {
		t.Fatalf("order edit not protected: %v %+v", err, state)
	}
	clear := adaptor.New(Driver(Config{}), adaptor.WithProfile(profile.Dedicated(target)), adaptor.WithProfileResources(profile.Resources{Agents: []profile.SubAgent{}}))
	defer clear.Close(ctx)
	if _, err := clear.SyncProfile(ctx); err == nil {
		t.Fatal("external reorder overwritten")
	}
	if err := os.WriteFile(file, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := clear.SyncProfile(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestAgentSourcePermissionDeliveryOrder(t *testing.T) {
	expected := json.RawMessage(`{"mode":"subagent","permission":{"read*":"deny","*":"allow","read":{"secret*":"deny","*":"allow"}}}`)
	for _, changed := range []bool{false, true} {
		actual := string(expected)
		if changed {
			actual = strings.ReplaceAll(actual, `"secret*":"deny","*":"allow"`, `"*":"allow","secret*":"deny"`)
		}
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"agent":{"reviewer":` + actual + `}}`))
		}))
		plan := agentInvocation{Entries: map[string]json.RawMessage{"reviewer": expected}}
		err := plan.verify(context.Background(), &serverClient{base: server.URL, http: server.Client()})
		server.Close()
		if (err != nil) != changed {
			t.Fatalf("changed=%v err=%v", changed, err)
		}
	}
}

func TestAgentSourceNativeLegacyTools(t *testing.T) {
	permission, err := sourcePermissions(json.RawMessage(`{"read":false,"write":true,"patch":false}`), json.RawMessage(`{"read":{"*":"deny","public*":"allow"},"*":"ask"}`))
	if err != nil {
		t.Fatal(err)
	}
	if string(permission) != `{"read":{"*":"deny","public*":"allow"},"edit":"deny","*":"ask"}` {
		t.Fatalf("legacy precedence: %s", permission)
	}
	for _, raw := range []string{`null`, `true`, `{"read":null}`, `{"read":{"*":true}}`, `{"read":"allow","read":"deny"}`, `{"question":{"*":"deny"}}`, `{"read":{"":"allow"}}`} {
		if _, err := sourcePermissions(nil, json.RawMessage(raw)); err == nil {
			t.Fatalf("accepted invalid permission %s", raw)
		}
	}
}

func TestAgentSourceThreadContentDrift(t *testing.T) {
	source := filepath.Join(t.TempDir(), "agent.md")
	write := func(body string) {
		t.Helper()
		if err := os.WriteFile(source, []byte("---\nmode: subagent\npermission:\n  read:\n    'secret*': deny\n    '*': allow\n---\n"+body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("first")
	spec := profile.SubAgent{Key: "worker", SourcePath: source, SourceFingerprint: "constant-host-token"}
	f := persistentServerFixture(t, adaptor.WithProfileResources(profile.Resources{Agents: []profile.SubAgent{spec}}))
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

func TestAgentSourceInvalidBeforeInitialization(t *testing.T) {
	for i, fields := range []string{"mode: primary", "permission: {read: sometimes}", "hooks: {}", "temperature: .inf", "tools: {read: null}", "hidden: null", "variant: low", "options: []", "steps: 0", "steps: 4\nmaxSteps: bad", "disable: true", "description: [a]"} {
		t.Run(string(rune('a'+i)), func(t *testing.T) {
			source := filepath.Join(t.TempDir(), "agent.md")
			if err := os.WriteFile(source, []byte("---\n"+fields+"\n---\nbody"), 0600); err != nil {
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

func TestAgentSourceRejectsInlineOverrides(t *testing.T) {
	for _, spec := range []driver.AgentSpec{{Model: "test/model"}, {Description: "role"}, {ToolPolicy: &driver.AgentToolPolicy{}}, {Native: map[string]any{"field": true}}, {Skills: []string{"skill"}}, {PermissionMode: "default"}} {
		spec.Key, spec.SourcePath = "worker", "unread.md"
		if _, _, err := agentProfilePlan("validate", driver.AgentPayload{Agents: []driver.AgentSpec{spec}}); err == nil || !strings.Contains(err.Error(), "inline agent fields") {
			t.Fatalf("ambiguous declaration: %v", err)
		}
	}
}
