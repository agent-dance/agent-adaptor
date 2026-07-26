package sse

// This file is the v1-API twin of handler.go: the same SSE wire, driven by
// the new adaptor.Runner / adaptor.Stream surface instead of the legacy SDK
// handle. The legacy Handler stays untouched until P5 deletes it; both
// entries coexist additively (hence the V1 suffix — P5 collapses the names).
//
//	agent := adaptor.New(codex.Driver(cfg))
//	http.Handle("/v1/chat", sse.HandlerV1(agent, sse.OptionsV1{}))
//
// Wire contract (unchanged from the legacy handler):
//
//   - Protocol=AGUI: inbound POST RunAgentInput; outbound AG-UI event SSE.
//   - Protocol=Raw:  inbound POST {"prompt","sessionKey"} (GET debug helper);
//     outbound one SSE frame per unified event, `event:` named by kind.
//
// Disconnect-cancel semantics, aligned item-by-item with the legacy handler:
// the run is started with r.Context(), so a client disconnect cancels the
// request context, which cancels the run (stream.Cancel is also deferred);
// the streaming loop observes ctx.Done() and returns context.Canceled, which
// the handler suppresses rather than writing an error frame — exactly the
// legacy `!errors.Is(err, context.Canceled)` gate.
//
// Session binding: when the inbound request carries a session identity
// (AG-UI threadId → "agui/<threadId>", Raw sessionKey → "ns/key") and the
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
	"strings"
	"sync"
	"time"

	aguisse "github.com/ag-ui-protocol/ag-ui/sdks/community/go/pkg/encoding/sse"

	"github.com/agent-dance/agent-adaptor/bridges/agui"
	adaptor "github.com/agent-dance/agent-adaptor/next"
)

// OptionsV1 configures a HandlerV1. Zero values pick the same defaults as
// the legacy Options (AG-UI protocol, no keep-alive ping, no CORS header,
// 30s write timeout).
type OptionsV1 struct {
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

	// Options are appended to every Stream call (call scope, decision D7) —
	// the v1 equivalent of the legacy RunOptions field.
	Options []adaptor.CallOption
}

// HandlerV1 returns an http.Handler that streams a Runner's unified events
// over SSE. POST is the canonical method; GET is accepted only for Raw
// protocol as a debugging convenience (prompt via ?prompt=…). All other
// methods return 405.
func HandlerV1(runner adaptor.Runner, opts OptionsV1) http.Handler {
	if runner == nil {
		panic("sse.HandlerV1: runner must not be nil")
	}
	if opts.WriteTimeout <= 0 {
		opts.WriteTimeout = 30 * time.Second
	}

	writer := aguisse.NewSSEWriter()

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if opts.CORSAllowedOrigin != "" {
			w.Header().Set("Access-Control-Allow-Origin", opts.CORSAllowedOrigin)
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
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

		prompt, threadKey, err := decodeRequestV1(r, opts)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
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

		flusher, _ := w.(http.Flusher)
		writeMu := &sync.Mutex{}
		if flusher != nil {
			writeMu.Lock()
			flusher.Flush()
			writeMu.Unlock()
		}

		pingCtx, cancelPing := context.WithCancel(r.Context())
		defer cancelPing()
		if opts.KeepAlivePing > 0 && flusher != nil {
			go runKeepAlive(pingCtx, writeMu, w, flusher, opts.KeepAlivePing)
		}

		ctx := r.Context()
		err = streamEventsV1(ctx, writer, writeMu, w, stream, opts)
		if err != nil && !errors.Is(err, context.Canceled) {
			writeMu.Lock()
			_ = writer.WriteErrorEvent(ctx, w, err, "")
			writeMu.Unlock()
		}
	})
}

// decodeRequestV1 parses the request body according to Options.Protocol and
// returns (prompt, threadKey). threadKey is empty when the request carries
// no session identity; otherwise it is the v1 thread key derived exactly
// like the legacy session binding ("agui/<threadId>", "ns/key").
func decodeRequestV1(r *http.Request, opts OptionsV1) (string, string, error) {
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
			threadKey = ns + "/" + key
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
			ns, key := splitSessionKey(req.SessionKey)
			threadKey = ns + "/" + key
		}
		return req.Prompt, threadKey, nil

	default:
		return "", "", fmt.Errorf("sse: unknown protocol %d", opts.Protocol)
	}
}

// streamEventsV1 drains the stream into SSE frames according to the chosen
// protocol. It returns when the stream is exhausted or the context is
// cancelled.
func streamEventsV1(ctx context.Context, writer *aguisse.SSEWriter, writeMu *sync.Mutex, w io.Writer, stream adaptor.Stream, opts OptionsV1) error {
	switch opts.Protocol {
	case AGUI:
		// agui.Events owns the AG-UI protocol invariants, including the
		// closing RUN_FINISHED / RUN_ERROR synthesis from stream.Result().
		for ev := range agui.Events(stream) {
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
		return streamRawV1(ctx, writeMu, w, stream)
	default:
		return fmt.Errorf("sse: unknown protocol %d", opts.Protocol)
	}
}

// streamRawV1 forwards the unified event family directly, one SSE frame per
// event, without the AG-UI mapping. Approval traffic keeps the legacy frame
// names decision.request / decision.resolved. The SSE id is a run-local
// zero-based counter (the v1 event family carries no producer sequence).
func streamRawV1(ctx context.Context, writeMu *sync.Mutex, w io.Writer, stream adaptor.Stream) error {
	flusher, _ := w.(http.Flusher)
	var id uint64
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case ev, ok := <-stream.Events():
			if !ok {
				// Result() returns immediately once Events() closed; the
				// terminal outcome already reached the wire as run.finished
				// (legacy raw ignored Wait errors the same way).
				_, _ = stream.Result()
				return nil
			}
			name, body := rawFrameForV1(ev)
			if body == nil {
				continue
			}
			payload, err := json.Marshal(body)
			if err != nil {
				return fmt.Errorf("sse: marshal payload: %w", err)
			}
			writeMu.Lock()
			_, err = fmt.Fprintf(w, "event: %s\nid: %d\ndata: %s\n\n", escapeEventName(name), id, payload)
			if err == nil && flusher != nil {
				flusher.Flush()
			}
			writeMu.Unlock()
			if err != nil {
				return fmt.Errorf("sse: write: %w", err)
			}
			id++
		}
	}
}

// rawFrameForV1 picks the event name + JSON body for one unified event.
// Returns a nil body to drop the frame.
func rawFrameForV1(ev adaptor.Event) (string, any) {
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
		return "stream.dropped", map[string]any{"dropped_count": e.Count}
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
		// SSE wire. Keys mirror the legacy HITLRequestedPayload frame.
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
