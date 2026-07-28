package subagentstream_test

// Tests for the Merge/SubagentEvent bridge. Determinism:
// all delegation events are published to the bus before Merge subscribes, so
// they sit in the replay buffer (delivered synchronously at SubscribeRun
// time); every wait is channel-based — no sleeps.

import (
	"context"
	"reflect"
	"runtime"
	"sync"
	"testing"
	"time"

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

func TestMergeInterleavesParentAndBus(t *testing.T) {
	const runID = "run-merge"
	bus := a2adelegation.NewEventBus(16)

	// Publish the full delegation lifecycle before Merge subscribes: replay
	// delivers it deterministically.
	started := delegationEvent(runID, "d1", a2adelegation.DelegationStarted)
	delta := delegationEvent(runID, "d1", a2adelegation.DelegationTextDelta)
	delta.Delta = "sub says hi"
	delta.Sequence = 3
	finished := delegationEvent(runID, "d1", a2adelegation.DelegationFinished)
	finished.Status = "completed"
	for _, ev := range []a2adelegation.DelegationEvent{started, delta, finished} {
		if !bus.Publish(ev) {
			t.Fatalf("Publish(%s) refused", ev.Kind)
		}
	}

	fake := newFakeStream(runID)
	fake.events <- adaptor.TextDelta{Text: "leader ", Role: adaptor.RoleAssistant}
	fake.events <- adaptor.TextDelta{Text: "output", Role: adaptor.RoleAssistant}
	close(fake.events)

	merged := subagentstream.Merge(context.Background(), fake, bus)
	if merged.RunID() != runID {
		t.Errorf("RunID = %q, want %q", merged.RunID(), runID)
	}

	parents, subs := collect(t, merged)

	if len(parents) != 2 {
		t.Fatalf("parent events = %d, want 2: %#v", len(parents), parents)
	}
	gotText := parents[0].(adaptor.TextDelta).Text + parents[1].(adaptor.TextDelta).Text
	if gotText != "leader output" {
		t.Errorf("parent text = %q, want %q (parent order must be preserved)", gotText, "leader output")
	}

	if len(subs) != 3 {
		t.Fatalf("subagent updates = %d, want 3: %#v", len(subs), subs)
	}
	wantKinds := []adaptor.SubagentEventKind{adaptor.SubagentStarted, adaptor.SubagentDelta, adaptor.SubagentFinished}
	for i, want := range wantKinds {
		if subs[i].Kind != want {
			t.Errorf("subs[%d].Kind = %v, want %v", i, subs[i].Kind, want)
		}
		if subs[i].Agent != "impl" {
			t.Errorf("subs[%d].Agent = %q, want impl", i, subs[i].Agent)
		}
	}
	if subs[1].Delta != "sub says hi" {
		t.Errorf("subs[1].Delta = %q, want %q", subs[1].Delta, "sub says hi")
	}
	if subs[2].Data["error"] != nil {
		t.Errorf("real terminal carries synthetic error: %v", subs[2].Data["error"])
	}

	// Result and Cancel delegate to the parent.
	res, err := merged.Result()
	if err != nil || res == nil || res.Text != "leader done" {
		t.Errorf("Result = (%+v, %v), want parent result", res, err)
	}
	merged.Cancel()
	select {
	case <-fake.cancelled:
	default:
		t.Error("Cancel did not reach the parent stream")
	}
}

func TestMergeSynthesizesTerminalOnParentClose(t *testing.T) {
	const runID = "run-synth"
	bus := a2adelegation.NewEventBus(16)
	if !bus.Publish(delegationEvent(runID, "d1", a2adelegation.DelegationStarted)) {
		t.Fatal("Publish refused")
	}

	fake := newFakeStream(runID)
	close(fake.events) // leader ends without a delegation terminal

	_, subs := collect(t, subagentstream.Merge(context.Background(), fake, bus))
	if len(subs) != 2 {
		t.Fatalf("subagent updates = %d, want started + synthetic terminal: %#v", len(subs), subs)
	}
	if subs[0].Kind != adaptor.SubagentStarted {
		t.Errorf("subs[0].Kind = %v, want started", subs[0].Kind)
	}
	last := subs[1]
	if last.Kind != adaptor.SubagentFinished {
		t.Errorf("synthetic kind = %v, want finished", last.Kind)
	}
	if got := last.Data["kind"]; got != string(a2adelegation.DelegationFailed) {
		t.Errorf("synthetic Data[kind] = %v, want %q", got, a2adelegation.DelegationFailed)
	}
	if got := last.Data["status"]; got != "failed" {
		t.Errorf("synthetic Data[status] = %v, want failed", got)
	}
	dErr, ok := last.Data["error"].(*a2adelegation.DelegationError)
	if !ok || dErr.Code != "parent_finished" {
		t.Errorf("synthetic Data[error] = %#v, want *DelegationError{Code: parent_finished}", last.Data["error"])
	}
}

func TestMergeCancelViaContext(t *testing.T) {
	bus := a2adelegation.NewEventBus(16)
	fake := newFakeStream("run-cancel") // parent stays open

	ctx, cancel := context.WithCancel(context.Background())
	merged := subagentstream.Merge(ctx, fake, bus)
	cancel()

	if _, subs := collect(t, merged); len(subs) != 0 {
		t.Errorf("subagent updates after cancel = %#v, want none (nothing was tracked)", subs)
	}
	<-fake.cancelled // Merge must cancel the parent run; blocks forever on failure
}

func TestMergeHoldsParentTerminalUntilAllSubagentsAndRestampsOrder(t *testing.T) {
	const runID = "run-terminal-order"
	bus := a2adelegation.NewEventBus(16)
	child := delegationEvent(runID, "d1", a2adelegation.DelegationStarted)
	child.Sequence = 70
	child.RemoteContextID = "remote-context"
	child.Time = time.Date(2026, 7, 28, 1, 2, 3, 0, time.UTC)
	if !bus.Publish(child) || !bus.Publish(delegationEvent(runID, "d1", a2adelegation.DelegationFinished)) {
		t.Fatal("publish refused")
	}

	fake := newFakeStream(runID)
	fake.events <- adaptor.WithEventMeta(adaptor.TextDelta{MessageID: "m", Text: "leader"}, adaptor.EventMeta{
		RunID: runID, ThreadKey: "host-thread", Sequence: 99,
	})
	// The parent terminal arrives before Merge drains replayed child events.
	fake.events <- adaptor.WithEventMeta(adaptor.RunFinished{RunID: runID}, adaptor.EventMeta{RunID: runID, ThreadKey: "host-thread", Sequence: 100})
	close(fake.events)

	var events []adaptor.Event
	for event := range subagentstream.Merge(context.Background(), fake, bus).Events() {
		events = append(events, event)
	}
	if len(events) < 4 {
		t.Fatalf("events = %#v", events)
	}
	if _, ok := events[len(events)-1].(adaptor.RunFinished); !ok {
		t.Fatalf("last event = %T, want parent RunFinished: %#v", events[len(events)-1], events)
	}
	for index, event := range events {
		if got := event.Meta().Sequence; got != uint64(index+1) {
			t.Fatalf("event[%d] sequence=%d, want %d (%T)", index, got, index+1, event)
		}
		if event.Meta().RunID != runID {
			t.Fatalf("event[%d] run id = %q", index, event.Meta().RunID)
		}
	}
	var sawChildSource bool
	for _, event := range events[:len(events)-1] {
		if child, ok := event.(adaptor.SubagentUpdate); ok && child.Meta().Source != nil && child.Meta().Source.Sequence == 70 && child.Meta().Source.ThreadID == "remote-context" {
			sawChildSource = true
		}
	}
	if !sawChildSource {
		t.Fatalf("child provider coordinates were not retained: %#v", events)
	}
}

func TestMergeCancelUnblocksSaturatedOutput(t *testing.T) {
	fake := newFakeStream("run-blocked")
	bus := a2adelegation.NewEventBus(0)
	merged := subagentstream.Merge(context.Background(), fake, bus)
	// Keep producing asynchronously until Merge's 64-event output is full.
	producerDone := make(chan struct{})
	go func() {
		defer close(producerDone)
		for index := 0; index < 256; index++ {
			select {
			case <-fake.cancelled:
				return
			case fake.events <- adaptor.TextDelta{MessageID: "m", Text: "x"}:
			}
		}
	}()
	deadline := time.Now().Add(time.Second)
	for len(merged.Events()) < cap(merged.Events()) && time.Now().Before(deadline) {
		runtime.Gosched()
	}
	if len(merged.Events()) < cap(merged.Events()) {
		t.Fatalf("merged output did not saturate: len=%d cap=%d", len(merged.Events()), cap(merged.Events()))
	}
	merged.Cancel()
	select {
	case <-fake.cancelled:
	case <-time.After(time.Second):
		t.Fatal("Cancel did not reach parent")
	}
	select {
	case <-producerDone:
	case <-time.After(time.Second):
		t.Fatal("blocked producer did not observe cancellation")
	}
	for range merged.Events() {
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
