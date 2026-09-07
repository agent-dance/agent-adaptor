package adaptor_test

// Backpressure contract tests. Semantic baselines (
// stream_internal_test.go, reproduced against the public API):
//
//   - default drop mode: overflow events are dropped and aggregated into ONE
//     Dropped{Count} marker, flushed before the next event that fits;
//   - a pending marker is flushed at close when the channel has room;
//   - WithEventBuffer sizes the buffer;
//   - WithBlockingEvents delivers everything, in order, with no markers;
//   - emitting on an ended run is a silent no-op (no panic).
//
// All synchronization is via channels — no sleeps.

import (
	"context"
	"fmt"
	"testing"

	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/driver"
)

// numberedPayload is a trackable text delta ("e1", "e2", ...) so drop tests
// can assert exactly which events survived and in what order.
func numberedPayload(n int) driver.StreamPayload {
	return driver.StreamPayload{Kind: driver.StreamTextContent, Delta: fmt.Sprintf("e%d", n)}
}

func deltaOf(t *testing.T, ev adaptor.Event) string {
	t.Helper()
	d, ok := ev.(adaptor.TextDelta)
	if !ok {
		t.Fatalf("want TextDelta, got %#v", ev)
	}
	return d.Text
}

// TestDropModeAggregatesAndOrders pins count, single-marker aggregation, and
// marker-before-next-event ordering. Buffer 2 holds core RunStarted and e1;
// e2..e5 are dropped. After the consumer drains both, the next emission must
// flush Dropped{4} before itself, and the core terminal remains last.
func TestDropModeAggregatesAndOrders(t *testing.T) {
	emitted := make(chan struct{})
	drained := make(chan struct{})

	fake := newFakeDriver()
	fake.runFunc = func(ctx context.Context, _ driver.Request, sink driver.EventSink) (driver.Response, error) {
		for n := 1; n <= 5; n++ {
			_ = sink.EmitStream(numberedPayload(n))
		}
		close(emitted) // buffer holds core start,e1; e2..e5 dropped
		<-drained      // consumer took core start,e1
		_ = sink.EmitStream(numberedPayload(6))
		return driver.Response{Output: "ok"}, nil
	}

	agent := adaptor.New(fake, adaptor.WithEventBuffer(2))
	st := agent.Stream(context.Background(), "overflow")

	<-emitted
	ev1 := <-st.Events()
	ev2 := <-st.Events()
	if _, ok := ev1.(adaptor.RunStarted); !ok || ev1.Meta().Sequence != 1 {
		t.Fatal("missing authoritative start", ev1)
	}
	if got := deltaOf(t, ev2); got != "e1" || ev2.Meta().Sequence != 2 {
		t.Fatal("start must occupy one normal slot", ev2)
	}
	close(drained)

	var rest []adaptor.Event
	for ev := range st.Events() {
		rest = append(rest, ev)
	}
	if _, err := st.Result(); err != nil {
		t.Fatalf("Result: %v", err)
	}

	if len(rest) != 3 {
		t.Fatalf("want [Dropped e6 RunFinished], got %#v", rest)
	}
	drop, ok := rest[0].(adaptor.Dropped)
	if !ok {
		t.Fatalf("marker must precede the event that flushed it, got %#v", rest[0])
	}
	if drop.Count != 4 || drop.ByKind["text.content"] != 4 || drop.FirstSequence != 3 || drop.LastSequence != 6 {
		t.Fatal("incomplete delta loss accounting", drop)
	}
	if got := deltaOf(t, rest[1]); got != "e6" {
		t.Errorf("event after marker = %q, want e6", got)
	}
	assertBackpressureTerminal(t, rest[2], st.RunID(), 9)
}

// TestDropModeFlushesMarkerAtClose: drops with no further emission are
// surfaced by the terminal flush when the channel has room (the consumer
// frees one slot before the run ends, so the flush deterministically fits).
func TestDropModeFlushesMarkerAtClose(t *testing.T) {
	emitted := make(chan struct{})
	freed := make(chan struct{})
	fake := newFakeDriver()
	fake.runFunc = func(ctx context.Context, _ driver.Request, sink driver.EventSink) (driver.Response, error) {
		for n := 1; n <= 6; n++ {
			_ = sink.EmitStream(numberedPayload(n))
		}
		close(emitted) // buffer 4 holds start,e1..e3; e4..e6 dropped
		<-freed        // consumer freed a slot: the close-time flush fits
		return driver.Response{Output: "ok"}, nil
	}

	agent := adaptor.New(fake, adaptor.WithEventBuffer(4))
	st := agent.Stream(context.Background(), "overflow-then-end")
	<-emitted
	if ev := <-st.Events(); ev.Meta().Sequence != 1 {
		t.Fatal(ev)
	} else if _, ok := ev.(adaptor.RunStarted); !ok {
		t.Fatal("missing core start", ev)
	}
	first := deltaOf(t, <-st.Events())
	if first != "e1" {
		t.Fatalf("first event = %q, want e1", first)
	}
	close(freed)

	events, _, err := collect(st)
	if err != nil {
		t.Fatalf("Result: %v", err)
	}
	if len(events) != 4 {
		t.Fatalf("want e2,e3,Dropped,RunFinished, got %#v", events)
	}
	for n := 2; n <= 3; n++ {
		if got := deltaOf(t, events[n-2]); got != fmt.Sprintf("e%d", n) {
			t.Errorf("events[%d] = %q, want e%d", n-2, got, n)
		}
	}
	drop, ok := events[2].(adaptor.Dropped)
	if !ok || drop.Count != 3 || drop.FirstSequence != 5 || drop.LastSequence != 7 {
		t.Errorf("terminal marker = %#v, want Dropped{3} covering e4..e6", events[2])
	}
	assertBackpressureTerminal(t, events[3], st.RunID(), 9)
}

// TestWithEventBufferSizes: a buffer big enough for the burst drops nothing.
func TestWithEventBufferSizes(t *testing.T) {
	fake := newFakeDriver()
	fake.runFunc = func(ctx context.Context, _ driver.Request, sink driver.EventSink) (driver.Response, error) {
		for n := 1; n <= 6; n++ {
			_ = sink.EmitStream(numberedPayload(n))
		}
		return driver.Response{Output: "ok"}, nil
	}
	agent := adaptor.New(fake, adaptor.WithEventBuffer(8))

	events, _, err := collect(agent.Stream(context.Background(), "fits"))
	if err != nil {
		t.Fatalf("Result: %v", err)
	}
	if len(events) != 8 {
		t.Fatalf("want start, all 6 deltas, terminal; got %d: %#v", len(events), events)
	}
	assertBackpressureStart(t, events[0])
	assertBackpressureTerminal(t, events[7], events[0].Meta().RunID, 8)
	for n := 1; n <= 6; n++ {
		if got := deltaOf(t, events[n]); got != fmt.Sprintf("e%d", n) {
			t.Errorf("events[%d] = %q, want e%d", n-1, got, n)
		}
	}
}

// TestBlockingEventsNeverDrop: WithBlockingEvents delivers every event in
// order through a 1-slot buffer — the driver waits for the consumer instead
// of dropping.
func TestBlockingEventsNeverDrop(t *testing.T) {
	const total = 16
	fake := newFakeDriver()
	fake.runFunc = func(ctx context.Context, _ driver.Request, sink driver.EventSink) (driver.Response, error) {
		for n := 1; n <= total; n++ {
			_ = sink.EmitStream(numberedPayload(n))
		}
		return driver.Response{Output: "ok"}, nil
	}
	agent := adaptor.New(fake, adaptor.WithEventBuffer(1), adaptor.WithBlockingEvents())

	events, _, err := collect(agent.Stream(context.Background(), "no-drop"))
	if err != nil {
		t.Fatalf("Result: %v", err)
	}
	if len(events) != total+2 {
		t.Fatalf("want %d events including core envelope, got %d", total+2, len(events))
	}
	assertBackpressureStart(t, events[0])
	assertBackpressureTerminal(t, events[total+1], events[0].Meta().RunID, total+2)
	for n := 1; n <= total; n++ {
		if got := deltaOf(t, events[n]); got != fmt.Sprintf("e%d", n) {
			t.Fatalf("events[%d] = %q, want e%d (blocking mode must preserve order)", n-1, got, n)
		}
	}
}

// TestEmitAfterRunEndIsNoop: a misbehaving driver that retains the sink and
// emits after the run ended must not panic or corrupt anything.
func TestEmitAfterRunEndIsNoop(t *testing.T) {
	var captured driver.EventSink
	fake := newFakeDriver()
	fake.runFunc = func(ctx context.Context, _ driver.Request, sink driver.EventSink) (driver.Response, error) {
		captured = sink
		return driver.Response{Output: "ok"}, nil
	}
	agent := adaptor.New(fake)

	events, res, err := collect(agent.Stream(context.Background(), "retain"))
	if err != nil || res.Text != "ok" {
		t.Fatalf("run: res=%v err=%v", res, err)
	}
	if len(events) != 2 {
		t.Fatalf("want only the core envelope: %#v", events)
	}
	assertBackpressureStart(t, events[0])
	assertBackpressureTerminal(t, events[1], res.RunID, 2)

	// The channel is closed now; both emit paths must be silent no-ops.
	if err := captured.Emit(driver.RunEvent{Type: driver.RunEventLifecycle, Text: "late"}); err != nil {
		t.Errorf("late Emit: %v", err)
	}
	if err := captured.EmitStream(numberedPayload(99)); err != nil {
		t.Errorf("late EmitStream: %v", err)
	}
}

func assertBackpressureStart(t *testing.T, ev adaptor.Event) {
	t.Helper()
	if _, ok := ev.(adaptor.RunStarted); !ok || ev.Meta().Sequence != 1 || ev.Meta().RunID == "" {
		t.Fatal("invalid core start", ev)
	}
}
func assertBackpressureTerminal(t *testing.T, ev adaptor.Event, runID string, sequence uint64) {
	t.Helper()
	if terminal, ok := ev.(adaptor.RunFinished); !ok || terminal.Failed || terminal.Meta().RunID != runID || terminal.Meta().Sequence != sequence {
		t.Fatal("invalid core terminal", ev)
	}
}
