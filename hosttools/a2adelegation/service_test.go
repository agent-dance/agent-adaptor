package a2adelegation_test

// Unit tests for the consolidated Service: construction validation,
// per-run sidecar lifecycle (bearer auth, idempotency, release), and the
// closed-service behavior. The end-to-end delegation paths are covered by
// service_s9_test.go and local_remote_parity_test.go.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/hosttools/a2adelegation"
)

func newUnitService(t *testing.T) *a2adelegation.Service {
	t.Helper()
	svc, err := a2adelegation.NewService(a2adelegation.Config{
		Agents: []a2adelegation.AgentRef{
			a2adelegation.Local("echo", adaptor.New(&scriptedRoleDriver{kind: "fake-unit", final: "done"}), a2adelegation.Policy{}),
		},
		ToolTimeout: 42 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close() })
	return svc
}

func delegationErrCode(t *testing.T, err error) string {
	t.Helper()
	var dErr *a2adelegation.DelegationError
	if !errors.As(err, &dErr) {
		t.Fatalf("error %v (%T) is not *DelegationError", err, err)
	}
	return dErr.Code
}

func TestNewServiceValidation(t *testing.T) {
	runner := adaptor.New(&scriptedRoleDriver{kind: "fake-unit", final: "done"})

	_, err := a2adelegation.NewService(a2adelegation.Config{})
	if code := delegationErrCode(t, err); code != "configuration_error" {
		t.Errorf("empty agents: code = %q, want configuration_error", code)
	}

	_, err = a2adelegation.NewService(a2adelegation.Config{
		Agents: []a2adelegation.AgentRef{{}},
	})
	if code := delegationErrCode(t, err); code != "invalid_agent" {
		t.Errorf("zero ref: code = %q, want invalid_agent", code)
	}

	_, err = a2adelegation.NewService(a2adelegation.Config{
		Agents: []a2adelegation.AgentRef{
			a2adelegation.Local("echo", runner, a2adelegation.Policy{}),
			a2adelegation.Local("echo", runner, a2adelegation.Policy{}),
		},
	})
	if code := delegationErrCode(t, err); code != "duplicate_agent" {
		t.Errorf("duplicate key: code = %q, want duplicate_agent", code)
	}

	_, err = a2adelegation.NewService(a2adelegation.Config{
		Agents: []a2adelegation.AgentRef{
			a2adelegation.Remote("card-less", "", a2adelegation.Policy{}),
		},
	})
	if code := delegationErrCode(t, err); code != "invalid_agent" {
		t.Errorf("remote without card URL: code = %q, want invalid_agent", code)
	}
}

// mcpRPC posts one JSON-RPC request to the sidecar and returns status + body.
func mcpRPC(t *testing.T, sc a2adelegation.Sidecar, bearer, method string, params any) (int, string) {
	t.Helper()
	payload := map[string]any{"jsonrpc": "2.0", "id": 1, "method": method}
	if params != nil {
		payload["params"] = params
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal rpc: %v", err)
	}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, sc.URL, bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", sc.URL, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return resp.StatusCode, string(body)
}

func TestEnsureSidecarLifecycle(t *testing.T) {
	svc := newUnitService(t)

	if _, err := svc.EnsureSidecar("  "); err == nil {
		t.Error("EnsureSidecar(blank) succeeded, want error")
	}

	sc, err := svc.EnsureSidecar("run-1")
	if err != nil {
		t.Fatalf("EnsureSidecar: %v", err)
	}
	if sc.RunID != "run-1" {
		t.Errorf("RunID = %q, want run-1", sc.RunID)
	}
	if !strings.HasPrefix(sc.URL, "http://127.0.0.1:") || !strings.HasSuffix(sc.URL, "/mcp") {
		t.Errorf("URL = %q, want loopback /mcp endpoint", sc.URL)
	}
	if len(sc.BearerToken) != 64 {
		t.Errorf("BearerToken length = %d, want 64 hex chars", len(sc.BearerToken))
	}
	if sc.ToolTimeout != 42*time.Second {
		t.Errorf("ToolTimeout = %v, want 42s", sc.ToolTimeout)
	}

	// Idempotent per run.
	again, err := svc.EnsureSidecar("run-1")
	if err != nil {
		t.Fatalf("EnsureSidecar(again): %v", err)
	}
	if again.URL != sc.URL || again.BearerToken != sc.BearerToken {
		t.Errorf("second EnsureSidecar returned different endpoint: %+v vs %+v", again, sc)
	}

	// Distinct runs get distinct endpoints and tokens.
	other, err := svc.EnsureSidecar("run-2")
	if err != nil {
		t.Fatalf("EnsureSidecar(run-2): %v", err)
	}
	if other.URL == sc.URL || other.BearerToken == sc.BearerToken {
		t.Errorf("run-2 sidecar not distinct: %+v vs %+v", other, sc)
	}

	// Bearer auth: missing/wrong token is rejected, right token accepted.
	if status, _ := mcpRPC(t, sc, "", "tools/list", nil); status != http.StatusUnauthorized {
		t.Errorf("no token: status = %d, want 401", status)
	}
	if status, _ := mcpRPC(t, sc, "wrong-token", "tools/list", nil); status != http.StatusUnauthorized {
		t.Errorf("wrong token: status = %d, want 401", status)
	}
	status, body := mcpRPC(t, sc, sc.BearerToken, "tools/list", nil)
	if status != http.StatusOK {
		t.Fatalf("tools/list: status = %d, body = %s", status, body)
	}
	if !strings.Contains(body, a2adelegation.DelegateToolName) {
		t.Errorf("tools/list body missing %q: %s", a2adelegation.DelegateToolName, body)
	}

	// ReleaseRun stops the endpoint; a later EnsureSidecar mints a fresh one.
	if err := svc.ReleaseRun("run-1"); err != nil {
		t.Fatalf("ReleaseRun: %v", err)
	}
	if _, err := http.Get(sc.URL); err == nil {
		t.Error("sidecar still reachable after ReleaseRun")
	}
	fresh, err := svc.EnsureSidecar("run-1")
	if err != nil {
		t.Fatalf("EnsureSidecar(after release): %v", err)
	}
	if fresh.BearerToken == sc.BearerToken {
		t.Error("re-created sidecar reused the released bearer token")
	}

	// Close is idempotent and finalizes everything.
	if err := svc.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := svc.Close(); err != nil {
		t.Fatalf("Close(again): %v", err)
	}
	if _, err := svc.EnsureSidecar("run-3"); err == nil {
		t.Error("EnsureSidecar succeeded on closed service")
	}
	if _, err := svc.Delegate(context.Background(), a2adelegation.DelegationRequest{RunID: "run-3", Agent: "echo", Objective: "x"}); err == nil {
		t.Error("Delegate succeeded on closed service")
	}
	if _, err := http.Get(other.URL); err == nil {
		t.Error("run-2 sidecar still reachable after Close")
	}
}

func TestResultsSurviveReleaseAndClose(t *testing.T) {
	svc := newUnitService(t)

	res, err := svc.Delegate(context.Background(), a2adelegation.DelegationRequest{
		RunID: "run-r", Agent: "echo", Objective: "finish",
	})
	if err != nil {
		t.Fatalf("Delegate: %v", err)
	}
	if res.Status != "completed" {
		t.Fatalf("status = %q, want completed", res.Status)
	}

	got, ok := svc.Result("run-r", "echo")
	if !ok || got.Summary != "done" {
		t.Fatalf("Result = (%+v, %v), want recorded summary %q", got, ok, "done")
	}
	if _, ok := svc.Result("run-r", "nobody"); ok {
		t.Error("Result for unknown key reported ok")
	}
	if _, ok := svc.Result("other-run", "echo"); ok {
		t.Error("Result for unknown run reported ok")
	}

	if err := svc.ReleaseRun("run-r"); err != nil {
		t.Fatalf("ReleaseRun: %v", err)
	}
	if _, ok := svc.Result("run-r", "echo"); !ok {
		t.Error("Result lost after ReleaseRun; results must outlive the run")
	}
	if err := svc.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if all := svc.Results("run-r"); len(all) != 1 {
		t.Errorf("Results after Close = %v, want the one recorded entry", all)
	}
}

func TestHasLine(t *testing.T) {
	res := a2adelegation.DelegationResult{
		Summary: "line one\n  APPROVED  \nline three",
		Messages: []a2adelegation.DelegationMessage{
			{Role: "ROLE_AGENT", Text: "prefix\nSENTINEL_IN_MESSAGE"},
		},
	}
	if !res.HasLine("APPROVED") {
		t.Error("HasLine missed trimmed summary line")
	}
	if !res.HasLine("SENTINEL_IN_MESSAGE") {
		t.Error("HasLine missed message line")
	}
	if res.HasLine("line") {
		t.Error("HasLine matched a substring, want whole-line match")
	}
	if res.HasLine("") {
		t.Error("HasLine(\"\") matched")
	}
}
