package main

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/bridges/agui/aguisession"
)

type sessionRunner struct{ stream adaptor.Stream }

func (r sessionRunner) Run(context.Context, string, ...adaptor.CallOption) (*adaptor.Result, error) {
	panic("must stream")
}
func (r sessionRunner) Stream(context.Context, string, ...adaptor.CallOption) adaptor.Stream {
	return r.stream
}

func TestRecordedSession(t *testing.T) {
	for _, mode := range []string{"fallback", "streamed", "empty", "partial", "approval"} {
		t.Run(mode, func(t *testing.T) {
			store := mustNewThreadStore(t)
			ch := make(chan adaptor.Event, 8)
			result := &adaptor.Result{Text: "answer"}
			var runErr error
			ch <- adaptor.RunStarted{}
			switch mode {
			case "streamed":
				ch <- adaptor.TextDelta{MessageID: "a", Text: "answer"}
			case "empty":
				result.Text = " \n\t"
			case "partial":
				runErr = &adaptor.RunError{Reason: adaptor.ReasonAgentError, Message: "failed", Result: result}
				result = nil
			case "approval":
				ch <- &adaptor.ApprovalRequest{ID: "req", Kind: adaptor.ApprovalPermission, Title: "shell"}
			}
			close(ch)
			s := aguisession.New(sessionRunner{&fakeStream{events: ch, result: result, err: runErr}}, aguisession.Options{Recorder: store.recorder, Observe: func(key string, e adaptor.Event) {
				if req, ok := e.(*adaptor.ApprovalRequest); ok {
					store.addPending(key, req)
				}
			}})
			w := httptest.NewRecorder()
			s.Handler().ServeHTTP(w, httptest.NewRequest("POST", "/agent", strings.NewReader(`{"protocolVersion":"1.0","threadId":"t","runId":"r","messages":[{"id":"u","role":"user","content":"hello"}]}`)))
			if w.Code != 200 {
				t.Fatal(w.Body)
			}
			records := store.historyAfter(aguisession.RecorderKey(recordingKey("t")), 0)
			if len(records) < 5 {
				t.Fatal("missing recorded user turn")
			}
			for i, phase := range []adaptor.Phase{adaptor.PhaseStart, adaptor.PhaseContent, adaptor.PhaseEnd} {
				e, ok := records[i+1].Event.(adaptor.TextDelta)
				if !ok || e.Role != adaptor.RoleUser || e.MessageID != "u" || e.Phase != phase {
					t.Fatalf("user record: %#v", records[i+1])
				}
			}
			for i, r := range records {
				if r.HostSeq != uint64(i+1) {
					t.Fatal("unordered history")
				}
			}
			if strings.Contains(w.Body.String(), `"messageId":"u"`) {
				t.Fatal("user echoed to client")
			}
			count := strings.Count(w.Body.String(), `"delta":"answer"`)
			if mode == "empty" {
				if count != 0 {
					t.Fatal(w.Body)
				}
			} else if count != 1 {
				t.Fatal(w.Body)
			}
			if mode == "partial" && (!strings.Contains(w.Body.String(), "RUN_ERROR") || strings.Contains(w.Body.String(), "RUN_FINISHED")) {
				t.Fatal(w.Body)
			}
			if mode == "approval" && len(store.pendingRequests(recordingKey("t"))) != 1 {
				t.Fatal("approval not parked")
			}
		})
	}
}
