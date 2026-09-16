//go:build claude_live

package claude_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/claude"
)

// Run with:
//
//	go test -tags claude_live -run TestClaudeInteractive -v ./claude
//
// Requires a Claude-compatible CLI with interactive stream-json support.
func TestClaudeInteractive_PlanApproved(t *testing.T) {
	requireInteractiveCLI(t)
	agent := newInteractiveAgent(t, envOr("CLAUDE_MODEL_P3", "claude-haiku-4-5"))
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	var mu sync.Mutex
	var requests []adaptor.ApprovalRequest
	result, err := agent.Run(ctx,
		"Enter plan mode, design a two-step plan for refactoring the file `main.go` (do not actually edit). "+
			"Call ExitPlanMode with the plan. Do not use any other tools.",
		interactivePolicy(adaptor.ApprovalAsk, adaptor.QuestionAutoDeny),
		adaptor.OnApproval(func(ctx context.Context, req *adaptor.ApprovalRequest) error {
			mu.Lock()
			requests = append(requests, *req)
			mu.Unlock()
			return req.Approve(ctx)
		}),
	)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	mu.Lock()
	calls := append([]adaptor.ApprovalRequest(nil), requests...)
	mu.Unlock()
	if len(calls) == 0 || calls[0].Kind != adaptor.ApprovalPlanReview {
		t.Fatalf("plan approval handler calls = %#v, result=%#v", calls, result)
	}
	if result.Text == "" {
		t.Fatal("final Text missing after approval")
	}
}

func TestClaudeInteractive_PlanRejected(t *testing.T) {
	requireInteractiveCLI(t)
	agent := newInteractiveAgent(t, envOr("CLAUDE_MODEL_P3", "claude-haiku-4-5"))
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	result, err := agent.Run(ctx,
		"Enter plan mode, design a two-step plan for refactoring the file `main.go` (do not actually edit). "+
			"Call ExitPlanMode with the plan. Do not ask questions or use other tools.",
		interactivePolicy(adaptor.ApprovalAsk, adaptor.QuestionAutoDeny),
		adaptor.OnApproval(func(ctx context.Context, req *adaptor.ApprovalRequest) error {
			return req.Deny(ctx, "plan rejected by live conformance host")
		}),
	)
	if result != nil {
		t.Fatalf("rejected plan returned successful Result: %#v", result)
	}
	var runErr *adaptor.RunError
	if !errors.As(err, &runErr) || runErr.Reason != adaptor.ReasonApprovalDenied || runErr.Result == nil {
		t.Fatalf("Run error = %#v, want approval-denied RunError with partial result", err)
	}
}

func TestClaudeInteractive_QuestionAnswered(t *testing.T) {
	requireInteractiveCLI(t)
	agent := newInteractiveAgent(t, envOr("CLAUDE_MODEL_P3", "claude-haiku-4-5"))
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	var mu sync.Mutex
	var requests []adaptor.ApprovalRequest
	result, err := agent.Run(ctx,
		"Call AskUserQuestion exactly once with one multiple-choice question: "+
			`"Which option should I use?" and exactly these option labels: "male", "female", "prefer not to say". `+
			"Do not use any other tools. After the user answers, reply with exactly `ANSWER=<chosen option>` and nothing else.",
		interactivePolicy(adaptor.ApprovalAutoApprove, adaptor.QuestionAsk),
		adaptor.OnApproval(func(ctx context.Context, req *adaptor.ApprovalRequest) error {
			mu.Lock()
			requests = append(requests, *req)
			mu.Unlock()
			return req.Answer(ctx, "male")
		}),
	)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	mu.Lock()
	calls := append([]adaptor.ApprovalRequest(nil), requests...)
	mu.Unlock()
	if len(calls) == 0 || calls[0].Kind != adaptor.ApprovalQuestion {
		t.Fatalf("question approval handler calls = %#v", calls)
	}
	if !strings.Contains(strings.ToLower(result.Text), "answer=male") {
		t.Fatalf("final Text must reflect the chosen answer, got %q", result.Text)
	}
}

func TestClaudeInteractive_PermissionAsk(t *testing.T) {
	requireInteractiveCLI(t)
	cfg := alignmentLiveConfig(t, "")
	// A harmless echo is normally allowed without a can_use_tool request.
	// Establish a real provider permission boundary in this private profile.
	// Disable sandbox auto-allow so it cannot bypass the explicit ask rule.
	for _, binding := range cfg.Env {
		if binding.Name != "CLAUDE_CONFIG_DIR" {
			continue
		}
		if err := os.MkdirAll(binding.Value, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(binding.Value, "settings.json"), []byte(`{"permissions":{"ask":["Bash(echo permission-ok)"]},"sandbox":{"autoAllowBashIfSandboxed":false}}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	agent := adaptor.New(claude.Driver(cfg))
	t.Cleanup(func() { _ = agent.Close(context.Background()) })
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	var seen adaptor.ApprovalKind
	var approvals int
	var approvedToolID string
	stream := agent.Stream(ctx,
		"Use the Bash tool exactly once to run `echo permission-ok`. Then reply with exactly PERMISSION_OK and nothing else.",
		adaptor.WithPolicy(adaptor.Policy{Approvals: adaptor.ApprovalPolicy{
			Permission: adaptor.ApprovalAsk,
			PlanReview: adaptor.ApprovalAutoApprove,
		}}),
		adaptor.OnApproval(func(ctx context.Context, req *adaptor.ApprovalRequest) error {
			seen = req.Kind
			approvals++
			approvedToolID = req.ToolCallID
			t.Logf("permission callback kind=%s request=%s tool=%s", req.Kind, req.ID, req.ToolCallID)
			if err := req.Approve(ctx); err != nil {
				return err
			}
			if err := req.Approve(ctx); !errors.Is(err, adaptor.ErrApprovalResolved) {
				return fmt.Errorf("duplicate permission response = %v, want ErrApprovalResolved", err)
			}
			return nil
		}),
	)
	for event := range stream.Events() {
		if notice, ok := event.(adaptor.Notice); ok && notice.Kind == adaptor.NoticeInvocation {
			t.Logf("permission invocation args=%v", notice.Data["args"])
		}
	}
	result, err := stream.Result()
	if result != nil {
		encoded, marshalErr := json.Marshal(struct {
			Raw        any `json:"raw"`
			Transcript any `json:"transcript"`
		}{result.Raw(), result.Transcript()})
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		t.Logf("permission protocol=%s", encoded)
	}
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if seen != adaptor.ApprovalPermission {
		t.Fatalf("approval kind = %q, want permission", seen)
	}
	if approvals != 1 {
		t.Fatalf("permission callbacks = %d, want exactly one", approvals)
	}
	assertLivePermissionProtocol(t, result, approvedToolID)
	if !strings.Contains(result.Text, "PERMISSION_OK") {
		t.Fatalf("final Text = %q, want permission result", result.Text)
	}
}

func newInteractiveAgent(t *testing.T, model string) *adaptor.Agent {
	t.Helper()
	a := adaptor.New(claude.Driver(alignmentLiveConfig(t, model)))
	t.Cleanup(func() { _ = a.Close(context.Background()) })
	return a
}

func interactivePolicy(plan adaptor.ApprovalMode, question adaptor.QuestionMode) adaptor.SharedOption {
	return adaptor.WithPolicy(adaptor.Policy{Approvals: adaptor.ApprovalPolicy{
		Permission: adaptor.ApprovalAutoApprove,
		PlanReview: plan,
		Question:   question,
		OnReject:   adaptor.FallbackAbort,
	}})
}

func requireInteractiveCLI(t *testing.T) {
	t.Helper()
	if os.Getenv("AGENT_ADAPTOR_LIVE_CONFORMANCE") != "1" {
		t.Skip("set AGENT_ADAPTOR_LIVE_CONFORMANCE=1 in addition to -tags claude_live")
	}
	cmd := claudeCLIName()
	if _, err := exec.LookPath(cmd); err != nil {
		t.Fatalf("authorized live run requires %s in PATH", cmd)
	}
	helpCommand := exec.Command(cmd, "--help")
	helpCommand.Env = os.Environ()
	for _, binding := range alignmentLiveConfig(t, "").Env {
		helpCommand.Env = append(helpCommand.Env, binding.Name+"="+binding.Value)
	}
	help, err := helpCommand.CombinedOutput()
	if err != nil {
		t.Fatalf("%s --help failed: %v", cmd, err)
	}
	if !strings.Contains(string(help), "--replay-user-messages") {
		t.Fatalf("%s does not support --replay-user-messages", cmd)
	}
}

func claudeCLIName() string {
	if value := strings.TrimSpace(os.Getenv("CLAUDE_CLI")); value != "" {
		return value
	}
	if _, err := exec.LookPath("trpc-claudecode"); err == nil {
		return "trpc-claudecode"
	}
	return "claude"
}

func envOr(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}
