package sse_test

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	agentadaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/memory"
	"github.com/agent-dance/agent-adaptor/pkg/bridges/agui"
	"github.com/agent-dance/agent-adaptor/pkg/bridges/sse"
	"github.com/agent-dance/agent-adaptor/pkg/hosttools/a2adelegation"
)

// fakeAdapter emits a fixed StreamPayload sequence so we can exercise the
// SSE handler without a real codex CLI.
type fakeAdapter struct{}

func (fakeAdapter) Descriptor() agentadaptor.DriverDescriptor {
	return agentadaptor.DriverDescriptor{Type: "fake", DisplayName: "Fake"}
}
func (fakeAdapter) ValidateConfig(any) error { return nil }
func (fakeAdapter) StreamCapability() agentadaptor.StreamCapability {
	return agentadaptor.StreamCapability{Native: true, TokenLevel: true}
}
func (fakeAdapter) Run(ctx context.Context, req agentadaptor.DriverRunRequest, sink agentadaptor.EventSink) (agentadaptor.DriverRunResult, error) {
	if !req.Streaming {
		return agentadaptor.DriverRunResult{Output: req.Prompt, ExitCode: 0}, nil
	}

	payloads := []agentadaptor.StreamPayload{
		{Kind: agentadaptor.StreamRunStarted, ThreadID: "t", RunID: req.RunID},
		{Kind: agentadaptor.StreamTextStart, MessageID: "m1"},
		{Kind: agentadaptor.StreamTextContent, MessageID: "m1", Delta: "hello"},
		{Kind: agentadaptor.StreamTextContent, MessageID: "m1", Delta: " world"},
		{Kind: agentadaptor.StreamTextEnd, MessageID: "m1"},
		{Kind: agentadaptor.StreamRunFinished, ThreadID: "t", RunID: req.RunID,
			Usage: &agentadaptor.Usage{InputTokens: 1, OutputTokens: 2}},
	}
	for _, p := range payloads {
		if err := sink.EmitStream(p); err != nil {
			return agentadaptor.DriverRunResult{}, err
		}
	}
	return agentadaptor.DriverRunResult{
		Output:   "hello world",
		ExitCode: 0,
		Usage:    &agentadaptor.Usage{InputTokens: 1, OutputTokens: 2},
	}, nil
}

// metadataCapturingAdapter records the run metadata the SDK hands the adapter,
// so a test can assert the AGUI bridge surfaced the resolved session identity.
type metadataCapturingAdapter struct{ got chan map[string]string }

func (metadataCapturingAdapter) Descriptor() agentadaptor.DriverDescriptor {
	return agentadaptor.DriverDescriptor{Type: "fake", DisplayName: "Fake"}
}
func (metadataCapturingAdapter) ValidateConfig(any) error { return nil }
func (metadataCapturingAdapter) StreamCapability() agentadaptor.StreamCapability {
	return agentadaptor.StreamCapability{Native: true, TokenLevel: true}
}
func (a metadataCapturingAdapter) Run(_ context.Context, req agentadaptor.DriverRunRequest, sink agentadaptor.EventSink) (agentadaptor.DriverRunResult, error) {
	select {
	case a.got <- req.Metadata:
	default:
	}
	_ = sink.EmitStream(agentadaptor.StreamPayload{Kind: agentadaptor.StreamRunStarted, ThreadID: "t", RunID: req.RunID})
	_ = sink.EmitStream(agentadaptor.StreamPayload{Kind: agentadaptor.StreamRunFinished, ThreadID: "t", RunID: req.RunID})
	return agentadaptor.DriverRunResult{ExitCode: 0}, nil
}

// TestSSEHandlerAGUISurfacesSessionMetadata proves the AGUI bridge injects the
// resolved session namespace/key into run metadata, which host RuntimeService /
// Workspace managers rely on to key session-scoped resources.
func TestSSEHandlerAGUISurfacesSessionMetadata(t *testing.T) {
	t.Parallel()
	got := make(chan map[string]string, 1)
	sdk := agentadaptor.New(
		agentadaptor.WithDefaultAgent(agentadaptor.Bind(metadataCapturingAdapter{got: got}, nil)),
		agentadaptor.WithSessionStore(memory.NewSessionStore()),
	)
	srv := httptest.NewServer(sse.Handler(sdk, sse.Options{Protocol: sse.AGUI}))
	defer srv.Close()

	body := `{"threadId":"thread-abc","runId":"run-xyz","messages":[{"id":"m","role":"user","content":"hello"}]}`
	resp, err := http.Post(srv.URL, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	_ = readSSEFrames(t, resp.Body, 2*time.Second)

	select {
	case md := <-got:
		if md[sse.MetadataSessionNamespace] != "agui" {
			t.Fatalf("session namespace = %q, want agui (metadata=%v)", md[sse.MetadataSessionNamespace], md)
		}
		if md[sse.MetadataSessionKey] != "thread-abc" {
			t.Fatalf("session key = %q, want thread-abc (metadata=%v)", md[sse.MetadataSessionKey], md)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("adapter Run was not invoked; decode path broken")
	}
}

func newSDK(t *testing.T) agentadaptor.SDK {
	t.Helper()
	// The AGUI protocol derives a SessionKey from RunAgentInput.threadId
	// and calls WithSessionKey — which requires a SessionStore. An
	// in-memory store is the minimal setup that reflects the real
	// handler contract.
	return agentadaptor.New(
		agentadaptor.WithDefaultAgent(agentadaptor.Bind(fakeAdapter{}, nil)),
		agentadaptor.WithSessionStore(memory.NewSessionStore()),
	)
}

func TestSSEHandlerAGUIHappyPath(t *testing.T) {
	t.Parallel()
	sdk := newSDK(t)
	srv := httptest.NewServer(sse.Handler(sdk, sse.Options{Protocol: sse.AGUI}))
	defer srv.Close()

	// AG-UI protocol mandates a RunAgentInput envelope. The bridge owns
	// the decoding — the host writes zero glue.
	body := strings.NewReader(`{
		"threadId": "t-1",
		"runId": "r-1",
		"messages": [{"id":"m-1","role":"user","content":"hi"}]
	}`)
	resp, err := http.Post(srv.URL, "application/json", body)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("content-type: %q", got)
	}

	frames := readSSEFrames(t, resp.Body, 2*time.Second)
	var types []string
	for _, f := range frames {
		if f.data == "" {
			continue
		}
		var payload map[string]any
		if err := json.Unmarshal([]byte(f.data), &payload); err != nil {
			t.Fatalf("decode frame data: %v (raw=%q)", err, f.data)
		}
		if kind, _ := payload["type"].(string); kind != "" {
			types = append(types, kind)
		}
	}

	wantTypes := []string{
		"RUN_STARTED",
		"TEXT_MESSAGE_START",
		"TEXT_MESSAGE_CONTENT",
		"TEXT_MESSAGE_CONTENT",
		"TEXT_MESSAGE_END",
		"RUN_FINISHED",
	}
	if len(types) != len(wantTypes) {
		t.Fatalf("want %d AG-UI events, got %d (%v)", len(wantTypes), len(types), types)
	}
	for i, want := range wantTypes {
		if types[i] != want {
			t.Fatalf("event[%d]: want %q got %q (full=%v)", i, want, types[i], types)
		}
	}
}

func TestSSEHandlerRawProtocol(t *testing.T) {
	t.Parallel()
	sdk := newSDK(t)
	srv := httptest.NewServer(sse.Handler(sdk, sse.Options{Protocol: sse.Raw}))
	defer srv.Close()

	body := strings.NewReader(`{"prompt":"hi"}`)
	resp, err := http.Post(srv.URL, "application/json", body)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()

	frames := readSSEFrames(t, resp.Body, 2*time.Second)
	if len(frames) == 0 {
		t.Fatal("no SSE frames")
	}
	foundRunStarted := false
	foundRunFinished := false
	for _, f := range frames {
		switch f.event {
		case "run.started":
			foundRunStarted = true
		case "run.finished":
			foundRunFinished = true
		}
		if f.event != "" && f.data != "" {
			var payload agentadaptor.StreamPayload
			if err := json.Unmarshal([]byte(f.data), &payload); err != nil {
				t.Fatalf("decode raw frame: %v (data=%q)", err, f.data)
			}
		}
	}
	if !foundRunStarted || !foundRunFinished {
		t.Fatalf("missing lifecycle markers: started=%v finished=%v", foundRunStarted, foundRunFinished)
	}
}

func TestSSEHandlerRejectsEmptyPrompt(t *testing.T) {
	t.Parallel()

	// AG-UI default: no user message → 400.
	t.Run("agui/no-user-message", func(t *testing.T) {
		t.Parallel()
		sdk := newSDK(t)
		srv := httptest.NewServer(sse.Handler(sdk, sse.Options{}))
		defer srv.Close()

		body := `{"threadId":"t","runId":"r","messages":[{"id":"m","role":"assistant","content":"hi"}]}`
		resp, err := http.Post(srv.URL, "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatalf("post: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("want 400, got %d", resp.StatusCode)
		}
	})

	// Raw: no prompt → 400.
	t.Run("raw/empty-prompt", func(t *testing.T) {
		t.Parallel()
		sdk := newSDK(t)
		srv := httptest.NewServer(sse.Handler(sdk, sse.Options{Protocol: sse.Raw}))
		defer srv.Close()

		resp, err := http.Post(srv.URL, "application/json", strings.NewReader(`{}`))
		if err != nil {
			t.Fatalf("post: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("want 400, got %d", resp.StatusCode)
		}
	})
}

func TestSSEHandlerAGUIDecodesRunAgentInput(t *testing.T) {
	t.Parallel()
	sdk := newSDK(t)
	srv := httptest.NewServer(sse.Handler(sdk, sse.Options{Protocol: sse.AGUI}))
	defer srv.Close()

	// Content-parts array: only the text parts should be concatenated.
	body := `{
		"threadId": "thread-abc",
		"runId": "run-xyz",
		"messages": [
			{"id":"m-1","role":"user","content":"older msg, should be ignored"},
			{"id":"m-2","role":"assistant","content":"reply"},
			{"id":"m-3","role":"user","content":[{"type":"text","text":"hello"},{"type":"image","url":"data:..."},{"type":"text","text":"world"}]}
		]
	}`
	resp, err := http.Post(srv.URL, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	frames := readSSEFrames(t, resp.Body, 2*time.Second)
	if len(frames) == 0 {
		t.Fatalf("no frames; body rejected")
	}
	// Must see a RUN_STARTED frame — proves the prompt + session
	// binding made it through and sdk.Start fired.
	seenRunStarted := false
	for _, f := range frames {
		if f.data == "" {
			continue
		}
		var payload map[string]any
		if err := json.Unmarshal([]byte(f.data), &payload); err != nil {
			continue
		}
		if payload["type"] == "RUN_STARTED" {
			seenRunStarted = true
			break
		}
	}
	if !seenRunStarted {
		t.Fatalf("RUN_STARTED not observed; decode path broken")
	}
}

// TestSSEHandlerAGUIRejectsRawBody confirms the AGUI handler does not
// silently accept Raw-shaped bodies. Keeping the two wire contracts
// mutually exclusive is the whole point of removing DecodeRequest.
func TestSSEHandlerAGUIRejectsRawBody(t *testing.T) {
	t.Parallel()
	sdk := newSDK(t)
	srv := httptest.NewServer(sse.Handler(sdk, sse.Options{Protocol: sse.AGUI}))
	defer srv.Close()

	resp, err := http.Post(srv.URL, "application/json",
		strings.NewReader(`{"prompt":"hi","sessionKey":"x/y"}`))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("want 400 (no user message), got %d", resp.StatusCode)
	}
}

func TestSSEHandlerRejectsWrongMethod(t *testing.T) {
	t.Parallel()
	sdk := newSDK(t)
	srv := httptest.NewServer(sse.Handler(sdk, sse.Options{}))
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodPut, srv.URL, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("want 405, got %d", resp.StatusCode)
	}
}

type blockingAdapter struct{}

func (blockingAdapter) Descriptor() agentadaptor.DriverDescriptor {
	return agentadaptor.DriverDescriptor{Type: "blocking", DisplayName: "Blocking"}
}
func (blockingAdapter) ValidateConfig(any) error { return nil }
func (blockingAdapter) StreamCapability() agentadaptor.StreamCapability {
	return agentadaptor.StreamCapability{Native: true, TokenLevel: true}
}
func (blockingAdapter) Run(ctx context.Context, req agentadaptor.DriverRunRequest, sink agentadaptor.EventSink) (agentadaptor.DriverRunResult, error) {
	if err := sink.EmitStream(agentadaptor.StreamPayload{Kind: agentadaptor.StreamRunStarted, ThreadID: "t", RunID: req.RunID}); err != nil {
		return agentadaptor.DriverRunResult{}, err
	}
	select {
	case <-ctx.Done():
		return agentadaptor.DriverRunResult{}, ctx.Err()
	case <-time.After(200 * time.Millisecond):
	}
	if err := sink.EmitStream(agentadaptor.StreamPayload{Kind: agentadaptor.StreamRunFinished, ThreadID: "t", RunID: req.RunID}); err != nil {
		return agentadaptor.DriverRunResult{}, err
	}
	return agentadaptor.DriverRunResult{Output: "done", ExitCode: 0}, nil
}

func newBlockingSDK(t *testing.T) agentadaptor.SDK {
	t.Helper()
	return agentadaptor.New(
		agentadaptor.WithDefaultAgent(agentadaptor.Bind(blockingAdapter{}, nil)),
		agentadaptor.WithSessionStore(memory.NewSessionStore()),
	)
}

func TestSSEHandlerAGUIOverlaysSubagentEventsCustomMode(t *testing.T) {
	t.Parallel()
	sdk := newBlockingSDK(t)
	bus := scriptedSubagentBus{}
	srv := httptest.NewServer(sse.Handler(sdk, sse.Options{
		Protocol:     sse.AGUI,
		SubagentBus:  bus,
		SubagentMode: agui.SubagentAsCustom, // explicit legacy mode
	}))
	defer srv.Close()

	body := strings.NewReader(`{
		"threadId": "t-1",
		"runId": "r-1",
		"messages": [{"id":"m-1","role":"user","content":"hi"}]
	}`)
	resp, err := http.Post(srv.URL, "application/json", body)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}

	frames := readSSEFrames(t, resp.Body, 2*time.Second)
	seenSubagent := false
	for _, f := range frames {
		if f.data == "" {
			continue
		}
		var payload map[string]any
		if err := json.Unmarshal([]byte(f.data), &payload); err != nil {
			t.Fatalf("decode frame data: %v (raw=%q)", err, f.data)
		}
		if payload["type"] == "CUSTOM" && payload["name"] == "subagent.started" {
			seenSubagent = true
			value, _ := payload["value"].(map[string]any)
			if value["delegationId"] != "del-1" || value["agentKey"] != "research" {
				t.Fatalf("unexpected subagent payload: %#v", value)
			}
		}
	}
	if !seenSubagent {
		t.Fatalf("subagent custom event not observed in frames: %#v", frames)
	}
}

func TestSSEHandlerAGUIOverlaysSubagentEventsActivityMode(t *testing.T) {
	t.Parallel()
	sdk := newBlockingSDK(t)
	bus := scriptedSubagentBus{}
	// Default SubagentMode = SubagentAsActivity.
	srv := httptest.NewServer(sse.Handler(sdk, sse.Options{Protocol: sse.AGUI, SubagentBus: bus}))
	defer srv.Close()

	body := strings.NewReader(`{
		"threadId": "t-2",
		"runId": "r-2",
		"messages": [{"id":"m-2","role":"user","content":"hi"}]
	}`)
	resp, err := http.Post(srv.URL, "application/json", body)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}

	frames := readSSEFrames(t, resp.Body, 2*time.Second)
	seenSnapshot, seenDelta := false, false
	for _, f := range frames {
		if f.data == "" {
			continue
		}
		var payload map[string]any
		if err := json.Unmarshal([]byte(f.data), &payload); err != nil {
			t.Fatalf("decode frame data: %v (raw=%q)", err, f.data)
		}
		switch payload["type"] {
		case "ACTIVITY_SNAPSHOT":
			if payload["messageId"] == "del-1" && payload["activityType"] == "subagent" {
				seenSnapshot = true
				content, _ := payload["content"].(map[string]any)
				if content["agentKey"] != "research" {
					t.Fatalf("snapshot content.agentKey: got %v want \"research\"", content["agentKey"])
				}
			}
		case "ACTIVITY_DELTA":
			if payload["messageId"] == "del-1" {
				seenDelta = true
			}
		}
	}
	if !seenSnapshot {
		t.Fatalf("ACTIVITY_SNAPSHOT for del-1 not observed; frames=%#v", frames)
	}
	if !seenDelta {
		t.Fatalf("ACTIVITY_DELTA for del-1 not observed; frames=%#v", frames)
	}
}

type scriptedSubagentBus struct{}

func (scriptedSubagentBus) SubscribeRun(ctx context.Context, runID string) <-chan a2adelegation.DelegationEvent {
	out := make(chan a2adelegation.DelegationEvent, 2)
	go func() {
		defer close(out)
		select {
		case <-ctx.Done():
			return
		case out <- a2adelegation.DelegationEvent{RunID: runID, DelegationID: "del-1", AgentKey: "research", Kind: a2adelegation.DelegationStarted}:
		}
		select {
		case <-ctx.Done():
			return
		case out <- a2adelegation.DelegationEvent{RunID: runID, DelegationID: "del-1", AgentKey: "research", Kind: a2adelegation.DelegationFinished, Status: "completed"}:
		}
	}()
	return out
}

// ---------------------------------------------------------------------------
// SSE parsing helpers
// ---------------------------------------------------------------------------

type sseFrame struct {
	event string
	id    string
	data  string
}

func readSSEFrames(t *testing.T, r interface {
	Read([]byte) (int, error)
}, timeout time.Duration) []sseFrame {
	t.Helper()
	done := make(chan []sseFrame, 1)
	go func() {
		scanner := bufio.NewScanner(readerFunc(r.Read))
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		var frames []sseFrame
		var current sseFrame
		for scanner.Scan() {
			line := scanner.Text()
			if line == "" {
				if current.event != "" || current.data != "" || current.id != "" {
					frames = append(frames, current)
				}
				current = sseFrame{}
				continue
			}
			switch {
			case strings.HasPrefix(line, ":"):
				// comment / keep-alive; ignore
			case strings.HasPrefix(line, "event: "):
				current.event = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "id: "):
				current.id = strings.TrimPrefix(line, "id: ")
			case strings.HasPrefix(line, "data: "):
				current.data = strings.TrimPrefix(line, "data: ")
			}
		}
		done <- frames
	}()
	select {
	case f := <-done:
		return f
	case <-time.After(timeout):
		t.Fatalf("timeout reading SSE stream")
		return nil
	}
}

type readerFunc func(p []byte) (int, error)

func (f readerFunc) Read(p []byte) (int, error) { return f(p) }
