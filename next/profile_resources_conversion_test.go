package adaptor

import (
	"testing"

	"github.com/agent-dance/agent-adaptor/mcp"
	"github.com/agent-dance/agent-adaptor/profile"
	"github.com/agent-dance/agent-adaptor/skill"
)

func TestProfileResourcesConversionPreservesDeclarationState(t *testing.T) {
	unset := profileResourcesToEngine(profile.Resources{})
	if unset.MCP != nil || unset.Agents != nil || unset.Hooks != nil || unset.Config != nil || unset.Instructions != nil {
		t.Fatalf("unset resources became declared: %+v", unset)
	}

	declaredEmpty := profileResourcesToEngine(profile.Resources{
		MCP:    []mcp.Server{},
		Agents: []profile.SubAgent{},
		Hooks:  []profile.Hook{},
		Config: []profile.ConfigPatch{},
	})
	if declaredEmpty.MCP == nil || declaredEmpty.Agents == nil || declaredEmpty.Hooks == nil || declaredEmpty.Config == nil {
		t.Fatalf("non-nil empty declaration became unset: %+v", declaredEmpty)
	}
	if len(declaredEmpty.MCP.Servers) != 0 || len(declaredEmpty.Agents) != 0 || len(declaredEmpty.Hooks) != 0 || len(declaredEmpty.Config) != 0 {
		t.Fatalf("empty declaration gained entries: %+v", declaredEmpty)
	}
}

func TestProfileResourcesConversionOwnsInputs(t *testing.T) {
	selectedSkill := skill.Inline("triage", "# triage")
	selectedSkill.Metadata = map[string]string{"owner": "platform"}
	resources := profile.Resources{
		Skills: []skill.Ref{selectedSkill},
		MCP: []mcp.Server{{
			Key:       "docs",
			Transport: mcp.TransportStdio,
			Command:   "docs-server",
			Args:      []string{"--stdio"},
			Env:       map[string]string{"MODE": "strict"},
		}},
		Agents: []profile.SubAgent{{
			Key:        "reviewer",
			ToolPolicy: &profile.ToolPolicy{Allow: []string{"read"}},
			Hooks: []profile.Hook{{
				Key: "nested",
				Handler: profile.HookHandler{
					Type:  profile.HookHandlerCommand,
					Input: map[string]any{"nested": map[string]any{"enabled": true}},
				},
			}},
			Native: map[string]any{"nested": map[string]any{"mode": "safe"}},
		}},
		Hooks: []profile.Hook{{
			Key: "audit",
			Handler: profile.HookHandler{
				Type: profile.HookHandlerCommand,
				Env:  map[string]string{"AUDIT": "1"},
			},
		}},
		Instructions: &profile.Instructions{
			Content: "follow policy",
			Scope:   profile.InstructionScopeProject,
			Native:  map[string]any{"nested": []any{"a", "b"}},
		},
		Config: []profile.ConfigPatch{{
			Key:    "telemetry",
			Values: map[string]any{"nested": map[string]any{"enabled": false}},
			Native: &profile.NativeConfigPatch{
				Provider: "codex",
				FileKind: profile.ConfigFileTOML,
				Values:   map[string]any{"tools": []string{"shell"}},
			},
		}},
	}

	got := profileResourcesToEngine(resources)
	resources.MCP[0].Args[0] = "--mutated"
	resources.MCP[0].Env["MODE"] = "mutated"
	resources.Agents[0].ToolPolicy.Allow[0] = "write"
	resources.Agents[0].Native["nested"].(map[string]any)["mode"] = "mutated"
	resources.Agents[0].Hooks[0].Handler.Input["nested"].(map[string]any)["enabled"] = false
	resources.Hooks[0].Handler.Env["AUDIT"] = "0"
	resources.Instructions.Native["nested"].([]any)[0] = "mutated"
	resources.Config[0].Values["nested"].(map[string]any)["enabled"] = true
	resources.Config[0].Native.Values["tools"].([]string)[0] = "mutated"
	selectedSkill.Metadata["owner"] = "mutated"

	if got.MCP.Servers[0].Args[0] != "--stdio" || got.MCP.Servers[0].Env["MODE"] != "strict" {
		t.Fatal("MCP conversion retained caller-owned collections")
	}
	if got.Agents[0].ToolPolicy.Allow[0] != "read" || got.Agents[0].Native["nested"].(map[string]any)["mode"] != "safe" {
		t.Fatal("sub-agent conversion retained caller-owned collections")
	}
	if got.Agents[0].Hooks[0].Handler.Input["nested"].(map[string]any)["enabled"] != true || got.Hooks[0].Handler.Env["AUDIT"] != "1" {
		t.Fatal("hook conversion retained caller-owned collections")
	}
	if got.Instructions.Native["nested"].([]any)[0] != "a" {
		t.Fatal("instructions conversion retained caller-owned collections")
	}
	if got.Config[0].Values["nested"].(map[string]any)["enabled"] != false || got.Config[0].Native.Values["tools"].([]string)[0] != "shell" {
		t.Fatal("config conversion retained caller-owned collections")
	}
	if got.Skills[0].(skill.Skill).Metadata["owner"] != "platform" {
		t.Fatal("skill conversion retained caller-owned metadata")
	}
}
