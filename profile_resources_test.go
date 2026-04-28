package agentadaptor_test

import (
	"context"
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
			Agents: []agentadaptor.AgentSpec{{Key: "reviewer", Content: "review things"}},
			Hooks:  []agentadaptor.HookSpec{{Key: "before", Event: "PreToolUse", Command: "echo", Args: []string{"ok"}}},
			Config: []agentadaptor.ProfileConfigPatch{{
				Key:      "sandbox",
				FileKind: agentadaptor.ProfileConfigFileJSON,
				Path:     "settings.json",
				Section:  "sandbox",
				Values:   map[string]any{"enabled": true},
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
	if len(payload.Agents.Agents) != 1 || payload.Agents.Agents[0].RuntimeName != "reviewer" {
		t.Fatalf("unexpected agent payload: %#v", payload.Agents)
	}
	if len(payload.Hooks.Hooks) != 1 || payload.Hooks.Hooks[0].Event != "PreToolUse" {
		t.Fatalf("unexpected hook payload: %#v", payload.Hooks)
	}
	if len(payload.Config.Patches) != 1 || payload.Config.Patches[0].Key != "sandbox" {
		t.Fatalf("unexpected config payload: %#v", payload.Config)
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
