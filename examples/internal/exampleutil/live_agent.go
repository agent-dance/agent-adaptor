package exampleutil

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/agent-dance/agent-adaptor/claude"
	"github.com/agent-dance/agent-adaptor/codebuddy"
	"github.com/agent-dance/agent-adaptor/codex"
	"github.com/agent-dance/agent-adaptor/cursor"
	"github.com/agent-dance/agent-adaptor/driver"
	adaptor "github.com/agent-dance/agent-adaptor/next"
)

const (
	AgentCodex     = "codex"
	AgentClaude    = "claude"
	AgentCursor    = "cursor"
	AgentCodebuddy = "codebuddy"
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
	Env         []driver.EnvBinding
	ExtraArgs   []string
}

func SupportedAgents() string {
	return "codex, claude, cursor, codebuddy"
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
	case AgentCodebuddy:
		return codebuddy.DriverType
	default:
		return codex.DriverType
	}
}

// NewLiveDriver builds the v1 driver value for the resolved local CLI. The
// four built-in packages all expose the same one-line shape —
// codex.Driver(codex.Config{...}) — so the example helper is just a switch
// over which Config type to fill.
//
// v1 note: a driver is now a plain value handed to adaptor.New; there is no
// binding wrapper and no named registry. Agent-level defaults (skills,
// profile, policy, ...) are adaptor.Option values passed to adaptor.New
// alongside this driver.
func NewLiveDriver(cfg LiveAgentConfig) driver.Driver {
	agent := normalizeAgent(cfg.Agent, "agent")
	model := ResolveAgentModel(agent, cfg.Model)

	switch agent {
	case AgentClaude:
		c := claude.Config{Model: model}
		c.Command, c.CWD, c.Env, c.ExtraArgs = cfg.Command, cfg.CWD, cfg.Env, cfg.ExtraArgs
		// PersistentProcess: true — the claude driver's resident-process reuse
		// lives on the cl/opt_examples branch and is out of v1.0.0 scope
		// (implementation plan R9). It becomes an additive Config field once
		// that branch merges; the v1 API shape does not change.
		return claude.Driver(c)
	case AgentCursor:
		c := cursor.Config{Model: model}
		c.Command, c.CWD, c.Env, c.ExtraArgs = cfg.Command, cfg.CWD, cfg.Env, cfg.ExtraArgs
		return cursor.Driver(c)
	case AgentCodebuddy:
		c := codebuddy.Config{Model: model}
		c.Command, c.CWD, c.Env, c.ExtraArgs = cfg.Command, cfg.CWD, cfg.Env, cfg.ExtraArgs
		return codebuddy.Driver(c)
	default:
		c := codex.Config{Model: model}
		c.Command, c.CWD, c.Env, c.ExtraArgs = cfg.Command, cfg.CWD, cfg.Env, cfg.ExtraArgs
		return codex.Driver(c)
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
	case AgentCodebuddy:
		return ""
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
	case AgentCodebuddy:
		return "CODEBUDDY_MODEL"
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
	case AgentCodebuddy:
		return "CODEBUDDY_COMMAND"
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
	case AgentCodebuddy:
		return "codebuddy"
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

// NonInteractivePolicy is the v1 spelling of the legacy
// NonInteractivePolicy(RunPolicy): one adaptor.Policy value carrying the
// sandbox strength plus the approval routing. Permission / PlanReview are
// auto-approved and Questions auto-denied so the examples never block on a
// human.
func NonInteractivePolicy(sandbox adaptor.SandboxLevel) adaptor.Policy {
	return adaptor.Policy{
		Sandbox: sandbox,
		Approvals: adaptor.ApprovalPolicy{
			Permission: adaptor.ApprovalAutoApprove,
			PlanReview: adaptor.ApprovalAutoApprove,
			Question:   adaptor.QuestionAutoDeny,
		},
	}
}

// NonInteractive returns the policy as a SharedOption, so the same value works
// as an adaptor.New default and as a per-call Run/Stream override.
func NonInteractive(sandbox adaptor.SandboxLevel) adaptor.SharedOption {
	return adaptor.WithPolicy(NonInteractivePolicy(sandbox))
}

func normalizeAgent(raw, field string) string {
	value := strings.ToLower(strings.TrimSpace(raw))
	if value == "" {
		return AgentCodex
	}
	switch value {
	case AgentCodex, AgentClaude, AgentCursor, AgentCodebuddy:
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
	case AgentCodebuddy:
		names = []string{"codebuddy.ps1", "codebuddy.cmd", "codebuddy"}
	}

	candidates := make([]string, 0, len(names))
	for _, name := range names {
		if path, err := exec.LookPath(name); err == nil {
			candidates = append(candidates, path)
		}
	}
	return dedupePaths(candidates)
}
