package cursor

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
)

func TestCursorRelayArgumentDispatch(t *testing.T) {
	for _, args := range [][]string{{"host"}, {"host", "run"}, {"host", "run", hookRelayArg}} {
		var out bytes.Buffer
		if runHookRelay(args, strings.NewReader("{}"), &out, func(string) string { return "" }) || out.Len() != 0 {
			t.Fatalf("ordinary args handled: %v %q", args, out.String())
		}
	}
	for _, args := range [][]string{{"host", hookRelayArg}, {"host", hookRelayArg, "unexpected"}, {"host", hookRelayArg, hookProbeArg, "extra"}} {
		var out bytes.Buffer
		if !runHookRelay(args, strings.NewReader("invalid"), &out, func(string) string { return "" }) || strings.TrimSpace(out.String()) != "{}" {
			t.Fatalf("relay failure: %v %q", args, out.String())
		}
	}
	var out bytes.Buffer
	if !runHookRelay([]string{"host", hookRelayArg, hookProbeArg}, nil, &out, nil) || strings.TrimSpace(out.String()) != hookProbeReply {
		t.Fatalf("probe: %q", out.String())
	}
	// Exercise the exported entry point's exit, so application code never runs.
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, hookRelayArg)
	cmd.Env = append(os.Environ(), hookURLEnv+"=", hookTokenEnv+"=")
	data, err := cmd.Output()
	if err != nil || strings.TrimSpace(string(data)) != "{}" {
		t.Fatalf("relay process: %s %v", data, err)
	}
}

type failingHookReader struct{}

func (failingHookReader) Read([]byte) (int, error) { return 0, errors.New("read failed") }

func TestCursorRelayFailuresAlwaysReturnJSONObject(t *testing.T) {
	for _, tc := range []struct {
		name, endpoint, token, input, reply string
		status                              int
		reader                              io.Reader
	}{
		{name: "missing environment", input: `{}`},
		{name: "non-loopback", endpoint: "http://192.0.2.1/hook", token: "secret", input: `{}`},
		{name: "https", endpoint: "https://127.0.0.1/hook", token: "secret", input: `{}`},
		{name: "hostname", endpoint: "http://localhost/hook", token: "secret", input: `{}`},
		{name: "url credentials", endpoint: "http://user@127.0.0.1/hook", token: "secret", input: `{}`},
		{name: "unreachable", endpoint: "http://127.0.0.1:0/hook", token: "secret", input: `{}`},
		{name: "invalid input", token: "secret", input: `{`},
		{name: "null input", token: "secret", input: `null`},
		{name: "extra input", token: "secret", input: `{} {}`},
		{name: "read error", token: "secret", reader: failingHookReader{}},
		{name: "request limit", token: "secret", input: `{"tool_use_id":"` + strings.Repeat("x", hookRequestLimit) + `"}`},
		{name: "stdin limit", token: "secret", reader: io.LimitReader(repeatingHookReader(' '), hookStdinLimit+1)},
		{name: "http error", token: "secret", input: `{}`, reply: `{}`, status: 401},
		{name: "malformed reply", token: "secret", input: `{}`, reply: `{`},
		{name: "non-object reply", token: "secret", input: `{}`, reply: `[]`},
		{name: "null reply", token: "secret", input: `{}`, reply: `null`},
		{name: "extra reply", token: "secret", input: `{}`, reply: `{} {}`},
		{name: "bad context", token: "secret", input: `{}`, reply: `{"additional_context":12}`},
		{name: "large reply", token: "secret", input: `{}`, reply: `{"ignored":"` + strings.Repeat("x", hookResponseLimit) + `"}`},
		{name: "large context", token: "secret", input: `{}`, reply: `{"additional_context":"` + strings.Repeat("x", hookContextLimit+1) + `"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			transport := hookHandlerTransport(func(w http.ResponseWriter, r *http.Request) {
				status := tc.status
				if status == 0 {
					status = 200
				}
				w.WriteHeader(status)
				_, _ = io.WriteString(w, tc.reply)
			})
			endpoint := tc.endpoint
			if endpoint == "" && tc.name != "missing environment" {
				endpoint = "http://127.0.0.1/hook"
			}
			getenv := func(k string) string {
				if k == hookURLEnv {
					return endpoint
				}
				if k == hookTokenEnv {
					return tc.token
				}
				return ""
			}
			reader := tc.reader
			if reader == nil {
				reader = strings.NewReader(tc.input)
			}
			out := relayHookWithTransport(reader, getenv, transport)
			if !json.Valid([]byte(out)) || out != "{}" {
				t.Fatalf("failure output: %q", out)
			}
		})
	}
}

type repeatingHookReader byte

func (r repeatingHookReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = byte(r)
	}
	return len(p), nil
}

func TestCursorRelayForwardsOnlyOwnershipAndToken(t *testing.T) {
	transport := hookHandlerTransport(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.Header.Get("Authorization") != "Bearer secret" {
			t.Errorf("request authentication: %s %s", r.Method, r.Header.Get("Authorization"))
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		if len(payload) != 4 || payload["hook_event_name"] != "postToolUseFailure" || payload["conversation_id"] != "conversation" || payload["tool_use_id"] != "call" || payload["parent_tool_call_id"] != "parent" {
			t.Errorf("forwarded payload: %+v", payload)
		}
		_, _ = io.WriteString(w, `{"additional_context":"guidance 😀","decision":"block"}`)
	})
	out := relayHookWithTransport(strings.NewReader(`{"hook_event_name":"postToolUseFailure","conversation_id":"conversation","tool_use_id":"call","parent_tool_call_id":"parent","tool_input":{"secret":true},"tool_output":"sensitive"}`), func(k string) string {
		if k == hookURLEnv {
			return "http://127.0.0.1/hook"
		}
		return "secret"
	}, transport)
	var reply map[string]string
	if json.Unmarshal([]byte(out), &reply) != nil || len(reply) != 1 || reply["additional_context"] != "guidance 😀" {
		t.Fatalf("reply: %q", out)
	}
}

func TestCursorRelayDoesNotFollowRedirects(t *testing.T) {
	transport := hookHandlerTransport(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/hook" {
			t.Error("redirect forwarded token")
		}
		http.Redirect(w, r, "http://127.0.0.1/target", http.StatusTemporaryRedirect)
	})
	out := relayHookWithTransport(strings.NewReader(`{}`), func(k string) string {
		if k == hookURLEnv {
			return "http://127.0.0.1/hook"
		}
		return "secret"
	}, transport)
	if out != "{}" {
		t.Fatalf("redirect output: %s", out)
	}
}

func TestCursorHookShellCommandQuoting(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell quoting")
	}
	argv := []string{"/path with spaces/host", "quote'arg", "$(secret)", "line\nbreak"}
	// printf is the only executable; hostile-looking argv remains literal data.
	cmd := exec.Command("/bin/sh", "-c", hookShellCommand(append([]string{"printf", "%s\\n"}, argv...)))
	out, err := cmd.Output()
	if err != nil || string(out) != strings.Join(argv, "\n")+"\n" {
		t.Fatalf("quoting: %q %v", out, err)
	}
}

type hookHandlerTransport http.HandlerFunc

func (h hookHandlerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	w := httptest.NewRecorder()
	h(w, r)
	return w.Result(), nil
}
