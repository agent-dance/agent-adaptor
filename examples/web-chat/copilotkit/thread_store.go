package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"

	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/bridges/sse"
	"github.com/agent-dance/agent-adaptor/hosttools/sessionrecorder"
)

// threadStore supplies the host's history backend and live approval responders.
// aguisession owns draining, recording, projection and active-run admission.
// Pending responders exist only during a run; provider checkpoints are separate.
//
// History storage is delegated to sessionrecorder.EventRecorder, which
// assigns a session-scoped monotonic HostSeq to every event. That cursor is
// what /session/events' `last_seq` and `after` parameters refer to — events
// carry no cross-run sequence of their own, so the host cursor is the only
// thing that can survive a run boundary. Pending approvals are stored as live
// *adaptor.ApprovalRequest values: every request owns its responder, so the
// store never has to probe unrelated live runs when resolving one.
type threadStore struct {
	recorder sessionrecorder.EventRecorder

	mu      sync.Mutex
	threads map[string]*threadRuntime
}

// threadRuntime carries the per-session state that is meaningful only
// while a stream is alive. It is not persisted.
type threadRuntime struct {
	pending map[string]*adaptor.ApprovalRequest
}

// newThreadStore returns a durable typed-Event recorder when
// THREAD_STORE_DIR is configured. Directory creation/open errors are returned
// to startup; configured persistence is never silently replaced with memory.
// With no directory configured, the in-memory backend is an explicit choice.
func newThreadStore() (*threadStore, error) {
	var backend sessionrecorder.EventBackend
	if dir := os.Getenv("THREAD_STORE_DIR"); dir != "" {
		jsonl, err := sessionrecorder.NewJSONLEventBackend(dir)
		if err != nil {
			return nil, fmt.Errorf("create persistent thread store: %w", err)
		}
		backend = jsonl
	} else {
		backend = sessionrecorder.NewMemoryEventBackend()
	}
	return &threadStore{
		recorder: sessionrecorder.NewEventRecorder(backend),
		threads:  map[string]*threadRuntime{},
	}, nil
}

func (s *threadStore) addPending(threadID string, req *adaptor.ApprovalRequest) {
	if req == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.thread(threadID).pending[req.ID] = req
}

func (s *threadStore) removePending(threadID, requestID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if st, ok := s.threads[threadID]; ok {
		delete(st.pending, requestID)
	}
}

func (s *threadStore) pendingRequests(threadID string) []*adaptor.ApprovalRequest {
	s.mu.Lock()
	defer s.mu.Unlock()

	st, ok := s.threads[threadID]
	if !ok {
		return nil
	}
	out := make([]*adaptor.ApprovalRequest, 0, len(st.pending))
	for _, req := range st.pending {
		out = append(out, req)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// resolveDecision answers a parked approval. The whole body is the v1
// approval contract in miniature: look the request up, then call exactly
// one of Approve / Deny / Answer on it. Wrong verb for the kind gives
// ErrApprovalKindMismatch; a second answer (or an answer to a request the
// SDK already expired) gives ErrApprovalResolved.
func (s *threadStore) resolveDecision(ctx context.Context, threadID string, body approvalResolveRequest) error {
	s.mu.Lock()
	var req *adaptor.ApprovalRequest
	if st, ok := s.threads[threadID]; ok {
		req = st.pending[body.RequestID]
	}
	s.mu.Unlock()

	if req == nil {
		// Unknown request id: either never seen or already settled.
		return adaptor.ErrApprovalResolved
	}

	var err error
	switch strings.ToLower(strings.TrimSpace(body.Result)) {
	case "approved", "approve":
		err = req.Approve(ctx)
	case "rejected", "reject", "denied", "deny":
		err = req.Deny(ctx, body.Text)
	case "answered", "answer":
		option := body.Choice
		if option == "" {
			option = body.Text
		}
		if option == "" && body.Answer != nil {
			return fmt.Errorf("%w: structured answers are unsupported; provide choice or text", adaptor.ErrApprovalKindMismatch)
		}
		err = req.Answer(ctx, option)
	default:
		return fmt.Errorf("%w: unsupported decision result %q", adaptor.ErrApprovalKindMismatch, body.Result)
	}
	if err == nil || errors.Is(err, adaptor.ErrApprovalResolved) || errors.Is(err, adaptor.ErrApprovalExpired) || errors.Is(err, adaptor.ErrApprovalUnavailable) {
		s.removePending(threadID, body.RequestID)
	}
	return err
}

func (s *threadStore) thread(threadID string) *threadRuntime {
	st := s.threads[threadID]
	if st == nil {
		st = &threadRuntime{
			pending: map[string]*adaptor.ApprovalRequest{},
		}
		s.threads[threadID] = st
	}
	return st
}

// ---- misc helpers used by server.go ----

// approvalResolveRequest is this example's companion-endpoint wire DTO. The
// bridge remains one-way; the host keeps the live ApprovalRequest responder
// and chooses its own HTTP endpoint shape.
type approvalResolveRequest struct {
	RunID     string         `json:"run_id"`
	RequestID string         `json:"request_id"`
	Result    string         `json:"result"`
	Choice    string         `json:"choice,omitempty"`
	Answer    map[string]any `json:"answer,omitempty"`
	Text      string         `json:"text,omitempty"`
}

func decodeApprovalResolveRequest(r *http.Request) (*approvalResolveRequest, error) {
	if r == nil || r.Body == nil {
		return nil, fmt.Errorf("approval response body is empty")
	}
	defer r.Body.Close()
	var body approvalResolveRequest
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("approval response body is empty")
		}
		return nil, fmt.Errorf("decode approval response: %w", err)
	}
	if body.RequestID == "" {
		return nil, fmt.Errorf("request_id is required")
	}
	if body.Result == "" {
		return nil, fmt.Errorf("result is required")
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("approval response must contain exactly one JSON value")
	}
	return &body, nil
}

// writeApprovalError delegates stable HTTP/JSON error semantics to the typed
// v1 SSE companion helper.
func writeApprovalError(w http.ResponseWriter, err error) bool {
	if err == nil {
		return false
	}
	sse.WriteApprovalError(w, err)
	return true
}

func corsMiddleware(allowOrigin string, next http.Handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if allowOrigin != "" {
			w.Header().Set("Access-Control-Allow-Origin", allowOrigin)
			allowHeaders := r.Header.Get("Access-Control-Request-Headers")
			if allowHeaders == "" {
				allowHeaders = "Content-Type, Authorization"
			}
			w.Header().Set("Access-Control-Allow-Headers", allowHeaders)
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	}
}

func writeJSON(w http.ResponseWriter, code int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(body)
}

func envOr(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}

func (s *threadStore) clearPending(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.threads, key)
}
