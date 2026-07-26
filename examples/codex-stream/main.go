// codex-stream shows the v1 event model: ONE typed event channel per run,
// consumed with a single for-range + type switch, closed out by Result().
//
// The legacy split between an operational RunEvent channel and a StreamPayload
// channel is gone. Process chunks, transcript items, notices, tool calls, and
// text deltas are all adaptor.Event values on the same channel — a consumer
// simply omits the cases it does not care about (no drain obligation).
//
// Usage:
//
//	go run ./examples/codex-stream -agent=codex
//	go run ./examples/codex-stream -agent=codex -cancel-after=3s
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/agent-dance/agent-adaptor/examples/internal/exampleutil"
	adaptor "github.com/agent-dance/agent-adaptor/next"
)

func main() {
	agent := flag.String("agent", "", "Local CLI agent to use: "+exampleutil.SupportedAgents()+" (default codex, or AGENT_ADAPTOR_EXAMPLE_AGENT)")
	model := flag.String("model", "", "Model to use. Defaults by agent or CODEX_MODEL/CLAUDE_MODEL/CURSOR_MODEL.")
	command := flag.String("command", "", "Optional explicit local CLI command. Defaults by agent or CODEX_COMMAND/CLAUDE_COMMAND/CURSOR_COMMAND/PATH.")
	prompt := flag.String("prompt", "Reply in three short lines for the stream example.", "Prompt to send to the selected agent")
	timeout := flag.Duration("timeout", 3*time.Minute, "Maximum time to wait for the run")
	cancelAfter := flag.Duration("cancel-after", 0, "If greater than zero, cancel the run after this duration")
	flag.Parse()

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	cwd, err := os.Getwd()
	exampleutil.Must(err, "resolve current working directory")
	agentCfg := exampleutil.ResolveLiveAgentConfig(*agent, *model, *command, cwd)

	ai := adaptor.New(exampleutil.NewLiveDriver(agentCfg))

	// Stream never returns an error: startup failures close Events() and
	// surface through Result(). The verb is the switch — Run for batch,
	// Stream for live.
	stream := ai.Stream(ctx, *prompt)
	defer stream.Cancel()

	if *cancelAfter > 0 {
		time.AfterFunc(*cancelAfter, stream.Cancel)
	}

	counts := map[string]int{}
	total := 0
	for ev := range stream.Events() {
		total++
		switch e := ev.(type) {
		case adaptor.TextDelta:
			counts["text"]++
			fmt.Print(e.Text)
		case adaptor.Thinking:
			counts["thinking"]++
		case adaptor.ToolCall:
			counts["tool_call"]++
			if e.Phase == adaptor.PhaseStart {
				fmt.Printf("\n[tool %s]\n", e.Name)
			}
		case adaptor.ProcessInfo:
			counts["process."+e.Kind]++
		case adaptor.Notice:
			counts["notice."+e.Kind]++
			if e.Kind == adaptor.NoticeTranscriptItem && e.Item != nil {
				fmt.Printf("\n[item %s] %s\n", e.Item.Kind, e.Item.Text)
			}
		case adaptor.Dropped:
			// Default backpressure is drop-with-marker; WithEventBuffer /
			// WithBlockingEvents tune it at construction scope.
			counts["dropped"] += e.Count
			fmt.Printf("\n[dropped %d events]\n", e.Count)
		case adaptor.RunFinished:
			counts["run.finished"]++
		}
	}
	fmt.Println()

	res, err := stream.Result()

	if *cancelAfter > 0 {
		exampleutil.Check(err != nil, "expected a cancellation error when -cancel-after is set")
		exampleutil.Check(isCancellation(err), "expected cancellation-like error, got %v", err)
		exampleutil.PrintJSON(map[string]any{
			"example":       "stream",
			"agent":         exampleutil.LiveAgentSummary(agentCfg),
			"run_id":        stream.RunID(),
			"cancelled":     true,
			"event_count":   total,
			"event_counts":  counts,
			"cancel_after":  cancelAfter.String(),
			"error_message": err.Error(),
		})
		return
	}

	if err != nil {
		var runErr *adaptor.RunError
		if errors.As(err, &runErr) {
			exampleutil.Fatalf("stream failed (%s): %s", runErr.Reason, runErr.Message)
		}
		exampleutil.Fatalf("stream example: %v", err)
	}
	exampleutil.Check(total > 0, "expected to receive at least one event")
	exampleutil.Check(strings.TrimSpace(res.Text) != "", "expected non-empty text from %s", agentCfg.Agent)

	exampleutil.PrintJSON(map[string]any{
		"example":      "stream",
		"agent":        exampleutil.LiveAgentSummary(agentCfg),
		"run_id":       res.RunID,
		"event_count":  total,
		"event_counts": counts,
	})
}

// isCancellation keeps the cancel path readable: adaptor.Stream.Cancel aborts
// via context cancellation, so the terminal error is either a plain
// context.Canceled (infrastructure) or a *RunError with ReasonCancelled
// (the driver classified it first).
func isCancellation(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, adaptor.ErrRunCancelled) {
		return true
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "canceled") || strings.Contains(text, "cancelled") || strings.Contains(text, "deadline")
}
