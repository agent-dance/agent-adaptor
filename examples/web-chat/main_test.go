package main

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	adaptor "github.com/agent-dance/agent-adaptor"
)

func TestWebChatNetworkDefaultsAreLoopbackAndSameOrigin(t *testing.T) {
	t.Setenv("ADDR", "")
	t.Setenv("CORS_ORIGIN", "")
	if got := envOverride("ADDR", defaultListenAddr); got != "127.0.0.1:8080" {
		t.Fatalf("default listen address = %q, want loopback", got)
	}
	if got := envOverride("CORS_ORIGIN", defaultCORSOrigin); got != "" {
		t.Fatalf("default CORS origin = %q, want disabled for same-origin UI", got)
	}
}

func TestWebChatNetworkDefaultsCanBeExplicitlyOverridden(t *testing.T) {
	t.Setenv("ADDR", "0.0.0.0:9080")
	t.Setenv("CORS_ORIGIN", "https://chat.example.com")
	if got := envOverride("ADDR", defaultListenAddr); got != "0.0.0.0:9080" {
		t.Fatalf("listen override = %q", got)
	}
	if got := envOverride("CORS_ORIGIN", defaultCORSOrigin); got != "https://chat.example.com" {
		t.Fatalf("CORS override = %q", got)
	}
}

func TestWebHandlerRehydratesSecondTurnWithoutCheckpoint(t *testing.T) {
	runner := &historyRunner{}
	handler := newChatHandler(runner, "")
	for _, body := range []string{
		`{"messages":[{"id":"u1","role":"user","content":"first"}]}`,
		`{"messages":[{"id":"u1","role":"user","content":"first"},{"id":"a1","role":"assistant","content":"answer"},{"id":"u2","role":"user","content":"second"}]}`,
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest("POST", "/v1/chat", strings.NewReader(body)))
		if response.Code != 200 {
			t.Fatalf("status=%d body=%s", response.Code, response.Body)
		}
	}
	if len(runner.prompts) != 2 || !strings.Contains(runner.prompts[1], "<agent-adaptor-history>") ||
		!strings.Contains(runner.prompts[1], `"text":"first"`) || !strings.HasSuffix(runner.prompts[1], "second") {
		t.Fatalf("second turn lost history: %v", runner.prompts)
	}
}

type historyRunner struct{ prompts []string }

func (r *historyRunner) Run(context.Context, string, ...adaptor.CallOption) (*adaptor.Result, error) {
	panic("handler must use Stream")
}

func (r *historyRunner) Stream(_ context.Context, prompt string, _ ...adaptor.CallOption) adaptor.Stream {
	r.prompts = append(r.prompts, prompt)
	events := make(chan adaptor.Event)
	close(events)
	return historyStream{events: events}
}

type historyStream struct{ events <-chan adaptor.Event }

func (s historyStream) Events() <-chan adaptor.Event { return s.events }

func (historyStream) Result() (*adaptor.Result, error) { return &adaptor.Result{Text: "answer"}, nil }

func (historyStream) RunID() string { return "example-run" }

func (historyStream) Cancel() {}
