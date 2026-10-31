package sse_test

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	adaptor "github.com/agent-dance/agent-adaptor"
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
