package hitlmock_test

import (
	"context"
	"strings"
	"testing"
	"time"

	agentadaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/examples/streaming-chat-copilotkit/hitlmock"
)

// TestMockDriverPlanApproved exercises the happy-path plan review flow:
// the driver emits a PlanReview DecisionRequest, a typed handler approves,
// and the final DriverRunResult reports success.
func TestMockDriverPlanApproved(t *testing.T) {
	sdk := agentadaptor.New(
		agentadaptor.WithDefaultAgent(hitlmock.New(hitlmock.Config{})),
	)

	var sawRequest bool
	planReview := func(ctx context.Context, req agentadaptor.PlanReviewRequest) (agentadaptor.PlanReviewResponse, error) {
		sawRequest = true
		if !strings.Contains(req.Plan, "Audit AGENTS.md") {
			t.Errorf("plan text missing: %q", req.Plan)
		}
		return agentadaptor.PlanReviewResponse{Result: agentadaptor.ApprovalApproved}, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	result, err := sdk.Run(ctx, "Plan a migration",
		agentadaptor.WithRunPolicy(agentadaptor.RunPolicy{
			HumanDecision: agentadaptor.HumanDecisionPolicy{PlanReview: agentadaptor.HumanDecisionAsk},
		}),
		agentadaptor.WithPlanReviewHandler(planReview),
	)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !sawRequest {
		t.Fatal("plan review handler was never invoked")
	}
	if result.Failure != nil {
		t.Fatalf("unexpected failure: %+v", result.Failure)
	}
	if !strings.Contains(result.Summary, "approved") {
		t.Errorf("summary missing approval wording: %q", result.Summary)
	}
}

// TestMockDriverPermissionDenied ensures rejecting a Permission decision
// produces a RunFailure with the right attribution.
func TestMockDriverPermissionDenied(t *testing.T) {
	sdk := agentadaptor.New(
		agentadaptor.WithDefaultAgent(hitlmock.New(hitlmock.Config{})),
	)

	handler := func(_ context.Context, req agentadaptor.PermissionRequest) (agentadaptor.PermissionResponse, error) {
		if req.Tool != "Bash" {
			t.Errorf("tool: got %q want Bash", req.Tool)
		}
		return agentadaptor.PermissionResponse{Result: agentadaptor.ApprovalRejected}, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	result, _ := sdk.Run(ctx, "Please run this bash command",
		agentadaptor.WithRunPolicy(agentadaptor.RunPolicy{
			HumanDecision: agentadaptor.HumanDecisionPolicy{
				Permission: agentadaptor.HumanDecisionAsk,
				OnReject:   agentadaptor.FailureAbort,
			},
		}),
		agentadaptor.WithPermissionHandler(handler),
	)
	if result.Failure == nil || result.Failure.Code != agentadaptor.FailureReject {
		t.Fatalf("expected FailureReject, got %+v", result.Failure)
	}
	if !result.Failure.IsHumanDecision() {
		t.Error("IsHumanDecision should be true")
	}
	if result.Failure.HumanDecision == nil || result.Failure.HumanDecision.Kind != agentadaptor.HumanDecisionPermission {
		t.Errorf("HumanDecision attribution: %+v", result.Failure.HumanDecision)
	}
}

// TestMockDriverQuestionAnswered covers the Question scenario with a typed
// handler returning structured answer.
func TestMockDriverQuestionAnswered(t *testing.T) {
	sdk := agentadaptor.New(
		agentadaptor.WithDefaultAgent(hitlmock.New(hitlmock.Config{})),
	)

	handler := func(_ context.Context, req agentadaptor.QuestionRequest) (agentadaptor.QuestionResponse, error) {
		if len(req.Choices) == 0 {
			t.Error("choices missing")
		}
		return agentadaptor.QuestionResponse{
			Result: agentadaptor.QuestionAnswered,
			Choice: "docs",
			Answer: map[string]any{"directory": "docs"},
		}, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	result, err := sdk.Run(ctx, "Ask me a question before you proceed",
		agentadaptor.WithRunPolicy(agentadaptor.RunPolicy{
			HumanDecision: agentadaptor.HumanDecisionPolicy{Question: agentadaptor.QuestionAsk},
		}),
		agentadaptor.WithQuestionHandler(handler),
	)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.Failure != nil {
		t.Fatalf("unexpected failure: %+v", result.Failure)
	}
	if !strings.Contains(strings.ToLower(result.Summary), "docs") {
		t.Errorf("expected summary to mention 'docs', got %q", result.Summary)
	}
}
