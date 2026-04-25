package runtimeservice_test

import (
	"context"
	"testing"

	agentadaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/runtimeservice"
)

// myManager is a minimal RuntimeServiceManager fixture using the
// NoopReleaseByLabels mixin. It exercises the "v0.4 implementation
// upgrades to v0.5 by embedding the mixin" path documented on the
// type.
type myManager struct {
	runtimeservice.NoopReleaseByLabels

	ensured  bool
	released string
}

func (m *myManager) Ensure(_ context.Context, _ agentadaptor.RuntimeServiceRequest) ([]agentadaptor.RuntimeServiceRef, error) {
	m.ensured = true
	return nil, nil
}

func (m *myManager) ReleaseByRun(_ context.Context, runID string) error {
	m.released = runID
	return nil
}

func TestNoopReleaseByLabelsSatisfiesV05Interface(t *testing.T) {
	t.Parallel()
	// Compile-time check: a manager that embeds the mixin is a
	// RuntimeServiceManager. If the embedding ever stops promoting
	// ReleaseByLabels, this assignment fails to compile.
	var _ agentadaptor.RuntimeServiceManager = (*myManager)(nil)
}

func TestNoopReleaseByLabelsIsNoop(t *testing.T) {
	t.Parallel()
	mgr := &myManager{}
	if err := mgr.ReleaseByLabels(context.Background(), map[string]string{"task_id": "t-1"}); err != nil {
		t.Fatalf("ReleaseByLabels: want nil, got %v", err)
	}
	// Confirm the mixin really is a noop: no fields on myManager
	// should change as a side-effect.
	if mgr.ensured || mgr.released != "" {
		t.Errorf("ReleaseByLabels mutated unrelated state: %+v", mgr)
	}
}

func TestNoopReleaseByLabelsAcceptsEmptyMap(t *testing.T) {
	t.Parallel()
	mgr := &myManager{}
	if err := mgr.ReleaseByLabels(context.Background(), nil); err != nil {
		t.Fatalf("ReleaseByLabels(nil): want nil, got %v", err)
	}
	if err := mgr.ReleaseByLabels(context.Background(), map[string]string{}); err != nil {
		t.Fatalf("ReleaseByLabels(empty): want nil, got %v", err)
	}
}
