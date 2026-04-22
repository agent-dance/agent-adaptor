package agentadaptor

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestStreamOptionsTriStateMerging(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		bindingDef  *bool
		perCall     *bool
		wantEnabled bool
	}{
		{"all defaults", nil, nil, false},
		{"binding enabled", boolPtr(true), nil, true},
		{"binding disabled explicit", boolPtr(false), nil, false},
		{"per-call override binding default", boolPtr(true), boolPtr(false), false},
		{"per-call enables when binding nil", nil, boolPtr(true), true},
		{"per-call enables when binding false", boolPtr(false), boolPtr(true), true},
		{"per-call disables when binding true", boolPtr(true), boolPtr(false), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			defaults := AgentDefaults{Streaming: tc.bindingDef}
			opts := runOptions{streaming: tc.perCall}

			got := false
			if defaults.Streaming != nil {
				got = *defaults.Streaming
			}
			if opts.streaming != nil {
				got = *opts.streaming
			}
			if got != tc.wantEnabled {
				t.Fatalf("tri-state merge: got %v want %v", got, tc.wantEnabled)
			}
		})
	}
}

func TestSeqSinkAssignsMonotonicSequences(t *testing.T) {
	t.Parallel()
	recorder := &recorderSink{}
	sink := wrapWithSeq(recorder)

	const streamCount = 25
	const eventCount = 13

	for i := 0; i < eventCount; i++ {
		if err := sink.Emit(newEvent(RunEventLifecycle, "e")); err != nil {
			t.Fatalf("emit %d: %v", i, err)
		}
	}
	for i := 0; i < streamCount; i++ {
		if err := sink.EmitStream(StreamPayload{Kind: StreamTextContent, Delta: "x"}); err != nil {
			t.Fatalf("emit stream %d: %v", i, err)
		}
	}

	if got := recorder.events; len(got) != eventCount {
		t.Fatalf("expected %d events; got %d", eventCount, len(got))
	}
	for i, ev := range recorder.events {
		if ev.Seq != uint64(i+1) {
			t.Fatalf("event %d seq: want %d got %d", i, i+1, ev.Seq)
		}
		if ev.Timestamp.IsZero() {
			t.Fatalf("event %d timestamp not backfilled", i)
		}
	}

	if got := recorder.streams; len(got) != streamCount {
		t.Fatalf("expected %d stream payloads; got %d", streamCount, len(got))
	}
	for i, p := range recorder.streams {
		if p.Sequence != uint64(i+1) {
			t.Fatalf("stream %d sequence: want %d got %d", i, i+1, p.Sequence)
		}
		if p.Timestamp.IsZero() {
			t.Fatalf("stream %d timestamp not backfilled", i)
		}
	}
}

func TestSeqSinkIndependentCountersUnderConcurrency(t *testing.T) {
	t.Parallel()
	recorder := &recorderSink{}
	sink := wrapWithSeq(recorder)

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				_ = sink.Emit(newEvent(RunEventLifecycle, "e"))
				_ = sink.EmitStream(StreamPayload{Kind: StreamTextContent})
			}
		}()
	}
	wg.Wait()

	recorder.mu.Lock()
	defer recorder.mu.Unlock()

	maxEventSeq := uint64(0)
	for _, ev := range recorder.events {
		if ev.Seq == 0 {
			t.Fatalf("unassigned event seq")
		}
		if ev.Seq > maxEventSeq {
			maxEventSeq = ev.Seq
		}
	}
	maxStreamSeq := uint64(0)
	for _, p := range recorder.streams {
		if p.Sequence == 0 {
			t.Fatalf("unassigned stream sequence")
		}
		if p.Sequence > maxStreamSeq {
			maxStreamSeq = p.Sequence
		}
	}
	if uint64(len(recorder.events)) != maxEventSeq {
		t.Fatalf("event seq has gaps: count=%d max=%d", len(recorder.events), maxEventSeq)
	}
	if uint64(len(recorder.streams)) != maxStreamSeq {
		t.Fatalf("stream seq has gaps: count=%d max=%d", len(recorder.streams), maxStreamSeq)
	}
}

func TestDualSinkDisabledStreamChannelClosedImmediately(t *testing.T) {
	t.Parallel()
	sink := newDualSink("run-test", false, 4, 4, BackpressureDropStream)
	defer sink.close()

	select {
	case _, ok := <-sink.stream:
		if ok {
			t.Fatal("disabled stream channel should be closed and empty")
		}
	case <-time.After(50 * time.Millisecond):
		t.Fatal("StreamEvents on disabled sink did not close the channel")
	}

	// EmitStream on a disabled sink is a no-op.
	if err := sink.EmitStream(StreamPayload{Kind: StreamTextContent}); err != nil {
		t.Fatalf("EmitStream on disabled sink returned error: %v", err)
	}
}

func TestDualSinkDropStreamEmitsMarker(t *testing.T) {
	t.Parallel()
	sink := newDualSink("run-test", true, 4, 2, BackpressureDropStream)
	defer sink.close()

	// Fill the stream buffer. Buffer size is 2.
	for i := 0; i < cap(sink.stream); i++ {
		if err := sink.EmitStream(StreamPayload{Kind: StreamTextContent, Delta: "full"}); err != nil {
			t.Fatalf("fill buffer: %v", err)
		}
	}
	// Next two emits overflow and should be counted as drops.
	_ = sink.EmitStream(StreamPayload{Kind: StreamTextContent, Delta: "drop1"})
	_ = sink.EmitStream(StreamPayload{Kind: StreamTextContent, Delta: "drop2"})

	// Fully drain the buffer so the marker + the next payload can both be
	// delivered without racing the flush's non-blocking select.
	<-sink.stream
	<-sink.stream

	// The next emit flushes the accumulated drop marker first, then the
	// payload itself.
	if err := sink.EmitStream(StreamPayload{Kind: StreamTextContent, Delta: "post"}); err != nil {
		t.Fatalf("post-drain emit: %v", err)
	}

	// First payload after drain must be the marker (flushDropped runs before
	// the channel send). Second payload must be the "post" delta.
	markerFound := false
	postFound := false
	deadline := time.After(500 * time.Millisecond)
	for !markerFound || !postFound {
		select {
		case p, ok := <-sink.stream:
			if !ok {
				t.Fatal("stream channel closed unexpectedly")
			}
			switch p.Kind {
			case StreamDropped:
				if count, _ := p.Raw["dropped_count"].(int); count != 2 {
					t.Fatalf("dropped_count: want 2 got %v", p.Raw["dropped_count"])
				}
				if markerFound {
					t.Fatal("received duplicate StreamDropped marker")
				}
				markerFound = true
			case StreamTextContent:
				if p.Delta == "post" {
					if !markerFound {
						t.Fatal("received post payload before the marker")
					}
					postFound = true
				}
			}
		case <-deadline:
			t.Fatalf("timeout: markerFound=%v postFound=%v", markerFound, postFound)
		}
	}
}

func TestDualSinkBlockModeWaitsForConsumer(t *testing.T) {
	t.Parallel()
	sink := newDualSink("run-test", true, 4, 1, BackpressureBlock)
	defer sink.close()

	// First emit fills the buffer.
	if err := sink.EmitStream(StreamPayload{Kind: StreamTextContent, Delta: "a"}); err != nil {
		t.Fatalf("emit 1: %v", err)
	}

	// Second emit must block until the consumer drains.
	var released atomic.Bool
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = sink.EmitStream(StreamPayload{Kind: StreamTextContent, Delta: "b"})
		released.Store(true)
	}()

	select {
	case <-done:
		t.Fatal("EmitStream in Block mode returned before consumer drained")
	case <-time.After(30 * time.Millisecond):
	}
	if released.Load() {
		t.Fatal("EmitStream in Block mode did not block")
	}

	// Drain the first payload; EmitStream should return.
	<-sink.stream
	select {
	case <-done:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("EmitStream did not unblock after consumer drained")
	}
	<-sink.stream
}

func TestDualSinkBlockModeCloseUnblocks(t *testing.T) {
	t.Parallel()
	sink := newDualSink("run-test", true, 4, 1, BackpressureBlock)

	_ = sink.EmitStream(StreamPayload{Kind: StreamTextContent})

	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = sink.EmitStream(StreamPayload{Kind: StreamTextContent})
	}()

	time.Sleep(20 * time.Millisecond) // let the producer reach its select
	sink.close()

	select {
	case <-done:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("close() did not unblock a pending EmitStream")
	}
}

func TestDualSinkEmitStreamAfterCloseDoesNotPanic(t *testing.T) {
	t.Parallel()
	sink := newDualSink("run-test", true, 4, 4, BackpressureDropStream)
	sink.close()

	// Should not panic, should not error.
	if err := sink.EmitStream(StreamPayload{Kind: StreamTextContent}); err != nil {
		t.Fatalf("EmitStream after close returned error: %v", err)
	}
}

// recorderSink captures events and stream payloads for assertions.
type recorderSink struct {
	mu      sync.Mutex
	events  []RunEvent
	streams []StreamPayload
}

func (r *recorderSink) Emit(event RunEvent) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, event)
	return nil
}

func (r *recorderSink) EmitStream(payload StreamPayload) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.streams = append(r.streams, payload)
	return nil
}

func boolPtr(v bool) *bool { return &v }
