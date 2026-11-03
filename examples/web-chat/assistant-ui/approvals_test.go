package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/agent-dance/agent-adaptor/v2/bridges/agui/aguisession"
)

func TestApprovalRoundTripThroughCompanionEndpoint(t *testing.T) {
	// Deny uses the SDK default OnReject (abort): the run ends with RUN_ERROR.
	for _, tc := range []struct{ result, want, terminal string }{
		{"approve", "Approved: the simulated deploy finished.", `"type":"RUN_FINISHED"`},
		{"deny", "I need your approval before the simulated deploy.", `"code":"approval_denied"`},
	} {
		t.Run(tc.result, func(t *testing.T) {
			a := appForTest(t, config{dataDir: t.TempDir()})
			thread := "hitl-" + tc.result
			done := make(chan *httptest.ResponseRecorder, 1)
			go func() {
				body := `{"protocolVersion":"1.0","threadId":"` + thread + `","runId":"r1","messages":[{"id":"u1","role":"user","content":"deploy with approval"}]}`
				w := httptest.NewRecorder()
				a.handler.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/agent", strings.NewReader(body)))
				done <- w
			}()
			var pending struct {
				Pending []pendingDecision `json:"pending"`
			}
			for deadline := time.Now().Add(10 * time.Second); len(pending.Pending) == 0; time.Sleep(5 * time.Millisecond) {
				if time.Now().After(deadline) {
					select {
					case w := <-done:
						t.Fatalf("decision never became pending; run ended: %s", w.Body)
					default:
						t.Fatal("decision never became pending")
					}
				}
				w := httptest.NewRecorder()
				a.handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/decision/pending?thread_id="+thread, nil))
				_ = json.Unmarshal(w.Body.Bytes(), &pending)
			}
			req := pending.Pending[0]
			if req.Kind != "permission" || req.ToolCallID != "deploy" || req.Title != "Run the simulated deploy?" {
				t.Fatalf("pending decision = %+v", req)
			}
			resolve := func() *httptest.ResponseRecorder {
				body, _ := json.Marshal(decision{ThreadID: thread, RequestID: req.RequestID, Result: tc.result, Text: "not now"})
				w := httptest.NewRecorder()
				a.handler.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/decision/resolve", strings.NewReader(string(body))))
				return w
			}
			if w := resolve(); w.Code != http.StatusNoContent {
				t.Fatalf("resolve: %d %s", w.Code, w.Body)
			}
			if w := <-done; w.Code != http.StatusOK || !strings.Contains(w.Body.String(), tc.terminal) {
				t.Fatalf("run: %d %s", w.Code, w.Body)
			}
			if w := resolve(); w.Code != http.StatusGone {
				t.Fatalf("second resolve: %d %s", w.Code, w.Body)
			}
			messages, err := a.session.Messages(context.Background(), aguisession.ThreadKey(thread))
			if err != nil {
				t.Fatal(err)
			}
			raw, _ := json.Marshal(messages)
			if !strings.Contains(string(raw), tc.want) || !strings.Contains(string(raw), `"toolCallId":"dec-`+req.RequestID+`"`) {
				t.Fatalf("history lacks decision outcome %q: %s", tc.want, raw)
			}
		})
	}
}

func TestDecisionEndpointsRejectMalformedInput(t *testing.T) {
	a := appForTest(t, config{dataDir: t.TempDir()})
	for _, body := range []string{`{`, `{"threadId":"t"}`, `{"threadId":"t","requestId":"r","result":"approve","extra":1}`} {
		w := httptest.NewRecorder()
		a.handler.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/decision/resolve", strings.NewReader(body)))
		if w.Code != http.StatusBadRequest {
			t.Fatalf("%s: %d %s", body, w.Code, w.Body)
		}
	}
	w := httptest.NewRecorder()
	a.handler.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/decision/resolve", strings.NewReader(`{"threadId":"t","requestId":"unknown","result":"approve"}`)))
	if w.Code != http.StatusGone {
		t.Fatalf("unknown request: %d %s", w.Code, w.Body)
	}
	w = httptest.NewRecorder()
	a.handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/decision/pending", nil))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("pending without thread: %d %s", w.Code, w.Body)
	}
}
