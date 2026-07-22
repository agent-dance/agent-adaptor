package agui_test

import (
	"encoding/json"
	"testing"

	aguievents "github.com/ag-ui-protocol/ag-ui/sdks/community/go/pkg/core/events"

	agentadaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/pkg/bridges/agui"
)

func TestTranslatorTypedSubagentIDDrivesActivityWithoutMessageID(t *testing.T) {
	t.Parallel()
	tr := agui.NewTranslator()
	ref := nativeSubagent("sub-1", "Research", "parent-call")

	events := translateAll(tr, []agentadaptor.StreamPayload{
		{Kind: agentadaptor.StreamRunStarted, ThreadID: "t", RunID: "r"},
		{Kind: agentadaptor.StreamSubagentStart, RunID: "r", Subagent: ref},
		{
			Kind:     agentadaptor.StreamSubagentEnd,
			RunID:    "r",
			Subagent: ref,
			Result:   map[string]any{"status": "completed", "text": "done"},
		},
		{Kind: agentadaptor.StreamRunFinished, ThreadID: "t", RunID: "r"},
	})

	assertTypesEqual(t, []aguievents.EventType{
		aguievents.EventTypeRunStarted,
		aguievents.EventTypeActivitySnapshot,
		aguievents.EventTypeActivityDelta,
		aguievents.EventTypeRunFinished,
	}, typesOf(events))

	snapshot := mustActivitySnapshot(t, events[1])
	if snapshot.MessageID != ref.ID {
		t.Fatalf("snapshot messageId: got %q want %q", snapshot.MessageID, ref.ID)
	}
	content := decodeSubagentContent(t, snapshot.Content)
	if content["subagentId"] != ref.ID || content["agentKey"] != ref.Name {
		t.Fatalf("snapshot did not use SubagentRef metadata: %#v", content)
	}
	delta := mustActivityDelta(t, events[2])
	assertPatchHas(t, delta.Patch, "/status", "completed")
	assertPatchHas(t, delta.Patch, "/text", "done")
	assertPatchPath(t, delta.Patch, "/result")
	assertPatchPath(t, delta.Patch, "/updatedAt")
}

func TestTranslatorDuplicateSubagentStartIsIdempotent(t *testing.T) {
	t.Parallel()
	tr := agui.NewTranslator()
	ref := nativeSubagent("sub-duplicate", "Worker", "parent-call")

	events := translateAll(tr, []agentadaptor.StreamPayload{
		{Kind: agentadaptor.StreamRunStarted, ThreadID: "t", RunID: "r"},
		{Kind: agentadaptor.StreamSubagentStart, Subagent: ref},
		{Kind: agentadaptor.StreamTextContent, Delta: "first", Subagent: ref},
		{Kind: agentadaptor.StreamToolCallStart, ToolCallID: "inner", Name: "Read", Subagent: ref},
		{Kind: agentadaptor.StreamSubagentStart, Subagent: ref},
		{Kind: agentadaptor.StreamTextContent, Delta: " second", Subagent: ref},
		{Kind: agentadaptor.StreamToolCallResult, ToolCallID: "inner", Result: map[string]any{"text": "kept"}, Subagent: ref},
		{Kind: agentadaptor.StreamSubagentEnd, Subagent: ref, Result: map[string]any{"status": "completed"}},
		{Kind: agentadaptor.StreamRunFinished, ThreadID: "t", RunID: "r"},
	})

	var snapshots int
	var preservedText, preservedTool bool
	for _, event := range events {
		switch typed := event.(type) {
		case *aguievents.ActivitySnapshotEvent:
			snapshots++
		case *aguievents.ActivityDeltaEvent:
			for _, operation := range typed.Patch {
				if operation.Path == "/text" && operation.Value == "first second" {
					preservedText = true
				}
				if operation.Path == "/toolCalls/0/result" {
					preservedTool = true
				}
			}
		}
	}
	if snapshots != 1 {
		t.Fatalf("duplicate start emitted %d Activity snapshots, want 1; types=%v", snapshots, typesOf(events))
	}
	if !preservedText || !preservedTool {
		t.Fatalf("duplicate start lost accumulated state: text=%v tool=%v", preservedText, preservedTool)
	}
}

func TestTranslatorTypedScopedEventsAggregateIntoActivity(t *testing.T) {
	t.Parallel()
	tr := agui.NewTranslator()
	ref := nativeSubagent("sub-2", "Worker", "parent-call")

	events := translateAll(tr, []agentadaptor.StreamPayload{
		{Kind: agentadaptor.StreamRunStarted, ThreadID: "t", RunID: "r"},
		{Kind: agentadaptor.StreamSubagentStart, RunID: "r", Subagent: ref},
		{Kind: agentadaptor.StreamTextStart, MessageID: "child-text", Subagent: ref},
		{Kind: agentadaptor.StreamTextContent, MessageID: "child-text", Delta: "answer", Subagent: ref},
		{Kind: agentadaptor.StreamTextEnd, MessageID: "child-text", Subagent: ref},
		{Kind: agentadaptor.StreamReasoningStart, MessageID: "child-thinking", Subagent: ref},
		{Kind: agentadaptor.StreamReasoningContent, MessageID: "child-thinking", Delta: "inspect", Subagent: ref},
		{Kind: agentadaptor.StreamReasoningEnd, MessageID: "child-thinking", Subagent: ref},
		{Kind: agentadaptor.StreamToolCallStart, ToolCallID: "inner", Name: "Read", Args: map[string]any{"path": "README.md"}, Subagent: ref},
		{Kind: agentadaptor.StreamToolCallArgs, ToolCallID: "inner", Delta: `{"limit":10}`, Subagent: ref},
		{Kind: agentadaptor.StreamToolCallEnd, ToolCallID: "inner", Subagent: ref},
		{Kind: agentadaptor.StreamToolCallResult, ToolCallID: "inner", Result: map[string]any{"text": "contents"}, Subagent: ref},
		{
			Kind:     agentadaptor.StreamSubagentEnd,
			Subagent: ref,
			Result:   map[string]any{"status": "completed", "summary": "finished"},
		},
		{Kind: agentadaptor.StreamRunFinished, ThreadID: "t", RunID: "r"},
	})

	var snapshots, deltas int
	for _, event := range events {
		switch event.Type() {
		case aguievents.EventTypeActivitySnapshot:
			snapshots++
		case aguievents.EventTypeActivityDelta:
			deltas++
		case aguievents.EventTypeTextMessageStart,
			aguievents.EventTypeTextMessageContent,
			aguievents.EventTypeReasoningMessageStart,
			aguievents.EventTypeReasoningMessageContent,
			aguievents.EventTypeToolCallStart,
			aguievents.EventTypeToolCallArgs:
			t.Fatalf("scoped event escaped Activity aggregation: %s", event.Type())
		}
	}
	if snapshots != 1 || deltas < 7 {
		t.Fatalf("unexpected Activity counts: snapshots=%d deltas=%d types=%v", snapshots, deltas, typesOf(events))
	}
}

func TestTranslatorTypedSubagentMetadataWinsOverLegacyRaw(t *testing.T) {
	t.Parallel()
	tr := agui.NewTranslator()
	ref := nativeSubagent("typed-id", "Typed Name", "typed-parent")

	events := translateAll(tr, []agentadaptor.StreamPayload{
		{Kind: agentadaptor.StreamRunStarted, ThreadID: "t", RunID: "r"},
		{
			Kind:     agentadaptor.StreamSubagentStart,
			Subagent: ref,
			Raw: map[string]any{
				"subagent_id":         "legacy-id",
				"agent_key":           "Legacy Name",
				"parent_tool_call_id": "legacy-parent",
				"subagent_kind":       "delegated",
				"subagent_protocol":   "a2a",
			},
		},
		{Kind: agentadaptor.StreamSubagentEnd, Subagent: ref, Result: map[string]any{"status": "completed"}},
		{Kind: agentadaptor.StreamRunFinished, ThreadID: "t", RunID: "r"},
	})

	snapshot := mustActivitySnapshot(t, events[1])
	if snapshot.MessageID != "typed-id" {
		t.Fatalf("typed ID did not win: %q", snapshot.MessageID)
	}
	content := decodeSubagentContent(t, snapshot.Content)
	if content["agentKey"] != "Typed Name" || content["parentToolCallId"] != "typed-parent" {
		t.Fatalf("typed metadata did not win: %#v", content)
	}
}

func TestTranslatorLegacyRawScopeFallback(t *testing.T) {
	t.Parallel()
	tr := agui.NewTranslator()
	raw := map[string]any{
		"subagent_id":           "legacy-id",
		"agent_key":             "legacy-worker",
		"subagent_kind":         "delegated",
		"subagent_protocol":     "a2a",
		"subagent_tool_call_id": "legacy-parent",
	}
	events := translateAll(tr, []agentadaptor.StreamPayload{
		{Kind: agentadaptor.StreamRunStarted, ThreadID: "t", RunID: "r"},
		{Kind: agentadaptor.StreamSubagentStart, Raw: raw},
		{Kind: agentadaptor.StreamTextContent, Delta: "legacy text", Raw: raw},
		{Kind: agentadaptor.StreamSubagentEnd, Result: map[string]any{"status": "completed"}, Raw: raw},
		{Kind: agentadaptor.StreamRunFinished, ThreadID: "t", RunID: "r"},
	})
	if got := mustActivitySnapshot(t, events[1]).MessageID; got != "legacy-id" {
		t.Fatalf("legacy Raw fallback ID: got %q", got)
	}
}

func TestTranslatorSubagentAsToolCallLifecycle(t *testing.T) {
	t.Parallel()
	tr := agui.NewTranslator(agui.WithSubagentMode(agui.SubagentAsToolCall))
	ref := nativeSubagent("sub-tool", "Research", "parent-call")

	events := translateAll(tr, []agentadaptor.StreamPayload{
		{Kind: agentadaptor.StreamRunStarted, ThreadID: "t", RunID: "r"},
		{Kind: agentadaptor.StreamSubagentStart, RunID: "r", Subagent: ref, Delta: "research task"},
		{Kind: agentadaptor.StreamSubagentStatus, Subagent: ref, Delta: "reading", Result: map[string]any{"status": "running"}},
		{
			Kind:     agentadaptor.StreamSubagentEnd,
			Subagent: ref,
			Result:   map[string]any{"status": "completed", "text": "result"},
		},
		{Kind: agentadaptor.StreamRunFinished, ThreadID: "t", RunID: "r"},
	})

	assertTypesEqual(t, []aguievents.EventType{
		aguievents.EventTypeRunStarted,
		aguievents.EventTypeToolCallStart,
		aguievents.EventTypeToolCallArgs,
		aguievents.EventTypeToolCallArgs,
		aguievents.EventTypeToolCallArgs,
		aguievents.EventTypeToolCallEnd,
		aguievents.EventTypeToolCallResult,
		aguievents.EventTypeRunFinished,
	}, typesOf(events))
	start := events[1].(*aguievents.ToolCallStartEvent)
	end := events[5].(*aguievents.ToolCallEndEvent)
	result := events[6].(*aguievents.ToolCallResultEvent)
	if start.ToolCallID != "subagent:sub-tool" || end.ToolCallID != start.ToolCallID || result.ToolCallID != start.ToolCallID {
		t.Fatalf("unstable subagent tool ID: start=%q end=%q result=%q", start.ToolCallID, end.ToolCallID, result.ToolCallID)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(result.Content), &body); err != nil {
		t.Fatalf("decode tool result: %v", err)
	}
	if body["status"] != "completed" || body["description"] != "reading" || body["text"] != "result" {
		t.Fatalf("tool result did not include status aggregation: %#v", body)
	}
	var argsJSON string
	for _, event := range events {
		if args, ok := event.(*aguievents.ToolCallArgsEvent); ok {
			argsJSON += args.Delta
		}
	}
	var argsBody map[string]any
	if err := json.Unmarshal([]byte(argsJSON), &argsBody); err != nil {
		t.Fatalf("streamed tool args are not valid JSON: %v (%q)", err, argsJSON)
	}
	updates, _ := argsBody["updates"].([]any)
	if len(updates) != 2 {
		t.Fatalf("tool args did not stream status and terminal updates: %#v", argsBody)
	}
	assertVerified(t, events)
}

func TestTranslatorSubagentAsToolCallSyntheticCloseBeforeRunFinished(t *testing.T) {
	t.Parallel()
	tr := agui.NewTranslator(agui.WithSubagentMode(agui.SubagentAsToolCall))
	ref := nativeSubagent("sub-open", "Worker", "")
	events := translateAll(tr, []agentadaptor.StreamPayload{
		{Kind: agentadaptor.StreamRunStarted, ThreadID: "t", RunID: "r"},
		{Kind: agentadaptor.StreamSubagentStart, Subagent: ref},
		{Kind: agentadaptor.StreamRunFinished, ThreadID: "t", RunID: "r"},
	})
	assertTypesEqual(t, []aguievents.EventType{
		aguievents.EventTypeRunStarted,
		aguievents.EventTypeToolCallStart,
		aguievents.EventTypeToolCallArgs,
		aguievents.EventTypeToolCallArgs,
		aguievents.EventTypeToolCallEnd,
		aguievents.EventTypeToolCallResult,
		aguievents.EventTypeRunFinished,
	}, typesOf(events))
}

func TestTranslatorActivitySyntheticCloseBeforeRunFinished(t *testing.T) {
	t.Parallel()
	tr := agui.NewTranslator()
	ref := nativeSubagent("sub-open-activity", "Worker", "")
	events := translateAll(tr, []agentadaptor.StreamPayload{
		{Kind: agentadaptor.StreamRunStarted, ThreadID: "t", RunID: "r"},
		{Kind: agentadaptor.StreamSubagentStart, Subagent: ref},
		{Kind: agentadaptor.StreamRunFinished, ThreadID: "t", RunID: "r"},
	})
	assertTypesEqual(t, []aguievents.EventType{
		aguievents.EventTypeRunStarted,
		aguievents.EventTypeActivitySnapshot,
		aguievents.EventTypeActivityDelta,
		aguievents.EventTypeRunFinished,
	}, typesOf(events))
	delta := mustActivityDelta(t, events[2])
	assertPatchHas(t, delta.Patch, "/status", "failed")
	assertPatchPath(t, delta.Patch, "/error")
}

func TestTranslatorSubagentCustomModeUsesTypedMetadata(t *testing.T) {
	t.Parallel()
	tr := agui.NewTranslator(agui.WithSubagentMode(agui.SubagentAsCustom))
	ref := nativeSubagent("sub-custom", "Worker", "parent")
	events := translateAll(tr, []agentadaptor.StreamPayload{
		{Kind: agentadaptor.StreamRunStarted, ThreadID: "t", RunID: "r"},
		{Kind: agentadaptor.StreamSubagentStart, Subagent: ref},
		{Kind: agentadaptor.StreamRunFinished, ThreadID: "t", RunID: "r"},
	})
	assertTypesEqual(t, []aguievents.EventType{
		aguievents.EventTypeRunStarted,
		aguievents.EventTypeCustom,
		aguievents.EventTypeRunFinished,
	}, typesOf(events))
	custom := events[1].(*aguievents.CustomEvent)
	value := custom.Value.(map[string]any)
	if value["subagentId"] != ref.ID || value["agentName"] != ref.Name {
		t.Fatalf("custom event omitted typed metadata: %#v", value)
	}
}

func TestTranslatorParentToolCallsRemainUnchangedWithActivities(t *testing.T) {
	t.Parallel()
	tr := agui.NewTranslator()
	ref := nativeSubagent("sub-3", "Worker", "parent-call")
	events := translateAll(tr, []agentadaptor.StreamPayload{
		{Kind: agentadaptor.StreamRunStarted, ThreadID: "t", RunID: "r"},
		{Kind: agentadaptor.StreamToolCallStart, ToolCallID: "parent-call", Name: "delegate_to_agent"},
		{Kind: agentadaptor.StreamToolCallArgs, ToolCallID: "parent-call", Delta: `{}`},
		{Kind: agentadaptor.StreamToolCallEnd, ToolCallID: "parent-call"},
		{Kind: agentadaptor.StreamSubagentStart, Subagent: ref},
		{Kind: agentadaptor.StreamSubagentEnd, Subagent: ref, Result: map[string]any{"status": "completed"}},
		{Kind: agentadaptor.StreamToolCallResult, ToolCallID: "parent-call", Result: map[string]any{"text": "done"}},
		{Kind: agentadaptor.StreamRunFinished, ThreadID: "t", RunID: "r"},
	})
	assertTypesEqual(t, []aguievents.EventType{
		aguievents.EventTypeRunStarted,
		aguievents.EventTypeToolCallStart,
		aguievents.EventTypeToolCallArgs,
		aguievents.EventTypeToolCallEnd,
		aguievents.EventTypeActivitySnapshot,
		aguievents.EventTypeActivityDelta,
		aguievents.EventTypeToolCallResult,
		aguievents.EventTypeRunFinished,
	}, typesOf(events))
}

func nativeSubagent(id, name, toolCallID string) *agentadaptor.SubagentRef {
	return &agentadaptor.SubagentRef{
		ID:         id,
		Name:       name,
		Kind:       "native",
		ToolCallID: toolCallID,
	}
}

func mustActivitySnapshot(t *testing.T, event aguievents.Event) *aguievents.ActivitySnapshotEvent {
	t.Helper()
	snapshot, ok := event.(*aguievents.ActivitySnapshotEvent)
	if !ok {
		t.Fatalf("expected ActivitySnapshotEvent, got %T", event)
	}
	return snapshot
}

func mustActivityDelta(t *testing.T, event aguievents.Event) *aguievents.ActivityDeltaEvent {
	t.Helper()
	delta, ok := event.(*aguievents.ActivityDeltaEvent)
	if !ok {
		t.Fatalf("expected ActivityDeltaEvent, got %T", event)
	}
	return delta
}

func assertPatchHas(t *testing.T, operations []aguievents.JSONPatchOperation, path string, value any) {
	t.Helper()
	for _, operation := range operations {
		if operation.Path == path && operation.Value == value {
			return
		}
	}
	t.Fatalf("patch missing path=%q value=%v: %#v", path, value, operations)
}

func assertPatchPath(t *testing.T, operations []aguievents.JSONPatchOperation, path string) {
	t.Helper()
	for _, operation := range operations {
		if operation.Path == path {
			return
		}
	}
	t.Fatalf("patch missing path=%q: %#v", path, operations)
}

func decodeSubagentContent(t *testing.T, content any) map[string]any {
	t.Helper()
	data, err := json.Marshal(content)
	if err != nil {
		t.Fatalf("marshal Activity content: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("decode Activity content: %v", err)
	}
	return decoded
}
