package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/examples/internal/exampleutil"
)

// This package's tests are deliberately configuration-only: they must never
// execute any of the four live CLI calls made by the showcase.
func TestPaidWebModeDefaultsAreLoopbackAndCORSDisabled(t *testing.T) {
	if defaultWebListenAddr != "127.0.0.1:8080" {
		t.Fatalf("default web address = %q, want loopback", defaultWebListenAddr)
	}
	if defaultWebCORSOrigin != "" {
		t.Fatalf("default CORS origin = %q, want disabled", defaultWebCORSOrigin)
	}
}

func TestTeamWebHandlerExposesHealthAndScopedCORS(t *testing.T) {
	handler := newTeamWebHandler(inertRunner{}, "http://127.0.0.1:3000")

	health := httptest.NewRecorder()
	handler.ServeHTTP(health, httptest.NewRequest(http.MethodGet, "/health", nil))
	if health.Code != http.StatusOK || health.Body.String() != "ok" {
		t.Fatalf("GET /health = (%d, %q), want (200, ok)", health.Code, health.Body.String())
	}

	preflight := httptest.NewRecorder()
	handler.ServeHTTP(preflight, httptest.NewRequest(http.MethodOptions, "/agent", nil))
	if preflight.Code != http.StatusNoContent {
		t.Fatalf("OPTIONS /agent status = %d, want 204", preflight.Code)
	}
	if got := preflight.Header().Get("Access-Control-Allow-Origin"); got != "http://127.0.0.1:3000" {
		t.Fatalf("Access-Control-Allow-Origin = %q", got)
	}
}

func TestStartAllScriptWiresTeamBackendToCopilotKit(t *testing.T) {
	info, err := os.Stat("start-all.sh")
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Fatalf("start-all.sh mode = %o, want executable", info.Mode().Perm())
	}

	raw, err := os.ReadFile("start-all.sh")
	if err != nil {
		t.Fatal(err)
	}
	script := string(raw)
	for _, want := range []string{
		"go build -o",
		"./examples/showcases/team-agent-workflow",
		"-web-mode",
		"NEXT_PUBLIC_COPILOTKIT_MODE=team-agent-workflow",
		"AGENT_BACKEND_URL",
		"examples/web-chat/copilotkit/web",
		"npm run build",
		"npm run start",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("start-all.sh missing %q", want)
		}
	}
}

func TestProviderDisplayNameAndPlanArtifactContract(t *testing.T) {
	for agent, want := range map[string]string{
		exampleutil.AgentClaude:    "Claude Code",
		exampleutil.AgentCodex:     "Codex",
		exampleutil.AgentCursor:    "Cursor",
		exampleutil.AgentCodebuddy: "CodeBuddy",
	} {
		if got := providerDisplayName(agent); got != want {
			t.Errorf("providerDisplayName(%q) = %q, want %q", agent, got, want)
		}
	}

	artifact := planFileArtifact{
		Filename: planArtifactFilename, MediaType: "text/markdown",
		Summary: "Plan summary", Content: "# Plan\n\n1. Inspect",
	}
	raw, err := json.Marshal(artifact)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{`"filename":"PLAN.md"`, `"media_type":"text/markdown"`, `"summary":`, `"content":`} {
		if !strings.Contains(string(raw), field) {
			t.Errorf("plan artifact JSON %s missing %s", raw, field)
		}
	}
	protocol := leaderProtocol(90 * time.Second)
	if strings.Count(protocol, planArtifactFilename) != 2 {
		t.Fatalf("leader protocol should hand the plan artifact from plan to impl: %s", protocol)
	}
}

func TestRoleCallOptionsAreAppliedLast(t *testing.T) {
	var order []string
	wrapped := withCallOptions(optionApplyingRunner{}, trackingCallOption{label: "role", order: &order})
	if _, err := wrapped.Run(context.Background(), "prompt", trackingCallOption{label: "caller", order: &order}); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(order, ","); got != "caller,role" {
		t.Fatalf("call option order = %q, want caller,role", got)
	}
}

type inertRunner struct{}

func (inertRunner) Run(context.Context, string, ...adaptor.CallOption) (*adaptor.Result, error) {
	panic("unexpected Run")
}

func (inertRunner) Stream(context.Context, string, ...adaptor.CallOption) adaptor.Stream {
	panic("unexpected Stream")
}

type trackingCallOption struct {
	label string
	order *[]string
}

func (o trackingCallOption) ApplyRun(*adaptor.RunSettings) {
	*o.order = append(*o.order, o.label)
}

type optionApplyingRunner struct{}

func (optionApplyingRunner) Run(_ context.Context, _ string, opts ...adaptor.CallOption) (*adaptor.Result, error) {
	settings := &adaptor.RunSettings{}
	for _, option := range opts {
		option.ApplyRun(settings)
	}
	return &adaptor.Result{}, nil
}

func (optionApplyingRunner) Stream(context.Context, string, ...adaptor.CallOption) adaptor.Stream {
	panic("unexpected Stream")
}
