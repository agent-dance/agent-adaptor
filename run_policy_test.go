package agentadaptor

import (
	"testing"
	"time"
)

func TestMergeRunPolicy(t *testing.T) {
	base := &RunPolicy{
		Isolation: IsolationReadOnly,
		WebSearch: FeatureDeny,
		HumanDecision: HumanDecisionPolicy{
			Permission: HumanDecisionAsk,
		},
	}
	t.Run("nil override", func(t *testing.T) {
		got, err := mergeRunPolicy(base, nil)
		if err != nil {
			t.Fatalf("merge: %v", err)
		}
		if got != *base {
			t.Fatalf("got %#v want %#v", got, *base)
		}
	})
	t.Run("partial override", func(t *testing.T) {
		got, err := mergeRunPolicy(base, &RunPolicy{
			HumanDecision: HumanDecisionPolicy{Permission: HumanDecisionAutoApprove},
		})
		if err != nil {
			t.Fatalf("merge: %v", err)
		}
		if got.HumanDecision.Permission != HumanDecisionAutoApprove ||
			got.Isolation != IsolationReadOnly ||
			got.WebSearch != FeatureDeny {
			t.Fatalf("got %#v", got)
		}
	})
	t.Run("nil base", func(t *testing.T) {
		got, err := mergeRunPolicy(nil, &RunPolicy{Isolation: IsolationUnrestricted})
		if err != nil {
			t.Fatalf("merge: %v", err)
		}
		if got.Isolation != IsolationUnrestricted || got.HumanDecision.Permission != HumanDecisionUnset {
			t.Fatalf("got %#v", got)
		}
	})
	t.Run("rejects negative max retries", func(t *testing.T) {
		_, err := mergeRunPolicy(nil, &RunPolicy{
			HumanDecision: HumanDecisionPolicy{MaxRetries: -1},
		})
		if err == nil {
			t.Fatal("expected validation error for MaxRetries=-1")
		}
	})
}

func TestEffectiveHumanDecisionPolicyDefaults(t *testing.T) {
	p := EffectiveHumanDecisionPolicy(HumanDecisionPolicy{})
	if p.Permission != HumanDecisionAsk {
		t.Errorf("Permission default: got %q, want Ask", p.Permission)
	}
	if p.PlanReview != HumanDecisionAsk {
		t.Errorf("PlanReview default: got %q, want Ask", p.PlanReview)
	}
	if p.Question != QuestionAutoReject {
		t.Errorf("Question default: got %q, want AutoReject", p.Question)
	}
	if p.Timeout != DefaultHumanDecisionTimeout {
		t.Errorf("Timeout default: got %s, want %s", p.Timeout, DefaultHumanDecisionTimeout)
	}
	if p.OnTimeout != FailureAbort {
		t.Errorf("OnTimeout default: got %q, want Abort", p.OnTimeout)
	}
	if p.OnReject != FailureAbort {
		t.Errorf("OnReject default: got %q, want Abort", p.OnReject)
	}
	if p.MaxRetries != DefaultHumanDecisionMaxRetries {
		t.Errorf("MaxRetries default: got %d, want %d", p.MaxRetries, DefaultHumanDecisionMaxRetries)
	}
}

// TestRunFailureHelpersAreNilSafe protects the nil-safe invariant documented
// on IsHumanDecision / IsRejected / IsTimedOut.
func TestRunFailureHelpersAreNilSafe(t *testing.T) {
	var f *RunFailure
	if f.IsHumanDecision() {
		t.Error("IsHumanDecision on nil should be false")
	}
	if f.IsRejected() {
		t.Error("IsRejected on nil should be false")
	}
	if f.IsTimedOut() {
		t.Error("IsTimedOut on nil should be false")
	}
}

func TestRunFailureHelpersClassify(t *testing.T) {
	rej := &RunFailure{Code: FailureReject, HumanDecision: &HumanDecisionFailure{Kind: HumanDecisionPlanReview}}
	if !rej.IsHumanDecision() || !rej.IsRejected() || rej.IsTimedOut() {
		t.Errorf("classify reject: %+v", rej)
	}
	to := &RunFailure{Code: FailureTimeout, HumanDecision: &HumanDecisionFailure{Kind: HumanDecisionPermission}}
	if !to.IsHumanDecision() || to.IsRejected() || !to.IsTimedOut() {
		t.Errorf("classify timeout: %+v", to)
	}
	ae := &RunFailure{Code: FailureAgentError}
	if ae.IsHumanDecision() || ae.IsRejected() || ae.IsTimedOut() {
		t.Errorf("classify agent error: %+v", ae)
	}
}

// Guard: spec §8.4.1 says constant string values must be stable.
func TestDecisionConstantsSnapshot(t *testing.T) {
	type kv struct {
		got, want, label string
	}
	cases := []kv{
		{string(HumanDecisionPermission), "permission", "HumanDecisionPermission"},
		{string(HumanDecisionPlanReview), "plan_review", "HumanDecisionPlanReview"},
		{string(HumanDecisionQuestion), "question", "HumanDecisionQuestion"},
		{string(HumanDecisionUnset), "", "HumanDecisionUnset"},
		{string(HumanDecisionAsk), "ask", "HumanDecisionAsk"},
		{string(HumanDecisionAutoApprove), "auto_approve", "HumanDecisionAutoApprove"},
		{string(HumanDecisionAutoReject), "auto_reject", "HumanDecisionAutoReject"},
		{string(QuestionUnset), "", "QuestionUnset"},
		{string(QuestionAsk), "ask", "QuestionAsk"},
		{string(QuestionAutoReject), "auto_reject", "QuestionAutoReject"},
		{string(DecisionApproved), "approved", "DecisionApproved"},
		{string(DecisionRejected), "rejected", "DecisionRejected"},
		{string(DecisionAnswered), "answered", "DecisionAnswered"},
		{string(DecisionTimedOut), "timed_out", "DecisionTimedOut"},
		{string(DecisionAborted), "aborted", "DecisionAborted"},
		{string(FailureActionUnset), "", "FailureActionUnset"},
		{string(FailureAbort), "abort", "FailureAbort"},
		{string(FailureContinue), "continue", "FailureContinue"},
		{string(FailureRetry), "retry", "FailureRetry"},
		{string(FailureReject), "decision_rejected", "FailureReject"},
		{string(FailureTimeout), "decision_timeout", "FailureTimeout"},
		{string(FailureAgentError), "agent_error", "FailureAgentError"},
		{string(FailureCancelled), "cancelled", "FailureCancelled"},
		{string(FailurePolicyError), "policy_error", "FailurePolicyError"},
		{string(ApprovalApproved), "approved", "ApprovalApproved"},
		{string(ApprovalRejected), "rejected", "ApprovalRejected"},
		{string(QuestionAnswered), "answered", "QuestionAnswered"},
		{string(QuestionRejected), "rejected", "QuestionRejected"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s drift: got %q want %q", c.label, c.got, c.want)
		}
	}
	_ = time.Second
}
