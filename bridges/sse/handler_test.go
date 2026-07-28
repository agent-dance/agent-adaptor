package sse_test

// Contract tests for the Handler(Runner) entry: Raw and AG-UI wire,
// thread binding through the inbound
// session identity, and — the mandated anchor — deterministic
// disconnect-cancel semantics (client disconnect → request ctx cancel → run
// cancel → suppressed error frame), synchronized entirely by channels.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/bridges/sse"
	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/memory"
)

const sseRequestLimitForTest = 4 << 20

// fakeDriver is a programmable driver.Driver for handler tests.
type fakeDriver struct {
	mu       sync.Mutex
	requests []driver.Request

	// payloads/response drive the default scripted run when runFunc is nil.
	payloads []driver.StreamPayload
	response driver.Response

	// runFunc, when set, fully controls the run.
	runFunc func(ctx context.Context, req driver.Request, sink driver.EventSink) (driver.Response, error)
}

type fakeSessionCodec struct{}

type deadlineTrackingWriter struct {
	header    http.Header
	body      bytes.Buffer
	status    int
	deadlines []time.Time
}

func (w *deadlineTrackingWriter) Header() http.Header {
	if w.header == nil {
		w.header = make(http.Header)
	}
	return w.header
}

func (w *deadlineTrackingWriter) WriteHeader(status int) { w.status = status }
func (w *deadlineTrackingWriter) Write(p []byte) (int, error) {
	return w.body.Write(p)
}
func (w *deadlineTrackingWriter) Flush() {}
func (w *deadlineTrackingWriter) SetWriteDeadline(deadline time.Time) error {
	w.deadlines = append(w.deadlines, deadline)
	return nil
}

func (fakeSessionCodec) Name() string { return "fake/session" }
func (fakeSessionCodec) ToParams(state *driver.SessionState) driver.SessionParams {
	if state == nil {
		return driver.SessionParams{}
	}
	return driver.SessionParams{ResumeID: state.ResumeID, DisplayID: state.DisplayID, Values: state.Data}
}
func (fakeSessionCodec) FromParams(params driver.SessionParams) *driver.SessionState {
	if params.ResumeID == "" && params.DisplayID == "" && len(params.Values) == 0 {
		return nil
	}
	return &driver.SessionState{ResumeID: params.ResumeID, DisplayID: params.DisplayID, Data: params.Values}
}
func (fakeSessionCodec) GuardFingerprint(params driver.SessionParams) string {
	return params.ResumeID
}

func (f *fakeDriver) Descriptor() driver.Descriptor {
	return driver.Descriptor{
		Type:        "fake",
		DisplayName: "Fake",
		Sessions:    driver.SessionCapability{SupportsResume: true},
	}
}

func (f *fakeDriver) ValidateConfig(any) error { return nil }

func (f *fakeDriver) SessionConfigFingerprint() (string, error) { return "fake", nil }
func (f *fakeDriver) SessionCodec() driver.SessionCodec         { return fakeSessionCodec{} }

func (f *fakeDriver) Run(ctx context.Context, req driver.Request, sink driver.EventSink) (driver.Response, error) {
	f.mu.Lock()
	f.requests = append(f.requests, req)
	fn := f.runFunc
	f.mu.Unlock()
	if fn != nil {
		return fn(ctx, req, sink)
	}
	for _, p := range f.payloads {
		if err := sink.EmitStream(p); err != nil {
			return driver.Response{}, err
		}
	}
	return f.response, nil
}

func (f *fakeDriver) request(t *testing.T, i int) driver.Request {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if i >= len(f.requests) {
		t.Fatalf("fake driver saw %d request(s), want index %d", len(f.requests), i)
	}
	return f.requests[i]
}

func TestHandlerRawHappyPath(t *testing.T) {
	t.Parallel()
	fake := &fakeDriver{
		payloads: []driver.StreamPayload{
			{Kind: driver.StreamRunStarted, ThreadID: "t", RunID: "r"},
			{Kind: driver.StreamTextContent, MessageID: "m1", Delta: "hello"},
			{Kind: driver.StreamRunFinished, ThreadID: "t", RunID: "r"},
		},
		response: driver.Response{Output: "hello"},
	}
	srv := httptest.NewServer(sse.Handler(adaptor.New(fake), sse.Options{Protocol: sse.Raw}))
	defer srv.Close()

	resp, err := http.Post(srv.URL, "application/json", strings.NewReader(`{"prompt":"hi"}`))
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
	if len(frames) == 0 {
		t.Fatal("no SSE frames")
	}

	counts := map[string]int{}
	sawHello := false
	lastID := 0
	for _, f := range frames {
		counts[f.event]++
		if f.event == "text.delta" && strings.Contains(f.data, `"hello"`) {
			sawHello = true
		}
		// EventMeta.Sequence is the v1 cursor authority and starts at one.
		id, err := strconv.Atoi(f.id)
		if err != nil {
			t.Fatalf("frame id %q is not an integer", f.id)
		}
		if id != lastID+1 {
			t.Fatalf("frame ids not sequential: %d after %d", id, lastID)
		}
		lastID = id
	}
	if counts["run.started"] == 0 {
		t.Errorf("no run.started frame (frames=%+v)", frames)
	}
	if !sawHello {
		t.Errorf("no text.delta frame carrying %q (frames=%+v)", "hello", frames)
	}
	if counts["run.finished"] == 0 {
		t.Errorf("no run.finished frame (frames=%+v)", frames)
	}
	if frames[len(frames)-1].event != "run.finished" {
		t.Errorf("last frame = %q, want run.finished", frames[len(frames)-1].event)
	}
}

func TestHandlerAppliesAndClearsWriteDeadline(t *testing.T) {
	t.Parallel()
	fake := &fakeDriver{
		payloads: []driver.StreamPayload{{Kind: driver.StreamTextContent, Delta: "hello"}},
		response: driver.Response{Output: "hello"},
	}
	handler := sse.Handler(adaptor.New(fake), sse.Options{
		Protocol:     sse.Raw,
		WriteTimeout: 250 * time.Millisecond,
	})
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"prompt":"hi"}`))
	recorder := &deadlineTrackingWriter{}

	handler.ServeHTTP(recorder, req)

	if recorder.status != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.status, http.StatusOK)
	}
	if len(recorder.deadlines) < 2 {
		t.Fatalf("SetWriteDeadline calls = %d, want at least an arm and clear", len(recorder.deadlines))
	}
	var armed bool
	for _, deadline := range recorder.deadlines[:len(recorder.deadlines)-1] {
		if !deadline.IsZero() {
			armed = true
			break
		}
	}
	if !armed {
		t.Fatal("no non-zero per-write deadline was applied")
	}
	if last := recorder.deadlines[len(recorder.deadlines)-1]; !last.IsZero() {
		t.Fatalf("final deadline = %v, want cleared zero value", last)
	}
}

func TestHandlerAGUIHappyPath(t *testing.T) {
	t.Parallel()
	fake := &fakeDriver{
		payloads: []driver.StreamPayload{
			{Kind: driver.StreamRunStarted, ThreadID: "t", RunID: "r"},
			{Kind: driver.StreamTextStart, MessageID: "m1"},
			{Kind: driver.StreamTextContent, MessageID: "m1", Delta: "hello"},
			{Kind: driver.StreamTextContent, MessageID: "m1", Delta: " world"},
			{Kind: driver.StreamTextEnd, MessageID: "m1"},
			{Kind: driver.StreamRunFinished, ThreadID: "t", RunID: "r"},
		},
		response: driver.Response{
			Output:     "hello world",
			Checkpoint: &driver.Checkpoint{Valid: true, State: &driver.SessionState{ResumeID: "session-happy"}},
		},
	}
	agent := adaptor.New(fake, adaptor.WithThreadStore(memory.NewStore()))
	srv := httptest.NewServer(sse.Handler(agent, sse.Options{Protocol: sse.AGUI}))
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
	if len(types) == 0 {
		t.Fatal("no AG-UI events on the wire")
	}
	if types[0] != "RUN_STARTED" {
		t.Errorf("first event = %q, want RUN_STARTED", types[0])
	}
	if types[len(types)-1] != "RUN_FINISHED" {
		t.Errorf("last event = %q, want RUN_FINISHED", types[len(types)-1])
	}

	// The core message lifecycle must retain protocol order; extra CUSTOM
	// notices may interleave.
	core := make([]string, 0, len(types))
	for _, k := range types {
		switch k {
		case "RUN_STARTED", "TEXT_MESSAGE_START", "TEXT_MESSAGE_CONTENT", "TEXT_MESSAGE_END", "RUN_FINISHED":
			core = append(core, k)
		}
	}
	want := []string{
		"RUN_STARTED",
		"TEXT_MESSAGE_START",
		"TEXT_MESSAGE_CONTENT",
		"TEXT_MESSAGE_CONTENT",
		"TEXT_MESSAGE_END",
		"RUN_FINISHED",
	}
	if len(core) != len(want) {
		t.Fatalf("core sequence = %v, want %v (all=%v)", core, want, types)
	}
	for i := range want {
		if core[i] != want[i] {
			t.Fatalf("core[%d] = %q, want %q (all=%v)", i, core[i], want[i], types)
		}
	}
}

// TestHandlerAGUIThreadContinuation anchors the session-binding half of the
// v1 handler: the AG-UI threadId maps to a collision-free AG-UI Thread key, so
// a second request on the same threadId continues from the checkpoint the
// first run left in the thread store.
func TestHandlerAGUIThreadContinuation(t *testing.T) {
	t.Parallel()
	fake := &fakeDriver{}
	fake.runFunc = func(ctx context.Context, req driver.Request, sink driver.EventSink) (driver.Response, error) {
		if req.Session != nil && req.Session.State != nil {
			_ = sink.EmitStream(driver.StreamPayload{Kind: driver.StreamTextContent, MessageID: "m2", Delta: "again"})
			return driver.Response{Output: "continued:" + req.Session.State.ResumeID}, nil
		}
		_ = sink.EmitStream(driver.StreamPayload{Kind: driver.StreamTextContent, MessageID: "m1", Delta: "first"})
		return driver.Response{
			Output:     "one",
			Checkpoint: &driver.Checkpoint{State: &driver.SessionState{ResumeID: "sess-9"}, Valid: true},
		}, nil
	}
	agent := adaptor.New(fake, adaptor.WithThreadStore(memory.NewStore()))
	srv := httptest.NewServer(sse.Handler(agent, sse.Options{Protocol: sse.AGUI}))
	defer srv.Close()

	post := func(prompt string) {
		t.Helper()
		body := strings.NewReader(`{
			"threadId": "chat-42",
			"runId": "",
			"messages": [{"id":"m-1","role":"user","content":"` + prompt + `"}]
		}`)
		resp, err := http.Post(srv.URL, "application/json", body)
		if err != nil {
			t.Fatalf("post: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status: %d", resp.StatusCode)
		}
		_ = readSSEFrames(t, resp.Body, 2*time.Second) // drain to completion
	}

	post("turn one")
	post("turn two")

	sess := fake.request(t, 1).Session
	if sess == nil || sess.State == nil || sess.State.ResumeID != "sess-9" {
		t.Fatalf("second run session = %+v, want continuation carrying sess-9", sess)
	}
	if sess.Mode != driver.SessionContinueOrStart {
		t.Errorf("second run session mode = %q, want %q", sess.Mode, driver.SessionContinueOrStart)
	}
}

// responseRecorder is a channel-instrumented ResponseWriter: it signals the first
// body write so the disconnect test can synchronize on "a frame reached the
// client" without sleeping.
type responseRecorder struct {
	mu     sync.Mutex
	header http.Header
	buf    bytes.Buffer
	wrote  chan struct{}
	once   sync.Once
}

func newResponseRecorder() *responseRecorder {
	return &responseRecorder{header: http.Header{}, wrote: make(chan struct{})}
}

func (r *responseRecorder) Header() http.Header { return r.header }
func (r *responseRecorder) WriteHeader(int)     {}
func (r *responseRecorder) Flush()              {}

func (r *responseRecorder) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	n, err := r.buf.Write(p)
	r.once.Do(func() { close(r.wrote) })
	return n, err
}

func (r *responseRecorder) String() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.buf.String()
}

// TestHandlerDisconnectCancelsRun is the mandated disconnect-cancel anchor,
// aligned item-by-item with the handler's disconnect contract:
//
//  1. client disconnect (request ctx cancel) →
//  2. the run context the driver sees is cancelled →
//  3. the streaming loop returns context.Canceled →
//  4. the handler suppresses the error frame (no RUN_ERROR on the wire).
//
// Deterministic: every step is gated on a channel — driver-emitted frame
// observed on the writer, then cancel, then handler return, then the driver's
// observed error. No sleeps.
func TestHandlerDisconnectCancelsRun(t *testing.T) {
	t.Parallel()

	runStarted := make(chan struct{})
	driverErr := make(chan error, 1)
	fake := &fakeDriver{}
	fake.runFunc = func(ctx context.Context, _ driver.Request, sink driver.EventSink) (driver.Response, error) {
		_ = sink.EmitStream(driver.StreamPayload{Kind: driver.StreamTextContent, MessageID: "m1", Delta: "hello"})
		close(runStarted)
		<-ctx.Done() // hold the run open until the disconnect propagates
		driverErr <- ctx.Err()
		return driver.Response{}, ctx.Err()
	}

	handler := sse.Handler(adaptor.New(fake), sse.Options{Protocol: sse.Raw})
	rec := newResponseRecorder()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"prompt":"hi"}`)).WithContext(ctx)

	handlerDone := make(chan struct{})
	go func() {
		defer close(handlerDone)
		handler.ServeHTTP(rec, req)
	}()

	waitSignal(t, runStarted, "driver started")
	waitSignal(t, rec.wrote, "first frame written")
	cancel() // the client disconnect
	waitSignal(t, handlerDone, "handler returned")

	var got error
	select {
	case got = <-driverErr:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the driver to observe cancellation")
	}
	if !errors.Is(got, context.Canceled) {
		t.Fatalf("driver saw %v, want context.Canceled", got)
	}

	body := rec.String()
	if !strings.Contains(body, "event: text.delta") {
		t.Errorf("pre-disconnect frame missing from the wire:\n%s", body)
	}
	if strings.Contains(body, "RUN_ERROR") {
		t.Errorf("disconnect must not produce an error frame:\n%s", body)
	}
}

func waitSignal(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting: %s", what)
	}
}

func TestHandlerRejectsEmptyPrompt(t *testing.T) {
	t.Parallel()
	handler := sse.Handler(adaptor.New(&fakeDriver{response: driver.Response{Output: "ok"}}), sse.Options{Protocol: sse.Raw})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{}`))
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestHandlerRequestBodyLimit(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		protocol sse.Protocol
		prefix   string
	}{
		{name: "Raw", protocol: sse.Raw, prefix: `{"prompt":"hi"}`},
		{name: "AG-UI", protocol: sse.AGUI, prefix: `{"messages":[{"role":"user","content":"hi"}]}`},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			handler := sse.Handler(adaptor.New(&fakeDriver{response: driver.Response{Output: "ok"}}), sse.Options{Protocol: tc.protocol})

			t.Run("exactly at limit", func(t *testing.T) {
				body := tc.prefix + strings.Repeat(" ", sseRequestLimitForTest-len(tc.prefix))
				rec := httptest.NewRecorder()
				req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
				handler.ServeHTTP(rec, req)
				if rec.Code != http.StatusOK {
					t.Fatalf("status = %d, want 200; body=%q", rec.Code, rec.Body.String())
				}
			})

			t.Run("one byte over limit", func(t *testing.T) {
				body := tc.prefix + strings.Repeat(" ", sseRequestLimitForTest-len(tc.prefix)+1)
				rec := httptest.NewRecorder()
				req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
				handler.ServeHTTP(rec, req)
				if rec.Code != http.StatusRequestEntityTooLarge {
					t.Fatalf("status = %d, want 413; body=%q", rec.Code, rec.Body.String())
				}
				if got := strings.TrimSpace(rec.Body.String()); got != "request body exceeds 4 MiB limit" {
					t.Fatalf("body = %q", got)
				}
			})
		})
	}
}

func TestHandlerRejectsWrongMethod(t *testing.T) {
	t.Parallel()
	handler := sse.Handler(adaptor.New(&fakeDriver{response: driver.Response{Output: "ok"}}), sse.Options{Protocol: sse.AGUI})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405 (GET is Raw-only)", rec.Code)
	}
}
