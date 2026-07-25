package a2adelegation

import (
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMCPServerCurrentRunIDPrefersResolver(t *testing.T) {
	t.Parallel()
	// A session-scoped sidecar reuses one server across a session's runs and
	// repoints attribution via RunIDResolver; the static RunID is only the
	// fallback for classic per-run sidecars.
	current := "run-1"
	server := NewMCPServer(NewDelegator(nil, nil), MCPServerOptions{
		RunID:         "spawn-run",
		RunIDResolver: func() string { return current },
	})
	if got := server.currentRunID(); got != "run-1" {
		t.Fatalf("currentRunID() = %q, want the resolver's value run-1", got)
	}
	current = "run-2"
	if got := server.currentRunID(); got != "run-2" {
		t.Fatalf("currentRunID() = %q, want the updated resolver value run-2", got)
	}
	// An empty resolver result falls back to the static RunID.
	current = "   "
	if got := server.currentRunID(); got != "spawn-run" {
		t.Fatalf("currentRunID() = %q, want fallback to static RunID spawn-run", got)
	}
	// With no resolver at all, the static RunID is used (trimmed).
	plain := NewMCPServer(NewDelegator(nil, nil), MCPServerOptions{RunID: " run-9 "})
	if got := plain.currentRunID(); got != "run-9" {
		t.Fatalf("currentRunID() = %q, want static run-9", got)
	}
}

func TestMCPServerStreamsProgressWhenClientProvidesProgressToken(t *testing.T) {
	t.Parallel()
	server := NewMCPServer(NewDelegator(nil, nil), MCPServerOptions{RunID: "run-1", BearerToken: "token"})
	requestBody := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"delegate_to_agent","arguments":{"agent":"research","objective":"research this"},"_meta":{"progressToken":1}}}`

	request := httptest.NewRequest(http.MethodPost, "http://localhost", strings.NewReader(requestBody))
	request.Header.Set("Authorization", "Bearer token")
	request.Header.Set("Accept", "application/json, text/event-stream")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)

	if got := response.Header().Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("expected streamable MCP response, got %q", got)
	}
	body := response.Body.String()
	if !strings.Contains(body, `"method":"notifications/progress"`) {
		t.Fatalf("expected progress notification, body=%q", body)
	}
	if !strings.Contains(body, `"progressToken":1`) {
		t.Fatalf("expected progress token, body=%q", body)
	}
	if !strings.Contains(body, `"result"`) {
		t.Fatalf("expected final JSON-RPC result, body=%q", body)
	}
}

func TestMCPServerKeepsJSONResponseWithoutProgressToken(t *testing.T) {
	t.Parallel()
	server := NewMCPServer(NewDelegator(nil, nil), MCPServerOptions{RunID: "run-1", BearerToken: "token"})
	request := httptest.NewRequest(http.MethodPost, "http://localhost", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"delegate_to_agent","arguments":{"agent":"research","objective":"research this"}}}`))
	request.Header.Set("Authorization", "Bearer token")
	request.Header.Set("Accept", "application/json, text/event-stream")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)

	if got := response.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("expected JSON response without progress token, got %q", got)
	}
	var envelope map[string]any
	if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil {
		t.Fatalf("decode JSON response: %v", err)
	}
	if envelope["jsonrpc"] != "2.0" || envelope["result"] == nil {
		t.Fatalf("response = %#v", envelope)
	}
}

func TestMCPServerRejectsDisabledDefaultToolCall(t *testing.T) {
	t.Parallel()
	server := NewMCPServer(NewDelegator(nil, nil), MCPServerOptions{
		RunID:              "run-1",
		BearerToken:        "token",
		DisableDefaultTool: true,
	})
	request := httptest.NewRequest(http.MethodPost, "http://localhost", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"delegate_to_agent","arguments":{"agent":"research","objective":"research this"}}}`))
	request.Header.Set("Authorization", "Bearer token")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)

	var envelope struct {
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if envelope.Error == nil || envelope.Error.Code != -32602 || !strings.Contains(envelope.Error.Message, "unknown tool") {
		t.Fatalf("response = %#v", envelope)
	}
}

func TestMCPServerReturnsJSONRPCErrorForUnencodableToolSchema(t *testing.T) {
	t.Parallel()
	server := NewMCPServer(NewDelegator(nil, nil), MCPServerOptions{
		RunID:       "run-1",
		BearerToken: "token",
		Tools: []ToolSpec{{
			Name:        "broken_schema",
			InputSchema: map[string]any{"type": "number", "const": math.NaN()},
		}},
	})
	request := httptest.NewRequest(http.MethodPost, "http://localhost", strings.NewReader(`{"jsonrpc":"2.0","id":7,"method":"tools/list"}`))
	request.Header.Set("Authorization", "Bearer token")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)

	var envelope struct {
		ID    int `json:"id"`
		Error *struct {
			Code int `json:"code"`
		} `json:"error"`
	}
	if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if envelope.ID != 7 || envelope.Error == nil || envelope.Error.Code != -32603 {
		t.Fatalf("response = %#v", envelope)
	}
}

func TestNewMCPServerRejectsAmbiguousToolNames(t *testing.T) {
	tests := []MCPServerOptions{
		{Tools: []ToolSpec{{Name: ""}}},
		{Tools: []ToolSpec{{Name: "same"}, {Name: "same"}}},
		{Tools: []ToolSpec{{Name: DelegateToolName}}},
	}
	for _, opts := range tests {
		func() {
			defer func() {
				if recover() == nil {
					t.Fatalf("expected options %#v to panic", opts)
				}
			}()
			_ = NewMCPServer(NewDelegator(nil, nil), opts)
		}()
	}
}
