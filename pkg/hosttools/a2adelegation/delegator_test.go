package a2adelegation

import (
	"context"
	"encoding/json"
	"errors"
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

	terminal := mapper.Map(clienta2a.Event{
		Kind:      clienta2a.EventTerminal,
		TaskID:    "task-1",
		ContextID: "ctx-1",
		Status:    &clienta2a.TaskStatus{State: clienta2a.TaskStateInputRequired},
	})
	if len(terminal) != 1 || terminal[0].Kind != DelegationInputRequired {
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
		name       string
		allow      bool
		wantErr    bool
		wantStatus string
	}{
		{name: "default rejects", allow: false, wantErr: true, wantStatus: "failed"},
		{name: "policy allows", allow: true, wantErr: false, wantStatus: "input_required"},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			registry, err := NewRegistry(RemoteAgentSpec{Key: "research", AgentCard: &card, Policy: DelegationPolicy{AllowInputRequired: tc.allow}})
			if err != nil {
				t.Fatalf("registry: %v", err)
			}
			client := &fakeA2AClient{card: card, sendTask: clienta2a.Task{ID: "task-1", ContextID: "ctx-1", Status: clienta2a.TaskStatus{State: clienta2a.TaskStateInputRequired}}}
			delegator := NewDelegator(registry, NewEventBus(16))
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
	client := &fakeA2AClient{card: card, sendTask: clienta2a.Task{ID: "task-1", ContextID: "ctx-1", Status: clienta2a.TaskStatus{State: clienta2a.TaskStateCompleted}, Artifacts: []clienta2a.Artifact{{ID: "large", Name: "large.txt", Parts: []clienta2a.Part{{Kind: clienta2a.PartText, Text: "too large"}}}}}}
	delegator := NewDelegator(registry, NewEventBus(16))
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
	card clienta2a.AgentCard

	sendTask  clienta2a.Task
	lastSend  clienta2a.SendRequest
	sendCalls int

	streamCalls int

	getTasks []clienta2a.Task
	getErr   error

	cancelCalls int
	lastCancel  clienta2a.CancelTaskRequest
}

func (f *fakeA2AClient) AgentCard(context.Context) (clienta2a.AgentCard, error) { return f.card, nil }

func (f *fakeA2AClient) Send(_ context.Context, req clienta2a.SendRequest) (clienta2a.Task, error) {
	f.sendCalls++
	f.lastSend = req
	return f.sendTask, nil
}

func (f *fakeA2AClient) SendStream(context.Context, clienta2a.SendRequest) (*clienta2a.Stream, error) {
	f.streamCalls++
	return nil, errors.New("stream unavailable")
}

func (f *fakeA2AClient) GetTask(context.Context, clienta2a.GetTaskRequest) (clienta2a.Task, error) {
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
