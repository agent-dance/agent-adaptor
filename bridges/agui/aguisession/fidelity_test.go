package aguisession

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	events "github.com/ag-ui-protocol/ag-ui/sdks/community/go/pkg/core/events"
	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/bridges/agui"
	"github.com/agent-dance/agent-adaptor/hosttools/sessionrecorder"
)

func TestSubmittedMessageCollisionBeforeRecordingOrStream(t *testing.T) {
	ctx := context.Background()
	calls := 0
	divergences := 0
	rec := recorder(t)
	s := New(fakeRunner{func(context.Context, string) adaptor.Stream {
		calls++
		return script(nil, nil, adaptor.TextDelta{MessageID: "answer", Text: "answer"},
			adaptor.ToolCall{ID: "tc", Name: "shell", Phase: adaptor.PhaseStart},
			adaptor.ToolResult{ID: "tc", Result: map[string]any{"output": "ok"}},
			adaptor.Thinking{MessageID: "reason", Text: "reason"},
			adaptor.SubagentUpdate{Agent: "helper", Kind: adaptor.SubagentStarted})
	}}, Options{Recorder: rec, Input: agui.InputOptions{HistoryFallback: true}, ResolveDivergence: func(context.Context, *agui.RunAgentInput, string) (string, error) {
		divergences++
		return ThreadKey("fresh-branch"), nil
	}})
	if w := request(s, "t", "first", []Message{user("u1", "go")}); w.Code != 200 {
		t.Fatal(w.Body)
	}
	prior, err := s.Messages(ctx, ThreadKey("t"))
	if err != nil {
		t.Fatal(err)
	}
	before, err := rec.Since(ctx, RecorderKey(ThreadKey("t")), 0)
	if err != nil {
		t.Fatal(err)
	}
	// Test all recorded IDs, including optional reasoning/activity omitted from
	// a round-trip client history. Neither divergence nor fresh-key policy waives it.
	for i, old := range prior {
		t.Run(fmt.Sprint(old["role"]), func(t *testing.T) {
			ms := []Message{user(str(old, "id"), "second")}
			w := request(s, "t", fmt.Sprintf("collision-%d", i), ms)
			if w.Code != 409 || !strings.Contains(w.Body.String(), "agui.duplicate_message") || strings.Contains(w.Body.String(), "RUN_STARTED") {
				t.Fatalf("%d %s", w.Code, w.Body)
			}
			after, err := rec.Since(ctx, RecorderKey(ThreadKey("t")), 0)
			if err != nil {
				t.Fatal(err)
			}
			if calls != 1 || divergences != 0 || !reflect.DeepEqual(before, after) {
				t.Fatal("collision executed or recorded")
			}
		})
	}
	w := request(s, "fresh", "intra-request", []Message{user("same", "first"), user("same", "second")})
	if w.Code != 409 || !strings.Contains(w.Body.String(), "agui.duplicate_message") || calls != 1 {
		t.Fatalf("duplicate request executed: %d %s", w.Code, w.Body)
	}
	if rows, err := rec.Since(ctx, RecorderKey(ThreadKey("fresh")), 0); err != nil || len(rows) != 0 {
		t.Fatalf("duplicate request recorded: %v %v", rows, err)
	}
	// This is the official idempotent seed behavior; admission, not Fold, fixes collisions.
	p := newProjector(nil)
	seed := p.apply(before[0])[0]
	once := Fold(nil, seed)
	if twice := Fold(once, seed); len(twice) != 1 {
		t.Fatalf("seed no longer idempotent: %v", twice)
	}
}

func TestOpaqueNumbersThroughRecordingWireAndReopenedHistory(t *testing.T) {
	ctx := context.Background()
	for _, mode := range []string{"exact", "large"} {
		t.Run(mode, func(t *testing.T) {
			number := json.Number("9007199254740993")
			if mode == "large" {
				number = json.Number("1e1000")
			}
			dir := t.TempDir()
			open := func() sessionrecorder.EventRecorder {
				b, e := sessionrecorder.NewJSONLEventBackend(dir)
				if e != nil {
					t.Fatal(e)
				}
				r := sessionrecorder.NewEventRecorder(b)
				t.Cleanup(func() { _ = r.Close() })
				return r
			}
			rec := open()
			opts := Options{Recorder: rec, Events: []agui.EventTranslatorOption{agui.WithProjectionHook(func(e events.Event) []events.Event {
				if e.GetBaseEvent().Metadata == nil {
					e.GetBaseEvent().Metadata = map[string]any{}
				}
				e.GetBaseEvent().Metadata["hook"] = map[string]any{mode: number}
				return []events.Event{e}
			})}}
			s := New(fakeRunner{func(context.Context, string) adaptor.Stream {
				return script(nil, nil,
					adaptor.TextDelta{MessageID: "a", Text: "answer"},
					adaptor.ToolCall{ID: "tc", Name: "shell", Phase: adaptor.PhaseStart},
					adaptor.ToolResult{ID: "tc", Result: map[string]any{"output": "fractional", "exitCode": json.Number("1.00000000000000000001")}},
					adaptor.SubagentUpdate{Agent: "helper", Kind: adaptor.SubagentStarted, Data: map[string]any{"value": number}})
			}}, opts)
			submitted := user("u", "go")
			submitted["metadata"] = map[string]any{mode: number}
			w := request(s, "t", "r", []Message{submitted})
			if w.Code != 200 || !strings.Contains(w.Body.String(), "RUN_FINISHED") {
				t.Fatalf("%d %s", w.Code, w.Body)
			}
			live := []map[string]any{}
			for _, line := range strings.Split(w.Body.String(), "\n") {
				if strings.HasPrefix(line, "data: ") {
					var ev map[string]any
					if err := decodeOpaque([]byte(line[6:]), &ev); err != nil {
						t.Fatal(err)
					}
					live = append(live, ev)
				}
			}
			check := func(label string, value any) {
				t.Helper()
				raw, err := json.Marshal(value)
				if err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(string(raw), fmt.Sprintf(`"%s":%s`, mode, number)) {
					t.Fatalf("%s lost number: %s", label, raw)
				}
			}
			check("live seed", live[0]["input"])
			check("live hook", live[1]["metadata"])
			ordered := func(session *Session) []map[string]any {
				t.Helper()
				records, err := session.Events(ctx, ThreadKey("t"), 0)
				if err != nil {
					t.Fatal(err)
				}
				var out []map[string]any
				for _, record := range records {
					for _, event := range record.Events {
						raw, err := event.ToJSON()
						if err != nil {
							t.Fatal(err)
						}
						var decoded map[string]any
						if err := decodeOpaque(raw, &decoded); err != nil {
							t.Fatal(err)
						}
						out = append(out, decoded)
					}
				}
				return out
			}
			if !reflect.DeepEqual(live, ordered(s)) {
				t.Fatal("ordered events differ from lossless live wire")
			}
			for _, ev := range live {
				if ev["type"] == "TOOL_CALL_RESULT" {
					m := ev["metadata"].(map[string]any)["agentAdaptor"].(map[string]any)
					if _, ok := m["exitCode"]; ok || strings.Contains(fmt.Sprint(ev["content"]), "[exit=") {
						t.Fatalf("fractional exit coerced: %v", ev)
					}
				}
			}
			before, err := s.Messages(ctx, ThreadKey("t"))
			if err != nil {
				t.Fatal(err)
			}
			if len(before) < 3 {
				t.Fatalf("Fold dropped hook events: %v", before)
			}
			check("messages seed", before[0]["metadata"])
			check("messages hook", before[1]["metadata"])
			if err := rec.Close(); err != nil {
				t.Fatal(err)
			}
			reopened := open()
			defer reopened.Close()
			opts.Recorder = reopened
			restored := New(fakeRunner{}, opts)
			after, err := restored.Messages(ctx, ThreadKey("t"))
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before, after) {
				t.Fatalf("reopened changed history: %v != %v", before, after)
			}
			if !reflect.DeepEqual(live, ordered(restored)) {
				t.Fatal("reopened ordered events differ from lossless live wire")
			}
			// Exercise both public HTTP views, not only internal projector values.
			for _, view := range []string{"events", "messages"} {
				out := httptest.NewRecorder()
				req := httptest.NewRequest("GET", "/?thread_id=t", nil)
				if view == "events" {
					restored.EventsHandler().ServeHTTP(out, req)
				} else {
					restored.MessagesHandler().ServeHTTP(out, req)
				}
				if out.Code != 200 {
					t.Fatal(out.Body)
				}
				check(view, json.RawMessage(out.Body.Bytes()))
			}
		})
	}
}

func TestFractionalExitCodeSurvivesFreezeWithoutCoercion(t *testing.T) {
	s := New(fakeRunner{func(context.Context, string) adaptor.Stream {
		return script(nil, nil, adaptor.ToolCall{ID: "tc", Name: "shell", Phase: adaptor.PhaseStart}, adaptor.ToolResult{ID: "tc", Result: map[string]any{"output": "fractional", "exitCode": json.Number("1.00000000000000000001")}})
	}}, Options{Recorder: recorder(t)})
	w := request(s, "t", "r", []Message{user("u", "go")})
	if w.Code != 200 || strings.Contains(w.Body.String(), `"exitCode":1`) || strings.Contains(w.Body.String(), "[exit=1]") {
		t.Fatalf("exit code rounded: %d %s", w.Code, w.Body)
	}
	found := false
	for _, event := range wireEvents(t, w) {
		if event["type"] == "TOOL_CALL_RESULT" {
			found = true
			status := event["metadata"].(map[string]any)["agentAdaptor"].(map[string]any)
			if status["status"] != "unknown" {
				t.Fatal(status)
			}
		}
	}
	if !found {
		t.Fatal("missing tool result")
	}
}

func TestFoldKeepsHugeHookMetadataAndActivityNumbers(t *testing.T) {
	ms := Fold(nil, jsonEvent{raw: json.RawMessage(`{"type":"TEXT_MESSAGE_START","messageId":"a","role":"assistant","metadata":{"huge":1e1000}}`)})
	ms = Fold(ms, jsonEvent{raw: json.RawMessage(`{"type":"TEXT_MESSAGE_CONTENT","messageId":"a","delta":"answer","metadata":{"exact":9007199254740993}}`)})
	if len(ms) != 1 || ms[0]["content"] != "answer" {
		t.Fatalf("Fold dropped valid hook event: %v", ms)
	}
	metadata := ms[0]["metadata"].(map[string]any)
	if metadata["huge"] != json.Number("1e1000") || metadata["exact"] != json.Number("9007199254740993") {
		t.Fatal(metadata)
	}
	// Copy and test must retain numeric values even inside activity documents.
	ms = Fold(ms, jsonEvent{raw: json.RawMessage(`{"type":"ACTIVITY_SNAPSHOT","messageId":"card","activityType":"card","content":{"n":{"large":1e1000,"exact":9007199254740993}}}`)})
	ms = Fold(ms, jsonEvent{raw: json.RawMessage(`{"type":"ACTIVITY_DELTA","messageId":"card","activityType":"card","patch":[{"op":"copy","from":"/n","path":"/copy"},{"op":"test","path":"/copy","value":{"large":10e999,"exact":9007199254740993.0}},{"op":"add","path":"/passed","value":true}]}`)})
	content := ms[1]["content"].(map[string]any)
	if content["passed"] != true || !reflect.DeepEqual(content["n"], content["copy"]) {
		t.Fatal(content)
	}
}
