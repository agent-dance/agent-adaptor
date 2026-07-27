package agentadaptor

import (
	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/engine"
)

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

// mergeRunPolicy delegates to the engine implementation; the merge and
// validation semantics moved to internal/engine with the run pipeline.
func mergeRunPolicy(base, override *RunPolicy) (RunPolicy, error) {
	return engine.MergeRunPolicy(base, override)
}

func cloneRunPolicy(p *RunPolicy) *RunPolicy { return engine.CloneRunPolicy(p) }

// EffectiveHumanDecisionPolicy materializes SDK defaults for unset fields in
// a HumanDecisionPolicy. Adapters use it when they need to know the actual
// Timeout / OnTimeout / OnReject / MaxRetries values that the runner applies
// so they can surface consistent Deadline timestamps and failure messages.
// The truth moved to the driver package in P5.2 (it only manipulates
// driver-owned types); this stays a forwarder so the root surface is
// unchanged.
func EffectiveHumanDecisionPolicy(p HumanDecisionPolicy) HumanDecisionPolicy {
	return driver.EffectiveHumanDecisionPolicy(p)
}
