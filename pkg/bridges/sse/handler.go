// Package sse exposes a minimal HTTP Server-Sent Events handler that
// streams the agent-adaptor SDK's output over an AG-UI compatible wire.
// A host can mount the handler directly:
//
//	mux.Handle("/chat", sse.Handler(sdk, sse.Options{Protocol: sse.AGUI}))
//
// Protocol contract (determined entirely by Options.Protocol):
//
//   - Protocol=AGUI
//   - inbound:  POST body is an AG-UI RunAgentInput envelope
//     (see docs.ag-ui.com/sdk/js/core/types#runagentinput). The
//     bridge extracts the latest user-role message as the prompt
//     and maps threadId into the SDK session namespace as
//     "agui/<threadId>".
//   - outbound: SSE frames whose `data` field is an AG-UI event JSON
//     (RUN_STARTED, TEXT_MESSAGE_*, RUN_FINISHED, …).
//   - Protocol=Raw
//   - inbound:  POST body is {"prompt":"…", "sessionKey":"ns/key"}.
//   - outbound: SSE frames whose `data` field is a raw StreamPayload
//     JSON (adapter-native view, no AG-UI mapping).
//
// There is no per-handler decoder hook: the choice of Protocol fully
// determines both the request contract and the event wire. Hosts that
// need to inject cross-cutting policy (auth, rate limits, custom
// session derivation) should wrap the returned http.Handler with their
// own middleware rather than mutating the bridge.
//
// This package ships no authentication, authorization, or input
// sanitation. Hosts embed this handler behind whatever middleware their
// platform requires.
package sse

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

	aguievents "github.com/ag-ui-protocol/ag-ui/sdks/community/go/pkg/core/events"
	aguisse "github.com/ag-ui-protocol/ag-ui/sdks/community/go/pkg/encoding/sse"

	agentadaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/pkg/bridges/agui"
	"github.com/agent-dance/agent-adaptor/pkg/bridges/subagentstream"
)

// Protocol selects the full on-wire contract (inbound + outbound).
type Protocol int

const (
	// AGUI is the default. Inbound: AG-UI RunAgentInput. Outbound:
	// AG-UI event stream. This is what CopilotKit-style clients expect.
	AGUI Protocol = iota
	// Raw is the adapter-native contract. Inbound: {"prompt", "sessionKey"}.
	// Outbound: StreamPayload JSON. Use when the host drives the SDK
	// directly and does not want an AG-UI mapping in the middle.
	Raw
)

// Run metadata keys the AGUI protocol sets from the resolved session identity.
// Host hooks (RuntimeServiceManager / WorkspaceManager) can read these from
// RuntimeServiceRequest.Metadata / WorkspaceRequest.Metadata to key
// session-scoped resources, since those request types carry no session field.
const (
	// MetadataSessionNamespace is the resolved session namespace (e.g. "agui").
	MetadataSessionNamespace = "agentadaptor.session.namespace"
	// MetadataSessionKey is the resolved session key (e.g. the AG-UI threadId).
	MetadataSessionKey = "agentadaptor.session.key"
)

// Options configures a Handler. Zero values pick sensible defaults
// (AG-UI protocol, no keep-alive ping, no CORS header, 30s write
// timeout).
type Options struct {
	// Protocol selects the wire contract. See package doc for the full
	// inbound/outbound mapping of each value. Default: AGUI.
	Protocol Protocol

	// KeepAlivePing, when non-zero, emits a periodic SSE comment frame
	// so intermediary proxies do not terminate idle streams. Default:
	// off.
	KeepAlivePing time.Duration

	// CORSAllowedOrigin controls the Access-Control-Allow-Origin
	// header. The empty string leaves CORS headers off. Use "*" to
	// allow any origin (the simplest development choice) or a specific
	// origin.
	CORSAllowedOrigin string

	// WriteTimeout bounds every individual SSE frame write; it is not
	// the total run timeout. Default: 30s. The handler uses the
	// incoming request's context for the run duration.
	WriteTimeout time.Duration

	// RunOptions lets the host append additional RunOptions to every
	// incoming request (for example, a per-handler WithSkills choice).
	// They apply before any options derived from the request body, so
	// the body can still override them (e.g. session binding).
	RunOptions []agentadaptor.RunOption

	// SubagentBus, when set, overlays host-published visual subagent delegation
	// events onto the AG-UI response stream. Default mode (SubagentAsActivity)
	// emits ACTIVITY_SNAPSHOT / ACTIVITY_DELTA events per SubagentID. Use
	// SubagentAsCustom to emit legacy CUSTOM events.
	SubagentBus subagentstream.EventBus

	// SubagentMode selects how SubagentBus events are translated to AG-UI events.
	// Default (zero value) is SubagentAsActivity. Ignored when SubagentBus is nil.
	SubagentMode agui.SubagentMode
}

// RawRequest is the canonical Raw-protocol chat request body.
// Unexported for AGUI callers; only hosts that choose Protocol=Raw see
// this shape on the wire.
type RawRequest struct {
	// Prompt is the user text for this turn.
	Prompt string `json:"prompt"`
	// SessionKey is an optional "namespace/key" identifier used by the
	// SDK to resume the same thread across calls. Empty runs stateless.
	SessionKey string `json:"sessionKey,omitempty"`
}

// Handler returns an http.Handler that streams chat events over SSE.
// POST is the canonical method; GET is accepted only for Raw protocol
// as a debugging convenience (prompt via ?prompt=…). All other methods
// return 405.
func Handler(sdk agentadaptor.SDK, opts Options) http.Handler {
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
		// GET is only meaningful for Raw protocol (debug helper);
		// AGUI mandates a POST body with the RunAgentInput envelope.
		allowGet := opts.Protocol == Raw
		if r.Method != http.MethodPost && !(allowGet && r.Method == http.MethodGet) {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		prompt, runOpts, err := decodeByProtocol(r, opts)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		handle, err := sdk.Start(r.Context(), prompt, runOpts...)
		if err != nil {
			http.Error(w, "start: "+err.Error(), http.StatusInternalServerError)
			return
		}
		defer handle.Cancel(r.Context())

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
		err = streamEvents(ctx, writer, writeMu, w, handle, opts)
		if err != nil && !errors.Is(err, context.Canceled) {
			writeMu.Lock()
			_ = writer.WriteErrorEvent(ctx, w, err, "")
			writeMu.Unlock()
		}
	})
}

// decodeByProtocol parses the request body according to Options.Protocol
// and returns (prompt, runOpts). It is the single choke point where the
// inbound contract is interpreted. runOpts always has WithStreaming()
// appended last, plus any host-level options provided in opts.RunOptions
// and the protocol-derived session binding (if any).
func decodeByProtocol(r *http.Request, opts Options) (string, []agentadaptor.RunOption, error) {
	runOpts := append([]agentadaptor.RunOption(nil), opts.RunOptions...)

	switch opts.Protocol {
	case AGUI:
		input, err := agui.DecodeHTTPRequest(r)
		if err != nil {
			return "", nil, err
		}
		prompt := input.LastUserText()
		if strings.TrimSpace(prompt) == "" {
			return "", nil, errors.New("agui: no user message in RunAgentInput")
		}
		if ns, key := input.SessionKey(); ns != "" {
			runOpts = append(runOpts, agentadaptor.WithSessionKey(ns, key))
			// Also surface the resolved session identity as run metadata so
			// host hooks (WorkspaceManager / RuntimeServiceManager) can key
			// session-scoped resources by it. RuntimeServiceRequest carries no
			// session field, so a host that needs to reuse one sidecar across a
			// thread's turns (e.g. a stable MCP delegation endpoint for a
			// persistent agent process) relies on this metadata.
			runOpts = append(runOpts,
				agentadaptor.WithMetadata(MetadataSessionNamespace, ns),
				agentadaptor.WithMetadata(MetadataSessionKey, key),
			)
		}
		runOpts = append(runOpts, agentadaptor.WithStreaming())
		return prompt, runOpts, nil

	case Raw:
		req, err := decodeRawRequest(r)
		if err != nil {
			return "", nil, err
		}
		if strings.TrimSpace(req.Prompt) == "" {
			return "", nil, errors.New("prompt is required")
		}
		if req.SessionKey != "" {
			ns, key := splitSessionKey(req.SessionKey)
			runOpts = append(runOpts, agentadaptor.WithSessionKey(ns, key))
		}
		runOpts = append(runOpts, agentadaptor.WithStreaming())
		return req.Prompt, runOpts, nil

	default:
		return "", nil, fmt.Errorf("sse: unknown protocol %d", opts.Protocol)
	}
}

// decodeRawRequest is the Raw-protocol body parser: JSON on POST, query
// parameters on GET.
func decodeRawRequest(r *http.Request) (*RawRequest, error) {
	if r.Method == http.MethodGet {
		return &RawRequest{
			Prompt:     r.URL.Query().Get("prompt"),
			SessionKey: r.URL.Query().Get("sessionKey"),
		}, nil
	}
	defer r.Body.Close()
	var req RawRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, errors.New("request body is empty")
		}
		return nil, fmt.Errorf("decode request: %w", err)
	}
	return &req, nil
}

// streamEvents drains the handle's streams into SSE frames according to
// the chosen protocol. It returns when the stream is exhausted or the
// context is cancelled.
func streamEvents(ctx context.Context, writer *aguisse.SSEWriter, writeMu *sync.Mutex, w io.Writer, handle agentadaptor.RunHandle, opts Options) error {
	switch opts.Protocol {
	case AGUI:
		var out <-chan aguievents.Event
		if opts.SubagentBus != nil {
			out = subagentstream.WrapAGUI(ctx, handle, subagentstream.MuxOptions{
				Bus:          opts.SubagentBus,
				SubagentMode: opts.SubagentMode,
			})
		} else {
			out = agui.WrapWithContext(ctx, handle)
		}
		for ev := range out {
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
		return streamRaw(ctx, writeMu, w, handle)
	default:
		return fmt.Errorf("sse: unknown protocol %d", opts.Protocol)
	}
}

// streamRaw forwards StreamPayload JSON directly without mapping to AG-UI.
// HITL events are renamed to decision.request / decision.resolved and their
// body is the corresponding structured payload (HITLRequestedPayload /
// HITLResolvedPayload) — see docs/workstream-hitl-v2.md §6.2.
func streamRaw(ctx context.Context, writeMu *sync.Mutex, w io.Writer, handle agentadaptor.RunHandle) error {
	flusher, _ := w.(http.Flusher)
	done := make(chan agentadaptor.RunResult, 1)
	go func() {
		r, _ := handle.Wait(ctx)
		done <- r
	}()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case p, ok := <-handle.StreamEvents():
			if !ok {
				<-done
				return nil
			}
			name, body := rawFrameFor(p)
			if body == nil {
				continue
			}
			payload, err := json.Marshal(body)
			if err != nil {
				return fmt.Errorf("sse: marshal payload: %w", err)
			}
			// Prefer Seq (run-local zero-based) over legacy Sequence for
			// SSE id so EventSource Last-Event-ID works with §4.3.1 protocol.
			id := p.Seq
			if id == 0 {
				id = p.Sequence
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
		}
	}
}

// rawFrameFor picks the event name + body for an SSE frame. Returns a nil
// body to drop the payload (should not happen for known kinds).
func rawFrameFor(p agentadaptor.StreamPayload) (string, any) {
	switch p.Kind {
	case agentadaptor.StreamHITLRequested:
		if p.HITLRequested != nil {
			return "decision.request", p.HITLRequested
		}
		return "decision.request", p
	case agentadaptor.StreamHITLResolved:
		if p.HITLResolved != nil {
			return "decision.resolved", p.HITLResolved
		}
		return "decision.resolved", p
	}
	return string(p.Kind), p
}

func runKeepAlive(ctx context.Context, writeMu *sync.Mutex, w io.Writer, flusher http.Flusher, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			writeMu.Lock()
			_, err := io.WriteString(w, ":keep-alive\n\n")
			if err == nil {
				flusher.Flush()
			}
			writeMu.Unlock()
			if err != nil {
				return
			}
		}
	}
}

// splitSessionKey splits a "namespace/key" form. When no slash is
// present the whole string becomes the key and namespace defaults to
// "default" — matching the Raw-protocol semantics hosts already rely
// on.
func splitSessionKey(raw string) (namespace, key string) {
	idx := strings.Index(raw, "/")
	if idx < 0 {
		return "default", raw
	}
	return raw[:idx], raw[idx+1:]
}

// escapeEventName strips control bytes from a StreamKind so it is safe
// to embed in an `event:` header. SSE forbids CR / LF inside that
// field.
func escapeEventName(name string) string {
	if name == "" {
		return "payload"
	}
	replaced := strings.Map(func(r rune) rune {
		switch r {
		case '\n', '\r':
			return ' '
		}
		return r
	}, name)
	return replaced
}
