package main

import (
	"context"
	"errors"
	"net/http"
	"strings"

	aguievents "github.com/ag-ui-protocol/ag-ui/sdks/community/go/pkg/core/events"
	aguisse "github.com/ag-ui-protocol/ag-ui/sdks/community/go/pkg/encoding/sse"

	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/bridges/agui"
)

type runStateStore interface {
	registerRun(threadID string, stream adaptor.Stream)
	unregisterRun(threadID string, stream adaptor.Stream)
	appendHistory(threadID string, ev adaptor.Event) error
	addPending(threadID string, req *adaptor.ApprovalRequest)
	removePending(threadID, requestID string)
}

// aguiRunSession is the host-side orchestration layer for one browser-visible
// run. It owns three concerns that the generic AG-UI bridge deliberately does
// not: teeing the event stream into host storage, tracking pending approvals,
// and writing translated AG-UI events to the HTTP response.
//
// Serve has one drain obligation: approvals arrive on the same channel as
// *adaptor.ApprovalRequest events, so a single for/range plus one Result() call
// covers the full run lifecycle.
//
// # User-turn recording (canonical pattern)
//
// Thread.Stream(ctx, prompt) takes the user prompt as an argument and never
// persists it anywhere SDK-side: the thread store keeps only resume metadata,
// and drivers emit assistant / tool / thinking events only. For the browser to
// recover its full transcript on refresh, the host MUST land the user turn
// into the same recorder driver-side events flow into. The recipe used here:
//
//  1. Build the user-turn triple ONCE in handleAgent via userTurnEvents —
//     see server.go.
//  2. Hand it to newAGUIRunSession so Serve() can play it through the
//     identical fan-out (recorder + EventTranslator + SSE) used for driver
//     events. The wire shape of user vs assistant TEXT_MESSAGE_* events
//     differs only in the AG-UI role tag, which comes from TextDelta.Role.
//  3. Land the triple BEFORE entering the Events() drain loop so
//     HostSeq(user) < HostSeq(first driver event). Recorder.Record is
//     monotonic per session key; the order is guaranteed by program order
//     in Serve().
type aguiRunSession struct {
	ctx      context.Context
	threadID string
	stream   adaptor.Stream
	store    runStateStore

	// userTurn is the synthesized user-side text triple (start/content/end
	// TextDeltas with Role=RoleUser) the host wants to land in the recorder
	// + SSE stream before driver output starts. May be nil when the AG-UI
	// input carried no user-text turn.
	userTurn []adaptor.Event

	writer     *sseEventWriter
	translator *agui.EventTranslator

	// assistantText records actual non-empty assistant content, not merely a
	// start/end lifecycle. It lets a non-streaming Driver's final Result.Text
	// take the same recorder + SSE path without duplicating streamed output.
	assistantText bool
}

func newAGUIRunSession(ctx context.Context, stream adaptor.Stream, store runStateStore, threadID string, userTurn []adaptor.Event, w http.ResponseWriter) *aguiRunSession {
	return &aguiRunSession{
		ctx:        ctx,
		threadID:   threadID,
		stream:     stream,
		store:      store,
		userTurn:   userTurn,
		writer:     newSSEEventWriter(ctx, w),
		translator: agui.NewEventTranslator(),
	}
}

func (s *aguiRunSession) Serve() error {
	s.store.registerRun(s.threadID, s.stream)
	defer s.store.unregisterRun(s.threadID, s.stream)

	// Stage 1 — land the user turn into recorder + SSE BEFORE draining
	// driver output. See the type docstring for the rationale. The
	// translator buffers anything produced before RunStarted and flushes it
	// straight after, so the user turn keeps its place at the front.
	if err := s.recordUserTurn(); err != nil {
		return err
	}

	// Stage 2 — one channel, one drain. Every event kind the demo does not
	// care about falls through the type switch untouched. The SDK's
	// RunFinished is informational, but it is still the terminal typed Event
	// in recovery history. Hold it until any Result.Text fallback has passed
	// through the recorder so history and AG-UI wire both remain terminal-last.
	var terminalEvents []adaptor.Event
	for ev := range s.stream.Events() {
		if _, terminal := ev.(adaptor.RunFinished); terminal {
			terminalEvents = append(terminalEvents, ev)
			continue
		}
		if err := s.forwardEvent(ev); err != nil {
			// The client hung up mid-run. Cancel, then keep draining: the
			// consumer owes the stream a full drain even when it stops
			// caring about the content.
			s.stream.Cancel()
			for range s.stream.Events() {
			}
			return err
		}
	}

	// Stage 3 — the single verdict. A Driver such as Cursor may expose its
	// assistant-facing answer only on Result.Text. If no assistant content was
	// streamed, send one complete message through the normal fan-out so both
	// realtime SSE and refresh history remain truthful. CloseResult then emits
	// the sole authoritative terminal and never duplicates the fallback.
	result, err := s.stream.Result()
	if !s.assistantText {
		if text := assistantResultText(result, err); strings.TrimSpace(text) != "" {
			for _, event := range assistantResultEvents(s.stream.RunID(), text) {
				if forwardErr := s.forwardEvent(event); forwardErr != nil {
					return forwardErr
				}
			}
		}
	}
	for _, event := range terminalEvents {
		if forwardErr := s.forwardEvent(event); forwardErr != nil {
			return forwardErr
		}
	}
	return s.writer.Write(s.translator.CloseResult(result, err)...)
}

// recordUserTurn replays the synthesised user-side text triple through the
// same fan-out as driver events. It is a no-op when the AG-UI input had no
// user-text turn (e.g. tool-result-only requests).
func (s *aguiRunSession) recordUserTurn() error {
	for _, ev := range s.userTurn {
		if err := s.forwardEvent(ev); err != nil {
			return err
		}
	}
	return nil
}

func (s *aguiRunSession) forwardEvent(ev adaptor.Event) error {
	if err := s.store.appendHistory(s.threadID, ev); err != nil {
		return err
	}

	switch e := ev.(type) {
	case adaptor.TextDelta:
		if e.Role != adaptor.RoleUser && strings.TrimSpace(e.Text) != "" {
			s.assistantText = true
		}
	case *adaptor.ApprovalRequest:
		// Form B: no OnApproval handler is installed, so the request
		// arrives here carrying its own responder. Park it; the browser
		// answers it later through POST /decision/resolve.
		s.store.addPending(s.threadID, e)
	case adaptor.Notice:
		// The SDK broadcasts every settled approval, including the ones it
		// resolved itself (policy auto-approve, timeout fallback), so the
		// pending list stays truthful without host bookkeeping.
		if e.Kind == adaptor.NoticeApprovalResolved {
			if id, ok := e.Data["request_id"].(string); ok {
				s.store.removePending(s.threadID, id)
			}
		}
	}

	return s.writer.Write(s.translator.Translate(ev)...)
}

func assistantResultText(result *adaptor.Result, err error) string {
	if result != nil {
		return result.Text
	}
	var runErr *adaptor.RunError
	if errors.As(err, &runErr) && runErr != nil && runErr.Result != nil {
		return runErr.Result.Text
	}
	return ""
}

func assistantResultEvents(runID, text string) []adaptor.Event {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	messageID := "adaptor-result-" + runID
	return []adaptor.Event{
		adaptor.TextDelta{MessageID: messageID, Phase: adaptor.PhaseStart},
		adaptor.TextDelta{MessageID: messageID, Text: text, Phase: adaptor.PhaseContent},
		adaptor.TextDelta{MessageID: messageID, Phase: adaptor.PhaseEnd},
	}
}

// userTurnEvents synthesizes the user's own message as the same TextDelta
// lifecycle a driver would emit for the assistant. Role is the only
// difference on the wire.
func userTurnEvents(messageID, text string) []adaptor.Event {
	if text == "" {
		return nil
	}
	if messageID == "" {
		messageID = "usr-" + text[:min(len(text), 8)]
	}
	return []adaptor.Event{
		adaptor.TextDelta{MessageID: messageID, Role: adaptor.RoleUser, Phase: adaptor.PhaseStart},
		adaptor.TextDelta{MessageID: messageID, Role: adaptor.RoleUser, Text: text, Phase: adaptor.PhaseContent},
		adaptor.TextDelta{MessageID: messageID, Role: adaptor.RoleUser, Phase: adaptor.PhaseEnd},
	}
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
