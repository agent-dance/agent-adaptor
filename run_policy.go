package agentadaptor

import "fmt"

// Presets: hosts may use these instead of ad-hoc field combinations.
var (
	// PolicyHostReview: ask the host for every HITL dimension. Suitable for
	// interactive UIs where the host wants to participate in all decisions.
	PolicyHostReview = RunPolicy{
		Isolation: IsolationWorkspaceWrite,
		HumanDecision: HumanDecisionPolicy{
			Permission: HumanDecisionAsk,
			PlanReview: HumanDecisionAsk,
			Question:   QuestionAsk,
		},
	}

	// PolicyReadOnlyReview: read-only workspace + host-reviewed HITL.
	PolicyReadOnlyReview = RunPolicy{
		Isolation: IsolationReadOnly,
		HumanDecision: HumanDecisionPolicy{
			Permission: HumanDecisionAsk,
			PlanReview: HumanDecisionAsk,
			Question:   QuestionAsk,
		},
	}

	// PolicyAutonomous: hand HITL back to the agent. Equivalent to the
	// legacy RunPolicyTrusted preset. Question is forced to
	// QuestionAutoReject because Question has no legitimate AutoApprove
	// value (see QuestionMode godoc).
	PolicyAutonomous = RunPolicy{
		Isolation: IsolationUnrestricted,
		HumanDecision: HumanDecisionPolicy{
			Permission: HumanDecisionAutoApprove,
			PlanReview: HumanDecisionAutoApprove,
			Question:   QuestionAutoReject,
		},
	}
)

// mergeRunPolicy layers per-call runPolicy on top of binding defaults. Empty
// fields in override mean "keep default for that field". Returns an error
// when the resolved policy has illegal values (e.g. MaxRetries < 0).
func mergeRunPolicy(base, override *RunPolicy) (RunPolicy, error) {
	var out RunPolicy
	if base != nil {
		out = *base
	}
	if override != nil {
		ov := *override
		if ov.Isolation != IsolationInherit {
			out.Isolation = ov.Isolation
		}
		if ov.WebSearch != FeatureInherit {
			out.WebSearch = ov.WebSearch
		}
		if ov.Browser != FeatureInherit {
			out.Browser = ov.Browser
		}
		out.HumanDecision = mergeHumanDecisionPolicy(out.HumanDecision, ov.HumanDecision)
	}
	if err := validateHumanDecisionPolicy(&out.HumanDecision); err != nil {
		return RunPolicy{}, err
	}
	return out, nil
}

// mergeHumanDecisionPolicy overlays override onto base using zero-value-is-
// inherit semantics for every field.
func mergeHumanDecisionPolicy(base, override HumanDecisionPolicy) HumanDecisionPolicy {
	out := base
	if override.Permission != HumanDecisionUnset {
		out.Permission = override.Permission
	}
	if override.PlanReview != HumanDecisionUnset {
		out.PlanReview = override.PlanReview
	}
	if override.Question != QuestionUnset {
		out.Question = override.Question
	}
	if override.Timeout != 0 {
		out.Timeout = override.Timeout
	}
	if override.OnTimeout != FailureActionUnset {
		out.OnTimeout = override.OnTimeout
	}
	if override.OnReject != FailureActionUnset {
		out.OnReject = override.OnReject
	}
	if override.MaxRetries != 0 {
		out.MaxRetries = override.MaxRetries
	}
	return out
}

// validateHumanDecisionPolicy rejects impossible configurations before the
// runner exposes them to adapters.
func validateHumanDecisionPolicy(p *HumanDecisionPolicy) error {
	if p == nil {
		return nil
	}
	switch p.Permission {
	case HumanDecisionUnset, HumanDecisionAsk, HumanDecisionAutoApprove, HumanDecisionAutoReject:
	default:
		return fmt.Errorf("agentadaptor: invalid HumanDecisionPolicy.Permission=%q", p.Permission)
	}
	switch p.PlanReview {
	case HumanDecisionUnset, HumanDecisionAsk, HumanDecisionAutoApprove, HumanDecisionAutoReject:
	default:
		return fmt.Errorf("agentadaptor: invalid HumanDecisionPolicy.PlanReview=%q", p.PlanReview)
	}
	switch p.Question {
	case QuestionUnset, QuestionAsk, QuestionAutoReject:
	default:
		return fmt.Errorf("agentadaptor: invalid HumanDecisionPolicy.Question=%q", p.Question)
	}
	switch p.OnTimeout {
	case FailureActionUnset, FailureAbort, FailureContinue, FailureRetry:
	default:
		return fmt.Errorf("agentadaptor: invalid HumanDecisionPolicy.OnTimeout=%q", p.OnTimeout)
	}
	switch p.OnReject {
	case FailureActionUnset, FailureAbort, FailureContinue, FailureRetry:
	default:
		return fmt.Errorf("agentadaptor: invalid HumanDecisionPolicy.OnReject=%q", p.OnReject)
	}
	if p.MaxRetries < 0 {
		return fmt.Errorf("agentadaptor: invalid HumanDecisionPolicy.MaxRetries=%d (must be >= 0)", p.MaxRetries)
	}
	return nil
}

// EffectiveHumanDecisionPolicy materializes SDK defaults for unset fields in
// a HumanDecisionPolicy. Adapters use it when they need to know the actual
// Timeout / OnTimeout / OnReject / MaxRetries values that the runner applies
// so they can surface consistent Deadline timestamps and failure messages.
func EffectiveHumanDecisionPolicy(p HumanDecisionPolicy) HumanDecisionPolicy {
	out := p
	if out.Permission == HumanDecisionUnset {
		out.Permission = HumanDecisionAsk
	}
	if out.PlanReview == HumanDecisionUnset {
		out.PlanReview = HumanDecisionAsk
	}
	if out.Question == QuestionUnset {
		out.Question = QuestionAutoReject
	}
	if out.Timeout == 0 {
		out.Timeout = DefaultHumanDecisionTimeout
	}
	if out.OnTimeout == FailureActionUnset {
		out.OnTimeout = FailureAbort
	}
	if out.OnReject == FailureActionUnset {
		out.OnReject = FailureAbort
	}
	if out.MaxRetries == 0 {
		out.MaxRetries = DefaultHumanDecisionMaxRetries
	}
	return out
}

func cloneRunPolicy(p *RunPolicy) *RunPolicy {
	if p == nil {
		return nil
	}
	c := *p
	return &c
}
