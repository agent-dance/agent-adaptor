//go:build claude_live

package claude_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agent-dance/agent-adaptor/claude"
	adaptor "github.com/agent-dance/agent-adaptor/next"
)

// Run with:
//
//	go test -tags claude_live -run TestClaudePhase3 -v ./claude
//
// Requires a Claude-compatible CLI with interactive stream-json support.
func TestClaudePhase3_PlanApproved(t *testing.T) {
	requirePhase3CLI(t)
	agent := newPhase3Agent(t, envOr("CLAUDE_MODEL_P3", "claude-haiku-4-5"))
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	var mu sync.Mutex
	var requests []adaptor.ApprovalRequest
	result, err := agent.Run(ctx,
		"Enter plan mode, design a two-step plan for refactoring the file `main.go` (do not actually edit). "+
			"Call ExitPlanMode with the plan. Do not use any other tools.",
		phase3Policy(adaptor.ApprovalAsk, adaptor.QuestionAutoDeny),
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

func TestClaudePhase3_PlanRejected(t *testing.T) {
	requirePhase3CLI(t)
	agent := newPhase3Agent(t, envOr("CLAUDE_MODEL_P3", "claude-haiku-4-5"))
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	result, err := agent.Run(ctx,
		"Enter plan mode, design a two-step plan for refactoring the file `main.go` (do not actually edit). "+
			"Call ExitPlanMode with the plan. Do not ask questions or use other tools.",
		phase3Policy(adaptor.ApprovalAsk, adaptor.QuestionAutoDeny),
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

func TestClaudePhase3_QuestionAnswered(t *testing.T) {
	requirePhase3CLI(t)
	agent := newPhase3Agent(t, envOr("CLAUDE_MODEL_P3", "claude-haiku-4-5"))
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	var mu sync.Mutex
	var requests []adaptor.ApprovalRequest
	result, err := agent.Run(ctx,
		"Call AskUserQuestion exactly once with one multiple-choice question: "+
			`"Which option should I use?" and exactly these option labels: "male", "female", "prefer not to say". `+
			"Do not use any other tools. After the user answers, reply with exactly `ANSWER=<chosen option>` and nothing else.",
		phase3Policy(adaptor.ApprovalAutoApprove, adaptor.QuestionAsk),
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

func TestClaudePhase3_PermissionAskRejected(t *testing.T) {
	requirePhase3CLI(t)
	agent := newPhase3Agent(t, "")
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	_, err := agent.Run(ctx, "hi", adaptor.WithPolicy(adaptor.Policy{Approvals: adaptor.ApprovalPolicy{
		Permission: adaptor.ApprovalAsk,
		PlanReview: adaptor.ApprovalAsk,
	}}))
	if err == nil || !strings.Contains(err.Error(), "Permission=Ask") {
		t.Fatalf("error = %v, want Permission=Ask policy rejection", err)
	}
}

func newPhase3Agent(t *testing.T, model string) *adaptor.Agent {
	t.Helper()
	return adaptor.New(claude.Driver(claude.Config{
		CommonConfig: claude.CommonConfig{CWD: t.TempDir(), Command: claudeCLIName()},
		Model:        model,
	}))
}

func phase3Policy(plan adaptor.ApprovalMode, question adaptor.QuestionMode) adaptor.SharedOption {
	return adaptor.WithPolicy(adaptor.Policy{Approvals: adaptor.ApprovalPolicy{
		Permission: adaptor.ApprovalAutoApprove,
		PlanReview: plan,
		Question:   question,
		OnReject:   adaptor.FallbackAbort,
	}})
}

func requirePhase3CLI(t *testing.T) {
	t.Helper()
	if os.Getenv("AGENT_ADAPTOR_LIVE_CONFORMANCE") != "1" {
		t.Skip("set AGENT_ADAPTOR_LIVE_CONFORMANCE=1 in addition to -tags claude_live")
	}
	cmd := claudeCLIName()
	if _, err := exec.LookPath(cmd); err != nil {
		t.Skipf("%s not in PATH", cmd)
	}
	help, err := exec.Command(cmd, "--help").CombinedOutput()
	if err != nil {
		t.Skipf("%s --help failed: %v", cmd, err)
	}
	if !strings.Contains(string(help), "--replay-user-messages") {
		t.Skipf("%s does not support --replay-user-messages", cmd)
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
