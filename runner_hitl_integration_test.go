package agentadaptor_test

import (
	"context"
	"errors"
	"testing"
	"time"

	agentadaptor "github.com/agent-dance/agent-adaptor"
)

// hitlMockDriver is a minimal DriverAdapter used to exercise the runner's HITL
// v2 dispatcher end-to-end. It is independent from claude/codex/cursor so
// tests can vary capabilities and simulated tool_use frames freely.
type hitlMockDriver struct {
	caps        agentadaptor.RunPolicyCapabilities
	requestKind agentadaptor.HumanDecisionKind
	// result captures what RequestDecision returned (for assertions).
	lastResp *agentadaptor.DecisionResponse
	lastErr  error
}

func (d *hitlMockDriver) Descriptor() agentadaptor.DriverDescriptor {
	return agentadaptor.DriverDescriptor{Type: "hitl-mock", DisplayName: "HITL Mock", RunPolicyCaps: d.caps}
}
func (d *hitlMockDriver) ValidateConfig(any) error { return nil }
func (d *hitlMockDriver) Run(ctx context.Context, req agentadaptor.DriverRunRequest, sink agentadaptor.EventSink) (agentadaptor.DriverRunResult, error) {
	if d.requestKind != "" {
		if ic, ok := sink.(agentadaptor.DecisionCapableSink); ok {
			resp, err := ic.RequestDecision(ctx, agentadaptor.DecisionRequest{
				Kind:   d.requestKind,
				Source: "hitl-mock." + string(d.requestKind),
				Prompt: "do it?",
				Payload: map[string]any{
					"tool": "bash",
					"plan": "echo hi",
					"schema": map[string]any{"type": "object"},
				},
			})
			d.lastResp = &resp
			d.lastErr = err
			if err != nil {
				return agentadaptor.DriverRunResult{ExitCode: 1}, nil
			}
		}
	}
	return agentadaptor.DriverRunResult{
		Output:   "ok",
		ExitCode: 0,
		Summary:  "done",
	}, nil
}

func fullCaps() agentadaptor.RunPolicyCapabilities {
	return agentadaptor.RunPolicyCapabilities{
		Permission: agentadaptor.HumanDecisionSupport{Ask: true, AutoApprove: true, AutoReject: true, Retry: true},
		PlanReview: agentadaptor.HumanDecisionSupport{Ask: true, AutoApprove: true, AutoReject: true, Retry: true},
		Question:   agentadaptor.QuestionSupport{Ask: true, AutoReject: true, Retry: true},
	}
}

func TestRunner_PermissionHandlerDispatchesApproved(t *testing.T) {
	drv := &hitlMockDriver{caps: fullCaps(), requestKind: agentadaptor.HumanDecisionPermission}
	sdk := agentadaptor.New(agentadaptor.WithDefaultAgent(agentadaptor.Bind(drv, struct{}{})))

	result, err := sdk.Run(context.Background(), "hi",
		agentadaptor.WithRunPolicy(agentadaptor.RunPolicy{
			HumanDecision: agentadaptor.HumanDecisionPolicy{Permission: agentadaptor.HumanDecisionAsk},
		}),
		agentadaptor.WithPermissionHandler(func(_ context.Context, req agentadaptor.PermissionRequest) (agentadaptor.PermissionResponse, error) {
			if req.Tool != "bash" {
				t.Errorf("Tool unset: %+v", req)
			}
			return agentadaptor.PermissionResponse{Result: agentadaptor.ApprovalApproved}, nil
		}),
	)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.Failure != nil {
		t.Errorf("unexpected failure: %+v", result.Failure)
	}
	if drv.lastResp == nil || drv.lastResp.Result != agentadaptor.DecisionApproved {
		t.Errorf("adapter-visible result: %+v", drv.lastResp)
	}
}

func TestRunner_RejectAbortSurfacesFailureOnResult(t *testing.T) {
	drv := &hitlMockDriver{caps: fullCaps(), requestKind: agentadaptor.HumanDecisionPlanReview}
	sdk := agentadaptor.New(agentadaptor.WithDefaultAgent(agentadaptor.Bind(drv, struct{}{})))

	result, _ := sdk.Run(context.Background(), "hi",
		agentadaptor.WithRunPolicy(agentadaptor.RunPolicy{
			HumanDecision: agentadaptor.HumanDecisionPolicy{PlanReview: agentadaptor.HumanDecisionAsk, OnReject: agentadaptor.FailureAbort},
		}),
		agentadaptor.WithPlanReviewHandler(func(_ context.Context, _ agentadaptor.PlanReviewRequest) (agentadaptor.PlanReviewResponse, error) {
			return agentadaptor.PlanReviewResponse{Result: agentadaptor.ApprovalRejected}, nil
		}),
	)
	if result.Failure == nil || result.Failure.Code != agentadaptor.FailureReject {
		t.Fatalf("expected FailureReject, got %+v", result.Failure)
	}
	if !result.Failure.IsHumanDecision() || !result.Failure.IsRejected() {
		t.Error("helper classification failed")
	}
	if result.Failure.HumanDecision == nil || result.Failure.HumanDecision.Kind != agentadaptor.HumanDecisionPlanReview {
		t.Errorf("HumanDecision: %+v", result.Failure.HumanDecision)
	}
}

func TestRunner_CapabilityRejectsUnsupportedAsk(t *testing.T) {
	drv := &hitlMockDriver{
		caps: agentadaptor.RunPolicyCapabilities{
			Permission: agentadaptor.HumanDecisionSupport{Ask: false, AutoApprove: true, AutoReject: false},
		},
	}
	sdk := agentadaptor.New(agentadaptor.WithDefaultAgent(agentadaptor.Bind(drv, struct{}{})))

	_, err := sdk.Run(context.Background(), "hi",
		agentadaptor.WithRunPolicy(agentadaptor.RunPolicy{
			HumanDecision: agentadaptor.HumanDecisionPolicy{Permission: agentadaptor.HumanDecisionAsk},
		}),
	)
	if !errors.Is(err, agentadaptor.ErrHumanDecisionModeUnsupported) {
		t.Fatalf("expected ErrHumanDecisionModeUnsupported, got %v", err)
	}
}

func TestRunner_ChannelDispatchWhenNoHandler(t *testing.T) {
	drv := &hitlMockDriver{caps: fullCaps(), requestKind: agentadaptor.HumanDecisionPermission}
	sdk := agentadaptor.New(agentadaptor.WithDefaultAgent(agentadaptor.Bind(drv, struct{}{})))

	handle, err := sdk.Start(context.Background(), "hi",
		agentadaptor.WithRunPolicy(agentadaptor.RunPolicy{
			HumanDecision: agentadaptor.HumanDecisionPolicy{Permission: agentadaptor.HumanDecisionAsk, Timeout: 5 * time.Second},
		}),
	)
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	go func() {
		req := <-handle.DecisionRequests()
		if req.Kind != agentadaptor.HumanDecisionPermission {
			t.Errorf("kind: %q", req.Kind)
		}
		if err := handle.ResolveDecision(req.RequestID, agentadaptor.DecisionResponse{Result: agentadaptor.DecisionApproved}); err != nil {
			t.Errorf("resolve: %v", err)
		}
	}()

	result, err := handle.Wait(context.Background())
	if err != nil {
		t.Fatalf("wait: %v", err)
	}
	if result.Failure != nil {
		t.Errorf("unexpected failure: %+v", result.Failure)
	}
}

// Default policy: empty HumanDecisionPolicy. Permission falls back to Ask and
// without handler / without channel consumer it times out → FailureAbort.
func TestRunner_DefaultPolicyTimesOutAndAborts(t *testing.T) {
	drv := &hitlMockDriver{caps: fullCaps(), requestKind: agentadaptor.HumanDecisionPermission}
	sdk := agentadaptor.New(agentadaptor.WithDefaultAgent(agentadaptor.Bind(drv, struct{}{})))

	result, err := sdk.Run(context.Background(), "hi",
		agentadaptor.WithRunPolicy(agentadaptor.RunPolicy{
			HumanDecision: agentadaptor.HumanDecisionPolicy{Timeout: 20 * time.Millisecond},
		}),
	)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.Failure == nil || result.Failure.Code != agentadaptor.FailureTimeout {
		t.Fatalf("expected FailureTimeout, got %+v", result.Failure)
	}
	if !result.Failure.IsTimedOut() {
		t.Error("IsTimedOut() helper did not classify")
	}
}

func TestRunner_PolicyMergeRejectsInvalid(t *testing.T) {
	drv := &hitlMockDriver{caps: fullCaps()}
	sdk := agentadaptor.New(agentadaptor.WithDefaultAgent(agentadaptor.Bind(drv, struct{}{})))

	_, err := sdk.Run(context.Background(), "hi",
		agentadaptor.WithRunPolicy(agentadaptor.RunPolicy{
			HumanDecision: agentadaptor.HumanDecisionPolicy{MaxRetries: -1},
		}),
	)
	if err == nil {
		t.Fatal("expected policy validation error")
	}
}

// TestRunner_PerKindSelectiveHandlerMount verifies §3.6: mounting only the
// PlanReview handler lets the Permission dispatch fall through to the
// channel while PlanReview runs through the typed handler.
func TestRunner_PerKindSelectiveHandlerMount(t *testing.T) {
	drv := &hitlMockDriver{caps: fullCaps(), requestKind: agentadaptor.HumanDecisionPlanReview}
	sdk := agentadaptor.New(agentadaptor.WithDefaultAgent(agentadaptor.Bind(drv, struct{}{})))

	called := false
	result, err := sdk.Run(context.Background(), "hi",
		agentadaptor.WithRunPolicy(agentadaptor.RunPolicy{
			HumanDecision: agentadaptor.HumanDecisionPolicy{PlanReview: agentadaptor.HumanDecisionAsk},
		}),
		agentadaptor.WithPlanReviewHandler(func(_ context.Context, _ agentadaptor.PlanReviewRequest) (agentadaptor.PlanReviewResponse, error) {
			called = true
			return agentadaptor.PlanReviewResponse{Result: agentadaptor.ApprovalApproved}, nil
		}),
	)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !called {
		t.Error("PlanReview handler not invoked")
	}
	if result.Failure != nil {
		t.Fatalf("unexpected failure: %+v", result.Failure)
	}
}
