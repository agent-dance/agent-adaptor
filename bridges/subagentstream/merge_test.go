package subagentstream_test

// Projection and nil-bus compatibility tests. The C03 Merge behavior is
// exercised with structural binding proofs in alignment_merge_test.go.

import (
	"context"
	"reflect"
	"sync"
	"testing"

	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/bridges/subagentstream"
	"github.com/agent-dance/agent-adaptor/hosttools/a2adelegation"
)

// fakeStream is a minimal chan-backed adaptor.Stream.
type fakeStream struct {
	runID  string
	events chan adaptor.Event
	result *adaptor.Result
	err    error

	cancelOnce sync.Once
	cancelled  chan struct{}
}

var _ adaptor.Stream = (*fakeStream)(nil)

func newFakeStream(runID string) *fakeStream {
	return &fakeStream{
		runID:     runID,
		events:    make(chan adaptor.Event, 16),
		result:    &adaptor.Result{Text: "leader done"},
		cancelled: make(chan struct{}),
	}
}

func (f *fakeStream) Events() <-chan adaptor.Event     { return f.events }
func (f *fakeStream) Result() (*adaptor.Result, error) { return f.result, f.err }
func (f *fakeStream) RunID() string                    { return f.runID }
func (f *fakeStream) Cancel()                          { f.cancelOnce.Do(func() { close(f.cancelled) }) }

func delegationEvent(runID, delegationID string, kind a2adelegation.DelegationEventKind) a2adelegation.DelegationEvent {
	return a2adelegation.DelegationEvent{
		RunID:        runID,
		DelegationID: delegationID,
		AgentKey:     "impl",
		Kind:         kind,
	}
}

func collect(t *testing.T, stream adaptor.Stream) (parents []adaptor.Event, subs []adaptor.SubagentUpdate) {
	t.Helper()
	for ev := range stream.Events() {
		if sub, ok := ev.(adaptor.SubagentUpdate); ok {
			subs = append(subs, sub)
			continue
		}
		parents = append(parents, ev)
	}
	return parents, subs
}

func TestMergeNilBusPassthrough(t *testing.T) {
	fake := newFakeStream("run-nil")
	if merged := subagentstream.Merge(context.Background(), fake, nil); merged != adaptor.Stream(fake) {
		t.Fatalf("Merge with nil bus = %T, want the parent stream unchanged", merged)
	}
	if merged := subagentstream.Merge(context.Background(), nil, nil); merged != nil {
		t.Fatalf("Merge(nil stream) = %v, want nil", merged)
	}
}

func TestSubagentEventProjection(t *testing.T) {
	started := a2adelegation.DelegationEvent{
		RunID:        "r",
		DelegationID: "d1",
		AgentKey:     "impl",
		AgentName:    "Implementer",
		Kind:         a2adelegation.DelegationStarted,
	}
	up := subagentstream.SubagentEvent(started)
	if up.Kind != adaptor.SubagentStarted || up.Agent != "impl" {
		t.Errorf("started projection = %+v", up)
	}
	if up.Data["kind"] != string(a2adelegation.DelegationStarted) || up.Data["agent_name"] != "Implementer" {
		t.Errorf("started Data = %#v", up.Data)
	}
	for _, key := range []string{"status", "delta", "text", "tool_name", "error", "sequence", "remote_task_id"} {
		if _, exists := up.Data[key]; exists {
			t.Errorf("empty field %q not pruned from Data: %#v", key, up.Data)
		}
	}

	delta := a2adelegation.DelegationEvent{
		RunID:        "r",
		DelegationID: "d1",
		AgentKey:     "impl",
		Kind:         a2adelegation.DelegationTextDelta,
		Delta:        "abc",
		Sequence:     7,
	}
	up = subagentstream.SubagentEvent(delta)
	if up.Kind != adaptor.SubagentDelta || up.Delta != "abc" {
		t.Errorf("delta projection = %+v", up)
	}
	if !reflect.DeepEqual(up.Data["sequence"], uint64(7)) {
		t.Errorf("Data[sequence] = %#v, want uint64(7)", up.Data["sequence"])
	}

	for _, kind := range []a2adelegation.DelegationEventKind{
		a2adelegation.DelegationFinished,
		a2adelegation.DelegationFailed,
		a2adelegation.DelegationCancelled,
		a2adelegation.DelegationInputRequired,
	} {
		terminal := a2adelegation.DelegationEvent{RunID: "r", DelegationID: "d1", AgentKey: "impl", Kind: kind}
		if got := subagentstream.SubagentEvent(terminal).Kind; got != adaptor.SubagentFinished {
			t.Errorf("terminal %s projected to %v, want finished", kind, got)
		}
	}

	if got := subagentstream.SubagentEvent(a2adelegation.DelegationEvent{
		RunID: "r", DelegationID: "d1", AgentKey: "impl", Kind: a2adelegation.DelegationStatus,
	}).Kind; got != adaptor.SubagentDelta {
		t.Errorf("status projected to %v, want delta", got)
	}
}
