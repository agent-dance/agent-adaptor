package subagentstream_test

import (
	"context"
	"sync"
	"testing"
	"time"

	aguievents "github.com/ag-ui-protocol/ag-ui/sdks/community/go/pkg/core/events"

	agentadaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/pkg/bridges/subagentstream"
	"github.com/agent-dance/agent-adaptor/pkg/hosttools/a2adelegation"
)

func TestAGUICustomEventMapsDelegationFields(t *testing.T) {
	t.Parallel()
	ev := subagentstream.AGUICustomEvent(a2adelegation.DelegationEvent{
		RunID:            "run-1",
		ParentToolCallID: "tool-1",
		DelegationID:     "del-1",
		AgentKey:         "research",
		AgentName:        "Research",
		Protocol:         a2adelegation.ProtocolA2A,
		RemoteTaskID:     "task-1",
		RemoteContextID:  "ctx-1",
		RemoteMessageID:  "msg-1",
		Kind:             a2adelegation.DelegationTextDelta,
		Delta:            "hello",
	})
	custom, ok := ev.(*aguievents.CustomEvent)
	if !ok {
		t.Fatalf("expected CustomEvent, got %T", ev)
	}
	if custom.Name != string(a2adelegation.DelegationTextDelta) {
		t.Fatalf("custom name: got %q", custom.Name)
	}
	value := custom.Value.(map[string]any)
	if value["delegationId"] != "del-1" || value["delta"] != "hello" || value["remoteTaskId"] != "task-1" {
		t.Fatalf("unexpected custom value: %#v", value)
	}
	if _, ok := value["raw"]; ok {
		t.Fatalf("raw should be omitted when empty: %#v", value)
	}
}

func TestAGUICustomEventOmitsRawPayload(t *testing.T) {
	t.Parallel()
	ev := subagentstream.AGUICustomEvent(a2adelegation.DelegationEvent{
		RunID:        "run-1",
		DelegationID: "del-1",
		AgentKey:     "research",
		Kind:         a2adelegation.DelegationArtifactCreated,
		Raw:          map[string]any{"secret": "inline remote payload"},
	})
	custom, ok := ev.(*aguievents.CustomEvent)
	if !ok {
		t.Fatalf("expected CustomEvent, got %T", ev)
	}
	value := custom.Value.(map[string]any)
	if _, ok := value["raw"]; ok {
		t.Fatalf("raw payload must not be exposed to AG-UI clients: %#v", value)
	}
}

func TestStreamPayloadUsesCustomPassThroughShape(t *testing.T) {
	t.Parallel()
	payload := subagentstream.StreamPayload(a2adelegation.DelegationEvent{
		RunID:        "run-1",
		DelegationID: "del-1",
		AgentKey:     "research",
		Kind:         a2adelegation.DelegationStatus,
		Status:       "working",
	})
	if payload.Kind != "" || payload.Name != string(a2adelegation.DelegationStatus) {
		t.Fatalf("expected custom StreamPayload, got %#v", payload)
	}
	if payload.Raw["delegation_id"] != "del-1" || payload.Raw["status"] != "working" {
		t.Fatalf("unexpected raw payload: %#v", payload.Raw)
	}
}

func TestWrapMergesParentAndDelegationAGUIEvents(t *testing.T) {
	t.Parallel()
	handle := &fakeHandle{
		stream:    make(chan agentadaptor.StreamPayload, 4),
		events:    make(chan agentadaptor.RunEvent),
		done:      make(chan struct{}),
		runID:     "run-1",
		runResult: agentadaptor.RunResult{RunID: "run-1"},
	}
	bus := a2adelegation.NewEventBus(8)
	out := subagentstream.Wrap(context.Background(), handle, subagentstream.MuxOptions{Bus: bus})

	handle.stream <- agentadaptor.StreamPayload{Kind: agentadaptor.StreamRunStarted, ThreadID: "thread-1", RunID: "run-1"}
	events := collectMuxEvents(t, out, 1)
	bus.Publish(a2adelegation.DelegationEvent{RunID: "run-1", DelegationID: "del-1", AgentKey: "research", Kind: a2adelegation.DelegationStarted})
	close(handle.stream)
	close(handle.done)
	events = append(events, collectMuxEvents(t, out, 1)...)

	if events[0].ID == 0 || events[1].ID <= events[0].ID {
		t.Fatalf("expected monotonic mux IDs, got %#v", events)
	}
	if events[0].AGUI.Type() != aguievents.EventTypeRunStarted {
		t.Fatalf("first event should be parent RUN_STARTED, got %s", events[0].AGUI.Type())
	}
	if events[1].AGUI.Type() != aguievents.EventTypeCustom || events[1].Subagent == nil {
		t.Fatalf("second event should be subagent custom, got %#v", events[1])
	}
}

func TestWrapDrainsBufferedSubagentsBeforeParentTerminal(t *testing.T) {
	t.Parallel()
	handle := &fakeHandle{
		stream:    make(chan agentadaptor.StreamPayload, 1),
		events:    make(chan agentadaptor.RunEvent),
		done:      make(chan struct{}),
		runID:     "run-1",
		runResult: agentadaptor.RunResult{RunID: "run-1"},
	}
	bus := a2adelegation.NewEventBus(8)
	bus.Publish(a2adelegation.DelegationEvent{RunID: "run-1", DelegationID: "del-1", AgentKey: "research", Kind: a2adelegation.DelegationFinished, Status: "completed"})
	out := subagentstream.Wrap(context.Background(), handle, subagentstream.MuxOptions{Bus: bus})

	handle.stream <- agentadaptor.StreamPayload{Kind: agentadaptor.StreamRunFinished, ThreadID: "thread-1", RunID: "run-1"}
	close(handle.stream)
	close(handle.done)
	events := collectMuxEvents(t, out, 3)
	seenSubagent := false
	for _, ev := range events[:len(events)-1] {
		if ev.AGUI.Type() == aguievents.EventTypeCustom && ev.Subagent != nil {
			seenSubagent = true
		}
	}
	if !seenSubagent {
		t.Fatalf("expected buffered subagent before terminal, got %#v", events)
	}
	if events[len(events)-1].AGUI.Type() != aguievents.EventTypeRunFinished {
		t.Fatalf("terminal should remain last, got %#v", events)
	}
}

func TestWrapAGUIExitsWhenContextCanceledAndConsumerStops(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	handle := &fakeHandle{
		stream:    make(chan agentadaptor.StreamPayload, 128),
		events:    make(chan agentadaptor.RunEvent),
		done:      make(chan struct{}),
		runID:     "run-1",
		runResult: agentadaptor.RunResult{RunID: "run-1"},
	}
	out := subagentstream.WrapAGUI(ctx, handle, subagentstream.MuxOptions{})
	for i := 0; i < 128; i++ {
		handle.stream <- agentadaptor.StreamPayload{Kind: agentadaptor.StreamRunStarted, ThreadID: "thread-1", RunID: "run-1"}
	}
	cancel()
	close(handle.stream)
	close(handle.done)
	for {
		select {
		case _, ok := <-out:
			if !ok {
				return
			}
		case <-time.After(time.Second):
			t.Fatal("WrapAGUI did not close after cancellation")
		}
	}
}

func TestWrapCancelUsesBestEffortContext(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	handle := &fakeHandle{
		stream:    make(chan agentadaptor.StreamPayload),
		events:    make(chan agentadaptor.RunEvent),
		done:      make(chan struct{}),
		runID:     "run-1",
		runResult: agentadaptor.RunResult{RunID: "run-1"},
	}
	out := subagentstream.Wrap(ctx, handle, subagentstream.MuxOptions{})
	cancel()
	for range out {
	}
	if !handle.sawLiveCancelContext() {
		t.Fatalf("expected at least one Cancel call with a live context, got errs=%v", handle.cancelErrs())
	}
}

func TestWrapCancelsSubagentSubscriptionWhenParentTerminates(t *testing.T) {
	t.Parallel()
	handle := &fakeHandle{
		stream:    make(chan agentadaptor.StreamPayload, 1),
		events:    make(chan agentadaptor.RunEvent),
		done:      make(chan struct{}),
		runID:     "run-1",
		runResult: agentadaptor.RunResult{RunID: "run-1"},
	}
	bus := newTrackingBus()
	out := subagentstream.Wrap(context.Background(), handle, subagentstream.MuxOptions{Bus: bus})
	handle.stream <- agentadaptor.StreamPayload{Kind: agentadaptor.StreamRunFinished, ThreadID: "thread-1", RunID: "run-1"}
	close(handle.stream)
	close(handle.done)
	for range out {
	}
	bus.assertCanceled(t, "run-1")
}

func TestWrapCancelsSubagentSubscriptionWhenParentStreamCloses(t *testing.T) {
	t.Parallel()
	handle := &fakeHandle{
		stream:    make(chan agentadaptor.StreamPayload),
		events:    make(chan agentadaptor.RunEvent),
		done:      make(chan struct{}),
		runID:     "run-1",
		runResult: agentadaptor.RunResult{RunID: "run-1"},
	}
	bus := newTrackingBus()
	out := subagentstream.Wrap(context.Background(), handle, subagentstream.MuxOptions{Bus: bus})
	close(handle.stream)
	close(handle.done)
	for range out {
	}
	bus.assertCanceled(t, "run-1")
}

func collectMuxEvents(t *testing.T, ch <-chan subagentstream.Event, want int) []subagentstream.Event {
	t.Helper()
	out := make([]subagentstream.Event, 0, want)
	for len(out) < want {
		select {
		case ev, ok := <-ch:
			if !ok {
				t.Fatalf("mux closed after %d events, wanted %d", len(out), want)
			}
			out = append(out, ev)
		case <-time.After(time.Second):
			t.Fatalf("timeout waiting for mux event %d/%d", len(out), want)
		}
	}
	return out
}

type trackingBus struct {
	mu      sync.Mutex
	runID   string
	ctxDone <-chan struct{}
	events  chan a2adelegation.DelegationEvent
}

func newTrackingBus() *trackingBus {
	return &trackingBus{events: make(chan a2adelegation.DelegationEvent)}
}

func (b *trackingBus) SubscribeRun(ctx context.Context, runID string) <-chan a2adelegation.DelegationEvent {
	b.mu.Lock()
	b.runID = runID
	b.ctxDone = ctx.Done()
	b.mu.Unlock()
	return b.events
}

func (b *trackingBus) assertCanceled(t *testing.T, wantRunID string) {
	t.Helper()
	b.mu.Lock()
	runID := b.runID
	done := b.ctxDone
	b.mu.Unlock()
	if runID != wantRunID {
		t.Fatalf("subscribed runID: got %q want %q", runID, wantRunID)
	}
	if done == nil {
		t.Fatal("subscription was not created")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("subagent subscription context was not canceled")
	}
}

type fakeHandle struct {
	stream    chan agentadaptor.StreamPayload
	events    chan agentadaptor.RunEvent
	done      chan struct{}
	runID     string
	runResult agentadaptor.RunResult
	runErr    error

	mu            sync.Mutex
	cancelCtxErrs []error
}

func (f *fakeHandle) Events() <-chan agentadaptor.RunEvent            { return f.events }
func (f *fakeHandle) StreamEvents() <-chan agentadaptor.StreamPayload { return f.stream }
func (f *fakeHandle) RunID() string                                   { return f.runID }
func (f *fakeHandle) Cancel(ctx context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cancelCtxErrs = append(f.cancelCtxErrs, ctx.Err())
	return nil
}
func (f *fakeHandle) DecisionRequests() <-chan agentadaptor.DecisionRequest {
	ch := make(chan agentadaptor.DecisionRequest)
	close(ch)
	return ch
}
func (f *fakeHandle) ResolveDecision(string, agentadaptor.DecisionResponse) error {
	return agentadaptor.ErrRunEnded
}
func (f *fakeHandle) Wait(ctx context.Context) (agentadaptor.RunResult, error) {
	select {
	case <-ctx.Done():
		return agentadaptor.RunResult{}, ctx.Err()
	case <-f.done:
		return f.runResult, f.runErr
	}
}

func (f *fakeHandle) cancelErrs() []error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]error(nil), f.cancelCtxErrs...)
}

func (f *fakeHandle) sawLiveCancelContext() bool {
	for _, err := range f.cancelErrs() {
		if err == nil {
			return true
		}
	}
	return false
}
