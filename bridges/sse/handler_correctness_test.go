package sse

import (
	"context"
	"errors"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	adaptor "github.com/agent-dance/agent-adaptor"
)

type terminalTestStream struct {
	events chan adaptor.Event
	result *adaptor.Result
	err    error
}

func (s *terminalTestStream) Events() <-chan adaptor.Event     { return s.events }
func (s *terminalTestStream) Result() (*adaptor.Result, error) { return s.result, s.err }
func (s *terminalTestStream) RunID() string                    { return "run-authoritative" }
func (s *terminalTestStream) Cancel()                          {}

func TestDecodeRequestPreservesRawThreadKeyAndSeparatesAGUITuple(t *testing.T) {
	rawKey := "tenant/a/b:c\x00d"
	rawReq := httptest.NewRequest("GET", "/?prompt=hello&sessionKey="+url.QueryEscape(rawKey), nil)
	_, gotRaw, err := decodeRequest(rawReq, Options{Protocol: Raw})
	if err != nil {
		t.Fatal(err)
	}
	if gotRaw != rawKey {
		t.Fatalf("raw thread key = %q, want exact %q", gotRaw, rawKey)
	}

	aguiReq := httptest.NewRequest("POST", "/", strings.NewReader(`{
		"threadId":"a/b:c","runId":"run","messages":[{"role":"user","content":"hello"}]
	}`))
	_, gotAGUI, err := decodeRequest(aguiReq, Options{Protocol: AGUI})
	if err != nil {
		t.Fatal(err)
	}
	if gotAGUI == "agui/a/b:c" || gotAGUI == gotRaw {
		t.Fatalf("AG-UI tuple used a collision-prone/raw encoding: %q", gotAGUI)
	}
}

func TestStreamRawUsesEventSequenceAndResultAsTerminalAuthority(t *testing.T) {
	events := make(chan adaptor.Event, 2)
	events <- adaptor.WithEventMeta(adaptor.TextDelta{MessageID: "m", Text: "x"}, adaptor.EventMeta{
		RunID: "run", Sequence: 41, Time: time.Unix(1, 0),
	})
	// The informational event says success. Result() must still win.
	events <- adaptor.WithEventMeta(adaptor.RunFinished{RunID: "run"}, adaptor.EventMeta{RunID: "run", Sequence: 42})
	close(events)
	stream := &terminalTestStream{events: events, err: &adaptor.RunError{
		Reason: adaptor.ReasonApprovalDenied, Message: "denied", Details: map[string]any{"request_id": "r"}, Result: &adaptor.Result{RunID: "run"},
	}}
	recorder := httptest.NewRecorder()
	if err := streamRaw(context.Background(), &sync.Mutex{}, recorder, stream, 0, false); err != nil {
		t.Fatal(err)
	}
	body := recorder.Body.String()
	if !strings.Contains(body, "id: 41\n") {
		t.Fatalf("event metadata sequence was not used as SSE id:\n%s", body)
	}
	if strings.Count(body, "event: run.error\n") != 1 || strings.Contains(body, "event: run.finished\n") {
		t.Fatalf("terminal must be one authoritative run.error:\n%s", body)
	}
	if !strings.Contains(body, `"code":"approval_denied"`) || !strings.Contains(body, `"request_id":"r"`) {
		t.Fatalf("structured business failure lost fidelity:\n%s", body)
	}
}

func TestStreamRawSeedsMetadataFreeCursorFromLastEventID(t *testing.T) {
	events := make(chan adaptor.Event, 1)
	events <- adaptor.TextDelta{MessageID: "m", Text: "x"}
	close(events)
	stream := &terminalTestStream{events: events, result: &adaptor.Result{RunID: "run"}}
	recorder := httptest.NewRecorder()
	if err := streamRaw(context.Background(), &sync.Mutex{}, recorder, stream, 80, true); err != nil {
		t.Fatal(err)
	}
	body := recorder.Body.String()
	if !strings.Contains(body, "id: 81\n") || !strings.Contains(body, "id: 82\n") {
		t.Fatalf("Last-Event-ID did not resume the fallback cursor:\n%s", body)
	}
	if got, ok := parseLastEventID(" 80 "); !ok || got != 80 {
		t.Fatalf("parseLastEventID = (%d,%v)", got, ok)
	}
	if _, ok := parseLastEventID("invalid"); ok {
		t.Fatal("invalid Last-Event-ID must not become a cursor")
	}
}

func TestRawFramePreservesApprovalDropAndEnvelope(t *testing.T) {
	deadline := time.Unix(123, 0).UTC()
	approval := adaptor.WithEventMeta(&adaptor.ApprovalRequest{
		ID: "approval", Kind: adaptor.ApprovalQuestion, Source: "driver", Title: "pick",
		Details: map[string]any{"secret": "value"}, Choices: []adaptor.Choice{{Key: "a", Label: "A"}},
		ToolCallID: "tool", Deadline: deadline, Attempt: 2,
	}, adaptor.EventMeta{RunID: "run", ThreadKey: "opaque", Sequence: 7})
	name, raw := rawFrameFor(approval)
	if name != "decision.request" {
		t.Fatalf("name = %q", name)
	}
	body := raw.(map[string]any)
	meta := body["meta"].(map[string]any)
	if meta["thread_key"] != "opaque" || meta["sequence"] != uint64(7) || body["retry_attempt"] != 2 {
		t.Fatalf("approval frame lost fields: %#v", body)
	}

	dropped := adaptor.WithEventMeta(adaptor.Dropped{
		Count: 3, ByKind: map[string]int{"text.content": 2, "thinking.content": 1},
		FirstSequence: 8, LastSequence: 10, Reason: "backpressure", Source: "sdk", Details: map[string]any{"buffer": 1},
	}, adaptor.EventMeta{Sequence: 11})
	_, raw = rawFrameFor(dropped)
	body = raw.(map[string]any)
	if body["first_sequence"] != uint64(8) || body["last_sequence"] != uint64(10) || body["reason"] != "backpressure" {
		t.Fatalf("dropped frame lost fields: %#v", body)
	}
}

func TestRawTerminalInfrastructureFailureIsStructured(t *testing.T) {
	name, body := rawTerminalFrame("run", nil, nil, errors.New("transport broke"))
	if name != "run.error" || body["code"] != "run.error" || body["message"] != "transport broke" {
		t.Fatalf("terminal = %s %#v", name, body)
	}
}
