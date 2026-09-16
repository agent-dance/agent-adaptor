package codebuddy

import (
	"context"
	"errors"
	"testing"

	adaptor "github.com/agent-dance/agent-adaptor"
)

// Shared by the hermetic policy regression and the gated live fixtures.
var livePolicyHeadless = adaptor.Policy{
	Sandbox: adaptor.SandboxInherit,
	Approvals: adaptor.ApprovalPolicy{
		Permission: adaptor.ApprovalAutoApprove,
		PlanReview: adaptor.ApprovalAutoApprove,
	},
}

func TestAlignmentCodeBuddyHeadlessLivePolicy(t *testing.T) {
	t.Run("inherited isolation executes", func(t *testing.T) {
		fx := newPersistentCodeBuddyFixture(t, nil)
		defer fx.close()
		result, err := fx.agent.Run(context.Background(), "headless fixture", adaptor.WithPolicy(livePolicyHeadless))
		if err != nil {
			t.Fatalf("headless live policy must reach the CLI: %v", err)
		}
		if result.Text != "ok" || fx.spawnCount(t) != 1 {
			t.Fatalf("real fake process result=%q spawns=%d", result.Text, fx.spawnCount(t))
		}
	})
	for _, sandbox := range []adaptor.SandboxLevel{adaptor.Unrestricted, adaptor.ReadOnly, adaptor.WorkspaceWrite} {
		t.Run("explicit "+string(sandbox)+" rejected", func(t *testing.T) {
			fx := newPersistentCodeBuddyFixture(t, nil)
			defer fx.close()
			policy := livePolicyHeadless
			policy.Sandbox = sandbox
			result, err := fx.agent.Run(context.Background(), "must not start", adaptor.WithPolicy(policy))
			if result != nil || !errors.Is(err, adaptor.ErrPolicyCapabilityUnsupported) {
				t.Fatalf("explicit unsupported isolation: result=%v err=%v", result, err)
			}
			if fx.spawnCount(t) != 0 {
				t.Fatal("unsupported isolation started a provider process")
			}
		})
	}
}
