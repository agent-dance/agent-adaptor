package exampleutil

import (
	"os"
	"strings"
	"time"

	agentadaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/claude"
	"github.com/agent-dance/agent-adaptor/codex"
	"github.com/agent-dance/agent-adaptor/examples/streaming-chat-copilotkit/hitlmock"
	"github.com/agent-dance/agent-adaptor/memory"
)

// ResolveAGUIAgent returns the driver name for AG-UI streaming examples.
// Env AGUI_AGENT (or first positional override via scripts): "codex" | "claude" | "mock".
// Default is codex. Unknown values abort the example process.
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
	case "mock", "hitlmock":
		return "mock"
	default:
		Fatalf("AGUI_AGENT must be codex, claude, or mock, got %q", raw)
		panic("unreachable")
	}
}

// NewAGUIStreamingSDK builds the SDK used by streaming-chat-aguiclient and
// streaming-chat-copilotkit: session store + default agent (codex / claude /
// mock). The returned string is the resolved driver name.
func NewAGUIStreamingSDK(cwd string) (agentadaptor.SDK, string) {
	switch a := ResolveAGUIAgent(); a {
	case "claude":
		return agentadaptor.New(
			agentadaptor.WithDefaultAgent(claude.New(
				agentadaptor.ClaudeConfig{
					CommonConfig: agentadaptor.CommonConfig{
						CWD: cwd, Command: "trpc-claudecode",
					},
					Model: envOrString("CLAUDE_CODE_MODEL", "claude-sonnet-4-6"),
				},
				agentadaptor.WithCloneProfile("~/.claudeme", agentadaptor.CloneProfileOptions{
					IncludeSettings: true,
					IncludeMCP:      true,
					IncludeSkills:   true,
					IncludeAuth:     true,
				}),
			)),
			agentadaptor.WithSessionStore(memory.NewSessionStore()),
		), a
	case "mock":
		return agentadaptor.New(
			agentadaptor.WithDefaultAgent(hitlmock.New(hitlmock.Config{
				StreamDelay: 30 * time.Millisecond,
			})),
			// The AG-UI frontend always sends threadId, which turns into a
			// WithSessionKey call; without a SessionStore the runner aborts
			// before the adapter can emit a single event. An in-memory store
			// is fine for the demo.
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

// AGUIExampleRunPolicy returns a RunOption appropriate for the resolved
// AG-UI agent driver:
//
//   - mock:   enable Ask for all three HITL kinds so the UI can demonstrate
//     the full request / pending / resolve loop.
//   - claude: enable Phase 3 interactive PlanReview + Question.
//     Permission stays AutoApprove because Phase 3 has no host-side
//     tool executor; the CLI would hang on Bash/Edit/Write waiting
//     for our tool_result otherwise. See
//     docs/workstream-hitl-claude-phase3.md §3.5.
//   - codex:  skip approvals & questions by default so the demo can run
//     end-to-end without vendor-side HITL (Phase 2 adds that).
func AGUIExampleRunPolicy() agentadaptor.RunOption {
	switch ResolveAGUIAgent() {
	case "mock":
		return agentadaptor.WithRunPolicy(agentadaptor.RunPolicy{
			Isolation: agentadaptor.IsolationWorkspaceWrite,
			HumanDecision: agentadaptor.HumanDecisionPolicy{
				Permission: agentadaptor.HumanDecisionAsk,
				PlanReview: agentadaptor.HumanDecisionAsk,
				Question:   agentadaptor.QuestionAsk,
				Timeout:    10 * time.Minute,
			},
		})
	case "claude":
		return agentadaptor.WithRunPolicy(agentadaptor.RunPolicy{
			Isolation: agentadaptor.IsolationWorkspaceWrite,
			HumanDecision: agentadaptor.HumanDecisionPolicy{
				Permission: agentadaptor.HumanDecisionAutoApprove,
				PlanReview: agentadaptor.HumanDecisionAsk,
				Question:   agentadaptor.QuestionAsk,
				Timeout:    10 * time.Minute,
			},
		})
	default:
		return agentadaptor.WithRunPolicy(agentadaptor.RunPolicy{
			Isolation: agentadaptor.IsolationWorkspaceWrite,
			HumanDecision: agentadaptor.HumanDecisionPolicy{
				Permission: agentadaptor.HumanDecisionAutoApprove,
				PlanReview: agentadaptor.HumanDecisionAutoApprove,
				Question:   agentadaptor.QuestionAutoReject,
			},
		})
	}
}
