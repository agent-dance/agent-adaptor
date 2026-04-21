package agui_test

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/agent-dance/agent-adaptor/pkg/bridges/agui"
)

func TestDecodeHTTPRequestHappyPath(t *testing.T) {
	t.Parallel()
	body := `{
		"threadId": "t-1",
		"runId": "r-1",
		"messages": [{"id":"m","role":"user","content":"hello"}]
	}`
	req := httptest.NewRequest(http.MethodPost, "/agent", strings.NewReader(body))
	in, err := agui.DecodeHTTPRequest(req)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if in.ThreadID != "t-1" {
		t.Fatalf("ThreadID: %q", in.ThreadID)
	}
	if len(in.Messages) != 1 || in.Messages[0].Role != "user" {
		t.Fatalf("messages: %+v", in.Messages)
	}
}

func TestDecodeHTTPRequestEmptyBody(t *testing.T) {
	t.Parallel()
	req := httptest.NewRequest(http.MethodPost, "/agent", bytes.NewReader(nil))
	if _, err := agui.DecodeHTTPRequest(req); err == nil {
		t.Fatal("want error on empty body")
	}
}

func TestDecodeHTTPRequestInvalidJSON(t *testing.T) {
	t.Parallel()
	req := httptest.NewRequest(http.MethodPost, "/agent", strings.NewReader(`{not json`))
	if _, err := agui.DecodeHTTPRequest(req); err == nil {
		t.Fatal("want error on invalid json")
	}
}

// TestLastUserText covers the two AG-UI content shapes plus edge cases.
func TestLastUserText(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		body string
		want string
	}{
		{
			name: "plain string content",
			body: `{"messages":[{"role":"user","content":"hi"}]}`,
			want: "hi",
		},
		{
			name: "parts array concatenated with newlines",
			body: `{"messages":[{"role":"user","content":[{"type":"text","text":"line1"},{"type":"text","text":"line2"}]}]}`,
			want: "line1\nline2",
		},
		{
			name: "parts array skips non-text parts",
			body: `{"messages":[{"role":"user","content":[{"type":"text","text":"a"},{"type":"image","url":"data:..."},{"type":"text","text":"b"}]}]}`,
			want: "a\nb",
		},
		{
			name: "prefers latest user message, ignores assistant reply after user",
			body: `{"messages":[
				{"role":"user","content":"first"},
				{"role":"assistant","content":"reply"},
				{"role":"user","content":"latest"}
			]}`,
			want: "latest",
		},
		{
			name: "skips non-user roles",
			body: `{"messages":[{"role":"assistant","content":"only assistant"}]}`,
			want: "",
		},
		{
			name: "empty messages",
			body: `{"messages":[]}`,
			want: "",
		},
		{
			name: "falls back past empty user content to older populated user msg",
			body: `{"messages":[
				{"role":"user","content":"older"},
				{"role":"user","content":""}
			]}`,
			want: "older",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			req := httptest.NewRequest(http.MethodPost, "/agent", strings.NewReader(tc.body))
			in, err := agui.DecodeHTTPRequest(req)
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			if got := in.LastUserText(); got != tc.want {
				t.Fatalf("LastUserText: want %q got %q", tc.want, got)
			}
		})
	}
}

func TestSessionKey(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name          string
		threadID      string
		wantNamespace string
		wantKey       string
	}{
		{name: "populated threadId", threadID: "thread-123", wantNamespace: "agui", wantKey: "thread-123"},
		{name: "empty threadId", threadID: "", wantNamespace: "", wantKey: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			in := &agui.RunAgentInput{ThreadID: tc.threadID}
			ns, key := in.SessionKey()
			if ns != tc.wantNamespace || key != tc.wantKey {
				t.Fatalf("SessionKey: want (%q, %q) got (%q, %q)",
					tc.wantNamespace, tc.wantKey, ns, key)
			}
		})
	}
}

func TestRunAgentInputPreservesExtensionFields(t *testing.T) {
	t.Parallel()
	// Ensure State / Tools / Context / ForwardedProps pass through.
	body := `{
		"threadId": "t",
		"runId": "r",
		"messages": [{"role":"user","content":"hi"}],
		"state": {"x": 1},
		"tools": [{"name":"lookup"}],
		"context": [{"role":"system","content":"be terse"}],
		"forwardedProps": {"ab":"cd"}
	}`
	req := httptest.NewRequest(http.MethodPost, "/agent", strings.NewReader(body))
	in, err := agui.DecodeHTTPRequest(req)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(in.State) == 0 || len(in.Tools) != 1 || len(in.Context) != 1 || len(in.ForwardedProps) == 0 {
		t.Fatalf("extension fields lost: %+v", in)
	}
}
