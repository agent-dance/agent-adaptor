package exampleutil

import (
	"os"
	"strings"

	agentadaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/claude"
	"github.com/agent-dance/agent-adaptor/codex"
	"github.com/agent-dance/agent-adaptor/memory"
)

// ResolveAGUIAgent returns the driver name for AG-UI streaming examples.
// Env AGUI_AGENT (or first positional override via scripts): "codex" | "claude".
// Default is codex. Unknown values fall back to codex after printing a warning.
func ResolveAGUIAgent() string {
	raw := strings.TrimSpace(os.Getenv("AGUI_AGENT"))
	if raw == "" {
		return "codex"
	}
	switch strings.ToLower(raw) {
	case "codex":
		return "codex"
	case "claude":
		return "claude"
	default:
		Fatalf("AGUI_AGENT must be codex or claude, got %q", raw)
		panic("unreachable")
	}
}

// NewAGUIStreamingSDK builds the SDK used by streaming-chat-aguiclient and
// streaming-chat-copilotkit: session store + default agent (codex or claude).
// The returned string is the resolved driver name ("codex" or "claude").
func NewAGUIStreamingSDK(cwd string) (agentadaptor.SDK, string) {
	switch a := ResolveAGUIAgent(); a {
	case "claude":
		return agentadaptor.New(
			agentadaptor.WithDefaultAgent(claude.New(agentadaptor.ClaudeConfig{
				CommonConfig: agentadaptor.CommonConfig{CWD: cwd, Command: "trpc-claudecode"},
				Model:        envOrString("CLAUDE_CODE_MODEL", "claude-sonnet-4-6"),
			})),
			agentadaptor.WithSessionStore(memory.NewSessionStore()),
		), a
	default:
		return agentadaptor.New(
			agentadaptor.WithDefaultAgent(codex.New(agentadaptor.CodexConfig{
				CommonConfig: agentadaptor.CommonConfig{CWD: cwd},
				Model:        envOrString("CODEX_MODEL", "gpt-5.4"),
			})),
			agentadaptor.WithSessionStore(memory.NewSessionStore()),
		), "codex"
	}
}

func envOrString(name, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(name)); v != "" {
		return v
	}
	return fallback
}

// AGUIExampleRunPolicy matches the prior examples: no interactive approvals and
// read-only isolation (maps per adapter in claude/codex drivers).
func AGUIExampleRunPolicy() agentadaptor.RunOption {
	return agentadaptor.WithRunPolicy(agentadaptor.RunPolicy{
		Approvals: agentadaptor.ApprovalOff,
		Isolation: agentadaptor.IsolationWorkspaceWrite,
	})
}
