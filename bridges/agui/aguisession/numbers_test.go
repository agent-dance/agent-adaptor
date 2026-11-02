package aguisession

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	adaptor "github.com/agent-dance/agent-adaptor/v2"
	"github.com/agent-dance/agent-adaptor/v2/hosttools/sessionrecorder"
)

func TestRecordedApprovalNumericProjections(t *testing.T) {
	ctx := context.Background()
	for _, mode := range []string{"memory-freeze", "jsonl-freeze", "old-jsonl"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			open := func() sessionrecorder.EventRecorder {
				t.Helper()
				var backend sessionrecorder.EventBackend = sessionrecorder.NewMemoryEventBackend()
				if mode != "memory-freeze" {
					var err error
					backend, err = sessionrecorder.NewJSONLEventBackend(dir)
					if err != nil {
						t.Fatal(err)
					}
				}
				r := sessionrecorder.NewEventRecorder(backend)
				t.Cleanup(func() { _ = r.Close() })
				return r
			}
			if mode == "old-jsonl" {
				// Literal pre-UseNumber JSONL: ordinary JSON numbers, not values
				// encoded with today's EventRecord implementation. The old float64
				// codec wrote attempt=2 and latency nanoseconds in this same form.
				old := `{"host_seq":1,"recorded_at":"2026-07-01T12:00:00Z","kind":"notice","event":{"Kind":"agentAdaptor.agui.boundary","Data":{"threadId":"t","runId":"r","projectionVersion":"agui-session/2"}}}
{"host_seq":2,"recorded_at":"2026-07-01T12:00:00Z","kind":"notice","event":{"Kind":"approval.requested","Text":"Allow?","Data":{"request_id":"q","kind":"permission","attempt":2}}}
{"host_seq":3,"recorded_at":"2026-07-01T12:00:00Z","kind":"notice","event":{"Kind":"approval.resolved","Data":{"request_id":"q","kind":"permission","attempt":2,"result":"approved","latency":1234000000}}}
{"host_seq":4,"recorded_at":"2026-07-01T12:00:00Z","kind":"tool.call","event":{"ID":"tc","Name":"shell","Phase":"start"}}
{"host_seq":5,"recorded_at":"2026-07-01T12:00:00Z","kind":"tool.result","event":{"ID":"tc","Result":{"output":"failed","exitCode":7}}}
{"host_seq":6,"recorded_at":"2026-07-01T12:00:00Z","kind":"notice","event":{"Kind":"agentAdaptor.agui.terminal","Data":{}}}
`
				if err := os.WriteFile(filepath.Join(dir, RecorderKey(ThreadKey("t"))+".jsonl"), []byte(old), 0600); err != nil {
					t.Fatal(err)
				}
			}
			rec := open()
			s := New(fakeRunner{func(context.Context, string) adaptor.Stream {
				if mode == "old-jsonl" {
					t.Fatal("replay executed the runner")
				}
				return script(nil, nil,
					adaptor.Notice{Kind: adaptor.NoticeApprovalRequested, Text: "Allow?", Data: map[string]any{"request_id": "q", "kind": "permission", "attempt": 2}},
					adaptor.Notice{Kind: adaptor.NoticeApprovalResolved, Data: map[string]any{"request_id": "q", "kind": "permission", "attempt": 2, "result": "approved", "latency": 1234 * time.Millisecond}},
					adaptor.ToolCall{ID: "tc", Name: "shell", Phase: adaptor.PhaseStart},
					adaptor.ToolResult{ID: "tc", Result: map[string]any{"output": "failed", "exitCode": 7}})
			}}, Options{Recorder: rec})
			if mode != "old-jsonl" {
				live := request(s, "t", "r", []Message{user("u", "go")})
				if live.Code != 200 || !strings.Contains(live.Body.String(), "RUN_FINISHED") {
					t.Fatalf("%d %s", live.Code, live.Body)
				}
				assertApprovalNumericEvents(t, wireEvents(t, live))
			}
			check := func(s *Session) ([]map[string]any, []Message) {
				t.Helper()
				// Confirm these paths really exercise the inner UseNumber decoder.
				rows, err := s.opts.Recorder.Since(ctx, RecorderKey(ThreadKey("t")), 0)
				if err != nil {
					t.Fatal(err)
				}
				notices := 0
				for _, row := range rows {
					if n, ok := row.Event.(adaptor.Notice); ok && (n.Kind == adaptor.NoticeApprovalRequested || n.Kind == adaptor.NoticeApprovalResolved) {
						notices++
						if n.Data["attempt"] != json.Number("2") {
							t.Fatalf("attempt = %T %v; wanted a decoded JSON number", n.Data["attempt"], n.Data["attempt"])
						}
					}
				}
				if notices != 2 {
					t.Fatalf("approval Notices = %d", notices)
				}
				records, err := s.Events(ctx, ThreadKey("t"), 0)
				if err != nil {
					t.Fatal(err)
				}
				ordered := flattened(t, records)
				assertApprovalNumericEvents(t, ordered)
				// Replayed SSE is a live wire boundary too, including old JSONL.
				wire := httptest.NewRecorder()
				s.ObserveHandler().ServeHTTP(wire, httptest.NewRequest("GET", "/?thread_id=t&after=0", nil))
				if wire.Code != 200 {
					t.Fatal(wire.Body)
				}
				assertApprovalNumericEvents(t, wireEvents(t, wire))
				messages, err := s.Messages(ctx, ThreadKey("t"))
				if err != nil {
					t.Fatal(err)
				}
				var args, results, exits int
				for _, m := range messages {
					if calls, ok := m["toolCalls"].([]any); ok {
						for _, call := range calls {
							f := call.(map[string]any)["function"].(map[string]any)
							if str(f, "arguments") != "" {
								assertApprovalNumericPayload(t, str(f, "arguments"), false)
								args++
							}
						}
					}
					if m["role"] == "tool" {
						if strings.HasPrefix(str(m, "content"), "{") {
							assertApprovalNumericPayload(t, str(m, "content"), true)
							results++
						} else {
							raw, _ := json.Marshal(m)
							if !strings.Contains(string(raw), `"exitCode":7`) || !strings.Contains(str(m, "content"), "[exit=7]") {
								t.Fatalf("Message[] exit code lost: %s", raw)
							}
							exits++
						}
					}
				}
				if args != 1 || results != 1 || exits != 1 {
					t.Fatalf("Message[] numeric projections: args=%d results=%d exits=%d", args, results, exits)
				}
				return ordered, messages
			}
			beforeEvents, beforeMessages := check(s)
			if mode != "memory-freeze" {
				if err := rec.Close(); err != nil {
					t.Fatal(err)
				}
				afterEvents, afterMessages := check(New(fakeRunner{}, Options{Recorder: open()}))
				if !reflect.DeepEqual(beforeEvents, afterEvents) || !reflect.DeepEqual(beforeMessages, afterMessages) {
					t.Fatal("reopening changed numeric projections")
				}
			}
		})
	}
}

func assertApprovalNumericEvents(t *testing.T, es []map[string]any) {
	t.Helper()
	args, results, exits := 0, 0, 0
	for _, ev := range es {
		switch ev["type"] {
		case "TOOL_CALL_ARGS":
			assertApprovalNumericPayload(t, str(ev, "delta"), false)
			args++
		case "TOOL_CALL_RESULT":
			if strings.HasPrefix(str(ev, "content"), "{") {
				assertApprovalNumericPayload(t, str(ev, "content"), true)
				results++
			} else {
				metadata := ev["metadata"].(map[string]any)["agentAdaptor"].(map[string]any)
				if metadata["exitCode"] != float64(7) || metadata["status"] != "failed" || !strings.Contains(str(ev, "content"), "[exit=7]") {
					t.Fatalf("wire exit code lost: %v", ev)
				}
				exits++
			}
		}
	}
	if args != 1 || results != 1 || exits != 1 {
		t.Fatalf("wire numeric projections: args=%d results=%d exits=%d", args, results, exits)
	}
}

func assertApprovalNumericPayload(t *testing.T, raw string, resolved bool) {
	t.Helper()
	var payload map[string]any
	if err := decodeOpaque([]byte(raw), &payload); err != nil {
		t.Fatal(err)
	}
	if payload["retry_attempt"] != json.Number("2") || resolved && payload["latency_ms"] != json.Number("1234") {
		t.Fatalf("approval numeric projection changed: %s", raw)
	}
}
