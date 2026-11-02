package agui

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	adaptor "github.com/agent-dance/agent-adaptor/v2"
)

type preparationRunner struct{}


func (preparationRunner) Send(context.Context, string, ...adaptor.CallOption) adaptor.Stream {
	panic("preparation must not execute")
}

func TestPreparationValidatesExecutionBeforeHostCallbacks(t *testing.T) {
	for _, body := range []string{
		`{"threadId":"t","messages":[]}`,
		`{"threadId":"t","messages":[{"id":"u","role":"user","content":"go"}],
		 "resume":[{"interruptId":"i","status":"resolved"}]}`,
		`{"threadId":"t","messages":[{"id":"a","role":"assistant","content":"old"}]}`,
	} {
		called := false
		_, err := PrepareHTTPRequest(httptest.NewRequest("POST", "/", strings.NewReader(body)),
			preparationRunner{}, RequestOptions{
				ResolveThreadKey: func(*http.Request, *RunAgentInput) (string, error) {
					called = true
					return "key", nil
				},
			})
		if err == nil || called {
			t.Fatalf("invalid execution reached host callback: called=%v err=%v", called, err)
		}
	}
}

func TestPreparationVerifierSeesResolvedLegacyBinding(t *testing.T) {
	seen := false
	result, err := PrepareHTTPRequest(httptest.NewRequest("POST", "/", strings.NewReader(
		`{"messages":[{"id":"u","role":"user","content":"go"}]}`)),
		preparationRunner{}, RequestOptions{
			ResolveThreadKey: func(*http.Request, *RunAgentInput) (string, error) { return "host-key", nil },
			VerifyHistory: func(_ context.Context, _ *RunAgentInput, _ adaptor.Runner, key string) (string, bool, error) {
				seen = key == "host-key"
				return key, false, nil
			},
		})
	if err != nil || !seen || result.ThreadKey != "host-key" {
		t.Fatalf("verifier skipped resolved binding: seen=%v request=%+v err=%v", seen, result, err)
	}
}

func TestPreparationHostErrorsPreserveStatusAndIdentity(t *testing.T) {
	internal := errors.New("store unavailable")
	denied := &InputError{Code: "host.forbidden", Message: "denied", Status: 403}
	for _, hostErr := range []error{internal, denied} {
		_, err := PrepareHTTPRequest(httptest.NewRequest("POST", "/", strings.NewReader(
			`{"threadId":"t","messages":[{"id":"u","role":"user","content":"go"}]}`)),
			preparationRunner{}, RequestOptions{
				OnAGUIRequest: func(*http.Request, *RunAgentInput) error { return hostErr },
			})
		var input *InputError
		if !errors.As(err, &input) || !errors.Is(err, hostErr) {
			t.Fatalf("lost host error chain: %v", err)
		}
		want := 500
		if hostErr == denied {
			want = 403
		}
		if input.Status != want {
			t.Fatalf("host failure status=%d, want %d", input.Status, want)
		}
	}
}
