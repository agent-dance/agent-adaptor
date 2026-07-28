package claude

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/agent-dance/agent-adaptor/adaptertest"
	agentadaptor "github.com/agent-dance/agent-adaptor/driver"
)

// TestHITLPlanApproved_EmitsRequestedAndResolved covers §8.4.7: the parser
// must emit paired StreamHITLRequested / StreamHITLResolved events when the
// CLI reports an approved plan, and must not mark the run as failed.
func TestHITLPlanApproved_EmitsRequestedAndResolved(t *testing.T) {
	data := mustReadFixture(t, "streaming-plan-approved.jsonl")

	sink := &streamSink{}
	p := newClaudeParser(sink)
	p.setHITLContext("run-plan-ok", agentadaptor.HumanDecisionPolicy{})
	p.enableStreaming("run-plan-ok")

	if err := p.onChunk("stdout", data, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	p.finalize()

	if p.pendingFailure != nil {
		t.Fatalf("approved plan should not produce a failure, got %+v", p.pendingFailure)
	}

	var requested, resolved int
	var resolvedDecision agentadaptor.DecisionResult
	for _, pl := range sink.snapshot() {
		switch pl.Kind {
		case agentadaptor.StreamHITLRequested:
			requested++
			if pl.HITLRequested == nil {
				t.Fatal("HITLRequested payload is nil")
			}
			if pl.HITLRequested.Kind != agentadaptor.HumanDecisionPlanReview {
				t.Errorf("expected plan_review kind, got %q", pl.HITLRequested.Kind)
			}
			if pl.HITLRequested.Source != "claude.exit_plan_mode" {
				t.Errorf("unexpected source: %q", pl.HITLRequested.Source)
			}
			if plan, _ := pl.HITLRequested.Payload["plan"].(string); plan == "" {
				t.Errorf("missing plan text in payload: %+v", pl.HITLRequested.Payload)
			}
		case agentadaptor.StreamHITLResolved:
			resolved++
			if pl.HITLResolved == nil {
				t.Fatal("HITLResolved payload is nil")
			}
			resolvedDecision = pl.HITLResolved.Result
		}
	}
	if requested != 1 || resolved != 1 {
		t.Fatalf("want 1 request + 1 resolved, got %d / %d", requested, resolved)
	}
	if resolvedDecision != agentadaptor.DecisionApproved {
		t.Errorf("want Approved, got %q", resolvedDecision)
	}
}

func TestObservationalPermissionRequestUsesOpaqueProviderEvent(t *testing.T) {
	data := mustReadFixture(t, "streaming-permission-agui.jsonl")
	sink := &streamSink{}
	p := newClaudeParser(sink)
	p.enableStreaming("run-observational-permission")
	if err := p.onChunk("stdout", data, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	p.finalize()
	p.completeStream(p.failureForOutcome(0, "", false), 0, "", false)

	payloads := sink.snapshot()
	if violations := adaptertest.VerifyStreamSequence(payloads); len(violations) != 0 {
		t.Fatalf("Claude permission fixture violates Driver stream contract: %v", violations)
	}
	foundOpaque := false
	for _, payload := range payloads {
		if payload.Kind == agentadaptor.StreamHITLRequested {
			t.Fatalf("observational permission_request emitted answerable HITL event: %+v", payload)
		}
		if payload.Kind == agentadaptor.StreamKind("claude.permission_request") {
			foundOpaque = true
			if payload.Raw == nil || payload.Raw["request_id"] != "pr-1" {
				t.Fatalf("opaque permission payload lost raw protocol: %+v", payload)
			}
		}
	}
	if !foundOpaque {
		t.Fatal("missing opaque claude.permission_request provider event")
	}
}

// TestHITLPlanRejected_SetsFailure covers §8.4.8: reject → RunFailure with
// structured HumanDecision attribution.
func TestHITLPlanRejected_SetsFailure(t *testing.T) {
	data := mustReadFixture(t, "streaming-plan-rejected.jsonl")

	sink := &streamSink{}
	p := newClaudeParser(sink)
	p.setHITLContext("run-plan-bad", agentadaptor.HumanDecisionPolicy{})
	p.enableStreaming("run-plan-bad")

	if err := p.onChunk("stdout", data, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	p.finalize()

	if p.pendingFailure == nil {
		t.Fatal("rejected plan must produce a failure")
	}
	if p.pendingFailure.Code != agentadaptor.FailureReject {
		t.Errorf("failure code: got %q want %q", p.pendingFailure.Code, agentadaptor.FailureReject)
	}
	if !p.pendingFailure.IsHumanDecision() || !p.pendingFailure.IsRejected() {
		t.Errorf("helper classification failed: %+v", p.pendingFailure)
	}
	hd := p.pendingFailure.HumanDecision
	if hd == nil {
		t.Fatal("HumanDecision attribution missing")
	}
	if hd.Kind != agentadaptor.HumanDecisionPlanReview {
		t.Errorf("HumanDecision.Kind: %q", hd.Kind)
	}
	if hd.Decision != agentadaptor.DecisionRejected {
		t.Errorf("HumanDecision.Decision: %q", hd.Decision)
	}
	if hd.Source != "claude.exit_plan_mode" {
		t.Errorf("HumanDecision.Source: %q", hd.Source)
	}
	if hd.Attempts != 1 {
		t.Errorf("HumanDecision.Attempts: got %d want 1", hd.Attempts)
	}
	if hd.Request == nil || hd.Request.Payload == nil {
		t.Fatal("HumanDecision.Request snapshot must be preserved")
	}

	// Verify paired streams still emit for observers.
	var requested, resolved int
	for _, pl := range sink.snapshot() {
		switch pl.Kind {
		case agentadaptor.StreamHITLRequested:
			requested++
		case agentadaptor.StreamHITLResolved:
			resolved++
			if pl.HITLResolved.Result != agentadaptor.DecisionRejected {
				t.Errorf("resolved.Result: %q", pl.HITLResolved.Result)
			}
		}
	}
	if requested != 1 || resolved != 1 {
		t.Fatalf("pair count: requested=%d resolved=%d", requested, resolved)
	}
}

// TestHITLPlanRejected_OnRejectContinueSuppressesFailure covers
// FailureContinue path: reject must emit streams but not produce a failure.
func TestHITLPlanRejected_OnRejectContinueSuppressesFailure(t *testing.T) {
	data := mustReadFixture(t, "streaming-plan-rejected.jsonl")

	sink := &streamSink{}
	p := newClaudeParser(sink)
	p.setHITLContext("run-plan-continue", agentadaptor.HumanDecisionPolicy{
		OnReject: agentadaptor.FailureContinue,
	})
	p.enableStreaming("run-plan-continue")

	if err := p.onChunk("stdout", data, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	p.finalize()

	if p.pendingFailure != nil {
		t.Fatalf("OnReject=Continue must not stash a failure, got %+v", p.pendingFailure)
	}
}

// TestHITLAskUserQuestion_PhaseOneEmitsObservabilityOnly validates that
// AskUserQuestion tool_use is recognised and its lifecycle broadcast.
func TestHITLAskUserQuestion_PhaseOneEmitsObservabilityOnly(t *testing.T) {
	data := mustReadFixture(t, "streaming-ask-user-question.jsonl")

	sink := &streamSink{}
	p := newClaudeParser(sink)
	p.setHITLContext("run-ask", agentadaptor.HumanDecisionPolicy{})
	p.enableStreaming("run-ask")
	if err := p.onChunk("stdout", data, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	p.finalize()

	var requested, resolved int
	for _, pl := range sink.snapshot() {
		switch pl.Kind {
		case agentadaptor.StreamHITLRequested:
			requested++
			if pl.HITLRequested.Kind != agentadaptor.HumanDecisionQuestion {
				t.Errorf("want question kind, got %q", pl.HITLRequested.Kind)
			}
			if len(pl.HITLRequested.Choices) != 2 {
				t.Errorf("want 2 choices, got %d", len(pl.HITLRequested.Choices))
			}
		case agentadaptor.StreamHITLResolved:
			resolved++
			if pl.HITLResolved.Result != agentadaptor.DecisionRejected {
				t.Errorf("want Rejected, got %q", pl.HITLResolved.Result)
			}
		}
	}
	if requested != 1 || resolved != 1 {
		t.Fatalf("want 1/1, got %d/%d", requested, resolved)
	}
}

// TestHITL_ToolUseValidationError_DoesNotEmitHITLEvents guards against the
// regression that mis-attributed CLI schema validation errors as human
// rejections. When Claude's CLI returns is_error:true + <tool_use_error>…
// the parser must treat it as a model bug, not a HITL decision.
func TestHITL_ToolUseValidationError_DoesNotEmitHITLEvents(t *testing.T) {
	data := mustReadFixture(t, "streaming-ask-user-question-validation-error.jsonl")

	sink := &streamSink{}
	p := newClaudeParser(sink)
	p.setHITLContext("run-validation", agentadaptor.HumanDecisionPolicy{})
	p.enableStreaming("run-validation")
	if err := p.onChunk("stdout", data, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	p.finalize()

	for _, pl := range sink.snapshot() {
		if pl.Kind == agentadaptor.StreamHITLRequested {
			t.Errorf("validation error must not emit StreamHITLRequested, got %+v", pl.HITLRequested)
		}
		if pl.Kind == agentadaptor.StreamHITLResolved {
			t.Errorf("validation error must not emit StreamHITLResolved, got %+v", pl.HITLResolved)
		}
	}
	if p.pendingFailure != nil {
		t.Fatalf("validation error must not set pendingFailure, got %+v", p.pendingFailure)
	}

	// But the underlying tool_call.result event MUST still be emitted so
	// hosts can render the error on the generic tool_call card.
	var sawToolResult bool
	for _, pl := range sink.snapshot() {
		if pl.Kind == agentadaptor.StreamToolCallResult && pl.ToolCallID == "toolu_ask_bad" {
			sawToolResult = true
			if isErr, _ := pl.Result["is_error"].(bool); !isErr {
				t.Errorf("tool_call.result should keep is_error=true; got %+v", pl.Result)
			}
		}
	}
	if !sawToolResult {
		t.Fatal("expected tool_call.result event for the validation-failed tool_use")
	}
}

// TestInterpretClaudeToolResult covers the decision-vs-error taxonomy that
// drives HITL emission. Unit test of the pure function to keep the matrix
// documented.
func TestInterpretClaudeToolResult(t *testing.T) {
	cases := []struct {
		name       string
		content    string
		isError    bool
		want       agentadaptor.DecisionResult
		isDecision bool
	}{
		{"approved explicit", "User approved the plan.", false, agentadaptor.DecisionApproved, true},
		{"approved variant", "User approved - proceeding", false, agentadaptor.DecisionApproved, true},
		{"approved heuristic", "plan approved by reviewer", false, agentadaptor.DecisionApproved, true},
		{"user rejected", "User rejected the plan", true, agentadaptor.DecisionRejected, true},
		{"user did not answer", "User did not answer", true, agentadaptor.DecisionRejected, true},
		{"validation error wrapped", "<tool_use_error>InputValidationError: bad</tool_use_error>", true, "", false},
		{"validation error partial", "  <tool_use_error>whatever", true, "", false},
		{"input validation marker", "InputValidationError: ...", true, "", false},
		{"unknown success text", "some new message", false, agentadaptor.DecisionApproved, true},
	}
	for _, c := range cases {
		got, ok := interpretClaudeToolResult(c.content, c.isError)
		if ok != c.isDecision || got != c.want {
			t.Errorf("%s: got (%q, %v) want (%q, %v)", c.name, got, ok, c.want, c.isDecision)
		}
	}
}

func mustReadFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return data
}
