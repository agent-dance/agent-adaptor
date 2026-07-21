package a2adelegation

import (
	"testing"
	"time"

	agentadaptor "github.com/agent-dance/agent-adaptor"
	bridgea2a "github.com/agent-dance/agent-adaptor/pkg/bridges/a2a"
	clienta2a "github.com/agent-dance/agent-adaptor/pkg/clients/a2a"
)

// TestEventMapperArtifactsNeverCreateProcessEvents verifies ArtifactUpdate is
// reserved for final artifacts even when an artifact uses a historical process name.
func TestEventMapperArtifactsNeverCreateProcessEvents(t *testing.T) {
	for _, name := range []string{"assistant-output", "tool-call-bash-1", "reasoning", "stream-dropped"} {
		mapper := newEventMapper(DelegationEvent{RunID: "run-1", DelegationID: "del-1", AgentKey: "implement"})
		events := mapper.Map(clienta2a.Event{
			Kind:      clienta2a.EventArtifact,
			TaskID:    "task-1",
			ContextID: "ctx-1",
			Artifact: &clienta2a.Artifact{
				ID:    name,
				Name:  name,
				Parts: []clienta2a.Part{{Kind: clienta2a.PartText, Text: "final result"}},
			},
			LastChunk: true,
		})
		if len(events) != 2 || events[0].Kind != DelegationStarted || events[1].Kind != DelegationArtifactCreated {
			t.Fatalf("artifact %q events = %#v", name, events)
		}
	}
}

// TestEventMapperStatusPreservesDataParts verifies that structured status data
// survives the A2A-to-delegation mapping for host-owned projections.
func TestEventMapperStatusPreservesDataParts(t *testing.T) {
	mapper := newEventMapper(DelegationEvent{RunID: "run-1", DelegationID: "del-1", AgentKey: "implement"})
	events := mapper.Map(clienta2a.Event{
		Kind:      clienta2a.EventStatus,
		TaskID:    "task-1",
		ContextID: "ctx-1",
		Status: &clienta2a.TaskStatus{
			State: clienta2a.TaskStateWorking,
			Message: &clienta2a.Message{ID: "status-1", Parts: []clienta2a.Part{
				{Kind: clienta2a.PartText, Text: "calling Bash"},
				{Kind: clienta2a.PartData, Data: map[string]any{"type": "STATUS_TYPE_TOOL_CALL", "toolCall": map[string]any{"id": "bash-1"}}},
			}},
		},
	})
	if len(events) != 5 || events[1].Kind != DelegationStatus || events[2].Kind != DelegationTextStart ||
		events[3].Kind != DelegationTextDelta || events[4].Kind != DelegationTextEnd {
		t.Fatalf("status events = %#v", events)
	}
	if events[1].RemoteMessageID != "status-1" || events[1].Text != "calling Bash" || len(events[1].StatusParts) != 2 {
		t.Fatalf("status payload = %#v", events[1])
	}
	data, ok := events[1].StatusParts[1].Data.(map[string]any)
	if !ok || data["type"] != "STATUS_TYPE_TOOL_CALL" {
		t.Fatalf("status data = %#v", events[1].StatusParts)
	}
}

// TestEventMapperTerminalStatusPreservesDataParts verifies terminal status
// events keep their structured data before the terminal lifecycle event.
func TestEventMapperTerminalStatusPreservesDataParts(t *testing.T) {
	mapper := newEventMapper(DelegationEvent{RunID: "run-1", DelegationID: "del-1", AgentKey: "implement"})
	events := mapper.Map(clienta2a.Event{
		Kind:      clienta2a.EventTerminal,
		TaskID:    "task-1",
		ContextID: "ctx-1",
		Status: &clienta2a.TaskStatus{
			State: clienta2a.TaskStateCompleted,
			Message: &clienta2a.Message{Parts: []clienta2a.Part{
				{Kind: clienta2a.PartData, Data: map[string]any{"type": "STATUS_TYPE_TOOL_RESPONSE", "toolResponse": map[string]any{"id": "bash-1"}}},
			}},
		},
	})
	if len(events) != 2 || events[1].Kind != DelegationStatus {
		t.Fatalf("terminal status events = %#v", events)
	}
	if len(events[1].StatusParts) != 1 {
		t.Fatalf("terminal status parts = %#v", events[1].StatusParts)
	}
}

// TestEventMapperClosesTextBeforeTerminal verifies terminal events cannot leave an open message lifecycle.
func TestEventMapperClosesTextBeforeTerminal(t *testing.T) {
	mapper := newEventMapper(DelegationEvent{RunID: "run-1", DelegationID: "del-1", AgentKey: "research"})
	message := clienta2a.Message{ID: "msg-1", TaskID: "task-1", ContextID: "ctx-1", Role: "agent", Parts: []clienta2a.Part{{Kind: clienta2a.PartText, Text: "done"}}}
	events := mapper.Map(clienta2a.Event{Kind: clienta2a.EventTerminal, TaskID: "task-1", ContextID: "ctx-1", Message: &message})
	events = append(events, mapper.terminalEventsForState("task-1", "ctx-1", clienta2a.TaskStateCompleted, nil)...)
	if len(events) != 5 {
		t.Fatalf("events = %#v", events)
	}
	if events[len(events)-2].Kind != DelegationTextEnd || events[len(events)-2].RemoteMessageID != "msg-1" || events[len(events)-1].Kind != DelegationFinished {
		t.Fatalf("terminal lifecycle = %#v", events)
	}
}

func TestAdapterStreamStatusEndIsPriorityEvent(t *testing.T) {
	mapper := newEventMapper(DelegationEvent{RunID: "run-1", DelegationID: "del-1", AgentKey: "implement"})
	events := mapper.Map(clienta2a.Event{
		Kind: clienta2a.EventStatus,
		Status: &clienta2a.TaskStatus{State: clienta2a.TaskStateWorking, Message: &clienta2a.Message{Parts: []clienta2a.Part{{
			Kind: clienta2a.PartData,
			Data: bridgea2a.AdapterStreamEnvelopeV1{
				Schema: bridgea2a.AdapterStreamSchemaV1,
				Event:  bridgea2a.AdapterStreamEventV1{Kind: string(agentadaptor.StreamTextEnd), MessageID: "msg-1"},
			},
		}}}},
	})
	if len(events) != 3 || events[2].Kind != DelegationTextEnd || !isPriorityEvent(events[2]) {
		t.Fatal("adapter stream text.end should be priority")
	}
}

// TestEventMapperAdapterStreamProfile verifies typed decoding, replay deduplication,
// sequence gap reporting, and profile locking for the built-in stream schema.
func TestEventMapperAdapterStreamProfile(t *testing.T) {
	mapper := newEventMapper(DelegationEvent{RunID: "run-1", DelegationID: "del-1", AgentKey: "implement"})
	eventTime := time.Date(2026, time.July, 21, 10, 30, 0, 0, time.UTC)
	firstStatus := adapterStreamStatusEvent(bridgea2a.AdapterStreamEventV1{
		Kind:      string(agentadaptor.StreamTextStart),
		Sequence:  1,
		RunID:     "member-run",
		ThreadID:  "member-thread",
		TurnID:    "member-turn",
		MessageID: "msg-1",
		Role:      string(agentadaptor.RoleUser),
		Timestamp: eventTime.Format(time.RFC3339Nano),
	})
	first := mapper.Map(firstStatus)
	if len(first) != 3 || first[0].Kind != DelegationStarted || first[1].Kind != DelegationStatus || first[2].Kind != DelegationTextStart {
		t.Fatalf("first events = %#v", first)
	}
	if first[2].Sequence != 1 || first[2].RemoteMessageID != "msg-1" || first[2].Role != string(agentadaptor.RoleUser) ||
		!first[2].Time.Equal(eventTime) || first[2].Raw["stream_profile"] != bridgea2a.AdapterStreamSchemaV1 ||
		first[2].Raw["member_run_id"] != "member-run" || first[2].Raw["member_thread_id"] != "member-thread" ||
		first[2].Raw["member_turn_id"] != "member-turn" {
		t.Fatalf("first decoded event = %#v", first[2])
	}

	replay := mapper.Map(firstStatus)
	if len(replay) != 1 || replay[0].Kind != DelegationStatus {
		t.Fatalf("replay events = %#v", replay)
	}

	third := mapper.Map(adapterStreamStatusEvent(bridgea2a.AdapterStreamEventV1{
		Kind:       string(agentadaptor.StreamToolCallArgs),
		Sequence:   3,
		ToolCallID: "bash-1",
		Delta:      `{"command":"go test ./..."}`,
	}))
	if len(third) != 3 || third[0].Kind != DelegationStatus || third[1].Kind != DelegationStreamDropped || third[2].Kind != DelegationToolCallArgs {
		t.Fatalf("third events = %#v", third)
	}
	if third[1].Raw["dropped_count"] != uint64(1) || third[1].Raw["first_missing"] != uint64(2) || third[1].Raw["last_missing"] != uint64(2) {
		t.Fatalf("gap event = %#v", third[1])
	}
	if third[2].Sequence != 3 || third[2].RemoteToolCallID != "bash-1" || third[2].Delta != `{"command":"go test ./..."}` {
		t.Fatalf("tool args event = %#v", third[2])
	}

	artifact := mapper.Map(clienta2a.Event{
		Kind: clienta2a.EventArtifact,
		Artifact: &clienta2a.Artifact{
			ID:    "assistant-output",
			Name:  "assistant-output",
			Parts: []clienta2a.Part{{Kind: clienta2a.PartText, Text: "duplicate"}},
		},
	})
	if len(artifact) != 1 || artifact[0].Kind != DelegationArtifactCreated {
		t.Fatalf("artifact events = %#v", artifact)
	}
}

type testStatusPartDecoder struct{}

func (testStatusPartDecoder) Profile() string {
	return "test.status.v1"
}

func (testStatusPartDecoder) DecodeStatusPart(data any) ([]StatusPartEvent, bool, error) {
	value, ok := data.(map[string]any)
	if !ok || value["schema"] != "test.status.v1" {
		return nil, false, nil
	}
	return []StatusPartEvent{{
		Kind:   DelegationCustom,
		Name:   "todo.update",
		Result: value["result"],
		Raw:    map[string]any{"source": "test"},
	}}, true, nil
}

// TestEventMapperInjectedStatusDecoder verifies host-owned schemas are normalized
// without adding private protocol dependencies to the adapter package.
func TestEventMapperInjectedStatusDecoder(t *testing.T) {
	mapper := newEventMapper(
		DelegationEvent{RunID: "run-1", DelegationID: "del-1", AgentKey: "external"},
		testStatusPartDecoder{},
	)
	events := mapper.Map(clienta2a.Event{
		Kind:      clienta2a.EventStatus,
		TaskID:    "task-1",
		ContextID: "ctx-1",
		Status: &clienta2a.TaskStatus{
			State: clienta2a.TaskStateWorking,
			Message: &clienta2a.Message{ID: "status-1", Parts: []clienta2a.Part{
				{Kind: clienta2a.PartText, Text: "working"},
				{Kind: clienta2a.PartData, Data: map[string]any{
					"schema": "test.status.v1",
					"result": map[string]any{"items": []any{"test"}},
				}},
			}},
		},
	})
	if len(events) != 6 || events[0].Kind != DelegationStarted || events[1].Kind != DelegationStatus ||
		events[2].Kind != DelegationCustom || events[3].Kind != DelegationTextStart ||
		events[4].Kind != DelegationTextDelta || events[5].Kind != DelegationTextEnd {
		t.Fatalf("events = %#v", events)
	}
	if events[2].Name != "todo.update" || events[2].Raw["source"] != "test" || events[2].Raw["stream_profile"] != "test.status.v1" {
		t.Fatalf("custom event = %#v", events[2])
	}
	if events[4].Delta != "working" || events[4].RemoteMessageID != "status-1" {
		t.Fatalf("text event = %#v", events[4])
	}

	mixed := mapper.Map(adapterStreamStatusEvent(bridgea2a.AdapterStreamEventV1{
		Kind:      string(agentadaptor.StreamTextContent),
		Sequence:  1,
		MessageID: "msg-1",
		Delta:     "duplicate",
	}))
	if len(mixed) != 1 || mixed[0].Kind != DelegationStatus {
		t.Fatalf("mixed profile events = %#v", mixed)
	}
}

func adapterStreamStatusEvent(event bridgea2a.AdapterStreamEventV1) clienta2a.Event {
	return clienta2a.Event{
		Kind:      clienta2a.EventStatus,
		TaskID:    "task-1",
		ContextID: "ctx-1",
		Status: &clienta2a.TaskStatus{
			State: clienta2a.TaskStateWorking,
			Message: &clienta2a.Message{ID: "status-1", Parts: []clienta2a.Part{{
				Kind: clienta2a.PartData,
				Data: bridgea2a.AdapterStreamEnvelopeV1{Schema: bridgea2a.AdapterStreamSchemaV1, Event: event},
			}}},
		},
	}
}
