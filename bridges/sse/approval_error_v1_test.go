package sse_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/agent-dance/agent-adaptor/bridges/sse"
	adaptor "github.com/agent-dance/agent-adaptor"
)

func TestApprovalErrorV1HTTPMapping(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{name: "resolved", err: adaptor.ErrApprovalResolved, status: http.StatusGone, code: "approval.resolved"},
		{name: "expired", err: adaptor.ErrApprovalExpired, status: http.StatusGone, code: "approval.expired"},
		{name: "kind mismatch", err: adaptor.ErrApprovalKindMismatch, status: http.StatusBadRequest, code: "approval.kind_mismatch"},
		{name: "unavailable", err: adaptor.ErrApprovalUnavailable, status: http.StatusServiceUnavailable, code: "approval.unavailable"},
		{name: "deadline", err: context.DeadlineExceeded, status: http.StatusGatewayTimeout, code: "approval.deadline_exceeded"},
		{name: "cancelled", err: context.Canceled, status: http.StatusRequestTimeout, code: "approval.cancelled"},
		{name: "internal", err: errors.New("boom"), status: http.StatusInternalServerError, code: "approval.internal"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			sse.WriteApprovalErrorV1(recorder, test.err)
			if recorder.Code != test.status || recorder.Header().Get("Content-Type") != "application/json" {
				t.Fatalf("status=%d content-type=%q", recorder.Code, recorder.Header().Get("Content-Type"))
			}
			var response sse.ApprovalErrorResponseV1
			if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if response.Code != test.code || response.Message == "" {
				t.Fatalf("response=%+v", response)
			}
		})
	}
}

func TestWriteApprovalErrorV1NilIsNoContent(t *testing.T) {
	recorder := httptest.NewRecorder()
	sse.WriteApprovalErrorV1(recorder, nil)
	if recorder.Code != http.StatusNoContent || recorder.Body.Len() != 0 {
		t.Fatalf("response=%d %q", recorder.Code, recorder.Body.String())
	}
}
