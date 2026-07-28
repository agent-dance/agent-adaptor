package adaptor_test

// Compile-time proof of the option scope system plus
// the merge-semantics contract "nearer scope wins; skills append, everything
// else replaces".

import (
	"context"
	"slices"
	"testing"
	"time"

	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/profile"
	"github.com/agent-dance/agent-adaptor/skill"
)

// ---- Positive scope assertions (compile-time) ----
//
// Every dual-scope option satisfies all three interfaces; construction-only
// options satisfy Option and nothing else. These vars are the machine-checked
// half of the scope table in docs/api-v1-redesign.md §2.3.
var (
	_ adaptor.SharedOption = adaptor.WithModel("m")
	_ adaptor.SharedOption = adaptor.WithTimeout(time.Second)
	_ adaptor.SharedOption = adaptor.WithInstructions("i")
	_ adaptor.SharedOption = adaptor.WithWorkspace("w")
	_ adaptor.SharedOption = adaptor.WithMetadata("k", "v")
	_ adaptor.SharedOption = adaptor.WithIdentity(adaptor.Identity{})
	_ adaptor.SharedOption = adaptor.WithPolicy(adaptor.Policy{})
	_ adaptor.SharedOption = adaptor.WithSkills()
	_ adaptor.SharedOption = adaptor.WithMCP()
	_ adaptor.SharedOption = adaptor.WithProfileResources(profile.Resources{})
	_ adaptor.SharedOption = adaptor.WithWorkspaceSpec(nil)
	_ adaptor.SharedOption = adaptor.WithServices()
	_ adaptor.SharedOption = adaptor.WithRunServices()

	// SharedOption is usable in both positions.
	_ adaptor.Option     = adaptor.WithModel("m")
	_ adaptor.CallOption = adaptor.WithModel("m")

	// Construction-scope-only options.
	_ adaptor.Option = adaptor.WithThreadStore(nil)
	_ adaptor.Option = adaptor.WithEventBuffer(64)
	_ adaptor.Option = adaptor.WithBlockingEvents()
	_ adaptor.Option = adaptor.WithSkillProvider(nil)
	_ adaptor.Option = adaptor.WithSkillMaterializer(nil)
	_ adaptor.Option = adaptor.WithProfile(profile.Default())
	_ adaptor.Option = adaptor.WithWorkspaceManager(nil)
	_ adaptor.Option = adaptor.WithServiceManager(nil)

	// Call-scope-only options.
	_ adaptor.CallOption = adaptor.WithSchema[struct{}]()
	_ adaptor.CallOption = adaptor.WithSchemaJSON([]byte(`{"type":"object"}`))
)

// ---- Negative scope assertions (must NOT compile) ----
//
// The following misuse examples are intentionally comments: they are
// rejected by the compiler, which is the whole point. Error texts below are
// representative go1.26 outputs.
//
//	agent.Run(ctx, "p", adaptor.WithThreadStore(store))
//	  → cannot use adaptor.WithThreadStore(store) (value of interface type
//	    adaptor.Option) as adaptor.CallOption value in argument to agent.Run:
//	    adaptor.Option does not implement adaptor.CallOption
//	    (missing method ApplyRun)
//
//	agent.Run(ctx, "p", adaptor.WithEventBuffer(64))
//	  → same shape: missing method ApplyRun.
//
//	var _ adaptor.CallOption = adaptor.WithThreadStore(store)
//	  → same compile error, without needing an Agent in scope.
//
// The reverse direction is symmetric for call-scope schema options:
//
//	adaptor.New(d, adaptor.WithSchema[Review]())
//	  → adaptor.CallOption does not implement adaptor.Option
//	    (missing method ApplyNew)
//
// fails symmetrically.

// TestOptionMergeSemantics pins the one-sentence merge rule at the SPI
// boundary: nearer scope wins; skills append; everything else
// replaces.
func TestOptionMergeSemantics(t *testing.T) {
	setSkillCache(t)
	ctx := context.Background()
	fake := newFakeDriver()

	agent := adaptor.New(fake,
		adaptor.WithModel("default-model"),
		adaptor.WithInstructions("default instructions"),
		adaptor.WithPolicy(adaptor.Policy{Sandbox: adaptor.ReadOnly, WebSearch: adaptor.FeatureDeny}),
		adaptor.WithMetadata("env", "prod"),
		adaptor.WithMetadata("team", "sdk"),
		adaptor.WithIdentity(adaptor.Identity{ID: "agent-1", Tenant: "acme", Profile: "p-1", Name: "Default Agent"}),
		adaptor.WithSkills(skill.Inline("agent/base", "# base\n")),
	)

	// Run 1: override a subset at the call site.
	if _, err := agent.Run(ctx, "run-1",
		adaptor.WithModel("override-model"),
		adaptor.WithInstructions("override instructions"),
		adaptor.WithPolicy(adaptor.Policy{Sandbox: adaptor.WorkspaceWrite}),
		adaptor.WithMetadata("team", "app"),
		adaptor.WithSkills(skill.Inline("run/extra", "# extra\n")),
	); err != nil {
		t.Fatalf("run-1: %v", err)
	}
	req1 := fake.request(t, 0)

	// Nearer scope wins.
	if req1.ModelOverride != "override-model" {
		t.Errorf("model = %q, want call-site value", req1.ModelOverride)
	}
	if req1.Instructions == nil || req1.Instructions.Content != "override instructions" {
		t.Errorf("instructions = %+v, want call-site replacement", req1.Instructions)
	}

	// Policy replaces as a whole value, not field-wise: the call-site
	// Policy did not set WebSearch, so the default's FeatureDeny must NOT
	// survive.
	if req1.Policy.Isolation != adaptor.WorkspaceWrite {
		t.Errorf("policy.Isolation = %q, want workspace_write", req1.Policy.Isolation)
	}
	if req1.Policy.WebSearch != adaptor.FeatureInherit {
		t.Errorf("policy.WebSearch = %q, want inherit — Policy replaces wholesale, no field merge", req1.Policy.WebSearch)
	}

	// Metadata merges per key: overridden key takes the call-site value,
	// untouched keys keep their defaults.
	if req1.Metadata["team"] != "app" || req1.Metadata["env"] != "prod" {
		t.Errorf("metadata = %v, want env=prod team=app", req1.Metadata)
	}

	// Identity default (D11 four fields) flows into the SPI request.
	wantID := driver.AgentIdentity{ID: "agent-1", TenantID: "acme", ProfileID: "p-1", Name: "Default Agent"}
	if req1.Agent != wantID {
		t.Errorf("agent identity = %+v, want %+v", req1.Agent, wantID)
	}
	if got, want := req1.Skills.Keys(), []string{"agent/base", "run/extra"}; !slices.Equal(got, want) {
		t.Errorf("run-1 skills = %v, want append order %v", got, want)
	}

	// Run 2: no overrides — defaults intact, run-1 overrides gone.
	if _, err := agent.Run(ctx, "run-2"); err != nil {
		t.Fatalf("run-2: %v", err)
	}
	req2 := fake.request(t, 1)
	if req2.ModelOverride != "default-model" {
		t.Errorf("run-2 model = %q, want default back", req2.ModelOverride)
	}
	if req2.Policy.Isolation != adaptor.ReadOnly || req2.Policy.WebSearch != adaptor.FeatureDeny {
		t.Errorf("run-2 policy = %+v, want pristine default", req2.Policy)
	}
	if req2.Metadata["team"] != "sdk" {
		t.Errorf("run-2 metadata = %v, want team=sdk default back", req2.Metadata)
	}
	if got, want := req2.Skills.Keys(), []string{"agent/base"}; !slices.Equal(got, want) {
		t.Errorf("run-2 skills = %v, want pristine Agent defaults %v", got, want)
	}
}

// TestIdentityContextInjection verifies the pipeline injects the caller
// Identity into ctx before dispatching, readable via IdentityFromContext
// (the provider-side identity contract).
func TestIdentityContextInjection(t *testing.T) {
	fake := newFakeDriver()
	var seen adaptor.Identity
	var ok bool
	fake.runFunc = func(ctx context.Context, _ driver.Request, _ driver.EventSink) (driver.Response, error) {
		seen, ok = adaptor.IdentityFromContext(ctx)
		return driver.Response{Output: "ok"}, nil
	}

	agent := adaptor.New(fake, adaptor.WithIdentity(adaptor.Identity{Tenant: "acme", Profile: "p-9"}))
	if _, err := agent.Run(context.Background(), "p"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !ok {
		t.Fatal("IdentityFromContext: identity not injected")
	}
	if seen.Tenant != "acme" || seen.Profile != "p-9" {
		t.Errorf("injected identity = %+v", seen)
	}

	// Without WithIdentity nothing is injected.
	fake2 := newFakeDriver()
	var ok2 bool
	fake2.runFunc = func(ctx context.Context, _ driver.Request, _ driver.EventSink) (driver.Response, error) {
		_, ok2 = adaptor.IdentityFromContext(ctx)
		return driver.Response{Output: "ok"}, nil
	}
	if _, err := adaptor.New(fake2).Run(context.Background(), "p"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if ok2 {
		t.Error("identity injected without WithIdentity")
	}
}
