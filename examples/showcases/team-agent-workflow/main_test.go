package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	agentadaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/examples/internal/contractdriver"
	"github.com/agent-dance/agent-adaptor/examples/internal/exampleutil"
	"github.com/agent-dance/agent-adaptor/pkg/hosttools/a2adelegation"
)

const passingSlugImplementation = `package teamworkflow

import "strings"

// NormalizeSlug converts a display label to an ASCII URL slug.
func NormalizeSlug(input string) string {
	var out strings.Builder
	separator := false
	for _, value := range []byte(input) {
		switch {
		case value >= 'A' && value <= 'Z':
			if separator && out.Len() > 0 { out.WriteByte('-') }
			out.WriteByte(value + ('a' - 'A'))
			separator = false
		case value >= 'a' && value <= 'z', value >= '0' && value <= '9':
			if separator && out.Len() > 0 { out.WriteByte('-') }
			out.WriteByte(value)
			separator = false
		default:
			separator = out.Len() > 0
		}
	}
	return out.String()
}
`

func TestWorkflowFixtureRequiresOnePassingImplementationDiff(t *testing.T) {
	fixture, err := newWorkflowFixture(false)
	if err != nil {
		t.Fatalf("newWorkflowFixture: %v", err)
	}
	defer fixture.Cleanup()

	if err := os.WriteFile(filepath.Join(fixture.WorkspaceDir, "slug.go"), []byte(passingSlugImplementation), 0o644); err != nil {
		t.Fatalf("write implementation: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	validation, err := fixture.Validate(ctx)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if len(validation.ChangedFiles) != 1 || validation.ChangedFiles[0] != "slug.go" {
		t.Fatalf("changed files = %v", validation.ChangedFiles)
	}
	if err := os.WriteFile(filepath.Join(fixture.WorkspaceDir, "unexpected.txt"), []byte("unexpected"), 0o644); err != nil {
		t.Fatalf("write unexpected file: %v", err)
	}
	if _, err := fixture.Validate(ctx); err == nil {
		t.Fatal("untracked file unexpectedly passed workspace validation")
	}
}

func TestReviewEvidenceUsesHostToolchainAndIncludesDiff(t *testing.T) {
	fixture, err := newWorkflowFixture(false)
	if err != nil {
		t.Fatalf("newWorkflowFixture: %v", err)
	}
	defer fixture.Cleanup()
	if err := os.WriteFile(filepath.Join(fixture.WorkspaceDir, "slug.go"), []byte(passingSlugImplementation), 0o644); err != nil {
		t.Fatalf("write implementation: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	evidence, err := buildReviewEvidence(ctx, fixture)
	if err != nil {
		t.Fatalf("buildReviewEvidence: %v", err)
	}
	for _, marker := range []string{"go test ./...: ok", "git diff --check: passed", "changed files: slug.go", "func NormalizeSlug"} {
		if !strings.Contains(evidence, marker) {
			t.Errorf("review evidence missing %q:\n%s", marker, evidence)
		}
	}
}

func TestWorkflowTraceRequiresExactRoleOrder(t *testing.T) {
	trace := newWorkflowTrace()
	trace.events = []a2adelegation.DelegationEvent{
		{AgentKey: "plan", Kind: a2adelegation.DelegationStarted},
		{AgentKey: "plan", Kind: a2adelegation.DelegationFinished},
		{AgentKey: "impl", Kind: a2adelegation.DelegationStarted},
		{AgentKey: "impl", Kind: a2adelegation.DelegationFinished},
		{AgentKey: "review", Kind: a2adelegation.DelegationStarted},
		{AgentKey: "review", Kind: a2adelegation.DelegationTextDelta, Delta: "Checks passed.\nTEAM_REVIEW_APPROVED"},
		{AgentKey: "review", Kind: a2adelegation.DelegationFinished},
	}
	if err := trace.ValidateOrderedRoles([]string{"plan", "impl", "review"}); err != nil {
		t.Fatalf("valid trace: %v", err)
	}

	trace.events[3].AgentKey = "review"
	if err := trace.ValidateOrderedRoles([]string{"plan", "impl", "review"}); err == nil {
		t.Fatal("out-of-order trace unexpectedly passed")
	}
}

func TestStructuredDelegationResultRequiresExactApprovalLine(t *testing.T) {
	result := a2adelegation.DelegationResult{
		Agent: "review",
		Messages: []a2adelegation.DelegationMessage{{
			Role: "assistant", Text: "Review complete.\nNOT TEAM_REVIEW_APPROVED",
		}},
	}
	if delegationResultHasLine(result, reviewApprovalSentinel) {
		t.Fatal("substring-only review marker unexpectedly passed")
	}
	result.Messages[0].Text = "Review complete.\nTEAM_REVIEW_APPROVED"
	if !delegationResultHasLine(result, reviewApprovalSentinel) {
		t.Fatal("exact review marker did not pass")
	}
}

func TestParseDelegationToolResult(t *testing.T) {
	raw := []byte(`{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"{\"agent\":\"review\",\"status\":\"completed\",\"summary\":\"TEAM_REVIEW_APPROVED\"}"}],"isError":false}}`)
	result, ok := parseDelegationToolResult(raw)
	if !ok || result.Agent != "review" || result.Summary != reviewApprovalSentinel {
		t.Fatalf("parsed result ok=%t result=%#v", ok, result)
	}
}

func TestLeaderMCPTimeoutOutlivesDelegatedRoleTimeout(t *testing.T) {
	cfg := withMCPToolTimeout(exampleutil.LiveAgentConfig{
		Env: []agentadaptor.EnvBinding{{Name: "MCP_TOOL_TIMEOUT", Value: "1000"}, {Name: "KEEP", Value: "yes"}},
	}, 4*time.Minute+30*time.Second)
	if len(cfg.Env) != 2 || cfg.Env[0].Name != "KEEP" || cfg.Env[1] != (agentadaptor.EnvBinding{Name: "MCP_TOOL_TIMEOUT", Value: "270000"}) {
		t.Fatalf("leader env = %#v", cfg.Env)
	}
}

func TestNewLeaderSDKWebModeSupportsAGUIThreadSession(t *testing.T) {
	binding := contractdriver.New(contractdriver.Config{Output: "ok"})

	webSDK := newLeaderSDK(binding, nil, true)
	_, err := webSDK.Run(context.Background(), "hello", agentadaptor.WithSessionKey("agui", "thread-1"))
	if errors.Is(err, agentadaptor.ErrSessionStoreRequired) {
		t.Fatalf("web SDK still rejected AG-UI session key: %v", err)
	}
	if !errors.Is(err, agentadaptor.ErrSessionCheckpointMissing) {
		t.Fatalf("web SDK error = %v, want fake driver's checkpoint limitation", err)
	}

	cliSDK := newLeaderSDK(binding, nil, false)
	if _, err := cliSDK.Run(context.Background(), "hello", agentadaptor.WithSessionKey("agui", "thread-1")); !errors.Is(err, agentadaptor.ErrSessionStoreRequired) {
		t.Fatalf("CLI SDK session error = %v, want ErrSessionStoreRequired", err)
	}
}

func TestRoleAuditEnforcesStageBoundaries(t *testing.T) {
	fixture, err := newWorkflowFixture(false)
	if err != nil {
		t.Fatalf("newWorkflowFixture: %v", err)
	}
	defer fixture.Cleanup()

	audit := newRoleAudit(fixture)
	audit.Record("plan")
	if err := os.WriteFile(filepath.Join(fixture.WorkspaceDir, "slug.go"), []byte(passingSlugImplementation), 0o644); err != nil {
		t.Fatalf("write implementation: %v", err)
	}
	audit.Record("impl")
	audit.Record("review")
	audit.Record("final")
	if err := audit.ValidateStageBoundaries(); err != nil {
		t.Fatalf("valid stage boundaries: %v", err)
	}

	if err := os.WriteFile(filepath.Join(fixture.WorkspaceDir, "review-note.txt"), []byte("mutation"), 0o644); err != nil {
		t.Fatalf("write review mutation: %v", err)
	}
	audit.Record("review")
	audit.Record("final")
	if err := audit.ValidateStageBoundaries(); err == nil {
		t.Fatal("review mutation unexpectedly passed")
	}
}

func TestDelegationRuntimeInjectsAuthenticatedPerRunMCP(t *testing.T) {
	registry, err := a2adelegation.NewRegistry()
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	manager := newDelegationRuntimeManager(registry, a2adelegation.NewEventBus(8))
	defer manager.Close()

	refs, err := manager.Ensure(context.Background(), agentadaptor.RuntimeServiceRequest{
		RunID: "run-test", Agent: agentadaptor.AgentIdentity{ID: "leader", TenantID: "example"},
	})
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if len(refs) != 1 || refs[0].Metadata["agentadaptor.mcp.enabled"] != "true" {
		t.Fatalf("unexpected runtime refs: %#v", refs)
	}
	if len(refs[0].SecretEnv) != 1 || refs[0].SecretEnv[0].Name != delegationTokenEnv {
		t.Fatalf("unexpected secret env: %#v", refs[0].SecretEnv)
	}

	request := []byte(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`)
	if status, body := postMCP(t, refs[0].URL, "wrong", request); status != http.StatusUnauthorized {
		t.Fatalf("wrong token status=%d body=%s", status, body)
	}
	if status, body := postMCP(t, refs[0].URL, refs[0].SecretEnv[0].Value, request); status != http.StatusOK || !strings.Contains(body, "agent-adaptor-a2a-delegation") {
		t.Fatalf("authorized initialize status=%d body=%s", status, body)
	}
	toolsRequest := []byte(`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`)
	if status, body := postMCP(t, refs[0].URL, refs[0].SecretEnv[0].Value, toolsRequest); status != http.StatusOK || !strings.Contains(body, a2adelegation.DelegateToolName) {
		t.Fatalf("tools/list status=%d body=%s", status, body)
	}

	if err := manager.ReleaseByRun(context.Background(), "run-test"); err != nil {
		t.Fatalf("ReleaseByRun: %v", err)
	}
	manager.mu.Lock()
	remaining := len(manager.servers)
	manager.mu.Unlock()
	if remaining != 0 {
		t.Fatalf("runtime sidecars remaining = %d", remaining)
	}
}

func postMCP(t *testing.T, endpoint, token string, body []byte) (int, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	return resp.StatusCode, string(raw)
}
