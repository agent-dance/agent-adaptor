//go:build codebuddy_live

// Package codebuddy live tests exercise the real CodeBuddy CLI through the
// v1 Agent/Thread/Stream API. They remain opt-in because they require a
// logged-in CLI and may make paid provider calls.
package codebuddy

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/memory"
	adaptor "github.com/agent-dance/agent-adaptor"
)

func envOr(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func codebuddyCLIName() string { return "codebuddy" }

func requireCodeBuddyCLI(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath(codebuddyCLIName()); err != nil {
		t.Skipf("%s CLI not in PATH", codebuddyCLIName())
	}
}

func liveModel() string { return "glm-5.2-ioa" }

// isolatedConfigDir copies only login material. In particular it omits
// mcp.json so live conformance never depends on an operator's MCP servers.
func isolatedConfigDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Logf("isolatedConfigDir: resolve home: %v", err)
		return dir
	}
	source := envOr("CODEBUDDY_CONFIG_DIR_SOURCE", filepath.Join(home, ".codebuddy"))
	for _, name := range []string{".credentials.json", "credentials.json", "settings.json"} {
		payload, readErr := os.ReadFile(filepath.Join(source, name))
		if readErr != nil {
			continue
		}
		if writeErr := os.WriteFile(filepath.Join(dir, name), payload, 0o600); writeErr != nil {
			t.Logf("isolatedConfigDir: copy %s: %v", name, writeErr)
		}
	}
	return dir
}

func newLiveAgent(t *testing.T, cwd string, planMode bool) *adaptor.Agent {
	t.Helper()
	cfg := Config{
		CommonConfig: CommonConfig{
			Command: codebuddyCLIName(),
			CWD:     cwd,
			Env: []driver.EnvBinding{
				{Name: "CODEBUDDY_CONFIG_DIR", Value: isolatedConfigDir(t)},
			},
		},
		Model: liveModel(),
	}
	if planMode {
		cfg.ExtraArgs = []string{"--permission-mode", "plan"}
	}
	return adaptor.New(
		Driver(cfg),
		adaptor.WithWorkspace(cwd),
		adaptor.WithThreadStore(memory.NewStore()),
	)
}

var livePolicyHeadless = adaptor.Policy{
	Sandbox: adaptor.Unrestricted,
	Approvals: adaptor.ApprovalPolicy{
		Permission: adaptor.ApprovalAutoApprove,
		PlanReview: adaptor.ApprovalAutoApprove,
	},
}

func collectLiveStream(ctx context.Context, runner adaptor.Runner, prompt string, opts ...adaptor.CallOption) (*adaptor.Result, []adaptor.Event, error) {
	stream := runner.Stream(ctx, prompt, opts...)
	events := make([]adaptor.Event, 0, 32)
	for event := range stream.Events() {
		events = append(events, event)
	}
	result, err := stream.Result()
	return result, events, err
}

func logLiveEvents(t *testing.T, events []adaptor.Event) {
	t.Helper()
	for _, event := range events {
		switch typed := event.(type) {
		case adaptor.TextDelta:
			if typed.Phase == adaptor.PhaseContent {
				t.Logf("[text] %s", typed.Text)
			}
		case adaptor.Thinking:
			if typed.Phase == adaptor.PhaseContent {
				t.Logf("[thinking] %s", typed.Text)
			}
		case adaptor.ToolCall:
			t.Logf("[tool] phase=%s name=%s id=%s", typed.Phase, typed.Name, typed.ID)
		case adaptor.ProcessInfo:
			t.Logf("[process] kind=%s text=%s bytes=%q", typed.Kind, typed.Text, truncateForLog(string(typed.Bytes)))
		case adaptor.RunFinished:
			t.Logf("[finished] failed=%v usage=%+v", typed.Failed, typed.Usage)
		case adaptor.Notice:
			t.Logf("[notice] kind=%s text=%s", typed.Kind, truncateForLog(typed.Text))
		case *adaptor.ApprovalRequest:
			t.Logf("[approval] kind=%s source=%s title=%q", typed.Kind, typed.Source, truncateForLog(typed.Title))
		}
	}
}

func truncateForLog(value string) string {
	const max = 300
	if len(value) <= max {
		return value
	}
	return value[:max] + "…"
}

func TestCodeBuddyLiveHeadlessStreaming(t *testing.T) {
	requireCodeBuddyCLI(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	agent := newLiveAgent(t, t.TempDir(), false)
	result, events, err := collectLiveStream(ctx, agent,
		"Write a haiku about autumn. Reply with only the haiku.",
		adaptor.WithPolicy(livePolicyHeadless),
	)
	logLiveEvents(t, events)
	if err != nil {
		t.Fatalf("Stream.Result: %v", err)
	}

	var assembled strings.Builder
	sawFinishWithUsage := false
	for _, event := range events {
		switch typed := event.(type) {
		case adaptor.TextDelta:
			if typed.Phase == adaptor.PhaseContent {
				assembled.WriteString(typed.Text)
			}
		case adaptor.RunFinished:
			if typed.Usage != nil && typed.Usage.InputTokens > 0 {
				sawFinishWithUsage = true
			}
		}
	}
	if assembled.Len() == 0 {
		t.Fatal("missing streamed text content")
	}
	if !sawFinishWithUsage {
		t.Fatal("missing RunFinished with observed input usage")
	}
	if !strings.Contains(strings.TrimSpace(result.Text), strings.TrimSpace(assembled.String())) {
		t.Fatalf("delta text diverges from result: stream=%q result=%q", assembled.String(), result.Text)
	}
}

func TestCodeBuddyLiveThreadResume(t *testing.T) {
	requireCodeBuddyCLI(t)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	agent := newLiveAgent(t, t.TempDir(), false)
	thread := agent.Thread("codebuddy/live/resume")
	first, events, err := collectLiveStream(ctx, thread,
		"Remember the word banana. Reply with only: OK.",
		adaptor.WithPolicy(livePolicyHeadless),
	)
	logLiveEvents(t, events)
	if err != nil {
		t.Fatalf("first turn: %v", err)
	}
	if first == nil || strings.TrimSpace(first.Text) == "" {
		t.Fatalf("first turn returned no text: %+v", first)
	}
	checkpoint, err := thread.Checkpoint(ctx)
	if err != nil || checkpoint == nil || checkpoint.State == nil || checkpoint.State.ResumeID == "" {
		t.Fatalf("checkpoint after first turn = (%+v, %v)", checkpoint, err)
	}

	second, events, err := collectLiveStream(ctx, thread,
		"What word did I ask you to remember? Reply with only that word.",
		adaptor.WithPolicy(livePolicyHeadless),
	)
	logLiveEvents(t, events)
	if err != nil {
		t.Fatalf("second turn: %v", err)
	}
	if !strings.Contains(strings.ToLower(second.Text), "banana") {
		t.Logf("resume recall is model-dependent; result=%q", second.Text)
	}
}

func TestCodeBuddyLivePermissionApprove(t *testing.T) {
	requireCodeBuddyCLI(t)
	cwd := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	var (
		mu       sync.Mutex
		requests []*adaptor.ApprovalRequest
	)
	agent := newLiveAgent(t, cwd, false)
	result, events, err := collectLiveStream(ctx, agent,
		"Create hello.txt containing hi using a file-editing tool. Reply only DONE.",
		adaptor.WithPolicy(adaptor.Policy{Approvals: adaptor.ApprovalPolicy{
			Permission: adaptor.ApprovalAsk,
			PlanReview: adaptor.ApprovalAutoApprove,
			Question:   adaptor.QuestionAutoDeny,
		}}),
		adaptor.OnApproval(func(callbackCtx context.Context, request *adaptor.ApprovalRequest) error {
			mu.Lock()
			requests = append(requests, request)
			mu.Unlock()
			if request.Kind != adaptor.ApprovalPermission {
				return request.Deny(callbackCtx, "unexpected approval kind")
			}
			return request.Approve(callbackCtx)
		}),
	)
	logLiveEvents(t, events)
	if err != nil {
		t.Fatalf("permission-approved run: %v", err)
	}
	mu.Lock()
	count := len(requests)
	mu.Unlock()
	if count == 0 {
		t.Fatalf("permission callback was not invoked; result=%q", result.Text)
	}
	payload, readErr := os.ReadFile(filepath.Join(cwd, "hello.txt"))
	if readErr != nil || strings.TrimSpace(string(payload)) != "hi" {
		t.Fatalf("hello.txt = (%q, %v), want hi", payload, readErr)
	}
}

func TestCodeBuddyLiveQuestionAnswered(t *testing.T) {
	requireCodeBuddyCLI(t)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	var calls int
	agent := newLiveAgent(t, t.TempDir(), false)
	result, events, err := collectLiveStream(ctx, agent,
		"请使用 AskUserQuestion 问我喜欢哪种编程语言，收到回答后只回复 ANSWER=<回答>。",
		adaptor.WithPolicy(adaptor.Policy{Approvals: adaptor.ApprovalPolicy{
			Permission: adaptor.ApprovalAutoApprove,
			PlanReview: adaptor.ApprovalAutoApprove,
			Question:   adaptor.QuestionAsk,
		}}),
		adaptor.OnApproval(func(callbackCtx context.Context, request *adaptor.ApprovalRequest) error {
			if request.Kind != adaptor.ApprovalQuestion {
				return request.Deny(callbackCtx, "unexpected approval kind")
			}
			calls++
			return request.Answer(callbackCtx, "Go")
		}),
	)
	logLiveEvents(t, events)
	if err != nil {
		t.Fatalf("question run: %v", err)
	}
	if calls != 1 {
		t.Fatalf("question callback calls=%d, want 1", calls)
	}
	if !strings.Contains(strings.ToLower(result.Text), "answer=go") {
		t.Fatalf("result must contain ANSWER=Go, got %q", result.Text)
	}
}

func TestCodeBuddyLivePlanApprove(t *testing.T) {
	requireCodeBuddyCLI(t)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	var plan string
	agent := newLiveAgent(t, t.TempDir(), true)
	result, events, err := collectLiveStream(ctx, agent,
		"Plan and then add hello.py that prints 'Hello, plan mode'. Submit the plan through ExitPlanMode before implementation.",
		adaptor.WithPolicy(adaptor.Policy{Approvals: adaptor.ApprovalPolicy{
			Permission: adaptor.ApprovalAutoApprove,
			PlanReview: adaptor.ApprovalAsk,
			Question:   adaptor.QuestionAutoDeny,
		}}),
		adaptor.OnApproval(func(callbackCtx context.Context, request *adaptor.ApprovalRequest) error {
			if request.Kind != adaptor.ApprovalPlanReview {
				return request.Deny(callbackCtx, "unexpected approval kind")
			}
			plan, _ = request.Details["plan"].(string)
			return request.Approve(callbackCtx)
		}),
	)
	logLiveEvents(t, events)
	if err != nil {
		t.Fatalf("plan-approved run: %v", err)
	}
	if strings.TrimSpace(plan) == "" {
		t.Fatal("plan approval did not carry captured plan text")
	}
	if result == nil || strings.TrimSpace(result.Text) == "" {
		t.Fatalf("missing result after plan approval: %+v", result)
	}
}

func TestCodeBuddyLivePlanReject(t *testing.T) {
	requireCodeBuddyCLI(t)
	cwd := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	agent := newLiveAgent(t, cwd, true)
	_, events, err := collectLiveStream(ctx, agent,
		"Plan and then add hello.py that prints 'Hello'. Submit the plan through ExitPlanMode before implementation.",
		adaptor.WithPolicy(adaptor.Policy{Approvals: adaptor.ApprovalPolicy{
			Permission: adaptor.ApprovalAutoApprove,
			PlanReview: adaptor.ApprovalAsk,
			Question:   adaptor.QuestionAutoDeny,
			OnReject:   adaptor.FallbackAbort,
		}}),
		adaptor.OnApproval(func(callbackCtx context.Context, request *adaptor.ApprovalRequest) error {
			return request.Deny(callbackCtx, "plan rejected by live test")
		}),
	)
	logLiveEvents(t, events)
	var runErr *adaptor.RunError
	if !errors.As(err, &runErr) || !errors.Is(err, adaptor.ErrApprovalDenied) {
		t.Fatalf("plan rejection error=%v, want RunError/ErrApprovalDenied", err)
	}
	if runErr.Result == nil {
		t.Fatal("plan rejection RunError lost partial Result")
	}
	if _, statErr := os.Stat(filepath.Join(cwd, "hello.py")); statErr == nil {
		t.Error("hello.py was created despite rejected plan")
	}
}

func TestCodeBuddyLivePermissionReject(t *testing.T) {
	requireCodeBuddyCLI(t)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	agent := newLiveAgent(t, t.TempDir(), false)
	_, events, err := collectLiveStream(ctx, agent,
		"Create blocked.txt with any content using a file-editing tool.",
		adaptor.WithPolicy(adaptor.Policy{Approvals: adaptor.ApprovalPolicy{
			Permission: adaptor.ApprovalAsk,
			PlanReview: adaptor.ApprovalAutoApprove,
			OnReject:   adaptor.FallbackAbort,
		}}),
		adaptor.OnApproval(func(callbackCtx context.Context, request *adaptor.ApprovalRequest) error {
			return request.Deny(callbackCtx, "permission rejected by live test")
		}),
	)
	logLiveEvents(t, events)
	if err != nil && !errors.Is(err, adaptor.ErrApprovalDenied) {
		t.Fatalf("permission rejection error=%v", err)
	}
}
