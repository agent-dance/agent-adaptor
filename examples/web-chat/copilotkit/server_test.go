package main

import (
	"context"
	"encoding/json"
	"errors"
	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/bridges/agui/aguisession"
	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/hosttools/sessionrecorder"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestSessionRejectsMissingUser(t *testing.T) {
	store := mustNewThreadStore(t)
	s := aguisession.New(sessionRunner{}, aguisession.Options{Recorder: store.recorder})
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest("POST", "/agent", strings.NewReader(`{"protocolVersion":"1.0","threadId":"t","runId":"r","messages":[]}`)))
	if w.Code != 400 {
		t.Fatal(w.Code, w.Body)
	}
	if len(store.historyAfter(aguisession.RecorderKey(recordingKey("t")), 0)) != 0 {
		t.Fatal("invalid request recorded")
	}
}

func TestHistoryEndpointAndApprovalLifecycle(t *testing.T) {
	store := mustNewThreadStore(t)
	ch := make(chan adaptor.Event, 4)
	req := &adaptor.ApprovalRequest{ID: "req", Kind: adaptor.ApprovalPermission, Title: "shell"}
	ch <- req
	ch <- adaptor.Notice{Kind: adaptor.NoticeApprovalResolved, Data: map[string]any{"request_id": "req"}}
	ch <- &adaptor.ApprovalRequest{ID: "unresolved", Kind: adaptor.ApprovalPermission, Title: "shell"}
	close(ch)
	server := &appServer{store: store}
	observed := 0
	server.session = aguisession.New(sessionRunner{&fakeStream{events: ch}}, aguisession.Options{
		Recorder: store.recorder,
		Observe: func(key string, e adaptor.Event) {
			server.observe(key, e)
			switch v := e.(type) {
			case *adaptor.ApprovalRequest:
				if len(store.pendingRequests(key)) != 1 {
					t.Error("approval not parked")
				}
				observed++
			case adaptor.Notice:
				if v.Kind == adaptor.NoticeApprovalResolved && len(store.pendingRequests(key)) != 0 {
					t.Error("approval not removed")
				}
			}
		}, OnRunEnd: store.clearPending,
	})
	w := httptest.NewRecorder()
	server.handleAgent(w, httptest.NewRequest("POST", "/agent", strings.NewReader(`{"threadId":"t","runId":"r","messages":[{"id":"u","role":"user","content":"go"}]}`)))
	if w.Code != 200 || observed != 2 || len(store.pendingRequests(recordingKey("t"))) != 0 {
		t.Fatal(w.Body, observed)
	}
	for _, after := range []string{"0", "2"} {
		w := httptest.NewRecorder()
		server.handleSessionEvents(w, httptest.NewRequest("GET", "/session/events?thread_id=t&after="+after, nil))
		var response struct {
			ThreadID string                        `json:"thread_id"`
			After    uint64                        `json:"after"`
			Events   []sessionrecorder.EventRecord `json:"events"`
			LastSeq  uint64                        `json:"last_seq"`
			Active   bool                          `json:"run_active"`
		}
		// Decode the envelope separately: typed event payload decoding belongs to recorder.
		var envelope map[string]json.RawMessage
		if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
			t.Fatal(err)
		}
		for _, field := range []string{"thread_id", "after", "events", "last_seq", "run_active"} {
			if _, ok := envelope[field]; !ok {
				t.Fatal("missing", field)
			}
		}
		_ = json.Unmarshal(envelope["thread_id"], &response.ThreadID)
		_ = json.Unmarshal(envelope["after"], &response.After)
		_ = json.Unmarshal(envelope["last_seq"], &response.LastSeq)
		_ = json.Unmarshal(envelope["run_active"], &response.Active)
		if response.ThreadID != "t" || response.Active || response.LastSeq <= response.After {
			t.Fatal(w.Body)
		}
		var records []map[string]any
		_ = json.Unmarshal(envelope["events"], &records)
		for _, record := range records {
			if record["host_seq"].(float64) <= float64(response.After) {
				t.Fatal("cursor ignored")
			}
		}
	}
}

func TestHistoryReportsActiveRun(t *testing.T) {
	store := mustNewThreadStore(t)
	ch := make(chan adaptor.Event)
	entered := make(chan struct{})
	server := &appServer{store: store}
	server.session = aguisession.New(sessionRunner{&fakeStream{events: ch}}, aguisession.Options{Recorder: store.recorder, Observe: func(_ string, e adaptor.Event) {
		if _, ok := e.(adaptor.TextDelta); ok {
			select {
			case <-entered:
			default:
				close(entered)
			}
		}
	}})
	done := make(chan struct{})
	go func() {
		defer close(done)
		server.handleAgent(httptest.NewRecorder(), httptest.NewRequest("POST", "/agent", strings.NewReader(`{"threadId":"t","runId":"r","messages":[{"id":"u","role":"user","content":"go"}]}`)))
	}()
	<-entered
	w := httptest.NewRecorder()
	server.handleSessionEvents(w, httptest.NewRequest("GET", "/session/events?thread_id=t", nil))
	if !strings.Contains(w.Body.String(), `"run_active":true`) {
		t.Error(w.Body)
	}
	close(ch)
	<-done
}

type failedResponseWriter struct{ header http.Header }

func (w *failedResponseWriter) Header() http.Header { return w.header }

func (w *failedResponseWriter) WriteHeader(int) {}

func (w *failedResponseWriter) Write([]byte) (int, error) { return 0, errors.New("connection lost") }

func TestAgentWriteFailureClearsPending(t *testing.T) {
	store := mustNewThreadStore(t)
	ch := make(chan adaptor.Event, 1)
	ch <- &adaptor.ApprovalRequest{ID: "pending", Kind: adaptor.ApprovalPermission, Title: "shell"}
	close(ch)
	stream := &fakeStream{events: ch}
	server := &appServer{store: store}
	server.session = aguisession.New(sessionRunner{stream}, aguisession.Options{Recorder: store.recorder, Observe: server.observe, OnRunEnd: store.clearPending})
	server.handleAgent(&failedResponseWriter{header: make(http.Header)}, httptest.NewRequest("POST", "/agent", strings.NewReader(`{"threadId":"t","runId":"r","messages":[{"id":"u","role":"user","content":"go"}]}`)))
	if !stream.cancelled.Load() || server.session.Active(recordingKey("t")) || len(store.pendingRequests(recordingKey("t"))) != 0 {
		t.Fatal("write failure left run state")
	}
}

func TestRoutesAndValidation(t *testing.T) {
	store := mustNewThreadStore(t)
	server := &appServer{store: store, cors: "*", session: aguisession.New(sessionRunner{}, aguisession.Options{Recorder: store.recorder})}
	for _, tc := range []struct {
		method, path, body string
		status             int
	}{
		{"GET", "/health", "", 200},
		{"POST", "/session/events", "", 405},
		{"GET", "/session/events", "", 400},
		{"POST", "/decision/pending", "", 405},
		{"GET", "/decision/pending", "", 400},
		{"GET", "/decision/pending?thread_id=t", "", 200},
		{"OPTIONS", "/decision/resolve", "", 204},
		{"GET", "/decision/resolve", "", 405},
		{"POST", "/decision/resolve", "{", 400},
		{"POST", "/decision/resolve", `{}`, 400},
	} {
		w := httptest.NewRecorder()
		server.routes().ServeHTTP(w, httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body)))
		if w.Code != tc.status {
			t.Errorf("%s %s: %d %s", tc.method, tc.path, w.Code, w.Body)
		}
	}
}

type decisionDriver struct {
	ask     bool
	prompts []string
}

func (d *decisionDriver) Descriptor() driver.Descriptor {
	return driver.Descriptor{Type: "decision-test", DisplayName: "Decision test", RunPolicyCaps: driver.RunPolicyCapabilities{
		Isolation: true, WebSearch: true, Browser: true,
		Permission: driver.HumanDecisionSupport{Ask: true, AutoApprove: true, AutoReject: true},
		PlanReview: driver.HumanDecisionSupport{Ask: true, AutoApprove: true, AutoReject: true},
		Question:   driver.QuestionSupport{Ask: true, AutoReject: true},
	}}
}
func (*decisionDriver) ValidateConfig(any) error { return nil }
func (d *decisionDriver) Run(ctx context.Context, req driver.Request, sink driver.EventSink) (driver.Response, error) {
	d.prompts = append(d.prompts, req.Prompt)
	if d.ask {
		response, err := sink.(driver.DecisionCapableSink).RequestDecision(ctx, driver.DecisionRequest{Kind: driver.HumanDecisionQuestion, Source: "question", Prompt: "choose", Choices: []driver.DecisionChoice{{Key: "yes", Label: "Yes"}}})
		if err != nil {
			return driver.Response{}, err
		}
		return driver.Response{Output: response.Text}, nil
	}
	return driver.Response{Output: "answer"}, nil
}

func TestApprovalHTTPResolveKeepsResponderAfterWrongKind(t *testing.T) {
	store := mustNewThreadStore(t)
	d := &decisionDriver{ask: true}
	agent := adaptor.New(d, adaptor.WithPolicy(adaptor.Policy{Approvals: adaptor.ApprovalPolicy{Question: adaptor.QuestionAsk}}))
	defer agent.Close(context.Background())
	server := &appServer{store: store}
	parked := make(chan *adaptor.ApprovalRequest, 1)
	server.session = aguisession.New(agent, aguisession.Options{Recorder: store.recorder, Observe: func(key string, e adaptor.Event) {
		server.observe(key, e)
		if req, ok := e.(*adaptor.ApprovalRequest); ok {
			parked <- req
		}
	}, OnRunEnd: store.clearPending})
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		w := httptest.NewRecorder()
		server.handleAgent(w, httptest.NewRequest("POST", "/agent", strings.NewReader(`{"threadId":"t","runId":"r","messages":[{"id":"u","role":"user","content":"go"}]}`)))
		done <- w
	}()
	var req *adaptor.ApprovalRequest
	select {
	case req = <-parked:
	case <-time.After(time.Second):
		t.Fatal("approval not parked")
	}
	pending := httptest.NewRecorder()
	server.handleDecisionPending(pending, httptest.NewRequest("GET", "/?thread_id=t", nil))
	var payload struct {
		Pending []adaptor.ApprovalRequest `json:"pending"`
	}
	if err := json.Unmarshal(pending.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Pending) != 1 || payload.Pending[0].ID != req.ID || payload.Pending[0].Title != "choose" {
		t.Fatal(pending.Body)
	}
	// A bad browser choice must leave the same live responder available.
	for _, result := range []string{"invalid", "approved"} {
		w := httptest.NewRecorder()
		body, _ := json.Marshal(map[string]string{"run_id": "t", "request_id": req.ID, "result": result})
		server.handleDecisionResolve(w, httptest.NewRequest("POST", "/decision/resolve", strings.NewReader(string(body))))
		if w.Code != 400 || len(store.pendingRequests(recordingKey("t"))) != 1 {
			t.Fatal(w.Code, w.Body)
		}
	}
	w := httptest.NewRecorder()
	body, _ := json.Marshal(map[string]string{"run_id": "t", "request_id": req.ID, "result": "answered", "choice": "yes"})
	server.handleDecisionResolve(w, httptest.NewRequest("POST", "/decision/resolve", strings.NewReader(string(body))))
	if w.Code != 204 {
		t.Fatal(w.Code, w.Body)
	}
	select {
	case response := <-done:
		if response.Code != 200 || !strings.Contains(response.Body.String(), `"delta":"yes"`) {
			t.Fatal(response.Body)
		}
	case <-time.After(time.Second):
		t.Fatal("approval answer did not resume drain")
	}
	if len(store.pendingRequests(recordingKey("t"))) != 0 {
		t.Fatal("OnRunEnd left pending approval")
	}
	again := httptest.NewRecorder()
	server.handleDecisionResolve(again, httptest.NewRequest("POST", "/decision/resolve", strings.NewReader(string(body))))
	if again.Code != 410 {
		t.Fatal(again.Code, again.Body)
	}
}

func TestNewAppServerAcceptsTwoTurns(t *testing.T) {
	t.Setenv("THREAD_STORE_DIR", "")
	d := &decisionDriver{}
	agent := adaptor.New(d)
	defer agent.Close(context.Background())
	server, err := newAppServer(agent, "test", "")
	if err != nil {
		t.Fatal(err)
	}
	defer server.store.recorder.Close()
	first := httptest.NewRecorder()
	server.handleAgent(first, httptest.NewRequest("POST", "/agent", strings.NewReader(`{"threadId":"t","runId":"one","messages":[{"id":"u1","role":"user","content":"first"}]}`)))
	if !strings.Contains(first.Body.String(), "RUN_FINISHED") {
		t.Fatal(first.Body)
	}
	prior, err := server.session.Messages(context.Background(), recordingKey("t"))
	if err != nil {
		t.Fatal(err)
	}
	messages := append(prior, aguisession.Message{"id": "u2", "role": "user", "content": "second"})
	body, _ := json.Marshal(map[string]any{"threadId": "t", "runId": "two", "messages": messages})
	second := httptest.NewRecorder()
	server.handleAgent(second, httptest.NewRequest("POST", "/agent", strings.NewReader(string(body))))
	if second.Code != 200 || !strings.Contains(second.Body.String(), "RUN_FINISHED") {
		t.Fatal(second.Code, second.Body)
	}
	if len(d.prompts) != 2 || !strings.Contains(d.prompts[1], "<agent-adaptor-history>") || !strings.HasSuffix(d.prompts[1], "second") {
		t.Fatal(d.prompts)
	}
}

type readErrorRecorder struct{ sessionrecorder.EventRecorder }

func (readErrorRecorder) Since(context.Context, string, uint64) ([]sessionrecorder.EventRecord, error) {
	return nil, errors.New("read failed")
}

func TestHistoryReadFailureAndCursorValidation(t *testing.T) {
	store := mustNewThreadStore(t)
	server := &appServer{store: store, session: aguisession.New(sessionRunner{}, aguisession.Options{Recorder: store.recorder})}
	for _, after := range []string{"-1", "not-a-number"} {
		w := httptest.NewRecorder()
		server.handleSessionEvents(w, httptest.NewRequest("GET", "/?thread_id=t&after="+after, nil))
		if w.Code != 400 {
			t.Fatal(w.Code, w.Body)
		}
	}
	w := httptest.NewRecorder()
	server.handleSessionEvents(w, httptest.NewRequest("GET", "/?thread_id=t&after=100", nil))
	if !strings.Contains(w.Body.String(), `"last_seq":100`) || !strings.Contains(w.Body.String(), `"events":[]`) {
		t.Fatal("empty suffix regressed cursor", w.Body)
	}
	store.recorder = readErrorRecorder{store.recorder}
	w = httptest.NewRecorder()
	server.handleSessionEvents(w, httptest.NewRequest("GET", "/?thread_id=t", nil))
	if w.Code != 500 {
		t.Fatal(w.Code, w.Body)
	}
}
