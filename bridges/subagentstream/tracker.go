package subagentstream

import (
	"context"
	"time"

	"github.com/agent-dance/agent-adaptor/hosttools/a2adelegation"
)

// EventBus is the minimal host-side subscription contract Merge consumes.
type EventBus interface {
	SubscribeRun(ctx context.Context, runID string) <-chan a2adelegation.DelegationEvent
}

type delegationTracker struct {
	active map[string]a2adelegation.DelegationEvent
	order  []string
}

func newDelegationTracker() *delegationTracker {
	return &delegationTracker{active: map[string]a2adelegation.DelegationEvent{}}
}

func (t *delegationTracker) Track(event a2adelegation.DelegationEvent) {
	if event.DelegationID == "" {
		return
	}
	if isTerminalDelegation(event.Kind) {
		delete(t.active, event.DelegationID)
		return
	}
	if _, exists := t.active[event.DelegationID]; !exists {
		t.order = append(t.order, event.DelegationID)
	}
	t.active[event.DelegationID] = event
}

func (t *delegationTracker) FlushSynthetic(kind a2adelegation.DelegationEventKind, status string, failure *a2adelegation.DelegationError, send func(a2adelegation.DelegationEvent) bool) bool {
	for _, delegationID := range t.order {
		event, ok := t.active[delegationID]
		if !ok {
			continue
		}
		event.Kind = kind
		event.Status = status
		event.Error = failure
		event.Time = time.Now()
		delete(t.active, delegationID)
		if !send(event) {
			return false
		}
	}
	return true
}

func isTerminalDelegation(kind a2adelegation.DelegationEventKind) bool {
	switch kind {
	case a2adelegation.DelegationFinished, a2adelegation.DelegationFailed,
		a2adelegation.DelegationCancelled, a2adelegation.DelegationInputRequired:
		return true
	default:
		return false
	}
}

func parentFinishedError() *a2adelegation.DelegationError {
	return &a2adelegation.DelegationError{
		Code: "parent_finished", Message: "parent run finished before subagent terminal event",
	}
}
