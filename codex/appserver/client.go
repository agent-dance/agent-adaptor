package appserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/sourcegraph/jsonrpc2"
)

// Method names for ClientRequest and notifications. Only the subset the
// codex adapter actually uses is listed; unknown notifications still reach
// the subscriber map as raw payloads so translate.go can forward them
// through StreamPayload.Raw.
const (
	// MethodInitialize performs the JSON-RPC initialize handshake.
	MethodInitialize = "initialize"
	// MethodInitialized notifies the server that the client is ready.
	MethodInitialized = "initialized"
	// MethodThreadStart starts a new codex thread.
	MethodThreadStart = "thread/start"
	// MethodThreadResume resumes an existing codex thread.
	MethodThreadResume = "thread/resume"
	// MethodTurnStart starts one turn inside a thread.
	MethodTurnStart = "turn/start"
	// MethodTurnInterrupt interrupts an in-flight turn.
	MethodTurnInterrupt = "turn/interrupt"

	// NotifyThreadStarted reports that a thread was created/resumed.
	NotifyThreadStarted = "thread/started"
	// NotifyThreadStatusChanged reports thread status transitions.
	NotifyThreadStatusChanged = "thread/status/changed"
	// NotifyThreadTokenUsageUpdated reports cumulative token usage.
	NotifyThreadTokenUsageUpdated = "thread/tokenUsage/updated"
	// NotifyTurnStarted reports that a turn began.
	NotifyTurnStarted = "turn/started"
	// NotifyTurnCompleted reports normal turn completion.
	NotifyTurnCompleted = "turn/completed"
	// NotifyItemStarted reports the start of a thread item lifecycle.
	NotifyItemStarted = "item/started"
	// NotifyItemCompleted reports the end of a thread item lifecycle.
	NotifyItemCompleted = "item/completed"
	// NotifyItemAgentMessageDelta carries assistant text deltas.
	NotifyItemAgentMessageDelta = "item/agentMessage/delta"
	// NotifyItemReasoningTextDelta carries reasoning text deltas.
	NotifyItemReasoningTextDelta = "item/reasoning/textDelta"
	// NotifyItemReasoningSummaryTextDelta carries reasoning-summary deltas.
	NotifyItemReasoningSummaryTextDelta = "item/reasoning/summaryTextDelta"
	// NotifyItemReasoningSummaryPartAdded reports a new reasoning-summary part.
	NotifyItemReasoningSummaryPartAdded = "item/reasoning/summaryPartAdded"
	// NotifyItemCommandExecutionOutputDelta carries command output deltas.
	NotifyItemCommandExecutionOutputDelta = "item/commandExecution/outputDelta"
	// NotifyCommandExecOutputDelta carries legacy command output deltas.
	NotifyCommandExecOutputDelta = "command/exec/outputDelta"
	// NotifyItemFileChangeOutputDelta carries file-change output deltas.
	NotifyItemFileChangeOutputDelta = "item/fileChange/outputDelta"
	// NotifyItemPlanDelta carries plan text deltas.
	NotifyItemPlanDelta = "item/plan/delta"
	// NotifyError carries a server-side error notification.
	NotifyError = "error"
)

// NotificationHandler receives one decoded server notification. The raw
// JSON params are provided so handlers can decode into whatever typed
// shape they need (see translate.go).
//
// Ordering contract: the underlying sourcegraph/jsonrpc2 dispatcher
// invokes Handler.Handle synchronously — each call must return before
// the next wire frame is dispatched — so this handler sees every
// notification in strict wire order. Keep the function inexpensive; any
// heavy work should be queued elsewhere. Blocking here is what
// preserves the order guarantee downstream.
type NotificationHandler func(method string, params json.RawMessage)

// Client is a thin, strongly-typed wrapper around
// sourcegraph/jsonrpc2.Conn. It hides the minimal JSON-RPC plumbing
// adapters should never need to re-implement.
//
// Why sourcegraph/jsonrpc2 rather than creachadair/jrpc2:
//   - Handler.Handle is called synchronously per inbound frame, giving
//     us a hard FIFO ordering contract without any extra wrapping.
//     jrpc2 spawned a goroutine per notification and serialized them
//     behind a sync.Mutex, whose non-FIFO wake order reordered codex
//     token deltas in practice.
//   - No "jsonrpc":"2.0" strictness — codex app-server omits the marker
//     on many frames, so no tolerant codec is needed.
type Client struct {
	conn *jsonrpc2.Conn

	handlerMu sync.RWMutex
	handler   NotificationHandler
	closed    atomic.Bool
}

// NewClient takes ownership of the ObjectStream and spins up the
// JSON-RPC dispatcher goroutine. The caller is responsible for
// producing the stream (usually by spawning
// `codex app-server --listen stdio://` and handing over its stdio).
func NewClient(ctx context.Context, stream jsonrpc2.ObjectStream) *Client {
	c := &Client{}
	h := &connHandler{client: c}
	c.conn = jsonrpc2.NewConn(ctx, stream, h)
	return c
}

// SetNotificationHandler registers the single handler that receives all
// server-initiated notifications. Calling it more than once replaces the
// previous handler. A nil handler disables delivery without failing.
func (c *Client) SetNotificationHandler(h NotificationHandler) {
	c.handlerMu.Lock()
	defer c.handlerMu.Unlock()
	c.handler = h
}

// Close tears down the client. It is safe to call multiple times.
func (c *Client) Close() error {
	if c.closed.Swap(true) {
		return nil
	}
	return c.conn.Close()
}

// DisconnectNotify is closed after the JSON-RPC reader reaches EOF or a
// protocol/transport error. Waiting for it before snapshotting captured stdout
// guarantees the reader has consumed every available inbound byte.
func (c *Client) DisconnectNotify() <-chan struct{} {
	return c.conn.DisconnectNotify()
}

// ---------------------------------------------------------------------------
// Typed method wrappers
// ---------------------------------------------------------------------------

// Initialize performs the "initialize" handshake.
func (c *Client) Initialize(ctx context.Context, params InitializeParams) (*InitializeResponse, error) {
	var resp InitializeResponse
	if err := c.conn.Call(ctx, MethodInitialize, params, &resp); err != nil {
		return nil, wrapRPCErr(MethodInitialize, err)
	}
	return &resp, nil
}

// NotifyInitialized sends the "initialized" notification that completes
// the handshake.
func (c *Client) NotifyInitialized(ctx context.Context) error {
	return c.conn.Notify(ctx, MethodInitialized, map[string]any{})
}

// ThreadStart creates a new thread.
func (c *Client) ThreadStart(ctx context.Context, params ThreadStartParams) (*ThreadStartResponse, error) {
	var resp ThreadStartResponse
	if err := c.conn.Call(ctx, MethodThreadStart, params, &resp); err != nil {
		return nil, wrapRPCErr(MethodThreadStart, err)
	}
	if resp.Thread.ID == "" {
		return nil, fmt.Errorf("appserver: thread/start returned empty thread id")
	}
	return &resp, nil
}

// ThreadResume resumes an existing thread by id.
func (c *Client) ThreadResume(ctx context.Context, params ThreadResumeParams) (*ThreadResumeResponse, error) {
	var resp ThreadResumeResponse
	if err := c.conn.Call(ctx, MethodThreadResume, params, &resp); err != nil {
		return nil, wrapRPCErr(MethodThreadResume, err)
	}
	if resp.Thread.ID == "" {
		return nil, fmt.Errorf("appserver: thread/resume returned empty thread id")
	}
	return &resp, nil
}

// TurnStart kicks off a new turn on the given thread and returns once
// the server has acknowledged the request (not once the turn completes).
func (c *Client) TurnStart(ctx context.Context, params TurnStartParams) (*TurnStartResponse, error) {
	var resp TurnStartResponse
	if err := c.conn.Call(ctx, MethodTurnStart, params, &resp); err != nil {
		return nil, wrapRPCErr(MethodTurnStart, err)
	}
	return &resp, nil
}

// TurnInterrupt cancels an in-flight turn.
func (c *Client) TurnInterrupt(ctx context.Context, params TurnInterruptParams) error {
	var resp TurnInterruptResponse
	if err := c.conn.Call(ctx, MethodTurnInterrupt, params, &resp); err != nil {
		return wrapRPCErr(MethodTurnInterrupt, err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Internals
// ---------------------------------------------------------------------------

// connHandler implements jsonrpc2.Handler. It is the single place where
// inbound frames are observed: notifications are forwarded to the
// registered NotificationHandler, unsolicited server requests (e.g.
// approval prompts — unused in v1) are rejected to unblock the peer.
type connHandler struct {
	client *Client
}

func (h *connHandler) Handle(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) {
	if !req.Notif {
		// codex app-server may emit ServerRequest frames for approval
		// prompts. v1 never enables approval policies that require
		// human-in-the-loop, but we must reply to avoid leaking a
		// pending request on the peer. Respond with "method not found"
		// so the server fails fast instead of blocking.
		_ = conn.ReplyWithError(ctx, req.ID, &jsonrpc2.Error{
			Code:    jsonrpc2.CodeMethodNotFound,
			Message: fmt.Sprintf("agent-adaptor does not accept server-initiated request %q in v1", req.Method),
		})
		return
	}
	h.client.handlerMu.RLock()
	fn := h.client.handler
	h.client.handlerMu.RUnlock()
	if fn == nil {
		return
	}
	var raw json.RawMessage
	if req.Params != nil {
		raw = *req.Params
	}
	fn(req.Method, raw)
}

// wrapRPCErr produces a uniform "appserver: <method>: <cause>" error
// for Call failures. It preserves the original error chain so callers
// can use errors.Is / errors.As if they need to.
func wrapRPCErr(method string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("appserver: %s: %w", method, err)
}

// IsDisconnected reports whether err indicates the underlying
// connection has been closed. Exposed for callers that want to
// distinguish a clean shutdown from an RPC-level failure.
func IsDisconnected(err error) bool {
	return errors.Is(err, jsonrpc2.ErrClosed)
}
