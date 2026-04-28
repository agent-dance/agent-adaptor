// streaming-chat is a minimal example showing how to consume
// RunHandle.StreamEvents() in pure Go to build a character-level chat UI.
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
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	agentadaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/examples/internal/exampleutil"
	"github.com/agent-dance/agent-adaptor/memory"
)

func main() {
	agent := flag.String("agent", "", "Local CLI agent to use: "+exampleutil.SupportedAgents()+" (default codex, or AGENT_ADAPTOR_EXAMPLE_AGENT)")
	model := flag.String("model", "", "Model to use. Defaults by agent or CODEX_MODEL/CLAUDE_MODEL/CURSOR_MODEL.")
	command := flag.String("command", "", "Optional explicit local CLI command. Defaults by agent or CODEX_COMMAND/CLAUDE_COMMAND/CURSOR_COMMAND/PATH.")
	prompt := flag.String("prompt", "Write a haiku about streaming text. Reply with only the haiku.", "Prompt to stream")
	timeout := flag.Duration("timeout", 5*time.Minute, "Maximum time to wait for the run")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()

	agentCfg := exampleutil.ResolveLiveAgentConfig(*agent, *model, *command, mustCwd())
	sdk := agentadaptor.New(
		agentadaptor.WithDefaultAgent(exampleutil.NewLiveAgentBinding(agentCfg)),
		agentadaptor.WithSessionStore(memory.NewSessionStore()),
	)

	handle, err := sdk.Start(ctx, *prompt,
		agentadaptor.WithStreaming(),
		agentadaptor.WithSessionKey("examples", "streaming-chat"),
		exampleutil.NonInteractiveRunOption(agentadaptor.IsolationReadOnly),
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
