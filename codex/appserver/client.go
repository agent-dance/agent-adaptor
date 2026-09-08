package appserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
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
	// MethodThreadFork creates a child thread without mutating its parent.
	MethodThreadFork = "thread/fork"
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
	// NotifyCommandExecOutputDelta carries command output deltas emitted under
	// the alternate app-server notification name.
	NotifyCommandExecOutputDelta = "command/exec/outputDelta"
	// NotifyItemFileChangeOutputDelta carries file-change output deltas.
	NotifyItemFileChangeOutputDelta = "item/fileChange/outputDelta"
	// NotifyItemPlanDelta carries plan text deltas.
	NotifyItemPlanDelta = "item/plan/delta"
	// NotifyTurnPlanUpdated carries an official complete plan snapshot.
	NotifyTurnPlanUpdated = "turn/plan/updated"
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
	conn   *jsonrpc2.Conn
	stream *ownedObjectStream

	callsMu  sync.Mutex
	calls    map[*clientCall]struct{}
	sendGate chan struct{}

	handlerMu sync.RWMutex
	handler   NotificationHandler
	closed    atomic.Bool
}

// NewClient takes ownership of the ObjectStream and spins up the
// JSON-RPC dispatcher goroutine. The caller is responsible for
// producing the stream (usually by spawning
// `codex app-server --listen stdio://` and handing over its stdio).
func NewClient(ctx context.Context, stream jsonrpc2.ObjectStream) *Client {
	c := &Client{stream: &ownedObjectStream{ObjectStream: stream}, calls: make(map[*clientCall]struct{}), sendGate: make(chan struct{}, 1)}
	h := &connHandler{client: c}
	c.conn = jsonrpc2.NewConn(ctx, c.stream, h)
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

// Close rejects new calls, releases active RPC waiters, and closes the owned
// transport. It is safe to call multiple times, including from a notification
// handler. It does not wait for the reader or the subprocess to exit.
func (c *Client) Close() error {
	if c.closed.Swap(true) {
		return nil
	}
	// The pinned Conn.Close closes pending channels without removing them;
	// an already decoded response could still send to one. Only the original
	// synchronous reader may settle that state, when it reaches EOF/read error.
	c.callsMu.Lock()
	for call := range c.calls {
		call.cancel(errClientShutdown)
	}
	c.callsMu.Unlock()
	return c.stream.Close()
}

// DisconnectNotify is closed after the JSON-RPC reader reaches EOF or a
// protocol/transport error, rather than when Close is requested. It does not
// prove that process stdout/stderr tails were drained or OS Wait completed;
// the process owner retains those separate joins before its final snapshot.
func (c *Client) DisconnectNotify() <-chan struct{} {
	return c.conn.DisconnectNotify()
}

// ---------------------------------------------------------------------------
// Typed method wrappers
// ---------------------------------------------------------------------------

// Initialize performs the "initialize" handshake.
func (c *Client) Initialize(ctx context.Context, params InitializeParams) (*InitializeResponse, error) {
	var resp InitializeResponse
	if err := c.call(ctx, MethodInitialize, params, &resp); err != nil {
		return nil, wrapRPCErr(MethodInitialize, err)
	}
	return &resp, nil
}

// NotifyInitialized sends the "initialized" notification that completes
// the handshake.
func (c *Client) NotifyInitialized(ctx context.Context) error {
	return c.notify(ctx, MethodInitialized, map[string]any{})
}

// ThreadStart creates a new thread.
func (c *Client) ThreadStart(ctx context.Context, params ThreadStartParams) (*ThreadStartResponse, error) {
	var resp ThreadStartResponse
	if err := c.call(ctx, MethodThreadStart, params, &resp); err != nil {
		return nil, wrapRPCErr(MethodThreadStart, err)
	}
	if strings.TrimSpace(resp.Thread.ID) == "" {
		return nil, fmt.Errorf("appserver: thread/start returned empty thread id")
	}
	return &resp, nil
}

// ThreadResume resumes an existing thread by id.
func (c *Client) ThreadResume(ctx context.Context, params ThreadResumeParams) (*ThreadResumeResponse, error) {
	var resp ThreadResumeResponse
	if err := c.call(ctx, MethodThreadResume, params, &resp); err != nil {
		return nil, wrapRPCErr(MethodThreadResume, err)
	}
	if strings.TrimSpace(resp.Thread.ID) == "" {
		return nil, fmt.Errorf("appserver: thread/resume returned empty thread id")
	}
	if resp.Thread.ID != params.ThreadID {
		return nil, fmt.Errorf("appserver: thread/resume returned thread id %q for requested thread %q", resp.Thread.ID, params.ThreadID)
	}
	return &resp, nil
}

// ThreadFork creates a new child thread from an existing parent thread id.
func (c *Client) ThreadFork(ctx context.Context, params ThreadForkParams) (*ThreadForkResponse, error) {
	var resp ThreadForkResponse
	if err := c.call(ctx, MethodThreadFork, params, &resp); err != nil {
		return nil, wrapRPCErr(MethodThreadFork, err)
	}
	if strings.TrimSpace(resp.Thread.ID) == "" {
		return nil, fmt.Errorf("appserver: thread/fork returned empty thread id")
	}
	if resp.Thread.ID == params.ThreadID {
		return nil, fmt.Errorf("appserver: thread/fork returned parent thread id %q", params.ThreadID)
	}
	return &resp, nil
}

// TurnStart kicks off a new turn on the given thread and returns once
// the server has acknowledged the request (not once the turn completes).
func (c *Client) TurnStart(ctx context.Context, params TurnStartParams) (*TurnStartResponse, error) {
	var resp TurnStartResponse
	if err := c.call(ctx, MethodTurnStart, params, &resp); err != nil {
		return nil, wrapRPCErr(MethodTurnStart, err)
	}
	if strings.TrimSpace(resp.Turn.ID) == "" {
		return nil, fmt.Errorf("appserver: turn/start returned empty turn id")
	}
	return &resp, nil
}

// TurnInterrupt cancels an in-flight turn.
func (c *Client) TurnInterrupt(ctx context.Context, params TurnInterruptParams) error {
	var resp TurnInterruptResponse
	if err := c.call(ctx, MethodTurnInterrupt, params, &resp); err != nil {
		return wrapRPCErr(MethodTurnInterrupt, err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Internals
// ---------------------------------------------------------------------------

// ownedObjectStream preserves the original stream and serial reader while
// making explicit transport shutdown and the later reader shutdown share one
// resource release. Close never takes a write lock: it must unblock a writer.
type ownedObjectStream struct {
	jsonrpc2.ObjectStream
	closing  atomic.Bool
	once     sync.Once
	closeErr error
	attempt  *writeAttempt
}

func (s *ownedObjectStream) Close() error {
	s.closing.Store(true)
	s.once.Do(func() { s.closeErr = s.ObjectStream.Close() })
	return s.closeErr
}
func (s *ownedObjectStream) WriteObject(v interface{}) error {
	if s.closing.Load() {
		return jsonrpc2.ErrClosed
	}
	err := s.ObjectStream.WriteObject(v)
	if s.attempt != nil {
		s.attempt.err = err
	}
	if err != nil && s.closing.Load() {
		return errors.Join(err, jsonrpc2.ErrClosed)
	}
	return err
}

// All outbound operations hold sendGate only through the synchronous send.
// Its attempt is private to that operation, including a server-request reply.
// The pinned sender may replace the actual write error with ErrClosed on EOF;
// the attempt preserves that evidence without attaching it to another call.
type writeAttempt struct {
	err error
}

type clientCall struct {
	cancel context.CancelCauseFunc
}

var errClientShutdown = errors.New("appserver: client shutdown")

func callerContextError(ctx context.Context) error {
	err := ctx.Err()
	if err != nil && context.Cause(ctx) != err {
		return errors.Join(err, context.Cause(ctx))
	}
	return err
}

// A Client close cancels only our wait, never the Conn's pending channels. The
// original reader can still finish an in-flight response and drain in FIFO order.
func (c *Client) beginCall(ctx context.Context) (context.Context, func(), error) {
	if err := callerContextError(ctx); err != nil {
		return nil, nil, err
	}
	callCtx, cancel := context.WithCancelCause(ctx)
	call := &clientCall{cancel: cancel}
	c.callsMu.Lock()
	if c.closed.Load() {
		c.callsMu.Unlock()
		cancel(nil)
		return nil, nil, jsonrpc2.ErrClosed
	}
	c.calls[call] = struct{}{}
	c.callsMu.Unlock()
	return callCtx, func() {
		c.callsMu.Lock()
		delete(c.calls, call)
		c.callsMu.Unlock()
		cancel(nil)
	}, nil
}

func rpcContextError(ctx context.Context) error {
	if ctx.Err() != nil && context.Cause(ctx) == errClientShutdown {
		return jsonrpc2.ErrClosed
	}
	return callerContextError(ctx)
}

// send never holds the gate while waiting for a response or closing transport.
// Every Conn write entry uses this gate, including replies from its sole reader.
func (c *Client) send(ctx context.Context, send func() error) error {
	select {
	case c.sendGate <- struct{}{}:
	case <-ctx.Done():
		return rpcContextError(ctx)
	}
	defer func() { <-c.sendGate }()
	if err := rpcContextError(ctx); err != nil {
		return err
	}
	if c.closed.Load() {
		return jsonrpc2.ErrClosed
	}
	attempt := &writeAttempt{}
	c.stream.attempt = attempt
	defer func() { c.stream.attempt = nil }()
	err := send()
	if err != nil {
		return errors.Join(rpcContextError(ctx), err, attempt.err)
	}
	return nil
}

func (c *Client) call(ctx context.Context, method string, params, result any) error {
	callCtx, finish, err := c.beginCall(ctx)
	if err != nil {
		return err
	}
	defer finish()
	var waiter jsonrpc2.Waiter
	err = c.send(callCtx, func() error {
		var err error
		waiter, err = c.conn.DispatchCall(callCtx, method, params)
		return err
	})
	if err != nil {
		return err
	}
	err = waiter.Wait(callCtx, result)
	if callCtx.Err() != nil && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, jsonrpc2.ErrClosed)) {
		// The private cause distinguishes our shutdown from a caller that
		// deliberately cancels with ErrClosed. A formal RPC error stays intact.
		if context.Cause(callCtx) == errClientShutdown {
			return jsonrpc2.ErrClosed
		}
		return errors.Join(callerContextError(callCtx), err)
	}
	return err
}
func (c *Client) notify(ctx context.Context, method string, params any) error {
	callCtx, finish, err := c.beginCall(ctx)
	if err != nil {
		return err
	}
	defer finish()
	return c.send(callCtx, func() error { return c.conn.Notify(callCtx, method, params) })
}
func (c *Client) rejectRequest(ctx context.Context, id jsonrpc2.ID, rpcErr *jsonrpc2.Error) error {
	callCtx, finish, err := c.beginCall(ctx)
	if err != nil {
		return err
	}
	defer finish()
	return c.send(callCtx, func() error { return c.conn.ReplyWithError(callCtx, id, rpcErr) })
}

// connHandler implements jsonrpc2.Handler. It is the single place where
// inbound frames are observed: notifications are forwarded to the
// registered NotificationHandler, unsolicited server requests (for example,
// approval prompts) are rejected to unblock the peer.
type connHandler struct {
	client *Client
}

func (h *connHandler) Handle(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) {
	if !req.Notif {
		// codex app-server may emit ServerRequest frames for approval
		// prompts. This client does not negotiate server-initiated approval
		// requests, but it must reply to avoid leaking a pending request on
		// the peer. Respond with "method not found" so the server fails fast
		// instead of blocking.
		_ = h.client.rejectRequest(ctx, req.ID, &jsonrpc2.Error{
			Code:    jsonrpc2.CodeMethodNotFound,
			Message: fmt.Sprintf("agent-adaptor does not accept server-initiated request %q", req.Method),
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
