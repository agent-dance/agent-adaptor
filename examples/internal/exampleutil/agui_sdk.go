package exampleutil

import (
	"fmt"
	"os"
	"strings"
	"time"

	agentadaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/memory"
)

// ResolveAGUIAgent returns the driver name for AG-UI streaming examples.
// Env AGUI_AGENT (or first positional override via scripts): "codex" | "claude" | "cursor".
// Default is codex. Unknown values abort the example process.
func ResolveAGUIAgent() string {
	return normalizeAgent(os.Getenv("AGUI_AGENT"), "AGUI_AGENT")
}

// BuildAGUIStreamingSDK builds the SDK used by web-agui and
// web-copilotkit-hitl: session store + default local CLI agent. The
// returned string is the resolved driver name.
func BuildAGUIStreamingSDK(cwd string, opts ...agentadaptor.AgentOption) (agentadaptor.SDK, string, error) {
	agent := strings.TrimSpace(os.Getenv("AGUI_AGENT"))
	if agent == "" {
		agent = AgentCodex
	}
	model := strings.TrimSpace(os.Getenv("AGUI_MODEL"))
	if model == "" && agent == AgentClaude {
		model = strings.TrimSpace(os.Getenv("CLAUDE_CODE_MODEL"))
	}
	cfg, err := TryResolveLiveAgentConfig(agent, model, "", cwd)
	if err != nil {
		return nil, agent, fmt.Errorf("resolve AG-UI agent: %w", err)
	}
	if cfg.Agent == AgentCodex {
		cfg.SkipGitRepoCheck = true
	}
	return agentadaptor.New(
		agentadaptor.WithDefaultAgent(NewLiveAgentBinding(cfg, opts...)),
		// The AG-UI frontend always sends threadId, which turns into a
		// WithSessionKey call; without a SessionStore the runner aborts before
		// the adapter can emit a single event. An in-memory store is fine for
		// the demo.
		agentadaptor.WithSessionStore(memory.NewSessionStore()),
	), cfg.Agent, nil
}

// AGUIExampleRunPolicy returns a RunOption appropriate for the resolved
// AG-UI agent driver:
//
//   - claude: enable Phase 3 interactive PlanReview + Question.
//     Permission stays AutoApprove because Phase 3 has no host-side
//     tool executor; the CLI would hang on Bash/Edit/Write waiting
//     for our tool_result otherwise. See
//     docs/workstream-hitl-claude-phase3.md §3.5.
//   - codex/cursor: skip approvals & questions by default so the demo can run
//     end-to-end on the local CLI.
func AGUIExampleRunPolicy() agentadaptor.RunOption {
	switch ResolveAGUIAgent() {
	case AgentClaude:
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
		return NonInteractiveRunOption(agentadaptor.IsolationWorkspaceWrite)
	}
}
