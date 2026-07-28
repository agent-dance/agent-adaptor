package sse

// Handler drives the SSE wire from adaptor.Runner / adaptor.Stream. There is
// no parallel execution implementation in this package.
//
//	agent := adaptor.New(codex.Driver(cfg))
//	http.Handle("/v1/chat", sse.Handler(agent, sse.Options{}))
//
// Wire contract:
//
//   - Protocol=AGUI: inbound POST RunAgentInput; outbound AG-UI event SSE.
//   - Protocol=Raw:  inbound POST {"prompt","sessionKey"} (GET debug helper);
//     outbound one SSE frame per unified event, `event:` named by kind.
//
// Disconnect-cancel semantics:
// the run is started with r.Context(), so a client disconnect cancels the
// request context, which cancels the run (stream.Cancel is also deferred);
// the streaming loop observes ctx.Done() and returns context.Canceled, which
// the handler suppresses rather than writing an error frame.
//
// Session binding: when the inbound request carries a session identity
// (AG-UI threadId → a collision-free ("agui", threadId) tuple, Raw
// sessionKey → the exact opaque string supplied by the host) and the
// configured Runner is an *adaptor.Agent with a thread store, the handler
// binds the run to agent.Thread(<key>). A Runner that is already a Thread
// ignores the inbound session identity (the thread is fixed by the host).
//
// Approvals: SSE is a one-way wire, so *adaptor.ApprovalRequest events are
// serialized informationally (Raw protocol: "decision.request" frames);
// hosts that need interactive approvals answer through adaptor.OnApproval
// or a companion endpoint holding the request's responder (design doc S3).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	aguisse "github.com/ag-ui-protocol/ag-ui/sdks/community/go/pkg/encoding/sse"

	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/bridges/agui"
	"github.com/agent-dance/agent-adaptor/bridges/internal/bridgekey"
)

// Options configures a Handler. Zero values select AG-UI protocol, no
// keep-alive ping, no CORS header, a 30s write timeout, and a 4 MiB POST
// request-body limit for both wire protocols.
type Options struct {
	// Protocol selects the wire contract. Default: AGUI.
	Protocol Protocol

	// KeepAlivePing, when non-zero, emits a periodic SSE comment frame so
	// intermediary proxies do not terminate idle streams. Default: off.
	KeepAlivePing time.Duration

	// CORSAllowedOrigin controls the Access-Control-Allow-Origin header.
	// Empty leaves CORS headers off.
	CORSAllowedOrigin string

	// WriteTimeout bounds every individual SSE frame write; it is not the
	// total run timeout (the request context governs the run). Default: 30s.
	WriteTimeout time.Duration

	// Options are appended to every Stream call at call scope.
	Options []adaptor.CallOption
}

// Handler returns an http.Handler that streams a Runner's unified events
// over SSE. POST is the canonical method; GET is accepted only for Raw
// protocol as a debugging convenience (prompt via ?prompt=…). All other
// methods return 405.
func Handler(runner adaptor.Runner, opts Options) http.Handler {
	if runner == nil {
		panic("sse.Handler: runner must not be nil")
	}
	if opts.WriteTimeout <= 0 {
		opts.WriteTimeout = 30 * time.Second
	}

	writer := aguisse.NewSSEWriter()

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if opts.CORSAllowedOrigin != "" {
			w.Header().Set("Access-Control-Allow-Origin", opts.CORSAllowedOrigin)
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Last-Event-ID")
			w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		allowGet := opts.Protocol == Raw
		if r.Method != http.MethodPost && !(allowGet && r.Method == http.MethodGet) {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		prompt, threadKey, err := decodeRequest(r, opts)
		if err != nil {
			status := http.StatusBadRequest
			message := err.Error()
			var tooLarge *http.MaxBytesError
			if errors.As(err, &tooLarge) {
				status = http.StatusRequestEntityTooLarge
				message = "request body exceeds 4 MiB limit"
			}
			http.Error(w, message, status)
			return
		}

		target := runner
		if threadKey != "" {
			if agent, ok := runner.(*adaptor.Agent); ok {
				target = agent.Thread(threadKey)
			}
		}

		// The run rides the request context: client disconnect → ctx cancel
		// → run cancel. Stream never fails to start; startup problems close
		// the event channel and surface through Result().
		stream := target.Stream(r.Context(), prompt, opts.Options...)
		defer stream.Cancel()

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("X-Accel-Buffering", "no")
		w.WriteHeader(http.StatusOK)

		deadlineWriter := newDeadlineResponseWriter(w, opts.WriteTimeout)
		defer deadlineWriter.clearDeadline()
		flusher, _ := any(deadlineWriter).(http.Flusher)
		writeMu := &sync.Mutex{}
		if flusher != nil {
			writeMu.Lock()
			flusher.Flush()
			writeMu.Unlock()
		}

		pingCtx, cancelPing := context.WithCancel(r.Context())
		defer cancelPing()
		if opts.KeepAlivePing > 0 && flusher != nil {
			go runKeepAlive(pingCtx, writeMu, deadlineWriter, flusher, opts.KeepAlivePing)
		}

		ctx := r.Context()
		lastEventID, hasLastEventID := parseLastEventID(r.Header.Get("Last-Event-ID"))
		err = streamEvents(ctx, writer, writeMu, deadlineWriter, stream, opts, lastEventID, hasLastEventID)
		if err != nil && !errors.Is(err, context.Canceled) {
			writeMu.Lock()
			if opts.Protocol == Raw {
				_ = writeRawFrame(deadlineWriter, flusher, "bridge.error", lastEventID+1, map[string]any{
					"code": "bridge.error", "message": err.Error(),
				})
			} else {
				_ = writer.WriteErrorEvent(ctx, deadlineWriter, err, "")
			}
			writeMu.Unlock()
		}
	})
}

// deadlineResponseWriter applies a fresh deadline to every low-level write.
// This covers multi-write AG-UI frames, Raw frames, flushes, and keep-alives
// without starting an unbounded goroutine per frame. Response writers that do
// not support write deadlines (notably httptest.ResponseRecorder) keep their
// normal behavior.
type deadlineResponseWriter struct {
	http.ResponseWriter
	controller *http.ResponseController
	timeout    time.Duration
}

func newDeadlineResponseWriter(w http.ResponseWriter, timeout time.Duration) *deadlineResponseWriter {
	return &deadlineResponseWriter{
		ResponseWriter: w,
		controller:     http.NewResponseController(w),
		timeout:        timeout,
	}
}

func (w *deadlineResponseWriter) Write(p []byte) (int, error) {
	if err := w.armDeadline(); err != nil {
		return 0, err
	}
	return w.ResponseWriter.Write(p)
}

func (w *deadlineResponseWriter) Flush() {
	if w.armDeadline() != nil {
		return
	}
	_ = w.controller.Flush()
}

func (w *deadlineResponseWriter) armDeadline() error {
	err := w.controller.SetWriteDeadline(time.Now().Add(w.timeout))
	if errors.Is(err, http.ErrNotSupported) {
		return nil
	}
	return err
}

func (w *deadlineResponseWriter) clearDeadline() {
	err := w.controller.SetWriteDeadline(time.Time{})
	if err != nil && !errors.Is(err, http.ErrNotSupported) {
		return
	}
}

// decodeRequest parses the request body according to Options.Protocol and
// returns (prompt, threadKey). threadKey is empty when the request carries
// no session identity. AG-UI's two-dimensional identity is length-prefixed
// before becoming one Thread key; Raw's sessionKey is already one opaque host
// key and is therefore preserved byte-for-byte.
func decodeRequest(r *http.Request, opts Options) (string, string, error) {
	switch opts.Protocol {
	case AGUI:
		input, err := agui.DecodeHTTPRequest(r)
		if err != nil {
			return "", "", err
		}
		prompt := input.LastUserText()
		if strings.TrimSpace(prompt) == "" {
			return "", "", errors.New("agui: no user message in RunAgentInput")
		}
		threadKey := ""
		if ns, key := input.SessionKey(); ns != "" {
			threadKey = bridgekey.Encode(ns, key)
		}
		return prompt, threadKey, nil

	case Raw:
		req, err := decodeRawRequest(r)
		if err != nil {
			return "", "", err
		}
		if strings.TrimSpace(req.Prompt) == "" {
			return "", "", errors.New("prompt is required")
		}
		threadKey := ""
		if req.SessionKey != "" {
			threadKey = req.SessionKey
		}
		return req.Prompt, threadKey, nil

	default:
		return "", "", fmt.Errorf("sse: unknown protocol %d", opts.Protocol)
	}
}

// streamEvents drains the stream into SSE frames according to the chosen
// protocol. It returns when the stream is exhausted or the context is
// cancelled.
func streamEvents(ctx context.Context, writer *aguisse.SSEWriter, writeMu *sync.Mutex, w io.Writer, stream adaptor.Stream, opts Options, lastEventID uint64, hasLastEventID bool) error {
	switch opts.Protocol {
	case AGUI:
		// agui.Events owns the AG-UI protocol invariants, including the
		// closing RUN_FINISHED / RUN_ERROR synthesis from stream.Result().
		for ev := range agui.EventsContext(ctx, stream) {
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}
			writeMu.Lock()
			err := writer.WriteEvent(ctx, w, ev)
			writeMu.Unlock()
			if err != nil {
				return err
			}
		}
		return nil
	case Raw:
		return streamRaw(ctx, writeMu, w, stream, lastEventID, hasLastEventID)
	default:
		return fmt.Errorf("sse: unknown protocol %d", opts.Protocol)
	}
}

// streamRaw forwards the unified event family directly, one SSE frame per
// event, without the AG-UI mapping. Approval traffic uses the stable frame
// names decision.request / decision.resolved. EventMeta.Sequence is the SSE
// cursor authority. Last-Event-ID seeds the fallback cursor only for events
// without metadata; replay storage remains a host responsibility.
//
// RunFinished is held until the channel is drained. Stream.Result() then
// decides the one terminal frame, so a misleading producer terminal can
// never hide an authoritative RunError or infrastructure failure.
func streamRaw(ctx context.Context, writeMu *sync.Mutex, w io.Writer, stream adaptor.Stream, lastEventID uint64, hasLastEventID bool) error {
	flusher, _ := w.(http.Flusher)
	id := uint64(0)
	if hasLastEventID {
		id = lastEventID
	}
	var terminal *adaptor.RunFinished
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case ev, ok := <-stream.Events():
			if !ok {
				res, runErr := stream.Result()
				name, body := rawTerminalFrame(stream.RunID(), terminal, res, runErr)
				id = nextRawEventID(nil, id)
				writeMu.Lock()
				err := writeRawFrame(w, flusher, name, id, body)
				writeMu.Unlock()
				return err
			}
			if finished, ok := ev.(adaptor.RunFinished); ok {
				copy := finished
				terminal = &copy
				continue
			}
			name, body := rawFrameFor(ev)
			if body == nil {
				continue
			}
			payload, err := json.Marshal(body)
			if err != nil {
				return fmt.Errorf("sse: marshal payload: %w", err)
			}
			id = nextRawEventID(ev, id)
			writeMu.Lock()
			err = writeRawPayload(w, flusher, name, id, payload)
			writeMu.Unlock()
			if err != nil {
				return fmt.Errorf("sse: write: %w", err)
			}
		}
	}
}

func nextRawEventID(ev adaptor.Event, previous uint64) uint64 {
	if ev != nil {
		if sequence := ev.Meta().Sequence; sequence > previous {
			return sequence
		}
	}
	return previous + 1
}

func writeRawFrame(w io.Writer, flusher http.Flusher, name string, id uint64, body any) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("sse: marshal payload: %w", err)
	}
	return writeRawPayload(w, flusher, name, id, payload)
}

func writeRawPayload(w io.Writer, flusher http.Flusher, name string, id uint64, payload []byte) error {
	if _, err := fmt.Fprintf(w, "event: %s\nid: %d\ndata: %s\n\n", escapeEventName(name), id, payload); err != nil {
		return fmt.Errorf("sse: write: %w", err)
	}
	if flusher != nil {
		flusher.Flush()
	}
	return nil
}

func parseLastEventID(raw string) (uint64, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, false
	}
	id, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		return 0, false
	}
	return id, true
}

// rawFrameFor picks the event name + JSON body for one unified event.
// Returns a nil body to drop the frame.
func rawFrameFor(ev adaptor.Event) (string, any) {
	name, body := rawFrameBody(ev)
	if body == nil {
		return name, nil
	}
	if object, ok := body.(map[string]any); ok {
		object["meta"] = rawEventMeta(ev.Meta())
	}
	return name, body
}

func rawFrameBody(ev adaptor.Event) (string, any) {
	switch e := ev.(type) {
	case adaptor.RunStarted:
		return "run.started", map[string]any{"run_id": e.RunID, "thread_id": e.ThreadID}
	case adaptor.RunFinished:
		body := map[string]any{"run_id": e.RunID, "thread_id": e.ThreadID, "failed": e.Failed}
		if e.Usage != nil {
			body["usage"] = e.Usage
		}
		if e.Failed {
			body["reason"] = string(e.Reason)
			body["message"] = e.Message
		}
		return "run.finished", body
	case adaptor.TextDelta:
		return "text.delta", map[string]any{
			"message_id": e.MessageID,
			"text":       e.Text,
			"role":       string(e.Role),
			"phase":      string(e.Phase),
		}
	case adaptor.Thinking:
		return "thinking.delta", map[string]any{
			"message_id": e.MessageID,
			"text":       e.Text,
			"phase":      string(e.Phase),
		}
	case adaptor.ToolCall:
		return "tool_call", map[string]any{
			"tool_call_id": e.ID,
			"name":         e.Name,
			"args":         e.Args,
			"args_delta":   e.ArgsDelta,
			"result":       e.Result,
			"phase":        string(e.Phase),
		}
	case adaptor.ToolResult:
		return "tool_call.result", map[string]any{"tool_call_id": e.ID, "result": e.Result}
	case adaptor.ProcessInfo:
		return "process." + e.Kind, map[string]any{
			"kind":     e.Kind,
			"text":     e.Text,
			"bytes":    e.Bytes,
			"metadata": e.Metadata,
			"data":     e.Data,
		}
	case adaptor.Notice:
		body := map[string]any{"kind": e.Kind, "text": e.Text, "data": e.Data}
		if e.Metadata != nil {
			body["metadata"] = e.Metadata
		}
		if e.Item != nil {
			body["item"] = e.Item
		}
		switch e.Kind {
		case adaptor.NoticeApprovalRequested:
			return "decision.request", body
		case adaptor.NoticeApprovalResolved:
			return "decision.resolved", body
		}
		return e.Kind, body
	case adaptor.Dropped:
		return "stream.dropped", map[string]any{
			"dropped_count":  e.Count,
			"by_kind":        e.ByKind,
			"first_sequence": e.FirstSequence,
			"last_sequence":  e.LastSequence,
			"reason":         e.Reason,
			"source":         e.Source,
			"details":        e.Details,
		}
	case adaptor.SubagentUpdate:
		return "subagent." + string(e.Kind), map[string]any{
			"agent": e.Agent,
			"delta": e.Delta,
			"data":  e.Data,
		}
	case *adaptor.ApprovalRequest:
		if e == nil {
			return "", nil
		}
		// Informational projection: the responder cannot ride a one-way
		// SSE wire. Keys are the stable approval request frame.
		return "decision.request", map[string]any{
			"request_id":    e.ID,
			"kind":          string(e.Kind),
			"source":        e.Source,
			"prompt":        e.Title,
			"payload":       e.Details,
			"choices":       e.Choices,
			"tool_call_id":  e.ToolCallID,
			"deadline":      e.Deadline,
			"retry_attempt": e.Attempt,
		}
	}
	return "", nil
}

func rawEventMeta(meta adaptor.EventMeta) map[string]any {
	out := map[string]any{
		"run_id": meta.RunID, "thread_key": meta.ThreadKey,
		"sequence": meta.Sequence, "time": meta.Time, "turn_id": meta.TurnID,
	}
	if meta.Source != nil {
		out["source"] = map[string]any{
			"run_id": meta.Source.RunID, "thread_id": meta.Source.ThreadID,
			"turn_id": meta.Source.TurnID, "sequence": meta.Source.Sequence,
			"timestamp": meta.Source.Timestamp,
		}
	}
	return out
}

func rawTerminalFrame(runID string, observed *adaptor.RunFinished, result *adaptor.Result, runErr error) (string, map[string]any) {
	if result != nil && result.RunID != "" {
		runID = result.RunID
	}
	threadID := ""
	if observed != nil {
		threadID = observed.ThreadID
		if runID == "" {
			runID = observed.RunID
		}
	}
	if runErr == nil {
		body := map[string]any{"run_id": runID, "thread_id": threadID, "failed": false}
		if observed != nil {
			body["meta"] = rawEventMeta(observed.Meta())
		}
		if observed != nil && observed.Usage != nil {
			body["usage"] = observed.Usage
		}
		if result != nil {
			body["result"] = map[string]any{
				"text": result.Text, "summary": result.Summary,
				"model": result.Model, "provider": result.Provider,
			}
		}
		return "run.finished", body
	}

	code := "run.error"
	message := runErr.Error()
	details := map[string]any{"layer": "infrastructure"}
	var business *adaptor.RunError
	switch {
	case errors.As(runErr, &business):
		code = string(business.Reason)
		message = nonEmpty(business.Message, message)
		details = business.Details
	case errors.Is(runErr, context.Canceled):
		code = string(adaptor.ReasonCancelled)
		details = map[string]any{"layer": "cancellation"}
	}
	body := map[string]any{
		"run_id": runID, "thread_id": threadID, "failed": true,
		"code": code, "message": message, "details": details,
	}
	if observed != nil {
		body["meta"] = rawEventMeta(observed.Meta())
	}
	return "run.error", body
}

func nonEmpty(value, fallback string) string {
	if value != "" {
		return value
	}
	return fallback
}
