package a2a

import (
	"encoding/json"
	"errors"
	"fmt"
	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/todo"
	"os"
	"strings"
	"testing"
	"time"
)

func TestAlignmentFrozenWireFixtures(t *testing.T) {
	for _, name := range []string{"capability-start", "capability-terminal-zero-duration", "todo-clear", "tool-parent"} {
		t.Run(name, func(t *testing.T) {
			raw, err := os.ReadFile("testdata/alignment/" + name + ".json")
			if err != nil {
				t.Fatal(err)
			}
			event, matched, err := DecodeAdapterEventV1(json.RawMessage(raw))
			if err != nil || !matched || event == nil {
				t.Fatalf("decode matched=%v event=%T err=%v", matched, event, err)
			}
			switch e := event.(type) {
			case adaptor.ToolCall:
				if e.ScopeID != "child-scope" || e.ParentToolCallID != "parent-p" {
					t.Fatalf("parent lost: %#v", e)
				}
			case adaptor.TodoUpdated:
				if e.Snapshot.Items == nil || len(e.Snapshot.Items) != 0 || e.Snapshot.Revision != 2 {
					t.Fatalf("clear lost: %#v", e)
				}
			case adaptor.CapabilityInvocation:
				if e.Invocation.InvocationID != "call-1" {
					t.Fatalf("invocation lost: %#v", e)
				}
			default:
				t.Fatalf("wrong event %T", event)
			}
		})
	}
}

func alignmentWire(t *testing.T, name string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile("testdata/alignment/" + name + ".json")
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}
func alignmentWireEvent(t *testing.T, name string) AdapterStreamEventV1 {
	t.Helper()
	raw, _ := json.Marshal(alignmentWire(t, name))
	var out AdapterStreamEnvelopeV1
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out.Event
}
func alignmentEvent(t *testing.T, name string) adaptor.Event {
	t.Helper()
	e, m, err := DecodeAdapterEventV1(alignmentWire(t, name))
	if !m || err != nil {
		t.Fatalf("decode %v %v", m, err)
	}
	return e
}

func TestAlignmentClosedObservationWire(t *testing.T) {
	cases := []struct {
		name, fixture string
		change        func(map[string]any)
	}{
		{"unknown_envelope", "capability-start", func(m map[string]any) { m["secret"] = "secret-payload" }},
		{"unknown_event", "capability-start", func(m map[string]any) {
			m["event"].(map[string]any)["raw"] = map[string]any{"secret": "secret-payload"}
		}},
		{"null_raw", "capability-start", func(m map[string]any) { m["event"].(map[string]any)["raw"] = nil }},
		{"wrong_capability_kind", "capability-start", func(m map[string]any) { m["event"].(map[string]any)["kind"] = "text.content" }},
		{"missing_meta", "capability-start", func(m map[string]any) { delete(m["event"].(map[string]any), "meta") }},
		{"flat_conflict", "capability-start", func(m map[string]any) { m["event"].(map[string]any)["sequence"] = 8 }},
		{"flat_zero", "capability-start", func(m map[string]any) { m["event"].(map[string]any)["sequence"] = 0 }},
		{"thread_conflict", "capability-start", func(m map[string]any) { m["event"].(map[string]any)["thread_id"] = "provider-thread" }},
		{"outer_parent", "capability-start", func(m map[string]any) { m["event"].(map[string]any)["scope_id"] = "" }},
	}
	capCases := map[string]any{"kind": "free-form", "key": "bad\u0000key", "operation": "", "phase": "unknown", "evidence": "relayed", "source": "host", "occurred_at": "0001-01-01T00:00:00Z", "duration_ns": -1, "error_code": "private-error", "scope_id": strings.Repeat("a", 2049), "parent_scope_id": "unpaired"}
	for field, value := range capCases {
		field, value := field, value
		cases = append(cases, struct {
			name, fixture string
			change        func(map[string]any)
		}{"cap_" + field, "capability-start", func(m map[string]any) { m["event"].(map[string]any)["capability"].(map[string]any)[field] = value }})
	}
	for _, field := range []string{"invocation_id", "kind", "key", "operation", "phase", "evidence", "source", "occurred_at"} {
		field := field
		cases = append(cases, struct {
			name, fixture string
			change        func(map[string]any)
		}{"null_" + field, "capability-start", func(m map[string]any) { m["event"].(map[string]any)["capability"].(map[string]any)[field] = nil }})
	}
	cases = append(cases,
		struct {
			name, fixture string
			change        func(map[string]any)
		}{"unknown_cap_field", "capability-start", func(m map[string]any) { m["event"].(map[string]any)["capability"].(map[string]any)["args"] = "secret" }},
		struct {
			name, fixture string
			change        func(map[string]any)
		}{"null_items", "todo-clear", func(m map[string]any) { m["event"].(map[string]any)["todo"].(map[string]any)["items"] = nil }},
		struct {
			name, fixture string
			change        func(map[string]any)
		}{"missing_items", "todo-clear", func(m map[string]any) { delete(m["event"].(map[string]any)["todo"].(map[string]any), "items") }},
		struct {
			name, fixture string
			change        func(map[string]any)
		}{"invalid_utf8_map", "capability-start", func(m map[string]any) {
			m["event"].(map[string]any)["capability"].(map[string]any)["key"] = string([]byte{255})
		}},
		struct {
			name, fixture string
			change        func(map[string]any)
		}{"invalid_utf8_map_key", "capability-start", func(m map[string]any) { m["event"].(map[string]any)[string([]byte{255})] = "x" }},
		struct {
			name, fixture string
			change        func(map[string]any)
		}{"unsafe_meta_integer", "capability-start", func(m map[string]any) {
			m["event"].(map[string]any)["meta"].(map[string]any)["sequence"] = uint64(9007199254740992)
		}},
		struct {
			name, fixture string
			change        func(map[string]any)
		}{"unsafe_revision", "todo-clear", func(m map[string]any) {
			m["event"].(map[string]any)["todo"].(map[string]any)["revision"] = uint64(9007199254740992)
		}},
	)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := alignmentWire(t, tc.fixture)
			tc.change(m)
			e, matched, err := DecodeAdapterEventV1(m)
			if !matched || err == nil || e != nil {
				t.Fatalf("unsafe decode matched=%v event=%T err=%v", matched, e, err)
			}
		})
	}
}

func TestAlignmentRawJSONLexicalBoundaries(t *testing.T) {
	b, _ := json.Marshal(alignmentWire(t, "capability-start"))
	valid := string(b)
	cases := map[string]string{
		"duplicate_key":       strings.Replace(valid, `"kind":"mcp"`, `"kind":"mcp","kind":"skill"`, 1),
		"escaped_duplicate":   strings.Replace(valid, `"kind":"mcp"`, `"kind":"mcp","k\u0069nd":"mcp"`, 1),
		"trailing_value":      valid + ` {"secret":"x"}`,
		"high_surrogate":      strings.Replace(valid, `"knowledge"`, `"\ud800"`, 1),
		"low_surrogate":       strings.Replace(valid, `"knowledge"`, `"\udc00"`, 1),
		"bad_utf8":            strings.Replace(valid, `knowledge`, string([]byte{255}), 1),
		"wrong_case":          strings.Replace(valid, `"key"`, `"Key"`, 1),
		"fractional_duration": strings.Replace(valid, `"phase":"started"`, `"phase":"completed","duration_ns":0.5`, 1),
		"overflow_duration":   strings.Replace(valid, `"phase":"started"`, `"phase":"completed","duration_ns":9223372036854775808`, 1),
	}
	cases["truncated"] = `{"schema":"adapter.stream.v1","event":{"kind":"capability.invocation",`
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			e, m, err := DecodeAdapterEventV1(json.RawMessage(raw))
			if !m || e != nil || err == nil {
				t.Fatalf("decode=%T,%v,%v", e, m, err)
			}
		})
	}
	valid = strings.Replace(valid, `"knowledge"`, `"\ud83d\ude00"`, 1)
	if _, m, err := DecodeAdapterEventV1(json.RawMessage(valid)); !m || err != nil {
		t.Fatalf("paired surrogate rejected: %v", err)
	}
	if _, m, err := DecodeAdapterEventV1(json.RawMessage(`{"schema":"another.v1","event":{"kind":"unknown"}}`)); m || err != nil {
		t.Fatalf("foreign schema=%v,%v", m, err)
	}
	if e, m, err := DecodeAdapterEventV1(json.RawMessage(`{"schema":"adapter.stream.v1","event":{"kind":"new.secret-kind"}}`)); !m || err == nil || e != nil || strings.Contains(err.Error(), "secret") {
		t.Fatalf("unknown kind=%T,%v,%v", e, m, err)
	}
	// A new reader still accepts a published old flat-only event.
	if e, m, err := DecodeAdapterEventV1(json.RawMessage(`{"schema":"adapter.stream.v1","event":{"kind":"text.content","run_id":"old","sequence":1,"delta":"hello"}}`)); !m || err != nil || e.(adaptor.TextDelta).Text != "hello" {
		t.Fatalf("old event=%#v,%v", e, err)
	}
}

func TestAlignmentObservationExposureAndCopies(t *testing.T) {
	capEvent := alignmentEvent(t, "capability-terminal-zero-duration").(adaptor.CapabilityInvocation)
	snap := todo.Snapshot{Items: []todo.Item{{ID: "91", Content: "do work\n\tAuthorization: Bearer hidden-secret", Status: todo.InProgress, SyntheticID: true}}, Source: todo.ToolResult, Revision: 1, OccurredAt: capEvent.Invocation.OccurredAt, ScopeID: "child", ParentToolCallID: "parent"}
	meta := capEvent.Meta()
	meta.Source = &adaptor.EventSourceMeta{RunID: "remote", Sequence: 89, ScopeID: "inner", InvocationID: "call", DelegationID: "d", Upstream: &adaptor.EventSourceMeta{RunID: "grandparent", Sequence: 99}}
	capEvent = adaptor.WithEventMeta(capEvent, meta).(adaptor.CapabilityInvocation)
	todoEvent := adaptor.WithEventMeta(adaptor.TodoUpdated{Snapshot: snap}, meta)
	for _, exposure := range []ExposurePolicy{{}, {IncludeToolCalls: true}, {IncludeReasoning: true, IncludeHITL: true, Diagnostics: DiagnosticsPolicy{IncludeMetadata: true, IncludeTranscript: true, IncludeRawStreams: true}}} {
		translator := newStreamTranslator(testTaskInfo{}, exposure)
		if len(translator.Translate(capEvent)) != 0 || len(translator.Translate(todoEvent)) != 0 {
			t.Fatal("default exposure leaked observations")
		}
	}
	for _, includeSource := range []bool{false, true} {
		t.Run(fmt.Sprint(includeSource), func(t *testing.T) {
			translator := newStreamTranslator(testTaskInfo{}, ExposurePolicy{IncludeCapabilityInvocations: true, IncludeTodos: true, Diagnostics: DiagnosticsPolicy{IncludeMetadata: includeSource}})
			for _, input := range []adaptor.Event{capEvent, todoEvent} {
				events := translator.Translate(input)
				if len(events) != 1 {
					t.Fatalf("translation=%v", translator.err)
				}
				data := dataValue(t, events[0])
				encoded, _ := json.Marshal(data)
				if strings.Contains(string(encoded), "hidden-secret") {
					t.Fatalf("secret leaked: %s", encoded)
				}
				decoded, _, err := DecodeAdapterEventV1(data)
				if err != nil {
					t.Fatal(err)
				}
				got := decoded.Meta()
				if got.Sequence != meta.Sequence || got.RunID != meta.RunID {
					t.Fatal("current meta replaced by source")
				}
				if includeSource {
					if got.Source == nil || got.Source.Upstream.RunID != "grandparent" || got.Source.DelegationID != "d" {
						t.Fatalf("source lost: %#v", got.Source)
					}
					got.Source.Upstream.RunID = "mutation"
				} else if got.Source != nil {
					t.Fatal("source exposure bypass")
				}
				switch value := decoded.(type) {
				case adaptor.CapabilityInvocation:
					if value.Invocation.Duration == nil || *value.Invocation.Duration != 0 {
						t.Fatal("observed zero lost")
					}
					*value.Invocation.Duration = time.Second
				case adaptor.TodoUpdated:
					if len(value.Snapshot.Items) != 1 || value.Snapshot.Items[0].ID != "91" || !value.Snapshot.Items[0].SyntheticID || value.Snapshot.ParentToolCallID != "parent" {
						t.Fatal("todo identity/order lost")
					}
					value.Snapshot.Items[0].Content = "mutation"
				}
				again, _, err := DecodeAdapterEventV1(data)
				if err != nil {
					t.Fatal(err)
				}
				if includeSource && again.Meta().Source.Upstream.RunID != "grandparent" {
					t.Fatal("decoded source alias")
				}
				if value, ok := again.(adaptor.CapabilityInvocation); ok && *value.Invocation.Duration != 0 {
					t.Fatal("duration alias")
				}
			}
		})
	}
	if *capEvent.Invocation.Duration != 0 || snap.Items[0].Content == "mutation" {
		t.Fatal("input mutated")
	}
}

func TestAlignmentEnvelopeLimitAndSafeDropped(t *testing.T) {
	event := alignmentWireEvent(t, "todo-clear")
	for i := 0; i < 16; i++ {
		event.Todo.Items = append(event.Todo.Items, AdapterTodoItemV1{ID: fmt.Sprint(i), Content: strings.Repeat("x", 4096), Status: "pending"})
	}
	raw, _ := json.Marshal(AdapterStreamEnvelopeV1{Schema: AdapterStreamSchemaV1, Event: event})
	event.Todo.Items[15].Content = strings.Repeat("x", 4096+65536-len(raw))
	raw, _ = json.Marshal(AdapterStreamEnvelopeV1{Schema: AdapterStreamSchemaV1, Event: event})
	if len(raw) != 65536 {
		t.Fatalf("fixture length=%d", len(raw))
	}
	if _, err := encodeAdapterStreamEvent(event); err != nil {
		t.Fatal(err)
	}
	if _, m, err := DecodeAdapterEventV1(json.RawMessage(raw)); !m || err != nil {
		t.Fatalf("65536 rejected %v", err)
	}
	event.Todo.Items[15].Content += "x"
	raw, _ = json.Marshal(AdapterStreamEnvelopeV1{Schema: AdapterStreamSchemaV1, Event: event})
	if len(raw) != 65537 {
		t.Fatalf("fixture length=%d", len(raw))
	}
	if _, err := encodeAdapterStreamEvent(event); !errors.Is(err, errAdapterSize) {
		t.Fatalf("65537 encode=%v", err)
	}
	if _, m, err := DecodeAdapterEventV1(json.RawMessage(raw)); !m || !errors.Is(err, errAdapterSize) {
		t.Fatalf("65537 decode=%v,%v", m, err)
	}
	event.Sequence = event.Meta.Sequence
	event.RunID = event.Meta.RunID
	drop, err := encodeAdapterStreamDrop(event, errAdapterSize)
	if err != nil {
		t.Fatal(err)
	}
	decoded, _, err := DecodeAdapterEventV1(drop)
	if err != nil {
		t.Fatal(err)
	}
	d := decoded.(adaptor.Dropped)
	if d.Count != 1 || d.Reason != "payload_too_large" || d.Source != "a2a" || d.Details["event_kind"] != "todo.updated" || d.Meta().Sequence != event.Sequence {
		t.Fatalf("drop=%#v", d)
	}
	wire := drop["event"].(map[string]any)
	fields := wire["raw"].(map[string]any)
	if len(fields) != 4 || fields["event_kind"] != "todo.updated" {
		t.Fatalf("not closed: %#v", fields)
	}
	drop, err = encodeAdapterStreamDrop(AdapterStreamEventV1{Kind: "secret-unknown"}, errors.New("private-error"))
	if err != nil {
		t.Fatal(err)
	}
	bytes, _ := json.Marshal(drop)
	if strings.Contains(string(bytes), "secret") || strings.Contains(string(bytes), "private-error") {
		t.Fatalf("drop leaked %s", bytes)
	}
}

func TestAlignmentEncodeValidationAndSourceLimits(t *testing.T) {
	cases := map[string]func(*AdapterStreamEventV1){
		"invalid_key":      func(e *AdapterStreamEventV1) { e.Capability.Key = string([]byte{255}) },
		"long_key":         func(e *AdapterStreamEventV1) { e.Capability.Key = strings.Repeat("界", 171) },
		"started_duration": func(e *AdapterStreamEventV1) { n := int64(0); e.Capability.DurationNS = &n },
		"mixed_payload":    func(e *AdapterStreamEventV1) { e.Raw = map[string]any{"secret": "x"} },
		"self_parent_tool": func(e *AdapterStreamEventV1) {
			e.Kind = "tool_call.start"
			e.Capability = nil
			e.ToolCallID = "x"
			e.ParentToolCallID = "x"
		},
		"invalid_time": func(e *AdapterStreamEventV1) { e.Capability.OccurredAt = "2026-99-01T00:00:00Z" },
		"too_large_duration": func(e *AdapterStreamEventV1) {
			e.Capability.Phase = "completed"
			n := int64(9007199254740992)
			e.Capability.DurationNS = &n
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			e := alignmentWireEvent(t, "capability-start")
			mutate(&e)
			if _, err := encodeAdapterStreamEvent(e); err == nil {
				t.Fatal("invalid encoding accepted")
			}
		})
	}
	e := alignmentWireEvent(t, "capability-start")
	tail := &e.Meta.Source
	for i := 0; i < 8; i++ {
		*tail = &AdapterEventSourceMetaV1{RunID: fmt.Sprint(i)}
		tail = &(*tail).Upstream
	}
	if _, err := encodeAdapterStreamEvent(e); err != nil {
		t.Fatal(err)
	}
	*tail = &AdapterEventSourceMetaV1{RunID: "ninth"}
	if _, err := encodeAdapterStreamEvent(e); !errors.Is(err, errAdapterDepth) {
		t.Fatalf("depth accepted: %v", err)
	}
	if _, err := encodeAdapterStreamDrop(e, errAdapterDepth); !errors.Is(err, errAdapterDepth) {
		t.Fatalf("invalid meta was rewritten: %v", err)
	}
	e.Meta.Source.Upstream = e.Meta.Source
	if _, err := encodeAdapterStreamEvent(e); !errors.Is(err, errAdapterDepth) {
		t.Fatalf("cycle accepted: %v", err)
	}
	e.Meta.Source = nil
	e.Meta.ThreadKey = strings.Repeat("x", 65537)
	if _, err := encodeAdapterStreamDrop(e, errAdapterPayload); err == nil {
		t.Fatal("opaque oversized key rewritten")
	}
}

func TestAlignmentTodoAndTimeBoundaries(t *testing.T) {
	makeEvent := func() AdapterStreamEventV1 {
		e := alignmentWireEvent(t, "todo-clear")
		e.Todo.Items = []AdapterTodoItemV1{{ID: "a", Content: "task", Status: "pending"}}
		return e
	}
	cases := map[string]func(*AdapterStreamEventV1){
		"duplicate_id":      func(e *AdapterStreamEventV1) { e.Todo.Items = append(e.Todo.Items, e.Todo.Items[0]) },
		"unknown_status":    func(e *AdapterStreamEventV1) { e.Todo.Items[0].Status = "requested" },
		"unknown_source":    func(e *AdapterStreamEventV1) { e.Todo.Source = "tool_input" },
		"zero_revision":     func(e *AdapterStreamEventV1) { e.Todo.Revision = 0 },
		"unsafe_revision":   func(e *AdapterStreamEventV1) { e.Todo.Revision = 9007199254740992 },
		"empty_content":     func(e *AdapterStreamEventV1) { e.Todo.Items[0].Content = "" },
		"long_content":      func(e *AdapterStreamEventV1) { e.Todo.Items[0].Content = strings.Repeat("x", 4097) },
		"invalid_utf8":      func(e *AdapterStreamEventV1) { e.Todo.Items[0].Content = string([]byte{255}) },
		"forbidden_control": func(e *AdapterStreamEventV1) { e.Todo.Items[0].Content = "task\r" },
		"too_many_items": func(e *AdapterStreamEventV1) {
			for i := 1; i < 129; i++ {
				e.Todo.Items = append(e.Todo.Items, AdapterTodoItemV1{ID: fmt.Sprint(i), Content: "task", Status: "pending"})
			}
		},
		"zero_meta_time":  func(e *AdapterStreamEventV1) { e.Meta.Time = "0001-01-01T00:00:00Z" },
		"zero_occurrence": func(e *AdapterStreamEventV1) { e.Todo.OccurredAt = "0001-01-01T00:00:00Z" },
		"invalid_year":    func(e *AdapterStreamEventV1) { e.Todo.OccurredAt = "0000-01-01T00:00:01Z" },
		"missing_parent":  func(e *AdapterStreamEventV1) { e.Todo.ParentScopeID = "x" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			e := makeEvent()
			mutate(&e)
			if _, err := encodeAdapterStreamEvent(e); err == nil {
				t.Fatal("invalid outgoing todo accepted")
			}
			// Invalid UTF-8 requires the original typed structure: marshaling it here
			// would intentionally destroy the evidence under test.
			if value, m, err := DecodeAdapterEventV1(AdapterStreamEnvelopeV1{Schema: AdapterStreamSchemaV1, Event: e}); !m || value != nil || err == nil {
				t.Fatalf("invalid incoming todo=%T,%v,%v", value, m, err)
			}
		})
	}
	e := makeEvent()
	e.Todo.Items[0].Content = strings.Repeat("界", 1364) + "\n\tAB" // exactly 4096 UTF-8 bytes
	for i := 1; i < 128; i++ {
		e.Todo.Items = append(e.Todo.Items, AdapterTodoItemV1{ID: fmt.Sprint(i), Content: "task", Status: "completed"})
	}
	if _, err := encodeAdapterStreamEvent(e); err != nil {
		t.Fatalf("valid content/items bounds: %v", err)
	}
	e = makeEvent()
	e.Todo.OccurredAt = "2026-09-07T08:00:01+08:00"
	e.Timestamp = "2026-09-07T08:00:01+08:00"
	decoded, m, err := DecodeAdapterEventV1(AdapterStreamEnvelopeV1{Schema: AdapterStreamSchemaV1, Event: e})
	if !m || err != nil {
		t.Fatalf("equivalent time: %v", err)
	}
	translated := newStreamTranslator(testTaskInfo{}, ExposurePolicy{IncludeTodos: true}).Translate(decoded)
	raw, _ := json.Marshal(dataValue(t, translated[0]))
	if strings.Contains(string(raw), "+08:00") {
		t.Fatal("encoder did not normalize UTC")
	}
	for _, field := range []string{"content", "status", "id", "synthetic_id"} {
		t.Run("missing_"+field, func(t *testing.T) {
			raw, _ := json.Marshal(AdapterStreamEnvelopeV1{Schema: AdapterStreamSchemaV1, Event: makeEvent()})
			var object map[string]any
			_ = json.Unmarshal(raw, &object)
			snap := object["event"].(map[string]any)["todo"].(map[string]any)
			delete(snap["items"].([]any)[0].(map[string]any), field)
			if _, m, err := DecodeAdapterEventV1(object); !m || err == nil {
				t.Fatal("required item field accepted missing")
			}
		})
	}
}

func TestAlignmentParentSnapshotsAndSourceCopies(t *testing.T) {
	input := alignmentEvent(t, "tool-parent").(adaptor.ToolCall)
	input.Args = map[string]any{"nested": map[string]any{"values": []any{"original"}}}
	for _, phase := range []adaptor.Phase{adaptor.PhaseStart, adaptor.PhaseContent, adaptor.PhaseEnd} {
		input.Phase = phase
		translator := newStreamTranslator(testTaskInfo{}, ExposurePolicy{IncludeToolCalls: true})
		translated := translator.Translate(input)
		decoded, _, err := DecodeAdapterEventV1(dataValue(t, translated[0]))
		if err != nil {
			t.Fatal(err)
		}
		call := decoded.(adaptor.ToolCall)
		if call.ScopeID != input.ScopeID || call.ParentScopeID != input.ParentScopeID || call.ParentToolCallID != input.ParentToolCallID || call.Phase != phase {
			t.Fatalf("parent/phase lost %#v", call)
		}
		call.Args["nested"].(map[string]any)["values"].([]any)[0] = "mutated"
		if input.Args["nested"].(map[string]any)["values"].([]any)[0] != "original" {
			t.Fatal("tool args alias")
		}
	}
	result := adaptor.WithEventMeta(adaptor.ToolResult{ID: input.ID, ScopeID: input.ScopeID, ParentToolCallID: input.ParentToolCallID, Result: map[string]any{"value": []any{"original"}}}, input.Meta())
	translated := newStreamTranslator(testTaskInfo{}, ExposurePolicy{IncludeToolCalls: true}).Translate(result)
	decoded, _, err := DecodeAdapterEventV1(dataValue(t, translated[0]))
	if err != nil {
		t.Fatal(err)
	}
	if decoded.(adaptor.ToolResult).ParentToolCallID != input.ParentToolCallID {
		t.Fatal("result parent lost")
	}
}
