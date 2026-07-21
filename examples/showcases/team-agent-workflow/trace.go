package main

import (
	"fmt"
	"reflect"
	"sync"

	"github.com/agent-dance/agent-adaptor/pkg/hosttools/a2adelegation"
)

type workflowTrace struct {
	mu     sync.Mutex
	events []a2adelegation.DelegationEvent
}

type delegationSummary struct {
	Agent      string `json:"agent"`
	Status     string `json:"status"`
	TaskID     string `json:"remote_task_id,omitempty"`
	Delegation string `json:"delegation_id"`
}

func newWorkflowTrace() *workflowTrace {
	return &workflowTrace{}
}

func (t *workflowTrace) Collect(events <-chan a2adelegation.DelegationEvent) {
	for event := range events {
		t.mu.Lock()
		t.events = append(t.events, event)
		t.mu.Unlock()
		switch event.Kind {
		case a2adelegation.DelegationStarted, a2adelegation.DelegationFinished,
			a2adelegation.DelegationFailed, a2adelegation.DelegationCancelled:
			term.Logf("[team] %-7s %-22s status=%s task=%s", event.AgentKey, event.Kind, event.Status, event.RemoteTaskID)
		case a2adelegation.DelegationTextDelta:
			term.Stream("  \u21b3 "+event.AgentKey, event.Delta)
		case a2adelegation.DelegationToolCallStart:
			args, _ := event.Args.(map[string]any)
			term.Logf("[team]   \u21b3 %s tool_call.start %s%s", event.AgentKey, toolLabel(event.ToolName, event.RemoteToolCallID), toolArgsHint(args))
		case a2adelegation.DelegationToolCallResult:
			term.Logf("[team]   \u21b3 %s tool_call.result %s", event.AgentKey, toolLabel(event.ToolName, event.RemoteToolCallID))
		}
	}
}

func (t *workflowTrace) ValidateOrderedRoles(expected []string) error {
	events := t.snapshot()
	started := map[string]bool{}
	var completed []string
	for _, event := range events {
		switch event.Kind {
		case a2adelegation.DelegationStarted:
			started[event.AgentKey] = true
		case a2adelegation.DelegationFailed, a2adelegation.DelegationCancelled, a2adelegation.DelegationInputRequired:
			return fmt.Errorf("delegation %s ended as %s: %+v", event.AgentKey, event.Kind, event.Error)
		case a2adelegation.DelegationFinished:
			completed = append(completed, event.AgentKey)
		}
	}
	for _, role := range expected {
		if !started[role] {
			return fmt.Errorf("leader did not start required role %q", role)
		}
	}
	if !reflect.DeepEqual(completed, expected) {
		return fmt.Errorf("leader must complete roles exactly in order %v, got %v", expected, completed)
	}
	return nil
}

func (t *workflowTrace) Summary() []delegationSummary {
	events := t.snapshot()
	var out []delegationSummary
	for _, event := range events {
		if event.Kind != a2adelegation.DelegationFinished {
			continue
		}
		out = append(out, delegationSummary{
			Agent: event.AgentKey, Status: event.Status, TaskID: event.RemoteTaskID, Delegation: event.DelegationID,
		})
	}
	return out
}

func (t *workflowTrace) snapshot() []a2adelegation.DelegationEvent {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]a2adelegation.DelegationEvent(nil), t.events...)
}
