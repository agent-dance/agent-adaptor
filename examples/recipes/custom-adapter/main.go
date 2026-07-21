package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	agentadaptor "github.com/agent-dance/agent-adaptor"
)

type config struct{ Prefix string }
type adapter struct{}

func (adapter) Descriptor() agentadaptor.DriverDescriptor {
	return agentadaptor.DriverDescriptor{Type: "echo", DisplayName: "Echo Adapter"}
}

func (adapter) ValidateConfig(value any) error {
	if _, ok := value.(config); !ok {
		return errors.New("echo adapter requires config")
	}
	return nil
}

func (adapter) Run(ctx context.Context, req agentadaptor.DriverRunRequest, sink agentadaptor.EventSink) (agentadaptor.DriverRunResult, error) {
	select {
	case <-ctx.Done():
		return agentadaptor.DriverRunResult{}, ctx.Err()
	default:
	}
	output := req.Config.(config).Prefix + req.Prompt
	item := agentadaptor.TranscriptItem{Kind: agentadaptor.TranscriptAssistant, Text: output}
	if err := sink.Emit(agentadaptor.RunEvent{Type: agentadaptor.RunEventItem, Item: &item}); err != nil {
		return agentadaptor.DriverRunResult{}, err
	}
	return agentadaptor.DriverRunResult{
		Output: output, RawStreams: &agentadaptor.RawStreams{Stdout: output + "\n"},
		Transcript: []agentadaptor.TranscriptItem{item}, Summary: "echo complete",
		Result: map[string]any{"echoed": true}, ExitCode: 0,
	}, nil
}

func main() {
	binding := agentadaptor.BindTyped(adapter{}, config{Prefix: "echo: "})
	sdk := agentadaptor.New(agentadaptor.WithDefaultAgent(binding))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result, err := sdk.Run(ctx, "custom adapters use the same Runner contract")
	if err != nil {
		log.Fatal(err)
	}
	if result.Failure != nil {
		log.Fatalf("run failed: %s", result.Failure.Message)
	}
	fmt.Println(result.Output)
}
