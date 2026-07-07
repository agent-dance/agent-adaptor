package a2adelegation

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	bridgea2a "github.com/agent-dance/agent-adaptor/pkg/bridges/a2a"
	clienta2a "github.com/agent-dance/agent-adaptor/pkg/clients/a2a"
)

func TestRegistryRejectsDuplicateKeys(t *testing.T) {
	t.Parallel()
	card := clienta2a.AgentCard{Name: "Research", Capabilities: clienta2a.Capabilities{Streaming: true}}
	_, err := NewRegistry(
		RemoteAgentSpec{Key: "research", AgentCard: &card},
		RemoteAgentSpec{Key: "research", AgentCard: &card},
	)
	if err == nil {
		t.Fatal("expected duplicate registry key to fail")
	}
}

func TestParseToolInputRejectsArbitraryEndpointURL(t *testing.T) {
	t.Parallel()
	_, err := ParseToolInput([]byte(`{"agent":"research","objective":"do work","endpoint_url":"https://evil.example/a2a"}`))
	if err == nil {
		t.Fatal("expected endpoint_url to be rejected")
	}
	var derr *DelegationError
	if !errors.As(err, &derr) || derr.Code != "invalid_tool_input" {
		t.Fatalf("expected invalid_tool_input, got %T %[1]v", err)
	}
}

func TestParseToolInputRejectsUnknownNestedFields(t *testing.T) {
	t.Parallel()
	cases := []string{
		`{"agent":"research","objective":"do work","input":{"prompt":"hi","extra":true}}`,
		`{"agent":"research","objective":"do work","input":{"artifacts":[{"uri":"file://a","extra":true}]}}`,
		`{"agent":"research","objective":"do work","constraints":{"stream":true,"extra":true}}`,
	}
	for _, raw := range cases {
		if _, err := ParseToolInput([]byte(raw)); err == nil {
			t.Fatalf("expected unknown nested field to fail for %s", raw)
		}
	}
}

func TestParseToolInputTracksExplicitMaxArtifacts(t *testing.T) {
	t.Parallel()
	input, err := ParseToolInput([]byte(`{"agent":"research","objective":"do work","constraints":{"max_artifacts":0}}`))
	if err != nil {
		t.Fatalf("parse explicit max_artifacts: %v", err)
	}
	if !input.Constraints.MaxArtifactsSet || input.Constraints.MaxArtifacts != 0 {
		t.Fatalf("expected explicit max_artifacts=0, got %#v", input.Constraints)
	}
	input, err = ParseToolInput([]byte(`{"agent":"research","objective":"do work"}`))
	if err != nil {
		t.Fatalf("parse omitted max_artifacts: %v", err)
	}
	if input.Constraints.MaxArtifactsSet {
		t.Fatalf("max_artifacts should be omitted: %#v", input.Constraints)
	}
}

func TestToolSchemaAllowsOnlyRegistryKeyObjectiveInputAndConstraints(t *testing.T) {
	t.Parallel()
	schema := ToolSchema()
	props, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("schema properties missing: %#v", schema)
	}
	if _, ok := props["endpoint_url"]; ok {
		t.Fatal("schema must not expose arbitrary endpoint_url")
	}
	if schema["additionalProperties"] != false {
		t.Fatalf("expected additionalProperties=false, got %#v", schema["additionalProperties"])
	}
}

func TestEventMapperMapsA2ABridgeArtifactsAndTerminal(t *testing.T) {
	t.Parallel()
	mapper := newEventMapper(DelegationEvent{RunID: "run-1", DelegationID: "del-1", AgentKey: "research"})
	events := mapper.Map(clienta2a.Event{
		Kind:      clienta2a.EventArtifact,
		TaskID:    "task-1",
		ContextID: "ctx-1",
		Artifact: &clienta2a.Artifact{
			ID:    "assistant-output",
			Name:  bridgea2a.ArtifactAssistantOutput,
			Parts: []clienta2a.Part{{Kind: clienta2a.PartText, Text: "hello"}},
		},
	})
	if len(events) != 2 {
		t.Fatalf("expected synthesized start + text delta, got %#v", events)
	}
	if events[0].Kind != DelegationStarted || events[1].Kind != DelegationTextDelta || events[1].Delta != "hello" {
		t.Fatalf("unexpected mapped events: %#v", events)
	}

	terminal := mapper.terminalForState("task-1", "ctx-1", clienta2a.TaskStateInputRequired, nil)
	if terminal.Kind != DelegationInputRequired {
		t.Fatalf("input-required terminal mapping: %#v", terminal)
	}
}

func TestEventMapperPreservesArtifactURI(t *testing.T) {
	t.Parallel()
	mapper := newEventMapper(DelegationEvent{RunID: "run-1", DelegationID: "del-1", AgentKey: "research"})
	events := mapper.Map(clienta2a.Event{
		Kind:      clienta2a.EventArtifact,
		TaskID:    "task-1",
		ContextID: "ctx-1",
		Artifact: &clienta2a.Artifact{
			ID:    "notes",
			Name:  "notes.md",
			Parts: []clienta2a.Part{{Kind: clienta2a.PartURL, URL: "file:///notes.md", MediaType: "text/markdown"}},
		},
	})
	if len(events) != 2 {
		t.Fatalf("expected started + artifact events, got %#v", events)
	}
	artifact := events[1].Artifact
	if artifact == nil || artifact.URI != "file:///notes.md" || artifact.MediaType != "text/markdown" {
		t.Fatalf("expected artifact URI/media type to be preserved, got %#v", artifact)
	}
}

func TestResultFromTaskUsesAgentAdaptorResultArtifact(t *testing.T) {
	t.Parallel()
	result := resultFromTask(DelegationResult{DelegationID: "del-1", Agent: "research", RemoteProtocol: ProtocolA2A}, clienta2a.Task{
		ID:        "task-1",
		ContextID: "ctx-1",
		Status:    clienta2a.TaskStatus{State: clienta2a.TaskStateCompleted},
		Artifacts: []clienta2a.Artifact{
			{
				ID:   bridgea2a.ArtifactAgentAdaptorResult,
				Name: bridgea2a.ArtifactAgentAdaptorResult,
				Parts: []clienta2a.Part{{Kind: clienta2a.PartData, Data: map[string]any{
					"summary": "structured summary",
					"result":  map[string]any{"confidence": "high"},
				}}},
			},
			{ID: "notes", Name: "notes.md", Description: "notes", Parts: []clienta2a.Part{{Kind: clienta2a.PartURL, URL: "file:///notes.md", MediaType: "text/markdown"}}},
		},
	})
	if result.Status != "completed" || result.Summary != "structured summary" {
		t.Fatalf("unexpected result summary/status: %#v", result)
	}
	if result.Metadata["confidence"] != "high" {
		t.Fatalf("expected structured result metadata, got %#v", result.Metadata)
	}
	if len(result.Artifacts) != 1 || result.Artifacts[0].Name != "notes.md" {
		t.Fatalf("expected non-internal artifact reference, got %#v", result.Artifacts)
	}
	if result.Artifacts[0].URI != "file:///notes.md" {
		t.Fatalf("expected artifact URI to be preserved, got %#v", result.Artifacts[0])
	}
}

func TestDelegatorPollingHappyPathEmitsEventsAndStructuredResult(t *testing.T) {
	t.Parallel()
	card := clienta2a.AgentCard{Name: "Research", Capabilities: clienta2a.Capabilities{Streaming: false}}
	registry, err := NewRegistry(RemoteAgentSpec{Key: "research", AgentCard: &card, Tenant: "tenant-a", Policy: DelegationPolicy{PollInterval: time.Millisecond, MaxPolls: 3}})
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	bus := NewEventBus(16)
	client := &fakeA2AClient{
		card:     card,
		sendTask: clienta2a.Task{ID: "task-1", ContextID: "ctx-1", Status: clienta2a.TaskStatus{State: clienta2a.TaskStateWorking}},
		getTasks: []clienta2a.Task{{
			ID:        "task-1",
			ContextID: "ctx-1",
			Status:    clienta2a.TaskStatus{State: clienta2a.TaskStateCompleted},
			Messages:  []clienta2a.Message{{Role: "agent", Parts: []clienta2a.Part{{Kind: clienta2a.PartText, Text: "done"}}}},
			Artifacts: []clienta2a.Artifact{{ID: "notes", Name: "notes.md", Parts: []clienta2a.Part{{Kind: clienta2a.PartText, MediaType: "text/markdown"}}}},
		}},
	}
	delegator := NewDelegator(registry, bus)
	delegator.NewClient = func(RemoteAgentSpec) A2AClient { return client }
	delegator.NewID = func() string { return "del-1" }

	result, err := delegator.Delegate(context.Background(), DelegationRequest{RunID: "run-1", Agent: "research", Objective: "research this"})
	if err != nil {
		t.Fatalf("delegate: %v", err)
	}
	if result.Status != "completed" || result.RemoteTaskID != "task-1" || result.Summary != "done" {
		t.Fatalf("unexpected delegation result: %#v", result)
	}
	if client.lastSend.Tenant != "tenant-a" || client.lastSend.Message.Parts[0].Text != "research this" {
		t.Fatalf("unexpected send request: %#v", client.lastSend)
	}
	replayed := drainBus(t, bus, "run-1", 5)
	if replayed[0].Kind != DelegationStarted || replayed[len(replayed)-1].Kind != DelegationFinished {
		t.Fatalf("unexpected bus replay: %#v", replayed)
	}
}

func TestDelegatorRequireStreamingRejectsSendStreamFailure(t *testing.T) {
	t.Parallel()
	card := clienta2a.AgentCard{Name: "Research", Capabilities: clienta2a.Capabilities{Streaming: true}}
	registry, err := NewRegistry(RemoteAgentSpec{Key: "research", AgentCard: &card, Policy: DelegationPolicy{RequireStreaming: true, PollInterval: time.Millisecond, MaxPolls: 1}})
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	client := &fakeA2AClient{card: card, sendTask: clienta2a.Task{ID: "task-1", Status: clienta2a.TaskStatus{State: clienta2a.TaskStateCompleted}}}
	delegator := NewDelegator(registry, NewEventBus(16))
	delegator.NewClient = func(RemoteAgentSpec) A2AClient { return client }
	delegator.NewID = func() string { return "del-1" }

	result, err := delegator.Delegate(context.Background(), DelegationRequest{RunID: "run-1", Agent: "research", Objective: "research this", Stream: true})
	if err == nil {
		t.Fatal("expected streaming failure")
	}
	var derr *DelegationError
	if !errors.As(err, &derr) || derr.Code != "stream_unavailable" {
		t.Fatalf("expected stream_unavailable error, got %T %[1]v", err)
	}
	if result.Status != "failed" || client.streamCalls != 1 || client.sendCalls != 0 {
		t.Fatalf("expected failed result without polling fallback, result=%#v streamCalls=%d sendCalls=%d", result, client.streamCalls, client.sendCalls)
	}
}

func TestDelegatorInputRequiredPolicy(t *testing.T) {
	t.Parallel()
	card := clienta2a.AgentCard{Name: "Research", Capabilities: clienta2a.Capabilities{Streaming: false}}
	for _, tc := range []struct {
		name         string
		allow        bool
		wantErr      bool
		wantStatus   string
		wantTerminal DelegationEventKind
	}{
		{name: "default rejects", allow: false, wantErr: true, wantStatus: "failed", wantTerminal: DelegationFailed},
		{name: "policy allows", allow: true, wantErr: false, wantStatus: "input_required", wantTerminal: DelegationInputRequired},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			registry, err := NewRegistry(RemoteAgentSpec{Key: "research", AgentCard: &card, Policy: DelegationPolicy{AllowInputRequired: tc.allow}})
			if err != nil {
				t.Fatalf("registry: %v", err)
			}
			bus := NewEventBus(16)
			client := &fakeA2AClient{card: card, sendTask: clienta2a.Task{ID: "task-1", ContextID: "ctx-1", Status: clienta2a.TaskStatus{State: clienta2a.TaskStateInputRequired}}}
			delegator := NewDelegator(registry, bus)
			delegator.NewClient = func(RemoteAgentSpec) A2AClient { return client }
			delegator.NewID = func() string { return "del-1" }

			result, err := delegator.Delegate(context.Background(), DelegationRequest{RunID: "run-1", Agent: "research", Objective: "research this"})
			if tc.wantErr && err == nil {
				t.Fatal("expected input-required error")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if result.Status != tc.wantStatus {
				t.Fatalf("status: got %q want %q result=%#v", result.Status, tc.wantStatus, result)
			}
			if !tc.wantErr && result.Error != nil {
				t.Fatalf("allowed input-required should not set error: %#v", result.Error)
			}
			replayed := drainBus(t, bus, "run-1", 3)
			if replayed[len(replayed)-1].Kind != tc.wantTerminal {
				t.Fatalf("terminal: got %#v want %s in %#v", replayed[len(replayed)-1], tc.wantTerminal, replayed)
			}
		})
	}
}

func TestDelegatorMaxArtifactBytesPolicy(t *testing.T) {
	t.Parallel()
	card := clienta2a.AgentCard{Name: "Research", Capabilities: clienta2a.Capabilities{Streaming: false}}
	registry, err := NewRegistry(RemoteAgentSpec{Key: "research", AgentCard: &card, Policy: DelegationPolicy{MaxArtifactBytes: 4}})
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	bus := NewEventBus(16)
	client := &fakeA2AClient{card: card, sendTask: clienta2a.Task{ID: "task-1", ContextID: "ctx-1", Status: clienta2a.TaskStatus{State: clienta2a.TaskStateCompleted}, Artifacts: []clienta2a.Artifact{{ID: "large", Name: "large.txt", Parts: []clienta2a.Part{{Kind: clienta2a.PartText, Text: "too large"}}}}}}
	delegator := NewDelegator(registry, bus)
	delegator.NewClient = func(RemoteAgentSpec) A2AClient { return client }
	delegator.NewID = func() string { return "del-1" }

	result, err := delegator.Delegate(context.Background(), DelegationRequest{RunID: "run-1", Agent: "research", Objective: "research this"})
	if err == nil {
		t.Fatal("expected artifact size policy error")
	}
	var derr *DelegationError
	if !errors.As(err, &derr) || derr.Code != "artifact_too_large" {
		t.Fatalf("expected artifact_too_large error, got %T %[1]v", err)
	}
	if result.Status != "failed" {
		t.Fatalf("expected failed result, got %#v", result)
	}
	replayed := drainBus(t, bus, "run-1", 4)
	if replayed[len(replayed)-1].Kind != DelegationFailed {
		t.Fatalf("policy failure should publish failed terminal last, got %#v", replayed)
	}
}

func TestDelegatorSendsInputArtifactsAsURLParts(t *testing.T) {
	t.Parallel()
	card := clienta2a.AgentCard{Name: "Research", Capabilities: clienta2a.Capabilities{Streaming: false}}
	registry, err := NewRegistry(RemoteAgentSpec{Key: "research", AgentCard: &card})
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	client := &fakeA2AClient{card: card, sendTask: clienta2a.Task{ID: "task-1", ContextID: "ctx-1", Status: clienta2a.TaskStatus{State: clienta2a.TaskStateCompleted}}}
	delegator := NewDelegator(registry, NewEventBus(16))
	delegator.NewClient = func(RemoteAgentSpec) A2AClient { return client }
	delegator.NewID = func() string { return "del-1" }

	_, err = delegator.Delegate(context.Background(), DelegationRequest{RunID: "run-1", Agent: "research", Objective: "research this", Artifacts: []InputArtifact{{Name: "notes.md", URI: "file:///notes.md", MediaType: "text/markdown"}}})
	if err != nil {
		t.Fatalf("delegate: %v", err)
	}
	parts := client.lastSend.Message.Parts
	if len(parts) != 2 || parts[1].Kind != clienta2a.PartURL || parts[1].URL != "file:///notes.md" || parts[1].Filename != "notes.md" || parts[1].MediaType != "text/markdown" {
		t.Fatalf("expected URL artifact part, got %#v", parts)
	}
}

func TestDelegatorRejectsInputArtifactWithoutURI(t *testing.T) {
	t.Parallel()
	card := clienta2a.AgentCard{Name: "Research", Capabilities: clienta2a.Capabilities{Streaming: false}}
	registry, err := NewRegistry(RemoteAgentSpec{Key: "research", AgentCard: &card})
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	delegator := NewDelegator(registry, NewEventBus(16))
	delegator.NewClient = func(RemoteAgentSpec) A2AClient { return &fakeA2AClient{card: card} }
	delegator.NewID = func() string { return "del-1" }

	_, err = delegator.Delegate(context.Background(), DelegationRequest{RunID: "run-1", Agent: "research", Objective: "research this", Artifacts: []InputArtifact{{Name: "notes.md"}}})
	if err == nil {
		t.Fatal("expected invalid artifact error")
	}
	var derr *DelegationError
	if !errors.As(err, &derr) || derr.Code != "invalid_artifact" {
		t.Fatalf("expected invalid_artifact error, got %T %[1]v", err)
	}
}

func TestDelegatorMaxArtifactsLimitsFinalResultOnly(t *testing.T) {
	t.Parallel()
	card := clienta2a.AgentCard{Name: "Research", Capabilities: clienta2a.Capabilities{Streaming: false}}
	registry, err := NewRegistry(RemoteAgentSpec{Key: "research", AgentCard: &card})
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	client := &fakeA2AClient{card: card, sendTask: clienta2a.Task{ID: "task-1", ContextID: "ctx-1", Status: clienta2a.TaskStatus{State: clienta2a.TaskStateCompleted}, Artifacts: []clienta2a.Artifact{{ID: "a", Name: "a.txt"}, {ID: "b", Name: "b.txt"}}}}
	delegator := NewDelegator(registry, NewEventBus(16))
	delegator.NewClient = func(RemoteAgentSpec) A2AClient { return client }
	delegator.NewID = func() string { return "del-1" }
	max := 1

	result, err := delegator.Delegate(context.Background(), DelegationRequest{RunID: "run-1", Agent: "research", Objective: "research this", MaxArtifacts: &max})
	if err != nil {
		t.Fatalf("delegate: %v", err)
	}
	if len(result.Artifacts) != 1 || result.Artifacts[0].ID != "a" {
		t.Fatalf("expected final artifacts to be limited to first artifact, got %#v", result.Artifacts)
	}
}

func TestDelegatorPollingCancellationCascadesToRemoteTask(t *testing.T) {
	t.Parallel()
	card := clienta2a.AgentCard{Name: "Research"}
	registry, err := NewRegistry(RemoteAgentSpec{Key: "research", AgentCard: &card, Tenant: "tenant-a", Policy: DelegationPolicy{PollInterval: time.Millisecond, MaxPolls: 10}})
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	client := &fakeA2AClient{
		card:     card,
		sendTask: clienta2a.Task{ID: "task-1", ContextID: "ctx-1", Status: clienta2a.TaskStatus{State: clienta2a.TaskStateWorking}},
		getErr:   context.DeadlineExceeded,
	}
	delegator := NewDelegator(registry, NewEventBus(16))
	delegator.NewClient = func(RemoteAgentSpec) A2AClient { return client }
	delegator.NewID = func() string { return "del-1" }
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err = delegator.Delegate(ctx, DelegationRequest{RunID: "run-1", Agent: "research", Objective: "research this"})
	if err == nil {
		t.Fatal("expected cancellation error")
	}
	if client.cancelCalls != 1 || client.lastCancel.TaskID != "task-1" || client.lastCancel.Tenant != "tenant-a" {
		t.Fatalf("expected remote cancel cascade, calls=%d req=%#v", client.cancelCalls, client.lastCancel)
	}
}

func TestDelegatorStreamingCancellationCascadesWithEffectiveTenant(t *testing.T) {
	t.Parallel()
	card := clienta2a.AgentCard{Name: "Research", Capabilities: clienta2a.Capabilities{Streaming: true}}
	registry, err := NewRegistry(RemoteAgentSpec{Key: "research", AgentCard: &card, Tenant: "spec-tenant"})
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	stream := &fakeA2AStream{events: make(chan streamRecv, 1), closed: make(chan struct{})}
	stream.events <- streamRecv{event: clienta2a.Event{Kind: clienta2a.EventStatus, TaskID: "task-1", ContextID: "ctx-1", Status: &clienta2a.TaskStatus{State: clienta2a.TaskStateWorking}}}
	bus := NewEventBus(16)
	client := &fakeA2AClient{card: card, stream: stream}
	delegator := NewDelegator(registry, bus)
	delegator.NewClient = func(RemoteAgentSpec) A2AClient { return client }
	delegator.NewID = func() string { return "del-1" }
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	result, err := delegator.Delegate(ctx, DelegationRequest{RunID: "run-1", Agent: "research", Objective: "research this", Stream: true, Tenant: "call-tenant"})
	if err == nil {
		t.Fatal("expected streaming cancellation error")
	}
	var derr *DelegationError
	if !errors.As(err, &derr) || derr.Code != "cancelled" {
		t.Fatalf("expected cancelled error, got %T %[1]v", err)
	}
	if result.Status != "cancelled" {
		t.Fatalf("expected cancelled result, got %#v", result)
	}
	if client.cancelCalls != 1 || client.lastCancel.TaskID != "task-1" || client.lastCancel.Tenant != "call-tenant" {
		t.Fatalf("expected streaming remote cancel with effective tenant, calls=%d req=%#v", client.cancelCalls, client.lastCancel)
	}
	replayed := drainBus(t, bus, "run-1", 3)
	if replayed[len(replayed)-1].Kind != DelegationCancelled {
		t.Fatalf("expected cancelled terminal, got %#v", replayed)
	}
}

func TestDelegatorStreamingRecoveryUsesEffectiveTenant(t *testing.T) {
	t.Parallel()
	card := clienta2a.AgentCard{Name: "Research", Capabilities: clienta2a.Capabilities{Streaming: true}}
	registry, err := NewRegistry(RemoteAgentSpec{Key: "research", AgentCard: &card, Tenant: "spec-tenant"})
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	stream := &fakeA2AStream{events: make(chan streamRecv, 1), closed: make(chan struct{})}
	stream.events <- streamRecv{event: clienta2a.Event{Kind: clienta2a.EventStatus, TaskID: "task-1", ContextID: "ctx-1", Status: &clienta2a.TaskStatus{State: clienta2a.TaskStateCompleted}}}
	close(stream.events)
	client := &fakeA2AClient{
		card:   card,
		stream: stream,
		getTasks: []clienta2a.Task{{
			ID:        "task-1",
			ContextID: "ctx-1",
			Status:    clienta2a.TaskStatus{State: clienta2a.TaskStateCompleted},
			Messages:  []clienta2a.Message{{Role: "agent", Parts: []clienta2a.Part{{Kind: clienta2a.PartText, Text: "done"}}}},
		}},
	}
	delegator := NewDelegator(registry, NewEventBus(16))
	delegator.NewClient = func(RemoteAgentSpec) A2AClient { return client }
	delegator.NewID = func() string { return "del-1" }

	result, err := delegator.Delegate(context.Background(), DelegationRequest{RunID: "run-1", Agent: "research", Objective: "research this", Stream: true, Tenant: "call-tenant"})
	if err != nil {
		t.Fatalf("delegate: %v", err)
	}
	if result.Status != "completed" || result.Summary != "done" {
		t.Fatalf("unexpected result: %#v", result)
	}
	if client.getCalls != 1 || client.lastGet.TaskID != "task-1" || client.lastGet.Tenant != "call-tenant" {
		t.Fatalf("expected recovery get with effective tenant, calls=%d req=%#v", client.getCalls, client.lastGet)
	}
}

func TestDelegatorStreamingCancelBeforeTaskIDPublishesTerminal(t *testing.T) {
	t.Parallel()
	card := clienta2a.AgentCard{Name: "Research", Capabilities: clienta2a.Capabilities{Streaming: true}}
	registry, err := NewRegistry(RemoteAgentSpec{Key: "research", AgentCard: &card})
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	stream := &fakeA2AStream{events: make(chan streamRecv), closed: make(chan struct{})}
	bus := NewEventBus(16)
	client := &fakeA2AClient{card: card, stream: stream}
	delegator := NewDelegator(registry, bus)
	delegator.NewClient = func(RemoteAgentSpec) A2AClient { return client }
	delegator.NewID = func() string { return "del-1" }
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()

	result, err := delegator.Delegate(ctx, DelegationRequest{RunID: "run-1", Agent: "research", Objective: "research this", Stream: true})
	if err == nil {
		t.Fatal("expected cancellation before first task id")
	}
	if result.Status != "cancelled" || client.cancelCalls != 0 {
		t.Fatalf("unexpected result=%#v cancelCalls=%d", result, client.cancelCalls)
	}
	replayed := drainBus(t, bus, "run-1", 1)
	if replayed[0].Kind != DelegationCancelled || replayed[0].RemoteTaskID != "" {
		t.Fatalf("expected cancelled terminal without remote task id, got %#v", replayed)
	}
}

func TestDelegatorStreamingRecvContextErrorReportsCancelled(t *testing.T) {
	t.Parallel()
	card := clienta2a.AgentCard{Name: "Research", Capabilities: clienta2a.Capabilities{Streaming: true}}
	registry, err := NewRegistry(RemoteAgentSpec{Key: "research", AgentCard: &card})
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	stream := &fakeA2AStream{events: make(chan streamRecv, 1), closed: make(chan struct{})}
	stream.events <- streamRecv{err: context.DeadlineExceeded}
	bus := NewEventBus(16)
	client := &fakeA2AClient{card: card, stream: stream}
	delegator := NewDelegator(registry, bus)
	delegator.NewClient = func(RemoteAgentSpec) A2AClient { return client }
	delegator.NewID = func() string { return "del-1" }
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	result, err := delegator.Delegate(ctx, DelegationRequest{RunID: "run-1", Agent: "research", Objective: "research this", Stream: true})
	if err == nil {
		t.Fatal("expected cancelled stream recv error")
	}
	var derr *DelegationError
	if !errors.As(err, &derr) || derr.Code != "cancelled" || result.Status != "cancelled" {
		t.Fatalf("expected cancelled result/error, result=%#v err=%T %[2]v", result, err)
	}
	replayed := drainBus(t, bus, "run-1", 1)
	if replayed[0].Kind != DelegationCancelled {
		t.Fatalf("expected cancelled terminal, got %#v", replayed)
	}
}

func TestDelegatorStreamingTerminalMessageCompletes(t *testing.T) {
	t.Parallel()
	card := clienta2a.AgentCard{Name: "Research", Capabilities: clienta2a.Capabilities{Streaming: true}}
	registry, err := NewRegistry(RemoteAgentSpec{Key: "research", AgentCard: &card})
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	stream := &fakeA2AStream{events: make(chan streamRecv, 1), closed: make(chan struct{})}
	stream.events <- streamRecv{event: clienta2a.Event{
		Kind:      clienta2a.EventTerminal,
		TaskID:    "task-1",
		ContextID: "ctx-1",
		Message:   &clienta2a.Message{Role: "agent", TaskID: "task-1", ContextID: "ctx-1", Parts: []clienta2a.Part{{Kind: clienta2a.PartText, Text: "done"}}},
	}}
	close(stream.events)
	bus := NewEventBus(16)
	client := &fakeA2AClient{card: card, stream: stream}
	delegator := NewDelegator(registry, bus)
	delegator.NewClient = func(RemoteAgentSpec) A2AClient { return client }
	delegator.NewID = func() string { return "del-1" }

	result, err := delegator.Delegate(context.Background(), DelegationRequest{RunID: "run-1", Agent: "research", Objective: "research this", Stream: true})
	if err != nil {
		t.Fatalf("delegate: %v", err)
	}
	if result.Status != "completed" || result.Summary != "done" || client.cancelCalls != 0 {
		t.Fatalf("unexpected streaming terminal message result=%#v cancelCalls=%d", result, client.cancelCalls)
	}
	replayed := drainBus(t, bus, "run-1", 4)
	if replayed[len(replayed)-1].Kind != DelegationFinished {
		t.Fatalf("expected finished terminal, got %#v", replayed)
	}
}

func TestDelegatorTimeoutCoversAgentCardDiscovery(t *testing.T) {
	t.Parallel()
	registry, err := NewRegistry(RemoteAgentSpec{Key: "research", AgentCardURL: "https://agent.example/card"})
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	client := &fakeA2AClient{cardErr: context.DeadlineExceeded}
	delegator := NewDelegator(registry, NewEventBus(16))
	delegator.NewClient = func(RemoteAgentSpec) A2AClient { return client }
	delegator.NewID = func() string { return "del-1" }

	result, err := delegator.Delegate(context.Background(), DelegationRequest{RunID: "run-1", Agent: "research", Objective: "research this", Timeout: time.Millisecond})
	if err == nil {
		t.Fatal("expected agent card timeout")
	}
	var derr *DelegationError
	if !errors.As(err, &derr) || derr.Code != "agent_unavailable" {
		t.Fatalf("expected agent_unavailable timeout, got %T %[1]v", err)
	}
	if result.Status != "failed" {
		t.Fatalf("expected failed result, got %#v", result)
	}
}

func TestEventBusDeduplicatesTerminalEventsAndReplays(t *testing.T) {
	t.Parallel()
	bus := NewEventBus(8)
	if !bus.Publish(DelegationEvent{RunID: "run-1", DelegationID: "del-1", Kind: DelegationStarted}) {
		t.Fatal("expected started event to publish")
	}
	if !bus.Publish(DelegationEvent{RunID: "run-1", DelegationID: "del-1", Kind: DelegationFinished}) {
		t.Fatal("expected first terminal event to publish")
	}
	if bus.Publish(DelegationEvent{RunID: "run-1", DelegationID: "del-1", Kind: DelegationFailed}) {
		t.Fatal("duplicate terminal event should be suppressed")
	}
	replayed := drainBus(t, bus, "run-1", 2)
	if len(replayed) != 2 || replayed[1].Kind != DelegationFinished {
		t.Fatalf("unexpected replayed events: %#v", replayed)
	}
}

func TestEventBusReplayAboveSubscriberBufferDoesNotDeadlock(t *testing.T) {
	t.Parallel()
	bus := NewEventBus(64)
	for i := 0; i < 40; i++ {
		if !bus.Publish(DelegationEvent{RunID: "run-1", DelegationID: "del-1", Kind: DelegationStatus, Status: "working"}) {
			t.Fatalf("publish %d failed", i)
		}
	}
	replayed := drainBus(t, bus, "run-1", 40)
	if len(replayed) != 40 {
		t.Fatalf("expected 40 replayed events, got %d", len(replayed))
	}
}

func TestEventBusPublishDoesNotBlockOnFullSubscriber(t *testing.T) {
	t.Parallel()
	bus := NewEventBus(0)
	ctx, cancel := context.WithCancel(context.Background())
	ch := bus.SubscribeRun(ctx, "run-1")
	for i := 0; i < subscriberBuffer; i++ {
		bus.Publish(DelegationEvent{RunID: "run-1", DelegationID: "del-1", Kind: DelegationStatus})
	}
	done := make(chan struct{})
	go func() {
		bus.Publish(DelegationEvent{RunID: "run-1", DelegationID: "del-1", Kind: DelegationStatus})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("publish blocked on full subscriber")
	}
	cancel()
	for range ch {
	}
	if !bus.Publish(DelegationEvent{RunID: "run-1", DelegationID: "del-1", Kind: DelegationStatus}) {
		t.Fatal("publish after unsubscribe should still accept event")
	}
}

func TestEventBusClearRunDropsStateAndClosesSubscribers(t *testing.T) {
	t.Parallel()
	bus := NewEventBus(8)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := bus.SubscribeRun(ctx, "run-1")
	bus.Publish(DelegationEvent{RunID: "run-1", DelegationID: "del-1", Kind: DelegationStarted})
	bus.Publish(DelegationEvent{RunID: "run-1", DelegationID: "del-1", Kind: DelegationFinished})
	bus.ClearRun("run-1")
	deadline := time.After(time.Second)
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				goto closed
			}
		case <-deadline:
			t.Fatal("subscriber did not close after ClearRun")
		}
	}
closed:
	if replayed := drainAvailableBus(t, bus, "run-1"); len(replayed) != 0 {
		t.Fatalf("expected replay to be cleared, got %#v", replayed)
	}
	if !bus.Publish(DelegationEvent{RunID: "run-1", DelegationID: "del-1", Kind: DelegationFailed}) {
		t.Fatal("terminal state should be cleared for run")
	}
}

func drainBus(t *testing.T, bus *EventBus, runID string, want int) []DelegationEvent {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := bus.SubscribeRun(ctx, runID)
	out := make([]DelegationEvent, 0, want)
	for len(out) < want {
		select {
		case ev := <-ch:
			out = append(out, ev)
		case <-time.After(time.Second):
			t.Fatalf("timeout waiting for replay event %d/%d", len(out), want)
		}
	}
	return out
}

func drainAvailableBus(t *testing.T, bus *EventBus, runID string) []DelegationEvent {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := bus.SubscribeRun(ctx, runID)
	out := []DelegationEvent{}
	for {
		select {
		case ev := <-ch:
			out = append(out, ev)
		default:
			return out
		}
	}
}

func TestMCPServerDelegatesToolCallAndReturnsStructuredResult(t *testing.T) {
	t.Parallel()
	card := clienta2a.AgentCard{Name: "Research", Capabilities: clienta2a.Capabilities{Streaming: false}}
	registry, err := NewRegistry(RemoteAgentSpec{Key: "research", AgentCard: &card, Tenant: "tenant-a", Policy: DelegationPolicy{PollInterval: time.Millisecond, MaxPolls: 2}})
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	client := &fakeA2AClient{
		card:     card,
		sendTask: clienta2a.Task{ID: "task-1", ContextID: "ctx-1", Status: clienta2a.TaskStatus{State: clienta2a.TaskStateCompleted}, Messages: []clienta2a.Message{{Role: "agent", Parts: []clienta2a.Part{{Kind: clienta2a.PartText, Text: "done"}}}}},
	}
	delegator := NewDelegator(registry, NewEventBus(16))
	delegator.NewClient = func(RemoteAgentSpec) A2AClient { return client }
	delegator.NewID = func() string { return "del-1" }

	server := httptest.NewServer(NewMCPServer(delegator, MCPServerOptions{RunID: "run-1", BearerToken: "token"}).Handler())
	defer server.Close()
	rpcBody := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"delegate_to_agent","arguments":{"agent":"research","objective":"research this","constraints":{"stream":false}}}}`
	req, err := http.NewRequest(http.MethodPost, server.URL, strings.NewReader(rpcBody))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer token")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post rpc: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	var envelope struct {
		Result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		} `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if envelope.Result.IsError || len(envelope.Result.Content) != 1 {
		t.Fatalf("unexpected tool response: %#v", envelope.Result)
	}
	var result DelegationResult
	if err := json.Unmarshal([]byte(envelope.Result.Content[0].Text), &result); err != nil {
		t.Fatalf("decode delegation result: %v", err)
	}
	if result.DelegationID != "del-1" || result.RemoteTaskID != "task-1" || result.Status != "completed" || result.Summary != "done" {
		t.Fatalf("unexpected delegation result: %#v", result)
	}
}

func TestMCPServerRejectsMissingBearerToken(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(NewMCPServer(NewDelegator(nil, nil), MCPServerOptions{BearerToken: "token"}).Handler())
	defer server.Close()
	resp, err := http.Post(server.URL, "application/json", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	if err != nil {
		t.Fatalf("post rpc: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected unauthorized, got %d", resp.StatusCode)
	}
}

type fakeA2AClient struct {
	card    clienta2a.AgentCard
	cardErr error

	sendTask  clienta2a.Task
	lastSend  clienta2a.SendRequest
	sendCalls int

	stream      A2AStream
	streamCalls int

	getTasks []clienta2a.Task
	getErr   error
	lastGet  clienta2a.GetTaskRequest
	getCalls int

	cancelCalls int
	lastCancel  clienta2a.CancelTaskRequest
}

func (f *fakeA2AClient) AgentCard(ctx context.Context) (clienta2a.AgentCard, error) {
	if f.cardErr != nil {
		<-ctx.Done()
		return clienta2a.AgentCard{}, ctx.Err()
	}
	return f.card, nil
}

func (f *fakeA2AClient) Send(_ context.Context, req clienta2a.SendRequest) (clienta2a.Task, error) {
	f.sendCalls++
	f.lastSend = req
	return f.sendTask, nil
}

func (f *fakeA2AClient) SendStream(context.Context, clienta2a.SendRequest) (A2AStream, error) {
	f.streamCalls++
	if f.stream != nil {
		return f.stream, nil
	}
	return nil, errors.New("stream unavailable")
}

func (f *fakeA2AClient) GetTask(_ context.Context, req clienta2a.GetTaskRequest) (clienta2a.Task, error) {
	f.getCalls++
	f.lastGet = req
	if f.getErr != nil {
		return clienta2a.Task{}, f.getErr
	}
	if len(f.getTasks) == 0 {
		return f.sendTask, nil
	}
	task := f.getTasks[0]
	f.getTasks = f.getTasks[1:]
	return task, nil
}

func (f *fakeA2AClient) CancelTask(_ context.Context, req clienta2a.CancelTaskRequest) (clienta2a.Task, error) {
	f.cancelCalls++
	f.lastCancel = req
	return clienta2a.Task{ID: req.TaskID, Status: clienta2a.TaskStatus{State: clienta2a.TaskStateCanceled}}, nil
}

type fakeA2AStream struct {
	events chan streamRecv
	closed chan struct{}
}

func (s *fakeA2AStream) Recv() (clienta2a.Event, error) {
	item, ok := <-s.events
	if !ok {
		return clienta2a.Event{}, io.EOF
	}
	return item.event, item.err
}

func (s *fakeA2AStream) Close() error {
	select {
	case <-s.closed:
	default:
		close(s.closed)
	}
	return nil
}
