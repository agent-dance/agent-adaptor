// tools gives a local coding agent one host-defined typed Go function. The
// application describes a Tool; agent-adaptor owns its authenticated runtime
// and delivers it to the provider internally.
//
// Usage:
//
//	go run ./examples/tools -agent=codex
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
	"github.com/agent-dance/agent-adaptor/tool"
)

type echoInput struct {
	Message string `json:"message" jsonschema:"required"`
}

type echoOutput struct {
	Message string `json:"message"`
}

func main() {
	agentName := flag.String("agent", "", "Local CLI agent to use: "+exampleutil.SupportedAgents()+" (default codex, or AGENT_ADAPTOR_EXAMPLE_AGENT)")
	model := flag.String("model", "", "Model to use. Defaults by agent or CODEX_MODEL/CLAUDE_MODEL/CURSOR_MODEL.")
	command := flag.String("command", "", "Optional explicit local CLI command. Defaults by agent or CODEX_COMMAND/CLAUDE_COMMAND/CURSOR_COMMAND/PATH.")
	timeout := flag.Duration("timeout", 3*time.Minute, "Maximum time to wait for the Tools example")
	flag.Parse()

	cwd, err := os.Getwd()
	exampleutil.Must(err, "resolve current working directory")
	agentCfg := exampleutil.ResolveLiveAgentConfig(*agentName, *model, *command, cwd)

	hostEcho := tool.Define(
		"host_echo",
		"Return the supplied message from the embedding host.",
		func(_ context.Context, in echoInput) (echoOutput, error) {
			return echoOutput{Message: "host observed: " + in.Message}, nil
		},
		tool.ReadOnly(),
		tool.Idempotent(),
		tool.Revision("host_echo/v1"),
	)

	ai := adaptor.New(
		exampleutil.NewLiveDriver(agentCfg),
		adaptor.WithTools(hostEcho),
	)
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		exampleutil.Must(ai.Close(closeCtx), "close Agent")
	}()

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	res, err := ai.Run(ctx, `Call the host_echo tool exactly once with {"message":"hello"}, then report its returned message.`)
	if err != nil {
		var runErr *adaptor.RunError
		if errors.As(err, &runErr) {
			exampleutil.Fatalf("Tools run failed (%s): %s", runErr.Reason, runErr.Message)
		}
		exampleutil.Fatalf("run Tools example: %v", err)
	}
	exampleutil.Check(strings.TrimSpace(res.Text) != "", "expected provider text after Tool execution")
	exampleutil.PrintJSON(map[string]any{
		"example": "tools",
		"agent":   exampleutil.LiveAgentSummary(agentCfg),
		"run_id":  res.RunID,
		"text":    res.Text,
	})
}
