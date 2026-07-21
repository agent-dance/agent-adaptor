package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	agentadaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/claude"
	"github.com/agent-dance/agent-adaptor/codex"
	"github.com/agent-dance/agent-adaptor/cursor"
	"github.com/agent-dance/agent-adaptor/examples/internal/exampleutil"
)

const sentinel = "AGENT_ADAPTOR_SMOKE_OK"

type smokeStatus string

const (
	statusPassed            smokeStatus = "passed"
	statusSkipped           smokeStatus = "skipped"
	statusEnvironmentFailed smokeStatus = "environment_failed"
	statusRunFailed         smokeStatus = "run_failed"
)

type report struct {
	Status        smokeStatus `json:"status"`
	Agent         string      `json:"agent"`
	Command       string      `json:"command,omitempty"`
	ModelOverride string      `json:"model_override,omitempty"`
	Reason        string      `json:"reason,omitempty"`
	Environment   any         `json:"environment,omitempty"`
	Run           any         `json:"run,omitempty"`
}

func main() {
	os.Exit(run())
}

func run() int {
	agent := flag.String("agent", "codex", "Local CLI agent: codex, claude, or cursor")
	command := flag.String("command", "", "Optional CLI command override")
	model := flag.String("model", "", "Optional model override; empty uses provider configuration")
	timeout := flag.Duration("timeout", 90*time.Second, "Hard deadline for preflight and one live run")
	skip := flag.Bool("skip", false, "Report skipped without probing or running a provider")
	keep := flag.Bool("keep-workspace", false, "Keep the isolated workspace/profile for debugging")
	flag.Parse()

	selected := strings.ToLower(strings.TrimSpace(*agent))
	if *skip {
		return finish(report{Status: statusSkipped, Agent: selected, Reason: "live smoke disabled explicitly"})
	}
	if !validAgent(selected) {
		return finish(report{Status: statusEnvironmentFailed, Agent: selected, Reason: "agent must be codex, claude, or cursor"})
	}
	if *timeout <= 0 {
		return finish(report{Status: statusEnvironmentFailed, Agent: selected, Reason: "timeout must be greater than zero"})
	}

	resolvedCommand, note, ok := exampleutil.DiscoverHealthyAgentCommand(selected, *command)
	if !ok {
		return finish(report{
			Status: statusEnvironmentFailed,
			Agent:  selected,
			Reason: fmt.Sprintf("no healthy CLI command; install/login the provider, set %s, or pass -command", exampleutil.CommandEnvForAgent(selected)),
		})
	}

	root, err := os.MkdirTemp("", "agent-adaptor-smoke-*")
	if err != nil {
		return finish(report{Status: statusEnvironmentFailed, Agent: selected, Command: resolvedCommand, Reason: "create isolated workspace: " + err.Error()})
	}
	if !*keep {
		defer os.RemoveAll(root)
	}
	workspace := filepath.Join(root, "workspace")
	profile := filepath.Join(root, selected+"-profile")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		return finish(report{Status: statusEnvironmentFailed, Agent: selected, Command: resolvedCommand, Reason: "create workspace: " + err.Error()})
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	base := report{
		Agent:         selected,
		Command:       resolvedCommand,
		ModelOverride: strings.TrimSpace(*model),
		Reason:        note,
	}
	preflightSDK, err := agentadaptor.Build(agentadaptor.WithDefaultAgent(
		binding(selected, resolvedCommand, *model, workspace, agentadaptor.WithNativeProfile()),
	))
	if err != nil {
		base.Status = statusEnvironmentFailed
		base.Reason = "build preflight binding: " + err.Error()
		return finish(base)
	}
	environment, err := preflightSDK.Admin().Default().CheckEnvironment(ctx)
	base.Environment = environmentSummary(environment)
	if err != nil {
		base.Status = statusEnvironmentFailed
		base.Reason = "environment preflight: " + err.Error()
		return finish(base)
	}
	if !environment.Healthy {
		base.Status = statusEnvironmentFailed
		base.Reason = environment.Summary
		return finish(base)
	}

	sdk, err := agentadaptor.Build(agentadaptor.WithDefaultAgent(binding(
		selected,
		resolvedCommand,
		*model,
		workspace,
		agentadaptor.WithCloneProfile(profile, agentadaptor.CloneProfileOptions{
			IncludeSettings: true,
			AuthMode:        agentadaptor.CloneProfileAuthLink,
		}),
	)))
	if err != nil {
		base.Status = statusRunFailed
		base.Reason = "build isolated binding: " + err.Error()
		return finish(base)
	}

	result, runErr := sdk.Run(
		ctx,
		"Reply with exactly this token and nothing else: "+sentinel,
		exampleutil.NonInteractiveRunOption(agentadaptor.IsolationReadOnly),
	)
	base.Run = runSummary(result, workspace, profile, *keep)
	base.Status, base.Reason = classifyRun(result, runErr)
	if base.Status == statusRunFailed && hasCredentialWarning(environment) {
		base.Status = statusEnvironmentFailed
		base.Reason = environment.Summary + "; live run failed: " + base.Reason
	}
	return finish(base)
}

func validAgent(agent string) bool {
	switch agent {
	case exampleutil.AgentCodex, exampleutil.AgentClaude, exampleutil.AgentCursor:
		return true
	default:
		return false
	}
}

func binding(agent, command, model, cwd string, opts ...agentadaptor.AgentOption) agentadaptor.AgentBinding {
	common := agentadaptor.CommonConfig{Command: command, CWD: cwd}
	switch agent {
	case exampleutil.AgentClaude:
		return claude.New(agentadaptor.ClaudeConfig{CommonConfig: common, Model: strings.TrimSpace(model)}, opts...)
	case exampleutil.AgentCursor:
		return cursor.New(agentadaptor.CursorConfig{CommonConfig: common, Model: strings.TrimSpace(model)}, opts...)
	default:
		return codex.New(agentadaptor.CodexConfig{
			CommonConfig:     common,
			Model:            strings.TrimSpace(model),
			SkipGitRepoCheck: true,
		}, opts...)
	}
}

func classifyRun(result agentadaptor.RunResult, err error) (smokeStatus, string) {
	if err != nil {
		if looksLikeEnvironmentFailure(err.Error()) {
			return statusEnvironmentFailed, err.Error()
		}
		return statusRunFailed, err.Error()
	}
	if result.Failure != nil {
		message := strings.TrimSpace(result.Failure.Message)
		if looksLikeEnvironmentFailure(message + " " + rawText(result)) {
			return statusEnvironmentFailed, message
		}
		return statusRunFailed, message
	}
	if result.ExitCode != 0 {
		message := fmt.Sprintf("provider exited with code %d", result.ExitCode)
		if looksLikeEnvironmentFailure(rawText(result)) {
			return statusEnvironmentFailed, message
		}
		return statusRunFailed, message
	}
	if !strings.Contains(result.Output, sentinel) {
		return statusRunFailed, "completed output did not contain the sentinel"
	}
	return statusPassed, "authenticated isolated run returned the sentinel"
}

func looksLikeEnvironmentFailure(value string) bool {
	value = strings.ToLower(value)
	markers := []string{
		"not logged in", "login required", "authentication required", "unauthorized",
		"invalid api key", "missing api key", "credential", "insufficient quota",
		"rate limit", "command not found", "executable file not found",
	}
	for _, marker := range markers {
		if strings.Contains(value, marker) {
			return true
		}
	}
	return false
}

func rawText(result agentadaptor.RunResult) string {
	if result.RawStreams == nil {
		return ""
	}
	return result.RawStreams.Stdout + "\n" + result.RawStreams.Stderr
}

func environmentSummary(value agentadaptor.EnvironmentReport) map[string]any {
	checks := make([]map[string]string, 0, len(value.Checks))
	for _, check := range value.Checks {
		checks = append(checks, map[string]string{
			"code": check.Code, "level": check.Level, "message": check.Message,
		})
	}
	return map[string]any{
		"status": value.Status, "healthy": value.Healthy, "summary": value.Summary,
		"checks": checks,
	}
}

func hasCredentialWarning(value agentadaptor.EnvironmentReport) bool {
	for _, check := range value.Checks {
		code := strings.ToLower(check.Code)
		if strings.Contains(code, "auth_missing") || strings.Contains(code, "credentials_missing") || strings.Contains(code, "api_key_missing") {
			return true
		}
	}
	return false
}

func runSummary(result agentadaptor.RunResult, workspace, profile string, keep bool) map[string]any {
	value := map[string]any{
		"run_id": result.RunID, "driver_type": result.DriverType,
		"exit_code": result.ExitCode, "sentinel_seen": strings.Contains(result.Output, sentinel),
		"workspace_retained": keep,
	}
	if keep {
		value["workspace"] = workspace
		value["profile"] = profile
	}
	if result.Failure != nil {
		value["failure_code"] = result.Failure.Code
	}
	return value
}

func finish(value report) int {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil && !errors.Is(err, os.ErrClosed) {
		fmt.Fprintln(os.Stderr, "encode smoke report:", err)
		return 3
	}
	switch value.Status {
	case statusPassed, statusSkipped:
		return 0
	case statusEnvironmentFailed:
		return 2
	default:
		return 3
	}
}
