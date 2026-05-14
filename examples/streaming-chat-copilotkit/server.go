package main

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	agentadaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/examples/internal/exampleutil"
	"github.com/agent-dance/agent-adaptor/pkg/bridges/agui"
	ssebridge "github.com/agent-dance/agent-adaptor/pkg/bridges/sse"
)

type appServer struct {
	sdk    agentadaptor.SDK
	driver string
	cors   string
	store  *threadStore
}

func newAppServer(sdk agentadaptor.SDK, driver, cors string) *appServer {
	return &appServer{
		sdk:    sdk,
		driver: driver,
		cors:   cors,
		store:  newThreadStore(),
	}
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
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	input, err := agui.DecodeHTTPRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	invocation, err := s.buildInvocation(input)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	handle, err := s.sdk.Start(r.Context(), invocation.prompt, invocation.opts...)
	if err != nil {
		http.Error(w, "start: "+err.Error(), http.StatusInternalServerError)
		return
	}
	defer handle.Cancel(r.Context())

	// Synthesize the user turn as a text.* triple tagged RoleUser so it
	// flows through the same fan-out (recorder + Translator + SSE) as
	// assistant text. See docs/workstream-user-message-event.md.
	userTurn := input.UserTurnPayloads(handle.RunID())
	session := newAGUIRunSession(r.Context(), handle, s.store, invocation.threadID, userTurn, w)
	if err := session.Serve(); err != nil && !errors.Is(err, r.Context().Err()) {
		slog.Warn("agui stream ended with error", "err", err, "thread_id", invocation.threadID)
	}
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
	// `after` is a host-scoped cursor (sessionrecorder.HostSeq), NOT
	// StreamPayload.Seq. HostSeq is monotonic across runs that share a
	// thread_id; StreamPayload.Seq restarts at zero on every new run and
	// would corrupt incremental recovery. See
	// docs/workstream-session-recorder.md.
	afterHostSeq, _ := strconv.ParseUint(r.URL.Query().Get("after"), 10, 64)
	records := s.store.historyAfter(threadID, afterHostSeq)
	writeJSON(w, http.StatusOK, map[string]any{
		"thread_id":  threadID,
		"after":      afterHostSeq,
		"events":     records,
		"last_seq":   lastHostSeq(records),
		"run_active": s.store.hasActiveRun(threadID),
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
		"pending":   s.store.pendingRequests(threadID),
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
	body, err := ssebridge.DecodeDecisionResolveRequest(r)
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
	if err := s.store.resolveDecision(threadID, body.RequestID, body.ToDecisionResponse()); err != nil {
		if ssebridge.WriteDecisionResolveError(w, err) {
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *appServer) handleHealth(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

type agentInvocation struct {
	threadID string
	prompt   string
	opts     []agentadaptor.RunOption
}

func (s *appServer) buildInvocation(input *agui.RunAgentInput) (agentInvocation, error) {
	prompt := input.LastUserText()
	if prompt == "" {
		return agentInvocation{}, errors.New("agui: no user message in RunAgentInput")
	}

	threadID := input.ThreadID
	if threadID == "" {
		threadID = "anon-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	}

	opts := []agentadaptor.RunOption{
		agentadaptor.WithStreaming(),
		exampleutil.AGUIExampleRunPolicy(),
	}
	if ns, key := input.SessionKey(); ns != "" {
		opts = append(opts, agentadaptor.WithSessionKey(ns, key))
	}

	return agentInvocation{
		threadID: threadID,
		prompt:   prompt,
		opts:     opts,
	}, nil
}
