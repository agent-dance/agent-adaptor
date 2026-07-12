package a2adelegation

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type flushingResponseRecorder struct {
	*httptest.ResponseRecorder
	flushCount int
}

// Flush 记录 handler 是否在最终 JSON-RPC 响应前主动刷新了响应。
func (r *flushingResponseRecorder) Flush() {
	r.flushCount++
}

type observingResponseWriter struct {
	header http.Header
	mu     sync.Mutex
	body   bytes.Buffer
	flush  chan struct{}
}

func newObservingResponseWriter() *observingResponseWriter {
	return &observingResponseWriter{
		header: make(http.Header),
		flush:  make(chan struct{}, 16),
	}
}

func (w *observingResponseWriter) Header() http.Header {
	return w.header
}

func (w *observingResponseWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.body.Write(data)
}

func (w *observingResponseWriter) WriteHeader(int) {}

func (w *observingResponseWriter) Flush() {
	select {
	case w.flush <- struct{}{}:
	default:
	}
}

func (w *observingResponseWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.body.String()
}

// TestWriteMCPRPCResponseWritesPlainJSON 验证未协商 SSE 时保持一次性 JSON-RPC 响应。
func TestWriteMCPRPCResponseWritesPlainJSON(t *testing.T) {
	t.Parallel()

	recorder := &flushingResponseRecorder{ResponseRecorder: httptest.NewRecorder()}
	writeMCPRPCResponse(recorder, func() map[string]any {
		return result(json.RawMessage(`1`), map[string]any{"ok": true})
	})

	if recorder.flushCount != 0 {
		t.Fatalf("plain JSON response must not flush before building the payload, got %d flushes", recorder.flushCount)
	}
	if got := recorder.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("unexpected content type: %q", got)
	}
	body := recorder.Body.String()
	if strings.HasPrefix(body, "\n") {
		t.Fatalf("plain JSON response must not contain a preamble: %q", body)
	}
	var response map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("response must remain valid JSON: %v", err)
	}
	if response["jsonrpc"] != "2.0" {
		t.Fatalf("unexpected JSON-RPC response: %#v", response)
	}
}

// TestWriteMCPToolCallEventStreamWritesProgressBeforeResult 验证 SSE 在 tool
// handler 完成前先发送接收状态，并持续发送运行中状态。
func TestWriteMCPToolCallEventStreamWritesProgressBeforeResult(t *testing.T) {
	recorder := newObservingResponseWriter()
	release := make(chan struct{})
	done := make(chan struct{})
	req := rpcRequest{Params: json.RawMessage(`{"_meta":{"progressToken":"progress-1"}}`)}

	go func() {
		writeMCPToolCallEventStream(recorder, req, time.Millisecond, func() map[string]any {
			<-release
			return result(json.RawMessage(`1`), map[string]any{"ok": true})
		})
		close(done)
	}()

	awaitFlush(t, recorder, "initial progress")
	if body := recorder.String(); !strings.Contains(body, `"message":"delegation accepted"`) {
		t.Fatalf("expected accepted progress before handler result, got %q", body)
	}
	awaitFlush(t, recorder, "running progress")
	if body := recorder.String(); !strings.Contains(body, `"message":"delegation running"`) {
		t.Fatalf("expected running progress before handler result, got %q", body)
	}

	close(release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for final MCP response")
	}
	body := recorder.String()
	if got := recorder.Header().Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("unexpected content type: %q", got)
	}
	if !strings.Contains(body, `"result":{"ok":true}`) {
		t.Fatalf("expected final JSON-RPC response in SSE stream, got %q", body)
	}
}

// TestWriteMCPToolCallEventStreamUsesCommentsWithoutProgressToken 验证未提供
// progressToken 时仍通过标准 SSE 注释维持连接。
func TestWriteMCPToolCallEventStreamUsesCommentsWithoutProgressToken(t *testing.T) {
	recorder := newObservingResponseWriter()
	release := make(chan struct{})
	done := make(chan struct{})

	go func() {
		writeMCPToolCallEventStream(recorder, rpcRequest{}, time.Millisecond, func() map[string]any {
			<-release
			return result(json.RawMessage(`1`), map[string]any{"ok": true})
		})
		close(done)
	}()

	awaitFlush(t, recorder, "initial keepalive")
	awaitFlush(t, recorder, "periodic keepalive")
	if body := recorder.String(); !strings.Contains(body, ": delegation accepted\n\n") || !strings.Contains(body, ": delegation running\n\n") {
		t.Fatalf("expected accepted and running SSE comments, got %q", body)
	}
	close(release)
	<-done
}

// TestMCPServerUsesEventStreamOnlyWhenRequested 验证 HTTP 客户端只有明确协商 SSE
// 时才切换传输，普通客户端继续接收 JSON。
func TestMCPServerUsesEventStreamOnlyWhenRequested(t *testing.T) {
	t.Parallel()

	server := NewMCPServer(NewDelegator(nil, nil), MCPServerOptions{RunID: "run-1", BearerToken: "token", SSEKeepAliveInterval: time.Millisecond})
	requestBody := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"delegate_to_agent","arguments":{"agent":"research","objective":"research this"},"_meta":{"progressToken":1}}}`

	request := httptest.NewRequest(http.MethodPost, "http://localhost", strings.NewReader(requestBody))
	request.Header.Set("Authorization", "Bearer token")
	request.Header.Set("Accept", "application/json, text/event-stream")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)

	if got := response.Header().Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("expected SSE response, got %q", got)
	}
	if body := response.Body.String(); !strings.Contains(body, "notifications/progress") || !strings.Contains(body, "isError") {
		t.Fatalf("expected progress and final tool response, got %q", body)
	}
}

func awaitFlush(t *testing.T, recorder *observingResponseWriter, want string) {
	t.Helper()
	select {
	case <-recorder.flush:
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for %s", want)
	}
}
