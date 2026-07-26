package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"

	"github.com/agent-dance/agent-adaptor/bridges/sse"
	"github.com/agent-dance/agent-adaptor/hosttools/sessionrecorder"
	adaptor "github.com/agent-dance/agent-adaptor/next"
)

// threadStore is the example's host-owned recovery state. It persists the
// unified adaptor.Event history and the unresolved approvals per browser
// thread, so a page refresh can reconstruct the session without replay
// support from the SDK.
//
// History storage is delegated to sessionrecorder.EventRecorder, which
// assigns a session-scoped monotonic HostSeq to every event. That cursor is
// what /session/events' `last_seq` and `after` parameters refer to — events
// carry no cross-run sequence of their own, so the host cursor is the only
// thing that can survive a run boundary. See
// docs/workstream-session-recorder.md for the full rationale.
//
// v1 note: pending approvals are stored as live *adaptor.ApprovalRequest
// values. Decision D2 gives every request its own responder, so resolving
// one no longer means finding the owning run handle and routing a
// DecisionResponse through it — the parked request IS the responder. That
// deletes the whole "try every live handle until one accepts" fallback the
// legacy version needed.
type threadStore struct {
	recorder sessionrecorder.EventRecorder

	mu      sync.Mutex
	threads map[string]*threadRuntime
}

// threadRuntime carries the per-session state that is meaningful only
// while a stream is alive. It is not persisted.
type threadRuntime struct {
	pending map[string]*adaptor.ApprovalRequest
	runs    map[adaptor.Stream]struct{}
}

// newThreadStore returns a threadStore backed by the in-memory event
// backend.
//
// GAP (v1): sessionrecorder ships NewMemoryEventBackend for adaptor.Event
// but no JSONL EventBackend yet — NewJSONLBackend is still typed on the
// legacy StreamPayload. THREAD_STORE_DIR therefore only logs a warning
// instead of switching to durable storage.
func newThreadStore() *threadStore {
	if dir := os.Getenv("THREAD_STORE_DIR"); dir != "" {
		slog.Warn("thread_store: THREAD_STORE_DIR ignored; the v1 EventBackend has no JSONL implementation yet", "dir", dir)
	}
	return &threadStore{
		recorder: sessionrecorder.NewEventRecorder(sessionrecorder.NewMemoryEventBackend()),
		threads:  map[string]*threadRuntime{},
	}
}

// ---- history (delegated to sessionrecorder) ----

// appendHistory persists one event under threadID. It is called on the hot
// path of event forwarding, so it intentionally does not block the caller
// on backend errors: hosts that need fail-hard persistence should replace
// this with a wrapper that surfaces errors.
func (s *threadStore) appendHistory(threadID string, ev adaptor.Event) {
	if _, err := s.recorder.Record(context.Background(), threadID, ev); err != nil {
		slog.Error("thread_store: record event", "err", err, "thread_id", threadID)
	}
}

// historyAfter returns the records with HostSeq strictly greater than
// afterHostSeq. afterHostSeq == 0 means "give me everything you have".
func (s *threadStore) historyAfter(threadID string, afterHostSeq uint64) []sessionrecorder.EventRecord {
	recs, err := s.recorder.Since(context.Background(), threadID, afterHostSeq)
	if err != nil {
		slog.Error("thread_store: read history", "err", err, "thread_id", threadID)
		return nil
	}
	return recs
}

// ---- pending approvals and live streams (in-memory only) ----

func (s *threadStore) registerRun(threadID string, stream adaptor.Stream) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.thread(threadID).runs[stream] = struct{}{}
}

func (s *threadStore) unregisterRun(threadID string, stream adaptor.Stream) {
	s.mu.Lock()
	defer s.mu.Unlock()

	st := s.thread(threadID)
	delete(st.runs, stream)
	// Best-effort: once the run is gone, any request it owns can no longer
	// be answered — the responder's reply channel is abandoned.
	for id, req := range st.pending {
		if req.RunID == stream.RunID() {
			delete(st.pending, id)
		}
	}
}

func (s *threadStore) hasActiveRun(threadID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok := s.threads[threadID]
	return ok && len(st.runs) > 0
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
	return out
}

// resolveDecision answers a parked approval. The whole body is the v1
// approval contract in miniature: look the request up, then call exactly
// one of Approve / Deny / Answer on it. Wrong verb for the kind gives
// ErrApprovalKindMismatch; a second answer (or an answer to a request the
// SDK already expired) gives ErrApprovalResolved.
func (s *threadStore) resolveDecision(ctx context.Context, threadID string, body sse.DecisionResolveRequest) error {
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
	defer s.removePending(threadID, body.RequestID)

	switch strings.ToLower(strings.TrimSpace(body.Result)) {
	case "approved", "approve":
		return req.Approve(ctx)
	case "rejected", "reject", "denied", "deny":
		return req.Deny(ctx, body.Text)
	case "answered", "answer":
		option := body.Choice
		if option == "" {
			option = body.Text
		}
		return req.Answer(ctx, option)
	default:
		return fmt.Errorf("unsupported decision result %q", body.Result)
	}
}

func (s *threadStore) thread(threadID string) *threadRuntime {
	st := s.threads[threadID]
	if st == nil {
		st = &threadRuntime{
			pending: map[string]*adaptor.ApprovalRequest{},
			runs:    map[adaptor.Stream]struct{}{},
		}
		s.threads[threadID] = st
	}
	return st
}

// ---- misc helpers used by server.go ----

// writeApprovalError maps the v1 approval sentinels onto HTTP status codes.
//
// GAP (v1): bridges/sse.WriteDecisionResolveError still only understands the
// legacy ErrDecision* family, so the mapping is inlined here. The inbound
// DTO (sse.DecisionResolveRequest) is protocol-level and unchanged, which is
// why the browser contract needs no edit.
func writeApprovalError(w http.ResponseWriter, err error) bool {
	if err == nil {
		return false
	}
	switch {
	case errors.Is(err, adaptor.ErrApprovalResolved):
		http.Error(w, err.Error(), http.StatusGone)
	case errors.Is(err, adaptor.ErrApprovalKindMismatch):
		http.Error(w, err.Error(), http.StatusBadRequest)
	default:
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
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

// lastHostSeq returns the HostSeq of the last record, or 0 if records is
// empty. Callers send it back as the `last_seq` field so the browser can
// pass it as `after` on the next pull for incremental recovery.
func lastHostSeq(records []sessionrecorder.EventRecord) uint64 {
	if n := len(records); n > 0 {
		return records[n-1].HostSeq
	}
	return 0
}

func envOr(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}
