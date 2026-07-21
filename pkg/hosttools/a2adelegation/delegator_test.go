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

func TestParseToolInputRejectsInvalidTimeouts(t *testing.T) {
	t.Parallel()
	cases := []string{
		`{"agent":"research","objective":"do work","constraints":{"timeout_seconds":0}}`,
		`{"agent":"research","objective":"do work","constraints":{"timeout_seconds":-1}}`,
		`{"agent":"research","objective":"do work","constraints":{"timeout_seconds":9223372037}}`,
	}
	for _, raw := range cases {
		if _, err := ParseToolInput([]byte(raw)); err == nil {
			t.Fatalf("expected invalid timeout to fail for %s", raw)
		}
	}
}

func TestParseToolInputTracksHistoryLength(t *testing.T) {
	t.Parallel()
	input, err := ParseToolInput([]byte(`{"agent":"research","objective":"do work","constraints":{"history_length":0}}`))
	if err != nil {
		t.Fatalf("parse explicit history_length: %v", err)
	}
	if !input.Constraints.HistoryLengthSet || input.Constraints.HistoryLength != 0 {
		t.Fatalf("expected explicit history_length=0, got %#v", input.Constraints)
	}
	if _, err := ParseToolInput([]byte(`{"agent":"research","objective":"do work","constraints":{"history_length":-1}}`)); err == nil {
		t.Fatal("expected negative history_length to fail")
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
	input := props["input"].(map[string]any)
	artifacts := input["properties"].(map[string]any)["artifacts"].(map[string]any)
	item := artifacts["items"].(map[string]any)
	required := item["required"].([]string)
	if len(required) != 1 || required[0] != "uri" {
		t.Fatalf("artifact required fields = %#v", required)
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
	if len(events) != 3 {
		t.Fatalf("expected delegation + synthesized text start/delta, got %#v", events)
	}
	if events[0].Kind != DelegationStarted || events[1].Kind != DelegationTextStart || events[2].Kind != DelegationTextDelta || events[2].Delta != "hello" {
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
	}, false)
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

func TestResultFromTaskOptInPreservesRemoteArtifacts(t *testing.T) {
	t.Parallel()
	result := resultFromTask(DelegationResult{DelegationID: "del-1", Agent: "research", RemoteProtocol: ProtocolA2A}, clienta2a.Task{
		ID:        "task-1",
		ContextID: "ctx-1",
		Status:    clienta2a.TaskStatus{State: clienta2a.TaskStateCompleted},
		Artifacts: []clienta2a.Artifact{
			{
				ID:   "artifact-1",
				Name: "artifact-1",
				Parts: []clienta2a.Part{
					{Kind: clienta2a.PartText, Text: "summary"},
					{Kind: clienta2a.PartData, Data: map[string]any{"state": "passed"}},
				},
				Metadata: map[string]any{"source": "remote"},
			},
		},
	}, true)
	if len(result.RemoteArtifacts) != 1 {
		t.Fatalf("expected one remote artifact, got %#v", result.RemoteArtifacts)
	}
	if len(result.RemoteArtifacts[0].Parts) != 2 || result.RemoteArtifacts[0].Parts[1].Data.(map[string]any)["state"] != "passed" {
		t.Fatalf("unexpected remote artifact payload: %#v", result.RemoteArtifacts)
	}
	if result.RemoteArtifacts[0].Metadata["source"] != "remote" {
		t.Fatalf("expected remote artifact metadata, got %#v", result.RemoteArtifacts[0].Metadata)
	}
}

func TestResultFromTaskFallsBackToStatusMessageOnFailure(t *testing.T) {
	t.Parallel()
	result := resultFromTask(DelegationResult{DelegationID: "del-1", Agent: "research", RemoteProtocol: ProtocolA2A}, clienta2a.Task{
		ID:        "task-1",
		ContextID: "ctx-1",
		Status: clienta2a.TaskStatus{
			State: clienta2a.TaskStateFailed,
			Message: &clienta2a.Message{
				Role: "agent",
				Parts: []clienta2a.Part{{
					Kind: clienta2a.PartText,
					Text: "result builder failed: structured output missing summary",
				}},
			},
		},
	}, false)
	if result.Summary != "result builder failed: structured output missing summary" {
		t.Fatalf("unexpected fallback summary: %#v", result)
	}
	if len(result.Messages) != 1 || result.Messages[0].Text != result.Summary {
		t.Fatalf("expected status message to be preserved, got %#v", result.Messages)
	}
}

func TestResultFromTaskPrefersTerminalStatusMessageOverUserSummary(t *testing.T) {
	t.Parallel()
	result := resultFromTask(DelegationResult{DelegationID: "del-1", Agent: "research", RemoteProtocol: ProtocolA2A}, clienta2a.Task{
		ID:        "task-1",
		ContextID: "ctx-1",
		Status: clienta2a.TaskStatus{
			State: clienta2a.TaskStateCompleted,
			Message: &clienta2a.Message{
				Role:  "agent",
				Parts: []clienta2a.Part{{Kind: clienta2a.PartText, Text: "final design summary"}},
			},
		},
		Messages: []clienta2a.Message{{
			Role:  "user",
			Parts: []clienta2a.Part{{Kind: clienta2a.PartText, Text: "original request"}},
		}},
	}, false)
	if result.Summary != "final design summary" {
		t.Fatalf("expected terminal summary to win, got %#v", result)
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
	historyLength := 7

	result, err := delegator.Delegate(context.Background(), DelegationRequest{RunID: "run-1", Agent: "research", Objective: "research this", HistoryLength: &historyLength})
	if err != nil {
		t.Fatalf("delegate: %v", err)
	}
	if result.Status != "completed" || result.RemoteTaskID != "task-1" || result.Summary != "done" {
		t.Fatalf("unexpected delegation result: %#v", result)
	}
	if client.lastSend.Tenant != "tenant-a" || client.lastSend.Message.Parts[0].Text != "research this" {
		t.Fatalf("unexpected send request: %#v", client.lastSend)
	}
	if client.lastSend.ContextID != "" {
		t.Fatalf("expected remote agent to allocate context id, got %q", client.lastSend.ContextID)
	}
	if client.lastSend.HistoryLength == nil || *client.lastSend.HistoryLength != historyLength {
		t.Fatalf("expected send history length %d, got %#v", historyLength, client.lastSend.HistoryLength)
	}
	if client.lastGet.HistoryLength == nil || *client.lastGet.HistoryLength != historyLength {
		t.Fatalf("expected get history length %d, got %#v", historyLength, client.lastGet.HistoryLength)
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

func TestDelegatorUsesExplicitMessageAndContextID(t *testing.T) {
	t.Parallel()
	card := clienta2a.AgentCard{Name: "Research", Capabilities: clienta2a.Capabilities{Streaming: false}}
	registry, err := NewRegistry(RemoteAgentSpec{Key: "research", AgentCard: &card})
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	client := &fakeA2AClient{card: card, sendTask: clienta2a.Task{ID: "task-1", ContextID: "ctx-explicit", Status: clienta2a.TaskStatus{State: clienta2a.TaskStateCompleted}}}
	delegator := NewDelegator(registry, NewEventBus(16))
	delegator.NewClient = func(RemoteAgentSpec) A2AClient { return client }
	delegator.NewID = func() string { return "del-1" }
	msg := &clienta2a.Message{
		Role: "user",
		Parts: []clienta2a.Part{
			{Kind: clienta2a.PartText, Text: "team mode request"},
			{Kind: clienta2a.PartData, Data: map[string]any{"stage": "review"}},
		},
		Metadata: map[string]any{"request_id": "req-1"},
	}

	_, err = delegator.Delegate(context.Background(), DelegationRequest{
		RunID:     "run-1",
		ContextID: "ctx-explicit",
		Agent:     "research",
		Message:   msg,
	})
	if err != nil {
		t.Fatalf("delegate: %v", err)
	}
	if client.lastSend.ContextID != "ctx-explicit" {
		t.Fatalf("expected explicit context id, got %#v", client.lastSend)
	}
	if len(client.lastSend.Message.Parts) != 2 || client.lastSend.Message.Parts[1].Kind != clienta2a.PartData {
		t.Fatalf("expected explicit message parts to be preserved, got %#v", client.lastSend.Message)
	}
	if client.lastSend.Message.Metadata["request_id"] != "req-1" {
		t.Fatalf("expected explicit message metadata, got %#v", client.lastSend.Message.Metadata)
	}
}

func TestDelegatorLifecycleHookCanBlockBeforeExecution(t *testing.T) {
	t.Parallel()
	card := clienta2a.AgentCard{Name: "Research", Capabilities: clienta2a.Capabilities{Streaming: false}}
	registry, err := NewRegistry(RemoteAgentSpec{Key: "research", AgentCard: &card})
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	client := &fakeA2AClient{card: card}
	delegator := NewDelegator(registry, NewEventBus(16))
	delegator.NewClient = func(RemoteAgentSpec) A2AClient { return client }
	delegator.NewID = func() string { return "del-1" }
	delegator.LifecycleHook = DelegationLifecycleHookFuncs{
		BeforeFunc: func(ctx context.Context, req BeforeDelegation) error {
			if req.Request.StageContext.Stage != "design_plan" || req.Request.StageContext.WorkflowRunID != "wf-1" {
				t.Fatalf("unexpected stage context: %#v", req.Request.StageContext)
			}
			return &DelegationError{Code: "stage_blocked", Message: "design_plan is not allowed yet"}
		},
	}

	result, err := delegator.Delegate(context.Background(), DelegationRequest{
		RunID: "run-1",
		Agent: "research",
		StageContext: DelegationStageContext{
			WorkflowRunID: "wf-1",
			Stage:         "design_plan",
		},
	})
	if err == nil {
		t.Fatal("expected lifecycle hook to block execution")
	}
	var derr *DelegationError
	if !errors.As(err, &derr) || derr.Code != "stage_blocked" {
		t.Fatalf("expected stage_blocked error, got %T %[1]v", err)
	}
	if result.Status != "failed" || result.Error == nil || result.Error.Code != "stage_blocked" {
		t.Fatalf("unexpected blocked result: %#v", result)
	}
	if client.sendCalls != 0 || client.streamCalls != 0 {
		t.Fatalf("expected no remote execution, send=%d stream=%d", client.sendCalls, client.streamCalls)
	}
}

func TestDelegatorLifecycleHookReportsAfterExecution(t *testing.T) {
	t.Parallel()
	card := clienta2a.AgentCard{Name: "Research", Capabilities: clienta2a.Capabilities{Streaming: false}}
	registry, err := NewRegistry(RemoteAgentSpec{Key: "research", AgentCard: &card})
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	client := &fakeA2AClient{card: card, sendTask: clienta2a.Task{
		ID:        "task-1",
		ContextID: "ctx-1",
		Status:    clienta2a.TaskStatus{State: clienta2a.TaskStateCompleted},
		Artifacts: []clienta2a.Artifact{{ID: "artifact-1", Name: "artifact-1", Parts: []clienta2a.Part{{Kind: clienta2a.PartData, Data: map[string]any{"state": "passed"}}}}},
	}}
	delegator := NewDelegator(registry, NewEventBus(16))
	delegator.NewClient = func(RemoteAgentSpec) A2AClient { return client }
	delegator.NewID = func() string { return "del-1" }
	var afterReq AfterDelegation
	delegator.LifecycleHook = DelegationLifecycleHookFuncs{
		AfterFunc: func(ctx context.Context, req AfterDelegation) error {
			afterReq = req
			return nil
		},
	}

	result, err := delegator.Delegate(context.Background(), DelegationRequest{
		RunID:                  "run-1",
		Agent:                  "research",
		Objective:              "review this",
		IncludeRemoteArtifacts: true,
		StageContext: DelegationStageContext{
			WorkflowRunID: "wf-1",
			Stage:         "review_code",
			StepID:        "step-2",
			Attempt:       1,
		},
	})
	if err != nil {
		t.Fatalf("delegate: %v", err)
	}
	if result.Status != "completed" {
		t.Fatalf("unexpected result: %#v", result)
	}
	if afterReq.DelegationID != "del-1" || afterReq.Request.StageContext.Stage != "review_code" {
		t.Fatalf("unexpected after hook request: %#v", afterReq)
	}
	if afterReq.Result.Status != "completed" || len(afterReq.Result.RemoteArtifacts) != 1 {
		t.Fatalf("unexpected after hook result: %#v", afterReq.Result)
	}
}

func TestDelegatorAfterHookFailurePublishesOnlyFailedTerminal(t *testing.T) {
	t.Parallel()
	card := clienta2a.AgentCard{Name: "Research", Capabilities: clienta2a.Capabilities{Streaming: false}}
	registry, err := NewRegistry(RemoteAgentSpec{Key: "research", AgentCard: &card})
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	bus := NewEventBus(16)
	client := &fakeA2AClient{card: card, sendTask: clienta2a.Task{ID: "task-1", ContextID: "ctx-1", Status: clienta2a.TaskStatus{State: clienta2a.TaskStateCompleted}}}
	delegator := NewDelegator(registry, bus)
	delegator.NewClient = func(RemoteAgentSpec) A2AClient { return client }
	delegator.NewID = func() string { return "del-1" }
	delegator.LifecycleHook = DelegationLifecycleHookFuncs{
		AfterFunc: func(context.Context, AfterDelegation) error {
			return errors.New("workflow report failed")
		},
	}

	result, err := delegator.Delegate(context.Background(), DelegationRequest{RunID: "run-1", Agent: "research", Objective: "review this"})
	var derr *DelegationError
	if !errors.As(err, &derr) || derr.Code != "workflow_after_failed" {
		t.Fatalf("result=%#v err=%v, want workflow_after_failed", result, err)
	}
	if result.Status != "failed" || result.Error == nil || result.Error.Code != "workflow_after_failed" {
		t.Fatalf("result = %#v", result)
	}
	events := drainAvailableBus(t, bus, "run-1")
	terminalCount := 0
	for _, event := range events {
		if !isTerminal(event.Kind) {
			continue
		}
		terminalCount++
		if event.Kind != DelegationFailed || event.Error == nil || event.Error.Code != "workflow_after_failed" {
			t.Fatalf("terminal = %#v", event)
		}
	}
	if terminalCount != 1 {
		t.Fatalf("terminal count = %d, events=%#v", terminalCount, events)
	}
}

func TestDelegatorTimeoutCoversBeforeHook(t *testing.T) {
	t.Parallel()
	card := clienta2a.AgentCard{Name: "Research", Capabilities: clienta2a.Capabilities{Streaming: false}}
	registry, err := NewRegistry(RemoteAgentSpec{Key: "research", AgentCard: &card})
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	delegator := NewDelegator(registry, NewEventBus(16))
	delegator.NewID = func() string { return "del-1" }
	delegator.LifecycleHook = DelegationLifecycleHookFuncs{
		BeforeFunc: func(ctx context.Context, _ BeforeDelegation) error {
			<-ctx.Done()
			return ctx.Err()
		},
	}

	result, err := delegator.Delegate(context.Background(), DelegationRequest{RunID: "run-1", Agent: "research", Objective: "review this", Timeout: 10 * time.Millisecond})
	var derr *DelegationError
	if !errors.As(err, &derr) || derr.Code != "workflow_before_failed" || !strings.Contains(derr.Message, context.DeadlineExceeded.Error()) {
		t.Fatalf("result=%#v err=%v, want workflow_before_failed", result, err)
	}
	if result.Status != "failed" {
		t.Fatalf("result = %#v", result)
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

func TestDelegatorStaticAgentCardRequiresCustomClient(t *testing.T) {
	t.Parallel()
	card := clienta2a.AgentCard{Name: "Research", Capabilities: clienta2a.Capabilities{Streaming: false}}
	registry, err := NewRegistry(RemoteAgentSpec{Key: "research", AgentCard: &card})
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	delegator := NewDelegator(registry, NewEventBus(16))
	delegator.NewID = func() string { return "del-1" }

	result, err := delegator.Delegate(context.Background(), DelegationRequest{RunID: "run-1", Agent: "research", Objective: "research this"})
	if err == nil {
		t.Fatal("expected static AgentCard-only spec to require custom client")
	}
	var derr *DelegationError
	if !errors.As(err, &derr) || derr.Code != "configuration_error" {
		t.Fatalf("expected configuration_error, got result=%#v err=%T %[2]v", result, err)
	}
	if result.Status != "failed" {
		t.Fatalf("expected failed result, got %#v", result)
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
	if client.lastSend.ContextID != "" {
		t.Fatalf("expected remote agent to allocate streaming context id, got %q", client.lastSend.ContextID)
	}
	replayed := drainBus(t, bus, "run-1", 3)
	if replayed[len(replayed)-1].Kind != DelegationCancelled {
		t.Fatalf("expected cancelled terminal, got %#v", replayed)
	}
}

func TestDelegatorStreamingCancellationUsesBoundedRemoteCancelContext(t *testing.T) {
	t.Parallel()
	card := clienta2a.AgentCard{Name: "Research", Capabilities: clienta2a.Capabilities{Streaming: true}}
	registry, err := NewRegistry(RemoteAgentSpec{Key: "research", AgentCard: &card})
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	stream := &fakeA2AStream{events: make(chan streamRecv, 1), closed: make(chan struct{})}
	stream.events <- streamRecv{event: clienta2a.Event{Kind: clienta2a.EventStatus, TaskID: "task-1", ContextID: "ctx-1", Status: &clienta2a.TaskStatus{State: clienta2a.TaskStateWorking}}}
	client := &fakeA2AClient{card: card, stream: stream}
	delegator := NewDelegator(registry, NewEventBus(16))
	delegator.NewClient = func(RemoteAgentSpec) A2AClient { return client }
	delegator.NewID = func() string { return "del-1" }
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	_, err = delegator.Delegate(ctx, DelegationRequest{RunID: "run-1", Agent: "research", Objective: "research this", Stream: true})
	if err == nil {
		t.Fatal("expected streaming cancellation error")
	}
	if !client.cancelHadDeadline {
		t.Fatalf("expected remote cancel to use bounded context, got calls=%d", client.cancelCalls)
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
	replayed := drainBus(t, bus, "run-1", 5)
	if replayed[len(replayed)-2].Kind != DelegationTextEnd || replayed[len(replayed)-1].Kind != DelegationFinished {
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

func TestEventBusTerminalDeliveryDropsOldestWhenSubscriberFull(t *testing.T) {
	t.Parallel()
	bus := NewEventBus(0)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := bus.SubscribeRun(ctx, "run-1")
	for i := 0; i < subscriberBuffer; i++ {
		bus.Publish(DelegationEvent{RunID: "run-1", DelegationID: "del-1", Kind: DelegationStatus, Status: "working"})
	}
	if !bus.Publish(DelegationEvent{RunID: "run-1", DelegationID: "del-1", Kind: DelegationFinished, Status: "completed"}) {
		t.Fatal("terminal publish should be accepted")
	}
	seenTerminal := false
	for i := 0; i < subscriberBuffer; i++ {
		select {
		case ev := <-ch:
			if ev.Kind == DelegationFinished {
				seenTerminal = true
			}
		case <-time.After(time.Second):
			t.Fatalf("timeout draining subscriber %d/%d", i, subscriberBuffer)
		}
	}
	if !seenTerminal {
		t.Fatal("terminal event should be enqueued by dropping an older event")
	}
}

func TestEventBusReportsBackpressureWhenSubscriberFull(t *testing.T) {
	t.Parallel()
	bus := NewEventBus(0)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := bus.SubscribeRun(ctx, "run-1")
	for i := 0; i < subscriberBuffer; i++ {
		bus.Publish(DelegationEvent{RunID: "run-1", DelegationID: "del-1", Kind: DelegationTextDelta, Sequence: uint64(i + 1)})
	}
	bus.Publish(DelegationEvent{RunID: "run-1", DelegationID: "del-1", Kind: DelegationTextDelta, Sequence: subscriberBuffer + 1})

	var dropped *DelegationEvent
	for i := 0; i < subscriberBuffer; i++ {
		event := <-ch
		if event.Kind == DelegationStreamDropped {
			dropped = &event
		}
	}
	if dropped == nil || dropped.Raw["reason"] != "event_bus_backpressure" || dropped.Raw["dropped_count"] != 2 {
		t.Fatalf("backpressure event = %#v", dropped)
	}
}

func TestEventBusPreservesTextEndBeforeTerminalForFullSubscriber(t *testing.T) {
	t.Parallel()
	bus := NewEventBus(0)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := bus.SubscribeRun(ctx, "run-1")
	for i := 0; i < subscriberBuffer; i++ {
		bus.Publish(DelegationEvent{RunID: "run-1", DelegationID: "del-1", Kind: DelegationStatus})
	}
	bus.Publish(DelegationEvent{RunID: "run-1", DelegationID: "del-1", Kind: DelegationTextEnd, RemoteMessageID: "msg-1"})
	bus.Publish(DelegationEvent{RunID: "run-1", DelegationID: "del-1", Kind: DelegationFinished})

	var lifecycle []DelegationEventKind
	for i := 0; i < subscriberBuffer; i++ {
		event := <-ch
		if event.Kind == DelegationTextEnd || event.Kind == DelegationFinished {
			lifecycle = append(lifecycle, event.Kind)
		}
	}
	if len(lifecycle) != 2 || lifecycle[0] != DelegationTextEnd || lifecycle[1] != DelegationFinished {
		t.Fatalf("lifecycle = %#v", lifecycle)
	}
}

func TestDelegatorPublishesAgentNotFoundTerminal(t *testing.T) {
	t.Parallel()
	registry, err := NewRegistry()
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	bus := NewEventBus(4)
	delegator := NewDelegator(registry, bus)
	delegator.NewID = func() string { return "del-missing" }

	result, err := delegator.Delegate(context.Background(), DelegationRequest{RunID: "run-1", Agent: "missing"})
	var derr *DelegationError
	if !errors.As(err, &derr) || derr.Code != "agent_not_found" || result.Status != "failed" {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	events := drainBus(t, bus, "run-1", 1)
	if events[0].Kind != DelegationFailed || events[0].DelegationID != "del-missing" {
		t.Fatalf("events = %#v", events)
	}
}

func TestDelegatorAfterCancelledErrorKeepsResultAndEventAligned(t *testing.T) {
	t.Parallel()
	card := clienta2a.AgentCard{Name: "Research", Capabilities: clienta2a.Capabilities{Streaming: false}}
	registry, err := NewRegistry(RemoteAgentSpec{Key: "research", AgentCard: &card})
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	bus := NewEventBus(8)
	client := &fakeA2AClient{card: card, sendTask: clienta2a.Task{ID: "task-1", Status: clienta2a.TaskStatus{State: clienta2a.TaskStateCompleted}}}
	delegator := NewDelegator(registry, bus)
	delegator.NewClient = func(RemoteAgentSpec) A2AClient { return client }
	delegator.NewID = func() string { return "del-1" }
	delegator.LifecycleHook = DelegationLifecycleHookFuncs{AfterFunc: func(context.Context, AfterDelegation) error {
		return &DelegationError{Code: "cancelled", Message: "workflow cancelled"}
	}}

	result, err := delegator.Delegate(context.Background(), DelegationRequest{RunID: "run-1", Agent: "research", Objective: "review"})
	if err == nil || result.Status != "cancelled" {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	events := drainAvailableBus(t, bus, "run-1")
	if events[len(events)-1].Kind != DelegationCancelled {
		t.Fatalf("events = %#v", events)
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

func TestDelegatorDefaultIDsAreUnique(t *testing.T) {
	t.Parallel()
	delegator := NewDelegator(nil, nil)
	seen := map[string]struct{}{}
	for i := 0; i < 256; i++ {
		id := delegator.newID()
		if id == "" {
			t.Fatal("newID returned empty string")
		}
		if _, ok := seen[id]; ok {
			t.Fatalf("duplicate delegation id %q", id)
		}
		seen[id] = struct{}{}
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
	rpcBody := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"delegate_to_agent","arguments":{"agent":"research","objective":"research this","constraints":{"stream":false,"history_length":3}}}}`
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
	if client.lastSend.HistoryLength == nil || *client.lastSend.HistoryLength != 3 {
		t.Fatalf("expected history_length to flow into send request, got %#v", client.lastSend.HistoryLength)
	}
}

func TestMCPServerCustomToolBuildsTypedResult(t *testing.T) {
	t.Parallel()
	card := clienta2a.AgentCard{Name: "Research", Capabilities: clienta2a.Capabilities{Streaming: false}}
	registry, err := NewRegistry(RemoteAgentSpec{Key: "research", AgentCard: &card, Policy: DelegationPolicy{PollInterval: time.Millisecond, MaxPolls: 2}})
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	client := &fakeA2AClient{
		card: card,
		sendTask: clienta2a.Task{
			ID:        "task-1",
			ContextID: "ctx-1",
			Status:    clienta2a.TaskStatus{State: clienta2a.TaskStateCompleted},
			Artifacts: []clienta2a.Artifact{{
				ID:   "review-code-artifact",
				Name: "review-code-artifact",
				Parts: []clienta2a.Part{
					{Kind: clienta2a.PartText, Text: "looks good"},
					{Kind: clienta2a.PartData, Data: map[string]any{"state": "passed", "description": "looks good"}},
				},
			}},
		},
	}
	delegator := NewDelegator(registry, NewEventBus(16))
	delegator.NewClient = func(RemoteAgentSpec) A2AClient { return client }
	delegator.NewID = func() string { return "del-1" }

	tool := ToolSpec{
		Name:        "review_code_stage",
		Description: "Run review code stage",
		InputSchema: map[string]any{"type": "object"},
		BuildRequest: func(ctx context.Context, raw json.RawMessage, env ToolContext) (DelegationRequest, error) {
			var in struct {
				Plan string `json:"plan"`
			}
			if err := json.Unmarshal(raw, &in); err != nil {
				return DelegationRequest{}, err
			}
			return DelegationRequest{
				RunID:                  env.RunID,
				ParentToolCallID:       env.ParentToolCallID,
				Agent:                  "research",
				IncludeRemoteArtifacts: true,
				Message: &clienta2a.Message{
					Role: "user",
					Parts: []clienta2a.Part{
						{Kind: clienta2a.PartText, Text: "请审查代码"},
						{Kind: clienta2a.PartData, Data: map[string]any{"plan": in.Plan}},
					},
				},
			}, nil
		},
		BuildResult: func(ctx context.Context, out DelegationResult) (any, error) {
			part := out.RemoteArtifacts[0].Parts[1]
			return part.Data, nil
		},
	}

	server := httptest.NewServer(NewMCPServer(delegator, MCPServerOptions{
		RunID:              "run-1",
		BearerToken:        "token",
		Tools:              []ToolSpec{tool},
		DisableDefaultTool: true,
	}).Handler())
	defer server.Close()
	rpcBody := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"review_code_stage","arguments":{"plan":"check stage path"}}}`
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
	var result map[string]any
	if err := json.Unmarshal([]byte(envelope.Result.Content[0].Text), &result); err != nil {
		t.Fatalf("decode typed result: %v", err)
	}
	if result["state"] != "passed" || result["description"] != "looks good" {
		t.Fatalf("unexpected typed tool result: %#v", result)
	}
	if len(client.lastSend.Message.Parts) != 2 || client.lastSend.Message.Parts[1].Kind != clienta2a.PartData {
		t.Fatalf("expected custom tool to send text+data parts, got %#v", client.lastSend.Message)
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

func TestMCPServerRejectsEmptyBearerTokenByDefault(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(NewMCPServer(NewDelegator(nil, nil), MCPServerOptions{RunID: "run-1"}).Handler())
	defer server.Close()
	resp, err := http.Post(server.URL, "application/json", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	if err != nil {
		t.Fatalf("post rpc: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected missing server bearer token to fail closed, got %d", resp.StatusCode)
	}
}

func TestMCPServerAllowsUnauthenticatedLoopbackWhenExplicit(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(NewMCPServer(NewDelegator(nil, nil), MCPServerOptions{RunID: "run-1", AllowUnauthenticatedLoopbackForTest: true}).Handler())
	defer server.Close()
	resp, err := http.Post(server.URL, "application/json", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	if err != nil {
		t.Fatalf("post rpc: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected explicit loopback test mode to allow request, got %d", resp.StatusCode)
	}
}

func TestMCPServerToolCallRequiresRunContext(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(NewMCPServer(NewDelegator(nil, nil), MCPServerOptions{BearerToken: "token"}).Handler())
	defer server.Close()
	result, isError := callDelegateTool(t, server.URL, "token", `{"agent":"research","objective":"research this"}`)
	if !isError {
		t.Fatalf("expected missing run context to be an MCP tool error: %#v", result)
	}
	if result.Error == nil || result.Error.Code != "configuration_error" || !strings.Contains(result.Error.Message, "run context") {
		t.Fatalf("expected run-context configuration_error, got %#v", result)
	}
}

func TestMCPServerPreservesDelegatorErrorDetails(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(NewMCPServer(NewDelegator(nil, nil), MCPServerOptions{RunID: "run-1", BearerToken: "token"}).Handler())
	defer server.Close()
	result, isError := callDelegateTool(t, server.URL, "token", `{"agent":"research","objective":"research this"}`)
	if !isError {
		t.Fatalf("expected delegator failure to be an MCP tool error: %#v", result)
	}
	if result.Status != "failed" || result.Error == nil || result.Error.Code != "configuration_error" {
		t.Fatalf("expected structured configuration_error to be preserved, got %#v", result)
	}
}

func callDelegateTool(t *testing.T, url, token, arguments string) (DelegationResult, bool) {
	t.Helper()
	rpcBody := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"delegate_to_agent","arguments":` + arguments + `}}`
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(rpcBody))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
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
	if len(envelope.Result.Content) != 1 {
		t.Fatalf("expected one content item, got %#v", envelope.Result)
	}
	var result DelegationResult
	if err := json.Unmarshal([]byte(envelope.Result.Content[0].Text), &result); err != nil {
		t.Fatalf("decode delegation result: %v", err)
	}
	return result, envelope.Result.IsError
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

	cancelCalls       int
	lastCancel        clienta2a.CancelTaskRequest
	cancelHadDeadline bool
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

func (f *fakeA2AClient) SendStream(_ context.Context, req clienta2a.SendRequest) (A2AStream, error) {
	f.streamCalls++
	f.lastSend = req
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

func (f *fakeA2AClient) CancelTask(ctx context.Context, req clienta2a.CancelTaskRequest) (clienta2a.Task, error) {
	f.cancelCalls++
	f.lastCancel = req
	_, f.cancelHadDeadline = ctx.Deadline()
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

func (s *fakeA2AStream) RecvContext(ctx context.Context) (clienta2a.Event, error) {
	select {
	case <-ctx.Done():
		return clienta2a.Event{}, ctx.Err()
	case item, ok := <-s.events:
		if !ok {
			return clienta2a.Event{}, io.EOF
		}
		return item.event, item.err
	}
}

func (s *fakeA2AStream) Close() error {
	select {
	case <-s.closed:
	default:
		close(s.closed)
	}
	return nil
}
