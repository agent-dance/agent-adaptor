package subagentstream

import (
	"context"
	"github.com/agent-dance/agent-adaptor/hosttools/a2adelegation"
)

// EventBus preserves Merge's published bus contract. Merge no longer invokes
// SubscribeRun: only an optional RunEventsBound(string) bool proof is queried.
type EventBus interface {
	SubscribeRun(ctx context.Context, runID string) <-chan a2adelegation.DelegationEvent
}
