package subagentstream_test

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	aguievents "github.com/ag-ui-protocol/ag-ui/sdks/community/go/pkg/core/events"

	agentadaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/pkg/bridges/agui"
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

func TestStreamPayloadUsesStronglyTypedShape(t *testing.T) {
	t.Parallel()
	payload := subagentstream.StreamPayload(a2adelegation.DelegationEvent{
		RunID:        "run-1",
		DelegationID: "del-1",
		AgentKey:     "research",
		Kind:         a2adelegation.DelegationStatus,
		Status:       "working",
	})
	if payload.Kind != agentadaptor.StreamSubagentStatus {
		t.Fatalf("expected typed StreamSubagentStatus, got %#v", payload)
	}
	if payload.Subagent == nil || payload.Subagent.ID != "del-1" ||
		payload.Subagent.Name != "research" || payload.Subagent.Kind != "delegated" {
		t.Fatalf("unexpected SubagentRef: %#v", payload.Subagent)
	}
	if _, exists := payload.Raw["subagent_id"]; exists {
		t.Fatalf("Raw must not duplicate the typed scope: %#v", payload.Raw)
	}
}

func TestToolCallFieldsPassThroughSubagentBridge(t *testing.T) {
	t.Parallel()
	event := a2adelegation.DelegationEvent{
		RunID:            "run-1",
		ParentToolCallID: "parent-tool",
		DelegationID:     "del-1",
		AgentKey:         "research",
		Kind:             a2adelegation.DelegationToolCallStart,
		RemoteToolCallID: "remote-tool",
		ToolName:         "Bash",
		Args:             map[string]any{"command": "go test ./..."},
		Result:           map[string]any{"exit_code": 0},
	}
	custom := subagentstream.AGUICustomEvent(event).(*aguievents.CustomEvent)
	value := custom.Value.(map[string]any)
	if value["remoteToolCallId"] != "remote-tool" || value["toolName"] != "Bash" || value["args"] == nil || value["result"] == nil {
		t.Fatalf("AG-UI tool fields = %#v", value)
	}
	payload := subagentstream.StreamPayload(event)
	if payload.ToolCallID != "remote-tool" || payload.Args["command"] != "go test ./..." || payload.Result["exit_code"] != 0 {
		t.Fatalf("stream tool fields = %#v", payload)
	}
	if payload.Raw["parent_tool_call_id"] != "parent-tool" || payload.Raw["remote_tool_call_id"] != "remote-tool" {
		t.Fatalf("stream tool IDs = %#v", payload.Raw)
	}
}

func TestWrapMergesParentAndDelegationAGUIEventsCustomMode(t *testing.T) {
	t.Parallel()
	handle := &fakeHandle{
		stream:    make(chan agentadaptor.StreamPayload, 4),
		events:    make(chan agentadaptor.RunEvent),
		done:      make(chan struct{}),
		runID:     "run-1",
		runResult: agentadaptor.RunResult{RunID: "run-1"},
	}
	bus := a2adelegation.NewEventBus(8)
	out := subagentstream.Wrap(context.Background(), handle, subagentstream.MuxOptions{Bus: bus, SubagentMode: agui.SubagentAsCustom})

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

func TestWrapMergesParentAndDelegationAGUIEventsActivityMode(t *testing.T) {
	t.Parallel()
	handle := &fakeHandle{
		stream:    make(chan agentadaptor.StreamPayload, 4),
		events:    make(chan agentadaptor.RunEvent),
		done:      make(chan struct{}),
		runID:     "run-1",
		runResult: agentadaptor.RunResult{RunID: "run-1"},
	}
	bus := a2adelegation.NewEventBus(8)
	// Default mode = SubagentAsActivity.
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
	if events[1].AGUI.Type() != aguievents.EventTypeActivitySnapshot || events[1].Subagent == nil {
		t.Fatalf("second event should be ACTIVITY_SNAPSHOT for subagent, got type=%s subagent=%v", events[1].AGUI.Type(), events[1].Subagent)
	}
	assertActivitySnapshot(t, events[1].AGUI, "del-1", "research", "started")
}

func TestWrapDrainsBufferedSubagentsBeforeParentTerminalCustomMode(t *testing.T) {
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
	out := subagentstream.Wrap(context.Background(), handle, subagentstream.MuxOptions{Bus: bus, SubagentMode: agui.SubagentAsCustom})

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

func TestWrapDrainsBufferedSubagentsBeforeParentTerminalActivityMode(t *testing.T) {
	t.Parallel()
	handle := &fakeHandle{
		stream:    make(chan agentadaptor.StreamPayload, 1),
		events:    make(chan agentadaptor.RunEvent),
		done:      make(chan struct{}),
		runID:     "run-1",
		runResult: agentadaptor.RunResult{RunID: "run-1"},
	}
	bus := a2adelegation.NewEventBus(8)
	// Pre-publish started+finished so they're in the replay buffer.
	bus.Publish(a2adelegation.DelegationEvent{RunID: "run-1", DelegationID: "del-1", AgentKey: "research", Kind: a2adelegation.DelegationStarted})
	bus.Publish(a2adelegation.DelegationEvent{RunID: "run-1", DelegationID: "del-1", AgentKey: "research", Kind: a2adelegation.DelegationFinished, Status: "completed"})
	out := subagentstream.Wrap(context.Background(), handle, subagentstream.MuxOptions{Bus: bus})

	handle.stream <- agentadaptor.StreamPayload{Kind: agentadaptor.StreamRunFinished, ThreadID: "thread-1", RunID: "run-1"}
	close(handle.stream)
	close(handle.done)
	events := collectAllMuxEvents(t, out)

	// Should have ACTIVITY_SNAPSHOT + ACTIVITY_DELTA (terminal) + RUN_FINISHED.
	assertLastAGUIType(t, events, aguievents.EventTypeRunFinished)
	seenActivity := false
	for _, ev := range events[:len(events)-1] {
		if (ev.AGUI.Type() == aguievents.EventTypeActivitySnapshot || ev.AGUI.Type() == aguievents.EventTypeActivityDelta) && ev.Subagent != nil {
			seenActivity = true
		}
	}
	if !seenActivity {
		t.Fatalf("expected Activity event before terminal, got %#v", events)
	}
}

func TestWrapSynthesizesDanglingSubagentFailureBeforeParentRunFinishedCustomMode(t *testing.T) {
	t.Parallel()
	handle := &fakeHandle{
		stream:    make(chan agentadaptor.StreamPayload, 1),
		events:    make(chan agentadaptor.RunEvent),
		done:      make(chan struct{}),
		runID:     "run-1",
		runResult: agentadaptor.RunResult{RunID: "run-1"},
	}
	bus := newTrackingBus()
	out := subagentstream.Wrap(context.Background(), handle, subagentstream.MuxOptions{Bus: bus, SubagentMode: agui.SubagentAsCustom})
	bus.waitSubscribed(t, "run-1")
	bus.publish(a2adelegation.DelegationEvent{RunID: "run-1", DelegationID: "active", AgentKey: "research", Kind: a2adelegation.DelegationStarted})
	bus.publish(a2adelegation.DelegationEvent{RunID: "run-1", DelegationID: "done", AgentKey: "review", Kind: a2adelegation.DelegationStarted})
	bus.publish(a2adelegation.DelegationEvent{RunID: "run-1", DelegationID: "done", AgentKey: "review", Kind: a2adelegation.DelegationFinished, Status: "completed"})

	handle.stream <- agentadaptor.StreamPayload{Kind: agentadaptor.StreamRunFinished, ThreadID: "thread-1", RunID: "run-1"}
	close(handle.stream)
	close(handle.done)
	events := collectAllMuxEvents(t, out)

	assertLastAGUIType(t, events, aguievents.EventTypeRunFinished)
	assertSyntheticSubagentTerminalBeforeParent(t, events, "active", a2adelegation.DelegationFailed, "parent_finished")
	assertNoExtraTerminal(t, events, "done", a2adelegation.DelegationFailed)
}

func TestWrapSynthesizesDanglingSubagentFailureBeforeParentRunFinishedActivityMode(t *testing.T) {
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
	bus.waitSubscribed(t, "run-1")
	bus.publish(a2adelegation.DelegationEvent{RunID: "run-1", DelegationID: "active", AgentKey: "research", Kind: a2adelegation.DelegationStarted})
	bus.publish(a2adelegation.DelegationEvent{RunID: "run-1", DelegationID: "done", AgentKey: "review", Kind: a2adelegation.DelegationStarted})
	bus.publish(a2adelegation.DelegationEvent{RunID: "run-1", DelegationID: "done", AgentKey: "review", Kind: a2adelegation.DelegationFinished, Status: "completed"})

	handle.stream <- agentadaptor.StreamPayload{Kind: agentadaptor.StreamRunFinished, ThreadID: "thread-1", RunID: "run-1"}
	close(handle.stream)
	close(handle.done)
	events := collectAllMuxEvents(t, out)

	assertLastAGUIType(t, events, aguievents.EventTypeRunFinished)
	// In Activity mode, "active" gets a synthetic ACTIVITY_DELTA(status=failed).
	assertActivityDeltaBeforeParent(t, events, "active", "failed")
	// "done" was already terminated → no extra terminal delta for it.
	assertNoExtraActivityTerminal(t, events, "done")
}

func TestWrapSynthesizesDanglingSubagentFailureBeforeSynthesizedParentTerminalOnStreamCloseCustomMode(t *testing.T) {
	t.Parallel()
	handle := &fakeHandle{
		stream:    make(chan agentadaptor.StreamPayload),
		events:    make(chan agentadaptor.RunEvent),
		done:      make(chan struct{}),
		runID:     "run-1",
		runResult: agentadaptor.RunResult{RunID: "run-1"},
	}
	bus := newTrackingBus()
	out := subagentstream.Wrap(context.Background(), handle, subagentstream.MuxOptions{Bus: bus, SubagentMode: agui.SubagentAsCustom})
	bus.waitSubscribed(t, "run-1")
	bus.publish(a2adelegation.DelegationEvent{RunID: "run-1", DelegationID: "active", AgentKey: "research", Kind: a2adelegation.DelegationStatus, Status: "working"})

	close(handle.stream)
	close(handle.done)
	events := collectAllMuxEvents(t, out)

	assertLastAGUIType(t, events, aguievents.EventTypeRunFinished)
	assertSyntheticSubagentTerminalBeforeParent(t, events, "active", a2adelegation.DelegationFailed, "parent_finished")
}

func TestWrapSynthesizesDanglingSubagentFailureBeforeParentRunErrorCustomMode(t *testing.T) {
	t.Parallel()
	handle := &fakeHandle{
		stream:    make(chan agentadaptor.StreamPayload, 1),
		events:    make(chan agentadaptor.RunEvent),
		done:      make(chan struct{}),
		runID:     "run-1",
		runResult: agentadaptor.RunResult{RunID: "run-1"},
	}
	bus := newTrackingBus()
	out := subagentstream.Wrap(context.Background(), handle, subagentstream.MuxOptions{Bus: bus, SubagentMode: agui.SubagentAsCustom})
	bus.waitSubscribed(t, "run-1")
	bus.publish(a2adelegation.DelegationEvent{RunID: "run-1", DelegationID: "active", AgentKey: "research", Kind: a2adelegation.DelegationStarted})

	handle.stream <- agentadaptor.StreamPayload{Kind: agentadaptor.StreamRunError, ThreadID: "thread-1", RunID: "run-1", Error: &agentadaptor.RunFailure{Message: "boom", Code: "parent.error"}}
	close(handle.stream)
	close(handle.done)
	events := collectAllMuxEvents(t, out)

	assertLastAGUIType(t, events, aguievents.EventTypeRunError)
	assertSyntheticSubagentTerminalBeforeParent(t, events, "active", a2adelegation.DelegationFailed, "parent_finished")
}

func TestWrapSynthesizesDanglingSubagentCancelledOnContextCancelCustomMode(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	handle := &fakeHandle{
		stream:    make(chan agentadaptor.StreamPayload),
		events:    make(chan agentadaptor.RunEvent),
		done:      make(chan struct{}),
		runID:     "run-1",
		runResult: agentadaptor.RunResult{RunID: "run-1"},
	}
	bus := newTrackingBus()
	out := subagentstream.Wrap(ctx, handle, subagentstream.MuxOptions{Bus: bus, SubagentMode: agui.SubagentAsCustom})
	bus.waitSubscribed(t, "run-1")
	bus.publish(a2adelegation.DelegationEvent{RunID: "run-1", DelegationID: "active", AgentKey: "research", Kind: a2adelegation.DelegationStarted})

	events := collectMuxEvents(t, out, 1)
	cancel()
	events = append(events, collectAllMuxEvents(t, out)...)

	assertSubagentTerminal(t, events, "active", a2adelegation.DelegationCancelled, "parent_cancelled")
	if !handle.sawLiveCancelContext() {
		t.Fatalf("expected at least one Cancel call with a live context, got errs=%v", handle.cancelErrs())
	}
}

func TestWrapAGUIForwardsDanglingSubagentCancelledOnContextCancelCustomMode(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	handle := &fakeHandle{
		stream:    make(chan agentadaptor.StreamPayload),
		events:    make(chan agentadaptor.RunEvent),
		done:      make(chan struct{}),
		runID:     "run-1",
		runResult: agentadaptor.RunResult{RunID: "run-1"},
	}
	bus := newTrackingBus()
	out := subagentstream.WrapAGUI(ctx, handle, subagentstream.MuxOptions{Bus: bus, SubagentMode: agui.SubagentAsCustom})
	bus.waitSubscribed(t, "run-1")
	bus.publish(a2adelegation.DelegationEvent{RunID: "run-1", DelegationID: "active", AgentKey: "research", Kind: a2adelegation.DelegationStarted})

	events := collectAGUIEvents(t, out, 1)
	cancel()
	events = append(events, collectAllAGUIEvents(t, out)...)

	assertAGUICustomTerminal(t, events, "active", a2adelegation.DelegationCancelled, "parent_cancelled")
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
		case <-time.After(3 * time.Second):
			t.Fatalf("timeout waiting for mux event %d/%d", len(out), want)
		}
	}
	return out
}

func collectAllMuxEvents(t *testing.T, ch <-chan subagentstream.Event) []subagentstream.Event {
	t.Helper()
	var out []subagentstream.Event
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return out
			}
			out = append(out, ev)
		case <-time.After(3 * time.Second):
			t.Fatalf("timeout waiting for mux to close after %d events", len(out))
		}
	}
}

func assertLastAGUIType(t *testing.T, events []subagentstream.Event, want aguievents.EventType) {
	t.Helper()
	if len(events) == 0 {
		t.Fatal("expected mux events")
	}
	if events[len(events)-1].AGUI.Type() != want {
		t.Fatalf("last AG-UI event: got %s want %s; events=%#v", events[len(events)-1].AGUI.Type(), want, events)
	}
}

func assertSyntheticSubagentTerminalBeforeParent(t *testing.T, events []subagentstream.Event, delegationID string, kind a2adelegation.DelegationEventKind, code string) {
	t.Helper()
	parentTerminal := len(events) - 1
	seen := 0
	for i, ev := range events {
		if ev.Subagent == nil || ev.Subagent.DelegationID != delegationID || ev.Subagent.Kind != kind {
			continue
		}
		seen++
		if i >= parentTerminal {
			t.Fatalf("synthetic terminal for %q was not before parent terminal: index=%d parent=%d", delegationID, i, parentTerminal)
		}
		if ev.Subagent.Error == nil || ev.Subagent.Error.Code != code {
			t.Fatalf("synthetic terminal error: got %#v want code %q", ev.Subagent.Error, code)
		}
	}
	if seen != 1 {
		t.Fatalf("synthetic terminal count for %q: got %d want 1; events=%#v", delegationID, seen, events)
	}
}

func assertSubagentTerminal(t *testing.T, events []subagentstream.Event, delegationID string, kind a2adelegation.DelegationEventKind, code string) {
	t.Helper()
	seen := 0
	for _, ev := range events {
		if ev.Subagent == nil || ev.Subagent.DelegationID != delegationID || ev.Subagent.Kind != kind {
			continue
		}
		seen++
		if ev.Subagent.Error == nil || ev.Subagent.Error.Code != code {
			t.Fatalf("subagent terminal error: got %#v want code %q", ev.Subagent.Error, code)
		}
	}
	if seen != 1 {
		t.Fatalf("subagent terminal count for %q: got %d want 1; events=%#v", delegationID, seen, events)
	}
}

func collectAGUIEvents(t *testing.T, ch <-chan aguievents.Event, want int) []aguievents.Event {
	t.Helper()
	out := make([]aguievents.Event, 0, want)
	for len(out) < want {
		select {
		case ev, ok := <-ch:
			if !ok {
				t.Fatalf("AG-UI mux closed after %d events, wanted %d", len(out), want)
			}
			out = append(out, ev)
		case <-time.After(3 * time.Second):
			t.Fatalf("timeout waiting for AG-UI event %d/%d", len(out), want)
		}
	}
	return out
}

func collectAllAGUIEvents(t *testing.T, ch <-chan aguievents.Event) []aguievents.Event {
	t.Helper()
	var out []aguievents.Event
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return out
			}
			out = append(out, ev)
		case <-time.After(3 * time.Second):
			t.Fatalf("timeout waiting for AG-UI mux to close after %d events", len(out))
		}
	}
}

func assertAGUICustomTerminal(t *testing.T, events []aguievents.Event, delegationID string, kind a2adelegation.DelegationEventKind, code string) {
	t.Helper()
	seen := 0
	for _, ev := range events {
		custom, ok := ev.(*aguievents.CustomEvent)
		if !ok || custom.Name != string(kind) {
			continue
		}
		value, ok := custom.Value.(map[string]any)
		if !ok {
			t.Fatalf("custom event value: got %T", custom.Value)
		}
		if value["delegationId"] != delegationID {
			continue
		}
		seen++
		derr, ok := value["error"].(*a2adelegation.DelegationError)
		if !ok || derr.Code != code {
			t.Fatalf("custom terminal error: got %#v want code %q", value["error"], code)
		}
	}
	if seen != 1 {
		t.Fatalf("custom terminal count for %q: got %d want 1; events=%#v", delegationID, seen, events)
	}
}

func assertNoExtraTerminal(t *testing.T, events []subagentstream.Event, delegationID string, kind a2adelegation.DelegationEventKind) {
	t.Helper()
	for _, ev := range events {
		if ev.Subagent != nil && ev.Subagent.DelegationID == delegationID && ev.Subagent.Kind == kind {
			t.Fatalf("delegation %q got duplicate terminal %s in events=%#v", delegationID, kind, events)
		}
	}
}

// ---------------------------------------------------------------------------
// Activity mode helpers
// ---------------------------------------------------------------------------

func assertActivitySnapshot(t *testing.T, ev aguievents.Event, messageID, agentKey, status string) {
	t.Helper()
	snap, ok := ev.(*aguievents.ActivitySnapshotEvent)
	if !ok {
		t.Fatalf("expected *ActivitySnapshotEvent, got %T", ev)
	}
	if snap.MessageID != messageID {
		t.Fatalf("snapshot messageId: got %q want %q", snap.MessageID, messageID)
	}
	if snap.ActivityType != "subagent" {
		t.Fatalf("snapshot activityType: got %q want \"subagent\"", snap.ActivityType)
	}
	b, _ := json.Marshal(snap.Content)
	var c map[string]any
	_ = json.Unmarshal(b, &c)
	if c["agentKey"] != agentKey {
		t.Fatalf("snapshot content agentKey: got %q want %q", c["agentKey"], agentKey)
	}
	if c["status"] != status {
		t.Fatalf("snapshot content status: got %q want %q", c["status"], status)
	}
}

func assertActivityDeltaBeforeParent(t *testing.T, events []subagentstream.Event, messageID, status string) {
	t.Helper()
	parentTerminal := len(events) - 1
	for i, ev := range events {
		if ev.AGUI == nil || ev.AGUI.Type() != aguievents.EventTypeActivityDelta {
			continue
		}
		delta, ok := ev.AGUI.(*aguievents.ActivityDeltaEvent)
		if !ok || delta.MessageID != messageID {
			continue
		}
		for _, op := range delta.Patch {
			if op.Path == "/status" && op.Value == status {
				if i >= parentTerminal {
					t.Fatalf("Activity delta(%q, status=%q) was not before parent terminal: idx=%d parent=%d", messageID, status, i, parentTerminal)
				}
				return
			}
		}
	}
	t.Fatalf("no ACTIVITY_DELTA(%q, status=%q) found before parent terminal; events=%v", messageID, status, summarize(events))
}

func assertNoExtraActivityTerminal(t *testing.T, events []subagentstream.Event, messageID string) {
	t.Helper()
	for _, ev := range events {
		if ev.AGUI == nil || ev.AGUI.Type() != aguievents.EventTypeActivityDelta {
			continue
		}
		delta, ok := ev.AGUI.(*aguievents.ActivityDeltaEvent)
		if !ok || delta.MessageID != messageID {
			continue
		}
		for _, op := range delta.Patch {
			if op.Path == "/status" {
				if v, _ := op.Value.(string); v == "failed" || v == "cancelled" {
					t.Fatalf("unexpected terminal Activity delta for %q: %#v", messageID, delta.Patch)
				}
			}
		}
	}
}

func summarize(events []subagentstream.Event) []string {
	out := make([]string, 0, len(events))
	for _, ev := range events {
		if ev.AGUI != nil {
			out = append(out, string(ev.AGUI.Type()))
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// Activity mode integration tests
// ---------------------------------------------------------------------------

func TestActivityAggregatorFullLifecycle(t *testing.T) {
	t.Parallel()
	handle := &fakeHandle{
		stream:    make(chan agentadaptor.StreamPayload, 8),
		events:    make(chan agentadaptor.RunEvent),
		done:      make(chan struct{}),
		runID:     "run-1",
		runResult: agentadaptor.RunResult{RunID: "run-1"},
	}
	bus := newTrackingBus()
	out := subagentstream.Wrap(context.Background(), handle, subagentstream.MuxOptions{Bus: bus})
	bus.waitSubscribed(t, "run-1")

	handle.stream <- agentadaptor.StreamPayload{Kind: agentadaptor.StreamRunStarted, ThreadID: "t", RunID: "run-1"}

	// Full lifecycle: started → status → text delta → tool call → finished.
	bus.publish(a2adelegation.DelegationEvent{
		RunID: "run-1", DelegationID: "del-1", AgentKey: "research", AgentName: "Research", Protocol: "a2a",
		Kind: a2adelegation.DelegationStarted,
	})
	bus.publish(a2adelegation.DelegationEvent{
		RunID: "run-1", DelegationID: "del-1", AgentKey: "research",
		Kind: a2adelegation.DelegationStatus, Delta: "searching...", Status: "TASK_STATE_WORKING",
	})
	bus.publish(a2adelegation.DelegationEvent{
		RunID: "run-1", DelegationID: "del-1", AgentKey: "research",
		Kind: a2adelegation.DelegationReasoningDelta, Delta: "Choosing search terms.",
	})
	bus.publish(a2adelegation.DelegationEvent{
		RunID: "run-1", DelegationID: "del-1", AgentKey: "research",
		Kind: a2adelegation.DelegationTextDelta, Delta: "Found result.",
	})
	bus.publish(a2adelegation.DelegationEvent{
		RunID: "run-1", DelegationID: "del-1", AgentKey: "research",
		Kind: a2adelegation.DelegationToolCallStart, RemoteToolCallID: "tc-1",
		ToolName: "search", Args: map[string]any{"q": "Go SDK"},
	})
	bus.publish(a2adelegation.DelegationEvent{
		RunID: "run-1", DelegationID: "del-1", AgentKey: "research",
		Kind: a2adelegation.DelegationToolCallArgs, RemoteToolCallID: "tc-1",
		Delta: "searching page 1\n",
	})
	bus.publish(a2adelegation.DelegationEvent{
		RunID: "run-1", DelegationID: "del-1", AgentKey: "research",
		Kind: a2adelegation.DelegationToolCallResult, RemoteToolCallID: "tc-1",
		ToolName: "search", Result: map[string]any{"count": 5},
	})
	bus.publish(a2adelegation.DelegationEvent{
		RunID: "run-1", DelegationID: "del-1", AgentKey: "research",
		Kind: a2adelegation.DelegationFinished, Status: "completed", Text: "done",
	})

	handle.stream <- agentadaptor.StreamPayload{Kind: agentadaptor.StreamRunFinished, ThreadID: "t", RunID: "run-1"}
	close(handle.stream)
	close(handle.done)

	events := collectAllMuxEvents(t, out)
	assertLastAGUIType(t, events, aguievents.EventTypeRunFinished)

	// Verify Activity events.
	var types []aguievents.EventType
	for _, ev := range events {
		if ev.AGUI != nil {
			types = append(types, ev.AGUI.Type())
		}
	}
	seenSnapshot, seenDelta, seenNormalizedStatus, seenResultAdd, seenReasoning, seenToolDelta := false, false, false, false, false, false
	for _, ev := range events {
		if ev.AGUI == nil {
			continue
		}
		switch ev.AGUI.Type() {
		case aguievents.EventTypeActivitySnapshot:
			if snap, ok := ev.AGUI.(*aguievents.ActivitySnapshotEvent); ok && snap.MessageID == "del-1" {
				seenSnapshot = true
				assertActivitySnapshot(t, ev.AGUI, "del-1", "research", "started")
			}
		case aguievents.EventTypeActivityDelta:
			if delta, ok := ev.AGUI.(*aguievents.ActivityDeltaEvent); ok && delta.MessageID == "del-1" {
				seenDelta = true
				for _, operation := range delta.Patch {
					if operation.Path == "/status" && operation.Value == "running" {
						seenNormalizedStatus = true
					}
					if operation.Path == "/toolCalls/0/result" && operation.Op == "add" {
						seenResultAdd = true
					}
					if operation.Path == "/reasoning" && operation.Op == "add" &&
						operation.Value == "Choosing search terms." {
						seenReasoning = true
					}
					if operation.Path == "/toolCalls/0/args" && operation.Op == "add" {
						seenToolDelta = true
					}
				}
			}
		}
	}
	if !seenSnapshot {
		t.Fatalf("missing ACTIVITY_SNAPSHOT for del-1; event types: %v", types)
	}
	if !seenDelta {
		t.Fatalf("missing ACTIVITY_DELTA for del-1; event types: %v", types)
	}
	if !seenNormalizedStatus {
		t.Fatal("A2A task status was not normalized to the Activity status vocabulary")
	}
	if !seenResultAdd {
		t.Fatal("tool result must use JSON Patch add when the optional result field is absent")
	}
	if !seenReasoning {
		t.Fatal("reasoning delta was not mapped into Activity content")
	}
	if !seenToolDelta {
		t.Fatal("tool args/output delta was not mapped into Activity content")
	}
}

func TestActivityAggregatorDuplicateStartIsIdempotent(t *testing.T) {
	t.Parallel()
	handle := &fakeHandle{
		stream:    make(chan agentadaptor.StreamPayload, 1),
		events:    make(chan agentadaptor.RunEvent),
		done:      make(chan struct{}),
		runID:     "run-duplicate",
		runResult: agentadaptor.RunResult{RunID: "run-duplicate"},
	}
	bus := newTrackingBus()
	out := subagentstream.Wrap(context.Background(), handle, subagentstream.MuxOptions{Bus: bus})
	bus.waitSubscribed(t, "run-duplicate")

	for _, event := range []a2adelegation.DelegationEvent{
		{RunID: "run-duplicate", DelegationID: "del-duplicate", AgentKey: "worker", Kind: a2adelegation.DelegationStarted},
		{RunID: "run-duplicate", DelegationID: "del-duplicate", AgentKey: "worker", Kind: a2adelegation.DelegationTextDelta, Delta: "first"},
		{
			RunID: "run-duplicate", DelegationID: "del-duplicate", AgentKey: "worker",
			Kind: a2adelegation.DelegationToolCallStart, RemoteToolCallID: "inner", ToolName: "Read",
		},
		{RunID: "run-duplicate", DelegationID: "del-duplicate", AgentKey: "worker", Kind: a2adelegation.DelegationStarted},
		{RunID: "run-duplicate", DelegationID: "del-duplicate", AgentKey: "worker", Kind: a2adelegation.DelegationTextDelta, Delta: " second"},
		{
			RunID: "run-duplicate", DelegationID: "del-duplicate", AgentKey: "worker",
			Kind: a2adelegation.DelegationToolCallResult, RemoteToolCallID: "inner", ToolName: "Read",
			Result: map[string]any{"text": "kept"},
		},
		{RunID: "run-duplicate", DelegationID: "del-duplicate", AgentKey: "worker", Kind: a2adelegation.DelegationFinished},
	} {
		bus.publish(event)
	}
	handle.stream <- agentadaptor.StreamPayload{Kind: agentadaptor.StreamRunFinished, RunID: "run-duplicate"}
	close(handle.stream)
	close(handle.done)

	events := collectAllMuxEvents(t, out)
	var snapshots int
	var preservedText, preservedTool bool
	for _, event := range events {
		switch typed := event.AGUI.(type) {
		case *aguievents.ActivitySnapshotEvent:
			if typed.MessageID == "del-duplicate" {
				snapshots++
			}
		case *aguievents.ActivityDeltaEvent:
			if typed.MessageID != "del-duplicate" {
				continue
			}
			for _, operation := range typed.Patch {
				if operation.Path == "/text" && operation.Value == "first second" {
					preservedText = true
				}
				if operation.Path == "/toolCalls/0/result" {
					preservedTool = true
				}
			}
		}
	}
	if snapshots != 1 {
		t.Fatalf("duplicate start emitted %d snapshots, want 1; events=%v", snapshots, summarize(events))
	}
	if !preservedText || !preservedTool {
		t.Fatalf("duplicate start lost accumulated state: text=%v tool=%v", preservedText, preservedTool)
	}
}

func TestActivityAggregatorSyntheticTerminalOnContextCancel(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	handle := &fakeHandle{
		stream:    make(chan agentadaptor.StreamPayload),
		events:    make(chan agentadaptor.RunEvent),
		done:      make(chan struct{}),
		runID:     "run-2",
		runResult: agentadaptor.RunResult{RunID: "run-2"},
	}
	bus := newTrackingBus()
	out := subagentstream.Wrap(ctx, handle, subagentstream.MuxOptions{Bus: bus})
	bus.waitSubscribed(t, "run-2")
	bus.publish(a2adelegation.DelegationEvent{
		RunID: "run-2", DelegationID: "del-2", AgentKey: "impl",
		Kind: a2adelegation.DelegationStarted,
	})

	events := collectMuxEvents(t, out, 1) // wait for snapshot
	cancel()
	events = append(events, collectAllMuxEvents(t, out)...)

	// Expect ACTIVITY_DELTA with status=cancelled for del-2.
	found := false
	for _, ev := range events {
		delta, ok := ev.AGUI.(*aguievents.ActivityDeltaEvent)
		if !ok || delta.MessageID != "del-2" {
			continue
		}
		for _, op := range delta.Patch {
			if op.Path == "/status" && op.Value == "cancelled" {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("expected ACTIVITY_DELTA(status=cancelled) for del-2; events=%v", summarize(events))
	}
}

func TestMapToStreamPayloadProperKinds(t *testing.T) {
	t.Parallel()
	cases := []struct {
		kind     a2adelegation.DelegationEventKind
		wantKind agentadaptor.StreamKind
	}{
		{a2adelegation.DelegationStarted, agentadaptor.StreamSubagentStart},
		{a2adelegation.DelegationStatus, agentadaptor.StreamSubagentStatus},
		{a2adelegation.DelegationTextDelta, agentadaptor.StreamTextContent},
		{a2adelegation.DelegationReasoningDelta, agentadaptor.StreamReasoningContent},
		{a2adelegation.DelegationFinished, agentadaptor.StreamSubagentEnd},
		{a2adelegation.DelegationFailed, agentadaptor.StreamSubagentEnd},
		{a2adelegation.DelegationCancelled, agentadaptor.StreamSubagentEnd},
		{a2adelegation.DelegationInputRequired, agentadaptor.StreamSubagentEnd},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(string(tc.kind), func(t *testing.T) {
			t.Parallel()
			p := subagentstream.MapToStreamPayload(a2adelegation.DelegationEvent{
				RunID: "run-1", DelegationID: "del-1", AgentKey: "research",
				ParentToolCallID: "parent-call", RemoteTaskID: "remote-task",
				Kind: tc.kind, Status: "running", Delta: "delta text",
			})
			if p.Kind != tc.wantKind {
				t.Fatalf("MapToStreamPayload kind: got %q want %q", p.Kind, tc.wantKind)
			}
			if p.Subagent == nil || p.Subagent.ID != "del-1" ||
				p.Subagent.Name != "research" || p.Subagent.Kind != "delegated" ||
				p.Subagent.Protocol != "a2a" || p.Subagent.ToolCallID != "parent-call" {
				t.Fatalf("missing typed SubagentRef for %q: %#v", p.Kind, p.Subagent)
			}
			if _, exists := p.Raw["subagent_id"]; exists {
				t.Fatalf("Raw duplicated scope ID for %q: %#v", p.Kind, p.Raw)
			}
			if p.Raw["remote_task_id"] != "remote-task" {
				t.Fatalf("Raw omitted A2A remote ID for %q: %#v", p.Kind, p.Raw)
			}
		})
	}
}

func TestMapToStreamPayloadRejectsEmptyDelegationID(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		kind a2adelegation.DelegationEventKind
	}{
		{name: "text", kind: a2adelegation.DelegationTextDelta},
		{name: "status", kind: a2adelegation.DelegationStatus},
		{name: "tool", kind: a2adelegation.DelegationToolCallStart},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			payload := subagentstream.MapToStreamPayload(a2adelegation.DelegationEvent{
				RunID:            "run-1",
				Kind:             test.kind,
				Delta:            "must not reach parent",
				RemoteToolCallID: "inner",
				ToolName:         "Read",
			})
			if payload.Kind != "" || payload.Subagent != nil || payload.Delta != "" ||
				payload.MessageID != "" || payload.ToolCallID != "" || payload.Raw != nil {
				t.Fatalf("empty DelegationID produced non-zero payload: %#v", payload)
			}
		})
	}
}

func TestStreamPayloadDelegatesToCanonicalMapping(t *testing.T) {
	t.Parallel()
	p := subagentstream.StreamPayload(a2adelegation.DelegationEvent{
		RunID: "run-1", DelegationID: "del-1", AgentKey: "research",
		Kind: a2adelegation.DelegationStatus, Status: "working",
	})
	if p.Kind != agentadaptor.StreamSubagentStatus || p.Subagent == nil || p.Subagent.ID != "del-1" {
		t.Fatalf("StreamPayload did not use canonical mapping: %#v", p)
	}
}

type trackingBus struct {
	mu        sync.Mutex
	readyOnce sync.Once
	runID     string
	ctxDone   <-chan struct{}
	ready     chan struct{}
	events    chan a2adelegation.DelegationEvent
}

func newTrackingBus() *trackingBus {
	return &trackingBus{ready: make(chan struct{}), events: make(chan a2adelegation.DelegationEvent, 16)}
}

func (b *trackingBus) SubscribeRun(ctx context.Context, runID string) <-chan a2adelegation.DelegationEvent {
	b.mu.Lock()
	b.runID = runID
	b.ctxDone = ctx.Done()
	b.mu.Unlock()
	b.readyOnce.Do(func() { close(b.ready) })
	return b.events
}

func (b *trackingBus) waitSubscribed(t *testing.T, wantRunID string) {
	t.Helper()
	select {
	case <-b.ready:
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for subagent subscription")
	}
	b.mu.Lock()
	runID := b.runID
	b.mu.Unlock()
	if runID != wantRunID {
		t.Fatalf("subscribed runID: got %q want %q", runID, wantRunID)
	}
}

func (b *trackingBus) publish(ev a2adelegation.DelegationEvent) {
	b.events <- ev
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
