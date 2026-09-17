package codebuddy

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/driver"
)

// alignmentLiveTerminalFailure reports only a fixed failed-field category. It
// never includes provider JSON, the expected session ID, or the observed ID.
// This test helper is deliberately untagged so ordinary CI exercises the same
// envelope/health checks used by the opt-in cold-resume fixture.
func alignmentLiveTerminalFailure(payload *driver.TerminalPayload, want string) string {
	if payload == nil {
		return "envelope_missing"
	}
	if payload.Event != "result" {
		return "envelope_event"
	}
	var terminal struct {
		SessionID string `json:"session_id"`
		Type      string `json:"type"`
		Subtype   string `json:"subtype"`
		IsError   *bool  `json:"is_error"`
	}
	if json.Unmarshal(payload.JSON, &terminal) != nil {
		return "json"
	}
	if terminal.Type != "result" {
		return "type"
	}
	if terminal.Subtype != "success" {
		return "subtype"
	}
	if want == "" || terminal.SessionID != want {
		return "session_match"
	}
	if terminal.IsError == nil || *terminal.IsError {
		return "is_error_explicit_false"
	}
	return ""
}

func TestAlignmentCodeBuddyLiveTerminalFields(t *testing.T) {
	const valid = `{"type":"result","subtype":"success","session_id":"private-session","is_error":false}`
	for _, tc := range []struct{ name, event, raw, want, failure string }{
		{"healthy", "result", valid, "private-session", ""},
		{"additive", "result", `{"type":"result","subtype":"success","session_id":"private-session","is_error":false,"extra":{"secret":"do-not-log"}}`, "private-session", ""},
		{"wrong_event", "unknown-secret", valid, "private-session", "envelope_event"},
		{"empty_json", "result", "", "private-session", "json"},
		{"malformed_json", "result", `{"secret":"do-not-log"`, "private-session", "json"},
		{"array", "result", `[` + valid + `]`, "private-session", "json"},
		{"null", "result", `null`, "private-session", "type"},
		{"envelope_as_json", "result", `{"Event":"result","JSON":` + valid + `}`, "private-session", "type"},
		{"nested_only", "result", `{"other":` + valid + `}`, "private-session", "type"},
		{"type_missing", "result", `{"subtype":"success","session_id":"private-session","is_error":false}`, "private-session", "type"},
		{"type_wrong", "result", `{"type":"error","subtype":"success","session_id":"private-session","is_error":false}`, "private-session", "type"},
		{"type_malformed", "result", `{"type":17,"subtype":"success","session_id":"private-session","is_error":false}`, "private-session", "json"},
		{"subtype_missing", "result", `{"type":"result","session_id":"private-session","is_error":false}`, "private-session", "subtype"},
		{"subtype_error", "result", `{"type":"result","subtype":"error","session_id":"private-session","is_error":false}`, "private-session", "subtype"},
		{"session_missing", "result", `{"type":"result","subtype":"success","is_error":false}`, "private-session", "session_match"},
		{"session_mismatch", "result", valid, "other-private-session", "session_match"},
		{"session_not_trimmed", "result", valid, " private-session ", "session_match"},
		{"empty_expected", "result", `{"type":"result","subtype":"success","session_id":"","is_error":false}`, "", "session_match"},
		{"error_missing", "result", `{"type":"result","subtype":"success","session_id":"private-session"}`, "private-session", "is_error_explicit_false"},
		{"error_null", "result", `{"type":"result","subtype":"success","session_id":"private-session","is_error":null}`, "private-session", "is_error_explicit_false"},
		{"error_true", "result", `{"type":"result","subtype":"success","session_id":"private-session","is_error":true}`, "private-session", "is_error_explicit_false"},
		{"error_string", "result", `{"type":"result","subtype":"success","session_id":"private-session","is_error":"false"}`, "private-session", "json"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := alignmentLiveTerminalFailure(&driver.TerminalPayload{Event: tc.event, JSON: json.RawMessage(tc.raw)}, tc.want)
			if got != tc.failure {
				t.Fatalf("safe failed-field category=%q want=%q", got, tc.failure)
			}
		})
	}
	t.Run("missing_envelope", func(t *testing.T) {
		if got := alignmentLiveTerminalFailure(nil, "private-session"); got != "envelope_missing" {
			t.Fatalf("safe failed-field category=%q", got)
		}
	})
}

type alignmentTerminalResultDriver struct{ configuredDriver }

func (d alignmentTerminalResultDriver) Run(context.Context, driver.Request, driver.EventSink) (driver.Response, error) {
	return driver.Response{RawStreams: &driver.RawStreams{Terminal: &driver.TerminalPayload{Event: "result", JSON: json.RawMessage(`{"type":"result","subtype":"success","session_id":"public-result-session","is_error":false}`)}}}, nil
}

func TestAlignmentCodeBuddyLiveTerminalPublicResult(t *testing.T) {
	root := t.TempDir()
	// The Driver returns a synthetic terminal through the real Agent pipeline;
	// no executable exists and no provider process or credential is used.
	d := alignmentTerminalResultDriver{Driver(Config{CommonConfig: CommonConfig{Command: filepath.Join(root, "no-provider"), Env: []driver.EnvBinding{{Name: "HOME", Value: root}, {Name: "USERPROFILE", Value: root}, {Name: "CODEBUDDY_CONFIG_DIR", Value: root}}}}).(configuredDriver)}
	a := adaptor.New(d)
	defer a.Close(context.Background())
	result, err := a.Run(context.Background(), "synthetic terminal")
	if err != nil {
		t.Fatal(err)
	}
	if failure := alignmentLiveTerminalFailure(result.Raw().Terminal, "public-result-session"); failure != "" {
		t.Fatalf("formal healthy terminal failed field=%s", failure)
	}
	// Explicit regression: the SDK envelope is not a provider protocol frame.
	envelope, err := json.Marshal(result.Raw().Terminal)
	if err != nil {
		t.Fatal("cannot encode synthetic envelope")
	}
	if failure := alignmentLiveTerminalFailure(&driver.TerminalPayload{Event: "result", JSON: envelope}, "public-result-session"); failure != "type" {
		t.Fatalf("envelope-as-provider failed field=%q", failure)
	}
}
