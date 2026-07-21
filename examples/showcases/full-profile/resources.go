package main

import (
	"path/filepath"
	"strings"
	"time"

	agentadaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/examples/internal/exampleutil"
)

const (
	instructionMarker = "AGENT_ADAPTOR_PROFILE_DEMO_INSTRUCTIONS"
	mcpOKMarker       = "MCP_PROFILE_DEMO_OK"
	hookOKMarker      = "PROFILE_HOOK_DEMO_OK"
)

func profileResources(agent, repoRoot, mcpServerPackage, hookPackage, hookLog, mcpLog string) agentadaptor.ProfileResources {
	hook := agentadaptor.HookSpec{
		Key:   "profile-session-start",
		Event: agentadaptor.HookEventSessionStart,
		Handler: agentadaptor.HookHandler{
			Type:    agentadaptor.HookHandlerCommand,
			Command: "go",
			Args:    []string{"-C", filepath.ToSlash(repoRoot), "run", hookPackage, "--log", filepath.ToSlash(hookLog), "--event", "SessionStart"},
		},
		Timeout: 10 * time.Second,
	}
	if agent == exampleutil.AgentCodex {
		hook.StatusMessage = "recording profile demo hook"
	}
	return agentadaptor.ProfileResources{
		Skills: []agentadaptor.SkillRef{
			agentadaptor.Key("profile-observer"),
		},
		MCP: &agentadaptor.MCPConfig{Servers: []agentadaptor.MCPServerSpec{{
			Key:       "profile-demo",
			Transport: agentadaptor.MCPTransportStdio,
			Command:   "go",
			Args:      []string{"-C", filepath.ToSlash(repoRoot), "run", mcpServerPackage},
			Env: map[string]string{
				"AGENT_ADAPTOR_PROFILE_DEMO":         "mcp",
				"AGENT_ADAPTOR_PROFILE_DEMO_MCP_LOG": filepath.ToSlash(mcpLog),
			},
			Required: true,
		}}},
		Agents: []agentadaptor.AgentSpec{{
			Key:          "profile-reviewer",
			RuntimeName:  "profile-reviewer",
			Description:  "Review profile resource materialization evidence.",
			Instructions: "When asked to review this demo, verify instructions, hooks, MCP, skills, and this subagent file in the effective provider profile.",
		}},
		Hooks: []agentadaptor.HookSpec{hook},
		Instructions: &agentadaptor.InstructionsBundleRef{
			ID:      "full-profile-demo",
			Content: "Profile instruction proof: mention " + instructionMarker + " if asked what profile instructions are active.",
			Scope:   agentadaptor.InstructionScopeProject,
			Mode:    agentadaptor.InstructionModeAdditive,
		},
	}
}

func defaultPrompt() string {
	return strings.Join([]string{
		"Confirm this selected-agent profile demo is active.",
		"Call the profile-demo MCP tool profile_effect_probe and include its exact result text.",
		"Mention " + instructionMarker + " if the profile instructions are visible.",
		"Keep the answer short.",
	}, " ")
}
