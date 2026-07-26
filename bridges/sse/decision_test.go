package sse_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	agentadaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/bridges/sse"
)

func TestDecodeDecisionResolveRequest(t *testing.T) {
	body := `{"run_id":"run-1","request_id":"req-1","result":"approved"}`
	r := httptest.NewRequest(http.MethodPost, "/decision/resolve", strings.NewReader(body))
	got, err := sse.DecodeDecisionResolveRequest(r)
	if err != nil {
		t.Fatal(err)
	}
	if got.RequestID != "req-1" || got.Result != "approved" {
		t.Fatalf("decoded: %+v", got)
	}
}

func TestDecodeDecisionResolveRequest_RejectsMissingRequired(t *testing.T) {
	for name, body := range map[string]string{
		"empty":         ``,
		"missing_id":    `{"result":"approved"}`,
		"missing_res":   `{"request_id":"x"}`,
		"unknown_field": `{"request_id":"x","result":"approved","extra":true}`,
	} {
		t.Run(name, func(t *testing.T) {
			var r *http.Request
			if body == "" {
				r = httptest.NewRequest(http.MethodPost, "/decision/resolve", bytes.NewReader(nil))
				r.Body = http.NoBody
			} else {
				r = httptest.NewRequest(http.MethodPost, "/decision/resolve", strings.NewReader(body))
			}
			if _, err := sse.DecodeDecisionResolveRequest(r); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestToDecisionResponse(t *testing.T) {
	r := sse.DecisionResolveRequest{
		RunID: "run", RequestID: "req",
		Result: "answered",
		Choice: "foo",
		Answer: map[string]any{"k": "v"},
		Text:   "note",
	}
	resp := r.ToDecisionResponse()
	if resp.RequestID != "req" ||
		resp.Result != agentadaptor.DecisionAnswered ||
		resp.Choice != "foo" ||
		resp.Answer["k"] != "v" ||
		resp.Text != "note" {
		t.Fatalf("mapping: %+v", resp)
	}
}

func TestWriteDecisionResolveError_Maps(t *testing.T) {
	cases := map[error]int{
		agentadaptor.ErrDecisionRequestExpired:     http.StatusGone,
		agentadaptor.ErrDecisionResultKindMismatch: http.StatusBadRequest,
		agentadaptor.ErrRunEnded:                   http.StatusConflict,
	}
	for err, want := range cases {
		w := httptest.NewRecorder()
		if !sse.WriteDecisionResolveError(w, err) {
			t.Fatalf("expected write for %v", err)
		}
		if w.Code != want {
			t.Errorf("%v: got %d want %d", err, w.Code, want)
		}
	}
	w := httptest.NewRecorder()
	if sse.WriteDecisionResolveError(w, nil) {
		t.Fatal("nil error must not write")
	}
}

func TestDecisionResolveRequestRoundTripJSON(t *testing.T) {
	orig := sse.DecisionResolveRequest{
		RunID: "r", RequestID: "x", Result: "approved",
	}
	raw, err := json.Marshal(orig)
	if err != nil {
		t.Fatal(err)
	}
	var round sse.DecisionResolveRequest
	if err := json.Unmarshal(raw, &round); err != nil {
		t.Fatal(err)
	}
	if round.RunID != orig.RunID || round.RequestID != orig.RequestID || round.Result != orig.Result || round.Choice != orig.Choice || round.Text != orig.Text {
		t.Fatalf("round-trip diff: got %#v want %#v", round, orig)
	}
}
