package main

import (
	"context"
	"flag"
	"strings"
	"time"

	agentadaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/codex"
	"github.com/agent-dance/agent-adaptor/examples/internal/exampleutil"
)

func main() {
	model := flag.String("model", "gpt-5.4", "Codex model to use")
	command := flag.String("command", "", "Optional explicit Codex-compatible command. Defaults to the healthy external Codex command discovered from PATH.")
	prompt := flag.String("prompt", "Reply with a short acknowledgement for the codex-basic example.", "Prompt to send to Codex")
	timeout := flag.Duration("timeout", 3*time.Minute, "Maximum time to wait for the run")
	flag.Parse()

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	commandPath, commandNote := exampleutil.RequireHealthyCodexCommand(*command)

	sdk := agentadaptor.New(
		agentadaptor.WithDefaultAgent(codex.New(agentadaptor.CodexConfig{
			CommonConfig: agentadaptor.CommonConfig{
				Command: commandPath,
			},
			Model: *model,
		})),
	)

	result, err := sdk.Run(ctx, *prompt)
	exampleutil.Must(err, "run codex-basic example")
	exampleutil.Check(result.DriverType == codex.DriverType, "expected driver type %q, got %q", codex.DriverType, result.DriverType)
	exampleutil.Check(result.ExitCode == 0, "expected exit code 0, got %d", result.ExitCode)
	exampleutil.Check(strings.TrimSpace(result.Output) != "", "expected non-empty output from Codex")

	exampleutil.PrintJSON(map[string]any{
		"example":      "codex-basic",
		"driver_type":  result.DriverType,
		"exit_code":    result.ExitCode,
		"output_lines": countNonEmptyLines(result.Output),
		"command": map[string]any{
			"path": commandPath,
			"note": commandNote,
		},
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
