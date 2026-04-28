package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	agentadaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/examples/internal/exampleutil"
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

	sdk := agentadaptor.New(
		agentadaptor.WithDefaultAgent(exampleutil.NewLiveAgentBinding(agentCfg)),
	)

	handle, err := sdk.Start(ctx, *prompt)
	exampleutil.Must(err, "start codex-stream example")

	if *cancelAfter > 0 {
		time.AfterFunc(*cancelAfter, func() {
			_ = handle.Cancel(context.Background())
		})
	}

	var (
		mu          sync.Mutex
		totalEvents int
		counts      = map[string]int{}
	)
	eventsDone := make(chan struct{})
	go func() {
		defer close(eventsDone)
		for event := range handle.Events() {
			switch event.Type {
			case agentadaptor.RunEventChunk:
				fmt.Printf("[chunk %s] %d bytes\n", event.Stream, len(event.Bytes))
			case agentadaptor.RunEventItem:
				if event.Item != nil {
					fmt.Printf("[item %s] %s\n", event.Item.Kind, event.Item.Text)
				}
			default:
				fmt.Printf("[%s] %s\n", event.Type, event.Text)
			}
			mu.Lock()
			totalEvents++
			counts[string(event.Type)]++
			mu.Unlock()
		}
	}()

	result, err := handle.Wait(ctx)
	<-eventsDone

	mu.Lock()
	snapshot := make(map[string]int, len(counts))
	for key, value := range counts {
		snapshot[key] = value
	}
	eventCount := totalEvents
	mu.Unlock()

	if *cancelAfter > 0 {
		exampleutil.Check(err != nil, "expected a cancellation error when -cancel-after is set")
		exampleutil.Check(isCancellation(err), "expected cancellation-like error, got %v", err)
		exampleutil.PrintJSON(map[string]any{
			"example":       "stream",
			"agent":         exampleutil.LiveAgentSummary(agentCfg),
			"cancelled":     true,
			"event_count":   eventCount,
			"event_counts":  snapshot,
			"cancel_after":  cancelAfter.String(),
			"error_message": err.Error(),
		})
		return
	}

	exampleutil.Must(err, "wait for stream example")
	exampleutil.Check(result.DriverType == agentCfg.DriverType, "expected driver type %q, got %q", agentCfg.DriverType, result.DriverType)
	exampleutil.Check(result.ExitCode == 0, "expected exit code 0, got %d", result.ExitCode)
	exampleutil.Check(eventCount > 0, "expected to receive at least one event")
	exampleutil.Check(strings.TrimSpace(result.Output) != "", "expected non-empty output from %s", agentCfg.Agent)

	exampleutil.PrintJSON(map[string]any{
		"example":      "stream",
		"agent":        exampleutil.LiveAgentSummary(agentCfg),
		"driver_type":  result.DriverType,
		"exit_code":    result.ExitCode,
		"event_count":  eventCount,
		"event_counts": snapshot,
	})
}

func isCancellation(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "canceled") || strings.Contains(text, "cancelled") || strings.Contains(text, "deadline")
}
