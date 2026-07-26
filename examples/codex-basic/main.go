// codex-basic is the v1 quickstart (design doc §3, scenario S1): construct an
// Agent from a driver, ask a question, read one Result.
//
// The whole SDK surface used here is three names — adaptor.New, agent.Run,
// res.Text — plus the single err verdict: a business failure arrives as a
// typed *adaptor.RunError that still carries the full Result.
//
// -structured switches the same single call to its typed twin (scenario S5):
// adaptor.RunAs[T] derives the schema from the Go struct, negotiates the
// strongest structured-output mode the driver supports, validates and decodes.
// The legacy two-step WithJSONSchemaOutputFor[T] + DecodeStructuredOutput[T]
// becomes one call, and the verdict path is unchanged.
//
// Usage:
//
//	go run ./examples/codex-basic -agent=codex -prompt="say hi"
//	go run ./examples/codex-basic -agent=codex -structured
package main

import (
	"context"
	"errors"
	"flag"
	"os"
	"strings"
	"time"

	"github.com/agent-dance/agent-adaptor/examples/internal/exampleutil"
	adaptor "github.com/agent-dance/agent-adaptor/next"
)

// Ack is the business struct -structured asks for. RunAs[Ack] is the only
// place it has to be mentioned: no schema literal, no decode step.
type Ack struct {
	Greeting string `json:"greeting"`
	Mood     string `json:"mood"`
}

func main() {
	agent := flag.String("agent", "", "Local CLI agent to use: "+exampleutil.SupportedAgents()+" (default codex, or AGENT_ADAPTOR_EXAMPLE_AGENT)")
	model := flag.String("model", "", "Model to use. Defaults by agent or CODEX_MODEL/CLAUDE_MODEL/CURSOR_MODEL.")
	command := flag.String("command", "", "Optional explicit local CLI command. Defaults by agent or CODEX_COMMAND/CLAUDE_COMMAND/CURSOR_COMMAND/PATH.")
	prompt := flag.String("prompt", "Reply with a short acknowledgement for the basic example.", "Prompt to send to the selected agent")
	structured := flag.Bool("structured", false, "Decode the answer into a Go struct with adaptor.RunAs[Ack] instead of reading free text")
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

	if *structured {
		runStructured(ctx, ai, agentCfg, *prompt)
		return
	}
	runText(ctx, ai, agentCfg, *prompt)
}

func runText(ctx context.Context, ai *adaptor.Agent, agentCfg exampleutil.LiveAgentConfig, prompt string) {
	res, err := ai.Run(ctx, prompt)
	checkRun(err, "run basic example")
	exampleutil.Check(strings.TrimSpace(res.Text) != "", "expected non-empty text from %s", agentCfg.Agent)

	exampleutil.PrintJSON(map[string]any{
		"example":      "basic",
		"mode":         "text",
		"agent":        exampleutil.LiveAgentSummary(agentCfg),
		"run_id":       res.RunID,
		"model":        res.Model,
		"summary":      res.Summary,
		"output_lines": countNonEmptyLines(res.Text),
	})
}

func runStructured(ctx context.Context, ai *adaptor.Agent, agentCfg exampleutil.LiveAgentConfig, prompt string) {
	// RunAs takes any Runner, so the identical call works on a Thread.
	ack, res, err := adaptor.RunAs[Ack](ctx, ai,
		prompt+" Report the acknowledgement text and your mood.")
	checkRun(err, "run basic example in structured mode")
	exampleutil.Check(strings.TrimSpace(ack.Greeting) != "", "expected a greeting field from %s", agentCfg.Agent)

	exampleutil.PrintJSON(map[string]any{
		"example": "basic",
		"mode":    "structured",
		"agent":   exampleutil.LiveAgentSummary(agentCfg),
		"run_id":  res.RunID,
		"model":   res.Model,
		"ack":     ack,
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
