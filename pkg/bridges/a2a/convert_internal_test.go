package a2a

import (
	"encoding/json"
	"math"
	"strings"
	"testing"

	a2aproto "github.com/a2aproject/a2a-go/v2/a2a"
	agentadaptor "github.com/agent-dance/agent-adaptor"
)

type testTaskInfo struct{}

func (testTaskInfo) TaskInfo() a2aproto.TaskInfo {
	return a2aproto.TaskInfo{TaskID: "task-1", ContextID: "ctx-1"}
}

func TestNormalizeJSONValuePreservesSafeIntegers(t *testing.T) {
	value, ok := normalizeJSONValue(map[string]any{"id": int64(9007199254740991)})
	if !ok {
		t.Fatal("expected value to normalize")
	}
	id, ok := value.(map[string]any)["id"].(int64)
	if !ok || id != 9007199254740991 {
		t.Fatalf("normalized id = %#v", value)
	}
	if _, ok := normalizeJSONValue(map[string]any{"id": int64(9007199254740993)}); ok {
		t.Fatal("expected unsafe JSON integer to be rejected")
	}
}

func TestOutboundPartsRejectInvalidWireValues(t *testing.T) {
	var typedNil *struct{}
	tests := []struct {
		name string
		part Part
	}{
		{name: "nil data", part: Part{Kind: PartData}},
		{name: "typed nil data", part: Part{Kind: PartData, Data: typedNil}},
		{name: "JSON null data", part: Part{Kind: PartData, Data: json.RawMessage("null")}},
		{name: "non finite data", part: Part{Kind: PartData, Data: math.NaN()}},
		{name: "unsafe integer", part: Part{Kind: PartData, Data: int64(9007199254740993)}},
		{name: "unsafe exponent integer", part: Part{Kind: PartData, Data: json.RawMessage("9.007199254740993e15")}},
		{name: "nil raw", part: Part{Kind: PartRaw}},
		{name: "empty URL", part: Part{Kind: PartURL}},
		{name: "unknown kind", part: Part{Kind: PartKind("unknown")}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := outboundParts([]Part{tc.part}); err == nil {
				t.Fatalf("expected invalid part to fail: %#v", tc.part)
			}
		})
	}
}

func TestConvertPartRejectsUnsafeInboundInteger(t *testing.T) {
	part := a2aproto.NewDataPart(float64(9007199254740992))
	if _, err := convertPart(part); err == nil {
		t.Fatal("expected unsafe inbound integer to fail")
	}
}

func TestConvertPartPreservesInboundNumberType(t *testing.T) {
	part := a2aproto.NewDataPart(float64(1))
	converted, err := convertPart(part)
	if err != nil {
		t.Fatalf("convertPart: %v", err)
	}
	if _, ok := converted.Data.(float64); !ok {
		t.Fatalf("data type = %T, want float64", converted.Data)
	}
}

func TestOutboundPartsTreatsZeroKindAsText(t *testing.T) {
	parts, err := outboundParts([]Part{{Text: "legacy text"}})
	if err != nil {
		t.Fatalf("outboundParts: %v", err)
	}
	if len(parts) != 1 || parts[0].Content != a2aproto.Text("legacy text") {
		t.Fatalf("parts = %#v", parts)
	}
}

func TestCustomTerminalArtifactsRejectReservedAndDuplicateIDs(t *testing.T) {
	part := []Part{{Kind: PartText, Text: "result"}}
	if _, err := customTerminalArtifacts(testTaskInfo{}, []ArtifactSpec{{ID: ArtifactAssistantOutput, Parts: part}}, map[string]struct{}{ArtifactAssistantOutput: {}}); err == nil {
		t.Fatal("expected reserved artifact ID to fail")
	}
	if _, err := customTerminalArtifacts(testTaskInfo{}, []ArtifactSpec{{ID: "result", Parts: part}, {ID: "result", Parts: part}}, nil); err == nil {
		t.Fatal("expected duplicate artifact ID to fail")
	}
}

func TestStreamTranslatorEmitsToolEndWithoutArgs(t *testing.T) {
	translator := newStreamTranslator(testTaskInfo{}, ExposurePolicy{IncludeToolCalls: true})
	start := translator.Translate(agentadaptor.StreamPayload{Kind: agentadaptor.StreamToolCallStart, ToolCallID: "tool-1", Name: "Read"})
	if len(start) != 1 {
		t.Fatalf("start = %#v", start)
	}
	end := translator.Translate(agentadaptor.StreamPayload{Kind: agentadaptor.StreamToolCallEnd, ToolCallID: "tool-1"})
	if len(end) != 1 {
		t.Fatalf("end = %#v", end)
	}
	update, ok := end[0].(*a2aproto.TaskArtifactUpdateEvent)
	if !ok || len(update.Artifact.Parts) != 1 {
		t.Fatalf("end event = %#v", end[0])
	}
	data, ok := update.Artifact.Parts[0].Content.(a2aproto.Data)
	if !ok || data.Value.(map[string]any)["kind"] != "tool_call.end" {
		t.Fatalf("end data = %#v", update.Artifact.Parts[0].Content)
	}
}

func TestStreamTranslatorStatusDataRoundTrip(t *testing.T) {
	translator := newStreamTranslator(testTaskInfo{}, ExposurePolicy{IncludeToolCalls: true}, StreamWireStatusData)
	want := []agentadaptor.StreamPayload{
		{Kind: agentadaptor.StreamTextStart, Sequence: 1, MessageID: "msg-1"},
		{Kind: agentadaptor.StreamTextContent, Sequence: 2, MessageID: "msg-1", Delta: "hello"},
		{Kind: agentadaptor.StreamTextEnd, Sequence: 3, MessageID: "msg-1"},
		{Kind: agentadaptor.StreamToolCallStart, Sequence: 4, ToolCallID: "tool-1", Name: "Bash"},
		{Kind: agentadaptor.StreamToolCallArgs, Sequence: 5, ToolCallID: "tool-1", Delta: `{"command":`},
		{Kind: agentadaptor.StreamToolCallEnd, Sequence: 6, ToolCallID: "tool-1"},
		{Kind: agentadaptor.StreamToolCallResult, Sequence: 7, ToolCallID: "tool-1", Result: map[string]any{"exit_code": 0}},
	}
	for _, payload := range want {
		events := translator.Translate(payload)
		if len(events) != 1 {
			t.Fatalf("%s events = %#v", payload.Kind, events)
		}
		update, ok := events[0].(*a2aproto.TaskStatusUpdateEvent)
		if !ok || update.Status.State != a2aproto.TaskStateWorking || update.Status.Message == nil || len(update.Status.Message.Parts) != 1 {
			t.Fatalf("%s status event = %#v", payload.Kind, events[0])
		}
		data, ok := update.Status.Message.Parts[0].Content.(a2aproto.Data)
		if !ok {
			t.Fatalf("%s data part = %#v", payload.Kind, update.Status.Message.Parts[0])
		}
		got, matched, err := DecodeAdapterStreamStatus(data.Value)
		if err != nil || !matched {
			t.Fatalf("%s decode matched=%v err=%v", payload.Kind, matched, err)
		}
		if got.Kind != payload.Kind || got.Sequence != payload.Sequence || got.MessageID != payload.MessageID ||
			got.ToolCallID != payload.ToolCallID || got.Name != payload.Name || got.Delta != payload.Delta {
			t.Fatalf("%s round trip = %#v, want %#v", payload.Kind, got, payload)
		}
	}
}

func TestDecodeAdapterStreamStatusIgnoresOtherSchemas(t *testing.T) {
	_, matched, err := DecodeAdapterStreamStatus(map[string]any{"type": "STATUS_TYPE_TOOL_CALL"})
	if err != nil || matched {
		t.Fatalf("matched=%v err=%v", matched, err)
	}
	_, matched, err = DecodeAdapterStreamStatus(map[string]any{
		"schema":  "external.status.v1",
		"payload": strings.Repeat("x", adapterStreamMaxBytes),
	})
	if err != nil || matched {
		t.Fatalf("oversized external schema matched=%v err=%v", matched, err)
	}
}

func TestStreamTranslatorStatusDataReportsOversizedEvent(t *testing.T) {
	translator := newStreamTranslator(testTaskInfo{}, ExposurePolicy{}, StreamWireStatusData)
	events := translator.Translate(agentadaptor.StreamPayload{
		Kind: agentadaptor.StreamTextContent, Sequence: 9, MessageID: "msg-1",
		Delta: strings.Repeat("x", adapterStreamMaxBytes),
	})
	if len(events) != 1 {
		t.Fatalf("events = %#v", events)
	}
	update := events[0].(*a2aproto.TaskStatusUpdateEvent)
	data := update.Status.Message.Parts[0].Content.(a2aproto.Data)
	payload, matched, err := DecodeAdapterStreamStatus(data.Value)
	if err != nil || !matched || payload.Kind != agentadaptor.StreamDropped || payload.Sequence != 9 {
		t.Fatalf("overflow payload=%#v matched=%v err=%v", payload, matched, err)
	}
	if payload.Raw["dropped_kind"] != string(agentadaptor.StreamTextContent) {
		t.Fatalf("overflow raw = %#v", payload.Raw)
	}
}
