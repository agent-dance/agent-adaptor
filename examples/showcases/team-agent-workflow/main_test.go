package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	adaptor "github.com/agent-dance/agent-adaptor/v2"
	"github.com/agent-dance/agent-adaptor/v2/examples/internal/exampleutil"
)

// These tests use configuration and fake Runners; they must never execute
// any of the four live CLI calls made by the showcase.
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
	var info map[string]any
	if health.Code != http.StatusOK || json.Unmarshal(health.Body.Bytes(), &info) != nil || info["mode"] != "real" || info["observation"] != true {
		t.Fatalf("GET /health = (%d, %q), want the assistant-ui health shape", health.Code, health.Body.String())
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

func TestStartAllScriptWiresTeamBackendToAssistantUI(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows filesystems do not expose the repository's POSIX executable bit")
	}
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
		"AGUI_BACKEND_URL",
		"examples/web-chat/assistant-ui/web",
		"npm ci",
		"npm run dev",
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
	if _, err := wrapped.Send(context.Background(), "prompt", trackingCallOption{label: "caller", order: &order}).Result(); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(order, ","); got != "caller,role" {
		t.Fatalf("call option order = %q, want caller,role", got)
	}
}

type inertRunner struct{}

func (inertRunner) Send(context.Context, string, ...adaptor.CallOption) adaptor.Stream {
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

func (optionApplyingRunner) Send(_ context.Context, _ string, opts ...adaptor.CallOption) adaptor.Stream {
	var settings adaptor.RunSettings
	for _, o := range opts {
		o.ApplyRun(&settings)
	}
	return optionStream{}
}

type optionStream struct{}

func (optionStream) Events() <-chan adaptor.Event {
	ch := make(chan adaptor.Event)
	close(ch)
	return ch
}
func (optionStream) Result() (*adaptor.Result, error) { return &adaptor.Result{}, nil }
func (optionStream) Send(context.Context, string, ...adaptor.MessageOption) error {
	return adaptor.ErrRunFinished
}
func (optionStream) Cancel()       {}
func (optionStream) RunID() string { return "test" }

func TestWebHandlerRehydratesSecondTurnWithoutCheckpoint(t *testing.T) {
	runner := &historyRunner{}
	handler := newTeamWebHandler(runner, "")
	post := func(body string) {
		t.Helper()
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest("POST", "/agent", strings.NewReader(body)))
		if response.Code != 200 {
			t.Fatalf("status=%d body=%s", response.Code, response.Body)
		}
	}
	post(`{"threadId":"team","runId":"r1","messages":[{"id":"u1","role":"user","content":"first"}]}`)
	// The page sends back the recorded history, as assistant-ui's adapter does.
	stored := httptest.NewRecorder()
	handler.ServeHTTP(stored, httptest.NewRequest("GET", "/history?thread_id=team", nil))
	var history []map[string]any
	if err := json.Unmarshal(stored.Body.Bytes(), &history); err != nil || len(history) != 2 {
		t.Fatalf("history = %s (%v)", stored.Body, err)
	}
	history = append(history, map[string]any{"id": "u2", "role": "user", "content": "second"})
	body, _ := json.Marshal(map[string]any{"threadId": "team", "runId": "r2", "messages": history})
	post(string(body))
	if len(runner.prompts) != 2 || !strings.Contains(runner.prompts[1], "<agent-adaptor-history>") ||
		!strings.Contains(runner.prompts[1], `"text":"first"`) || !strings.HasSuffix(runner.prompts[1], "second") {
		t.Fatalf("second turn lost history: %v", runner.prompts)
	}
}

type historyRunner struct{ prompts []string }

func (r *historyRunner) Send(_ context.Context, prompt string, _ ...adaptor.CallOption) adaptor.Stream {
	r.prompts = append(r.prompts, prompt)
	events := make(chan adaptor.Event)
	close(events)
	return historyStream{events: events}
}

type historyStream struct{ events <-chan adaptor.Event }

func (s historyStream) Events() <-chan adaptor.Event { return s.events }

func (historyStream) Result() (*adaptor.Result, error) { return &adaptor.Result{Text: "answer"}, nil }

func (historyStream) RunID() string { return "example-run" }

func (historyStream) Cancel() {}

func (historyStream) Send(context.Context, string, ...adaptor.MessageOption) error {
	return adaptor.ErrRunFinished
}
