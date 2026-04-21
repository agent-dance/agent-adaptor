package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"strings"
	"sync"
	"time"

	agentadaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/codex"
	"github.com/agent-dance/agent-adaptor/examples/internal/exampleutil"
)

func main() {
	model := flag.String("model", "gpt-5.4", "Codex model to use")
	command := flag.String("command", "", "Optional explicit Codex-compatible command. Defaults to the healthy external Codex command discovered from PATH.")
	prompt := flag.String("prompt", "Reply in three short lines for the codex-stream example.", "Prompt to send to Codex")
	timeout := flag.Duration("timeout", 3*time.Minute, "Maximum time to wait for the run")
	cancelAfter := flag.Duration("cancel-after", 0, "If greater than zero, cancel the run after this duration")
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
			"example":       "codex-stream",
			"cancelled":     true,
			"event_count":   eventCount,
			"event_counts":  snapshot,
			"cancel_after":  cancelAfter.String(),
			"error_message": err.Error(),
			"command": map[string]any{
				"path": commandPath,
				"note": commandNote,
			},
		})
		return
	}

	exampleutil.Must(err, "wait for codex-stream example")
	exampleutil.Check(result.DriverType == codex.DriverType, "expected driver type %q, got %q", codex.DriverType, result.DriverType)
	exampleutil.Check(result.ExitCode == 0, "expected exit code 0, got %d", result.ExitCode)
	exampleutil.Check(eventCount > 0, "expected to receive at least one event")
	exampleutil.Check(strings.TrimSpace(result.Output) != "", "expected non-empty output from Codex")

	exampleutil.PrintJSON(map[string]any{
		"example":      "codex-stream",
		"driver_type":  result.DriverType,
		"exit_code":    result.ExitCode,
		"event_count":  eventCount,
		"event_counts": snapshot,
		"command": map[string]any{
			"path": commandPath,
			"note": commandNote,
		},
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
