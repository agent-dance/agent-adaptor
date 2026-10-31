package cursor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"time"
)

const (
	hookRelayArg      = "__agent_adaptor_cursor_hook"
	hookProbeArg      = "--agent-adaptor-hook-probe"
	hookProbeReply    = `{"agent_adaptor_cursor_hook":"v1"}`
	hookURLEnv        = "AGENT_ADAPTOR_CURSOR_HOOK_URL"
	hookTokenEnv      = "AGENT_ADAPTOR_CURSOR_HOOK_TOKEN"
	hookTimeout       = 5 * time.Second
	hookStdinLimit    = 64 << 20
	hookRequestLimit  = 64 << 10
	hookResponseLimit = 256 << 10
)

// RunHookRelayIfRequested handles the SDK's hidden Cursor hook command and
// exits with status zero after printing a JSON object ({} on failure). Otherwise
// it returns immediately. Call it first in the host's main, before argument
// parsing, and avoid expensive init functions: every tool hook starts a process.
// Config.HookRelay may instead select a separate binary that calls this function.
func RunHookRelayIfRequested() {
	if runHookRelay(os.Args, os.Stdin, os.Stdout, os.Getenv) {
		os.Exit(0)
	}
}

// Keeping argument dispatch separate permits testing without terminating the host.
func runHookRelay(args []string, in io.Reader, out io.Writer, getenv func(string) string) bool {
	if len(args) < 2 || args[1] != hookRelayArg {
		return false
	}
	if len(args) == 3 && args[2] == hookProbeArg {
		_, _ = io.WriteString(out, hookProbeReply+"\n")
		return true
	}
	reply := "{}"
	if len(args) == 2 {
		reply = relayHook(in, getenv)
	}
	_, _ = io.WriteString(out, reply+"\n")
	return true
}

// hookRequest deliberately omits tool inputs and outputs. Only ownership fields
// cross the loopback transport, even when stdin contains a very large tool result.
type hookRequest struct {
	Event            string `json:"hook_event_name"`
	ConversationID   string `json:"conversation_id"`
	ToolUseID        string `json:"tool_use_id,omitempty"`
	ParentToolCallID string `json:"parent_tool_call_id,omitempty"`
}

func readHookJSON(r io.Reader, limit int64, dst any) error {
	data, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return err
	}
	if int64(len(data)) > limit {
		return errors.New("Cursor hook JSON exceeds size limit")
	}
	return json.Unmarshal(data, dst)
}

func relayHook(in io.Reader, getenv func(string) string) string {
	transport := &http.Transport{Proxy: nil, MaxResponseHeaderBytes: hookRequestLimit}
	defer transport.CloseIdleConnections()
	return relayHookWithTransport(in, getenv, transport)
}

func relayHookWithTransport(in io.Reader, getenv func(string) string, transport http.RoundTripper) string {
	endpoint, token := getenv(hookURLEnv), getenv(hookTokenEnv)
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "http" || u.User != nil || u.Fragment != "" || token == "" {
		return "{}"
	}
	ip := net.ParseIP(u.Hostname())
	if ip == nil || !ip.IsLoopback() {
		return "{}"
	}
	var input *hookRequest
	if readHookJSON(in, hookStdinLimit, &input) != nil || input == nil {
		return "{}"
	}
	data, err := json.Marshal(input)
	if err != nil || len(data) > hookRequestLimit {
		return "{}"
	}
	ctx, cancel := context.WithTimeout(context.Background(), hookTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(data))
	if err != nil {
		return "{}"
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{
		Transport: transport,
		// A redirect must never forward the bearer token away from this endpoint.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	resp, err := client.Do(req)
	if err != nil {
		return "{}"
	}
	defer resp.Body.Close()
	var output map[string]json.RawMessage
	if resp.StatusCode != http.StatusOK || readHookJSON(resp.Body, hookResponseLimit, &output) != nil || output == nil {
		return "{}"
	}
	// The relay can only return additional_context, never permission decisions
	// or other hook controls, even if a misconfigured endpoint returns them.
	var text string
	if raw, ok := output["additional_context"]; !ok || json.Unmarshal(raw, &text) != nil || utf16Units(text) > hookContextLimit {
		return "{}"
	}
	data, err = json.Marshal(map[string]string{"additional_context": text})
	if err != nil {
		return "{}"
	}
	return string(data)
}
