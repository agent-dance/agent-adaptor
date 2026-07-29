package exampleutil

import (
	"testing"

	adaptor "github.com/agent-dance/agent-adaptor"
)

func TestNonInteractivePolicyMatchesProviderCapabilities(t *testing.T) {
	tests := []struct {
		agent          string
		wantPlanReview adaptor.ApprovalMode
		wantSandbox    adaptor.SandboxLevel
	}{
		{agent: AgentClaude, wantPlanReview: adaptor.ApprovalAutoApprove, wantSandbox: adaptor.SandboxInherit},
		{agent: AgentCodebuddy, wantPlanReview: adaptor.ApprovalAutoApprove, wantSandbox: adaptor.SandboxInherit},
		{agent: AgentCodex, wantPlanReview: adaptor.ApprovalInherit, wantSandbox: adaptor.WorkspaceWrite},
		{agent: AgentCursor, wantPlanReview: adaptor.ApprovalInherit, wantSandbox: adaptor.SandboxInherit},
	}

	for _, test := range tests {
		t.Run(test.agent, func(t *testing.T) {
			policy := NonInteractivePolicy(test.agent, adaptor.WorkspaceWrite)
			if policy.Approvals.Permission != adaptor.ApprovalAutoApprove {
				t.Fatalf("Permission = %q, want auto_approve", policy.Approvals.Permission)
			}
			if policy.Approvals.PlanReview != test.wantPlanReview {
				t.Fatalf("PlanReview = %q, want %q", policy.Approvals.PlanReview, test.wantPlanReview)
			}
			if policy.Sandbox != test.wantSandbox {
				t.Fatalf("Sandbox = %q, want %q", policy.Sandbox, test.wantSandbox)
			}
		})
	}
}
