package exampleutil

import (
	"os"
	"strings"
	"time"

	"github.com/agent-dance/agent-adaptor/memory"
	adaptor "github.com/agent-dance/agent-adaptor/next"
)

// ResolveAGUIAgent returns the driver name for AG-UI streaming examples.
// Env AGUI_AGENT (or first positional override via scripts): "codex" | "claude" | "cursor".
// Default is codex. Unknown values abort the example process.
func ResolveAGUIAgent() string {
	return normalizeAgent(os.Getenv("AGUI_AGENT"), "AGUI_AGENT")
}

// NewAGUIStreamingAgent builds the Agent used by streaming-chat-aguiclient and
// streaming-chat-copilotkit. The returned string is the resolved driver name.
//
// v1 shape: one adaptor.New call taking the driver value plus agent-level
// defaults. WithThreadStore is what makes agent.Thread(key) work — the AG-UI
// frontend always sends threadId, and the bridge turns it into a thread key.
func NewAGUIStreamingAgent(cwd string) (*adaptor.Agent, string) {
	agent := ResolveAGUIAgent()
	model := strings.TrimSpace(os.Getenv("AGUI_MODEL"))
	if model == "" && agent == AgentClaude {
		model = strings.TrimSpace(os.Getenv("CLAUDE_CODE_MODEL"))
	}
	cfg := ResolveLiveAgentConfig(agent, model, "", cwd)
	return adaptor.New(
		NewLiveDriver(cfg),
		adaptor.WithThreadStore(memory.NewStore()),
		AGUIExamplePolicy(),
	), agent
}

// AGUIExamplePolicy returns the policy option appropriate for the resolved
// AG-UI agent driver:
//
//   - claude: enable interactive PlanReview + Question approvals.
//     Permission stays auto-approve because the demo has no host-side
//     tool executor; the CLI would hang on Bash/Edit/Write waiting for a
//     tool_result otherwise.
//   - codex/cursor/codebuddy: skip approvals & questions by default so the
//     demo can run end-to-end on the local CLI.
//
// It is a SharedOption, so it can seed adaptor.New (as here) or override a
// single Run/Stream call.
func AGUIExamplePolicy() adaptor.SharedOption {
	switch ResolveAGUIAgent() {
	case AgentClaude:
		return adaptor.WithPolicy(adaptor.Policy{
			Sandbox: adaptor.WorkspaceWrite,
			Approvals: adaptor.ApprovalPolicy{
				Permission: adaptor.ApprovalAutoApprove,
				PlanReview: adaptor.ApprovalAsk,
				Question:   adaptor.QuestionAsk,
				Timeout:    10 * time.Minute,
			},
		})
	default:
		return NonInteractive(adaptor.WorkspaceWrite)
	}
}
