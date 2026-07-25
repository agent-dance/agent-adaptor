package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	agentadaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/pkg/bridges/sse"
	"github.com/agent-dance/agent-adaptor/pkg/hosttools/a2adelegation"
)

const delegationTokenEnv = "AGENT_ADAPTOR_TEAM_DELEGATION_TOKEN"

type delegationRuntimeManager struct {
	mu       sync.Mutex
	registry *a2adelegation.Registry
	bus      *a2adelegation.EventBus
	servers  map[string]*delegationSidecar
	results  map[string]map[string]a2adelegation.DelegationResult
}

// delegationSidecar is one MCP delegation endpoint. In web (session) mode it is
// keyed by the AG-UI session and reused across every turn of that thread, so
// its URL + bearer token stay stable and a persistent Claude leader process can
// keep talking to a live endpoint. In CLI (stateless) mode it is keyed by the
// single run and torn down when that run releases, exactly as before.
type delegationSidecar struct {
	scopeKey      string
	sessionScoped bool
	url           string
	token         string
	metadata      map[string]string
	server        *http.Server
	listener      net.Listener
	serveErr      chan error

	runMu    sync.Mutex
	curRunID string // the host's current run; drives event attribution
}

// setCurrentRun repoints event attribution at the run now driving the sidecar.
// A reused session sidecar serves many runs; each turn updates this so the MCP
// server's RunIDResolver tags progress/results with the live run.
func (s *delegationSidecar) setCurrentRun(runID string) {
	s.runMu.Lock()
	s.curRunID = runID
	s.runMu.Unlock()
}

func (s *delegationSidecar) runID() string {
	s.runMu.Lock()
	defer s.runMu.Unlock()
	return s.curRunID
}

func (s *delegationSidecar) serviceRef(agent agentadaptor.AgentIdentity) agentadaptor.RuntimeServiceRef {
	lifecycle := agentadaptor.RuntimeLifecycleEphemeral
	reuseKey := ""
	if s.sessionScoped {
		// Shared tells the SDK the endpoint outlives one run, which lets the
		// Claude adapter keep a persistent process across the session's turns.
		lifecycle = agentadaptor.RuntimeLifecycleShared
		reuseKey = s.scopeKey
	}
	return agentadaptor.RuntimeServiceRef{
		ID: s.scopeKey + ":team-delegation", Name: "team-delegation", URL: s.url,
		Status: agentadaptor.RuntimeServiceRunning, Lifecycle: lifecycle, ReuseKey: reuseKey,
		OwnerAgentID: agent.ID, Health: agentadaptor.RuntimeHealthHealthy, Metadata: cloneLabels(s.metadata),
		SecretEnv: []agentadaptor.EnvBinding{{Name: delegationTokenEnv, Value: s.token}},
	}
}

func newDelegationRuntimeManager(registry *a2adelegation.Registry, bus *a2adelegation.EventBus) *delegationRuntimeManager {
	return &delegationRuntimeManager{
		registry: registry,
		bus:      bus,
		servers:  map[string]*delegationSidecar{},
		results:  map[string]map[string]a2adelegation.DelegationResult{},
	}
}

// delegationScopeKey resolves the reuse scope for a run. When the SSE bridge
// surfaces an AG-UI session (web mode) the sidecar is scoped to that session
// and reused across its turns; otherwise (CLI mode) it is scoped to the run.
func delegationScopeKey(req agentadaptor.RuntimeServiceRequest) (key string, sessionScoped bool) {
	ns := strings.TrimSpace(req.Metadata[sse.MetadataSessionNamespace])
	sessionKey := strings.TrimSpace(req.Metadata[sse.MetadataSessionKey])
	if ns != "" || sessionKey != "" {
		return ns + "/" + sessionKey, true
	}
	return req.RunID, false
}

func (m *delegationRuntimeManager) Ensure(ctx context.Context, req agentadaptor.RuntimeServiceRequest) ([]agentadaptor.RuntimeServiceRef, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if req.RunID == "" {
		return nil, errors.New("delegation runtime requires a run ID")
	}
	scopeKey, sessionScoped := delegationScopeKey(req)

	m.mu.Lock()
	defer m.mu.Unlock()

	// Reuse an existing sidecar for this scope (session mode across turns). Its
	// URL + token are unchanged, so the persistent leader process keeps talking
	// to a live endpoint; we only repoint attribution at the new run.
	if existing := m.servers[scopeKey]; existing != nil {
		existing.setCurrentRun(req.RunID)
		return []agentadaptor.RuntimeServiceRef{existing.serviceRef(req.Agent)}, nil
	}

	token, err := randomToken()
	if err != nil {
		return nil, fmt.Errorf("create delegation bearer token: %w", err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("listen for delegation MCP sidecar: %w", err)
	}
	sidecar := &delegationSidecar{
		scopeKey:      scopeKey,
		sessionScoped: sessionScoped,
		url:           "http://" + listener.Addr().String() + "/mcp",
		token:         token,
		server:        &http.Server{ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second},
		listener:      listener,
		serveErr:      make(chan error, 1),
	}
	sidecar.setCurrentRun(req.RunID)
	sidecar.metadata = map[string]string{
		"example":                               "team-agent-workflow",
		"delegation_scope":                      scopeKey,
		"agentadaptor.mcp.enabled":              "true",
		"agentadaptor.mcp.key":                  "team-delegation",
		"agentadaptor.mcp.transport":            string(agentadaptor.MCPTransportHTTP),
		"agentadaptor.mcp.url":                  sidecar.url,
		"agentadaptor.mcp.bearer_token_env_var": delegationTokenEnv,
		"agentadaptor.mcp.required":             "true",
		"agentadaptor.mcp.required_reason":      "The Claude leader must delegate plan, implementation, and review through curated A2A roles.",
	}
	delegator := a2adelegation.NewDelegator(m.registry, m.bus)
	delegator.Observe = func(event a2adelegation.DelegationEvent) {
		term.Logf(
			"[a2a-live] time=%s agent=%s kind=%s status=%s delta_bytes=%d tool=%s",
			event.Time.UTC().Format(time.RFC3339Nano),
			event.AgentKey,
			event.Kind,
			event.Status,
			len(event.Delta),
			event.RemoteToolCallID,
		)
	}
	// RunIDResolver returns the sidecar's current run so a session-scoped
	// endpoint attributes each turn's delegation to the live run rather than
	// the run that happened to spawn it.
	mcp := a2adelegation.NewMCPServer(delegator, a2adelegation.MCPServerOptions{
		RunIDResolver: sidecar.runID, BearerToken: token, Tenant: req.Agent.TenantID,
	})
	mux := http.NewServeMux()
	mux.Handle("/mcp", auditMCPCalls(mcp.Handler(), func(agent string, result a2adelegation.DelegationResult) {
		m.recordResult(sidecar.runID(), agent, result)
	}))
	sidecar.server.Handler = mux

	m.servers[scopeKey] = sidecar
	go func() {
		err := sidecar.server.Serve(listener)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		sidecar.serveErr <- err
	}()
	return []agentadaptor.RuntimeServiceRef{sidecar.serviceRef(req.Agent)}, nil
}

func auditMCPCalls(next http.Handler, record func(string, a2adelegation.DelegationResult)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "read request body", http.StatusBadRequest)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(raw))
		agent, timeout, isToolCall := mcpCallSummary(raw)
		if !isToolCall {
			next.ServeHTTP(w, r)
			return
		}
		started := time.Now()
		status := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		term.Logf("[mcp] delegate agent=%s timeout_seconds=%d", agent, timeout)
		next.ServeHTTP(status, r)
		term.Logf("[mcp] complete agent=%s http_status=%d elapsed=%s", agent, status.status, time.Since(started).Round(time.Millisecond))
		if result, ok := parseDelegationToolResult(status.body.Bytes()); ok && record != nil {
			record(agent, result)
		}
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
	body   bytes.Buffer
}

func (w *statusRecorder) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *statusRecorder) Write(value []byte) (int, error) {
	_, _ = w.body.Write(value)
	return w.ResponseWriter.Write(value)
}

func mcpCallSummary(raw []byte) (string, int, bool) {
	var request struct {
		Method string `json:"method"`
		Params struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		} `json:"params"`
	}
	if json.Unmarshal(raw, &request) != nil || request.Method != "tools/call" || request.Params.Name != a2adelegation.DelegateToolName {
		return "", 0, false
	}
	var input a2adelegation.ToolInput
	if json.Unmarshal(request.Params.Arguments, &input) != nil {
		return "invalid", 0, true
	}
	return input.Agent, input.Constraints.TimeoutSeconds, true
}

func parseDelegationToolResult(raw []byte) (a2adelegation.DelegationResult, bool) {
	var response struct {
		Result struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	if json.Unmarshal(raw, &response) != nil {
		return a2adelegation.DelegationResult{}, false
	}
	for _, content := range response.Result.Content {
		if content.Type != "text" || content.Text == "" {
			continue
		}
		var result a2adelegation.DelegationResult
		if json.Unmarshal([]byte(content.Text), &result) == nil && result.Agent != "" {
			return result, true
		}
	}
	return a2adelegation.DelegationResult{}, false
}

func (m *delegationRuntimeManager) recordResult(runID, agent string, result a2adelegation.DelegationResult) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.results[runID] == nil {
		m.results[runID] = map[string]a2adelegation.DelegationResult{}
	}
	m.results[runID][agent] = result
}

func (m *delegationRuntimeManager) RequireResultLine(runID, agent, marker string) error {
	m.mu.Lock()
	result, ok := m.results[runID][agent]
	m.mu.Unlock()
	if !ok {
		return fmt.Errorf("missing structured MCP delegation result for role %q", agent)
	}
	if delegationResultHasLine(result, marker) {
		return nil
	}
	return fmt.Errorf("delegated role %q did not return required line %q", agent, marker)
}

func delegationResultHasLine(result a2adelegation.DelegationResult, marker string) bool {
	values := []string{result.Summary}
	for _, message := range result.Messages {
		values = append(values, message.Text)
	}
	for _, value := range values {
		for _, line := range strings.Split(value, "\n") {
			if strings.TrimSpace(line) == marker {
				return true
			}
		}
	}
	return false
}

func (m *delegationRuntimeManager) ReleaseByRun(_ context.Context, runID string) error {
	m.mu.Lock()
	// Session-scoped sidecars are keyed by session, not run, so a per-run
	// release must not tear them down — they outlive the run and are closed at
	// manager shutdown (Close) or by label instead. Only run-scoped (CLI)
	// sidecars, whose key equals the run ID, are released here.
	if sidecar := m.servers[runID]; sidecar == nil || sidecar.sessionScoped {
		m.mu.Unlock()
		return nil
	}
	m.mu.Unlock()
	return m.releaseScope(runID)
}

// releaseScope unconditionally stops and removes the sidecar for a scope key.
// The shutdown paths (Close / ReleaseByLabels) use it so session-scoped
// sidecars, which ReleaseByRun deliberately keeps alive, are still cleaned up.
func (m *delegationRuntimeManager) releaseScope(key string) error {
	m.mu.Lock()
	sidecar := m.servers[key]
	delete(m.servers, key)
	m.mu.Unlock()
	if sidecar == nil {
		return nil
	}
	return sidecar.Close()
}

func (m *delegationRuntimeManager) ReleaseByLabels(_ context.Context, labels map[string]string) error {
	if len(labels) == 0 {
		return nil
	}
	m.mu.Lock()
	var keys []string
	for key, sidecar := range m.servers {
		if covers(sidecar.metadata, labels) {
			keys = append(keys, key)
		}
	}
	m.mu.Unlock()
	for _, key := range keys {
		if err := m.releaseScope(key); err != nil {
			return err
		}
	}
	return nil
}

func (m *delegationRuntimeManager) Close() error {
	m.mu.Lock()
	keys := make([]string, 0, len(m.servers))
	for key := range m.servers {
		keys = append(keys, key)
	}
	m.mu.Unlock()
	for _, key := range keys {
		if err := m.releaseScope(key); err != nil {
			return err
		}
	}
	return nil
}

func (s *delegationSidecar) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := s.server.Shutdown(ctx)
	if err != nil {
		_ = s.server.Close()
	}
	serveErr := <-s.serveErr
	if err != nil {
		return err
	}
	return serveErr
}

func randomToken() (string, error) {
	var value [32]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(value[:]), nil
}

func covers(metadata, labels map[string]string) bool {
	for key, value := range labels {
		if metadata[key] != value {
			return false
		}
	}
	return true
}

func cloneLabels(values map[string]string) map[string]string {
	out := make(map[string]string, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out
}
