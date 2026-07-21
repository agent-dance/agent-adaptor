package main

import (
	"context"
	"net/http"

	aguievents "github.com/ag-ui-protocol/ag-ui/sdks/community/go/pkg/core/events"
	aguisse "github.com/ag-ui-protocol/ag-ui/sdks/community/go/pkg/encoding/sse"

	agentadaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/pkg/bridges/agui"
)

type runStateStore interface {
	registerRun(threadID string, h agentadaptor.RunHandle)
	unregisterRun(threadID string, h agentadaptor.RunHandle)
	appendHistory(threadID string, p agentadaptor.StreamPayload)
	addPending(threadID string, req agentadaptor.DecisionRequest)
	removePending(threadID, requestID string)
}

// aguiRunSession is the host-side orchestration layer for one browser-visible
// run. It owns three concerns that the generic AG-UI bridge deliberately does
// not: teeing raw StreamPayload into host storage, tracking pending decisions,
// and writing translated AG-UI events to the HTTP response.
//
// # User-turn recording (canonical pattern)
//
// `sdk.Start(ctx, prompt, ...)` accepts the user prompt as an argument and
// never persists it anywhere SDK-side: SessionStore stores only resume
// metadata, and adapters emit assistant / tool / reasoning only. For the
// browser to recover its full transcript on refresh, the host MUST land
// the user turn into the same recorder that driver-side payloads flow
// into. The recipe used here:
//
//  1. Build the user-turn triple ONCE in handleAgent via
//     input.UserTurnPayloads(handle.RunID()) — see server.go.
//  2. Hand it to newAGUIRunSession so Serve() records it. CopilotKit already
//     renders the submitted user message optimistically, so this host does not
//     echo the synthetic turn through Translator/SSE and duplicate the bubble.
//  3. Land the triple BEFORE entering the StreamEvents() drain loop so
//     HostSeq(user) < HostSeq(first driver event). Recorder.Record is
//     monotonic per session key; the order is guaranteed by program
//     order in Serve().
//
// Reference: docs/workstream-user-message-event.md §6.1.
type aguiRunSession struct {
	ctx      context.Context
	threadID string
	handle   agentadaptor.RunHandle
	store    runStateStore

	// userTurn is the synthesized user-side text triple
	// (text.start/content/end with Role=RoleUser) the host wants to land
	// in the recorder before driver output starts. May be
	// nil when the AG-UI input carried no user-text turn.
	userTurn []agentadaptor.StreamPayload

	writer     *sseEventWriter
	translator *agui.Translator

	waitDone   chan struct{}
	waitResult agentadaptor.RunResult
	waitErr    error
}

func newAGUIRunSession(ctx context.Context, handle agentadaptor.RunHandle, store runStateStore, threadID string, userTurn []agentadaptor.StreamPayload, w http.ResponseWriter) *aguiRunSession {
	return &aguiRunSession{
		ctx:        ctx,
		threadID:   threadID,
		handle:     handle,
		store:      store,
		userTurn:   userTurn,
		writer:     newSSEEventWriter(ctx, w),
		translator: agui.NewTranslator(),
		waitDone:   make(chan struct{}),
	}
}

func (s *aguiRunSession) Serve() error {
	s.store.registerRun(s.threadID, s.handle)
	defer s.store.unregisterRun(s.threadID, s.handle)

	// Stage 1 — land the user turn into the recorder BEFORE draining driver
	// output. CopilotKit already owns the realtime user bubble, so this stage is
	// intentionally recorder-only.
	s.recordUserTurn()

	s.startWaiter()
	go s.watchDecisionRequests()

	if err := s.forwardStream(); err != nil {
		return err
	}
	<-s.waitDone
	return s.writeClosingEvents()
}

// recordUserTurn persists the synthesised user-side text triple without
// echoing it to CopilotKit. It is a no-op when the AG-UI input had no user-text
// turn (e.g. tool-result-only requests).
func (s *aguiRunSession) recordUserTurn() {
	for _, p := range s.userTurn {
		s.store.appendHistory(s.threadID, p)
	}
}

func (s *aguiRunSession) startWaiter() {
	go func() {
		defer close(s.waitDone)
		s.waitResult, s.waitErr = s.handle.Wait(s.ctx)
	}()
}

func (s *aguiRunSession) watchDecisionRequests() {
	for req := range s.handle.DecisionRequests() {
		s.store.addPending(s.threadID, req)
	}
}

func (s *aguiRunSession) forwardStream() error {
	stream := s.handle.StreamEvents()
	for {
		select {
		case payload, ok := <-stream:
			if !ok {
				return nil
			}
			if err := s.forwardPayload(payload); err != nil {
				return err
			}
		case <-s.ctx.Done():
			_ = s.handle.Cancel(context.WithoutCancel(s.ctx))
			return s.ctx.Err()
		}
	}
}

func (s *aguiRunSession) forwardPayload(payload agentadaptor.StreamPayload) error {
	s.store.appendHistory(s.threadID, payload)
	if payload.Kind == agentadaptor.StreamHITLResolved && payload.HITLResolved != nil {
		s.store.removePending(s.threadID, payload.HITLResolved.RequestID)
	}
	return s.writer.Write(s.translator.Translate(payload)...)
}

func (s *aguiRunSession) writeClosingEvents() error {
	if s.waitErr != nil {
		return s.writer.Write(s.translator.CloseRun(s.waitErr)...)
	}
	if s.waitResult.Failure != nil {
		return s.writer.Write(s.translator.Translate(agentadaptor.StreamPayload{
			Kind:  agentadaptor.StreamRunError,
			Error: s.waitResult.Failure,
		})...)
	}
	return s.writer.Write(s.translator.CloseRun(nil)...)
}

type sseEventWriter struct {
	ctx     context.Context
	writer  *aguisse.SSEWriter
	target  http.ResponseWriter
	flusher http.Flusher
}

func newSSEEventWriter(ctx context.Context, w http.ResponseWriter) *sseEventWriter {
	prepareSSEStreamResponse(w)
	flusher, _ := w.(http.Flusher)
	if flusher != nil {
		flusher.Flush()
	}
	return &sseEventWriter{
		ctx:     ctx,
		writer:  aguisse.NewSSEWriter(),
		target:  w,
		flusher: flusher,
	}
}

func (w *sseEventWriter) Write(events ...aguievents.Event) error {
	for _, ev := range events {
		if err := w.writer.WriteEvent(w.ctx, w.target, ev); err != nil {
			return err
		}
		if w.flusher != nil {
			w.flusher.Flush()
		}
	}
	return nil
}

func prepareSSEStreamResponse(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
}
