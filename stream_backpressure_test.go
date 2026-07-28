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
// marker-before-next-event ordering. Buffer 2; e1..e5 emitted with no
// consumer (e3..e5 dropped); after the consumer drains two, the next
// emission must flush Dropped{3} BEFORE itself.
func TestDropModeAggregatesAndOrders(t *testing.T) {
	emitted := make(chan struct{})
	drained := make(chan struct{})

	fake := newFakeDriver()
	fake.runFunc = func(ctx context.Context, _ driver.Request, sink driver.EventSink) (driver.Response, error) {
		for n := 1; n <= 5; n++ {
			_ = sink.EmitStream(numberedPayload(n))
		}
		close(emitted) // buffer now holds e1,e2; e3..e5 dropped
		<-drained      // consumer took e1,e2
		_ = sink.EmitStream(numberedPayload(6))
		return driver.Response{Output: "ok"}, nil
	}

	agent := adaptor.New(fake, adaptor.WithEventBuffer(2))
	st := agent.Stream(context.Background(), "overflow")

	<-emitted
	ev1 := <-st.Events()
	ev2 := <-st.Events()
	if got := deltaOf(t, ev1) + deltaOf(t, ev2); got != "e1e2" {
		t.Fatalf("first two events = %q, want e1e2 (drops never reorder survivors)", got)
	}
	close(drained)

	var rest []adaptor.Event
	for ev := range st.Events() {
		rest = append(rest, ev)
	}
	if _, err := st.Result(); err != nil {
		t.Fatalf("Result: %v", err)
	}

	if len(rest) != 2 {
		t.Fatalf("want [Dropped e6], got %#v", rest)
	}
	drop, ok := rest[0].(adaptor.Dropped)
	if !ok {
		t.Fatalf("marker must precede the event that flushed it, got %#v", rest[0])
	}
	if drop.Count != 3 {
		t.Errorf("Dropped.Count = %d, want 3 (aggregated, one marker)", drop.Count)
	}
	if got := deltaOf(t, rest[1]); got != "e6" {
		t.Errorf("event after marker = %q, want e6", got)
	}
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
		close(emitted) // buffer 4 holds e1..e4; e5,e6 dropped; nothing else emitted
		<-freed        // consumer freed a slot: the close-time flush fits
		return driver.Response{Output: "ok"}, nil
	}

	agent := adaptor.New(fake, adaptor.WithEventBuffer(4))
	st := agent.Stream(context.Background(), "overflow-then-end")
	<-emitted
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
		t.Fatalf("want e2..e4 + terminal Dropped, got %#v", events)
	}
	for n := 2; n <= 4; n++ {
		if got := deltaOf(t, events[n-2]); got != fmt.Sprintf("e%d", n) {
			t.Errorf("events[%d] = %q, want e%d", n-2, got, n)
		}
	}
	drop, ok := events[3].(adaptor.Dropped)
	if !ok || drop.Count != 2 {
		t.Errorf("terminal marker = %#v, want Dropped{2}", events[3])
	}
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
	if len(events) != 6 {
		t.Fatalf("want all 6 events, got %d: %#v", len(events), events)
	}
	for n := 1; n <= 6; n++ {
		if got := deltaOf(t, events[n-1]); got != fmt.Sprintf("e%d", n) {
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
	if len(events) != total {
		t.Fatalf("want %d events, got %d", total, len(events))
	}
	for n := 1; n <= total; n++ {
		if got := deltaOf(t, events[n-1]); got != fmt.Sprintf("e%d", n) {
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
	if len(events) != 0 {
		t.Fatalf("unexpected events: %#v", events)
	}

	// The channel is closed now; both emit paths must be silent no-ops.
	if err := captured.Emit(driver.RunEvent{Type: driver.RunEventLifecycle, Text: "late"}); err != nil {
		t.Errorf("late Emit: %v", err)
	}
	if err := captured.EmitStream(numberedPayload(99)); err != nil {
		t.Errorf("late EmitStream: %v", err)
	}
}
