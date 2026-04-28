package main

import (
	"context"
	"flag"
	"os"
	"strings"
	"time"

	agentadaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/examples/internal/exampleutil"
)

func main() {
	agent := flag.String("agent", "", "Local CLI agent to use: "+exampleutil.SupportedAgents()+" (default codex, or AGENT_ADAPTOR_EXAMPLE_AGENT)")
	model := flag.String("model", "", "Model to use. Defaults by agent or CODEX_MODEL/CLAUDE_MODEL/CURSOR_MODEL.")
	command := flag.String("command", "", "Optional explicit local CLI command. Defaults by agent or CODEX_COMMAND/CLAUDE_COMMAND/CURSOR_COMMAND/PATH.")
	prompt := flag.String("prompt", "Reply with a short acknowledgement for the basic example.", "Prompt to send to the selected agent")
	timeout := flag.Duration("timeout", 3*time.Minute, "Maximum time to wait for the run")
	flag.Parse()

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	cwd, err := os.Getwd()
	exampleutil.Must(err, "resolve current working directory")
	agentCfg := exampleutil.ResolveLiveAgentConfig(*agent, *model, *command, cwd)

	sdk := agentadaptor.New(
		agentadaptor.WithDefaultAgent(exampleutil.NewLiveAgentBinding(agentCfg)),
	)

	result, err := sdk.Run(ctx, *prompt)
	exampleutil.Must(err, "run basic example")
	exampleutil.Check(result.DriverType == agentCfg.DriverType, "expected driver type %q, got %q", agentCfg.DriverType, result.DriverType)
	exampleutil.Check(result.ExitCode == 0, "expected exit code 0, got %d", result.ExitCode)
	exampleutil.Check(strings.TrimSpace(result.Output) != "", "expected non-empty output from %s", agentCfg.Agent)

	exampleutil.PrintJSON(map[string]any{
		"example":      "basic",
		"agent":        exampleutil.LiveAgentSummary(agentCfg),
		"driver_type":  result.DriverType,
		"exit_code":    result.ExitCode,
		"output_lines": countNonEmptyLines(result.Output),
	})
}

func countNonEmptyLines(value string) int {
	count := 0
	for _, line := range strings.Split(value, "\n") {
		if strings.TrimSpace(line) != "" {
			count++
		}
	}
	return count
}
