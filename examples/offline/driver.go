package main

import (
	"context"
	"fmt"
	"time"

	"github.com/agent-dance/agent-adaptor/capability"
	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/todo"
)

// This is a teaching fixture, not an adapter for a real provider. All events
// and raw bytes below are scripted; they cannot certify a provider capability.
// Only this file imports the extension SPI. main.go consumes the public Agent,
// Stream, Event and Result contracts just as it would with a built-in Driver.
type demoDriver struct{}

func (demoDriver) ValidateConfig(any) error { return nil }
func (demoDriver) StreamCapability() driver.StreamCapability {
	return driver.StreamCapability{Native: true, HITL: true}
}
func (demoDriver) Descriptor() driver.Descriptor {
	support := driver.ObservationSupport{Skills: true, Todos: true}
	return driver.Descriptor{
		Type: "offline-demo", DisplayName: "Scripted offline example",
		SystemPrompt: driver.SystemPromptCapability{Append: true},
		Observation:  driver.ObservationCapabilities{Batch: support, Streaming: support},
		RunPolicyCaps: driver.RunPolicyCapabilities{
			Permission: driver.HumanDecisionSupport{Ask: true},
			PlanReview: driver.HumanDecisionSupport{Ask: true},
			Question:   driver.QuestionSupport{Ask: true, AutoReject: true},
		},
	}
}

func (demoDriver) Run(ctx context.Context, req driver.Request, sink driver.EventSink) (driver.Response, error) {
	switch req.Prompt {
	case "show channels":
		return driver.Response{Output: fmt.Sprintf("prompt=%q append=%q", req.Prompt, req.AppendSystemPrompt)}, nil
	case "wait for budget":
		response := driver.Response{
			Output: "work started", Summary: "Interrupted demo",
			RawStreams: &driver.RawStreams{Stdout: "offline demo: work started\n"},
		}
		<-ctx.Done()
		return response, ctx.Err()
	case "review demo":
		return reviewDemo(ctx, sink)
	default:
		return driver.Response{}, fmt.Errorf("unknown offline script")
	}
}

func reviewDemo(ctx context.Context, sink driver.EventSink) (driver.Response, error) {
	at := time.Now().UTC()
	invocation := capability.Invocation{
		InvocationID: "demo-call", Ref: capability.Ref{Kind: capability.Skill, Key: "demo-review", Operation: "activate"},
		Phase: capability.Started, Evidence: capability.ProviderProtocol, Source: capability.Provider,
		OccurredAt: at,
	}
	if err := sink.EmitStream(driver.StreamPayload{Kind: driver.StreamCapabilityInvocation, Capability: &invocation}); err != nil {
		return driver.Response{}, err
	}
	snapshot := todo.Snapshot{
		Items:  []todo.Item{{ID: "demo-task", Content: "Review the chosen directory", Status: todo.Pending}},
		Source: todo.PlanUpdate, Revision: 1, OccurredAt: at,
	}
	if err := sink.EmitStream(driver.StreamPayload{Kind: driver.StreamTodoUpdated, Todo: &snapshot}); err != nil {
		return driver.Response{}, err
	}
	decisionSink, ok := sink.(driver.DecisionCapableSink)
	if !ok {
		return driver.Response{}, fmt.Errorf("offline example requires an approval responder")
	}
	answer, err := decisionSink.RequestDecision(ctx, driver.DecisionRequest{
		Kind: driver.HumanDecisionQuestion, Prompt: "Which directory should the demo review?",
		Choices: []driver.DecisionChoice{{Key: "docs", Label: "Documentation"}},
	})
	if err != nil {
		invocation.Phase = capability.Interrupted
		invocation.ErrorCode = capability.RunInterrupted
		invocation.OccurredAt = time.Now().UTC()
		_ = sink.EmitStream(driver.StreamPayload{Kind: driver.StreamCapabilityInvocation, Capability: &invocation})
		return driver.Response{}, err
	}
	if answer.Result != driver.DecisionAnswered || answer.Choice != "docs" {
		return driver.Response{}, fmt.Errorf("offline example expected the docs answer")
	}
	for _, payload := range []driver.StreamPayload{
		{Kind: driver.StreamTextStart, MessageID: "demo-message"},
		{Kind: driver.StreamTextContent, MessageID: "demo-message", Delta: "Reviewed docs."},
		{Kind: driver.StreamTextEnd, MessageID: "demo-message"},
	} {
		if err := sink.EmitStream(payload); err != nil {
			return driver.Response{}, err
		}
	}
	// An empty confirmed snapshot explicitly clears the display.
	snapshot.Items = []todo.Item{}
	snapshot.Revision = 2
	snapshot.OccurredAt = time.Now().UTC()
	if err := sink.EmitStream(driver.StreamPayload{Kind: driver.StreamTodoUpdated, Todo: &snapshot}); err != nil {
		return driver.Response{}, err
	}
	invocation.Phase = capability.Completed
	invocation.OccurredAt = time.Now().UTC()
	if err := sink.EmitStream(driver.StreamPayload{Kind: driver.StreamCapabilityInvocation, Capability: &invocation}); err != nil {
		return driver.Response{}, err
	}
	return driver.Response{Output: "Reviewed docs.", Summary: "Demo review complete"}, nil
}
