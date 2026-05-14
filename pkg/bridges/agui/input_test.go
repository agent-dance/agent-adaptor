package agui_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	agentadaptor "github.com/agent-dance/agent-adaptor"
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

// TestUserTurnPayloadsHappy proves UserTurnPayloads returns a
// well-formed text.start / text.content / text.end triple tagged with
// RoleUser, sharing a stable MessageID lifted from the AG-UI message.
func TestUserTurnPayloadsHappy(t *testing.T) {
	t.Parallel()
	in := &agui.RunAgentInput{
		ThreadID: "thr-1",
		Messages: []agui.Message{
			{ID: "msg-a", Role: "assistant", Content: rawString("greetings")},
			{ID: "msg-u", Role: "user", Content: rawString("hello there")},
		},
	}
	got := in.UserTurnPayloads("run-9")
	if len(got) != 3 {
		t.Fatalf("want 3 payloads, got %d (%+v)", len(got), got)
	}
	wantKinds := []agentadaptor.StreamKind{
		agentadaptor.StreamTextStart,
		agentadaptor.StreamTextContent,
		agentadaptor.StreamTextEnd,
	}
	for i, p := range got {
		if p.Kind != wantKinds[i] {
			t.Fatalf("payload %d: want kind %q got %q", i, wantKinds[i], p.Kind)
		}
		if p.Role != agentadaptor.RoleUser {
			t.Fatalf("payload %d: want Role=RoleUser, got %q", i, p.Role)
		}
		if p.MessageID != "msg-u" {
			t.Fatalf("payload %d: want MessageID=msg-u, got %q", i, p.MessageID)
		}
		if p.RunID != "run-9" {
			t.Fatalf("payload %d: want RunID=run-9, got %q", i, p.RunID)
		}
		if p.ThreadID != "thr-1" {
			t.Fatalf("payload %d: want ThreadID=thr-1, got %q", i, p.ThreadID)
		}
		if p.Timestamp.IsZero() {
			t.Fatalf("payload %d: timestamp must be set", i)
		}
	}
	if got[1].Delta != "hello there" {
		t.Fatalf("content delta: want %q got %q", "hello there", got[1].Delta)
	}
	if got[0].Delta != "" || got[2].Delta != "" {
		t.Fatalf("start/end must have empty Delta")
	}
}

// TestUserTurnPayloadsSynthesizesMessageID covers the case where the
// AG-UI Message has no ID — the helper must mint a stable identifier so
// the triple still shares a single MessageID.
func TestUserTurnPayloadsSynthesizesMessageID(t *testing.T) {
	t.Parallel()
	in := &agui.RunAgentInput{
		ThreadID: "thr",
		Messages: []agui.Message{
			{Role: "user", Content: rawString("hi")},
		},
	}
	got := in.UserTurnPayloads("run-x")
	if len(got) != 3 {
		t.Fatalf("want 3 payloads, got %d", len(got))
	}
	id := got[0].MessageID
	if id == "" {
		t.Fatal("MessageID must be synthesized")
	}
	if got[1].MessageID != id || got[2].MessageID != id {
		t.Fatalf("triple must share MessageID, got %q / %q / %q",
			got[0].MessageID, got[1].MessageID, got[2].MessageID)
	}
	if !strings.Contains(id, "run-x") {
		t.Fatalf("synthesized MessageID should include runID, got %q", id)
	}
}

// TestUserTurnPayloadsReturnsNilOnNoUser covers two empty cases:
// (a) no user-role message and (b) user message with empty content.
// Both must return nil so the caller can simply range over the result.
func TestUserTurnPayloadsReturnsNilOnNoUser(t *testing.T) {
	t.Parallel()

	noUser := &agui.RunAgentInput{
		Messages: []agui.Message{
			{Role: "assistant", Content: rawString("hello")},
		},
	}
	if got := noUser.UserTurnPayloads("r"); got != nil {
		t.Fatalf("expected nil when no user message, got %+v", got)
	}

	emptyUser := &agui.RunAgentInput{
		Messages: []agui.Message{
			{Role: "user", Content: rawString("")},
		},
	}
	if got := emptyUser.UserTurnPayloads("r"); got != nil {
		t.Fatalf("expected nil when user content empty, got %+v", got)
	}

	var nilIn *agui.RunAgentInput
	if got := nilIn.UserTurnPayloads("r"); got != nil {
		t.Fatalf("expected nil on nil receiver, got %+v", got)
	}
}

// TestUserTurnPayloadsPicksLatestUser confirms the helper picks the most
// recent user message, matching LastUserText.
func TestUserTurnPayloadsPicksLatestUser(t *testing.T) {
	t.Parallel()
	in := &agui.RunAgentInput{
		Messages: []agui.Message{
			{ID: "u1", Role: "user", Content: rawString("first")},
			{ID: "a1", Role: "assistant", Content: rawString("reply")},
			{ID: "u2", Role: "user", Content: rawString("latest")},
		},
	}
	got := in.UserTurnPayloads("r")
	if len(got) != 3 {
		t.Fatalf("want 3 payloads, got %d", len(got))
	}
	if got[0].MessageID != "u2" {
		t.Fatalf("want MessageID=u2, got %q", got[0].MessageID)
	}
	if got[1].Delta != "latest" {
		t.Fatalf("want delta=latest, got %q", got[1].Delta)
	}
}

func rawString(s string) json.RawMessage {
	if s == "" {
		return json.RawMessage(`""`)
	}
	return json.RawMessage(`"` + s + `"`)
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
