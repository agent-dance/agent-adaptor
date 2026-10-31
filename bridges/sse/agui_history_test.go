package sse_test

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/bridges/agui"
	"github.com/agent-dance/agent-adaptor/bridges/sse"
	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/memory"
)

func TestAGUIHandlerCheckpointHistory(t *testing.T) {
	fake := &fakeDriver{runFunc: func(_ context.Context, _ driver.Request, sink driver.EventSink) (driver.Response, error) {
		_ = sink.EmitStream(driver.StreamPayload{Kind: driver.StreamTextContent, MessageID: "answer", Delta: "answer"})
		return driver.Response{Output: "answer", Checkpoint: &driver.Checkpoint{State: &driver.SessionState{ResumeID: "checkpoint"}, Valid: true}}, nil
	}}
	agent := adaptor.New(fake, adaptor.WithThreadStore(memory.NewStore()))
	defer agent.Close(context.Background())
	body1 := `{"protocolVersion":"1.0","threadId":"client/thread:opaque","runId":"r1","messages":[{"id":"u1","role":"user","content":"first"}]}`
	body2 := `{
 "protocolVersion":"1.0", "threadId":"client/thread:opaque", "runId":"r2",
 "messages":[
  {"id":"u1","role":"user","content":"first"},
  {"id":"a1","role":"assistant","content":"answer"},
  {"id":"u2","role":"user","content":"second"}
 ]
}`
	post := func(body string, opts sse.Options) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		sse.Handler(agent, opts).ServeHTTP(w, httptest.NewRequest("POST", "/", strings.NewReader(body)))
		return w
	}
	if w := post(body2, sse.Options{}); w.Code != 409 {
		t.Fatalf("missing history checkpoint: %d %s", w.Code, w.Body)
	}
	if w := post(body1, sse.Options{}); w.Code != 200 {
		t.Fatal(w.Body)
	}
	if w := post(body2, sse.Options{}); w.Code != 200 {
		t.Fatal(w.Body)
	}
	req := fake.request(t, 1)
	if req.Prompt != "second" || req.Session == nil || req.Session.State.ResumeID != "checkpoint" || req.Session.Mode != driver.SessionContinueOnly {
		t.Fatalf("unsafe continuation: %+v", req)
	}
	// Full invocation compatibility is checked by the sole execution pipeline;
	// ResumeOnly must prevent starting fresh after this later configuration drift.
	w := post(body2, sse.Options{Options: []adaptor.CallOption{adaptor.WithInstructions("changed instructions")}})
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"type":"RUN_ERROR"`) {
		t.Fatalf("expected explicit incompatible run: %d %s", w.Code, w.Body)
	}
	fake.mu.Lock()
	calls := len(fake.requests)
	fake.mu.Unlock()
	if calls != 2 {
		t.Fatalf("driver replayed incompatible history: %d calls", calls)
	}
}

func TestAGUIHandlerStatelessAgentHistoryFallback(t *testing.T) {
	fake := &fakeDriver{runFunc: func(_ context.Context, req driver.Request, _ driver.EventSink) (driver.Response, error) {
		if req.Session != nil {
			t.Error("stateless Agent received a Thread session")
		}
		return driver.Response{Output: "answer"}, nil
	}}
	agent := adaptor.New(fake)
	defer agent.Close(context.Background())
	body := `{"protocolVersion":"1.0","threadId":"public","runId":"run",
		"messages":[{"id":"u1","role":"user","content":"first"},
		{"id":"a1","role":"assistant","content":"answer"},
		{"id":"u2","role":"user","content":"second"}]}`
	for _, fallback := range []bool{false, true} {
		w := httptest.NewRecorder()
		sse.Handler(agent, sse.Options{AGUIInput: agui.InputOptions{HistoryFallback: fallback}}).
			ServeHTTP(w, httptest.NewRequest("POST", "/", strings.NewReader(body)))
		if !fallback {
			if w.Code != 409 {
				t.Fatalf("default silently discarded history: %d %s", w.Code, w.Body)
			}
		} else if w.Code != 200 || !strings.Contains(w.Body.String(), `"type":"RUN_FINISHED"`) {
			t.Fatalf("fallback failed on Agent without store: %d %s", w.Code, w.Body)
		}
	}
	if got := fake.request(t, 0).Prompt; !strings.Contains(got, `"text":"first"`) ||
		!strings.HasSuffix(got, "second") {
		t.Fatalf("history not rehydrated: %s", got)
	}
	// A lone user request also runs statelessly, without requiring fallback.
	w := httptest.NewRecorder()
	sse.Handler(agent, sse.Options{}).ServeHTTP(w, httptest.NewRequest("POST", "/",
		strings.NewReader(`{"threadId":"public","runId":"first","messages":[{"id":"u","role":"user","content":"first"}]}`)))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"type":"RUN_FINISHED"`) {
		t.Fatalf("first stateless turn failed: %d %s", w.Code, w.Body)
	}
}

func TestAGUIBoundThreadUsesHostKeyForHistoryVerification(t *testing.T) {
	agent := adaptor.New(&fakeDriver{}, adaptor.WithThreadStore(memory.NewStore()))
	defer agent.Close(context.Background())
	fixed := agent.Thread("host-owned")
	for _, replacement := range []string{"host-owned", "branch"} {
		seen := false
		prepared, err := agui.PrepareHTTPRequest(httptest.NewRequest("POST", "/", strings.NewReader(
			`{"threadId":"public","runId":"run","messages":[{"id":"u","role":"user","content":"go"}]}`)),
			fixed, agui.RequestOptions{
				VerifyHistory: func(_ context.Context, _ *agui.RunAgentInput, target adaptor.Runner, key string) (string, bool, error) {
					seen = target == fixed && key == fixed.Key()
					return replacement, false, nil
				},
			})
		if !seen {
			t.Fatal("history verifier did not see the fixed host Thread/key")
		}
		if replacement == "host-owned" {
			if err != nil || prepared.Target != fixed || prepared.ThreadKey != fixed.Key() {
				t.Fatalf("fixed Thread rebound: %+v %v", prepared, err)
			}
		} else {
			var input *agui.InputError
			if !errors.As(err, &input) || input.Status != 409 {
				t.Fatalf("unexecutable fixed-Thread branch was accepted: %v", err)
			}
		}
	}
}
