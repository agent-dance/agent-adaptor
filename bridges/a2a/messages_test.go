package a2a_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	adaptor "github.com/agent-dance/agent-adaptor/v2"
	bridge "github.com/agent-dance/agent-adaptor/v2/bridges/a2a"
	client "github.com/agent-dance/agent-adaptor/v2/clients/a2a"
	"github.com/agent-dance/agent-adaptor/v2/driver"
)

// handlerTransport streams HTTP directly between handlers and clients. Neither
// discovery nor JSON-RPC/SSE opens a socket.
type handlerTransport struct{ handler http.Handler }

func (transport handlerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	reader, writer := io.Pipe()
	ready := make(chan *http.Response, 1)
	response := &pipeResponse{header: make(http.Header), writer: writer, ready: ready, request: req}
	stop := context.AfterFunc(req.Context(), func() { _ = reader.CloseWithError(req.Context().Err()) })
	go func() {
		defer stop()
		transport.handler.ServeHTTP(response, req)
		response.WriteHeader(http.StatusOK)
		_ = writer.Close()
	}()
	select {
	case <-req.Context().Done():
		_ = reader.Close()
		return nil, req.Context().Err()
	case result := <-ready:
		result.Body = reader
		return result, nil
	}
}

type pipeResponse struct {
	header  http.Header
	writer  *io.PipeWriter
	ready   chan *http.Response
	request *http.Request
	once    sync.Once
}

func (w *pipeResponse) Header() http.Header { return w.header }
func (w *pipeResponse) WriteHeader(code int) {
	w.once.Do(func() { w.ready <- &http.Response{StatusCode: code, Header: w.header.Clone(), Request: w.request} })
}
func (w *pipeResponse) Write(p []byte) (int, error) {
	w.WriteHeader(http.StatusOK)
	return w.writer.Write(p)
}
func (w *pipeResponse) Flush() { w.WriteHeader(http.StatusOK) }

func bridgeClient(t *testing.T, runner adaptor.Runner, opts bridge.ServerOptions) *client.Client {
	t.Helper()
	opts.AgentCard = testCard()
	srv := bridge.NewServer(runner, opts)
	mux := http.NewServeMux()
	mux.Handle("/card", srv.AgentCardHandler())
	mux.Handle("/a2a", srv.Handler())
	c := client.New(client.Options{AgentCardURL: "https://example.com/card", HTTPClient: &http.Client{Transport: handlerTransport{mux}}})
	t.Cleanup(func() { _ = c.Close() })
	return c
}
func sendInput(id, text, task, conversation string) client.SendRequest {
	return client.SendRequest{TaskID: task, ContextID: conversation, ReturnImmediately: true, Message: client.Message{ID: id, Role: "user", Parts: []client.Part{{Kind: client.PartText, Text: text}}}}
}
func deadline(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}
func healthy(text string) driver.Response {
	return driver.Response{Output: text, Summary: "safe", Checkpoint: &driver.Checkpoint{Valid: true, State: &driver.SessionState{ResumeID: "healthy"}}}
}
func wireEvents(t *testing.T, event client.Event) []adaptor.Event {
	t.Helper()
	var out []adaptor.Event
	if event.Status == nil || event.Status.Message == nil {
		return out
	}
	for _, part := range event.Status.Message.Parts {
		if part.Kind != client.PartData {
			continue
		}
		decoded, matched, err := bridge.DecodeAdapterEventV1(part.Data)
		if err != nil {
			t.Fatal(err)
		}
		if matched {
			out = append(out, decoded)
		}
	}
	return out
}

func TestSameTaskSupplementAndBusyContext(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprintf("failure=%v", fail), func(t *testing.T) {
			ctx := deadline(t)
			started, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			t.Cleanup(func() { once.Do(func() { close(release) }) })
			fake := &scriptedDriver{run: func(turn int, req driver.Request, _ driver.EventSink) (driver.Response, error) {
				if turn == 0 {
					close(started)
					select {
					case <-release:
					case <-ctx.Done():
						return driver.Response{}, ctx.Err()
					}
					if fail {
						return driver.Response{}, errors.New("provider stopped")
					}
					return healthy("first"), nil
				}
				if !strings.Contains(req.Prompt, "Peer agent input") || !strings.Contains(req.Prompt, "Reviewer") || !strings.Contains(req.Prompt, "supplement") {
					return driver.Response{}, fmt.Errorf("missing supplement: %s", req.Prompt)
				}
				return healthy("integrated"), nil
			}}
			c := bridgeClient(t, adaptor.New(fake), bridge.ServerOptions{})
			stream, err := c.SendStream(ctx, sendInput("initial", "start", "", "ctx"))
			if err != nil {
				t.Fatal(err)
			}
			defer stream.Close()
			first, err := stream.RecvContext(ctx)
			if err != nil {
				t.Fatal(err)
			}
			task := first.TaskID
			select {
			case <-started:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			if req := fake.request(t, 0); req.Session == nil || req.Session.Mode != driver.SessionContinueOrStart {
				t.Fatalf("context-only default did not use Thread.Send: %+v", req.Session)
			}
			_, err = c.Send(ctx, sendInput("busy", "unrelated", "", "ctx"))
			if err == nil || !strings.Contains(err.Error(), "task in progress") || !strings.Contains(err.Error(), task) {
				t.Fatalf("busy error = %v, task=%s", err, task)
			}
			busyStream, err := c.SendStream(ctx, sendInput("busy-stream", "unrelated", "", "ctx"))
			if err != nil {
				t.Fatal(err)
			}
			_, err = busyStream.RecvContext(ctx)
			_ = busyStream.Close()
			if err == nil || !strings.Contains(err.Error(), "task in progress") || !strings.Contains(err.Error(), task) {
				t.Fatalf("streaming busy error = %v, task=%s", err, task)
			}
			request := sendInput("supplement-id", "supplement", task, first.ContextID)
			request.Message.Metadata = map[string]any{"agentadaptor.author": map[string]any{"kind": "agent", "id": "peer", "name": "Reviewer"}}
			// Returning/cancelling the short request must not withdraw accepted input.
			requestCtx, cancel := context.WithCancel(ctx)
			accepted, err := c.Send(requestCtx, request)
			cancel()
			if err != nil || accepted.ID != task || accepted.Status.State.Terminal() {
				t.Fatalf("accepted=%+v err=%v", accepted, err)
			}
			once.Do(func() { close(release) })
			states := map[adaptor.MessageState]bool{}
			turns, done := 0, 0
			var last adaptor.Event
			for {
				event, err := stream.RecvContext(ctx)
				if errors.Is(err, io.EOF) {
					break
				}
				if err != nil {
					t.Fatal(err)
				}
				for _, decoded := range wireEvents(t, event) {
					last = decoded
					switch e := decoded.(type) {
					case adaptor.MessageUpdate:
						if e.ID == "supplement-id" {
							states[e.State] = true
							if e.State == adaptor.MessageFailed && e.Text != "supplement" {
								t.Fatalf("failed text=%q", e.Text)
							}
						}
					case adaptor.TurnEnd:
						turns++
						if e.Turn == 2 && (len(e.MessageIDs) != 1 || e.MessageIDs[0] != "supplement-id") {
							t.Fatalf("turn message IDs=%v", e.MessageIDs)
						}
					case adaptor.Done:
						done++
						if fail && e.Err == nil {
							t.Fatal("missing final failure")
						}
						if !fail && (e.Result == nil || e.Result.Text != "integrated" || e.Result.Metadata != nil || e.Result.Usage != nil) {
							t.Fatalf("done=%+v", e)
						}
					}
				}
			}
			want := adaptor.MessageDelivered
			if fail {
				want = adaptor.MessageFailed
			}
			if !states[adaptor.MessageAccepted] || !states[want] || done != 1 {
				t.Fatalf("states=%v done=%d", states, done)
			}
			if _, ok := last.(adaptor.Done); !ok {
				t.Fatalf("last=%T", last)
			}
			if !fail && turns != 2 {
				t.Fatalf("turns=%d", turns)
			}
			_, err = c.Send(ctx, sendInput("late", "late", task, first.ContextID))
			if err == nil || !strings.Contains(err.Error(), "task finished") {
				t.Fatalf("late=%v", err)
			}
			fake.mu.Lock()
			runs := len(fake.requests)
			fake.mu.Unlock()
			if (!fail && runs != 2) || (fail && runs != 1) {
				t.Fatalf("runs=%d", runs)
			}
		})
	}
}

func TestSameTaskAdmissionFailureIsProtocolError(t *testing.T) {
	ctx := deadline(t)
	release := make(chan struct{})
	defer close(release)
	fake := &scriptedDriver{run: func(_ int, _ driver.Request, _ driver.EventSink) (driver.Response, error) {
		select {
		case <-release:
			return healthy("done"), nil
		case <-ctx.Done():
			return driver.Response{}, ctx.Err()
		}
	}}
	c := bridgeClient(t, adaptor.New(fake, adaptor.WithPolicy(adaptor.Policy{Messages: adaptor.MessagePolicy{Disabled: true}})), bridge.ServerOptions{})
	task, err := c.Send(ctx, sendInput("initial", "start", "", "ctx"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Send(ctx, sendInput("rejected", "supplement", task.ID, task.ContextID))
	if err == nil {
		t.Fatal("disabled messages accepted")
	}
}

func TestDoneDefaultExposureFiltersErrorAndTurnMetadata(t *testing.T) {
	fake := &scriptedDriver{run: func(_ int, _ driver.Request, _ driver.EventSink) (driver.Response, error) {
		return driver.Response{Output: "answer", Summary: "summary", Metadata: map[string]string{"private": "private-metadata"}, Usage: &driver.Usage{InputTokens: 19}, RawStreams: &driver.RawStreams{Stdout: "private-raw"}, Failure: &driver.RunFailure{Code: driver.FailureAgentError, Message: "failure", Metadata: map[string]any{"private": "private-details"}}}, nil
	}}
	c := bridgeClient(t, adaptor.New(fake), bridge.ServerOptions{})
	stream, err := c.SendStream(deadline(t), sendInput("initial", "start", "", "ctx"))
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	done := false
	for {
		ev, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		data, _ := json.Marshal(ev.Raw)
		for _, secret := range []string{"private-metadata", "private-details", "private-raw"} {
			if strings.Contains(string(data), secret) {
				t.Fatalf("leaked %s: %s", secret, data)
			}
		}
		for _, e := range wireEvents(t, ev) {
			if _, ok := e.(adaptor.Done); ok {
				done = true
			}
		}
	}
	if !done {
		t.Fatal("no final Done")
	}
}

type cancellingDriver struct{ scriptedDriver }

func (*cancellingDriver) Run(ctx context.Context, _ driver.Request, _ driver.EventSink) (driver.Response, error) {
	<-ctx.Done()
	return driver.Response{}, ctx.Err()
}

func TestCancelKeepsFailedReceiptBeforeDone(t *testing.T) {
	ctx := deadline(t)
	c := bridgeClient(t, adaptor.New(&cancellingDriver{}), bridge.ServerOptions{})
	stream, err := c.SendStream(ctx, sendInput("initial", "start", "", "ctx"))
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	first, err := stream.RecvContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Send(ctx, sendInput("pending", "original", first.TaskID, first.ContextID))
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.CancelTask(ctx, client.CancelTaskRequest{TaskID: first.TaskID})
	if err != nil {
		t.Fatal(err)
	}
	failed, done := false, false
	for {
		event, err := stream.RecvContext(ctx)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		for _, decoded := range wireEvents(t, event) {
			switch e := decoded.(type) {
			case adaptor.MessageUpdate:
				if e.ID == "pending" && e.State == adaptor.MessageFailed {
					if done || e.Text != "original" {
						t.Fatal(e)
					}
					failed = true
				}
			case adaptor.Done:
				if !failed || !errors.Is(e.Err, context.Canceled) {
					t.Fatal(e.Err, failed)
				}
				done = true
			}
		}
	}
	if !failed || !done {
		t.Fatal(failed, done)
	}
}

type statelessOnlyDriver struct{ scriptedDriver }

func (d *statelessOnlyDriver) Descriptor() driver.Descriptor {
	desc := d.scriptedDriver.Descriptor()
	desc.Sessions = driver.SessionCapability{}
	return desc
}

func TestExplicitStatelessBindingDoesNotRequireThreadDriver(t *testing.T) {
	fake := &statelessOnlyDriver{}
	c := bridgeClient(t, adaptor.New(fake), bridge.ServerOptions{Session: bridge.Stateless()})
	task, err := c.Send(deadline(t), client.SendRequest{Message: client.Message{ID: "initial", Role: "user", Parts: []client.Part{{Kind: client.PartText, Text: "start"}}}, ContextID: "context"})
	if err != nil || task.Status.State != client.TaskStateCompleted {
		t.Fatal(task.Status, err)
	}
}
