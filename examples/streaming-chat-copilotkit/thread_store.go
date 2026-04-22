package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"sync"

	agentadaptor "github.com/agent-dance/agent-adaptor"
)

// threadStore is the example's host-owned recovery state. It persists the raw
// StreamPayload history and unresolved decisions per browser thread, so a page
// refresh can reconstruct the session without replay support from the SDK.
type threadStore struct {
	mu      sync.RWMutex
	threads map[string]*threadState
}

type threadState struct {
	history []agentadaptor.StreamPayload // append-only; last 500 entries kept
	pending map[string]agentadaptor.DecisionRequest
	runs    map[agentadaptor.RunHandle]struct{}
}

const historyCap = 500

func newThreadStore() *threadStore {
	return &threadStore{threads: map[string]*threadState{}}
}

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
	// Best-effort: once the handle is gone, any unresolved request owned by
	// that run can no longer be answered.
	for id, req := range st.pending {
		if req.RunID == h.RunID() {
			delete(st.pending, id)
		}
	}
}

func (s *threadStore) hasActiveRun(threadID string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	st, ok := s.threads[threadID]
	return ok && len(st.runs) > 0
}

func (s *threadStore) appendHistory(threadID string, payload agentadaptor.StreamPayload) {
	s.mu.Lock()
	defer s.mu.Unlock()

	st := s.thread(threadID)
	st.history = append(st.history, payload)
	if len(st.history) > historyCap {
		st.history = st.history[len(st.history)-historyCap:]
	}
}

func (s *threadStore) historyAfter(threadID string, afterSeq uint64) []agentadaptor.StreamPayload {
	s.mu.RLock()
	defer s.mu.RUnlock()

	st, ok := s.threads[threadID]
	if !ok {
		return nil
	}
	out := make([]agentadaptor.StreamPayload, 0, len(st.history))
	for _, ev := range st.history {
		if ev.Seq <= afterSeq {
			continue
		}
		out = append(out, ev)
	}
	return out
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
	s.mu.RLock()
	defer s.mu.RUnlock()

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

func (s *threadStore) thread(threadID string) *threadState {
	st := s.threads[threadID]
	if st == nil {
		st = &threadState{
			pending: map[string]agentadaptor.DecisionRequest{},
			runs:    map[agentadaptor.RunHandle]struct{}{},
		}
		s.threads[threadID] = st
	}
	return st
}

func corsMiddleware(allowOrigin string, next http.Handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if allowOrigin != "" {
			w.Header().Set("Access-Control-Allow-Origin", allowOrigin)
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
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

func lastSeq(events []agentadaptor.StreamPayload) uint64 {
	if n := len(events); n > 0 {
		return events[n-1].Seq
	}
	return 0
}

func envOr(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}
