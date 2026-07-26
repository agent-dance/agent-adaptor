// streaming-chat is a minimal character-level chat UI in pure Go.
//
// It is the clearest before/after in the v1 migration. The legacy version had
// to consume two channels — a mandatory operational RunEvent drain in a
// goroutine plus the StreamPayload channel — and then call Wait. v1 has one
// channel and one drain obligation: range over Events(), then read Result().
// Events you do not handle simply fall through the type switch.
//
// Usage:
//
//	go run ./examples/streaming-chat -agent=claude -prompt="Write a haiku about streaming"
//
// The example requires the selected local CLI in PATH and existing
// authentication.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/agent-dance/agent-adaptor/examples/internal/exampleutil"
	"github.com/agent-dance/agent-adaptor/memory"
	adaptor "github.com/agent-dance/agent-adaptor/next"
)

func main() {
	agentName := flag.String("agent", "", "Local CLI agent to use: "+exampleutil.SupportedAgents()+" (default codex, or AGENT_ADAPTOR_EXAMPLE_AGENT)")
	model := flag.String("model", "", "Model to use. Defaults by agent or CODEX_MODEL/CLAUDE_MODEL/CURSOR_MODEL.")
	command := flag.String("command", "", "Optional explicit local CLI command. Defaults by agent or CODEX_COMMAND/CLAUDE_COMMAND/CURSOR_COMMAND/PATH.")
	prompt := flag.String("prompt", "Write a haiku about streaming text. Reply with only the haiku.", "Prompt to stream")
	threadKey := flag.String("thread", "examples/streaming-chat", "Host-owned thread key")
	timeout := flag.Duration("timeout", 5*time.Minute, "Maximum time to wait for the run")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()

	agentCfg := exampleutil.ResolveLiveAgentConfig(*agentName, *model, *command, mustCwd())
	ai := adaptor.New(
		exampleutil.NewLiveDriver(agentCfg),
		adaptor.WithThreadStore(memory.NewStore()),
		exampleutil.NonInteractive(adaptor.ReadOnly),
	)

	// A Thread is a Runner too: Run/Stream on it behave identically to the
	// Agent's, only bound to the conversation under this key.
	stream := ai.Thread(*threadKey).Stream(ctx, *prompt)
	defer stream.Cancel()

	fmt.Fprintf(os.Stderr, "[run %s]\n", stream.RunID())

	for ev := range stream.Events() {
		switch e := ev.(type) {
		case adaptor.TextDelta:
			fmt.Print(e.Text)
		case adaptor.Thinking:
			fmt.Fprint(os.Stderr, e.Text)
		case adaptor.ToolCall:
			if e.Phase == adaptor.PhaseStart {
				fmt.Fprintf(os.Stderr, "\n[tool:%s]\n", e.Name)
			}
		case adaptor.Dropped:
			fmt.Fprintf(os.Stderr, "\n[dropped %d events]\n", e.Count)
		case adaptor.RunFinished:
			// Informational only: the authoritative outcome is Result().
			fmt.Println()
			if e.Usage != nil {
				fmt.Fprintf(os.Stderr, "[usage input=%d output=%d cached=%d]\n",
					e.Usage.InputTokens, e.Usage.OutputTokens, e.Usage.CachedInputTokens)
			}
		}
	}

	res, err := stream.Result()
	if err != nil {
		// One err, one verdict — a business failure is a typed *RunError that
		// still carries whatever Result was produced.
		var runErr *adaptor.RunError
		if errors.As(err, &runErr) {
			fmt.Fprintf(os.Stderr, "[run error %s]: %s\n", runErr.Reason, runErr.Message)
			os.Exit(1)
		}
		fmt.Fprintln(os.Stderr, "stream:", err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "[thread %s | model %s]\n", *threadKey, res.Model)
}

func mustCwd() string {
	cwd, err := os.Getwd()
	if err != nil {
		panic(err)
	}
	return cwd
}
