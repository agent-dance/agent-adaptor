package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"sync"

	agentadaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/pkg/hosttools/sessionrecorder"
)

// threadStore is the example's host-owned recovery state. It persists the
// raw StreamPayload history and unresolved decisions per browser thread,
// so a page refresh can reconstruct the session without replay support
// from the SDK.
//
// History storage is delegated to sessionrecorder.Recorder, which assigns
// a session-scoped monotonic HostSeq to every payload. That cursor is
// what /session/events' `last_seq` and `after` parameters refer to —
// StreamPayload.Seq is per-run and restarts at zero on every new run, so
// it cannot carry cross-run recovery on its own. See
// docs/workstream-session-recorder.md for the full rationale.
//
// pending decisions and live run handles are kept in-process only; they
// evaporate when the RunHandle goes away, which is the intended lifetime.
type threadStore struct {
	recorder sessionrecorder.Recorder

	mu      sync.Mutex
	threads map[string]*threadRuntime
}

// threadRuntime carries the per-session state that is meaningful only
// while a run handle is alive. It is not persisted.
type threadRuntime struct {
	pending map[string]agentadaptor.DecisionRequest
	runs    map[agentadaptor.RunHandle]struct{}
}

// newThreadStore returns a threadStore backed by either a JSONL
// directory (when THREAD_STORE_DIR is set) or an in-memory backend
// (when it isn't — useful for demos and tests). The JSONL backend is
// the one to trust in "does refresh actually recover?" smoke tests.
func newThreadStore() *threadStore {
	return &threadStore{
		recorder: newRecorder(),
		threads:  map[string]*threadRuntime{},
	}
}

func newRecorder() sessionrecorder.Recorder {
	if dir := os.Getenv("THREAD_STORE_DIR"); dir != "" {
		be, err := sessionrecorder.NewJSONLBackend(dir)
		if err != nil {
			slog.Error("thread_store: jsonl backend init, falling back to memory", "err", err, "dir", dir)
			return sessionrecorder.New(sessionrecorder.NewMemoryBackend())
		}
		slog.Info("thread_store: jsonl backend", "dir", dir)
		return sessionrecorder.New(be)
	}
	return sessionrecorder.New(sessionrecorder.NewMemoryBackend())
}

// ---- history (delegated to sessionrecorder) ----

// appendHistory persists one payload under threadID. It is called on the
// hot path of StreamPayload forwarding, so it intentionally does not
// block the caller on backend errors: hosts that need fail-hard
// persistence should replace this with a wrapper that surfaces errors.
func (s *threadStore) appendHistory(threadID string, p agentadaptor.StreamPayload) {
	if _, err := s.recorder.Record(context.Background(), threadID, p); err != nil {
		slog.Error("thread_store: record payload", "err", err, "thread_id", threadID)
	}
}

// historyAfter returns the records with HostSeq strictly greater than
// afterHostSeq. afterHostSeq == 0 means "give me everything you have".
func (s *threadStore) historyAfter(threadID string, afterHostSeq uint64) []sessionrecorder.Record {
	recs, err := s.recorder.Since(context.Background(), threadID, afterHostSeq)
	if err != nil {
		slog.Error("thread_store: read history", "err", err, "thread_id", threadID)
		return nil
	}
	return recs
}

// ---- pending decisions and run handles (in-memory only) ----

func (s *threadStore) registerRun(threadID string, h agentadaptor.RunHandle) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.thread(threadID).runs[h] = struct{}{}
}

func (s *threadStore) unregisterRun(threadID string, h agentadaptor.RunHandle) {
	s.mu.Lock()
	defer s.mu.Unlock()

	st := s.thread(threadID)
	delete(st.runs, h)
	// Best-effort: once the handle is gone, any unresolved request owned
	// by that run can no longer be answered.
	for id, req := range st.pending {
		if req.RunID == h.RunID() {
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

func (s *threadStore) addPending(threadID string, req agentadaptor.DecisionRequest) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.thread(threadID).pending[req.RequestID] = req
}

func (s *threadStore) removePending(threadID, requestID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if st, ok := s.threads[threadID]; ok {
		delete(st.pending, requestID)
	}
}

func (s *threadStore) pendingRequests(threadID string) []agentadaptor.DecisionRequest {
	s.mu.Lock()
	defer s.mu.Unlock()

	st, ok := s.threads[threadID]
	if !ok {
		return nil
	}
	out := make([]agentadaptor.DecisionRequest, 0, len(st.pending))
	for _, req := range st.pending {
		out = append(out, req)
	}
	return out
}

func (s *threadStore) resolveDecision(threadID, requestID string, resp agentadaptor.DecisionResponse) error {
	s.mu.Lock()
	st, ok := s.threads[threadID]
	if !ok {
		s.mu.Unlock()
		return agentadaptor.ErrDecisionRequestExpired
	}
	handles := make([]agentadaptor.RunHandle, 0, len(st.runs))
	for h := range st.runs {
		handles = append(handles, h)
	}
	_, known := st.pending[requestID]
	s.mu.Unlock()

	if !known {
		return agentadaptor.ErrDecisionRequestExpired
	}

	var lastErr error
	for _, h := range handles {
		if err := h.ResolveDecision(requestID, resp); err == nil {
			return nil
		} else if !errors.Is(err, agentadaptor.ErrDecisionRequestExpired) {
			lastErr = err
		}
	}
	if lastErr != nil {
		return lastErr
	}
	return agentadaptor.ErrDecisionRequestExpired
}

func (s *threadStore) thread(threadID string) *threadRuntime {
	st := s.threads[threadID]
	if st == nil {
		st = &threadRuntime{
			pending: map[string]agentadaptor.DecisionRequest{},
			runs:    map[agentadaptor.RunHandle]struct{}{},
		}
		s.threads[threadID] = st
	}
	return st
}

// ---- misc helpers used by server.go ----

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
func lastHostSeq(records []sessionrecorder.Record) uint64 {
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
