package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	adaptor "github.com/agent-dance/agent-adaptor/v2"
	"github.com/agent-dance/agent-adaptor/v2/bridges/agui"
	"github.com/agent-dance/agent-adaptor/v2/driver"
)

type chatDriver struct {
	started chan struct{}
	release chan struct{}
	mu      sync.Mutex
	calls   int
}

func (*chatDriver) Descriptor() driver.Descriptor {
	return driver.Descriptor{Type: "chat", Sessions: driver.SessionCapability{SupportsResume: true}, RunPolicyCaps: driver.RunPolicyCapabilities{Permission: driver.HumanDecisionSupport{Ask: true, AutoApprove: true, AutoReject: true}, Isolation: true}}
}
func (*chatDriver) ValidateConfig(any) error                  { return nil }
func (*chatDriver) SessionConfigFingerprint() (string, error) { return "chat", nil }
func (*chatDriver) SessionCodec() driver.SessionCodec         { return chatCodec{} }
func (d *chatDriver) Run(ctx context.Context, req driver.Request, _ driver.EventSink) (driver.Response, error) {
	d.mu.Lock()
	d.calls++
	call := d.calls
	d.mu.Unlock()
	if call == 1 {
		close(d.started)
		select {
		case <-d.release:
		case <-ctx.Done():
			return driver.Response{}, ctx.Err()
		}
	} else if !strings.Contains(req.Prompt, "supplement") {
		return driver.Response{}, fmt.Errorf("missing input: %s", req.Prompt)
	}
	return driver.Response{Output: "answer", Checkpoint: &driver.Checkpoint{Valid: true, State: &driver.SessionState{ResumeID: "chat"}}}, nil
}

type chatCodec struct{}

func (chatCodec) Name() string { return "chat" }
func (chatCodec) ToParams(state *driver.SessionState) driver.SessionParams {
	if state == nil {
		return driver.SessionParams{}
	}
	return driver.SessionParams{ResumeID: state.ResumeID}
}
func (chatCodec) FromParams(params driver.SessionParams) *driver.SessionState {
	if params.ResumeID == "" {
		return nil
	}
	return &driver.SessionState{ResumeID: params.ResumeID}
}
func (chatCodec) GuardFingerprint(params driver.SessionParams) string { return params.ResumeID }

func TestBackendRoutesInRunInputAndRecordsOutcomes(t *testing.T) {
	for _, endpoint := range []string{"/agent", "/message"} {
		t.Run(endpoint, func(t *testing.T) {
			testBackendRoutesInRunInputAndRecordsOutcomes(t, endpoint)
		})
	}
}

func testBackendRoutesInRunInputAndRecordsOutcomes(t *testing.T, endpoint string) {
	t.Setenv("AGUI_AGENT", "codex")
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	fake := &chatDriver{started: make(chan struct{}), release: make(chan struct{})}
	var release sync.Once
	defer release.Do(func() { close(fake.release) })
	agent := adaptor.New(fake)
	server, err := newAppServer(agent, "fake", "*")
	if err != nil {
		t.Fatal(err)
	}
	defer server.store.recorder.Close()
	handler := server.routes()
	request := func(id, text string) *http.Request {
		return httptest.NewRequest(http.MethodPost, "/agent", strings.NewReader(fmt.Sprintf(`{"threadId":"thread","runId":"request-%s","messages":[{"id":%q,"role":"user","content":%q}]}`, id, id, text))).WithContext(ctx)
	}
	initial := httptest.NewRecorder()
	done := make(chan struct{})
	go func() { defer close(done); handler.ServeHTTP(initial, request("initial", "start")) }()
	select {
	case <-fake.started:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	followup := httptest.NewRecorder()
	followupRequest := request("supplement-id", "supplement")
	if endpoint == "/message" {
		followupRequest = httptest.NewRequest(http.MethodPost, endpoint, strings.NewReader(`{"threadId":"thread","messageId":"supplement-id","text":"supplement"}`))
	}
	followupCtx, cancelFollowup := context.WithCancel(ctx)
	handler.ServeHTTP(followup, followupRequest.WithContext(followupCtx))
	// The companion request ends after admission, before the provider claims
	// the supplement. It must remain queued for the original execution.
	cancelFollowup()
	if followup.Code != http.StatusAccepted {
		t.Fatalf("HTTP %d: %s", followup.Code, followup.Body.String())
	}
	release.Do(func() { close(fake.release) })
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if !strings.Contains(initial.Body.String(), `"name":"message.update"`) || !strings.Contains(initial.Body.String(), `"name":"turn.end"`) {
		t.Fatal(initial.Body.String())
	}
	records := server.store.historyAfter("thread", 0)
	delivered := false
	userInputRecorded := false
	for _, record := range records {
		if e, ok := record.Event.(adaptor.MessageUpdate); ok && e.ID == "supplement-id" && e.State == adaptor.MessageDelivered {
			delivered = true
		}
		if e, ok := record.Event.(adaptor.TextDelta); ok && e.MessageID == "supplement-id" && e.Role == adaptor.RoleUser && e.Text == "supplement" {
			userInputRecorded = true
		}
	}
	if !delivered {
		t.Fatal("supplement delivery absent from recorder")
	}
	if !userInputRecorded {
		t.Fatal("supplement user text absent from recovery history")
	}
	if _, ok := records[len(records)-1].Event.(adaptor.Done); !ok {
		t.Fatalf("last=%T", records[len(records)-1].Event)
	}
	if server.runs.Active(agui.ThreadKey("thread")) != nil {
		t.Fatal("active run retained")
	}
	finished := httptest.NewRecorder()
	handler.ServeHTTP(finished, httptest.NewRequest(http.MethodPost, "/message", strings.NewReader(`{"threadId":"thread","messageId":"late","text":"late supplement"}`)))
	if finished.Code != http.StatusConflict {
		t.Fatalf("finished companion request HTTP %d: %s", finished.Code, finished.Body.String())
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.calls != 2 {
		t.Fatalf("provider turns=%d", fake.calls)
	}
}

func TestMessageEndpointRejectsInvalidOrInactiveInput(t *testing.T) {
	server := &appServer{}
	for _, tc := range []struct {
		name, method, body string
		want               int
	}{
		{name: "method", method: http.MethodGet, want: http.StatusMethodNotAllowed},
		{name: "malformed", body: `{`, want: http.StatusBadRequest},
		{name: "missing id", body: `{"threadId":"thread","text":"guidance"}`, want: http.StatusBadRequest},
		{name: "trailing object", body: `{"threadId":"thread","messageId":"m","text":"guidance"}{}`, want: http.StatusBadRequest},
		{name: "inactive", body: `{"threadId":"thread","messageId":"m","text":"guidance"}`, want: http.StatusConflict},
	} {
		t.Run(tc.name, func(t *testing.T) {
			method := tc.method
			if method == "" {
				method = http.MethodPost
			}
			response := httptest.NewRecorder()
			server.routes().ServeHTTP(response, httptest.NewRequest(method, "/message", strings.NewReader(tc.body)))
			if response.Code != tc.want {
				t.Fatalf("HTTP %d: %s; want %d", response.Code, response.Body.String(), tc.want)
			}
			if server.runs.Active(agui.ThreadKey("thread")) != nil {
				t.Fatal("companion endpoint started a new run")
			}
		})
	}
}

func TestChatEndpointsRejectRequestsCancelledBeforeDispatch(t *testing.T) {
	for _, endpoint := range []string{"/agent", "/message"} {
		t.Run(endpoint, func(t *testing.T) {
			// No Agent is available: a cancelled request must return before
			// it can dispatch a new execution or access an existing one.
			server := &appServer{}
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			request := httptest.NewRequest(http.MethodPost, endpoint, strings.NewReader("{}")).WithContext(ctx)
			response := httptest.NewRecorder()
			server.routes().ServeHTTP(response, request)
			if response.Code != http.StatusRequestTimeout {
				t.Fatalf("HTTP %d: %s; want cancellation rejection", response.Code, response.Body.String())
			}
		})
	}
}

func TestInitialHTTPConnectionCancellationStopsDetachedRun(t *testing.T) {
	t.Setenv("AGUI_AGENT", "codex")
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	fake := &chatDriver{started: make(chan struct{}), release: make(chan struct{})}
	agent := adaptor.New(fake)
	server, err := newAppServer(agent, "fake", "*")
	if err != nil {
		t.Fatal(err)
	}
	defer server.store.recorder.Close()
	requestCtx, disconnect := context.WithCancel(ctx)
	defer disconnect()
	request := httptest.NewRequest(http.MethodPost, "/agent", strings.NewReader(`{"threadId":"thread","messages":[{"id":"initial","role":"user","content":"start"}]}`)).WithContext(requestCtx)
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		server.routes().ServeHTTP(httptest.NewRecorder(), request)
	}()
	select {
	case <-fake.started:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	disconnect()
	select {
	case <-finished:
	case <-ctx.Done():
		t.Fatal("original SSE disconnect did not stop the new execution")
	}
	if server.runs.Active(agui.ThreadKey("thread")) != nil {
		t.Fatal("cancelled new execution retained an active registry entry")
	}
	records := server.store.historyAfter("thread", 0)
	if len(records) == 0 {
		t.Fatal("cancelled execution has no recovery history")
	}
	done, ok := records[len(records)-1].Event.(adaptor.Done)
	if !ok || !errors.Is(done.Err, context.Canceled) {
		t.Fatalf("last event = %#v, want cancelled Done", records[len(records)-1].Event)
	}
}
