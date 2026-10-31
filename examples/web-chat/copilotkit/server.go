package main

import (
	"net/http"
	"strconv"

	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/bridges/agui/aguisession"

	"github.com/agent-dance/agent-adaptor/examples/internal/exampleutil"
)

type appServer struct {
	agent   *adaptor.Agent
	driver  string
	cors    string
	store   *threadStore
	session *aguisession.Session
}

func newAppServer(agent *adaptor.Agent, driver, cors string) (*appServer, error) {
	store, err := newThreadStore()
	if err != nil {
		return nil, err
	}
	server := &appServer{agent: agent, driver: driver, cors: cors, store: store}
	server.session = aguisession.New(agent, aguisession.Options{
		Recorder:    store.recorder,
		OnRunEnd:    store.clearPending,
		CallOptions: []adaptor.CallOption{exampleutil.AGUIExamplePolicy()},
		Observe:     server.observe,
	})
	return server, nil
}

func (s *appServer) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/agent", corsMiddleware(s.cors, http.HandlerFunc(s.handleAgent)))
	mux.HandleFunc("/session/events", corsMiddleware(s.cors, http.HandlerFunc(s.handleSessionEvents)))
	mux.HandleFunc("/decision/pending", corsMiddleware(s.cors, http.HandlerFunc(s.handleDecisionPending)))
	mux.HandleFunc("/decision/resolve", corsMiddleware(s.cors, http.HandlerFunc(s.handleDecisionResolve)))
	mux.HandleFunc("/health", corsMiddleware(s.cors, http.HandlerFunc(s.handleHealth)))
	return mux
}

func (s *appServer) handleAgent(w http.ResponseWriter, r *http.Request) {
	s.session.Handler().ServeHTTP(w, r)
}

func (s *appServer) handleSessionEvents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	threadID := r.URL.Query().Get("thread_id")
	if threadID == "" {
		http.Error(w, "thread_id required", http.StatusBadRequest)
		return
	}
	// `after` is a host-scoped cursor (sessionrecorder.HostSeq). Unified
	// events carry no cross-run sequence number of their own, so this cursor
	// is the only thing that stays monotonic when a thread spans several runs.
	afterHostSeq, _ := strconv.ParseUint(r.URL.Query().Get("after"), 10, 64)
	records := s.store.historyAfter(aguisession.RecorderKey(recordingKey(threadID)), afterHostSeq)
	writeJSON(w, http.StatusOK, map[string]any{
		"thread_id":  threadID,
		"after":      afterHostSeq,
		"events":     records,
		"last_seq":   lastHostSeq(records),
		"run_active": s.session.Active(recordingKey(threadID)),
	})
}

func (s *appServer) handleDecisionPending(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	threadID := r.URL.Query().Get("thread_id")
	if threadID == "" {
		http.Error(w, "thread_id required", http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"thread_id": threadID,
		"pending":   s.store.pendingRequests(recordingKey(threadID)),
	})
}

func (s *appServer) handleDecisionResolve(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	body, err := decodeApprovalResolveRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	threadID := body.RunID // example contract: browser-owned thread_id is sent via RunID
	if threadID == "" {
		threadID = r.URL.Query().Get("thread_id")
	}
	if threadID == "" {
		http.Error(w, "thread_id (RunID in body or ?thread_id=) required", http.StatusBadRequest)
		return
	}
	if writeApprovalError(w, s.store.resolveDecision(r.Context(), recordingKey(threadID), *body)) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *appServer) handleHealth(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

// recordingKey uses the same collision-free mapping as the session bridge.
func recordingKey(id string) string { return aguisession.ThreadKey(id) }

// observe maintains live responders from the sole recorded event drain.
func (s *appServer) observe(key string, ev adaptor.Event) {
	switch e := ev.(type) {
	case *adaptor.ApprovalRequest:
		s.store.addPending(key, e)
	case adaptor.Notice:
		if e.Kind == adaptor.NoticeApprovalResolved {
			if id, ok := e.Data["request_id"].(string); ok {
				s.store.removePending(key, id)
			}
		}
	}
}
