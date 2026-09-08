package appserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sourcegraph/jsonrpc2"
)

type alignmentRPCFrame struct {
	raw     string
	err     error
	decoded chan struct{}
	release <-chan struct{}
}

// This is a half-close stream, like the real stdio codec: Close releases writes
// but cannot retract a response already decoded from the independent reader.
// The pinned jsonrpc2.Conn still decodes/distributes every request and response.
type alignmentRPCStream struct {
	ctx          context.Context
	frames       chan alignmentRPCFrame
	writes       chan json.RawMessage
	writeEntered chan struct{}
	blockWrite   bool
	writeFailure error
	writeRelease <-chan struct{}
	closeFailure error
	closed       chan struct{}
	closeOnce    sync.Once
	closeCount   atomic.Int64
	readDone     chan struct{}
	readOnce     sync.Once
	readError    error
}

var _ jsonrpc2.ObjectStream = (*alignmentRPCStream)(nil)

func (s *alignmentRPCStream) ReadObject(v interface{}) error {
	var frame alignmentRPCFrame
	select {
	case frame = <-s.frames:
	case <-s.ctx.Done():
		frame.err = io.EOF
	}
	err := frame.err
	if err == nil {
		err = json.Unmarshal([]byte(frame.raw), v)
	}
	if frame.decoded != nil {
		close(frame.decoded)
	}
	if frame.release != nil {
		select {
		case <-frame.release:
		case <-s.ctx.Done():
			err = io.EOF
		}
	}
	if err != nil {
		s.readOnce.Do(func() { s.readError = err; close(s.readDone) })
	}
	return err
}
func (s *alignmentRPCStream) WriteObject(v interface{}) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	select {
	case s.writes <- raw:
	case <-s.ctx.Done():
		return s.ctx.Err()
	}
	if s.blockWrite {
		select {
		case s.writeEntered <- struct{}{}:
		default:
		}
		select {
		case <-s.closed:
			if s.writeRelease != nil {
				select {
				case <-s.writeRelease:
				case <-s.ctx.Done():
					return s.ctx.Err()
				}
			}
			return s.writeFailure
		case <-s.ctx.Done():
			return s.ctx.Err()
		}
	}
	select {
	case <-s.closed:
		return io.ErrClosedPipe
	default:
		return nil
	}
}
func (s *alignmentRPCStream) Close() error {
	s.closeCount.Add(1)
	s.closeOnce.Do(func() { close(s.closed) })
	return s.closeFailure
}

func alignmentRPCWait[T any](t *testing.T, ctx context.Context, ch <-chan T) T {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-ctx.Done():
		t.Fatalf("bounded phase did not finish: %v", ctx.Err())
		var zero T
		return zero
	}
}
func alignmentRPCSetup(t *testing.T, configure func(*alignmentRPCStream)) (context.Context, *Client, *alignmentRPCStream) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	s := &alignmentRPCStream{ctx: ctx, frames: make(chan alignmentRPCFrame, 16), writes: make(chan json.RawMessage, 16), writeEntered: make(chan struct{}, 1), closed: make(chan struct{}), readDone: make(chan struct{})}
	if configure != nil {
		configure(s)
	}
	client := NewClient(context.Background(), s)
	t.Cleanup(func() {
		_ = client.Close()
		cancel() // Every test barrier/ReadObject releases even after a fatal assertion.
		cleanup, stop := context.WithTimeout(context.Background(), time.Second)
		defer stop()
		alignmentRPCWait(t, cleanup, s.readDone)
		alignmentRPCWait(t, cleanup, client.DisconnectNotify())
	})
	return ctx, client, s
}
func alignmentRPCCall(ctx context.Context, client *Client) <-chan error {
	done := make(chan error, 1)
	go func() { _, err := client.ThreadStart(ctx, ThreadStartParams{}); done <- err }()
	return done
}

func alignmentRPCWaitCalls(t *testing.T, ctx context.Context, client *Client, want int) {
	t.Helper()
	for {
		client.callsMu.Lock()
		active := len(client.calls)
		client.callsMu.Unlock()
		if active == want {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("active calls=%d, want %d: %v", active, want, ctx.Err())
		default:
			runtime.Gosched() // Observe registration, never infer readiness from elapsed time.
		}
	}
}
func alignmentRPCResponse(t *testing.T, ctx context.Context, s *alignmentRPCStream, result string) string {
	t.Helper()
	var req struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
	}
	if err := json.Unmarshal(alignmentRPCWait(t, ctx, s.writes), &req); err != nil || req.Method != "thread/start" {
		t.Fatalf("real RPC request: %+v err=%v", req, err)
	}
	return fmt.Sprintf(`{"id":%s,%s}`, req.ID, result)
}
func alignmentRPCFinish(t *testing.T, ctx context.Context, client *Client, s *alignmentRPCStream) {
	t.Helper()
	s.frames <- alignmentRPCFrame{err: io.EOF}
	alignmentRPCWait(t, ctx, s.readDone)
	alignmentRPCWait(t, ctx, client.DisconnectNotify())
	if err := client.Close(); err != nil && !IsDisconnected(err) {
		t.Fatalf("post-EOF Close: %v", err)
	}
	if s.closeCount.Load() != 1 {
		t.Fatalf("resource closed %d times", s.closeCount.Load())
	}
	client.callsMu.Lock()
	active := len(client.calls)
	client.callsMu.Unlock()
	if active != 0 {
		t.Fatalf("completed RPCs retained %d active cancellation registrations", active)
	}
}

func TestAlignmentClientClose(t *testing.T) {
	t.Run("response-pending", func(t *testing.T) {
		ctx, client, s := alignmentRPCSetup(t, nil)
		call := alignmentRPCCall(ctx, client)
		response := alignmentRPCResponse(t, ctx, s, `"result":{"thread":{"id":"thread-1"}}`)
		decoded, release := make(chan struct{}), make(chan struct{})
		s.frames <- alignmentRPCFrame{raw: response, decoded: decoded, release: release}
		alignmentRPCWait(t, ctx, decoded)
		t.Log("real pinned reader decoded response; ReadObject return held before dispatch")
		if err := client.Close(); err != nil {
			t.Fatal(err)
		}
		if err := alignmentRPCWait(t, ctx, call); !IsDisconnected(err) {
			t.Fatalf("pending call: %v", err)
		}
		t.Log("Client.Close completed and pending waiter released before response dispatch")
		afterResponse := make(chan struct{})
		client.SetNotificationHandler(func(string, json.RawMessage) { close(afterResponse) })
		s.frames <- alignmentRPCFrame{raw: `{"method":"after-response","params":{}}`}
		close(release)
		// Reaching the next synchronous notification proves the real response
		// dispatch finished. The old Conn.Close path panics before this point.
		alignmentRPCWait(t, ctx, afterResponse)
		select {
		case <-client.DisconnectNotify():
			t.Fatal("logical close falsely reported reader EOF")
		default:
		}
		alignmentRPCFinish(t, ctx, client, s)
	})

	for _, rpcError := range []bool{false, true} {
		t.Run(fmt.Sprintf("response-first/error-%v", rpcError), func(t *testing.T) {
			ctx, client, s := alignmentRPCSetup(t, nil)
			call := alignmentRPCCall(ctx, client)
			result := `"result":{"thread":{"id":"thread-1"}}`
			if rpcError {
				result = `"error":{"code":-32001,"message":"original rpc failure"}`
			}
			s.frames <- alignmentRPCFrame{raw: alignmentRPCResponse(t, ctx, s, result)}
			err := alignmentRPCWait(t, ctx, call)
			if rpcError {
				var rpc *jsonrpc2.Error
				if !errors.As(err, &rpc) || rpc.Code != -32001 || rpc.Message != "original rpc failure" {
					t.Fatalf("RPC cause: %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if closeErr := client.Close(); closeErr != nil {
				t.Fatal(closeErr)
			}
			if rpcError {
				var rpc *jsonrpc2.Error
				if !errors.As(err, &rpc) || rpc.Code != -32001 {
					t.Fatalf("Close changed settled RPC error: %v", err)
				}
			}
			alignmentRPCFinish(t, ctx, client, s)
		})
	}
	t.Run("active-background-and-new-calls", func(t *testing.T) {
		ctx, client, s := alignmentRPCSetup(t, nil)
		call := alignmentRPCCall(context.Background(), client)
		_ = alignmentRPCResponse(t, ctx, s, `"result":{}`)
		if err := client.Close(); err != nil {
			t.Fatal(err)
		}
		if err := alignmentRPCWait(t, ctx, call); !IsDisconnected(err) || errors.Is(err, context.Canceled) {
			t.Fatalf("client-only close: %v", err)
		}
		calls := []func() error{
			func() error { _, e := client.Initialize(context.Background(), InitializeParams{}); return e },
			func() error { _, e := client.ThreadStart(context.Background(), ThreadStartParams{}); return e },
			func() error { _, e := client.ThreadResume(context.Background(), ThreadResumeParams{}); return e },
			func() error { _, e := client.ThreadFork(context.Background(), ThreadForkParams{}); return e },
			func() error { _, e := client.TurnStart(context.Background(), TurnStartParams{}); return e },
			func() error { return client.TurnInterrupt(context.Background(), TurnInterruptParams{}) },
			func() error { return client.NotifyInitialized(context.Background()) },
		}
		for i, call := range calls {
			if err := call(); !IsDisconnected(err) {
				t.Fatalf("closed typed method %d: %v", i, err)
			}
		}
		select {
		case raw := <-s.writes:
			t.Fatalf("new call wrote after Close: %s", raw)
		default:
		}
		alignmentRPCFinish(t, ctx, client, s)
	})
	t.Run("caller-cause-before-close", func(t *testing.T) {
		ctx, client, s := alignmentRPCSetup(t, nil)
		callCtx, cancel := context.WithCancelCause(ctx)
		defer cancel(nil)
		cause := errors.New("original caller cause")
		call := alignmentRPCCall(callCtx, client)
		response := alignmentRPCResponse(t, ctx, s, `"result":{"thread":{"id":"late"}}`)
		cancel(cause)
		err := alignmentRPCWait(t, ctx, call)
		if !errors.Is(err, context.Canceled) || !errors.Is(err, cause) || IsDisconnected(err) {
			t.Fatalf("caller cause: %v", err)
		}
		if closeErr := client.Close(); closeErr != nil {
			t.Fatal(closeErr)
		}
		s.frames <- alignmentRPCFrame{raw: response}
		alignmentRPCFinish(t, ctx, client, s)
		if !errors.Is(err, cause) {
			t.Fatalf("Close changed caller cause: %v", err)
		}
	})
	t.Run("caller-expired-deadline", func(t *testing.T) {
		ctx, client, s := alignmentRPCSetup(t, nil)
		deadlineCtx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
		defer cancel()
		if _, err := client.ThreadStart(deadlineCtx, ThreadStartParams{}); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("deadline: %v", err)
		}
		if err := client.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := client.ThreadStart(deadlineCtx, ThreadStartParams{}); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("already established deadline overridden: %v", err)
		}
		select {
		case raw := <-s.writes:
			t.Fatalf("expired call wrote: %s", raw)
		default:
		}
		alignmentRPCFinish(t, ctx, client, s)
	})
	t.Run("close-before-caller-cancel", func(t *testing.T) {
		ctx, client, s := alignmentRPCSetup(t, nil)
		callCtx, cancel := context.WithCancelCause(ctx)
		defer cancel(nil)
		cause := errors.New("later caller cancellation")
		call := alignmentRPCCall(callCtx, client)
		_ = alignmentRPCResponse(t, ctx, s, `"result":{}`)
		if err := client.Close(); err != nil {
			t.Fatal(err)
		}
		cancel(cause)
		if err := alignmentRPCWait(t, ctx, call); !IsDisconnected(err) || errors.Is(err, context.Canceled) || errors.Is(err, cause) {
			t.Fatalf("later cancellation relabelled completed close: %v", err)
		}
		alignmentRPCFinish(t, ctx, client, s)
	})
	t.Run("caller-cancel-before-eof", func(t *testing.T) {
		ctx, client, s := alignmentRPCSetup(t, nil)
		callCtx, cancel := context.WithCancelCause(ctx)
		defer cancel(nil)
		// Even a caller-provided ErrClosed cause remains caller cancellation.
		call := alignmentRPCCall(callCtx, client)
		_ = alignmentRPCResponse(t, ctx, s, `"result":{}`)
		cancel(jsonrpc2.ErrClosed)
		s.frames <- alignmentRPCFrame{err: io.EOF}
		alignmentRPCWait(t, ctx, client.DisconnectNotify())
		if err := alignmentRPCWait(t, ctx, call); !errors.Is(err, context.Canceled) || !IsDisconnected(err) {
			t.Fatalf("EOF changed established caller cancellation: %v", err)
		}
		alignmentRPCFinish(t, ctx, client, s)
	})
	for _, malformed := range []bool{false, true} {
		t.Run(fmt.Sprintf("reader-end/malformed-%v", malformed), func(t *testing.T) {
			ctx, client, s := alignmentRPCSetup(t, nil)
			call := alignmentRPCCall(context.Background(), client)
			_ = alignmentRPCResponse(t, ctx, s, `"result":{}`)
			frame := alignmentRPCFrame{err: io.EOF}
			if malformed {
				frame = alignmentRPCFrame{raw: `{broken`}
			}
			s.frames <- frame
			alignmentRPCWait(t, ctx, s.readDone)
			if malformed {
				var syntax *json.SyntaxError
				if !errors.As(s.readError, &syntax) {
					t.Fatalf("protocol cause: %v", s.readError)
				}
			} else if !errors.Is(s.readError, io.EOF) {
				t.Fatalf("EOF: %v", s.readError)
			}
			if err := alignmentRPCWait(t, ctx, call); !IsDisconnected(err) {
				t.Fatalf("reader did not release call: %v", err)
			}
			alignmentRPCWait(t, ctx, client.DisconnectNotify())
			if err := client.Close(); err != nil {
				t.Fatal(err)
			}
			if s.closeCount.Load() != 1 {
				t.Fatalf("resource closes: %d", s.closeCount.Load())
			}
		})
	}
	t.Run("handler-close-preserves-fifo", func(t *testing.T) {
		ctx, client, s := alignmentRPCSetup(t, nil)
		call := alignmentRPCCall(context.Background(), client)
		response := alignmentRPCResponse(t, ctx, s, `"result":{"thread":{"id":"late"}}`)
		first, release := make(chan error, 1), make(chan struct{})
		seen := make(chan string, 2)
		client.SetNotificationHandler(func(method string, _ json.RawMessage) {
			if method == "first" {
				first <- client.Close()
				select {
				case <-release:
				case <-ctx.Done():
					return
				}
			}
			seen <- method
		})
		s.frames <- alignmentRPCFrame{raw: `{"method":"first","params":{}}`}
		if err := alignmentRPCWait(t, ctx, first); err != nil {
			t.Fatal(err)
		}
		if err := alignmentRPCWait(t, ctx, call); !IsDisconnected(err) {
			t.Fatalf("callback Close pending wait: %v", err)
		}
		// The same synchronous handler remains blocked while later frames queue.
		s.frames <- alignmentRPCFrame{raw: response}
		s.frames <- alignmentRPCFrame{raw: `{"method":"second","params":{}}`}
		select {
		case value := <-seen:
			t.Fatalf("blocked first handler bypassed: %s", value)
		default:
		}
		close(release)
		for _, want := range []string{"first", "second"} {
			if got := alignmentRPCWait(t, ctx, seen); got != want {
				t.Fatalf("FIFO got %q want %q", got, want)
			}
		}
		alignmentRPCFinish(t, ctx, client, s)
	})
	t.Run("server-request-remains-rejected", func(t *testing.T) {
		ctx, client, s := alignmentRPCSetup(t, nil)
		s.frames <- alignmentRPCFrame{raw: `{"id":55,"method":"item/tool/requestApproval","params":{}}`}
		var response struct {
			ID     int             `json:"id"`
			Error  *jsonrpc2.Error `json:"error"`
			Result json.RawMessage `json:"result"`
		}
		raw := alignmentRPCWait(t, ctx, s.writes)
		if err := json.Unmarshal(raw, &response); err != nil || response.ID != 55 || response.Error == nil || response.Error.Code != jsonrpc2.CodeMethodNotFound || len(response.Result) != 0 {
			t.Fatalf("server request: %s err=%v", raw, err)
		}
		if err := client.Close(); err != nil {
			t.Fatal(err)
		}
		alignmentRPCFinish(t, ctx, client, s)
	})
	t.Run("concurrent-close-resource-once", func(t *testing.T) {
		closeFailure := errors.New("original close failure")
		ctx, client, s := alignmentRPCSetup(t, func(s *alignmentRPCStream) { s.closeFailure = closeFailure })
		call := alignmentRPCCall(context.Background(), client)
		_ = alignmentRPCResponse(t, ctx, s, `"result":{}`)
		start := make(chan struct{})
		done := make(chan error, 16)
		for i := 0; i < 16; i++ {
			go func() { <-start; done <- client.Close() }()
		}
		close(start)
		failureCount := 0
		for i := 0; i < 16; i++ {
			if err := alignmentRPCWait(t, ctx, done); err != nil {
				if !errors.Is(err, closeFailure) {
					t.Fatal(err)
				}
				failureCount++
			}
		}
		if failureCount != 1 || s.closeCount.Load() != 1 {
			t.Fatalf("close errors=%d resource closes=%d", failureCount, s.closeCount.Load())
		}
		if err := alignmentRPCWait(t, ctx, call); !IsDisconnected(err) {
			t.Fatal(err)
		}
		alignmentRPCFinish(t, ctx, client, s)
	})
	t.Run("close-unblocks-write-preserves-cause", func(t *testing.T) {
		writeFailure := errors.New("original blocked write failure")
		ctx, client, s := alignmentRPCSetup(t, func(s *alignmentRPCStream) { s.blockWrite = true; s.writeFailure = writeFailure })
		call := alignmentRPCCall(context.Background(), client)
		alignmentRPCWait(t, ctx, s.writeEntered)
		if err := client.Close(); err != nil {
			t.Fatal(err)
		}
		if err := alignmentRPCWait(t, ctx, call); !errors.Is(err, writeFailure) || !IsDisconnected(err) {
			t.Fatalf("write cause: %v", err)
		}
		alignmentRPCFinish(t, ctx, client, s)
	})
	t.Run("eof-before-write-error-preserves-cause", func(t *testing.T) {
		writeFailure := errors.New("actual write failure concurrent with EOF")
		release := make(chan struct{})
		ctx, client, s := alignmentRPCSetup(t, func(s *alignmentRPCStream) {
			s.blockWrite, s.writeFailure, s.writeRelease = true, writeFailure, release
		})
		call := alignmentRPCCall(context.Background(), client)
		alignmentRPCWait(t, ctx, s.writeEntered)
		if err := client.Close(); err != nil {
			t.Fatal(err)
		}
		s.frames <- alignmentRPCFrame{err: io.EOF}
		alignmentRPCWait(t, ctx, client.DisconnectNotify())
		// Force the pinned sender's ErrClosed rewrite after a real write error.
		close(release)
		if err := alignmentRPCWait(t, ctx, call); !errors.Is(err, writeFailure) || !IsDisconnected(err) {
			t.Fatalf("reader EOF erased actual write cause: %v", err)
		}
		alignmentRPCFinish(t, ctx, client, s)
	})
	for _, queued := range []string{"rpc-close", "rpc-caller-cancel", "notification", "server-reply"} {
		t.Run("queued-"+queued, func(t *testing.T) {
			writeFailure := errors.New("first operation write failure")
			release := make(chan struct{})
			ctx, client, s := alignmentRPCSetup(t, func(s *alignmentRPCStream) {
				s.blockWrite, s.writeFailure, s.writeRelease = true, writeFailure, release
			})
			first := alignmentRPCCall(context.Background(), client)
			alignmentRPCWait(t, ctx, s.writeEntered)
			_ = alignmentRPCResponse(t, ctx, s, `"result":{}`)
			queuedCtx, cancel := context.WithCancelCause(ctx)
			defer cancel(nil)
			var second <-chan error
			switch queued {
			case "notification":
				done := make(chan error, 1)
				go func() { done <- client.NotifyInitialized(queuedCtx) }()
				second = done
			case "server-reply":
				s.frames <- alignmentRPCFrame{raw: `{"id":55,"method":"item/tool/requestApproval","params":{}}`}
			default:
				second = alignmentRPCCall(queuedCtx, client)
			}
			alignmentRPCWaitCalls(t, ctx, client, 2)
			cause := errors.New("queued caller cancellation")
			if queued == "rpc-caller-cancel" {
				cancel(cause)
			} else if err := client.Close(); err != nil {
				t.Fatal(err)
			}
			var secondErr error
			if second != nil {
				secondErr = alignmentRPCWait(t, ctx, second)
				if queued == "rpc-caller-cancel" {
					if !errors.Is(secondErr, context.Canceled) || !errors.Is(secondErr, cause) || IsDisconnected(secondErr) {
						t.Fatalf("queued caller cause: %v", secondErr)
					}
				} else if !IsDisconnected(secondErr) || errors.Is(secondErr, context.Canceled) {
					t.Fatalf("queued client close: %v", secondErr)
				}
			} else {
				// The synchronous request handler must leave the cancelled gate so
				// its reader can reach EOF while the first write is still held.
				alignmentRPCWaitCalls(t, ctx, client, 1)
			}
			if err := client.Close(); err != nil {
				t.Fatal(err)
			}
			s.frames <- alignmentRPCFrame{err: io.EOF}
			alignmentRPCWait(t, ctx, client.DisconnectNotify())
			close(release)
			if err := alignmentRPCWait(t, ctx, first); !errors.Is(err, writeFailure) || !IsDisconnected(err) {
				t.Fatalf("first write's actual cause: %v", err)
			}
			if errors.Is(secondErr, writeFailure) {
				t.Fatalf("unwritten operation inherited another write's cause: %v", secondErr)
			}
			select {
			case raw := <-s.writes:
				t.Fatalf("queued operation unexpectedly wrote: %s", raw)
			default:
			}
			alignmentRPCFinish(t, ctx, client, s)
		})
	}
}
