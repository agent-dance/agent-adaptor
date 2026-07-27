package main

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/agent-dance/agent-adaptor/bridges/agui"
	"github.com/agent-dance/agent-adaptor/bridges/sse"
	"github.com/agent-dance/agent-adaptor/examples/internal/exampleutil"
	adaptor "github.com/agent-dance/agent-adaptor/next"
)

type appServer struct {
	agent  *adaptor.Agent
	driver string
	cors   string
	store  *threadStore
}

func newAppServer(agent *adaptor.Agent, driver, cors string) (*appServer, error) {
	store, err := newThreadStore()
	if err != nil {
		return nil, err
	}
	return &appServer{
		agent:  agent,
		driver: driver,
		cors:   cors,
		store:  store,
	}, nil
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

	// The browser's threadId is the host-owned conversation key; Thread turns
	// it into continue-or-start semantics against the agent's thread store.
	// Stream is the verb — there is no WithStreaming option to remember.
	stream := s.agent.Thread(invocation.threadKey).Stream(r.Context(), invocation.prompt, invocation.opts...)
	defer stream.Cancel()

	session := newAGUIRunSession(r.Context(), stream, s.store, invocation.threadID, invocation.userTurn, w)
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
	// `after` is a host-scoped cursor (sessionrecorder.HostSeq). Unified
	// events carry no cross-run sequence number of their own, so this cursor
	// is the only thing that stays monotonic when a thread spans several
	// runs. See docs/workstream-session-recorder.md.
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
	// The inbound DTO is protocol-level and unchanged across v1, so the
	// browser code needed no edit when the SDK moved to responder-carrying
	// approval requests.
	body, err := sse.DecodeDecisionResolveRequest(r)
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
	if writeApprovalError(w, s.store.resolveDecision(r.Context(), threadID, *body)) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *appServer) handleHealth(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

type agentInvocation struct {
	threadID  string
	threadKey string
	prompt    string
	opts      []adaptor.CallOption
	userTurn  []adaptor.Event
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

	// Two ID layers only (design doc §2.6): the host-owned thread key and the
	// SDK-assigned run id. The legacy "session namespace + key" pair collapses
	// into this one string.
	namespace, key := input.SessionKey()
	threadKey := namespace + "/" + key
	if namespace == "" || key == "" {
		threadKey = "agui/" + threadID
	}

	return agentInvocation{
		threadID:  threadID,
		threadKey: threadKey,
		prompt:    prompt,
		// Call scope: overrides for this invocation only. The agent-level
		// default policy was already installed by NewAGUIStreamingAgent;
		// repeating it here shows the "nearer scope wins" merge rule.
		opts:     []adaptor.CallOption{exampleutil.AGUIExamplePolicy()},
		userTurn: userTurnEvents(lastUserMessageID(input), prompt),
	}, nil
}

// lastUserMessageID returns the AG-UI message id of the latest user-role
// message so the recorded user turn keeps the browser's own id.
func lastUserMessageID(input *agui.RunAgentInput) string {
	for i := len(input.Messages) - 1; i >= 0; i-- {
		if input.Messages[i].Role == agui.RoleUser {
			return input.Messages[i].ID
		}
	}
	return ""
}
