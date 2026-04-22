// streaming-chat is a minimal example showing how to consume
// RunHandle.StreamEvents() in pure Go to build a character-level chat UI.
//
// Usage:
//
//	go run ./examples/streaming-chat "Write a haiku about streaming"
//
// The example requires a local `codex` CLI in PATH and existing
// authentication (run `codex login` once up front).
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	agentadaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/codex"
	"github.com/agent-dance/agent-adaptor/memory"
)

func main() {
	prompt := "Write a haiku about streaming text. Reply with only the haiku."
	if len(os.Args) > 1 {
		prompt = os.Args[1]
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	sdk := agentadaptor.New(
		agentadaptor.WithDefaultAgent(codex.New(agentadaptor.CodexConfig{
			CommonConfig: agentadaptor.CommonConfig{CWD: mustCwd()},
			Model:        "gpt-5.4",
		})),
		agentadaptor.WithSessionStore(memory.NewSessionStore()),
	)

	handle, err := sdk.Start(ctx, prompt,
		agentadaptor.WithStreaming(),
		agentadaptor.WithSessionKey("examples", "streaming-chat"),
		agentadaptor.WithRunPolicy(agentadaptor.RunPolicy{
			Isolation: agentadaptor.IsolationReadOnly,
			HumanDecision: agentadaptor.HumanDecisionPolicy{
				Permission: agentadaptor.HumanDecisionAutoApprove,
				PlanReview: agentadaptor.HumanDecisionAutoApprove,
				Question:   agentadaptor.QuestionAutoReject,
			},
		}),
	)
	if err != nil {
		fmt.Fprintln(os.Stderr, "start:", err)
		os.Exit(1)
	}
	defer handle.Cancel(ctx)

	fmt.Fprintf(os.Stderr, "[run %s]\n", handle.RunID())

	go drainRunEvents(handle.Events())

	for ev := range handle.StreamEvents() {
		switch ev.Kind {
		case agentadaptor.StreamTextContent:
			fmt.Print(ev.Delta)
		case agentadaptor.StreamReasoningContent:
			fmt.Fprint(os.Stderr, ev.Delta)
		case agentadaptor.StreamToolCallStart:
			fmt.Fprintf(os.Stderr, "\n[tool:%s]\n", ev.Name)
		case agentadaptor.StreamRunFinished:
			fmt.Println()
			if ev.Usage != nil {
				fmt.Fprintf(os.Stderr, "[usage input=%d output=%d cached=%d]\n",
					ev.Usage.InputTokens, ev.Usage.OutputTokens, ev.Usage.CachedInputTokens)
			}
		case agentadaptor.StreamRunError:
			msg := "unknown"
			if ev.Error != nil {
				msg = ev.Error.Message
			}
			fmt.Fprintln(os.Stderr, "[run error]:", msg)
		}
	}

	result, err := handle.Wait(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "wait:", err)
		os.Exit(1)
	}
	if result.Session != nil {
		fmt.Fprintf(os.Stderr, "[session %s]\n", result.Session.ID)
	}
}

// drainRunEvents silently drains the operational event channel. A real
// application would surface spawn / stderr / lifecycle entries to its logs.
func drainRunEvents(events <-chan agentadaptor.RunEvent) {
	for range events {
	}
}

func mustCwd() string {
	cwd, err := os.Getwd()
	if err != nil {
		panic(err)
	}
	return cwd
}
