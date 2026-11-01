package agui

import (
	"encoding/json"
	"math"
	"reflect"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	events "github.com/ag-ui-protocol/ag-ui/sdks/community/go/pkg/core/events"
	adaptor "github.com/agent-dance/agent-adaptor"
	schema "github.com/santhosh-tekuri/jsonschema/v6"
)

func frozenEventSchema(t *testing.T) *schema.Schema {
	t.Helper()
	var doc any
	if err := json.Unmarshal(protocolSchema, &doc); err != nil {
		t.Fatal(err)
	}
	c := schema.NewCompiler()
	if err := c.AddResource("events.json", doc); err != nil {
		t.Fatal(err)
	}
	compiled, err := c.Compile("events.json")
	if err != nil {
		t.Fatal(err)
	}
	return compiled
}

func assertWireEvents(t *testing.T, projected []events.Event) {
	t.Helper()
	compiled := frozenEventSchema(t)
	for _, e := range projected {
		raw, err := e.ToJSON()
		if err != nil {
			t.Fatal(err)
		}
		var value any
		if err := json.Unmarshal(raw, &value); err != nil {
			t.Fatal(err)
		}
		if err := compiled.Validate(value); err != nil {
			t.Fatalf("%s: %v", raw, err)
		}
	}
	if err := VerifySequence(projected); err != nil {
		t.Fatal(err)
	}
}

func TestBufferedOutputRetainsStateAndWarnsAfterStart(t *testing.T) {
	var hooked []events.EventType
	tr := NewEventTranslator(
		WithInputDiagnostics([]Diagnostic{{Code: "test", Path: "/", Message: "warning"}}),
		WithProjectionHook(func(e events.Event) []events.Event {
			hooked = append(hooked, e.Type())
			return []events.Event{e}
		}),
	)
	for _, e := range []adaptor.Event{
		adaptor.TextDelta{MessageID: "m", Text: "before"},
		adaptor.Thinking{MessageID: "r", Text: "thinking"},
		adaptor.ToolCall{ID: "tool", Name: "command", Phase: adaptor.PhaseStart},
	} {
		if out := tr.Translate(e); len(out) != 0 {
			t.Fatalf("pre-start output: %v", out)
		}
	}
	if len(hooked) != 0 {
		t.Fatalf("buffered output reached hook prematurely: %v", hooked)
	}
	out := tr.Translate(adaptor.RunStarted{RunID: "sdk-run"})
	if len(out) < 3 || out[0].Type() != events.EventTypeRunStarted ||
		out[1].Type() != events.EventTypeCustom {
		t.Fatalf("warning ordering: %v", out)
	}
	// A repeated boundary must neither reset open lifecycles nor resend warnings.
	if next := tr.Translate(adaptor.RunStarted{RunID: "different", ThreadID: "late"}); len(next) != 0 {
		t.Fatal(next)
	}
	out = append(out, tr.Translate(adaptor.TextDelta{MessageID: "m", Text: "after"})...)
	out = append(out, tr.CloseRun(nil)...)
	assertWireEvents(t, out)
	if out[len(out)-1].ThreadID() != out[0].ThreadID() ||
		out[len(out)-1].RunID() != out[0].RunID() {
		t.Fatal("run identity drift")
	}
	if len(hooked) != len(out)-2 {
		t.Fatalf("events projected more than once: hook=%d events=%d", len(hooked), len(out))
	}
}

func TestBoundaryIdentityFrozenBeforeLateMetadata(t *testing.T) {
	tr := NewEventTranslator()
	out := tr.Translate(adaptor.RunStarted{})
	out = append(out, tr.Translate(adaptor.WithEventMeta(adaptor.Notice{},
		adaptor.EventMeta{RunID: "late-run", ThreadKey: "late-key"}))...)
	out = append(out, tr.CloseRun(nil)...)
	if out[0].ThreadID() != out[len(out)-1].ThreadID() ||
		out[0].RunID() != out[len(out)-1].RunID() {
		t.Fatalf("boundary identity changed: %s -> %s", out[0].ThreadID(), out[len(out)-1].ThreadID())
	}
	assertWireEvents(t, out)
}

func TestStepLifecycleIsRepaired(t *testing.T) {
	for _, phases := range [][]string{
		{"started"}, {"finished"}, {"started", "started", "finished", "finished"},
	} {
		tr := NewEventTranslator()
		out := tr.Translate(adaptor.RunStarted{})
		for _, phase := range phases {
			out = append(out, tr.Translate(adaptor.Notice{Kind: adaptor.NoticeStep,
				Text: "step", Data: map[string]any{"phase": phase}})...)
		}
		out = append(out, tr.CloseRun(nil)...)
		assertWireEvents(t, out)
		starts, ends := 0, 0
		for _, e := range out {
			switch e.Type() {
			case events.EventTypeStepStarted:
				starts++
			case events.EventTypeStepFinished:
				ends++
			}
		}
		if starts != 1 || ends != 1 {
			t.Fatalf("step markers were duplicated: %v", out)
		}
	}
}

func TestToolExitCodeExactnessAndEmptyOutput(t *testing.T) {
	for _, tc := range []struct {
		value any
		want  int64
		valid bool
	}{
		{json.Number("1.0"), 1, true},
		{json.Number("1e0"), 1, true},
		{json.Number("1.00000000000000000001"), 0, false},
		{json.Number("9007199254740991.1"), 0, false},
		{float64(1.5), 0, false},
		{math.NaN(), 0, false},
		{math.Inf(1), 0, false},
		{float64(9007199254740992), 0, false},
	} {
		got, valid := exitCode(map[string]any{"exitCode": tc.value})
		if got != tc.want || valid != tc.valid {
			t.Errorf("exitCode(%v) = %d, %v; want %d, %v", tc.value, got, valid, tc.want, tc.valid)
		}
	}
	content := toolResultContent(map[string]any{"output": "", "exitCode": 1})
	if !strings.Contains(content, "[exit=1]") {
		t.Fatalf("empty command output lost exit suffix: %q", content)
	}
}

func TestSubagentActivityDoesNotMutateReturnedEvents(t *testing.T) {
	tr := NewEventTranslator()
	tr.Translate(adaptor.RunStarted{})
	update := func(kind string, extra map[string]any) []events.Event {
		data := map[string]any{"delegation_id": "child", "kind": kind, "remote_tool_call_id": "call"}
		for k, v := range extra {
			data[k] = v
		}
		return tr.Translate(adaptor.SubagentUpdate{Agent: "worker", Data: data})
	}
	out := update("subagent.tool_call.start", map[string]any{"tool_name": "command"})
	before, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	update("subagent.tool_call.args", map[string]any{"args": map[string]any{"cmd": "pwd"}})
	update("subagent.tool_call.result", map[string]any{"result": "done"})
	after, _ := json.Marshal(out)
	if string(before) != string(after) {
		t.Fatalf("previous activity changed:\n%s\n%s", before, after)
	}
}

func TestTranslatorConcurrentCalls(t *testing.T) {
	tr := NewEventTranslator()
	tr.Translate(adaptor.RunStarted{})
	var wg sync.WaitGroup
	closed := make(chan []events.Event, 20)
	for range 20 {
		wg.Go(func() {
			tr.Translate(adaptor.Notice{Kind: "concurrent"})
			closed <- tr.CloseRun(nil)
		})
	}
	wg.Wait()
	close(closed)
	terminals := 0
	for batch := range closed {
		for _, event := range batch {
			if event.Type() == events.EventTypeRunFinished {
				terminals++
			}
		}
	}
	if terminals != 1 {
		t.Fatalf("concurrent closes emitted %d terminals", terminals)
	}
	if got := tr.CloseRun(nil); !reflect.DeepEqual(got, []events.Event(nil)) {
		t.Fatal(got)
	}
}

func TestEmptyCustomNoticeMatchesFrozenSchema(t *testing.T) {
	tr := NewEventTranslator(WithNoticePassthrough("empty"))
	out := tr.Translate(adaptor.RunStarted{})
	out = append(out, tr.Translate(adaptor.Notice{Kind: "empty"})...)
	out = append(out, tr.CloseRun(nil)...)
	assertWireEvents(t, out)
}

func TestSubagentToolDoesNotInheritParentAgentMessage(t *testing.T) {
	tr := NewEventTranslator()
	tr.Translate(adaptor.RunStarted{})
	tr.Translate(adaptor.TextDelta{MessageID: "parent-message", Text: "delegating"})
	tr.Translate(adaptor.SubagentUpdate{Agent: "worker", Data: map[string]any{
		"delegation_id": "child", "kind": "subagent.tool_call.start",
		"remote_tool_call_id": "child-tool", "tool_name": "shell",
	}})
	start := tr.Translate(adaptor.ToolCall{ID: "child-tool", Name: "shell", Phase: adaptor.PhaseStart})
	tool := start[0].(*events.ToolCallStartEvent)
	// @ag-ui/client rejects child attribution on a parent-owned message.
	if tool.SubagentRunID != "child" || tool.ParentMessageID != nil {
		t.Fatalf("incompatible parent/subagent ownership: %+v", tool)
	}
}

func TestUTF8ToolTruncationAtEveryByteBoundary(t *testing.T) {
	text := "aé界🌍z"
	for limit := 1; limit <= len(text)+1; limit++ {
		tr := NewEventTranslator(WithToolContentLimit(limit))
		result := tr.toolResultEvent("call", map[string]any{"output": text}).(*events.ToolCallResultEvent)
		if len(result.Content) > limit || !strings.HasPrefix(text, result.Content) ||
			!utf8.ValidString(result.Content) || !json.Valid(mustJSON(t, result)) {
			t.Fatalf("limit=%d content=%q", limit, result.Content)
		}
		meta := result.Metadata["agentAdaptor"].(map[string]any)
		if limit < len(text) {
			if meta["truncated"] != true || meta["originalBytes"] != len(text) {
				t.Fatalf("missing truncation metadata: %v", meta)
			}
		} else if _, found := meta["truncated"]; found {
			t.Fatalf("untruncated output marked truncated: %v", meta)
		}
	}
}

func mustJSON(t *testing.T, e events.Event) []byte {
	t.Helper()
	raw, err := e.ToJSON()
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestToolAttributionRemainsFixedAfterLateSubagentDiscovery(t *testing.T) {
	tr := NewEventTranslator()
	out := tr.Translate(adaptor.RunStarted{})
	out = append(out, tr.Translate(adaptor.TextDelta{MessageID: "parent", Text: "running"})...)
	out = append(out, tr.Translate(adaptor.ToolCall{ID: "call", Name: "shell", Phase: adaptor.PhaseStart})...)
	out = append(out, tr.Translate(adaptor.SubagentUpdate{Agent: "worker", Data: map[string]any{
		"delegation_id": "child", "kind": "subagent.tool_call.start",
		"remote_tool_call_id": "call", "tool_name": "shell",
	}})...)
	out = append(out, tr.Translate(adaptor.ToolCall{ID: "call", ArgsDelta: "{}"})...)
	out = append(out, tr.Translate(adaptor.ToolCall{ID: "call", Phase: adaptor.PhaseEnd,
		Result: map[string]any{"output": "done"}})...)
	out = append(out, tr.CloseRun(nil)...)
	for _, e := range out {
		switch tool := e.(type) {
		case *events.ToolCallArgsEvent:
			if tool.SubagentRunID != "" {
				t.Fatalf("args changed owner: %+v", tool)
			}
		case *events.ToolCallEndEvent:
			if tool.SubagentRunID != "" {
				t.Fatalf("end changed owner: %+v", tool)
			}
		case *events.ToolCallResultEvent:
			if tool.SubagentRunID != "" {
				t.Fatalf("result changed owner: %+v", tool)
			}
		}
	}
	assertWireEvents(t, out)
}

func TestEndingOlderMessageDoesNotStealToolParent(t *testing.T) {
	tr := NewEventTranslator()
	tr.Translate(adaptor.RunStarted{})
	for _, id := range []string{"older", "current"} {
		tr.Translate(adaptor.TextDelta{MessageID: id, Phase: adaptor.PhaseStart})
	}
	tr.Translate(adaptor.TextDelta{MessageID: "older", Phase: adaptor.PhaseEnd})
	out := tr.Translate(adaptor.ToolCall{ID: "tool", Name: "shell", Phase: adaptor.PhaseStart})
	parent := out[0].(*events.ToolCallStartEvent).ParentMessageID
	if parent == nil || *parent != "current" {
		t.Fatalf("ending older message changed the current tool parent: %v", parent)
	}
}

func TestToolContentLimitCanRestoreDefault(t *testing.T) {
	for _, reset := range []int{0, -1} {
		tr := NewEventTranslator(WithToolContentLimit(1), WithToolContentLimit(reset))
		result := tr.toolResultEvent("call", map[string]any{"text": "whole output"}).(*events.ToolCallResultEvent)
		if result.Content != "whole output" || tr.toolContentLimit != 64<<10 {
			t.Fatalf("non-positive option did not restore the default: limit=%d content=%q",
				tr.toolContentLimit, result.Content)
		}
	}
}

func TestVerifierRejectsNestedRunStart(t *testing.T) {
	if err := verifyCopilotKitRules([]events.Event{
		events.NewRunStartedEvent("t", "r1"),
		events.NewRunStartedEvent("t", "r2"),
	}); err == nil {
		t.Fatal("verifier accepted a new run before the previous terminal")
	}
	if err := verifyCopilotKitRules([]events.Event{
		events.NewRunStartedEvent("t", "r1"),
		events.NewRunFinishedEvent("t", "r1"),
		events.NewRunStartedEvent("t", "r2"),
		events.NewRunFinishedEvent("t", "r2"),
	}); err != nil {
		t.Fatalf("verifier rejected sequential runs: %v", err)
	}
}

func TestVerifierReportsOpenMessagesDeterministically(t *testing.T) {
	out := []events.Event{
		events.NewRunStartedEvent("t", "r"),
		events.NewTextMessageStartEvent("z"),
		events.NewTextMessageStartEvent("a"),
		events.NewRunFinishedEvent("t", "r"),
	}
	for range 100 {
		err := verifyCopilotKitRules(out)
		if err == nil || !strings.Contains(err.Error(), "[a z]") {
			t.Fatalf("unstable lifecycle diagnostic: %v", err)
		}
	}
}
