package main

import (
	"encoding/json"
	"errors"
	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/bridges/agui/aguisession"
	"github.com/agent-dance/agent-adaptor/hosttools/sessionrecorder"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
	if !stream.cancelled || server.session.Active(recordingKey("t")) || len(store.pendingRequests(recordingKey("t"))) != 0 {
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
