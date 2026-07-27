package adaptor_test

// P3.7 migration of the root profile-resources baseline
// (profile_resources_test.go) onto the v1 surface. Mapping (roots stay
// untouched):
//
//	TestProfileResourcesResolveIntoProfilePayload      → TestProfileResourcesResolveIntoProfilePayload
//	TestRunWithoutProfileResourcesLeavesOptionalKindsUndeclared → TestRunWithoutProfileResourcesLeavesOptionalKindsUndeclared
//	TestExplicitEmptyAgentsAreDeclared                 → TestExplicitEmptyResourceKindsAreDeclared (widened to hooks/config/instructions)
//	TestProfileResourcesPerRunOverridesReplaceResourceKinds → merge_semantics_test.go rows "agents/hooks/config: run scope replaces"
//	TestProfileResourcesRejectAmbiguousInstructionSources → TestProfileResourcesRejectAmbiguousInstructionSources
//	TestInstructionPathFingerprintChangesWithFileContent  → TestInstructionPathFingerprintChangesWithFileContent
//	TestAgentSourcePathFingerprintChangesWithFileContent  → TestAgentSourcePathFingerprintChangesWithFileContent
//	TestAgentSourcePathFingerprintChangesWithDirectoryContent → TestAgentSourcePathFingerprintChangesWithDirectoryContent
//	TestProfileResourcesRejectDivergentAgentContentAlias  → TestProfileResourcesRejectDivergentAgentContentAlias
//	TestProfileResourcesRejectConfigCapabilityAndNativeTarget → TestProfileResourcesRejectConfigCapabilityAndNativeTarget
//	TestProfileSnapshotFallbackReportsSupportAndMaterialization → TestProfileStateFallbackReportsSupportAndMaterialization
//
// v1 deltas: the dedicated WithDefaultAgents/WithDefaultHooks/... options
// collapse into WithProfileResources (per-kind merge rules unchanged), the
// Admin().Default().ProfileSnapshot verb becomes Agent.ProfileState, and the
// rejection tests additionally pin that the failure is pre-launch (driver
// never runs) — a strengthening, not a change, of the root assertion.

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/mcp"
	adaptor "github.com/agent-dance/agent-adaptor/next"
	"github.com/agent-dance/agent-adaptor/profile"
	"github.com/agent-dance/agent-adaptor/skill"
)

func TestProfileResourcesResolveIntoProfilePayload(t *testing.T) {
	t.Setenv(skill.SkillCacheRootEnv, t.TempDir())
	fake := capsFake()
	agent := adaptor.New(fake, adaptor.WithProfileResources(profile.Resources{
		Skills: []adaptor.SkillRef{skill.Inline("team/default", "# default")},
		MCP: []mcp.Server{{
			Key:       "default-mcp",
			Transport: driver.MCPTransportStdio,
			Command:   "npx",
		}},
		Agents: []profile.SubAgent{{
			Key:             "reviewer",
			Description:     "Review code changes",
			Instructions:    "review things",
			Model:           "gpt-test",
			ReasoningEffort: "medium",
			ToolPolicy:      &profile.ToolPolicy{Allow: []string{"shell"}},
			MCPServers:      []string{"default-mcp"},
			Native:          map[string]any{"provider": "native"},
		}},
		Hooks: []profile.Hook{{
			Key:         "before",
			Event:       "PreToolUse", // legacy event alias — normalized into HookEventPreTool
			MatcherSpec: profile.HookMatcher{Subject: profile.HookMatcherSubjectTool, Syntax: profile.HookMatcherSyntaxRegex, Pattern: ".*"},
			Handler:     profile.HookHandler{Type: profile.HookHandlerCommand, Command: "echo", Args: []string{"ok"}},
			FailPolicy:  profile.HookFailPolicyOpen,
		}},
		Config: []profile.ConfigPatch{{
			Key:        "sandbox",
			Capability: "sandbox",
			Values:     map[string]any{"enabled": true},
		}},
	}))

	if _, err := agent.Run(context.Background(), "hello"); err != nil {
		t.Fatalf("run: %v", err)
	}
	payload := fake.lastRequest(t).ProfilePayload
	if payload.Fingerprint == "" {
		t.Fatal("expected profile payload fingerprint")
	}
	if len(payload.Skills.Entries) != 1 || payload.Skills.Entries[0].Key != "team/default" {
		t.Fatalf("unexpected skills payload: %#v", payload.Skills)
	}
	if len(payload.MCP.Servers) != 1 || payload.MCP.Servers[0].Key != "default-mcp" {
		t.Fatalf("unexpected MCP payload: %#v", payload.MCP)
	}
	if len(payload.Agents.Agents) != 1 || payload.Agents.Agents[0].RuntimeName != "reviewer" || payload.Agents.Agents[0].Instructions != "review things" {
		t.Fatalf("unexpected agent payload: %#v", payload.Agents)
	}
	if len(payload.Hooks.Hooks) != 1 || payload.Hooks.Hooks[0].Event != driver.HookEventPreTool || payload.Hooks.Hooks[0].Handler.Command != "echo" {
		t.Fatalf("unexpected hook payload: %#v", payload.Hooks)
	}
	if len(payload.Config.Patches) != 1 || payload.Config.Patches[0].Capability != "sandbox" {
		t.Fatalf("unexpected config payload: %#v", payload.Config)
	}
	if !payload.Declared.Agents || !payload.Declared.Hooks || !payload.Declared.Config {
		t.Fatalf("expected declared profile resources, got %#v", payload.Declared)
	}
}

func TestRunWithoutProfileResourcesLeavesOptionalKindsUndeclared(t *testing.T) {
	fake := newFakeDriver()
	if _, err := adaptor.New(fake).Run(context.Background(), "hello"); err != nil {
		t.Fatalf("run: %v", err)
	}
	declared := fake.lastRequest(t).ProfilePayload.Declared
	if declared.Agents || declared.Hooks || declared.Instructions || declared.Config {
		t.Fatalf("expected optional profile resources to be undeclared, got %#v", declared)
	}
}

// TestExplicitEmptyResourceKindsAreDeclared: a non-nil empty slice in
// WithProfileResources means "explicitly none" — declared, with zero
// entries — while instructions declare-and-clear via WithInstructions("").
// This widens the root TestExplicitEmptyAgentsAreDeclared to every optional
// kind, because v1 has no per-kind zero-arg options.
func TestExplicitEmptyResourceKindsAreDeclared(t *testing.T) {
	fake := newFakeDriver()
	agent := adaptor.New(fake)

	if _, err := agent.Run(context.Background(), "hello",
		adaptor.WithProfileResources(profile.Resources{
			Agents: []profile.SubAgent{},
			Hooks:  []profile.Hook{},
			Config: []profile.ConfigPatch{},
		}),
		adaptor.WithInstructions(""),
	); err != nil {
		t.Fatalf("run: %v", err)
	}
	req := fake.lastRequest(t)
	payload := req.ProfilePayload
	if !payload.Declared.Agents || len(payload.Agents.Agents) != 0 {
		t.Errorf("agents: declared=%v entries=%#v, want explicit empty declaration", payload.Declared.Agents, payload.Agents.Agents)
	}
	if !payload.Declared.Hooks || len(payload.Hooks.Hooks) != 0 {
		t.Errorf("hooks: declared=%v entries=%#v, want explicit empty declaration", payload.Declared.Hooks, payload.Hooks.Hooks)
	}
	if !payload.Declared.Config || len(payload.Config.Patches) != 0 {
		t.Errorf("config: declared=%v entries=%#v, want explicit empty declaration", payload.Declared.Config, payload.Config.Patches)
	}
	if !payload.Declared.Instructions {
		t.Error("instructions: WithInstructions(\"\") must declare while clearing")
	}
	if req.Instructions != nil {
		t.Errorf("instructions = %#v, want cleared", req.Instructions)
	}
}

func TestProfileResourcesRejectAmbiguousInstructionSources(t *testing.T) {
	fake := newFakeDriver()
	agent := adaptor.New(fake, adaptor.WithProfileResources(profile.Resources{
		Instructions: &profile.Instructions{Path: "AGENTS.md", Content: "inline"},
	}))

	if _, err := agent.Run(context.Background(), "hello"); err == nil {
		t.Fatal("expected instructions path/content conflict to fail")
	}
	if fake.runCount() != 0 {
		t.Errorf("driver ran %d time(s), want pre-launch failure", fake.runCount())
	}
}

func TestInstructionPathFingerprintChangesWithFileContent(t *testing.T) {
	ctx := context.Background()
	fake := newFakeDriver()
	path := filepath.Join(t.TempDir(), "AGENTS.md")
	if err := os.WriteFile(path, []byte("version one"), 0o644); err != nil {
		t.Fatalf("write instructions v1: %v", err)
	}
	agent := adaptor.New(fake, adaptor.WithProfileResources(profile.Resources{
		Instructions: &profile.Instructions{ID: "team", Path: path},
	}))

	if _, err := agent.Run(ctx, "hello"); err != nil {
		t.Fatalf("first run: %v", err)
	}
	firstPayload := fake.request(t, 0).ProfilePayload
	first := firstPayload.Fingerprint
	firstInstructions := firstPayload.Instructions.Fingerprint
	if first == "" || firstInstructions == "" {
		t.Fatalf("expected instruction file fingerprints, got profile=%q instructions=%q", first, firstInstructions)
	}
	if err := os.WriteFile(path, []byte("version two"), 0o644); err != nil {
		t.Fatalf("write instructions v2: %v", err)
	}
	if _, err := agent.Run(ctx, "hello"); err != nil {
		t.Fatalf("second run: %v", err)
	}
	secondPayload := fake.request(t, 1).ProfilePayload
	if secondPayload.Fingerprint == first {
		t.Error("expected profile fingerprint to change after instruction file content changed")
	}
	if secondPayload.Instructions.Fingerprint == firstInstructions {
		t.Error("expected instruction fingerprint to change after instruction file content changed")
	}
}

func TestAgentSourcePathFingerprintChangesWithFileContent(t *testing.T) {
	ctx := context.Background()
	fake := newFakeDriver()
	path := filepath.Join(t.TempDir(), "reviewer.md")
	if err := os.WriteFile(path, []byte("review version one"), 0o644); err != nil {
		t.Fatalf("write agent v1: %v", err)
	}
	agent := adaptor.New(fake, adaptor.WithProfileResources(profile.Resources{
		Agents: []profile.SubAgent{{Key: "reviewer", SourcePath: path}},
	}))

	if _, err := agent.Run(ctx, "hello"); err != nil {
		t.Fatalf("first run: %v", err)
	}
	firstPayload := fake.request(t, 0).ProfilePayload
	firstProfile := firstPayload.Fingerprint
	firstAgents := firstPayload.Agents.Fingerprint
	firstSource := firstPayload.Agents.Agents[0].SourceFingerprint
	if firstProfile == "" || firstAgents == "" || firstSource == "" {
		t.Fatalf("expected agent source fingerprints, got profile=%q agents=%q source=%q", firstProfile, firstAgents, firstSource)
	}
	if err := os.WriteFile(path, []byte("review version two"), 0o644); err != nil {
		t.Fatalf("write agent v2: %v", err)
	}
	if _, err := agent.Run(ctx, "hello"); err != nil {
		t.Fatalf("second run: %v", err)
	}
	secondPayload := fake.request(t, 1).ProfilePayload
	if secondPayload.Fingerprint == firstProfile {
		t.Error("expected profile fingerprint to change after agent source content changed")
	}
	if secondPayload.Agents.Fingerprint == firstAgents {
		t.Error("expected agent payload fingerprint to change after agent source content changed")
	}
	if secondPayload.Agents.Agents[0].SourceFingerprint == firstSource {
		t.Error("expected source fingerprint to change after agent source content changed")
	}
}

func TestAgentSourcePathFingerprintChangesWithDirectoryContent(t *testing.T) {
	ctx := context.Background()
	fake := newFakeDriver()
	sourceDir := t.TempDir()
	path := filepath.Join(sourceDir, "reviewer.md")
	if err := os.WriteFile(path, []byte("review version one"), 0o644); err != nil {
		t.Fatalf("write agent v1: %v", err)
	}
	agent := adaptor.New(fake, adaptor.WithProfileResources(profile.Resources{
		Agents: []profile.SubAgent{{Key: "reviewer", SourcePath: sourceDir}},
	}))

	if _, err := agent.Run(ctx, "hello"); err != nil {
		t.Fatalf("first run: %v", err)
	}
	firstPayload := fake.request(t, 0).ProfilePayload
	firstProfile := firstPayload.Fingerprint
	firstAgents := firstPayload.Agents.Fingerprint
	firstSource := firstPayload.Agents.Agents[0].SourceFingerprint
	if firstProfile == "" || firstAgents == "" || firstSource == "" {
		t.Fatalf("expected agent source fingerprints, got profile=%q agents=%q source=%q", firstProfile, firstAgents, firstSource)
	}
	if err := os.WriteFile(path, []byte("review version two"), 0o644); err != nil {
		t.Fatalf("write agent v2: %v", err)
	}
	if _, err := agent.Run(ctx, "hello"); err != nil {
		t.Fatalf("second run: %v", err)
	}
	secondPayload := fake.request(t, 1).ProfilePayload
	if secondPayload.Fingerprint == firstProfile {
		t.Error("expected profile fingerprint to change after directory source content changed")
	}
	if secondPayload.Agents.Fingerprint == firstAgents {
		t.Error("expected agent payload fingerprint to change after directory source content changed")
	}
	if secondPayload.Agents.Agents[0].SourceFingerprint == firstSource {
		t.Error("expected source fingerprint to change after directory source content changed")
	}
}

func TestProfileResourcesRejectDivergentAgentContentAlias(t *testing.T) {
	fake := newFakeDriver()
	agent := adaptor.New(fake, adaptor.WithProfileResources(profile.Resources{
		Agents: []profile.SubAgent{{Key: "reviewer", Instructions: "new", Content: "old"}},
	}))

	if _, err := agent.Run(context.Background(), "hello"); err == nil {
		t.Fatal("expected divergent instructions/content alias to fail")
	}
	if fake.runCount() != 0 {
		t.Errorf("driver ran %d time(s), want pre-launch failure", fake.runCount())
	}
}

func TestProfileResourcesRejectConfigCapabilityAndNativeTarget(t *testing.T) {
	fake := newFakeDriver()
	agent := adaptor.New(fake, adaptor.WithProfileResources(profile.Resources{
		Config: []profile.ConfigPatch{{
			Key:        "ambiguous",
			Capability: "sandbox",
			FileKind:   profile.ConfigFileTOML,
			Path:       "config.toml",
		}},
	}))

	if _, err := agent.Run(context.Background(), "hello"); err == nil {
		t.Fatal("expected config capability/native conflict to fail")
	}
	if fake.runCount() != 0 {
		t.Errorf("driver ran %d time(s), want pre-launch failure", fake.runCount())
	}
}

// TestProfileStateFallbackReportsSupportAndMaterialization: for a driver
// without the profile-resource extension, ProfileState builds the snapshot
// from the desired payload and reports honestly — skills are the portable
// file-managed resource, agents are desired-but-unsupported, and inline
// instructions still carry a content fingerprint.
func TestProfileStateFallbackReportsSupportAndMaterialization(t *testing.T) {
	agent := adaptor.New(newFakeDriver(), adaptor.WithProfileResources(profile.Resources{
		Agents:       []profile.SubAgent{{Key: "reviewer", Instructions: "review"}},
		Instructions: profile.Text("inline"),
	}))

	snapshot, err := agent.ProfileState(context.Background())
	if err != nil {
		t.Fatalf("profile state: %v", err)
	}
	skills := profileResourceByKind(t, snapshot, adaptor.ProfileResourceSkills)
	if skills.Support != adaptor.ProfileResourceSupportPortableCore || skills.Materialization != adaptor.ProfileResourceMaterializationFileManaged {
		t.Fatalf("unexpected skills support status: %#v", skills)
	}
	agents := profileResourceByKind(t, snapshot, adaptor.ProfileResourceAgents)
	if agents.Support != adaptor.ProfileResourceSupportUnsupported || agents.Materialization != adaptor.ProfileResourceMaterializationNotMaterialized {
		t.Fatalf("unexpected agents support status: %#v", agents)
	}
	instructions := profileResourceByKind(t, snapshot, adaptor.ProfileResourceInstructions)
	if instructions.Fingerprint == "" {
		t.Fatalf("expected inline instructions fingerprint, got %#v", instructions)
	}
}

func profileResourceByKind(t *testing.T, snapshot adaptor.ProfileSnapshot, kind adaptor.ProfileResourceKind) adaptor.ResourceSnapshot {
	t.Helper()
	for _, resource := range snapshot.Resources {
		if resource.Kind == kind {
			return resource
		}
	}
	t.Fatalf("missing resource %s in %#v", kind, snapshot.Resources)
	return adaptor.ResourceSnapshot{}
}
