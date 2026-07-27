package sse

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	adaptor "github.com/agent-dance/agent-adaptor/next"
)

// ApprovalErrorResponseV1 is the stable JSON error body for companion HTTP
// endpoints that answer ApprovalRequest events emitted over SSE.
type ApprovalErrorResponseV1 struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// MapApprovalErrorV1 maps the public exactly-once approval errors to stable
// HTTP semantics. In particular, stale/duplicate answers are Gone rather
// than generic conflicts and a method-kind mismatch is a client error.
func MapApprovalErrorV1(err error) (int, ApprovalErrorResponseV1) {
	response := ApprovalErrorResponseV1{Code: "approval.internal", Message: "approval response failed"}
	if err == nil {
		return http.StatusNoContent, ApprovalErrorResponseV1{}
	}
	response.Message = err.Error()
	switch {
	case errors.Is(err, adaptor.ErrApprovalExpired):
		response.Code = "approval.expired"
		return http.StatusGone, response
	case errors.Is(err, adaptor.ErrApprovalResolved):
		response.Code = "approval.resolved"
		return http.StatusGone, response
	case errors.Is(err, adaptor.ErrApprovalKindMismatch):
		response.Code = "approval.kind_mismatch"
		return http.StatusBadRequest, response
	case errors.Is(err, adaptor.ErrApprovalUnavailable):
		response.Code = "approval.unavailable"
		return http.StatusServiceUnavailable, response
	case errors.Is(err, context.DeadlineExceeded):
		response.Code = "approval.deadline_exceeded"
		return http.StatusGatewayTimeout, response
	case errors.Is(err, context.Canceled):
		response.Code = "approval.cancelled"
		return http.StatusRequestTimeout, response
	default:
		return http.StatusInternalServerError, response
	}
}

// WriteApprovalErrorV1 writes MapApprovalErrorV1 as application/json. A nil
// error writes 204 with no body.
func WriteApprovalErrorV1(w http.ResponseWriter, err error) {
	if w == nil {
		return
	}
	status, response := MapApprovalErrorV1(err)
	if status == http.StatusNoContent {
		w.WriteHeader(status)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(response)
}
