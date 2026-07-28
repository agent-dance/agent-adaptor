// structured-output derives a JSON schema from a Go type, asks the Driver for
// structured output, validates it, and returns the decoded value with Result.
//
// Usage:
//
//	go run ./examples/structured-output -agent=codex
package main

import (
	"context"
	"flag"
	"os"
	"strings"
	"time"

	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/examples/internal/exampleutil"
)

type acknowledgement struct {
	Greeting string `json:"greeting"`
	Mood     string `json:"mood"`
}

func main() {
	agent := flag.String("agent", "", "Local CLI agent to use: "+exampleutil.SupportedAgents()+" (default codex)")
	model := flag.String("model", "", "Model to use; defaults by agent")
	command := flag.String("command", "", "Optional explicit local CLI command")
	prompt := flag.String("prompt", "Acknowledge this request and describe your mood.", "Prompt to send")
	timeout := flag.Duration("timeout", 3*time.Minute, "Maximum run duration")
	flag.Parse()

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	cwd, err := os.Getwd()
	exampleutil.Must(err, "resolve current working directory")
	cfg := exampleutil.ResolveLiveAgentConfig(*agent, *model, *command, cwd)
	ai := adaptor.New(exampleutil.NewLiveDriver(cfg))

	value, result, err := adaptor.RunAs[acknowledgement](ctx, ai, *prompt)
	exampleutil.Must(err, "run structured-output example")
	exampleutil.Check(strings.TrimSpace(value.Greeting) != "", "expected a non-empty greeting")
	exampleutil.PrintJSON(map[string]any{
		"example": "structured-output",
		"agent":   exampleutil.LiveAgentSummary(cfg),
		"run_id":  result.RunID,
		"model":   result.Model,
		"value":   value,
	})
}
