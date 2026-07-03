package a2a_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	agentadaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/pkg/bridges/a2a"
)

func TestAgentCardHandlerReturnsConfiguredCard(t *testing.T) {
	t.Parallel()

	server := a2a.NewServer(fakeRunner{}, a2a.ServerOptions{
		AgentCard: a2a.AgentCard{
			Name: "Bridge Agent", Description: "test", Version: "1.0.0", URL: "https://example.com/a2a",
			Skills: []a2a.Skill{{ID: "chat", Name: "Chat", Description: "chat"}},
		},
	})

	rec := httptest.NewRecorder()
	server.AgentCardHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/.well-known/agent-card.json", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var card struct {
		Name         string `json:"name"`
		Capabilities struct {
			Streaming bool `json:"streaming"`
		} `json:"capabilities"`
		SupportedInterfaces []struct {
			URL             string `json:"url"`
			ProtocolBinding string `json:"protocolBinding"`
			ProtocolVersion string `json:"protocolVersion"`
		} `json:"supportedInterfaces"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&card); err != nil {
		t.Fatalf("decode card: %v", err)
	}
	if card.Name != "Bridge Agent" || !card.Capabilities.Streaming {
		t.Fatalf("unexpected card: %+v", card)
	}
	if len(card.SupportedInterfaces) != 1 || card.SupportedInterfaces[0].ProtocolVersion != "1.0" {
		t.Fatalf("interfaces = %+v", card.SupportedInterfaces)
	}
}

func TestSendMessageMapsRunnerToA2ATask(t *testing.T) {
	t.Parallel()

	var starts atomic.Int32
	server := a2a.NewServer(scriptedRunner{start: func(ctx context.Context, prompt string, opts ...agentadaptor.RunOption) (agentadaptor.RunHandle, error) {
		starts.Add(1)
		if prompt != "hello bridge" {
			t.Fatalf("prompt = %q", prompt)
		}
		h := newScriptedHandle("run-1")
		go func() {
			h.emit(agentadaptor.StreamPayload{Kind: agentadaptor.StreamTextContent, Delta: "hello"})
			h.finish(agentadaptor.RunResult{
				RunID: "run-1", DriverType: "fake", Output: "hello", Summary: "done",
				Result: map[string]any{"ok": true},
			}, nil)
		}()
		return h, nil
	}}, testOptions())

	resp := postRPC(t, server.Handler(), `{"jsonrpc":"2.0","id":"1","method":"SendMessage","params":{"message":{"messageId":"m1","role":"ROLE_USER","parts":[{"text":"hello bridge"}]}}}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var envelope struct {
		Result struct {
			Task *struct {
				ID     string `json:"id"`
				Status struct {
					State string `json:"state"`
				} `json:"status"`
				Artifacts []struct {
					Name  string `json:"name"`
					Parts []struct {
						Text string         `json:"text"`
						Data map[string]any `json:"data"`
					} `json:"parts"`
				} `json:"artifacts"`
			} `json:"task"`
		} `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if starts.Load() != 1 {
		t.Fatalf("Start calls = %d", starts.Load())
	}
	if envelope.Result.Task == nil || envelope.Result.Task.Status.State != "TASK_STATE_COMPLETED" {
		t.Fatalf("task = %+v", envelope.Result.Task)
	}
	if len(envelope.Result.Task.Artifacts) == 0 {
		t.Fatalf("expected artifacts: %+v", envelope.Result.Task)
	}
}

func TestSendStreamingMessageEmitsOrderedUpdates(t *testing.T) {
	t.Parallel()

	server := a2a.NewServer(scriptedRunner{start: func(ctx context.Context, prompt string, opts ...agentadaptor.RunOption) (agentadaptor.RunHandle, error) {
		h := newScriptedHandle("run-stream")
		go func() {
			time.Sleep(20 * time.Millisecond)
			h.emit(agentadaptor.StreamPayload{Kind: agentadaptor.StreamTextContent, Delta: "hel"})
			h.emit(agentadaptor.StreamPayload{Kind: agentadaptor.StreamTextContent, Delta: "lo"})
			h.finish(agentadaptor.RunResult{RunID: "run-stream", Output: "hello", Summary: "done"}, nil)
		}()
		return h, nil
	}}, testOptions())

	resp := postRPC(t, server.Handler(), `{"jsonrpc":"2.0","id":"stream","method":"SendStreamingMessage","params":{"message":{"messageId":"m1","role":"ROLE_USER","parts":[{"text":"stream"}]}}}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	frames := readSSEFrames(t, resp.Body, 2*time.Second)
	if len(frames) < 4 {
		t.Fatalf("frames = %d, want at least 4: %+v", len(frames), frames)
	}
	if frames[0].Result.Task == nil {
		t.Fatalf("first frame should be task: %+v", frames[0])
	}
	if frames[1].Result.StatusUpdate == nil || frames[1].Result.StatusUpdate.Status.State != "TASK_STATE_WORKING" {
		t.Fatalf("second frame should be working: %+v", frames[1])
	}
	if frames[2].Result.ArtifactUpdate == nil || frames[2].Result.ArtifactUpdate.Artifact.Parts[0].Text != "hel" {
		t.Fatalf("third frame should be artifact hel: %+v", frames[2])
	}
	if frames[len(frames)-1].Result.StatusUpdate == nil || frames[len(frames)-1].Result.StatusUpdate.Status.State != "TASK_STATE_COMPLETED" {
		t.Fatalf("last frame should be completed: %+v", frames[len(frames)-1])
	}
}

func TestCancelTaskCancelsUnderlyingRun(t *testing.T) {
	t.Parallel()

	handle := newScriptedHandle("run-cancel")
	runStarted := make(chan struct{})
	server := a2a.NewServer(scriptedRunner{start: func(ctx context.Context, prompt string, opts ...agentadaptor.RunOption) (agentadaptor.RunHandle, error) {
		close(runStarted)
		return handle, nil
	}}, testOptions())

	resp := postRPC(t, server.Handler(), `{"jsonrpc":"2.0","id":"start","method":"SendMessage","params":{"message":{"messageId":"m1","role":"ROLE_USER","parts":[{"text":"cancel"}]},"configuration":{"returnImmediately":true}}}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("send status = %d", resp.StatusCode)
	}
	var started struct {
		Result struct {
			Task struct {
				ID string `json:"id"`
			} `json:"task"`
		} `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&started); err != nil {
		t.Fatalf("decode start: %v", err)
	}
	if started.Result.Task.ID == "" {
		t.Fatal("missing task id")
	}
	select {
	case <-runStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("runner did not start before cancellation")
	}

	cancel := postRPC(t, server.Handler(), fmt.Sprintf(`{"jsonrpc":"2.0","id":"cancel","method":"CancelTask","params":{"id":%q}}`, started.Result.Task.ID))
	defer cancel.Body.Close()
	if cancel.StatusCode != http.StatusOK {
		t.Fatalf("cancel status = %d", cancel.StatusCode)
	}
	if handle.cancelled.Load() != 1 {
		t.Fatalf("Cancel calls = %d", handle.cancelled.Load())
	}
	if handle.cancelHadDeadline.Load() != 1 {
		t.Fatal("Cancel context did not carry a deadline")
	}
	var cancelled struct {
		Result struct {
			Status struct {
				State string `json:"state"`
			} `json:"status"`
		} `json:"result"`
	}
	if err := json.NewDecoder(cancel.Body).Decode(&cancelled); err != nil {
		t.Fatalf("decode cancel: %v", err)
	}
	if cancelled.Result.Status.State != "TASK_STATE_CANCELED" {
		t.Fatalf("cancelled state = %q", cancelled.Result.Status.State)
	}
}

func testOptions() a2a.ServerOptions {
	return a2a.ServerOptions{AgentCard: a2a.AgentCard{
		Name: "Test Agent", Description: "test", Version: "1.0.0", URL: "https://example.com/a2a",
		Skills: []a2a.Skill{{ID: "chat", Name: "Chat", Description: "chat"}},
	}}
}

type scriptedRunner struct {
	start func(context.Context, string, ...agentadaptor.RunOption) (agentadaptor.RunHandle, error)
}

func (r scriptedRunner) Run(ctx context.Context, prompt string, opts ...agentadaptor.RunOption) (agentadaptor.RunResult, error) {
	handle, err := r.Start(ctx, prompt, opts...)
	if err != nil {
		return agentadaptor.RunResult{}, err
	}
	return handle.Wait(ctx)
}

func (r scriptedRunner) Start(ctx context.Context, prompt string, opts ...agentadaptor.RunOption) (agentadaptor.RunHandle, error) {
	if r.start == nil {
		return nil, errors.New("unexpected start")
	}
	return r.start(ctx, prompt, opts...)
}

type fakeRunner struct{}

func (fakeRunner) Run(context.Context, string, ...agentadaptor.RunOption) (agentadaptor.RunResult, error) {
	return agentadaptor.RunResult{}, nil
}

func (fakeRunner) Start(context.Context, string, ...agentadaptor.RunOption) (agentadaptor.RunHandle, error) {
	return nil, errors.New("not implemented")
}

type scriptedHandle struct {
	runID             string
	stream            chan agentadaptor.StreamPayload
	done              chan waitResult
	once              sync.Once
	cancelled         atomic.Int32
	cancelHadDeadline atomic.Int32
}

type waitResult struct {
	result agentadaptor.RunResult
	err    error
}

func newScriptedHandle(runID string) *scriptedHandle {
	return &scriptedHandle{runID: runID, stream: make(chan agentadaptor.StreamPayload, 16), done: make(chan waitResult, 1)}
}

func (h *scriptedHandle) Events() <-chan agentadaptor.RunEvent {
	ch := make(chan agentadaptor.RunEvent)
	close(ch)
	return ch
}
func (h *scriptedHandle) StreamEvents() <-chan agentadaptor.StreamPayload { return h.stream }
func (h *scriptedHandle) RunID() string                                   { return h.runID }
func (h *scriptedHandle) DecisionRequests() <-chan agentadaptor.DecisionRequest {
	ch := make(chan agentadaptor.DecisionRequest)
	close(ch)
	return ch
}
func (h *scriptedHandle) ResolveDecision(string, agentadaptor.DecisionResponse) error { return nil }
func (h *scriptedHandle) Wait(ctx context.Context) (agentadaptor.RunResult, error) {
	select {
	case <-ctx.Done():
		return agentadaptor.RunResult{}, ctx.Err()
	case r := <-h.done:
		return r.result, r.err
	}
}
func (h *scriptedHandle) Cancel(ctx context.Context) error {
	h.cancelled.Add(1)
	if _, ok := ctx.Deadline(); ok {
		h.cancelHadDeadline.Store(1)
	}
	h.once.Do(func() {
		close(h.stream)
		h.done <- waitResult{result: agentadaptor.RunResult{
			RunID:   h.runID,
			Failure: &agentadaptor.RunFailure{Code: agentadaptor.FailureCancelled, Message: "cancelled"},
		}}
	})
	return nil
}
func (h *scriptedHandle) emit(p agentadaptor.StreamPayload) { h.stream <- p }
func (h *scriptedHandle) finish(result agentadaptor.RunResult, err error) {
	h.once.Do(func() {
		close(h.stream)
		h.done <- waitResult{result: result, err: err}
	})
}

func postRPC(t *testing.T, handler http.Handler, body string) *http.Response {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	resp, err := http.Post(srv.URL, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("post RPC: %v", err)
	}
	return resp
}

type sseFrame struct {
	Result struct {
		Task *struct {
			ID string `json:"id"`
		} `json:"task"`
		StatusUpdate *struct {
			Status struct {
				State string `json:"state"`
			} `json:"status"`
		} `json:"statusUpdate"`
		ArtifactUpdate *struct {
			Artifact struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"artifact"`
		} `json:"artifactUpdate"`
	} `json:"result"`
}

func readSSEFrames(t *testing.T, body io.Reader, timeout time.Duration) []sseFrame {
	t.Helper()
	done := make(chan []sseFrame, 1)
	go func() {
		scanner := bufio.NewScanner(body)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		var frames []sseFrame
		for scanner.Scan() {
			line := scanner.Text()
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			var frame sseFrame
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &frame); err == nil {
				frames = append(frames, frame)
			}
		}
		done <- frames
	}()
	select {
	case frames := <-done:
		return frames
	case <-time.After(timeout):
		t.Fatal("timed out reading SSE")
		return nil
	}
}
