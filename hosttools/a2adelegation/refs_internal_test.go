package a2adelegation

import (
	"context"
	"testing"

	adaptor "github.com/agent-dance/agent-adaptor"
)

type inertLocalRunner struct{}

func (inertLocalRunner) Run(context.Context, string, ...adaptor.CallOption) (*adaptor.Result, error) {
	panic("unexpected Run")
}

func (inertLocalRunner) Stream(context.Context, string, ...adaptor.CallOption) adaptor.Stream {
	panic("unexpected Stream")
}

func TestLocalNamedRegistersDisplayName(t *testing.T) {
	service, err := NewService(Config{Agents: []AgentRef{
		LocalNamed("plan", "Claude Code Planner", inertLocalRunner{}, Policy{}),
	}})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	t.Cleanup(func() { _ = service.Close() })

	spec, ok := service.registry.Lookup("plan")
	if !ok {
		t.Fatal("named local agent was not registered")
	}
	if spec.Key != "plan" || spec.DisplayName != "Claude Code Planner" {
		t.Fatalf("registered spec = %+v", spec)
	}
}
