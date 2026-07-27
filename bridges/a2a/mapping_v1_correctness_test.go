package a2a

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	a2aproto "github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/agent-dance/agent-adaptor/driver"
	adaptor "github.com/agent-dance/agent-adaptor/next"
)

func dataValueV1(t *testing.T, event a2aproto.Event) any {
	t.Helper()
	update, ok := event.(*a2aproto.TaskStatusUpdateEvent)
	if !ok || update.Status.Message == nil || len(update.Status.Message.Parts) != 1 {
		t.Fatalf("status event = %#v", event)
	}
	data, ok := update.Status.Message.Parts[0].Content.(a2aproto.Data)
	if !ok {
		t.Fatalf("part = %#v", update.Status.Message.Parts[0])
	}
	return data.Value
}

func TestV1TranslatorAndDecoderPreserveEventMetaAndToolSnapshots(t *testing.T) {
	sourceTime := time.Date(2026, 7, 28, 1, 2, 3, 4, time.UTC)
	meta := adaptor.EventMeta{
		RunID: "run", ThreadKey: "opaque/thread:key", TurnID: "turn", Sequence: 17, Time: sourceTime,
		Source: &adaptor.EventSourceMeta{RunID: "provider-run", ThreadID: "provider-thread", TurnID: "provider-turn", Sequence: 99, Timestamp: sourceTime.Add(time.Second)},
	}
	input := adaptor.WithEventMeta(adaptor.ToolCall{
		ID: "tool", Name: "shell", Args: map[string]any{"cmd": "go test"},
		Result: map[string]any{"exit": 0}, Phase: adaptor.PhaseEnd,
	}, meta)
	translator := newStreamTranslatorV1(testTaskInfo{}, ExposurePolicy{
		IncludeToolCalls: true, Diagnostics: DiagnosticsPolicy{IncludeMetadata: true},
	})
	events := translator.Translate(input)
	if len(events) != 1 {
		t.Fatalf("events = %#v", events)
	}
	decoded, matched, err := DecodeAdapterEventV1(dataValueV1(t, events[0]))
	if err != nil || !matched {
		t.Fatalf("decode matched=%v err=%v", matched, err)
	}
	tool, ok := decoded.(adaptor.ToolCall)
	if !ok || tool.Phase != adaptor.PhaseEnd || !reflect.DeepEqual(tool.Args, map[string]any{"cmd": "go test"}) || !reflect.DeepEqual(tool.Result, map[string]any{"exit": float64(0)}) {
		t.Fatalf("decoded tool = %#v", decoded)
	}
	gotMeta := decoded.Meta()
	if gotMeta.RunID != meta.RunID || gotMeta.ThreadKey != meta.ThreadKey || gotMeta.Sequence != 17 || !gotMeta.Time.Equal(meta.Time) || gotMeta.Source == nil || gotMeta.Source.ThreadID != "provider-thread" {
		t.Fatalf("decoded meta = %#v", gotMeta)
	}
}

func TestV1ApprovalAndDroppedRoundTripAreCompleteAndPolicyGated(t *testing.T) {
	deadline := time.Date(2026, 7, 28, 2, 3, 4, 0, time.UTC)
	approval := adaptor.WithEventMeta(&adaptor.ApprovalRequest{
		ID: "req", Kind: adaptor.ApprovalQuestion, Source: "provider", Title: "choose",
		Details: map[string]any{"path": "safe"}, Choices: []adaptor.Choice{{Key: "a", Label: "A", Description: "first"}},
		ToolCallID: "tool", Deadline: deadline, Attempt: 3,
	}, adaptor.EventMeta{RunID: "run", Sequence: 1})

	hidden := newStreamTranslatorV1(testTaskInfo{}, ExposurePolicy{})
	if got := hidden.Translate(approval); len(got) != 0 {
		t.Fatalf("default policy exposed HITL: %#v", got)
	}
	exposed := newStreamTranslatorV1(testTaskInfo{}, ExposurePolicy{
		IncludeHITL: true, Diagnostics: DiagnosticsPolicy{IncludeHITLPayloads: true},
	})
	events := exposed.Translate(approval)
	decoded, matched, err := DecodeAdapterEventV1(dataValueV1(t, events[0]))
	if err != nil || !matched {
		t.Fatalf("decode matched=%v err=%v", matched, err)
	}
	request := decoded.(*adaptor.ApprovalRequest)
	if request.ID != "req" || request.Kind != adaptor.ApprovalQuestion || request.Title != "choose" || request.ToolCallID != "tool" || request.Attempt != 3 || !request.Deadline.Equal(deadline) || len(request.Choices) != 1 || request.Choices[0].Description != "first" {
		t.Fatalf("approval = %#v", request)
	}

	dropped := adaptor.WithEventMeta(adaptor.Dropped{
		Count: 4, ByKind: map[string]int{"text.content": 3, "thinking.content": 1},
		FirstSequence: 2, LastSequence: 5, Reason: "backpressure", Source: "sdk", Details: map[string]any{"capacity": 1},
	}, adaptor.EventMeta{RunID: "run", Sequence: 6})
	if got := hidden.Translate(dropped); len(got) != 0 {
		t.Fatalf("default policy exposed drop diagnostics: %#v", got)
	}
	diagnostic := newStreamTranslatorV1(testTaskInfo{}, ExposurePolicy{Diagnostics: DiagnosticsPolicy{IncludeUsage: true}})
	events = diagnostic.Translate(dropped)
	decoded, matched, err = DecodeAdapterEventV1(dataValueV1(t, events[0]))
	if err != nil || !matched {
		t.Fatalf("decode drop matched=%v err=%v", matched, err)
	}
	drop := decoded.(adaptor.Dropped)
	if drop.Count != 4 || drop.FirstSequence != 2 || drop.LastSequence != 5 || drop.ByKind["text.content"] != 3 || drop.Reason != "backpressure" || drop.Details["capacity"] != float64(1) {
		t.Fatalf("drop = %#v", drop)
	}
}

func TestV1RunFinishedNeverCreatesASecondA2ATerminal(t *testing.T) {
	translator := newStreamTranslatorV1(testTaskInfo{}, ExposurePolicy{})
	if got := translator.Translate(adaptor.RunFinished{RunID: "run", Failed: true, Reason: adaptor.ReasonAgentError, Message: "misleading"}); len(got) != 0 {
		t.Fatalf("informational RunFinished emitted terminal status: %#v", got)
	}
}

func TestV1BusinessFailureDetailsRespectExposurePolicy(t *testing.T) {
	runError := &adaptor.RunError{
		Reason: adaptor.ReasonPolicyViolation, Message: "blocked",
		Details: map[string]any{"rule": "no-write", "authorization": "Bearer secret"},
	}
	hidden := failureDetailsV1(runError, ExposurePolicy{})
	if !reflect.DeepEqual(hidden, map[string]any{"code": string(adaptor.ReasonPolicyViolation)}) {
		t.Fatalf("default failure details = %#v", hidden)
	}
	exposed := failureDetailsV1(runError, ExposurePolicy{Diagnostics: DiagnosticsPolicy{IncludeMetadata: true}})
	metadata, ok := exposed["metadata"].(map[string]any)
	if !ok || metadata["rule"] != "no-write" {
		t.Fatalf("exposed failure details = %#v", exposed)
	}
	serialized, _ := json.Marshal(metadata)
	if strings.Contains(string(serialized), "secret") {
		t.Fatalf("failure metadata leaked secret: %s", serialized)
	}
}

func TestV1ProviderTerminalExposureIsExplicitSanitizedAndValidJSONOnly(t *testing.T) {
	result := resultWithRawV1ForTest(t, adaptor.RawStreams{Terminal: &driver.TerminalPayload{
		Event: "turn/completed", JSON: json.RawMessage(`{"authorization":"Bearer secret","answer":42}`),
	}})

	hidden := defaultTerminalArtifactsV1(testTaskInfo{}, result, ExposurePolicy{})
	if artifactDataV1(t, hidden)["provider_result"] != nil {
		t.Fatalf("default exposure leaked provider terminal: %#v", artifactDataV1(t, hidden))
	}
	exposed := defaultTerminalArtifactsV1(testTaskInfo{}, result, ExposurePolicy{Diagnostics: DiagnosticsPolicy{IncludeProviderResult: true}})
	provider, ok := artifactDataV1(t, exposed)["provider_result"].(map[string]any)
	if !ok || provider["event"] != "turn/completed" {
		t.Fatalf("provider result = %#v", provider)
	}
	serialized, _ := json.Marshal(provider)
	if string(serialized) == "" || strings.Contains(string(serialized), "secret") {
		t.Fatalf("provider terminal was not sanitized: %s", serialized)
	}

	result = resultWithRawV1ForTest(t, adaptor.RawStreams{Terminal: &driver.TerminalPayload{Event: "bad", JSON: json.RawMessage(`{`)}})
	invalid := defaultTerminalArtifactsV1(testTaskInfo{}, result, ExposurePolicy{Diagnostics: DiagnosticsPolicy{IncludeProviderResult: true}})
	if artifactDataV1(t, invalid)["provider_result"] != nil {
		t.Fatalf("invalid JSON terminal must be omitted: %#v", artifactDataV1(t, invalid))
	}
}

type rawResultDriverV1 struct{ raw adaptor.RawStreams }

func (d rawResultDriverV1) Descriptor() driver.Descriptor { return driver.Descriptor{Type: "raw-test"} }
func (d rawResultDriverV1) ValidateConfig(any) error      { return nil }
func (d rawResultDriverV1) Run(context.Context, driver.Request, driver.EventSink) (driver.Response, error) {
	return driver.Response{Summary: "summary", RawStreams: &d.raw}, nil
}

func resultWithRawV1ForTest(t *testing.T, raw adaptor.RawStreams) *adaptor.Result {
	t.Helper()
	result, err := adaptor.New(rawResultDriverV1{raw: raw}).Run(context.Background(), "prompt")
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func artifactDataV1(t *testing.T, events []a2aproto.Event) map[string]any {
	t.Helper()
	artifact := events[0].(*a2aproto.TaskArtifactUpdateEvent)
	return artifact.Artifact.Parts[0].Content.(a2aproto.Data).Value.(map[string]any)
}
