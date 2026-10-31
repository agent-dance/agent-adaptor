package agui

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	events "github.com/ag-ui-protocol/ag-ui/sdks/community/go/pkg/core/events"
	adaptor "github.com/agent-dance/agent-adaptor"
	schema "github.com/santhosh-tekuri/jsonschema/v6"
)

func TestToolMetadata(t *testing.T) {
	for _, code := range []any{int(1), int64(1), float64(1), json.Number("1")} {
		tr := NewEventTranslator()
		tr.Translate(adaptor.RunStarted{RunID: "r"})
		out := tr.Translate(adaptor.ToolResult{ID: "t", Result: map[string]any{"output": "bad", "exitCode": code}})
		e := out[0].(*events.ToolCallResultEvent)
		if !strings.HasSuffix(e.Content, "[exit=1]") || e.Metadata["agentAdaptor"].(map[string]any)["status"] != "failed" {
			t.Fatalf("%#v", e)
		}
	}
	for _, tc := range []struct {
		m      map[string]any
		status string
	}{
		{map[string]any{"status": "completed"}, "succeeded"},
		{map[string]any{"status": "declined"}, "failed"},
		{map[string]any{"status": "interrupted"}, "cancelled"},
		{map[string]any{"is_error": true}, "failed"},
		{map[string]any{"isError": true}, "failed"},
		{map[string]any{"success": false}, "failed"},
		{map[string]any{"error": "bad"}, "failed"},
		{map[string]any{}, "unknown"},
		{map[string]any{"exit_code": json.Number("0")}, "succeeded"},
	} {
		if got := toolMetadata(tc.m)["status"]; got != tc.status {
			t.Fatalf("%v -> %v", tc.m, got)
		}
	}
	for _, limit := range []int{5, 64 << 10} {
		tr := NewEventTranslator(WithToolContentLimit(limit))
		e := tr.toolResultEvent("t", map[string]any{"text": strings.Repeat("界", 30000)}).(*events.ToolCallResultEvent)
		m := e.Metadata["agentAdaptor"].(map[string]any)
		if len(e.Content) > limit || !utf8.ValidString(e.Content) || m["truncated"] != true || m["originalBytes"] != 90000 {
			t.Fatalf("len=%d meta=%v", len(e.Content), m)
		}
	}
}

func TestIdentityAttributionHook(t *testing.T) {
	tr := NewEventTranslator(WithRunIdentity("public-t", "public-r", "parent"), WithProjectionHook(func(e events.Event) []events.Event {
		if e.Type() == events.EventTypeReasoningMessageContent {
			return nil
		}
		return []events.Event{e}
	}))
	out := tr.Translate(adaptor.WithEventMeta(adaptor.RunStarted{ThreadID: "provider"}, adaptor.EventMeta{RunID: "sdk", ThreadKey: "key"}))
	raw, _ := out[0].ToJSON()
	if !strings.Contains(string(raw), `"protocolVersion":"1.0"`) || out[0].RunID() != "public-r" {
		t.Fatal(string(raw))
	}
	tr.Translate(adaptor.TextDelta{MessageID: "assistant", Phase: adaptor.PhaseStart})
	start := tr.Translate(adaptor.ToolCall{ID: "tool", Name: "f", Phase: adaptor.PhaseStart})[0].(*events.ToolCallStartEvent)
	tr.Translate(adaptor.TextDelta{MessageID: "later-assistant", Phase: adaptor.PhaseStart})
	if start.ParentMessageID == nil || *start.ParentMessageID != "assistant" {
		t.Fatal(start)
	}
	if out := tr.Translate(adaptor.ProcessInfo{Text: "private raw protocol"}); len(out) != 0 {
		t.Fatal(out)
	}
	out = tr.CloseRun(nil)
	if out[len(out)-1].RunID() != "public-r" || out[len(out)-1].ThreadID() != "public-t" {
		t.Fatal(out)
	}
	tr = NewEventTranslator(WithRunIdentity("t", "r", ""))
	out = tr.CloseRun(&InputError{Code: "fail"})
	m := out[len(out)-1].GetBaseEvent().Metadata["agentAdaptor"].(map[string]any)
	if m["runId"] != "r" || m["threadId"] != "t" {
		t.Fatal(m)
	}
}

func TestSubagentToolAttribution(t *testing.T) {
	tr := NewEventTranslator()
	tr.Translate(adaptor.RunStarted{RunID: "r"})
	tr.Translate(adaptor.SubagentUpdate{Agent: "worker", Kind: adaptor.SubagentStarted, Data: map[string]any{"delegation_id": "delegated-run"}})
	tr.Translate(adaptor.SubagentUpdate{
		Agent: "worker",
		Data: map[string]any{
			"delegation_id": "delegated-run", "kind": "subagent.tool_call.start",
			"remote_tool_call_id": "tool", "tool_name": "shell",
		},
	})
	start := tr.Translate(adaptor.ToolCall{ID: "tool", Name: "shell", Phase: adaptor.PhaseStart})[0].(*events.ToolCallStartEvent)
	args := tr.Translate(adaptor.ToolCall{ID: "tool", ArgsDelta: "{}"})[0].(*events.ToolCallArgsEvent)
	end := tr.Translate(adaptor.ToolCall{ID: "tool", Phase: adaptor.PhaseEnd})[0].(*events.ToolCallEndEvent)
	result := tr.Translate(adaptor.ToolResult{ID: "tool", Result: map[string]any{"status": "completed"}})[0].(*events.ToolCallResultEvent)
	if start.SubagentRunID != "delegated-run" || args.SubagentRunID != start.SubagentRunID ||
		end.SubagentRunID != start.SubagentRunID || result.SubagentRunID != start.SubagentRunID {
		t.Fatalf("%+v %+v %+v %+v", start, args, end, result)
	}
}

func TestErrorBoundaryMatchesFrozenSchema(t *testing.T) {
	tr := NewEventTranslator(WithRunIdentity("thread", "run", "parent"))
	projected := tr.CloseRun(&InputError{Code: "test", Message: "failure"})
	var doc any
	if err := json.Unmarshal(protocolSchema, &doc); err != nil {
		t.Fatal(err)
	}
	compiler := schema.NewCompiler()
	if err := compiler.AddResource("events.json", doc); err != nil {
		t.Fatal(err)
	}
	compiled, err := compiler.Compile("events.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range projected {
		raw, err := e.ToJSON()
		if err != nil {
			t.Fatal(err)
		}
		var v any
		_ = json.Unmarshal(raw, &v)
		if err := compiled.Validate(v); err != nil {
			t.Fatal(err)
		}
	}
}

func TestProjectionHookCannotRemoveRunBoundaries(t *testing.T) {
	for _, terminalError := range []error{nil, &InputError{Code: "test", Message: "failed"}} {
		var hooked []events.EventType
		translator := NewEventTranslator(WithProjectionHook(func(event events.Event) []events.Event {
			hooked = append(hooked, event.Type())
			return nil
		}))
		projected := translator.Translate(adaptor.RunStarted{RunID: "run"})
		projected = append(projected, translator.Translate(adaptor.TextDelta{
			MessageID: "message", Text: "redacted",
		})...)
		projected = append(projected, translator.CloseRun(terminalError)...)
		wantTerminal := events.EventTypeRunFinished
		if terminalError != nil {
			wantTerminal = events.EventTypeRunError
		}
		if len(projected) != 2 || projected[0].Type() != events.EventTypeRunStarted ||
			projected[1].Type() != wantTerminal {
			t.Fatalf("run boundaries were removed: %v", projected)
		}
		for _, kind := range hooked {
			if kind == events.EventTypeRunStarted || kind == wantTerminal {
				t.Fatalf("boundary reached hook: %v", hooked)
			}
		}
		if err := VerifySequence(projected); err != nil {
			t.Fatal(err)
		}
	}
}
