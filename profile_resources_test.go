package agentadaptor_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	agentadaptor "github.com/agent-dance/agent-adaptor"
)

func TestProfileResourcesResolveIntoProfilePayload(t *testing.T) {
	driver := &fakeDriver{
		mcpCapability: agentadaptor.MCPCapability{Supported: true, Stdio: true, HTTP: true, SSE: true},
	}
	sdk := newSDK(nil, fakeBinding("default", driver,
		agentadaptor.WithDefaultProfileResources(agentadaptor.ProfileResources{
			Skills: []agentadaptor.SkillRef{agentadaptor.InlineSkill("team/default", "# default")},
			MCP: &agentadaptor.MCPConfig{Servers: []agentadaptor.MCPServerSpec{{
				Key:       "default-mcp",
				Transport: agentadaptor.MCPTransportStdio,
				Command:   "npx",
			}}},
			Agents: []agentadaptor.AgentSpec{{
				Key:             "reviewer",
				Description:     "Review code changes",
				Instructions:    "review things",
				Model:           "gpt-test",
				ReasoningEffort: "medium",
				ToolPolicy:      &agentadaptor.AgentToolPolicy{Allow: []string{"shell"}},
				MCPServers:      []string{"default-mcp"},
				Native:          map[string]any{"provider": "native"},
			}},
			Hooks: []agentadaptor.HookSpec{{
				Key:         "before",
				Event:       "PreToolUse",
				MatcherSpec: agentadaptor.HookMatcher{Subject: agentadaptor.HookMatcherSubjectTool, Syntax: agentadaptor.HookMatcherSyntaxRegex, Pattern: ".*"},
				Handler:     agentadaptor.HookHandler{Type: agentadaptor.HookHandlerCommand, Command: "echo", Args: []string{"ok"}},
				FailPolicy:  agentadaptor.HookFailPolicyOpen,
			}},
			Config: []agentadaptor.ProfileConfigPatch{{
				Key:        "sandbox",
				Capability: "sandbox",
				Values:     map[string]any{"enabled": true},
			}},
		}),
	), nil)

	if _, err := sdk.Run(context.Background(), "hello"); err != nil {
		t.Fatalf("run: %v", err)
	}
	payload := driver.lastProfile
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
	if len(payload.Hooks.Hooks) != 1 || payload.Hooks.Hooks[0].Event != agentadaptor.HookEventPreTool || payload.Hooks.Hooks[0].Handler.Command != "echo" {
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
	driver := &fakeDriver{}
	sdk := newSDK(nil, fakeBinding("default", driver), nil)

	if _, err := sdk.Run(context.Background(), "hello"); err != nil {
		t.Fatalf("run: %v", err)
	}
	if driver.lastProfile.Declared.Agents || driver.lastProfile.Declared.Hooks || driver.lastProfile.Declared.Instructions || driver.lastProfile.Declared.Config {
		t.Fatalf("expected optional profile resources to be undeclared, got %#v", driver.lastProfile.Declared)
	}
}

func TestExplicitEmptyAgentsAreDeclared(t *testing.T) {
	driver := &fakeDriver{}
	sdk := newSDK(nil, fakeBinding("default", driver), nil)

	if _, err := sdk.Run(context.Background(), "hello", agentadaptor.WithAgents()); err != nil {
		t.Fatalf("run: %v", err)
	}
	if !driver.lastProfile.Declared.Agents || len(driver.lastProfile.Agents.Agents) != 0 {
		t.Fatalf("expected explicit empty agents declaration, got declared=%#v agents=%#v", driver.lastProfile.Declared, driver.lastProfile.Agents)
	}
}

func TestProfileResourcesPerRunOverridesReplaceResourceKinds(t *testing.T) {
	driver := &fakeDriver{
		mcpCapability: agentadaptor.MCPCapability{Supported: true, Stdio: true, HTTP: true, SSE: true},
	}
	sdk := newSDK(nil, fakeBinding("default", driver,
		agentadaptor.WithDefaultAgents(agentadaptor.AgentSpec{Key: "default-agent"}),
		agentadaptor.WithDefaultHooks(agentadaptor.HookSpec{Key: "default-hook", Event: "PreToolUse", Command: "echo"}),
		agentadaptor.WithDefaultProfileConfig(agentadaptor.ProfileConfigPatch{Key: "default-config", FileKind: agentadaptor.ProfileConfigFileJSON, Path: "settings.json"}),
	), nil)

	if _, err := sdk.Run(context.Background(), "hello",
		agentadaptor.WithAgents(agentadaptor.AgentSpec{Key: "run-agent"}),
		agentadaptor.WithHooks(agentadaptor.HookSpec{Key: "run-hook", Event: "PostToolUse", Command: "echo"}),
		agentadaptor.WithProfileConfig(agentadaptor.ProfileConfigPatch{Key: "run-config", FileKind: agentadaptor.ProfileConfigFileTOML, Path: "config.toml"}),
	); err != nil {
		t.Fatalf("run: %v", err)
	}
	payload := driver.lastProfile
	if got := payload.Agents.Agents[0].Key; got != "run-agent" {
		t.Fatalf("expected per-run agents to replace defaults, got %q", got)
	}
	if got := payload.Hooks.Hooks[0].Key; got != "run-hook" {
		t.Fatalf("expected per-run hooks to replace defaults, got %q", got)
	}
	if got := payload.Config.Patches[0].Key; got != "run-config" {
		t.Fatalf("expected per-run config to replace defaults, got %q", got)
	}
}

func TestProfileResourcesRejectAmbiguousInstructionSources(t *testing.T) {
	sdk := newSDK(nil, fakeBinding("default", &fakeDriver{},
		agentadaptor.WithDefaultInstructions(&agentadaptor.InstructionsBundleRef{Path: "AGENTS.md", Content: "inline"}),
	), nil)

	if _, err := sdk.Run(context.Background(), "hello"); err == nil {
		t.Fatal("expected instructions path/content conflict to fail")
	}
}

func TestInstructionPathFingerprintChangesWithFileContent(t *testing.T) {
	driver := &fakeDriver{}
	path := filepath.Join(t.TempDir(), "AGENTS.md")
	if err := os.WriteFile(path, []byte("version one"), 0o644); err != nil {
		t.Fatalf("write instructions v1: %v", err)
	}
	sdk := newSDK(nil, fakeBinding("default", driver,
		agentadaptor.WithDefaultInstructions(&agentadaptor.InstructionsBundleRef{ID: "team", Path: path}),
	), nil)

	if _, err := sdk.Run(context.Background(), "hello"); err != nil {
		t.Fatalf("first run: %v", err)
	}
	first := driver.lastProfile.Fingerprint
	firstInstructions := driver.lastProfile.Instructions.Fingerprint
	if first == "" || firstInstructions == "" {
		t.Fatalf("expected instruction file fingerprints, got profile=%q instructions=%q", first, firstInstructions)
	}
	if err := os.WriteFile(path, []byte("version two"), 0o644); err != nil {
		t.Fatalf("write instructions v2: %v", err)
	}
	if _, err := sdk.Run(context.Background(), "hello"); err != nil {
		t.Fatalf("second run: %v", err)
	}
	if driver.lastProfile.Fingerprint == first {
		t.Fatalf("expected profile fingerprint to change after instruction file content changed")
	}
	if driver.lastProfile.Instructions.Fingerprint == firstInstructions {
		t.Fatalf("expected instruction fingerprint to change after instruction file content changed")
	}
}

func TestAgentSourcePathFingerprintChangesWithFileContent(t *testing.T) {
	driver := &fakeDriver{}
	path := filepath.Join(t.TempDir(), "reviewer.md")
	if err := os.WriteFile(path, []byte("review version one"), 0o644); err != nil {
		t.Fatalf("write agent v1: %v", err)
	}
	sdk := newSDK(nil, fakeBinding("default", driver,
		agentadaptor.WithDefaultAgents(agentadaptor.AgentSpec{Key: "reviewer", SourcePath: path}),
	), nil)

	if _, err := sdk.Run(context.Background(), "hello"); err != nil {
		t.Fatalf("first run: %v", err)
	}
	firstProfile := driver.lastProfile.Fingerprint
	firstAgents := driver.lastProfile.Agents.Fingerprint
	firstSource := driver.lastProfile.Agents.Agents[0].SourceFingerprint
	if firstProfile == "" || firstAgents == "" || firstSource == "" {
		t.Fatalf("expected agent source fingerprints, got profile=%q agents=%q source=%q", firstProfile, firstAgents, firstSource)
	}
	if err := os.WriteFile(path, []byte("review version two"), 0o644); err != nil {
		t.Fatalf("write agent v2: %v", err)
	}
	if _, err := sdk.Run(context.Background(), "hello"); err != nil {
		t.Fatalf("second run: %v", err)
	}
	if driver.lastProfile.Fingerprint == firstProfile {
		t.Fatalf("expected profile fingerprint to change after agent source content changed")
	}
	if driver.lastProfile.Agents.Fingerprint == firstAgents {
		t.Fatalf("expected agent payload fingerprint to change after agent source content changed")
	}
	if driver.lastProfile.Agents.Agents[0].SourceFingerprint == firstSource {
		t.Fatalf("expected source fingerprint to change after agent source content changed")
	}
}

func TestAgentSourcePathFingerprintChangesWithDirectoryContent(t *testing.T) {
	driver := &fakeDriver{}
	sourceDir := t.TempDir()
	path := filepath.Join(sourceDir, "reviewer.md")
	if err := os.WriteFile(path, []byte("review version one"), 0o644); err != nil {
		t.Fatalf("write agent v1: %v", err)
	}
	sdk := newSDK(nil, fakeBinding("default", driver,
		agentadaptor.WithDefaultAgents(agentadaptor.AgentSpec{Key: "reviewer", SourcePath: sourceDir}),
	), nil)

	if _, err := sdk.Run(context.Background(), "hello"); err != nil {
		t.Fatalf("first run: %v", err)
	}
	firstProfile := driver.lastProfile.Fingerprint
	firstAgents := driver.lastProfile.Agents.Fingerprint
	firstSource := driver.lastProfile.Agents.Agents[0].SourceFingerprint
	if firstProfile == "" || firstAgents == "" || firstSource == "" {
		t.Fatalf("expected agent source fingerprints, got profile=%q agents=%q source=%q", firstProfile, firstAgents, firstSource)
	}
	if err := os.WriteFile(path, []byte("review version two"), 0o644); err != nil {
		t.Fatalf("write agent v2: %v", err)
	}
	if _, err := sdk.Run(context.Background(), "hello"); err != nil {
		t.Fatalf("second run: %v", err)
	}
	if driver.lastProfile.Fingerprint == firstProfile {
		t.Fatalf("expected profile fingerprint to change after directory source content changed")
	}
	if driver.lastProfile.Agents.Fingerprint == firstAgents {
		t.Fatalf("expected agent payload fingerprint to change after directory source content changed")
	}
	if driver.lastProfile.Agents.Agents[0].SourceFingerprint == firstSource {
		t.Fatalf("expected source fingerprint to change after directory source content changed")
	}
}

func TestProfileResourcesRejectDivergentAgentContentAlias(t *testing.T) {
	sdk := newSDK(nil, fakeBinding("default", &fakeDriver{},
		agentadaptor.WithDefaultAgents(agentadaptor.AgentSpec{Key: "reviewer", Instructions: "new", Content: "old"}),
	), nil)

	if _, err := sdk.Run(context.Background(), "hello"); err == nil {
		t.Fatal("expected divergent instructions/content alias to fail")
	}
}

func TestProfileResourcesRejectConfigCapabilityAndNativeTarget(t *testing.T) {
	sdk := newSDK(nil, fakeBinding("default", &fakeDriver{},
		agentadaptor.WithDefaultProfileConfig(agentadaptor.ProfileConfigPatch{
			Key:        "ambiguous",
			Capability: "sandbox",
			FileKind:   agentadaptor.ProfileConfigFileTOML,
			Path:       "config.toml",
		}),
	), nil)

	if _, err := sdk.Run(context.Background(), "hello"); err == nil {
		t.Fatal("expected config capability/native conflict to fail")
	}
}

func TestProfileSnapshotFallbackReportsSupportAndMaterialization(t *testing.T) {
	sdk := newSDK(nil, fakeBinding("default", &fakeDriver{},
		agentadaptor.WithDefaultAgents(agentadaptor.AgentSpec{Key: "reviewer", Instructions: "review"}),
		agentadaptor.WithDefaultInstructions(&agentadaptor.InstructionsBundleRef{Content: "inline"}),
	), nil)

	snapshot, err := sdk.Admin().Default().ProfileSnapshot(context.Background())
	if err != nil {
		t.Fatalf("profile snapshot: %v", err)
	}
	skills := profileResourceByKind(t, snapshot, agentadaptor.ProfileResourceSkills)
	if skills.Support != agentadaptor.ProfileResourceSupportPortableCore || skills.Materialization != agentadaptor.ProfileResourceMaterializationFileManaged {
		t.Fatalf("unexpected skills support status: %#v", skills)
	}
	agents := profileResourceByKind(t, snapshot, agentadaptor.ProfileResourceAgents)
	if agents.Support != agentadaptor.ProfileResourceSupportUnsupported || agents.Materialization != agentadaptor.ProfileResourceMaterializationNotMaterialized {
		t.Fatalf("unexpected agents support status: %#v", agents)
	}
	instructions := profileResourceByKind(t, snapshot, agentadaptor.ProfileResourceInstructions)
	if instructions.Fingerprint == "" {
		t.Fatalf("expected inline instructions fingerprint, got %#v", instructions)
	}
}

func profileResourceByKind(t *testing.T, snapshot agentadaptor.ProfileSnapshot, kind agentadaptor.ProfileResourceKind) agentadaptor.ResourceSnapshot {
	t.Helper()
	for _, resource := range snapshot.Resources {
		if resource.Kind == kind {
			return resource
		}
	}
	t.Fatalf("missing resource %s in %#v", kind, snapshot.Resources)
	return agentadaptor.ResourceSnapshot{}
}
