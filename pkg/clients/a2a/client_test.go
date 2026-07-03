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

func UserText(text string) Message {
	return Message{Role: "user", Parts: []Part{{Kind: PartText, Text: text, MediaType: "text/plain"}}}
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
