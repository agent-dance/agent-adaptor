// quickstart constructs an Agent from a Driver, asks one question, and reads
// one Result.
//
// The whole SDK surface used here is three names — adaptor.New, agent.Run,
// res.Text — plus the single err verdict: a business failure arrives as a
// typed *adaptor.RunError that still carries the full Result.
//
// Usage:
//
//	go run ./examples/quickstart -agent=codex -prompt="say hi"
package main

import (
	"context"
	"errors"
	"flag"
	"os"
	"strings"
	"time"

	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/examples/internal/exampleutil"
)

func main() {
	agent := flag.String("agent", "", "Local CLI agent to use: "+exampleutil.SupportedAgents()+" (default codex, or AGENT_ADAPTOR_EXAMPLE_AGENT)")
	model := flag.String("model", "", "Model to use. Defaults by agent or CODEX_MODEL/CLAUDE_MODEL/CURSOR_MODEL.")
	command := flag.String("command", "", "Optional explicit local CLI command. Defaults by agent or CODEX_COMMAND/CLAUDE_COMMAND/CURSOR_COMMAND/PATH.")
	prompt := flag.String("prompt", "Reply with a short acknowledgement for the quickstart example.", "Prompt to send to the selected agent")
	timeout := flag.Duration("timeout", 3*time.Minute, "Maximum time to wait for the run")
	flag.Parse()

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	cwd, err := os.Getwd()
	exampleutil.Must(err, "resolve current working directory")
	agentCfg := exampleutil.ResolveLiveAgentConfig(*agent, *model, *command, cwd)

	// One driver value + agent-level defaults = one Agent. Multiple agents
	// are simply multiple Go variables; there is no SDK object to register
	// them in.
	ai := adaptor.New(exampleutil.NewLiveDriver(agentCfg))

	res, err := ai.Run(ctx, *prompt)
	checkRun(err, "run quickstart example")
	exampleutil.Check(strings.TrimSpace(res.Text) != "", "expected non-empty text from %s", agentCfg.Agent)

	exampleutil.PrintJSON(map[string]any{
		"example":      "quickstart",
		"agent":        exampleutil.LiveAgentSummary(agentCfg),
		"run_id":       res.RunID,
		"model":        res.Model,
		"summary":      res.Summary,
		"output_lines": countNonEmptyLines(res.Text),
	})
}

// checkRun is the D1 verdict in one place: business failures are typed and
// still carry the Result; everything else is a plain wrapped error.
func checkRun(err error, what string) {
	if err == nil {
		return
	}
	var runErr *adaptor.RunError
	if errors.As(err, &runErr) {
		exampleutil.Fatalf("run failed (%s): %s", runErr.Reason, runErr.Message)
	}
	exampleutil.Fatalf("%s: %v", what, err)
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
