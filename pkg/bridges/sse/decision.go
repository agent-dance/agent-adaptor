// decision.go: HTTP helpers for HITL v2 decision resolution.
//
// The SSE bridge itself is intentionally one-directional — hosts register
// their own HTTP route (POST /decision/resolve) and forward the body here
// through DecisionResolveRequest.ToDecisionResponse(). See
// docs/workstream-hitl-v2.md §6.2 for the full wire contract.
package sse

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	agentadaptor "github.com/agent-dance/agent-adaptor"
)

// DecisionResolveRequest is the JSON shape hosts decode from
// POST /decision/resolve.
type DecisionResolveRequest struct {
	RunID     string         `json:"run_id"`
	RequestID string         `json:"request_id"`
	Result    string         `json:"result"`
	Choice    string         `json:"choice,omitempty"`
	Answer    map[string]any `json:"answer,omitempty"`
	Text      string         `json:"text,omitempty"`
}

// ToDecisionResponse converts the DTO into the SDK DecisionResponse value.
func (r DecisionResolveRequest) ToDecisionResponse() agentadaptor.DecisionResponse {
	return agentadaptor.DecisionResponse{
		RequestID: r.RequestID,
		Result:    agentadaptor.DecisionResult(r.Result),
		Choice:    r.Choice,
		Answer:    r.Answer,
		Text:      r.Text,
	}
}

// DecodeDecisionResolveRequest parses the body of a POST /decision/resolve
// request.
func DecodeDecisionResolveRequest(r *http.Request) (*DecisionResolveRequest, error) {
	if r.Body == nil {
		return nil, errors.New("sse: empty request body")
	}
	defer r.Body.Close()
	var body DecisionResolveRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, errors.New("sse: empty request body")
		}
		return nil, err
	}
	if body.RequestID == "" {
		return nil, errors.New("sse: request_id is required")
	}
	if body.Result == "" {
		return nil, errors.New("sse: result is required")
	}
	return &body, nil
}

// WriteDecisionResolveError translates SDK errors from handle.ResolveDecision
// to appropriate HTTP status codes. Returns true when an error was written.
func WriteDecisionResolveError(w http.ResponseWriter, err error) bool {
	if err == nil {
		return false
	}
	switch {
	case errors.Is(err, agentadaptor.ErrDecisionRequestExpired):
		http.Error(w, err.Error(), http.StatusGone)
	case errors.Is(err, agentadaptor.ErrDecisionResultKindMismatch):
		http.Error(w, err.Error(), http.StatusBadRequest)
	case errors.Is(err, agentadaptor.ErrRunEnded):
		http.Error(w, err.Error(), http.StatusConflict)
	default:
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
	return true
}
