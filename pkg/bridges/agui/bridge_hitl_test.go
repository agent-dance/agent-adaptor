package agui_test

import (
	"encoding/json"
	"testing"

	aguievents "github.com/ag-ui-protocol/ag-ui/sdks/community/go/pkg/core/events"

	agentadaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/pkg/bridges/agui"
)

// TestDecisionAsToolCall_HappyPath ensures HITL requested/resolved pairs are
// mapped onto AG-UI tool-call events under the default DecisionAsToolCall
// mode.
func TestDecisionAsToolCall_HappyPath(t *testing.T) {
	tr := agui.NewTranslator()

	events := translateAll(tr, []agentadaptor.StreamPayload{
		{Kind: agentadaptor.StreamRunStarted, RunID: "r", ThreadID: "t"},
		{Kind: agentadaptor.StreamHITLRequested, RunID: "r", ThreadID: "t",
			HITLRequested: &agentadaptor.HITLRequestedPayload{
				RequestID: "run-r-dec-1",
				Kind:      agentadaptor.HumanDecisionPlanReview,
				Source:    "claude.exit_plan_mode",
				Prompt:    "Approve the plan?",
				Payload:   map[string]any{"plan": "Edit AGENTS.md"},
			}},
		{Kind: agentadaptor.StreamHITLResolved, RunID: "r", ThreadID: "t",
			HITLResolved: &agentadaptor.HITLResolvedPayload{
				RequestID: "run-r-dec-1",
				Kind:      agentadaptor.HumanDecisionPlanReview,
				Source:    "claude.exit_plan_mode",
				Result:    agentadaptor.DecisionApproved,
			}},
		{Kind: agentadaptor.StreamRunFinished, RunID: "r", ThreadID: "t"},
	})

	var (
		toolCallID string
		toolName   string
		argsJSON   string
		resultJSON string
		sawEnd     bool
	)
	for _, ev := range events {
		switch tc := ev.(type) {
		case *aguievents.ToolCallStartEvent:
			toolCallID = tc.ToolCallID
			toolName = tc.ToolCallName
		case *aguievents.ToolCallArgsEvent:
			argsJSON = tc.Delta
		case *aguievents.ToolCallEndEvent:
			sawEnd = true
		case *aguievents.ToolCallResultEvent:
			resultJSON = tc.Content
		}
	}

	if toolCallID != "dec-run-r-dec-1" {
		t.Errorf("toolCallID: got %q want dec-run-r-dec-1", toolCallID)
	}
	if toolName != "dec.plan_review.claude.exit_plan_mode" {
		t.Errorf("toolName: got %q", toolName)
	}
	if !sawEnd {
		t.Error("ToolCallEnd missing")
	}

	var args map[string]any
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		t.Fatalf("args json: %v (raw=%q)", err, argsJSON)
	}
	if args["kind"] != "plan_review" {
		t.Errorf("args kind: %v", args["kind"])
	}
	payload, _ := args["payload"].(map[string]any)
	if payload["plan"] != "Edit AGENTS.md" {
		t.Errorf("args payload.plan: %v", payload["plan"])
	}

	var res map[string]any
	if err := json.Unmarshal([]byte(resultJSON), &res); err != nil {
		t.Fatalf("result json: %v (raw=%q)", err, resultJSON)
	}
	if res["result"] != "approved" {
		t.Errorf("result.result: %v", res["result"])
	}
}

// TestDecisionAsCustom_LegacyMappingRetained verifies DecisionAsCustom
// reverts to the legacy CustomEvent mapping.
func TestDecisionAsCustom_LegacyMappingRetained(t *testing.T) {
	tr := agui.NewTranslator(agui.WithDecisionMode(agui.DecisionAsCustom))
	events := translateAll(tr, []agentadaptor.StreamPayload{
		{Kind: agentadaptor.StreamRunStarted, RunID: "r", ThreadID: "t"},
		{Kind: agentadaptor.StreamHITLRequested, RunID: "r", ThreadID: "t",
			HITLRequested: &agentadaptor.HITLRequestedPayload{RequestID: "x", Kind: agentadaptor.HumanDecisionPermission}},
		{Kind: agentadaptor.StreamRunFinished, RunID: "r", ThreadID: "t"},
	})
	var sawCustom bool
	for _, ev := range events {
		if _, ok := ev.(*aguievents.CustomEvent); ok {
			sawCustom = true
		}
	}
	if !sawCustom {
		t.Fatal("DecisionAsCustom must produce CustomEvent for HITL")
	}
}

// TestDecisionAsToolCall_RetryUsesDistinctToolCallIDs ensures each retry
// attempt receives a fresh ToolCallID — front-end useCopilotAction sees a
// new tool call and renders approval UI again.
func TestDecisionAsToolCall_RetryUsesDistinctToolCallIDs(t *testing.T) {
	tr := agui.NewTranslator()
	events := translateAll(tr, []agentadaptor.StreamPayload{
		{Kind: agentadaptor.StreamRunStarted, RunID: "r"},
		{Kind: agentadaptor.StreamHITLRequested, RunID: "r",
			HITLRequested: &agentadaptor.HITLRequestedPayload{RequestID: "attempt-1", Kind: agentadaptor.HumanDecisionPlanReview, RetryAttempt: 0}},
		{Kind: agentadaptor.StreamHITLResolved, RunID: "r",
			HITLResolved: &agentadaptor.HITLResolvedPayload{RequestID: "attempt-1", Kind: agentadaptor.HumanDecisionPlanReview, Result: agentadaptor.DecisionRejected, RetryAttempt: 0}},
		{Kind: agentadaptor.StreamHITLRequested, RunID: "r",
			HITLRequested: &agentadaptor.HITLRequestedPayload{RequestID: "attempt-2", Kind: agentadaptor.HumanDecisionPlanReview, RetryAttempt: 1}},
		{Kind: agentadaptor.StreamHITLResolved, RunID: "r",
			HITLResolved: &agentadaptor.HITLResolvedPayload{RequestID: "attempt-2", Kind: agentadaptor.HumanDecisionPlanReview, Result: agentadaptor.DecisionApproved, RetryAttempt: 1}},
		{Kind: agentadaptor.StreamRunFinished, RunID: "r"},
	})

	ids := map[string]bool{}
	for _, ev := range events {
		if tc, ok := ev.(*aguievents.ToolCallStartEvent); ok {
			ids[tc.ToolCallID] = true
		}
	}
	if len(ids) != 2 {
		t.Fatalf("expected two distinct ToolCallIDs across retry attempts, got %v", ids)
	}
}
