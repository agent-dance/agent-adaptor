//go:build claude_live

package claude_test

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	agentadaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/claude"
	"github.com/agent-dance/agent-adaptor/memory"
)

// TestClaudePhase3_PlanApproved exercises the full interactive HITL loop
// against the real CLI: sdk.Start with PlanReview=Ask → parser intercepts
// ExitPlanMode → PlanReviewHandler approves → parser injects approve
// tool_result into stdin → CLI continues and emits final result.
//
// Run with:
//
//	go test -tags claude_live -run TestClaudePhase3 -v ./claude
//
// Requires `claude` or `trpc-claudecode` in PATH and the CLI must support
// --input-format stream-json + --replay-user-messages (trpc-claudecode
// 2.1.112+).
func TestClaudePhase3_PlanApproved(t *testing.T) {
	requirePhase3CLI(t)

	cmd := claudeCLIName()
	cwd := t.TempDir()

	sdk := agentadaptor.New(
		agentadaptor.WithDefaultAgent(claude.New(agentadaptor.ClaudeConfig{
			CommonConfig: agentadaptor.CommonConfig{CWD: cwd, Command: cmd},
			Model:        envOr("CLAUDE_MODEL_P3", "claude-haiku-4-5"),
		})),
		agentadaptor.WithSessionStore(memory.NewSessionStore()),
	)

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	var (
		handlerCallsMu sync.Mutex
		handlerCalls   []agentadaptor.PlanReviewRequest
	)

	result, err := sdk.Run(ctx,
		"Enter plan mode, design a two-step plan for refactoring the file `main.go` (do not actually edit). "+
			"Call ExitPlanMode with the plan. Do not use any other tools.",
		agentadaptor.WithRunPolicy(agentadaptor.RunPolicy{
			HumanDecision: agentadaptor.HumanDecisionPolicy{
				Permission: agentadaptor.HumanDecisionAutoApprove, // Phase 3 requires this
				PlanReview: agentadaptor.HumanDecisionAsk,
				Question:   agentadaptor.QuestionAutoReject,
			},
		}),
		agentadaptor.WithPlanReviewHandler(func(_ context.Context, req agentadaptor.PlanReviewRequest) (agentadaptor.PlanReviewResponse, error) {
			handlerCallsMu.Lock()
			handlerCalls = append(handlerCalls, req)
			handlerCallsMu.Unlock()
			return agentadaptor.PlanReviewResponse{Result: agentadaptor.ApprovalApproved}, nil
		}),
		agentadaptor.WithSessionKey("claude_phase3", "plan-approve"),
	)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	handlerCallsMu.Lock()
	calls := append([]agentadaptor.PlanReviewRequest(nil), handlerCalls...)
	handlerCallsMu.Unlock()

	if len(calls) == 0 {
		t.Fatalf("PlanReview handler never invoked; model likely did not call ExitPlanMode — summary=%q output=%q",
			result.Summary, result.Output)
	}
	if calls[0].Plan == "" {
		t.Errorf("PlanReview request missing Plan text: %+v", calls[0])
	}
	if result.Failure != nil {
		t.Errorf("approved plan should not produce a failure: %+v", result.Failure)
	}
	// After approval the CLI must have continued — the final result must
	// exist and not be a tool_use stop.
	if result.Output == "" {
		t.Error("final Output missing after approval")
	}
}

// TestClaudePhase3_PlanRejected verifies the OnReject=FailureAbort path:
// user rejects the plan card, adapter records a structured RunFailure, and
// the CLI is told the plan was rejected (so it can acknowledge).
func TestClaudePhase3_PlanRejected(t *testing.T) {
	requirePhase3CLI(t)

	cmd := claudeCLIName()
	cwd := t.TempDir()

	sdk := agentadaptor.New(
		agentadaptor.WithDefaultAgent(claude.New(agentadaptor.ClaudeConfig{
			CommonConfig: agentadaptor.CommonConfig{CWD: cwd, Command: cmd},
			Model:        envOr("CLAUDE_MODEL_P3", "claude-haiku-4-5"),
		})),
		agentadaptor.WithSessionStore(memory.NewSessionStore()),
	)

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	result, err := sdk.Run(ctx,
		"Enter plan mode, design a two-step plan for refactoring the file `main.go` (do not actually edit). "+
			"Call ExitPlanMode with the plan. Do not ask any questions. Do not use any other tools.",
		agentadaptor.WithRunPolicy(agentadaptor.RunPolicy{
			HumanDecision: agentadaptor.HumanDecisionPolicy{
				Permission: agentadaptor.HumanDecisionAutoApprove,
				PlanReview: agentadaptor.HumanDecisionAsk,
				Question:   agentadaptor.QuestionAutoReject,
				OnReject:   agentadaptor.FailureAbort,
			},
		}),
		agentadaptor.WithPlanReviewHandler(func(_ context.Context, _ agentadaptor.PlanReviewRequest) (agentadaptor.PlanReviewResponse, error) {
			return agentadaptor.PlanReviewResponse{Result: agentadaptor.ApprovalRejected}, nil
		}),
		agentadaptor.WithSessionKey("claude_phase3", "plan-reject"),
	)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if result.Failure == nil {
		t.Fatalf("rejected plan must set Failure; got Output=%q Summary=%q", result.Output, result.Summary)
	}
	if result.Failure.Code != agentadaptor.FailureReject {
		t.Errorf("Failure.Code: %q want decision_rejected", result.Failure.Code)
	}
	if !result.Failure.IsHumanDecision() || !result.Failure.IsRejected() {
		t.Errorf("Failure helpers: %+v", result.Failure)
	}
	if result.Failure.HumanDecision == nil || result.Failure.HumanDecision.Kind != agentadaptor.HumanDecisionPlanReview {
		t.Errorf("HumanDecision attribution: %+v", result.Failure.HumanDecision)
	}
}

// TestClaudePhase3_QuestionAnswered verifies the real AskUserQuestion flow:
// the model must pause for a structured question, the host answers it, and
// Claude's final output must reflect that concrete answer rather than falling
// back to "no choice" semantics.
func TestClaudePhase3_QuestionAnswered(t *testing.T) {
	requirePhase3CLI(t)

	cmd := claudeCLIName()
	cwd := t.TempDir()

	sdk := agentadaptor.New(
		agentadaptor.WithDefaultAgent(claude.New(agentadaptor.ClaudeConfig{
			CommonConfig: agentadaptor.CommonConfig{CWD: cwd, Command: cmd},
			Model:        envOr("CLAUDE_MODEL_P3", "claude-haiku-4-5"),
		})),
		agentadaptor.WithSessionStore(memory.NewSessionStore()),
	)

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	var (
		handlerCallsMu sync.Mutex
		handlerCalls   []agentadaptor.QuestionRequest
	)

	result, err := sdk.Run(ctx,
		"Call AskUserQuestion exactly once with one multiple-choice question: "+
			`"Which option should I use?" and exactly these option labels: "male", "female", "prefer not to say". `+
			"Do not use any other tools. After the user answers, reply with exactly `ANSWER=<chosen option>` and nothing else.",
		agentadaptor.WithRunPolicy(agentadaptor.RunPolicy{
			HumanDecision: agentadaptor.HumanDecisionPolicy{
				Permission: agentadaptor.HumanDecisionAutoApprove,
				PlanReview: agentadaptor.HumanDecisionAutoApprove,
				Question:   agentadaptor.QuestionAsk,
			},
		}),
		agentadaptor.WithQuestionHandler(func(_ context.Context, req agentadaptor.QuestionRequest) (agentadaptor.QuestionResponse, error) {
			handlerCallsMu.Lock()
			handlerCalls = append(handlerCalls, req)
			handlerCallsMu.Unlock()
			return agentadaptor.QuestionResponse{
				Result: agentadaptor.QuestionAnswered,
				Choice: "male",
				Text:   "male",
				Answer: map[string]any{
					"text": "male",
				},
			}, nil
		}),
		agentadaptor.WithSessionKey("claude_phase3", "question-answered"),
	)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	handlerCallsMu.Lock()
	calls := append([]agentadaptor.QuestionRequest(nil), handlerCalls...)
	handlerCallsMu.Unlock()

	if len(calls) == 0 {
		t.Fatalf("Question handler never invoked; output=%q summary=%q", result.Output, result.Summary)
	}
	if result.Failure != nil {
		t.Fatalf("answered question should not produce Failure: %+v", result.Failure)
	}
	if !strings.Contains(strings.ToLower(result.Output), "answer=male") {
		t.Fatalf("final output must reflect the chosen answer, got %q", result.Output)
	}
}

// TestClaudePhase3_PermissionAskRejected verifies the policy guard: Phase 3
// MUST reject Permission=Ask at Start time (no host-side executor).
func TestClaudePhase3_PermissionAskRejected(t *testing.T) {
	requirePhase3CLI(t)

	cmd := claudeCLIName()
	cwd := t.TempDir()

	sdk := agentadaptor.New(
		agentadaptor.WithDefaultAgent(claude.New(agentadaptor.ClaudeConfig{
			CommonConfig: agentadaptor.CommonConfig{CWD: cwd, Command: cmd},
		})),
	)

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	_, err := sdk.Run(ctx, "hi",
		agentadaptor.WithRunPolicy(agentadaptor.RunPolicy{
			HumanDecision: agentadaptor.HumanDecisionPolicy{
				Permission: agentadaptor.HumanDecisionAsk,
				PlanReview: agentadaptor.HumanDecisionAsk,
			},
		}),
	)
	if err == nil {
		t.Fatal("expected Phase 3 to reject Permission=Ask policy")
	}
	if !strings.Contains(err.Error(), "Phase 3") || !strings.Contains(err.Error(), "Permission=Ask") {
		t.Errorf("error should mention Phase 3 + Permission=Ask, got %v", err)
	}
}

// -----------------------------------------------------------------------------
// test helpers
// -----------------------------------------------------------------------------

// requirePhase3CLI asserts a suitable claude CLI is in PATH and supports
// --replay-user-messages (the flag that gates Phase 3). Skips with a
// message otherwise so CI without the CLI still passes.
func requirePhase3CLI(t *testing.T) {
	t.Helper()
	cmd := claudeCLIName()
	if _, err := exec.LookPath(cmd); err != nil {
		t.Skipf("%s not in PATH", cmd)
	}
	help, err := exec.Command(cmd, "--help").CombinedOutput()
	if err != nil {
		t.Skipf("%s --help failed: %v", cmd, err)
	}
	if !strings.Contains(string(help), "--replay-user-messages") {
		t.Skipf("%s does not support --replay-user-messages (Phase 3 requirement). Upgrade to trpc-claudecode 2.1.112+ or equivalent.", cmd)
	}
}

func claudeCLIName() string {
	if v := strings.TrimSpace(os.Getenv("CLAUDE_CLI")); v != "" {
		return v
	}
	if _, err := exec.LookPath("trpc-claudecode"); err == nil {
		return "trpc-claudecode"
	}
	return "claude"
}

func envOr(name, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(name)); v != "" {
		return v
	}
	return fallback
}
