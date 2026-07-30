package toolruntime

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agent-dance/agent-adaptor/tool"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type testInput struct {
	Value string `json:"value" jsonschema:"required"`
}

type testOutput struct {
	Value string `json:"value"`
}

func TestGatewaySharesEndpointAndRoutesBearerToImmutableCatalog(t *testing.T) {
	manager := newGatewayManager(testGatewayConfig())
	first := newTestRuntime(t, manager, "first", func(_ context.Context, input testInput) (testOutput, error) {
		return testOutput{Value: "first:" + input.Value}, nil
	})
	second := newTestRuntime(t, manager, "second", func(_ context.Context, input testInput) (testOutput, error) {
		return testOutput{Value: "second:" + input.Value}, nil
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	firstEndpoint, err := first.Start(ctx)
	if err != nil {
		t.Fatalf("first.Start: %v", err)
	}
	secondEndpoint, err := second.Start(ctx)
	if err != nil {
		t.Fatalf("second.Start: %v", err)
	}
	if firstEndpoint != secondEndpoint {
		t.Fatalf("process gateway endpoints differ: first=%+v second=%+v", firstEndpoint, secondEndpoint)
	}
	parsed, err := url.Parse(firstEndpoint.URL)
	if err != nil {
		t.Fatalf("parse endpoint: %v", err)
	}
	host, _, err := net.SplitHostPort(parsed.Host)
	if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
		t.Fatalf("endpoint is not numeric loopback: %q (%v)", firstEndpoint.URL, err)
	}
	firstToken := runtimeToken(t, first)
	secondToken := runtimeToken(t, second)
	if firstToken == secondToken {
		t.Fatal("Agent registrations received the same bearer token")
	}
	if strings.Contains(first.Fingerprint(), firstToken) || strings.Contains(second.Fingerprint(), secondToken) {
		t.Fatal("catalog fingerprint contains bearer secret")
	}
	if formatted := fmt.Sprintf("%+v %#v", first, first); strings.Contains(formatted, firstToken) {
		t.Fatal("formatted runtime contains bearer secret")
	}

	firstSession := connectClient(t, ctx, firstEndpoint.URL, firstToken)
	assertOnlyTool(t, ctx, firstSession, "first")
	assertToolValue(t, ctx, firstSession, "first", "x", "first:x")
	_ = firstSession.Close()
	secondSession := connectClient(t, ctx, secondEndpoint.URL, secondToken)
	assertOnlyTool(t, ctx, secondSession, "second")
	assertToolValue(t, ctx, secondSession, "second", "y", "second:y")
	_ = secondSession.Close()

	assertHTTPStatus(t, firstEndpoint.URL, "", "", http.StatusUnauthorized)
	assertHTTPStatus(t, firstEndpoint.URL, firstToken, "http://attacker.invalid", http.StatusForbidden)
	assertHostStatus(t, firstEndpoint.URL, firstToken, "attacker.invalid", http.StatusForbidden)

	if err := first.Close(ctx); err != nil {
		t.Fatalf("first.Close: %v", err)
	}
	assertHTTPStatus(t, firstEndpoint.URL, firstToken, "", http.StatusUnauthorized)
	secondSession = connectClient(t, ctx, secondEndpoint.URL, secondToken)
	assertToolValue(t, ctx, secondSession, "second", "still-live", "second:still-live")
	_ = secondSession.Close()

	if err := second.Close(ctx); err != nil {
		t.Fatalf("second.Close: %v", err)
	}
	if err := second.Close(ctx); err != nil {
		t.Fatalf("second.Close again: %v", err)
	}
	request, _ := http.NewRequestWithContext(ctx, http.MethodPost, secondEndpoint.URL, strings.NewReader(`{}`))
	request.Header.Set("Authorization", "Bearer "+secondToken)
	if response, requestErr := http.DefaultClient.Do(request); requestErr == nil {
		response.Body.Close()
		t.Fatalf("last registration close left endpoint reachable: status %d", response.StatusCode)
	}
}

func TestToolFailuresAreSanitizedAndDeadlinesPropagate(t *testing.T) {
	config := testGatewayConfig()
	config.handlerTimeout = 75 * time.Millisecond
	manager := newGatewayManager(config)
	definitions := []tool.Definition{
		tool.Define("reject", "reject safely", func(context.Context, testInput) (testOutput, error) {
			return testOutput{}, tool.Reject("not_found", "Choose an existing item.")
		}),
		tool.Define("internal", "fail internally", func(context.Context, testInput) (testOutput, error) {
			return testOutput{}, errors.New("DATABASE_PASSWORD=do-not-leak")
		}),
		tool.Define("panic", "panic internally", func(context.Context, testInput) (testOutput, error) {
			panic("PANIC_SECRET=do-not-leak")
		}),
		tool.Define("timeout", "honor a deadline", func(ctx context.Context, _ testInput) (testOutput, error) {
			<-ctx.Done()
			return testOutput{}, ctx.Err()
		}),
	}
	runtime, err := newRuntime(manager, definitions)
	if err != nil {
		t.Fatalf("newRuntime: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	endpoint, err := runtime.Start(ctx)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = runtime.Close(context.Background()) })
	session := connectClient(t, ctx, endpoint.URL, runtimeToken(t, runtime))
	defer session.Close()

	assertToolError(t, ctx, session, "reject", "not_found", "Choose an existing item.")
	internal := callToolErrorText(t, ctx, session, "internal")
	if strings.Contains(internal, "DATABASE_PASSWORD") || !strings.Contains(internal, "internal_error") {
		t.Fatalf("internal error was not sanitized: %q", internal)
	}
	panicText := callToolErrorText(t, ctx, session, "panic")
	if strings.Contains(panicText, "PANIC_SECRET") || !strings.Contains(panicText, "internal_error") {
		t.Fatalf("panic was not sanitized: %q", panicText)
	}
	timeoutText := callToolErrorText(t, ctx, session, "timeout")
	if !strings.Contains(timeoutText, "deadline_exceeded") {
		t.Fatalf("deadline error missing stable code: %q", timeoutText)
	}
}

func TestGatewayBoundsConcurrencyAndRequestBodies(t *testing.T) {
	config := testGatewayConfig()
	config.maxConcurrent = 1
	config.maxRequestBodyBytes = 256
	manager := newGatewayManager(config)
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	runtime := newTestRuntime(t, manager, "blocking", func(ctx context.Context, input testInput) (testOutput, error) {
		once.Do(func() { close(started) })
		select {
		case <-release:
			return testOutput{Value: input.Value}, nil
		case <-ctx.Done():
			return testOutput{}, ctx.Err()
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	endpoint, err := runtime.Start(ctx)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
		_ = runtime.Close(context.Background())
	})
	token := runtimeToken(t, runtime)
	session := connectClient(t, ctx, endpoint.URL, token)
	defer session.Close()
	callDone := make(chan error, 1)
	go func() {
		_, callErr := session.CallTool(ctx, &mcp.CallToolParams{Name: "blocking", Arguments: map[string]any{"value": "one"}})
		callDone <- callErr
	}()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("first tool call did not enter handler")
	}

	assertHTTPStatus(t, endpoint.URL, token, "", http.StatusServiceUnavailable)
	close(release)
	if err := <-callDone; err != nil {
		t.Fatalf("first tool call: %v", err)
	}

	request, _ := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.URL, bytes.NewReader(bytes.Repeat([]byte("x"), 1024)))
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("oversized request: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusRequestEntityTooLarge {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("oversized status = %d, want 413; body=%q", response.StatusCode, body)
	}
}

func TestClientCancellationReachesToolHandler(t *testing.T) {
	manager := newGatewayManager(testGatewayConfig())
	started := make(chan struct{})
	canceled := make(chan error, 1)
	runtime := newTestRuntime(t, manager, "cancel", func(ctx context.Context, _ testInput) (testOutput, error) {
		close(started)
		<-ctx.Done()
		canceled <- ctx.Err()
		return testOutput{}, ctx.Err()
	})
	baseCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	endpoint, err := runtime.Start(baseCtx)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = runtime.Close(context.Background()) })
	session := connectClient(t, baseCtx, endpoint.URL, runtimeToken(t, runtime))
	defer session.Close()
	callCtx, cancelCall := context.WithCancel(baseCtx)
	callDone := make(chan error, 1)
	go func() {
		_, callErr := session.CallTool(callCtx, &mcp.CallToolParams{
			Name: "cancel", Arguments: map[string]any{"value": "x"},
		})
		callDone <- callErr
	}()
	select {
	case <-started:
	case <-baseCtx.Done():
		t.Fatal("tool handler did not start")
	}
	cancelCall()
	select {
	case err := <-canceled:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("handler context error = %v, want canceled", err)
		}
	case <-baseCtx.Done():
		t.Fatal("client cancellation did not reach tool handler")
	}
	if err := <-callDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("CallTool error = %v, want canceled", err)
	}
}

func TestRuntimeFingerprintIsOrderIndependentAndRevisionSensitive(t *testing.T) {
	manager := newGatewayManager(testGatewayConfig())
	oneV1 := tool.Define("one", "first", echoHandler, tool.Revision("v1"), tool.ReadOnly())
	two := tool.Define("two", "second", echoHandler)
	first, err := newRuntime(manager, []tool.Definition{oneV1, two})
	if err != nil {
		t.Fatalf("first runtime: %v", err)
	}
	reordered, err := newRuntime(manager, []tool.Definition{two, oneV1})
	if err != nil {
		t.Fatalf("reordered runtime: %v", err)
	}
	changed, err := newRuntime(manager, []tool.Definition{
		tool.Define("one", "first", echoHandler, tool.Revision("v2"), tool.ReadOnly()), two,
	})
	if err != nil {
		t.Fatalf("changed runtime: %v", err)
	}
	if first.Fingerprint() != reordered.Fingerprint() {
		t.Fatal("definition order changed catalog fingerprint")
	}
	if first.Fingerprint() == changed.Fingerprint() {
		t.Fatal("semantic revision did not change catalog fingerprint")
	}
}

func TestExplicitFalseAnnotationsReachMCPAndFingerprint(t *testing.T) {
	falseHints := tool.Define("local", "local update", echoHandler,
		tool.NonDestructive(), tool.ClosedWorld(), tool.Revision("v1"))
	descriptor, err := falseHints.Descriptor()
	if err != nil {
		t.Fatal(err)
	}
	projected := mcpAnnotations(descriptor)
	if projected.DestructiveHint == nil || *projected.DestructiveHint {
		t.Fatalf("DestructiveHint = %#v, want explicit false", projected.DestructiveHint)
	}
	if projected.OpenWorldHint == nil || *projected.OpenWorldHint {
		t.Fatalf("OpenWorldHint = %#v, want explicit false", projected.OpenWorldHint)
	}

	manager := newGatewayManager(testGatewayConfig())
	unspecified, err := newRuntime(manager, []tool.Definition{
		tool.Define("local", "local update", echoHandler, tool.Revision("v1")),
	})
	if err != nil {
		t.Fatal(err)
	}
	explicit, err := newRuntime(manager, []tool.Definition{falseHints})
	if err != nil {
		t.Fatal(err)
	}
	if unspecified.Fingerprint() == explicit.Fingerprint() {
		t.Fatal("explicit false annotations did not change catalog fingerprint")
	}
}

func TestProductionRuntimeCloseCanRetryAfterCallerDeadline(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var entered atomic.Bool
	runtime, err := New([]tool.Definition{tool.Define("slow", "test delayed close", func(context.Context, testInput) (testOutput, error) {
		if entered.CompareAndSwap(false, true) {
			close(started)
		}
		// Deliberately violate the Handler contract to prove Close remains
		// bounded even when host code ignores cancellation.
		<-release
		return testOutput{}, errors.New("released after close")
	}, tool.Revision("v1"))})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	endpoint, err := runtime.Start(ctx)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	token := runtimeToken(t, runtime)
	session := connectClient(t, ctx, endpoint.URL, token)
	defer session.Close()
	callCtx, cancelCall := context.WithCancel(ctx)
	callDone := make(chan struct{})
	go func() {
		defer close(callDone)
		_, _ = session.CallTool(callCtx, &mcp.CallToolParams{Name: "slow", Arguments: map[string]any{"value": "x"}})
	}()
	<-started
	closeCtx, cancelClose := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancelClose()
	if err := runtime.Close(closeCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Close error = %v, want deadline exceeded", err)
	}
	if _, err := runtime.Start(ctx); !errors.Is(err, ErrClosed) {
		t.Fatalf("Start after close = %v, want ErrClosed", err)
	}
	assertHTTPUnavailable(t, endpoint.URL, token)

	const waiters = 8
	retryErrors := make(chan error, waiters)
	for range waiters {
		go func() {
			retryCtx, cancelRetry := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancelRetry()
			retryErrors <- runtime.Close(retryCtx)
		}()
	}
	close(release)
	cancelCall()
	<-callDone
	for range waiters {
		if err := <-retryErrors; err != nil {
			t.Fatalf("retry Close: %v", err)
		}
	}
	if err := runtime.Close(ctx); err != nil {
		t.Fatalf("idempotent Close after cleanup: %v", err)
	}
}

func echoHandler(_ context.Context, input testInput) (testOutput, error) {
	return testOutput{Value: input.Value}, nil
}

func testGatewayConfig() gatewayConfig {
	config := defaultGatewayConfig()
	config.handlerTimeout = 2 * time.Second
	config.shutdownTimeout = time.Second
	return config
}

func newTestRuntime(t *testing.T, manager *gatewayManager, name string, handler tool.Handler[testInput, testOutput]) *Runtime {
	t.Helper()
	runtime, err := newRuntime(manager, []tool.Definition{tool.Define(name, "test tool "+name, handler, tool.Revision("v1"))})
	if err != nil {
		t.Fatalf("newRuntime(%s): %v", name, err)
	}
	return runtime
}

func runtimeToken(t *testing.T, runtime *Runtime) string {
	t.Helper()
	token, ok := runtime.BearerToken()
	if !ok || token == "" {
		t.Fatal("runtime bearer token unavailable")
	}
	return token
}

type bearerTransport struct {
	token string
	base  http.RoundTripper
}

func (t bearerTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	clone := request.Clone(request.Context())
	clone.Header = request.Header.Clone()
	clone.Header.Set("Authorization", "Bearer "+t.token)
	return t.base.RoundTrip(clone)
}

func connectClient(t *testing.T, ctx context.Context, endpoint, token string) *mcp.ClientSession {
	t.Helper()
	client := mcp.NewClient(&mcp.Implementation{Name: "toolruntime-test", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint: endpoint,
		HTTPClient: &http.Client{Transport: bearerTransport{
			token: token,
			base:  http.DefaultTransport,
		}},
		DisableStandaloneSSE: true,
		MaxRetries:           -1,
	}, nil)
	if err != nil {
		t.Fatalf("connect MCP client: %v", err)
	}
	return session
}

func assertOnlyTool(t *testing.T, ctx context.Context, session *mcp.ClientSession, name string) {
	t.Helper()
	result, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(result.Tools) != 1 || result.Tools[0].Name != name {
		t.Fatalf("tools = %+v, want only %q", result.Tools, name)
	}
}

func assertToolValue(t *testing.T, ctx context.Context, session *mcp.ClientSession, name, input, want string) {
	t.Helper()
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: map[string]any{"value": input}})
	if err != nil {
		t.Fatalf("CallTool(%s): %v", name, err)
	}
	if result.IsError {
		t.Fatalf("CallTool(%s) returned tool error: %+v", name, result.Content)
	}
	structured, ok := result.StructuredContent.(map[string]any)
	if !ok || structured["value"] != want {
		t.Fatalf("CallTool(%s) structured content = %#v, want value %q", name, result.StructuredContent, want)
	}
}

func callToolErrorText(t *testing.T, ctx context.Context, session *mcp.ClientSession, name string) string {
	t.Helper()
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: map[string]any{"value": "x"}})
	if err != nil {
		t.Fatalf("CallTool(%s): %v", name, err)
	}
	if !result.IsError || len(result.Content) != 1 {
		t.Fatalf("CallTool(%s) result = %+v, want one error content", name, result)
	}
	text, ok := result.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("CallTool(%s) content type = %T", name, result.Content[0])
	}
	return text.Text
}

func assertToolError(t *testing.T, ctx context.Context, session *mcp.ClientSession, name, code, message string) {
	t.Helper()
	text := callToolErrorText(t, ctx, session, name)
	if !strings.Contains(text, code) || !strings.Contains(text, message) {
		t.Fatalf("CallTool(%s) error = %q, want code %q and message %q", name, text, code, message)
	}
}

func assertHTTPStatus(t *testing.T, endpoint, token, origin string, want int) {
	t.Helper()
	request, _ := http.NewRequest(http.MethodPost, endpoint, strings.NewReader(`{}`))
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	if origin != "" {
		request.Header.Set("Origin", origin)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("HTTP request: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != want {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("HTTP status = %d, want %d; body=%q", response.StatusCode, want, body)
	}
}

func assertHostStatus(t *testing.T, endpoint, token, host string, want int) {
	t.Helper()
	request, _ := http.NewRequest(http.MethodPost, endpoint, strings.NewReader(`{}`))
	request.Host = host
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("HTTP request: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != want {
		t.Fatalf("HTTP status = %d, want %d", response.StatusCode, want)
	}
}

func assertHTTPUnavailable(t *testing.T, endpoint, token string) {
	t.Helper()
	request, _ := http.NewRequest(http.MethodPost, endpoint, strings.NewReader(`{}`))
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := (&http.Client{Timeout: 250 * time.Millisecond}).Do(request)
	if err == nil {
		defer response.Body.Close()
		if response.StatusCode != http.StatusUnauthorized {
			t.Fatalf("closed registration HTTP status = %d, want unavailable or unauthorized", response.StatusCode)
		}
	}
}
