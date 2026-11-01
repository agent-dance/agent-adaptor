package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"time"

	adaptor "github.com/agent-dance/agent-adaptor/v2"
	"github.com/agent-dance/agent-adaptor/v2/bridges/agui"
	"github.com/agent-dance/agent-adaptor/v2/examples/internal/exampleutil"
)

type appServer struct {
	agent  *adaptor.Agent
	driver string
	cors   string
	store  *threadStore
	runs   agui.RunRegistry
	// Entries share the lifetime of the host's per-thread recovery state.
	historyBarriers sync.Map // thread ID -> *historyBarrier
}

func (s *appServer) historyBarrier(threadID string) *historyBarrier {
	barrier, _ := s.historyBarriers.LoadOrStore(threadID, new(historyBarrier))
	return barrier.(*historyBarrier)
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
	mux.HandleFunc("/message", corsMiddleware(s.cors, http.HandlerFunc(s.handleMessage)))
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
	if err := r.Context().Err(); err != nil {
		writeMessageAdmissionError(w, err)
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

	if err := r.Context().Err(); err != nil {
		writeMessageAdmissionError(w, err)
		return
	}
	// This host deliberately lets supplemental input survive its companion
	// HTTP request. Once dispatch begins, even a full admission queue waits
	// for space or the original execution's end. New executions are bound
	// back to their original SSE request immediately after registration.
	barrier := s.historyBarrier(invocation.threadID)
	barrier.begin()
	stream, supplement, err := s.runs.Send(context.WithoutCancel(r.Context()), s.agent.Thread(invocation.threadKey), input, invocation.opts...)
	if err != nil {
		barrier.end()
		writeMessageAdmissionError(w, err)
		return
	}
	if supplement {
		// Delivery receipts flow on the original run's SSE connection and
		// recorder. This response acknowledges admission only.
		s.acknowledgeSupplement(w, invocation.threadID, stream, input, barrier)
		return
	}
	barrier.end()
	stop := context.AfterFunc(r.Context(), stream.Cancel)
	defer stop()
	defer s.runs.Forget(invocation.threadKey, stream)
	defer stream.Cancel()

	session := newAGUIRunSession(r.Context(), stream, s.store, invocation.threadID, invocation.userTurn, w)
	session.admissions = barrier
	if err := session.Serve(); err != nil && !errors.Is(err, r.Context().Err()) {
		slog.Warn("agui stream ended with error", "err", err, "thread_id", invocation.threadID)
	}
}

// handleMessage is the companion endpoint for input while /agent's SSE
// response is open. It does not create another AG-UI run or event consumer.
// A request already cancelled before dispatch is rejected. Once dispatch
// begins, the input and any admission wait belong to the original execution;
// cancelling this companion HTTP request does not withdraw them.
func (s *appServer) handleMessage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := r.Context().Err(); err != nil {
		writeMessageAdmissionError(w, err)
		return
	}
	defer r.Body.Close()
	var request struct {
		ThreadID  string `json:"threadId"`
		MessageID string `json:"messageId"`
		Text      string `json:"text"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		var tooLarge *http.MaxBytesError
		status := http.StatusBadRequest
		if errors.As(err, &tooLarge) {
			status = http.StatusRequestEntityTooLarge
		}
		http.Error(w, err.Error(), status)
		return
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		http.Error(w, "a single JSON object is required", http.StatusBadRequest)
		return
	}
	if request.ThreadID == "" || request.MessageID == "" || request.Text == "" {
		http.Error(w, "threadId, messageId and text are required", http.StatusBadRequest)
		return
	}
	content, _ := json.Marshal(request.Text)
	input := &agui.RunAgentInput{
		ThreadID: request.ThreadID,
		Messages: []agui.Message{{ID: request.MessageID, Role: agui.RoleUser, Content: content}},
	}
	if err := r.Context().Err(); err != nil {
		writeMessageAdmissionError(w, err)
		return
	}
	barrier := s.historyBarrier(request.ThreadID)
	barrier.begin()
	stream, err := s.runs.SendActive(context.WithoutCancel(r.Context()), agui.ThreadKey(request.ThreadID), input)
	if err != nil {
		barrier.end()
		writeMessageAdmissionError(w, err)
		return
	}
	s.acknowledgeSupplement(w, request.ThreadID, stream, input, barrier)
}

func writeMessageAdmissionError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	var rejected *adaptor.MessageError
	switch {
	case errors.Is(err, adaptor.ErrRunFinished):
		status = http.StatusConflict
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		status = http.StatusRequestTimeout
	case errors.As(err, &rejected):
		status = http.StatusUnprocessableEntity
	}
	http.Error(w, err.Error(), status)
}

func (s *appServer) acknowledgeSupplement(w http.ResponseWriter, threadID string, stream adaptor.Stream, input *agui.RunAgentInput, barrier *historyBarrier) {
	response := map[string]any{"runId": stream.RunID(), "messageId": input.LastUserMessageID(), "state": "accepted"}
	status := http.StatusAccepted
	for _, event := range userTurnEvents(input.LastUserMessageID(), input.LastUserText()) {
		if err := s.store.appendHistory(threadID, event); err != nil {
			// Admission has already succeeded. Keep it explicit even when the
			// configured history backend fails, so retries can use the same ID.
			response["error"] = err.Error()
			status = http.StatusInternalServerError
			break
		}
	}
	// Terminal recording waits for history, never for the companion's network.
	barrier.end()
	writeJSON(w, status, response)
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

	threadKey := agui.ThreadKey(threadID)

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
	return input.LastUserMessageID()
}
