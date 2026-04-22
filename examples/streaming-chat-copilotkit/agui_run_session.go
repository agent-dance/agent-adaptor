package main

import (
	"context"
	"errors"
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
type aguiRunSession struct {
	ctx      context.Context
	threadID string
	handle   agentadaptor.RunHandle
	store    runStateStore

	writer     *sseEventWriter
	translator *agui.Translator

	waitDone chan struct{}
	waitErr  error
}

func newAGUIRunSession(ctx context.Context, handle agentadaptor.RunHandle, store runStateStore, threadID string, w http.ResponseWriter) *aguiRunSession {
	return &aguiRunSession{
		ctx:        ctx,
		threadID:   threadID,
		handle:     handle,
		store:      store,
		writer:     newSSEEventWriter(ctx, w),
		translator: agui.NewTranslator(),
		waitDone:   make(chan struct{}),
	}
}

func (s *aguiRunSession) Serve() error {
	s.store.registerRun(s.threadID, s.handle)
	defer s.store.unregisterRun(s.threadID, s.handle)

	s.startWaiter()
	go s.watchDecisionRequests()

	if err := s.forwardStream(); err != nil {
		return err
	}
	<-s.waitDone
	return s.writeClosingEvents()
}

func (s *aguiRunSession) startWaiter() {
	go func() {
		defer close(s.waitDone)
		_, s.waitErr = s.handle.Wait(s.ctx)
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
	closing := s.translator.Translate(agentadaptor.StreamPayload{Kind: agentadaptor.StreamRunFinished})
	if s.waitErr != nil {
		code := "run.error"
		if errors.Is(s.waitErr, context.Canceled) {
			code = "run.cancelled"
		}
		closing = s.translator.Translate(agentadaptor.StreamPayload{
			Kind: agentadaptor.StreamRunError,
			Error: &agentadaptor.RunFailure{
				Code:    agentadaptor.FailureCode(code),
				Message: s.waitErr.Error(),
			},
		})
	}
	return s.writer.Write(closing...)
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
