package a2adelegation

import (
	"testing"

	bridgea2a "github.com/agent-dance/agent-adaptor/pkg/bridges/a2a"
	clienta2a "github.com/agent-dance/agent-adaptor/pkg/clients/a2a"
)

// TestEventMapperAssistantOutputChunksFollowTextLifecycle verifies that A2A artifact chunks become an AG-UI-compatible text lifecycle.
func TestEventMapperAssistantOutputChunksFollowTextLifecycle(t *testing.T) {
	mapper := newEventMapper(DelegationEvent{RunID: "run-1", DelegationID: "del-1", AgentKey: "research"})
	first := mapper.Map(clienta2a.Event{
		Kind:      clienta2a.EventArtifact,
		TaskID:    "task-1",
		ContextID: "ctx-1",
		Artifact: &clienta2a.Artifact{
			ID: "assistant-output", Name: bridgea2a.ArtifactAssistantOutput,
			Parts: []clienta2a.Part{{Kind: clienta2a.PartText, Text: "hello"}},
		},
	})
	if len(first) != 3 {
		t.Fatalf("first chunk events = %#v", first)
	}
	if first[0].Kind != DelegationStarted || first[1].Kind != DelegationTextStart || first[2].Kind != DelegationTextDelta {
		t.Fatalf("first chunk lifecycle = %#v", first)
	}
	if first[1].RemoteMessageID != "assistant-output" || first[2].RemoteMessageID != first[1].RemoteMessageID {
		t.Fatalf("first chunk message IDs = %#v", first)
	}

	last := mapper.Map(clienta2a.Event{
		Kind:      clienta2a.EventArtifact,
		TaskID:    "task-1",
		ContextID: "ctx-1",
		Artifact: &clienta2a.Artifact{
			ID: "assistant-output", Name: bridgea2a.ArtifactAssistantOutput,
			Parts: []clienta2a.Part{{Kind: clienta2a.PartText, Text: " world"}},
		},
		Append: true, LastChunk: true,
	})
	if len(last) != 2 || last[0].Kind != DelegationTextDelta || last[1].Kind != DelegationTextEnd {
		t.Fatalf("last chunk lifecycle = %#v", last)
	}
	if last[0].RemoteMessageID != "assistant-output" || last[1].RemoteMessageID != "assistant-output" {
		t.Fatalf("last chunk message IDs = %#v", last)
	}
}

// TestEventMapperToolArtifactsFollowToolLifecycle verifies bridge tool artifacts are restored to typed host events.
func TestEventMapperToolArtifactsFollowToolLifecycle(t *testing.T) {
	mapper := newEventMapper(DelegationEvent{RunID: "run-1", DelegationID: "del-1", AgentKey: "implement"})
	start := mapper.Map(clienta2a.Event{
		Kind:      clienta2a.EventArtifact,
		TaskID:    "task-1",
		ContextID: "ctx-1",
		Artifact: &clienta2a.Artifact{
			ID:   "tool-call-bash-1",
			Name: "tool-call-bash-1",
			Parts: []clienta2a.Part{{
				Kind: clienta2a.PartData,
				Data: map[string]any{"kind": "tool_call.start", "id": "bash-1", "name": "Bash", "args": map[string]any{"command": "go test ./..."}},
			}},
		},
	})
	if len(start) != 3 || start[1].Kind != DelegationArtifactCreated || start[2].Kind != DelegationToolCallStart || start[2].RemoteToolCallID != "bash-1" || start[2].ToolName != "Bash" {
		t.Fatalf("start events = %#v", start)
	}

	args := mapper.Map(clienta2a.Event{
		Kind: clienta2a.EventArtifact, TaskID: "task-1", ContextID: "ctx-1",
		Artifact: &clienta2a.Artifact{ID: "tool-call-bash-1", Name: "tool-call-bash-1", Parts: []clienta2a.Part{{Kind: clienta2a.PartText, Text: " --race"}}},
	})
	if len(args) != 2 || args[0].Kind != DelegationArtifactCreated || args[1].Kind != DelegationToolCallArgs || args[1].Delta != " --race" || args[1].RemoteToolCallID != "bash-1" {
		t.Fatalf("args events = %#v", args)
	}

	end := mapper.Map(clienta2a.Event{
		Kind: clienta2a.EventArtifact, TaskID: "task-1", ContextID: "ctx-1", LastChunk: true,
		Artifact: &clienta2a.Artifact{
			ID:   "tool-call-bash-1-end",
			Name: "tool-call-bash-1-end",
			Parts: []clienta2a.Part{{
				Kind: clienta2a.PartData,
				Data: map[string]any{"kind": "tool_call.end", "id": "bash-1"},
			}},
		},
	})
	if len(end) != 2 || end[0].Kind != DelegationArtifactCreated || end[1].Kind != DelegationToolCallEnd || end[1].RemoteToolCallID != "bash-1" {
		t.Fatalf("end events = %#v", end)
	}

	result := mapper.Map(clienta2a.Event{
		Kind:      clienta2a.EventArtifact,
		TaskID:    "task-1",
		ContextID: "ctx-1",
		Artifact: &clienta2a.Artifact{
			ID:   "tool-call-bash-1-result",
			Name: "tool-call-bash-1-result",
			Parts: []clienta2a.Part{{
				Kind: clienta2a.PartData,
				Data: map[string]any{"kind": "tool_call.result", "id": "bash-1", "result": "ok"},
			}},
		},
	})
	if len(result) != 2 || result[0].Kind != DelegationArtifactCreated || result[1].Kind != DelegationToolCallResult || result[1].RemoteToolCallID != "bash-1" || result[1].Result != "ok" {
		t.Fatalf("result events = %#v", result)
	}
}

// TestEventMapperFinalToolArgsEmitsArgsBeforeEnd verifies recovered task snapshots retain complete arguments.
func TestEventMapperFinalToolArgsEmitsArgsBeforeEnd(t *testing.T) {
	mapper := newEventMapper(DelegationEvent{RunID: "run-1", DelegationID: "del-1", AgentKey: "implement"})
	events := mapper.Map(clienta2a.Event{
		Kind: clienta2a.EventArtifact, TaskID: "task-1", ContextID: "ctx-1", LastChunk: true,
		Artifact: &clienta2a.Artifact{
			ID:   "tool-call-bash-1",
			Name: "tool-call-bash-1",
			Parts: []clienta2a.Part{{
				Kind: clienta2a.PartText,
				Text: `{"command":"go test ./..."}`,
			}},
		},
	})
	if len(events) != 4 || events[1].Kind != DelegationArtifactCreated || events[2].Kind != DelegationToolCallArgs || events[3].Kind != DelegationToolCallEnd {
		t.Fatalf("events = %#v", events)
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
