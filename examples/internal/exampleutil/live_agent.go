package exampleutil

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	agentadaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/claude"
	"github.com/agent-dance/agent-adaptor/codex"
	"github.com/agent-dance/agent-adaptor/cursor"
)

const (
	AgentCodex  = "codex"
	AgentClaude = "claude"
	AgentCursor = "cursor"
)

// LiveAgentConfig is the resolved local-CLI agent configuration shared by the
// examples. Command is always a runnable binary discovered from PATH, a
// provider-specific env var, or an explicit -command flag.
type LiveAgentConfig struct {
	Agent       string
	DriverType  string
	Model       string
	Command     string
	CommandNote string
	CWD         string
	Env         []agentadaptor.EnvBinding
	ExtraArgs   []string
	CursorMode  agentadaptor.CursorMode
}

func SupportedAgents() string {
	return "codex, claude, cursor"
}

func ResolveLiveAgent(raw string) string {
	if strings.TrimSpace(raw) == "" {
		raw = os.Getenv("AGENT_ADAPTOR_EXAMPLE_AGENT")
	}
	return normalizeAgent(raw, "agent")
}

func ResolveLiveAgentConfig(rawAgent, rawModel, rawCommand, cwd string) LiveAgentConfig {
	agent := ResolveLiveAgent(rawAgent)
	command, note := RequireHealthyAgentCommand(agent, rawCommand)
	return LiveAgentConfig{
		Agent:       agent,
		DriverType:  DriverTypeForAgent(agent),
		Model:       ResolveAgentModel(agent, rawModel),
		Command:     command,
		CommandNote: note,
		CWD:         cwd,
	}
}

func DriverTypeForAgent(agent string) string {
	switch normalizeAgent(agent, "agent") {
	case AgentClaude:
		return claude.DriverType
	case AgentCursor:
		return cursor.DriverType
	default:
		return codex.DriverType
	}
}

func NewLiveAgentBinding(cfg LiveAgentConfig, opts ...agentadaptor.AgentOption) agentadaptor.AgentBinding {
	agent := normalizeAgent(cfg.Agent, "agent")
	common := agentadaptor.CommonConfig{
		Command:   cfg.Command,
		CWD:       cfg.CWD,
		Env:       cfg.Env,
		ExtraArgs: cfg.ExtraArgs,
	}
	model := ResolveAgentModel(agent, cfg.Model)

	switch agent {
	case AgentClaude:
		return claude.New(agentadaptor.ClaudeConfig{
			CommonConfig: common,
			Model:        model,
		}, opts...)
	case AgentCursor:
		return cursor.New(agentadaptor.CursorConfig{
			CommonConfig: common,
			Model:        model,
			Mode:         cfg.CursorMode,
		}, opts...)
	default:
		return codex.New(agentadaptor.CodexConfig{
			CommonConfig: common,
			Model:        model,
		}, opts...)
	}
}

func ResolveAgentModel(agent, override string) string {
	if strings.TrimSpace(override) != "" {
		return strings.TrimSpace(override)
	}
	if env := strings.TrimSpace(os.Getenv(ModelEnvForAgent(agent))); env != "" {
		return env
	}
	return DefaultModelForAgent(agent)
}

func DefaultModelForAgent(agent string) string {
	switch normalizeAgent(agent, "agent") {
	case AgentClaude:
		return "claude-sonnet-4"
	case AgentCursor:
		return "gpt-5"
	default:
		return "gpt-5.4"
	}
}

func ModelEnvForAgent(agent string) string {
	switch normalizeAgent(agent, "agent") {
	case AgentClaude:
		return "CLAUDE_MODEL"
	case AgentCursor:
		return "CURSOR_MODEL"
	default:
		return "CODEX_MODEL"
	}
}

func CommandEnvForAgent(agent string) string {
	switch normalizeAgent(agent, "agent") {
	case AgentClaude:
		return "CLAUDE_COMMAND"
	case AgentCursor:
		return "CURSOR_COMMAND"
	default:
		return "CODEX_COMMAND"
	}
}

func DefaultCommandForAgent(agent string) string {
	switch normalizeAgent(agent, "agent") {
	case AgentClaude:
		return "claude"
	case AgentCursor:
		return "agent"
	default:
		return "codex"
	}
}

func DiscoverHealthyAgentCommand(agent, override string) (string, string, bool) {
	agent = normalizeAgent(agent, "agent")
	if strings.TrimSpace(override) != "" {
		if ProbeAgentCommand(override) {
			return override, fmt.Sprintf("Using the explicitly requested %s command.", agent), true
		}
		return "", "", false
	}

	if envName := CommandEnvForAgent(agent); envName != "" {
		if envCommand := strings.TrimSpace(os.Getenv(envName)); envCommand != "" {
			if ProbeAgentCommand(envCommand) {
				return envCommand, fmt.Sprintf("Using %s from %s.", agent, envName), true
			}
			return "", "", false
		}
	}

	for _, candidate := range commandCandidatesForAgent(agent) {
		if ProbeAgentCommand(candidate) {
			return candidate, fmt.Sprintf("Using healthy external %s command %q.", agent, candidate), true
		}
	}
	return "", "", false
}

func RequireHealthyAgentCommand(agent, override string) (string, string) {
	agent = normalizeAgent(agent, "agent")
	command, note, ok := DiscoverHealthyAgentCommand(agent, override)
	if ok {
		return command, note
	}
	Fatalf("no healthy local %s CLI command found; install/login the CLI, set %s, or pass -command=/path/to/%s",
		agent, CommandEnvForAgent(agent), DefaultCommandForAgent(agent))
	return "", ""
}

func ProbeAgentCommand(command string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	execPath, args := WrapCommandForPlatform(command, []string{"--help"})
	cmd := exec.CommandContext(ctx, execPath, args...)
	cmd.Env = EnsureWindowsProcessEnv(os.Environ())
	if output, err := cmd.CombinedOutput(); err != nil {
		_ = output
		return false
	}
	return cmd.ProcessState != nil && cmd.ProcessState.ExitCode() == 0
}

func CommandSummary(cfg LiveAgentConfig) map[string]any {
	return map[string]any{
		"agent": cfg.Agent,
		"path":  cfg.Command,
		"note":  cfg.CommandNote,
	}
}

func LiveAgentSummary(cfg LiveAgentConfig) map[string]any {
	return map[string]any{
		"agent":       cfg.Agent,
		"driver_type": cfg.DriverType,
		"model":       cfg.Model,
		"command":     CommandSummary(cfg),
	}
}

func NonInteractivePolicy(isolation agentadaptor.IsolationLevel) agentadaptor.RunPolicy {
	return agentadaptor.RunPolicy{
		Isolation: isolation,
		HumanDecision: agentadaptor.HumanDecisionPolicy{
			Permission: agentadaptor.HumanDecisionAutoApprove,
			PlanReview: agentadaptor.HumanDecisionAutoApprove,
			Question:   agentadaptor.QuestionAutoReject,
		},
	}
}

func NonInteractiveRunOption(isolation agentadaptor.IsolationLevel) agentadaptor.RunOption {
	return agentadaptor.WithRunPolicy(NonInteractivePolicy(isolation))
}

func normalizeAgent(raw, field string) string {
	value := strings.ToLower(strings.TrimSpace(raw))
	if value == "" {
		return AgentCodex
	}
	switch value {
	case AgentCodex, AgentClaude, AgentCursor:
		return value
	default:
		Fatalf("%s must be one of %s, got %q", field, SupportedAgents(), raw)
		panic("unreachable")
	}
}

func commandCandidatesForAgent(agent string) []string {
	names := []string{DefaultCommandForAgent(agent)}
	switch normalizeAgent(agent, "agent") {
	case AgentCodex:
		names = []string{"codex.ps1", "codex.cmd", "codex"}
	case AgentClaude:
		names = []string{"claude.ps1", "claude.cmd", "claude", "trpc-claudecode.ps1", "trpc-claudecode.cmd", "trpc-claudecode"}
	case AgentCursor:
		names = []string{"agent.ps1", "agent.cmd", "agent", "cursor-agent.ps1", "cursor-agent.cmd", "cursor-agent"}
	}

	candidates := make([]string, 0, len(names))
	for _, name := range names {
		if path, err := exec.LookPath(name); err == nil {
			candidates = append(candidates, path)
		}
	}
	return dedupePaths(candidates)
}
