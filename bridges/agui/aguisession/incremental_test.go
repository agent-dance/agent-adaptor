package aguisession

import (
	"context"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	events "github.com/ag-ui-protocol/ag-ui/sdks/community/go/pkg/core/events"
	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/bridges/agui"
	"github.com/agent-dance/agent-adaptor/hosttools/sessionrecorder"
)

type countedRead struct {
	after sessionrecorder.HostSeq
	count int
}

type countingRecorder struct {
	sessionrecorder.EventRecorder
	mu        sync.Mutex
	zeroReads int
	returned  int
	reads     chan countedRead
}

func (r *countingRecorder) Since(
	ctx context.Context, key string, after sessionrecorder.HostSeq,
) ([]sessionrecorder.EventRecord, error) {
	records, err := r.EventRecorder.Since(ctx, key, after)
	r.mu.Lock()
	if after == 0 {
		r.zeroReads++
	}
	r.returned += len(records)
	r.mu.Unlock()
	select {
	case r.reads <- countedRead{after: after, count: len(records)}:
	default:
	}
	return records, err
}

func (r *countingRecorder) Subscribe(ctx context.Context, key string) (<-chan struct{}, func(), error) {
	return r.EventRecorder.(sessionrecorder.Follower).Subscribe(ctx, key)
}

func recordEvent(
	t *testing.T, r sessionrecorder.EventRecorder, key string, ev adaptor.Event,
) sessionrecorder.EventRecord {
	t.Helper()
	if n, ok := ev.(adaptor.Notice); ok && n.Kind == BoundaryKind {
		if n.Data == nil {
			n.Data = map[string]any{}
		}
		n.Data["projectionVersion"] = ProjectionVersion
		ev = n
	}
	rec, err := r.Record(context.Background(), RecorderKey(key), ev)
	if err != nil {
		t.Fatal(err)
	}
	return rec
}

func TestObserveProjectsIncrementally(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	r := &countingRecorder{EventRecorder: recorder(t), reads: make(chan countedRead, 64)}
	var translated atomic.Int64
	s := New(fakeRunner{}, Options{Recorder: r, Events: []agui.EventTranslatorOption{
		agui.WithProjectionHook(func(ev events.Event) []events.Event {
			translated.Add(1)
			return []events.Event{ev}
		}),
	}})
	key := ThreadKey("long")
	boundary := recordEvent(t, r, key, adaptor.Notice{Kind: BoundaryKind, Data: map[string]any{
		"threadId": "long", "runId": "r",
	}})
	recordEvent(t, r, key, adaptor.TextDelta{MessageID: "a", Phase: adaptor.PhaseStart})
	var last sessionrecorder.EventRecord
	for i := 0; i < 3000; i++ {
		last = recordEvent(t, r, key, adaptor.TextDelta{
			MessageID: "a", Phase: adaptor.PhaseContent, Text: "x",
		})
	}
	// Isolate the follow reader from HTTP admission reads. These records model
	// an active run owned by the Session, with a preexisting 3002-record prefix.
	s.mu.Lock()
	s.active[key] = true
	s.boundaries[key] = boundary.HostSeq
	s.mu.Unlock()
	defer s.release(key)
	progress := make(chan sessionrecorder.HostSeq, 3100)
	done := make(chan error, 1)
	go func() {
		done <- s.Observe(ctx, key, 1000, func(rec ProjectedRecord) error {
			progress <- rec.HostSeq
			return nil
		})
	}()
	waitFor := func(seq sessionrecorder.HostSeq) {
		t.Helper()
		for {
			select {
			case got := <-progress:
				if got == seq {
					return
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
		}
	}
	waitFor(last.HostSeq)
	projectedPrefix := translated.Load()
	// Exercise idle wakeups too: they must not re-read or re-project the prefix.
	for idleReads := 0; idleReads < 2; {
		select {
		case read := <-r.reads:
			if read.after == last.HostSeq && read.count == 0 {
				idleReads++
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	if translated.Load() != projectedPrefix {
		t.Fatal("idle wakeups re-projected old events")
	}
	for i := 0; i < 12; i++ {
		last = recordEvent(t, r, key, adaptor.TextDelta{
			MessageID: "a", Phase: adaptor.PhaseContent, Text: "y",
		})
		waitFor(last.HostSeq)
	}
	if got := translated.Load() - projectedPrefix; got != 12 {
		t.Fatalf("projected %d events for 12 new content records", got)
	}
	last = recordEvent(t, r, key, adaptor.Notice{Kind: TerminalKind})
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.zeroReads != 1 || r.returned != int(last.HostSeq) {
		t.Fatalf("full reads=%d, returned records=%d, want one full read and %d total records",
			r.zeroReads, r.returned, last.HostSeq)
	}
}

func TestIncrementalIncompleteRun(t *testing.T) {
	for _, tc := range []struct {
		name         string
		failed       bool
		nextBoundary bool
	}{
		{name: "missing_terminal"},
		{name: "recording_failed", failed: true},
		{name: "next_boundary", nextBoundary: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := recorder(t)
			s := New(fakeRunner{}, Options{Recorder: r, Unattended: true, BaseContext: context.Background()})
			key := ThreadKey("t")
			boundary := recordEvent(t, r, key, adaptor.Notice{Kind: BoundaryKind, Data: map[string]any{
				"threadId": "t", "runId": "r",
			}})
			s.mu.Lock()
			s.active[key] = true
			s.boundaries[key] = boundary.HostSeq
			s.mu.Unlock()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			delivered := make(chan struct{})
			done := make(chan error, 1)
			go func() {
				done <- s.Observe(ctx, key, 0, func(ProjectedRecord) error {
					close(delivered)
					return nil
				})
			}()
			await(t, delivered)
			if tc.nextBoundary {
				recordEvent(t, r, key, adaptor.Notice{Kind: BoundaryKind, Data: map[string]any{
					"threadId": "t", "runId": "replacement",
				}})
			} else {
				s.mu.Lock()
				delete(s.active, key)
				s.recordingFailures[key] = tc.failed
				s.mu.Unlock()
			}
			defer s.release(key)
			select {
			case err := <-done:
				want := "run.incomplete"
				if tc.failed {
					want = "agui.recording_failed"
				}
				input, ok := err.(*agui.InputError)
				if !ok || input.Code != want {
					t.Fatalf("got %v, want %s", err, want)
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
		})
	}
}

func TestProjectorBatchBoundariesPreserveIncompleteHistory(t *testing.T) {
	r := recorder(t)
	key := ThreadKey("t")
	for _, ev := range []adaptor.Event{
		adaptor.Notice{Kind: BoundaryKind, Data: map[string]any{"threadId": "t", "runId": "abandoned"}},
		adaptor.Thinking{MessageID: "reason", Text: "thinking"},
		adaptor.TextDelta{MessageID: "partial", Text: "partial"},
		adaptor.Notice{Kind: BoundaryKind, Data: map[string]any{"threadId": "t", "runId": "next"}},
		adaptor.TextDelta{MessageID: "next", Text: "answer"},
	} {
		recordEvent(t, r, key, ev)
	}
	raw, err := r.Since(context.Background(), RecorderKey(key), 0)
	if err != nil {
		t.Fatal(err)
	}
	full := newProjector(nil).project(raw, 0, true, "run.incomplete")
	incremental := newProjector(nil)
	var batched []ProjectedRecord
	appendBatch := func(batch []ProjectedRecord) {
		for _, rec := range batch {
			if len(batched) > 0 && batched[len(batched)-1].HostSeq == rec.HostSeq {
				batched[len(batched)-1].Events = append(batched[len(batched)-1].Events, rec.Events...)
			} else {
				batched = append(batched, rec)
			}
		}
	}
	for _, rec := range raw {
		appendBatch(incremental.project([]sessionrecorder.EventRecord{rec}, 0, false, "run.incomplete"))
	}
	appendBatch(incremental.project(nil, 0, true, "run.incomplete"))
	if !reflect.DeepEqual(flattened(t, full), flattened(t, batched)) {
		t.Fatal("incomplete-run projection changed at batch boundaries")
	}
}
