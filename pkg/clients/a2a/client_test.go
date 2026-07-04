package a2a

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestAgentCardFetchValidateAndCache(t *testing.T) {
	t.Parallel()

	var hits int
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/agent-card.json":
			hits++
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{
				"name":"Remote Reviewer",
				"description":"reviews code",
				"version":"1.0.0",
				"supportedInterfaces":[{"url":%q,"protocolBinding":"JSONRPC","protocolVersion":"1.0"}],
				"capabilities":{"streaming":true},
				"defaultInputModes":["text/plain"],
				"defaultOutputModes":["text/plain"],
				"skills":[{"id":"review","name":"Review","description":"review code","tags":["code"]}]
			}`, srv.URL+"/a2a")
		case "/a2a":
			t.Fatalf("unexpected protocol request while fetching card")
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	client := New(Options{AgentCardURL: srv.URL})
	first, err := client.AgentCard(context.Background())
	if err != nil {
		t.Fatalf("AgentCard() error = %v", err)
	}
	second, err := client.AgentCard(context.Background())
	if err != nil {
		t.Fatalf("AgentCard() second error = %v", err)
	}
	if hits != 1 {
		t.Fatalf("card fetch count = %d, want 1", hits)
	}
	if first.Name != "Remote Reviewer" || second.Fingerprint == "" {
		t.Fatalf("unexpected card: first=%+v second=%+v", first, second)
	}
	if len(first.SupportedInterfaces) != 1 || first.SupportedInterfaces[0].URL != srv.URL+"/a2a" {
		t.Fatalf("supported interfaces lost: %+v", first.SupportedInterfaces)
	}
}

func TestSendGetCancelAndStreamPreserveStructuredTask(t *testing.T) {
	t.Parallel()

	var methods []string
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/agent-card.json":
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{
				"name":"Remote Agent",
				"description":"test",
				"version":"1.0.0",
				"supportedInterfaces":[{"url":%q,"protocolBinding":"JSONRPC","protocolVersion":"1.0"}],
				"capabilities":{"streaming":true},
				"defaultInputModes":["text/plain"],
				"defaultOutputModes":["text/plain"],
				"skills":[{"id":"chat","name":"Chat","description":"chat","tags":["chat"]}]
			}`, srv.URL+"/a2a")
		case "/a2a":
			method := readRPCMethod(t, r)
			methods = append(methods, method)
			switch method {
			case "SendMessage":
				writeRPCResult(t, w, `{"task":`+taskJSON("TASK_STATE_COMPLETED")+`}`)
			case "GetTask":
				writeRPCResult(t, w, taskJSON("TASK_STATE_WORKING"))
			case "CancelTask":
				writeRPCResult(t, w, taskJSON("TASK_STATE_CANCELED"))
			case "SendStreamingMessage":
				writeSSE(t, w,
					rpcResult(`{"statusUpdate":{"taskId":"task-1","contextId":"ctx-1","status":{"state":"TASK_STATE_WORKING"}}}`),
					rpcResult(`{"artifactUpdate":{"taskId":"task-1","contextId":"ctx-1","artifact":{"artifactId":"artifact-1","name":"report","parts":[{"text":"chunk"}]},"lastChunk":true}}`),
					rpcResult(`{"task":`+taskJSON("TASK_STATE_COMPLETED")+`}`),
				)
			default:
				t.Fatalf("unexpected method %q", method)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	client := New(Options{AgentCardURL: srv.URL})
	ctx := context.Background()
	task, err := client.Send(ctx, SendRequest{Message: UserText("review this")})
	if err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	if task.ID != "task-1" || task.ContextID != "ctx-1" || task.Status.State != TaskStateCompleted {
		t.Fatalf("Send() task = %+v", task)
	}
	if len(task.Artifacts) != 1 || task.Artifacts[0].ID != "artifact-1" {
		t.Fatalf("Send() artifacts = %+v", task.Artifacts)
	}

	got, err := client.GetTask(ctx, "task-1")
	if err != nil {
		t.Fatalf("GetTask() error = %v", err)
	}
	if got.Status.State != TaskStateWorking {
		t.Fatalf("GetTask() state = %q", got.Status.State)
	}

	cancelled, err := client.CancelTask(ctx, "task-1")
	if err != nil {
		t.Fatalf("CancelTask() error = %v", err)
	}
	if cancelled.Status.State != TaskStateCanceled {
		t.Fatalf("CancelTask() state = %q", cancelled.Status.State)
	}

	stream, err := client.SendStream(ctx, SendRequest{Message: UserText("stream")})
	if err != nil {
		t.Fatalf("SendStream() error = %v", err)
	}
	defer stream.Close()
	var events []Event
	for {
		ev, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("Recv() error = %v", err)
		}
		events = append(events, ev)
	}
	if gotKinds := []EventKind{events[0].Kind, events[1].Kind, events[2].Kind}; fmt.Sprint(gotKinds) != "[status artifact terminal]" {
		t.Fatalf("stream kinds = %v", gotKinds)
	}
	if events[1].Artifact == nil || events[1].Artifact.ID != "artifact-1" {
		t.Fatalf("artifact event = %+v", events[1])
	}
	if len(methods) < 4 {
		t.Fatalf("expected protocol methods, got %v", methods)
	}
}

func TestClientClassifiesProtocolErrors(t *testing.T) {
	t.Parallel()

	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/agent-card.json":
			_, _ = fmt.Fprintf(w, `{
				"name":"Remote Agent",
				"description":"test",
				"version":"1.0.0",
				"supportedInterfaces":[{"url":%q,"protocolBinding":"JSONRPC","protocolVersion":"1.0"}],
				"capabilities":{},
				"defaultInputModes":["text/plain"],
				"defaultOutputModes":["text/plain"],
				"skills":[{"id":"chat","name":"Chat","description":"chat","tags":["chat"]}]
			}`, srv.URL+"/a2a")
		case "/a2a":
			_ = readRPCMethod(t, r)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":"1","error":{"code":-32001,"message":"missing task","data":[{"@type":"type.googleapis.com/google.protobuf.Struct","reason":"expired","scope":"task.read"}]}}`))
		}
	}))
	defer srv.Close()

	client := New(Options{AgentCardURL: srv.URL})
	_, err := client.GetTask(context.Background(), "missing")
	if err == nil {
		t.Fatal("GetTask() error = nil")
	}
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("errors.Is(err, ErrNotFound) = false; err=%v", err)
	}
	var protoErr *ProtocolError
	if !errors.As(err, &protoErr) {
		t.Fatalf("errors.As(%T, *ProtocolError) = false", err)
	}
	details, ok := protoErr.Raw["details"].(map[string]any)
	if !ok {
		t.Fatalf("ProtocolError.Raw details missing: %+v", protoErr.Raw)
	}
	if details["reason"] != "expired" || details["scope"] != "task.read" {
		t.Fatalf("ProtocolError.Raw details = %+v", details)
	}
	typed, ok := protoErr.Raw["typed_details"].([]map[string]any)
	if !ok || len(typed) != 1 || typed[0]["@type"] != "type.googleapis.com/google.protobuf.Struct" {
		t.Fatalf("ProtocolError.Raw typed_details = %+v", protoErr.Raw["typed_details"])
	}
}

func TestStreamCloseCancelsUpstreamRequest(t *testing.T) {
	t.Parallel()

	requestStarted := make(chan struct{})
	requestDone := make(chan struct{})
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/agent-card.json":
			_, _ = fmt.Fprintf(w, `{
				"name":"Remote Agent",
				"description":"test",
				"version":"1.0.0",
				"supportedInterfaces":[{"url":%q,"protocolBinding":"JSONRPC","protocolVersion":"1.0"}],
				"capabilities":{"streaming":true},
				"defaultInputModes":["text/plain"],
				"defaultOutputModes":["text/plain"],
				"skills":[{"id":"chat","name":"Chat","description":"chat","tags":["chat"]}]
			}`, srv.URL+"/a2a")
		case "/a2a":
			if method := readRPCMethod(t, r); method != "SendStreamingMessage" {
				t.Fatalf("method = %q, want SendStreamingMessage", method)
			}
			w.Header().Set("Content-Type", "text/event-stream")
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
			close(requestStarted)
			<-r.Context().Done()
			close(requestDone)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	client := New(Options{AgentCardURL: srv.URL})
	stream, err := client.SendStream(context.Background(), SendRequest{Message: UserText("stream")})
	if err != nil {
		t.Fatalf("SendStream() error = %v", err)
	}
	select {
	case <-requestStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("stream request did not start")
	}
	if err := stream.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	select {
	case <-requestDone:
	case <-time.After(2 * time.Second):
		t.Fatal("stream Close did not cancel upstream request")
	}
}

func TestSendStreamTreatsMessageAsExecutionFinal(t *testing.T) {
	t.Parallel()

	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/agent-card.json":
			_, _ = fmt.Fprintf(w, `{
				"name":"Remote Agent",
				"description":"test",
				"version":"1.0.0",
				"supportedInterfaces":[{"url":%q,"protocolBinding":"JSONRPC","protocolVersion":"1.0"}],
				"capabilities":{"streaming":true},
				"defaultInputModes":["text/plain"],
				"defaultOutputModes":["text/plain"],
				"skills":[{"id":"chat","name":"Chat","description":"chat","tags":["chat"]}]
			}`, srv.URL+"/a2a")
		case "/a2a":
			if method := readRPCMethod(t, r); method != "SendStreamingMessage" {
				t.Fatalf("method = %q, want SendStreamingMessage", method)
			}
			writeSSE(t, w,
				rpcResult(`{"message":`+messageJSON("final answer")+`}`),
				rpcResult(statusUpdateJSON("TASK_STATE_WORKING")),
				rpcResult(`{"task":`+taskJSON("TASK_STATE_COMPLETED")+`}`),
			)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	client := New(Options{AgentCardURL: srv.URL})
	stream, err := client.SendStream(context.Background(), SendRequest{Message: UserText("stream")})
	if err != nil {
		t.Fatalf("SendStream() error = %v", err)
	}
	defer stream.Close()

	events := collectStreamEvents(t, stream)
	if len(events) != 1 {
		t.Fatalf("event count = %d, want 1", len(events))
	}
	if events[0].Kind != EventTerminal || events[0].Message == nil || events[0].Message.ID != "msg-agent" {
		t.Fatalf("message final event = %+v", events[0])
	}
}

func TestSendStreamTreatsInputRequiredAsExecutionFinal(t *testing.T) {
	t.Parallel()

	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/agent-card.json":
			_, _ = fmt.Fprintf(w, `{
				"name":"Remote Agent",
				"description":"test",
				"version":"1.0.0",
				"supportedInterfaces":[{"url":%q,"protocolBinding":"JSONRPC","protocolVersion":"1.0"}],
				"capabilities":{"streaming":true},
				"defaultInputModes":["text/plain"],
				"defaultOutputModes":["text/plain"],
				"skills":[{"id":"chat","name":"Chat","description":"chat","tags":["chat"]}]
			}`, srv.URL+"/a2a")
		case "/a2a":
			if method := readRPCMethod(t, r); method != "SendStreamingMessage" {
				t.Fatalf("method = %q, want SendStreamingMessage", method)
			}
			writeSSE(t, w,
				rpcResult(statusUpdateJSON("TASK_STATE_WORKING")),
				rpcResult(statusUpdateJSON("TASK_STATE_INPUT_REQUIRED")),
				rpcResult(`{"task":`+taskJSON("TASK_STATE_COMPLETED")+`}`),
			)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	client := New(Options{AgentCardURL: srv.URL})
	stream, err := client.SendStream(context.Background(), SendRequest{Message: UserText("stream")})
	if err != nil {
		t.Fatalf("SendStream() error = %v", err)
	}
	defer stream.Close()

	events := collectStreamEvents(t, stream)
	if len(events) != 2 {
		t.Fatalf("event count = %d, want 2", len(events))
	}
	if gotKinds := []EventKind{events[0].Kind, events[1].Kind}; fmt.Sprint(gotKinds) != "[status terminal]" {
		t.Fatalf("stream kinds = %v", gotKinds)
	}
	if events[1].Status == nil || events[1].Status.State != TaskStateInputRequired {
		t.Fatalf("input-required final event = %+v", events[1])
	}
}

func TestSendStreamIgnoresLateTerminalAfterFirstFinal(t *testing.T) {
	t.Parallel()

	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/agent-card.json":
			_, _ = fmt.Fprintf(w, `{
				"name":"Remote Agent",
				"description":"test",
				"version":"1.0.0",
				"supportedInterfaces":[{"url":%q,"protocolBinding":"JSONRPC","protocolVersion":"1.0"}],
				"capabilities":{"streaming":true},
				"defaultInputModes":["text/plain"],
				"defaultOutputModes":["text/plain"],
				"skills":[{"id":"chat","name":"Chat","description":"chat","tags":["chat"]}]
			}`, srv.URL+"/a2a")
		case "/a2a":
			if method := readRPCMethod(t, r); method != "SendStreamingMessage" {
				t.Fatalf("method = %q, want SendStreamingMessage", method)
			}
			writeSSE(t, w,
				rpcResult(`{"task":`+taskJSON("TASK_STATE_COMPLETED")+`}`),
				rpcResult(statusUpdateJSON("TASK_STATE_FAILED")),
			)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	client := New(Options{AgentCardURL: srv.URL})
	stream, err := client.SendStream(context.Background(), SendRequest{Message: UserText("stream")})
	if err != nil {
		t.Fatalf("SendStream() error = %v", err)
	}
	defer stream.Close()

	events := collectStreamEvents(t, stream)
	if len(events) != 1 {
		t.Fatalf("event count = %d, want 1", len(events))
	}
	if events[0].Kind != EventTerminal || events[0].Task == nil || events[0].Task.Status.State != TaskStateCompleted {
		t.Fatalf("terminal event = %+v", events[0])
	}
}

func TestSendStreamRecoversExecutionFinalTask(t *testing.T) {
	t.Parallel()

	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/agent-card.json":
			_, _ = fmt.Fprintf(w, `{
				"name":"Remote Agent",
				"description":"test",
				"version":"1.0.0",
				"supportedInterfaces":[{"url":%q,"protocolBinding":"JSONRPC","protocolVersion":"1.0"}],
				"capabilities":{"streaming":true},
				"defaultInputModes":["text/plain"],
				"defaultOutputModes":["text/plain"],
				"skills":[{"id":"chat","name":"Chat","description":"chat","tags":["chat"]}]
			}`, srv.URL+"/a2a")
		case "/a2a":
			switch method := readRPCMethod(t, r); method {
			case "SendStreamingMessage":
				writeBrokenSSE(t, w,
					rpcResult(statusUpdateJSON("TASK_STATE_WORKING")),
					`{"jsonrpc":"2.0","id":"1","result":`,
				)
			case "GetTask":
				writeRPCResult(t, w, taskJSON("TASK_STATE_INPUT_REQUIRED"))
			default:
				t.Fatalf("unexpected method %q", method)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	client := New(Options{AgentCardURL: srv.URL})
	stream, err := client.SendStream(context.Background(), SendRequest{Message: UserText("stream")})
	if err != nil {
		t.Fatalf("SendStream() error = %v", err)
	}
	defer stream.Close()

	events := collectStreamEvents(t, stream)
	if len(events) != 2 {
		t.Fatalf("event count = %d, want 2", len(events))
	}
	if events[1].Kind != EventTerminal || events[1].Task == nil || !events[1].RecoveredState {
		t.Fatalf("recovered terminal event = %+v", events[1])
	}
	if events[1].Task.Status.State != TaskStateInputRequired {
		t.Fatalf("recovered state = %q, want %q", events[1].Task.Status.State, TaskStateInputRequired)
	}
}

func TestSendStreamRecoveryFailsForNonFinalTask(t *testing.T) {
	t.Parallel()

	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/agent-card.json":
			_, _ = fmt.Fprintf(w, `{
				"name":"Remote Agent",
				"description":"test",
				"version":"1.0.0",
				"supportedInterfaces":[{"url":%q,"protocolBinding":"JSONRPC","protocolVersion":"1.0"}],
				"capabilities":{"streaming":true},
				"defaultInputModes":["text/plain"],
				"defaultOutputModes":["text/plain"],
				"skills":[{"id":"chat","name":"Chat","description":"chat","tags":["chat"]}]
			}`, srv.URL+"/a2a")
		case "/a2a":
			switch method := readRPCMethod(t, r); method {
			case "SendStreamingMessage":
				writeBrokenSSE(t, w,
					rpcResult(statusUpdateJSON("TASK_STATE_WORKING")),
					`{"jsonrpc":"2.0","id":"1","result":`,
				)
			case "GetTask":
				writeRPCResult(t, w, taskJSON("TASK_STATE_WORKING"))
			default:
				t.Fatalf("unexpected method %q", method)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	client := New(Options{AgentCardURL: srv.URL})
	stream, err := client.SendStream(context.Background(), SendRequest{Message: UserText("stream")})
	if err != nil {
		t.Fatalf("SendStream() error = %v", err)
	}
	defer stream.Close()

	first, err := stream.Recv()
	if err != nil {
		t.Fatalf("first Recv() error = %v", err)
	}
	if first.Kind != EventStatus || first.Status == nil || first.Status.State != TaskStateWorking {
		t.Fatalf("first event = %+v", first)
	}
	_, err = stream.Recv()
	var recoveryErr *StreamRecoveryError
	if !errors.As(err, &recoveryErr) {
		t.Fatalf("Recv() error = %v, want *StreamRecoveryError", err)
	}
}

func TestSubscribeRejectsUnsupportedSinceCursor(t *testing.T) {
	t.Parallel()

	client := New(Options{AgentCardURL: "https://remote.example/.well-known/agent-card.json"})
	_, err := client.Subscribe(context.Background(), SubscribeRequest{TaskID: "task-1", Since: "cursor-1"})
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("Subscribe() error = %v, want ErrUnsupported", err)
	}
}

func TestClientRejectsCrossOriginBearerByDefault(t *testing.T) {
	t.Parallel()

	type hit struct {
		auth string
	}
	hits := make(chan hit, 1)

	var protocol *httptest.Server
	protocol = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits <- hit{auth: r.Header.Get("Authorization")}
		t.Fatalf("unexpected request to untrusted protocol endpoint")
	}))
	defer protocol.Close()

	var card *httptest.Server
	card = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/agent-card.json":
			_, _ = fmt.Fprintf(w, `{
				"name":"Remote Agent",
				"description":"test",
				"version":"1.0.0",
				"supportedInterfaces":[{"url":%q,"protocolBinding":"JSONRPC","protocolVersion":"1.0"}],
				"capabilities":{},
				"defaultInputModes":["text/plain"],
				"defaultOutputModes":["text/plain"],
				"skills":[{"id":"chat","name":"Chat","description":"chat","tags":["chat"]}]
			}`, protocol.URL+"/a2a")
		default:
			http.NotFound(w, r)
		}
	}))
	defer card.Close()

	client := New(Options{AgentCardURL: card.URL, Auth: BearerToken("secret")})
	_, err := client.Send(context.Background(), SendRequest{Message: UserText("review this")})
	if !errors.Is(err, ErrUntrustedOrigin) {
		t.Fatalf("Send() error = %v, want ErrUntrustedOrigin", err)
	}
	select {
	case got := <-hits:
		t.Fatalf("unexpected request to untrusted endpoint with auth %q", got.auth)
	default:
	}
}

func TestClientAllowsTrustedCrossOriginBearerWithOptIn(t *testing.T) {
	t.Parallel()

	authHeaders := make(chan string, 1)

	var protocol *httptest.Server
	protocol = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeaders <- r.Header.Get("Authorization")
		if method := readRPCMethod(t, r); method != "SendMessage" {
			t.Fatalf("method = %q, want SendMessage", method)
		}
		writeRPCResult(t, w, `{"task":`+taskJSON("TASK_STATE_COMPLETED")+`}`)
	}))
	defer protocol.Close()

	var card *httptest.Server
	card = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/agent-card.json":
			_, _ = fmt.Fprintf(w, `{
				"name":"Remote Agent",
				"description":"test",
				"version":"1.0.0",
				"supportedInterfaces":[{"url":%q,"protocolBinding":"JSONRPC","protocolVersion":"1.0"}],
				"capabilities":{},
				"defaultInputModes":["text/plain"],
				"defaultOutputModes":["text/plain"],
				"skills":[{"id":"chat","name":"Chat","description":"chat","tags":["chat"]}]
			}`, protocol.URL+"/a2a")
		default:
			http.NotFound(w, r)
		}
	}))
	defer card.Close()

	client := New(Options{
		AgentCardURL:       card.URL,
		Auth:               BearerToken("secret"),
		TrustedAuthOrigins: []string{protocol.URL},
	})
	if _, err := client.Send(context.Background(), SendRequest{Message: UserText("review this")}); err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	select {
	case got := <-authHeaders:
		if got != "Bearer secret" {
			t.Fatalf("Authorization = %q, want %q", got, "Bearer secret")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("trusted protocol request did not arrive")
	}
}

func TestClientDoesNotLeakBearerOnCrossOriginRedirect(t *testing.T) {
	t.Parallel()

	redirectedAuth := make(chan string, 1)

	var redirected *httptest.Server
	redirected = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirectedAuth <- r.Header.Get("Authorization")
		if method := readRPCMethod(t, r); method != "SendMessage" {
			t.Fatalf("redirected method = %q, want SendMessage", method)
		}
		writeRPCResult(t, w, `{"task":`+taskJSON("TASK_STATE_COMPLETED")+`}`)
	}))
	defer redirected.Close()

	var card *httptest.Server
	card = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/agent-card.json":
			_, _ = fmt.Fprintf(w, `{
				"name":"Remote Agent",
				"description":"test",
				"version":"1.0.0",
				"supportedInterfaces":[{"url":%q,"protocolBinding":"JSONRPC","protocolVersion":"1.0"}],
				"capabilities":{},
				"defaultInputModes":["text/plain"],
				"defaultOutputModes":["text/plain"],
				"skills":[{"id":"chat","name":"Chat","description":"chat","tags":["chat"]}]
			}`, card.URL+"/a2a")
		case "/a2a":
			http.Redirect(w, r, redirected.URL+"/a2a", http.StatusTemporaryRedirect)
		default:
			http.NotFound(w, r)
		}
	}))
	defer card.Close()

	client := New(Options{AgentCardURL: card.URL, Auth: BearerToken("secret")})
	if _, err := client.Send(context.Background(), SendRequest{Message: UserText("review this")}); err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	select {
	case got := <-redirectedAuth:
		if got != "" {
			t.Fatalf("redirected Authorization = %q, want empty", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("redirected protocol request did not arrive")
	}
}

func UserText(text string) Message {
	return Message{Role: "user", Parts: []Part{{Kind: PartText, Text: text, MediaType: "text/plain"}}}
}

func collectStreamEvents(t *testing.T, stream *Stream) []Event {
	t.Helper()
	var events []Event
	for {
		ev, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			return events
		}
		if err != nil {
			t.Fatalf("Recv() error = %v", err)
		}
		events = append(events, ev)
	}
}

func taskJSON(state string) string {
	return `{
		"id":"task-1",
		"contextId":"ctx-1",
		"status":{
			"state":"` + state + `",
			"message":{"messageId":"msg-agent","role":"ROLE_AGENT","taskId":"task-1","contextId":"ctx-1","parts":[{"text":"done"}]}
		},
		"history":[{"messageId":"msg-user","role":"ROLE_USER","taskId":"task-1","contextId":"ctx-1","parts":[{"text":"hi"}]}],
		"artifacts":[{"artifactId":"artifact-1","name":"report","parts":[{"text":"body"}]}],
		"metadata":{"remote":"yes"}
	}`
}

func messageJSON(text string) string {
	return `{
		"messageId":"msg-agent",
		"role":"ROLE_AGENT",
		"taskId":"task-1",
		"contextId":"ctx-1",
		"parts":[{"text":"` + text + `"}]
	}`
}

func statusUpdateJSON(state string) string {
	return `{
		"statusUpdate":{
			"taskId":"task-1",
			"contextId":"ctx-1",
			"status":{
				"state":"` + state + `",
				"message":{"messageId":"msg-agent","role":"ROLE_AGENT","taskId":"task-1","contextId":"ctx-1","parts":[{"text":"state"}]}
			}
		}
	}`
}

func readRPCMethod(t *testing.T, r *http.Request) string {
	t.Helper()
	var req struct {
		Method string `json:"method"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		t.Fatalf("decode RPC request: %v", err)
	}
	return req.Method
}

func writeRPCResult(t *testing.T, w http.ResponseWriter, result string) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":"1","result":%s}`, result)
}

func rpcResult(result string) string {
	return fmt.Sprintf(`{"jsonrpc":"2.0","id":"1","result":%s}`, result)
}

func writeSSE(t *testing.T, w http.ResponseWriter, payloads ...string) {
	t.Helper()
	w.Header().Set("Content-Type", "text/event-stream")
	for _, payload := range payloads {
		var compact bytes.Buffer
		if err := json.Compact(&compact, []byte(payload)); err != nil {
			t.Fatalf("compact SSE payload: %v\n%s", err, payload)
		}
		_, _ = fmt.Fprintf(w, "data: %s\n\n", compact.String())
	}
}

func writeBrokenSSE(t *testing.T, w http.ResponseWriter, payloads ...string) {
	t.Helper()
	w.Header().Set("Content-Type", "text/event-stream")
	if flusher, ok := w.(http.Flusher); ok {
		defer flusher.Flush()
	}
	for i, payload := range payloads {
		if i == len(payloads)-1 {
			_, _ = fmt.Fprintf(w, "data: %s\n\n", payload)
			return
		}
		var compact bytes.Buffer
		if err := json.Compact(&compact, []byte(payload)); err != nil {
			t.Fatalf("compact broken SSE payload: %v\n%s", err, payload)
		}
		_, _ = fmt.Fprintf(w, "data: %s\n\n", compact.String())
	}
}
