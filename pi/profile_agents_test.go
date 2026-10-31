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
	"github.com/agent-dance/agent-adaptor/internal/profilejson"
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

func TestProfileAgentsOwnershipClearAndClone(t *testing.T) {
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "profile")
	create := func(opts ...adaptor.Option) *adaptor.Agent {
		a := adaptor.New(Driver(Config{Env: []driver.EnvBinding{{Name: "PI_CODING_AGENT_DIR", Value: root}}}), opts...)
		t.Cleanup(func() { _ = a.Close(ctx) })
		return a
	}
	spec := profile.SubAgent{Key: "host/reviewer", RuntimeName: "reviewer", Instructions: "private proof", Model: "test/model", ReasoningEffort: "low", ToolPolicy: &profile.ToolPolicy{Allow: []string{"Read", "Grep"}, Deny: []string{"bash"}}}
	a := create(adaptor.WithProfileResources(profile.Resources{Agents: []profile.SubAgent{spec}}))
	state, err := a.ProfileState(ctx)
	if err != nil || len(agentRow(t, state).Managed) != 0 {
		t.Fatalf("before: %+v %v", state, err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatal("probe created profile")
	}
	state, err = a.SyncProfile(ctx)
	if err != nil || !reflect.DeepEqual(agentRow(t, state).Managed, []string{spec.Key}) {
		t.Fatalf("sync: %+v %v", state, err)
	}
	file := filepath.Join(root, agentProfileFile)
	raw, _ := os.ReadFile(file)
	fresh := create()
	if _, err := fresh.SyncProfile(ctx); err != nil {
		t.Fatal(err)
	}
	unchanged, _ := os.ReadFile(file)
	if string(raw) != string(unchanged) {
		t.Fatal("undeclared family changed")
	}
	target := filepath.Join(t.TempDir(), "clone")
	clone := create(adaptor.WithProfile(profile.CloneFrom(root, target, profile.CopySettings())))
	state, err = clone.SyncProfile(ctx)
	if err != nil || !reflect.DeepEqual(agentRow(t, state).Managed, []string{spec.Key}) {
		t.Fatalf("clone: %+v %v", state, err)
	}
	clear := create(adaptor.WithProfile(profile.Dedicated(target)), adaptor.WithProfileResources(profile.Resources{Agents: []profile.SubAgent{}}))
	if _, err := clear.SyncProfile(ctx); err != nil {
		t.Fatal(err)
	}
	entries, err := profilejson.ReadEntries(target, agentProfileFile, "agents")
	if err != nil || len(entries) != 0 {
		t.Fatalf("clear: %v %v", entries, err)
	}
	if err := os.WriteFile(file, []byte(strings.ReplaceAll(string(raw), "private proof", "external edit")), 0600); err != nil {
		t.Fatal(err)
	}
	state, err = fresh.ProfileState(ctx)
	if err != nil || len(agentRow(t, state).Managed) != 0 || len(agentRow(t, state).External) != 1 {
		t.Fatalf("external: %+v %v", state, err)
	}
	if _, err := a.SyncProfile(ctx); err == nil {
		t.Fatal("external change overwritten")
	}
}

func TestProfileAgentsInvocationAndDrift(t *testing.T) {
	f := persistentRPCFixture(t)
	spec := driver.AgentSpec{Key: "worker", Instructions: "first"}
	sync := func() {
		entries, err := agentProfileEntries(driver.AgentPayload{Agents: []driver.AgentSpec{spec}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := profilejson.Sync(context.Background(), agentProfileLayout(f.profile), entries, ""); err != nil {
			t.Fatal(err)
		}
	}
	sync()
	f.run(t)
	f.run(t)
	if len(f.pids(t)) != 1 {
		t.Fatal("unchanged agents did not reuse parent")
	}
	spec.Instructions = "second"
	sync()
	f.run(t)
	if len(f.pids(t)) != 2 {
		t.Fatal("agent edit retained stale extension")
	}
	file := filepath.Join(f.profile, agentProfileFile)
	before, _ := os.ReadFile(file)
	f.run(t, adaptor.WithProfileResources(profile.Resources{Agents: []profile.SubAgent{}}))
	if len(f.pids(t)) != 3 {
		t.Fatal("clear retained delegation extension")
	}
	after, _ := os.ReadFile(file)
	if string(before) != string(after) {
		t.Fatal("invocation clear changed durable file")
	}
	payload, err := resolveProfileAgents(driver.Request{}, f.profile)
	if err != nil || len(payload.Agents) != 1 || payload.Agents[0].Instructions != "second" {
		t.Fatalf("fresh discovery: %+v %v", payload, err)
	}
}

func TestProfileAgentInvalidBeforeInitialization(t *testing.T) {
	for _, spec := range []profile.SubAgent{{Key: "x", RuntimeName: "../bad"}, {Key: "x", SourcePath: "missing.md"}, {Key: "x", ToolPolicy: &profile.ToolPolicy{Allow: []string{"Read(*)"}}}, {Key: "x", Model: "provider/"}, {Key: "x", ReasoningEffort: "bogus"}, {Key: "x", Skills: []string{"invalid\x00"}}} {
		t.Run(spec.Key+spec.RuntimeName+spec.Model+spec.ReasoningEffort, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "profile")
			a := adaptor.New(Driver(Config{Env: []driver.EnvBinding{{Name: "PI_CODING_AGENT_DIR", Value: root}}}), adaptor.WithProfileResources(profile.Resources{Agents: []profile.SubAgent{spec}}))
			defer a.Close(context.Background())
			if _, err := a.SyncProfile(context.Background()); err == nil {
				t.Fatal("invalid agent synchronized")
			}
			if _, err := os.Stat(root); !os.IsNotExist(err) {
				t.Fatal("invalid agent initialized profile")
			}
		})
	}
	entries, err := agentProfileEntries(driver.AgentPayload{Agents: []driver.AgentSpec{{Key: "x", ToolPolicy: &driver.AgentToolPolicy{Allow: []string{"Read", "read"}, Deny: []string{"Bash"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	var normalized driver.AgentSpec
	_ = json.Unmarshal(entries["x"], &normalized)
	if !reflect.DeepEqual(normalized.ToolPolicy.Allow, []string{"read"}) || !reflect.DeepEqual(normalized.ToolPolicy.Deny, []string{"bash"}) {
		t.Fatalf("mapping: %+v", normalized)
	}
}
