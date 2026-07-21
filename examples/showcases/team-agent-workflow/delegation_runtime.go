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

type delegationSidecar struct {
	runID    string
	metadata map[string]string
	server   *http.Server
	listener net.Listener
	serveErr chan error
}

func newDelegationRuntimeManager(registry *a2adelegation.Registry, bus *a2adelegation.EventBus) *delegationRuntimeManager {
	return &delegationRuntimeManager{
		registry: registry,
		bus:      bus,
		servers:  map[string]*delegationSidecar{},
		results:  map[string]map[string]a2adelegation.DelegationResult{},
	}
}

func (m *delegationRuntimeManager) Ensure(ctx context.Context, req agentadaptor.RuntimeServiceRequest) ([]agentadaptor.RuntimeServiceRef, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if req.RunID == "" {
		return nil, errors.New("delegation runtime requires a run ID")
	}
	token, err := randomToken()
	if err != nil {
		return nil, fmt.Errorf("create delegation bearer token: %w", err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("listen for delegation MCP sidecar: %w", err)
	}
	delegator := a2adelegation.NewDelegator(m.registry, m.bus)
	mcp := a2adelegation.NewMCPServer(delegator, a2adelegation.MCPServerOptions{
		RunID: req.RunID, BearerToken: token, Tenant: req.Agent.TenantID,
	})
	mux := http.NewServeMux()
	mux.Handle("/mcp", auditMCPCalls(mcp.Handler(), func(agent string, result a2adelegation.DelegationResult) {
		m.recordResult(req.RunID, agent, result)
	}))
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second}
	metadata := map[string]string{
		"example":                               "team-agent-workflow",
		"run_id":                                req.RunID,
		"agentadaptor.mcp.enabled":              "true",
		"agentadaptor.mcp.key":                  "team-delegation",
		"agentadaptor.mcp.transport":            string(agentadaptor.MCPTransportHTTP),
		"agentadaptor.mcp.url":                  "http://" + listener.Addr().String() + "/mcp",
		"agentadaptor.mcp.bearer_token_env_var": delegationTokenEnv,
		"agentadaptor.mcp.required":             "true",
		"agentadaptor.mcp.required_reason":      "The Claude leader must delegate plan, implementation, and review through curated A2A roles.",
	}
	sidecar := &delegationSidecar{
		runID: req.RunID, metadata: metadata, server: server, listener: listener, serveErr: make(chan error, 1),
	}
	m.mu.Lock()
	if _, exists := m.servers[req.RunID]; exists {
		m.mu.Unlock()
		_ = listener.Close()
		return nil, fmt.Errorf("delegation sidecar already exists for run %s", req.RunID)
	}
	m.servers[req.RunID] = sidecar
	m.mu.Unlock()
	go func() {
		err := server.Serve(listener)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		sidecar.serveErr <- err
	}()
	return []agentadaptor.RuntimeServiceRef{{
		ID: req.RunID + ":team-delegation", Name: "team-delegation", URL: metadata["agentadaptor.mcp.url"],
		Status: agentadaptor.RuntimeServiceRunning, Lifecycle: agentadaptor.RuntimeLifecycleEphemeral,
		OwnerAgentID: req.Agent.ID, Health: agentadaptor.RuntimeHealthHealthy, Metadata: cloneLabels(metadata),
		SecretEnv: []agentadaptor.EnvBinding{{Name: delegationTokenEnv, Value: token}},
	}}, nil
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
	sidecar := m.servers[runID]
	delete(m.servers, runID)
	m.mu.Unlock()
	if sidecar == nil {
		return nil
	}
	return sidecar.Close()
}

func (m *delegationRuntimeManager) ReleaseByLabels(ctx context.Context, labels map[string]string) error {
	if len(labels) == 0 {
		return nil
	}
	m.mu.Lock()
	var runIDs []string
	for runID, sidecar := range m.servers {
		if covers(sidecar.metadata, labels) {
			runIDs = append(runIDs, runID)
		}
	}
	m.mu.Unlock()
	for _, runID := range runIDs {
		if err := m.ReleaseByRun(ctx, runID); err != nil {
			return err
		}
	}
	return nil
}

func (m *delegationRuntimeManager) Close() error {
	m.mu.Lock()
	runIDs := make([]string, 0, len(m.servers))
	for runID := range m.servers {
		runIDs = append(runIDs, runID)
	}
	m.mu.Unlock()
	for _, runID := range runIDs {
		if err := m.ReleaseByRun(context.Background(), runID); err != nil {
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
