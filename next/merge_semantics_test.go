package adaptor_test

// P3.7 mandated table: "WithSkills appends vs everything else replaces",
// asserted at the driver SPI boundary. This is the P3 growth of
// TestOptionMergeSemantics (options_scope_test.go), which anchored the rule
// for the P0 option families; this table pins it for the P3 families
// (skills, MCP, profile resources) plus one representative replacing family
// from P0 (model, instructions) so the contrast lives in a single test.
//
// Migration mapping (root baseline → here):
//
//	skill_contract_test.go   "additive merging"     → rows "skills: WithSkills appends across scopes",
//	                                                   "skills: profile-resources defaults append too"
//	mcp_sdk_test.go          default→override→clear → rows "mcp: run-scope WithMCP replaces",
//	                                                   "mcp: zero-arg WithMCP is an explicit clear"
//	profile_resources_test.go per-run replace       → rows "agents/hooks/config: run scope replaces"
//
// The identity-context rows of caller_identity_test.go migrate as
// TestIdentityFromContextContract below (attach-side coverage lives in
// TestIdentityContextInjection, options_scope_test.go).

import (
	"context"
	"testing"

	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/mcp"
	adaptor "github.com/agent-dance/agent-adaptor/next"
	"github.com/agent-dance/agent-adaptor/profile"
	"github.com/agent-dance/agent-adaptor/skill"
)

// capsFake returns a fakeDriver whose descriptor advertises every MCP
// transport, so option-merge rows that carry MCP servers pass capability
// validation. Type stays "fake" to match the default descriptor.
func capsFake() *fakeDriver {
	f := newFakeDriver()
	f.descriptor = &driver.Descriptor{
		Type:        "fake",
		DisplayName: "Fake Driver",
		MCP:         driver.MCPCapability{Supported: true, Stdio: true, HTTP: true, SSE: true},
	}
	return f
}

func TestWithSkillsAppendsEverythingElseReplaces(t *testing.T) {
	// Inline skills materialize to disk; keep the cache inside the test
	// sandbox. (Importing the root package also installs the default
	// materializer factory, exactly like production hosts do.)
	t.Setenv(skill.SkillCacheRootEnv, t.TempDir())
	ctx := context.Background()

	rows := []struct {
		name    string
		newOpts []adaptor.Option
		runOpts []adaptor.CallOption
		check   func(t *testing.T, req driver.Request)
	}{
		{
			name:    "skills: WithSkills appends across scopes",
			newOpts: []adaptor.Option{adaptor.WithSkills(skill.Inline("team/a", "# a"))},
			runOpts: []adaptor.CallOption{adaptor.WithSkills(skill.Inline("team/b", "# b"))},
			check: func(t *testing.T, req driver.Request) {
				if !equalUnordered(req.Skills.Keys(), []string{"team/a", "team/b"}) {
					t.Errorf("skills = %v, want default+run union (append, not replace)", req.Skills.Keys())
				}
			},
		},
		{
			name: "skills: profile-resources defaults append too",
			newOpts: []adaptor.Option{adaptor.WithProfileResources(profile.Resources{
				Skills: []adaptor.SkillRef{skill.Inline("team/base", "# base")},
			})},
			runOpts: []adaptor.CallOption{adaptor.WithSkills(skill.Inline("team/extra", "# extra"))},
			check: func(t *testing.T, req driver.Request) {
				if !equalUnordered(req.Skills.Keys(), []string{"team/base", "team/extra"}) {
					t.Errorf("skills = %v, want resources-default + run-append union", req.Skills.Keys())
				}
			},
		},
		{
			name:    "mcp: run-scope WithMCP replaces the default set",
			newOpts: []adaptor.Option{adaptor.WithMCP(mcp.Stdio("default-stdio", "npx", mcp.Args("default-server")))},
			runOpts: []adaptor.CallOption{adaptor.WithMCP(mcp.HTTP("remote-http", "https://example.com/mcp"))},
			check: func(t *testing.T, req driver.Request) {
				if len(req.MCP.Servers) != 1 || req.MCP.Servers[0].Key != "remote-http" {
					t.Errorf("mcp servers = %+v, want exactly [remote-http] — replace, not merge", req.MCP.Servers)
				}
			},
		},
		{
			name:    "mcp: zero-arg WithMCP is an explicit clear",
			newOpts: []adaptor.Option{adaptor.WithMCP(mcp.Stdio("default-stdio", "npx", mcp.Args("default-server")))},
			runOpts: []adaptor.CallOption{adaptor.WithMCP()},
			check: func(t *testing.T, req driver.Request) {
				if len(req.MCP.Servers) != 0 {
					t.Errorf("mcp servers = %+v, want none after zero-arg clear", req.MCP.Servers)
				}
			},
		},
		{
			name:    "model: run scope replaces",
			newOpts: []adaptor.Option{adaptor.WithModel("m1")},
			runOpts: []adaptor.CallOption{adaptor.WithModel("m2")},
			check: func(t *testing.T, req driver.Request) {
				if req.ModelOverride != "m2" {
					t.Errorf("model = %q, want call-site replacement m2", req.ModelOverride)
				}
			},
		},
		{
			name:    "instructions: run scope replaces",
			newOpts: []adaptor.Option{adaptor.WithInstructions("default instructions")},
			runOpts: []adaptor.CallOption{adaptor.WithInstructions("per call")},
			check: func(t *testing.T, req driver.Request) {
				if req.Instructions == nil || req.Instructions.Content != "per call" {
					t.Errorf("instructions = %+v, want call-site replacement", req.Instructions)
				}
			},
		},
		{
			name: "agents: run scope replaces the declared set",
			newOpts: []adaptor.Option{adaptor.WithProfileResources(profile.Resources{
				Agents: []profile.SubAgent{{Key: "default-agent", Instructions: "default"}},
			})},
			runOpts: []adaptor.CallOption{adaptor.WithProfileResources(profile.Resources{
				Agents: []profile.SubAgent{{Key: "run-agent", Instructions: "run"}},
			})},
			check: func(t *testing.T, req driver.Request) {
				agents := req.ProfilePayload.Agents.Agents
				if len(agents) != 1 || agents[0].Key != "run-agent" {
					t.Errorf("agents = %+v, want exactly [run-agent]", agents)
				}
				if !req.ProfilePayload.Declared.Agents {
					t.Error("Declared.Agents = false, want true after explicit declaration")
				}
			},
		},
		{
			name: "hooks: run scope replaces the declared set",
			newOpts: []adaptor.Option{adaptor.WithProfileResources(profile.Resources{
				Hooks: []profile.Hook{{
					Key:         "default-hook",
					Event:       profile.HookEventPreTool,
					MatcherSpec: profile.HookMatcher{Subject: profile.HookMatcherSubjectTool, Syntax: profile.HookMatcherSyntaxRegex, Pattern: ".*"},
					Handler:     profile.HookHandler{Type: profile.HookHandlerCommand, Command: "echo", Args: []string{"default"}},
					FailPolicy:  profile.HookFailPolicyOpen,
				}},
			})},
			runOpts: []adaptor.CallOption{adaptor.WithProfileResources(profile.Resources{
				Hooks: []profile.Hook{{
					Key:         "run-hook",
					Event:       profile.HookEventPreTool,
					MatcherSpec: profile.HookMatcher{Subject: profile.HookMatcherSubjectTool, Syntax: profile.HookMatcherSyntaxRegex, Pattern: ".*"},
					Handler:     profile.HookHandler{Type: profile.HookHandlerCommand, Command: "echo", Args: []string{"run"}},
					FailPolicy:  profile.HookFailPolicyOpen,
				}},
			})},
			check: func(t *testing.T, req driver.Request) {
				hooks := req.ProfilePayload.Hooks.Hooks
				if len(hooks) != 1 || hooks[0].Key != "run-hook" {
					t.Errorf("hooks = %+v, want exactly [run-hook]", hooks)
				}
				if !req.ProfilePayload.Declared.Hooks {
					t.Error("Declared.Hooks = false, want true after explicit declaration")
				}
			},
		},
		{
			name: "config: run scope replaces the declared set",
			newOpts: []adaptor.Option{adaptor.WithProfileResources(profile.Resources{
				Config: []profile.ConfigPatch{{Key: "default-config", Capability: "sandbox", Values: map[string]any{"enabled": true}}},
			})},
			runOpts: []adaptor.CallOption{adaptor.WithProfileResources(profile.Resources{
				Config: []profile.ConfigPatch{{Key: "run-config", Capability: "sandbox", Values: map[string]any{"enabled": false}}},
			})},
			check: func(t *testing.T, req driver.Request) {
				patches := req.ProfilePayload.Config.Patches
				if len(patches) != 1 || patches[0].Key != "run-config" {
					t.Errorf("config patches = %+v, want exactly [run-config]", patches)
				}
				if !req.ProfilePayload.Declared.Config {
					t.Error("Declared.Config = false, want true after explicit declaration")
				}
			},
		},
	}

	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			fake := capsFake()
			agent := adaptor.New(fake, row.newOpts...)
			if _, err := agent.Run(ctx, "merge probe", row.runOpts...); err != nil {
				t.Fatalf("Run: %v", err)
			}
			row.check(t, fake.lastRequest(t))

			// A second bare run proves the per-call value did not leak
			// into the agent defaults (clone-on-run isolation).
			if _, err := agent.Run(ctx, "isolation probe"); err != nil {
				t.Fatalf("isolation run: %v", err)
			}
		})
	}
}

// TestIdentityFromContextContract migrates the read-side rows of the legacy
// caller_identity_test.go baseline: nil and identity-free contexts report
// ok=false with a zero Identity. (The attach side — SDK injection before the
// driver runs — is TestIdentityContextInjection; next/ deliberately has no
// public WithCallerIdentity equivalent, providers receive identity only via
// SDK plumbing.)
func TestIdentityFromContextContract(t *testing.T) {
	//lint:ignore SA1012 the nil-context leg is the documented contract.
	var nilCtx context.Context
	if id, ok := adaptor.IdentityFromContext(nilCtx); ok || id != (adaptor.Identity{}) {
		t.Errorf("nil ctx: got (%+v, %v), want zero identity and ok=false", id, ok)
	}
	if id, ok := adaptor.IdentityFromContext(context.Background()); ok || id != (adaptor.Identity{}) {
		t.Errorf("empty ctx: got (%+v, %v), want zero identity and ok=false", id, ok)
	}
}
