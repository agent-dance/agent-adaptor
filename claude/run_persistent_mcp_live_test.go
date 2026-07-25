//go:build claude_live

package claude_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	agentadaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/claude"
	"github.com/agent-dance/agent-adaptor/memory"
)

// TestClaudePersistentReuseWithSharedMCP is the load-bearing proof for the
// session-scoped delegation change: a persistent Claude process may be REUSED
// across a session's turns while a Shared MCP runtime service is injected.
//
// The regression it guards against: before this change a reused long-lived
// process kept the first turn's now-dead MCP endpoint, so the second turn's
// tool call hung. Here the host keeps ONE stable MCP endpoint (fixed URL +
// bearer token) for the session and declares it RuntimeLifecycleShared, so:
//
//   - turn 1 spawns exactly one persistent process (persistent=true spawn),
//   - turn 2 REUSES it (zero spawns), and
//   - the MCP tool is invoked and returns on BOTH turns — proving the reused
//     process still reaches a live endpoint AND that its frozen env bearer
//     token is still accepted after reuse.
//
// Run with: go test -tags claude_live -run TestClaudePersistentReuseWithSharedMCP -v ./claude/
func TestClaudePersistentReuseWithSharedMCP(t *testing.T) {
	requireClaudeCLI(t)

	const bearerToken = "team-delegation-stable-token"
	mcp := newStubMCPServer(t, bearerToken)
	defer mcp.Close()

	rm := &stableSharedRuntimeManager{url: mcp.URL(), token: bearerToken}

	sdk := agentadaptor.New(
		agentadaptor.WithDefaultAgent(claude.New(agentadaptor.ClaudeConfig{
			CommonConfig:      agentadaptor.CommonConfig{CWD: t.TempDir()},
			Model:             envOr("CLAUDE_MODEL", "claude-haiku-4"),
			PersistentProcess: true,
		},
			agentadaptor.WithDefaultRunPolicy(agentadaptor.PolicyAutonomous),
			// Declaring the service Shared is what unlocks the persistent path
			// (persistentEligible gates injected runtimes by lifecycle).
			agentadaptor.WithDefaultRuntimeServices(agentadaptor.RuntimeServiceSpec{
				ID:        "stub-mcp",
				Name:      "stub-mcp",
				Lifecycle: agentadaptor.RuntimeLifecycleShared,
			}),
		)),
		agentadaptor.WithRuntimeServiceManager(rm),
		agentadaptor.WithSessionStore(memory.NewSessionStore()),
	)

	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()

	const prompt = "You have exactly one tool: echo_ping. You MUST call echo_ping once with " +
		"message set to the word PING. Do not answer in plain text; just call the tool."

	var sess *agentadaptor.SessionRef
	for turn := 1; turn <= 2; turn++ {
		opts := []agentadaptor.RunOption{}
		if sess == nil {
			opts = append(opts, agentadaptor.WithSessionKey("claude_live_shared_mcp", "v1"))
		} else {
			opts = append(opts, agentadaptor.WithSession(agentadaptor.SessionRequest{
				Namespace: sess.Namespace, Key: sess.Key, ID: sess.ID,
				Mode: agentadaptor.SessionContinueOnly,
			}))
		}

		callsBefore := mcp.calls()
		h, err := sdk.Start(ctx, prompt, opts...)
		if err != nil {
			t.Fatalf("turn %d Start: %v", turn, err)
		}

		var mu sync.Mutex
		spawns := 0
		done := make(chan struct{})
		go func() {
			defer close(done)
			for ev := range h.Events() {
				if ev.Type == agentadaptor.RunEventSpawn {
					if persistent, _ := ev.Data["persistent"].(bool); persistent {
						mu.Lock()
						spawns++
						mu.Unlock()
					}
				}
			}
		}()

		res, err := h.Wait(ctx)
		if err != nil {
			t.Fatalf("turn %d Wait: %v", turn, err)
		}
		<-done
		if res.Session == nil || res.Session.ID == "" {
			t.Fatalf("turn %d missing session ref", turn)
		}
		sess = res.Session

		mu.Lock()
		s := spawns
		mu.Unlock()
		turnCalls := mcp.calls() - callsBefore
		t.Logf("turn %d: spawns=%d mcp_tool_calls=%d output=%q", turn, s, turnCalls, res.Output)

		if res.Failure != nil {
			t.Fatalf("turn %d must not fail: %+v", turn, res.Failure)
		}
		if turn == 1 && s != 1 {
			t.Fatalf("turn 1 expected exactly 1 persistent spawn, got %d", s)
		}
		if turn == 2 && s != 0 {
			t.Fatalf("turn 2 expected 0 spawns (process reuse with Shared MCP), got %d", s)
		}
		if turnCalls == 0 {
			t.Fatalf("turn %d: MCP tool was never invoked; the injected Shared endpoint was not reachable", turn)
		}
	}

	// The host handed out one stable endpoint across both turns (session-scoped
	// stability), which is exactly what lets the reused process keep working.
	if got := rm.ensureCalls(); got != 2 {
		t.Fatalf("expected 2 Ensure calls (one per turn), got %d", got)
	}
	if rm.distinctURLs() != 1 {
		t.Fatalf("expected one stable endpoint URL across turns, got %d distinct", rm.distinctURLs())
	}
}

// stableSharedRuntimeManager models a host that keeps ONE Shared MCP endpoint
// for the session and returns the same URL + token on every run.
type stableSharedRuntimeManager struct {
	url   string
	token string

	mu      sync.Mutex
	ensures int
	urls    map[string]struct{}
}

func (m *stableSharedRuntimeManager) Ensure(_ context.Context, req agentadaptor.RuntimeServiceRequest) ([]agentadaptor.RuntimeServiceRef, error) {
	m.mu.Lock()
	m.ensures++
	if m.urls == nil {
		m.urls = map[string]struct{}{}
	}
	m.urls[m.url] = struct{}{}
	m.mu.Unlock()
	const tokenEnv = "STUB_MCP_TOKEN"
	return []agentadaptor.RuntimeServiceRef{{
		ID: "stub-mcp", Name: "stub-mcp", URL: m.url,
		Status: agentadaptor.RuntimeServiceRunning, Lifecycle: agentadaptor.RuntimeLifecycleShared,
		ReuseKey: "stub-mcp", OwnerAgentID: req.Agent.ID, Health: agentadaptor.RuntimeHealthHealthy,
		Metadata: map[string]string{
			"agentadaptor.mcp.enabled":              "true",
			"agentadaptor.mcp.key":                  "stub-mcp",
			"agentadaptor.mcp.transport":            string(agentadaptor.MCPTransportHTTP),
			"agentadaptor.mcp.url":                  m.url,
			"agentadaptor.mcp.bearer_token_env_var": tokenEnv,
		},
		SecretEnv: []agentadaptor.EnvBinding{{Name: tokenEnv, Value: m.token}},
	}}, nil
}

func (m *stableSharedRuntimeManager) ReleaseByRun(context.Context, string) error { return nil }
func (m *stableSharedRuntimeManager) ReleaseByLabels(context.Context, map[string]string) error {
	return nil
}

func (m *stableSharedRuntimeManager) ensureCalls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.ensures
}

func (m *stableSharedRuntimeManager) distinctURLs() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.urls)
}

// stubMCPServer is a minimal HTTP MCP endpoint exposing one instant echo tool.
// It validates the bearer token so a successful tool call on the reused turn 2
// also proves the process's frozen env token is still accepted.
type stubMCPServer struct {
	srv   *httptest.Server
	token string

	mu        sync.Mutex
	callCount int
}

func newStubMCPServer(t *testing.T, token string) *stubMCPServer {
	t.Helper()
	s := &stubMCPServer{token: token}
	s.srv = httptest.NewServer(http.HandlerFunc(s.serve))
	return s
}

func (s *stubMCPServer) URL() string { return s.srv.URL }
func (s *stubMCPServer) Close()      { s.srv.Close() }
func (s *stubMCPServer) calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.callCount
}

func (s *stubMCPServer) serve(w http.ResponseWriter, r *http.Request) {
	if got := strings.TrimSpace(r.Header.Get("Authorization")); got != "Bearer "+s.token {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var req struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Method  string          `json:"method"`
		Params  json.RawMessage `json:"params"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	// Notifications carry no id and expect no body.
	if len(req.ID) == 0 && strings.HasPrefix(req.Method, "notifications/") {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	reply := func(result any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
	}
	switch req.Method {
	case "initialize":
		reply(map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "stub-mcp", "version": "0.1.0"},
		})
	case "ping":
		reply(map[string]any{})
	case "tools/list":
		reply(map[string]any{"tools": []map[string]any{{
			"name":        "echo_ping",
			"description": "Echo the provided message back as PONG.",
			"inputSchema": map[string]any{
				"type":                 "object",
				"properties":           map[string]any{"message": map[string]any{"type": "string"}},
				"required":             []string{"message"},
				"additionalProperties": false,
			},
		}}})
	case "tools/call":
		s.mu.Lock()
		s.callCount++
		s.mu.Unlock()
		var params struct {
			Name      string `json:"name"`
			Arguments struct {
				Message string `json:"message"`
			} `json:"arguments"`
		}
		_ = json.Unmarshal(req.Params, &params)
		reply(map[string]any{
			"content": []map[string]any{{"type": "text", "text": "PONG:" + params.Arguments.Message}},
			"isError": false,
		})
	default:
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"jsonrpc": "2.0", "id": req.ID,
			"error": map[string]any{"code": -32601, "message": "method not found"},
		})
	}
}
