package aguisession

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	events "github.com/ag-ui-protocol/ag-ui/sdks/community/go/pkg/core/events"
	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/bridges/agui"
	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/hosttools/sessionrecorder"
	"github.com/agent-dance/agent-adaptor/memory"
)

// Freeze the first read at an explicitly chosen cut, while permitting a writer
// to complete a whole admission/run. No sleeps or concurrent option mutations.
func TestObserveStableReadCut(t *testing.T) {
	for _, transition := range []string{"idle_to_complete", "idle_to_active", "active_to_complete", "consecutive_runs"} {
		t.Run(transition, func(t *testing.T) {
			base := recorder(t)
			key := ThreadKey("t")
			rec := &staleReadRecorder{EventRecorder: base, key: RecorderKey(key), entered: make(chan struct{}), release: make(chan struct{})}
			s := New(fakeRunner{}, Options{Recorder: rec})
			publish := func(run string) {
				if err := s.reserve(context.Background(), key, run, nil); err != nil {
					t.Fatal(err)
				}
				recordEvent(t, base, key, adaptor.Notice{Kind: BoundaryKind, Data: map[string]any{"threadId": "t", "runId": run}})
				rs, err := base.Since(context.Background(), RecorderKey(key), 0)
				if err != nil {
					t.Fatal(err)
				}
				s.mu.Lock()
				s.boundaries[key] = rs[len(rs)-1].HostSeq
				s.changeLocked(key)
				s.mu.Unlock()
				recordEvent(t, base, key, adaptor.TextDelta{MessageID: "a", Text: run})
			}
			complete := func() {
				recordEvent(t, base, key, adaptor.Notice{Kind: TerminalKind})
				s.release(key)
			}
			// The barrier is not used while setting up the initial run.
			if strings.HasPrefix(transition, "active") || transition == "consecutive_runs" {
				s.opts.Recorder = base
				publish("first")
				s.opts.Recorder = rec
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			type result struct {
				records []ProjectedRecord
				err     error
			}
			done := make(chan result, 1)
			progress := make(chan uint64, 16)
			go func() {
				var records []ProjectedRecord
				err := s.Observe(ctx, key, 0, func(rec ProjectedRecord) error {
					records = append(records, rec)
					progress <- rec.HostSeq
					return nil
				})
				done <- result{records, err}
			}()
			await(t, rec.entered)
			// Subsequent writes use the unblocked recorder, but Session state uses the
			// same production reserve/release/publication transitions.
			// reserve's read is not the gated first read
			if transition == "active_to_complete" || transition == "consecutive_runs" {
				complete()
			}
			if transition != "active_to_complete" {
				publish("next")
			}
			if transition != "idle_to_active" && transition != "active_to_complete" {
				complete()
			}
			close(rec.release)
			if transition == "idle_to_active" {
				// Ensure the stale prefix did not terminate this still-active run.
				// A cursor-visible source record proves the observer has caught up.
				for seq := uint64(0); seq != 2; {
					select {
					case seq = <-progress:
					case <-ctx.Done():
						t.Fatal(ctx.Err())
					}
				}
				complete()
			}
			got := <-done
			if got.err != nil {
				t.Fatal(got.err)
			}
			raw, err := base.Since(ctx, RecorderKey(key), 0)
			if err != nil || len(got.records) != len(raw) {
				t.Fatalf("lost suffix: %d/%d: %v", len(got.records), len(raw), err)
			}
			for i, rec := range got.records {
				if rec.HostSeq != raw[i].HostSeq {
					t.Fatalf("cursor %d: %d/%d", i, rec.HostSeq, raw[i].HostSeq)
				}
			}
			es := flattened(t, got.records)
			for _, ev := range es {
				if str(ev, "type") == "RUN_ERROR" {
					t.Fatalf("fabricated failure: %v", ev)
				}
			}
			if str(es[len(es)-1], "type") != "RUN_FINISHED" {
				t.Fatal(es)
			}
		})
	}
}

// A later admission can begin in the delivery callback for a prior terminal.
// Observe must wait for its unpublished boundary rather than closing on the old
// terminal, then preserve both runs in HostSeq order.
func TestObserveRunStartsDuringDelivery(t *testing.T) {
	base := recorder(t)
	s := New(fakeRunner{}, Options{Recorder: base})
	key := ThreadKey("t")
	recordCompletedTurn(t, base, key)
	var out []ProjectedRecord
	if err := s.Observe(context.Background(), key, 0, func(rec ProjectedRecord) error {
		out = append(out, rec)
		if rec.HostSeq == 8 {
			if err := s.reserve(context.Background(), key, "second", nil); err != nil {
				return err
			}
			next := recordEvent(t, base, key, adaptor.Notice{Kind: BoundaryKind, Data: map[string]any{"threadId": "t", "runId": "second"}})
			s.mu.Lock()
			s.boundaries[key] = next.HostSeq
			s.changeLocked(key)
			s.mu.Unlock()
			recordEvent(t, base, key, adaptor.Notice{Kind: TerminalKind})
			s.release(key)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(out) != 10 {
		t.Fatalf("consecutive run truncated: %d records", len(out))
	}
	for i, rec := range out {
		if rec.HostSeq != uint64(i+1) {
			t.Fatal(out)
		}
	}
	var runs []string
	for _, ev := range flattened(t, out) {
		if str(ev, "type") == "RUN_STARTED" {
			runs = append(runs, str(ev, "runId"))
		}
	}
	if !reflect.DeepEqual(runs, []string{"completed", "second"}) {
		t.Fatal(runs)
	}
}

func TestObserverSeqPreservesOpaqueMetadata(t *testing.T) {
	ns := map[string]any{"number": json.Number("9007199254740993"), "huge": json.Number("1e999")}
	metadata := map[string]any{"agentAdaptor": ns, "outside": json.Number("9007199254740995")}
	ev := events.NewTextMessageStartEvent("a")
	ev.GetBaseEvent().Metadata = metadata
	stampRecord(ProjectedRecord{HostSeq: 42, Events: []events.Event{ev}})
	if _, ok := ns["seq"]; ok {
		t.Fatal("mutated shared hook metadata")
	}
	raw, err := ev.ToJSON()
	if err != nil || !strings.Contains(string(raw), "9007199254740993") || !strings.Contains(string(raw), "1e999") || !strings.Contains(string(raw), "9007199254740995") {
		t.Fatalf("corrupted metadata: %s, %v", raw, err)
	}
}

type admissionRecorder struct {
	sessionrecorder.EventRecorder
	entered chan struct{}
	once    sync.Once
}

func (r *admissionRecorder) Since(ctx context.Context, key string, after uint64) ([]sessionrecorder.EventRecord, error) {
	blocked := false
	r.once.Do(func() { close(r.entered); blocked = true })
	if blocked {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return r.EventRecorder.Since(ctx, key, after)
}

func TestCancelAndShutdownInterruptAdmission(t *testing.T) {
	for _, action := range []string{"cancel", "shutdown", "base_context"} {
		t.Run(action, func(t *testing.T) {
			rec := &admissionRecorder{EventRecorder: recorder(t), entered: make(chan struct{})}
			base, stop := context.WithCancel(context.Background())
			defer stop()
			var calls atomic.Int32
			s := New(fakeRunner{func(context.Context, string) adaptor.Stream { calls.Add(1); return script(nil, nil) }}, Options{Recorder: rec, Unattended: true, BaseContext: base})
			disconnect, post := launchRequest(s, "t", "r")
			defer disconnect()
			await(t, rec.entered)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			switch action {
			case "cancel":
				if err := s.Cancel(ctx, ThreadKey("t")); err != nil {
					t.Fatal(err)
				}
			case "shutdown":
				if err := s.Shutdown(ctx); err != nil {
					t.Fatal(err)
				}
			case "base_context":
				stop()
			}
			select {
			case w := <-post:
				if w.Code != 500 || !json.Valid(w.Body.Bytes()) {
					t.Fatal(w.Code, w.Body)
				}
			case <-ctx.Done():
				t.Fatal("admission did not cancel")
			}
			if calls.Load() != 0 || s.Active(ThreadKey("t")) {
				t.Fatal("cancelled admission executed or retained key")
			}
			if err := s.Shutdown(ctx); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestHostContextAndBoundedRepeatedShutdown(t *testing.T) {
	type contextKey struct{}
	base, stop := context.WithCancel(context.WithValue(context.Background(), contextKey{}, "worker"))
	defer stop()
	started, release := make(chan struct{}), make(chan struct{})
	// Intentionally defer cancellation acknowledgement to prove caller deadlines
	// bound Shutdown, and repeated callers share one completion signal.
	runner := fakeRunner{func(ctx context.Context, _ string) adaptor.Stream {
		if ctx.Value(contextKey{}) != "worker" {
			t.Error("lost host context")
		}
		ch := make(chan adaptor.Event)
		go func() { close(started); <-release; close(ch) }()
		return &pacedStream{ctx: ctx, ch: ch}
	}}
	s := New(runner, Options{Recorder: recorder(t), Unattended: true, BaseContext: base})
	disconnect, post := launchRequest(s, "t", "r")
	defer disconnect()
	await(t, started)
	before := runtime.NumGoroutine()
	for i := 0; i < 30; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := s.Shutdown(ctx); !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	}
	// Sampling is supplementary: the structural oracle is a shared channel with
	// exactly one close on running==0, exercised below by concurrent waiters.
	if extra := runtime.NumGoroutine() - before; extra > 5 {
		t.Fatalf("Shutdown added %d waiting goroutines", extra)
	}
	const waiters = 10
	results := make(chan error, waiters)
	for i := 0; i < waiters; i++ {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			results <- s.Shutdown(ctx)
		}()
	}
	close(release)
	for i := 0; i < waiters; i++ {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	<-post
	if w := request(s, "new", "new", []Message{user("u", "go")}); w.Code != 503 || !strings.Contains(w.Body.String(), "agui.session_closed") {
		t.Fatal(w.Code, w.Body)
	}
}

func TestObservationHTTPFailuresAndMethods(t *testing.T) {
	for _, tc := range []struct {
		method, path, allow string
		status              int
		cancel              bool
	}{
		{"POST", "/?thread_id=t", "GET", 405, false},
		{"GET", "/", "", 400, false},
		{"GET", "/?thread_id=t&after=-1", "", 400, false},
		{"GET", "/?thread_id=t&after=x", "", 400, false},
		{"GET", "/?thread_id=t&after=18446744073709551616", "", 400, false},
		{"GET", "/?thread_id=t", "POST", 405, true},
		{"POST", "/", "", 400, true},
	} {
		s := New(fakeRunner{}, Options{Recorder: recorder(t)})
		w := httptest.NewRecorder()
		h := s.ObserveHandler()
		if tc.cancel {
			h = s.CancelHandler()
		}
		h.ServeHTTP(w, httptest.NewRequest(tc.method, tc.path, nil))
		if w.Code != tc.status || !json.Valid(w.Body.Bytes()) || w.Header().Get("Allow") != tc.allow || w.Header().Get("Content-Type") != "application/json" {
			t.Fatalf("%+v: %d %v %s", tc, w.Code, w.Header(), w.Body)
		}
	}
	for _, mode := range []string{"flush", "deadline", "ping"} {
		t.Run(mode, func(t *testing.T) {
			s, _, started := unattended(t, 1)
			s.opts.WriteTimeout = 10 * time.Millisecond
			s.opts.KeepAlivePing = 5 * time.Millisecond
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var w http.ResponseWriter
			stall := &stalledWriter{header: make(http.Header)}
			switch mode {
			case "flush":
				w = &flushFailureWriter{header: make(http.Header)}
			case "deadline":
				w = stall
			case "ping":
				stall.ping = cancel
				w = stall
			}
			done := make(chan struct{})
			go func() {
				defer close(done)
				s.Handler().ServeHTTP(w, httptest.NewRequest("POST", "/", strings.NewReader(`{"threadId":"t","runId":"r","messages":[{"id":"u","role":"user","content":"go"}]}`)).WithContext(ctx))
			}()
			await(t, started)
			await(t, done)
			if !s.Active(ThreadKey("t")) {
				t.Fatal("broken observer cancelled run")
			}
			if mode == "deadline" && !stall.deadline.IsZero() {
				t.Fatal("deadline not cleared")
			}
			if mode == "ping" && ctx.Err() == nil {
				t.Fatal("missing heartbeat")
			}
		})
	}
}

// Revoke permits the in-flight callback to finish, then prevents all further
// records; it does not append a provider terminal or permanently deny new reads.
func TestRevokeDuringDeliveryAndAuthorizationRemap(t *testing.T) {
	for _, mode := range []string{"revoke", "remap", "deny"} {
		t.Run(mode, func(t *testing.T) {
			s, input, started := unattended(t, 1)
			var access atomic.Int32
			s.opts.Resolve = func(_ *http.Request, a Access) (string, error) {
				if a.Op == OpFollow && access.Load() > 0 {
					if mode == "deny" {
						return "", errors.New("denied")
					}
					if mode == "remap" {
						return "other", nil
					}
				}
				return ThreadKey(a.ThreadID), nil
			}
			disconnect, post := launchRequest(s, "t", "r")
			await(t, started)
			disconnect()
			<-post
			key := ThreadKey("t")
			entered, release := make(chan struct{}), make(chan struct{})
			var delivered int
			done := make(chan error, 1)
			check := func(context.Context) error {
				next, err := s.resolve(httptest.NewRequest("GET", "/", nil), Access{Op: OpFollow, ThreadID: "t"})
				if err != nil {
					return err
				}
				if next != key {
					return errors.New("remap")
				}
				return nil
			}
			go func() {
				done <- s.observe(context.Background(), key, 0, check, func(ProjectedRecord) error { delivered++; close(entered); <-release; return nil })
			}()
			await(t, entered)
			if mode == "revoke" {
				s.Revoke(key)
			} else {
				access.Store(1)
			}
			input <- adaptor.TextDelta{MessageID: "a", Text: "later"}
			close(release)
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			if delivered != 1 || !s.Active(key) {
				t.Fatal(delivered, s.Active(key))
			}
			if err := s.Cancel(context.Background(), key); err != nil {
				t.Fatal(err)
			}
			// Trusted host can read fresh history after a local Revoke.
			if mode == "revoke" {
				var records int
				if err := s.Observe(context.Background(), key, 0, func(ProjectedRecord) error { records++; return nil }); err != nil || records < 2 {
					t.Fatal(records, err)
				}
			}
		})
	}
}

func TestSnapshotWaitsForBoundaryPublication(t *testing.T) {
	for _, action := range []string{"publish", "release", "replace"} {
		t.Run(action, func(t *testing.T) {
			rec := recorder(t)
			s := New(fakeRunner{}, Options{Recorder: rec})
			key := ThreadKey("t")
			recordCompletedTurn(t, rec, key)
			if err := s.reserve(context.Background(), key, "next", nil); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			type result struct {
				snap Snapshot
				err  error
			}
			done := make(chan result, 1)
			go func() { snap, err := s.Snapshot(ctx, key); done <- result{snap, err} }()
			if action != "publish" {
				s.release(key)
			}
			if action == "replace" {
				if err := s.reserve(ctx, key, "replacement", nil); err != nil {
					t.Fatal(err)
				}
			}
			if action != "release" {
				run := "next"
				if action == "replace" {
					run = "replacement"
				}
				boundary := recordEvent(t, rec, key, adaptor.Notice{Kind: BoundaryKind, Data: map[string]any{"threadId": "t", "runId": run}})
				s.mu.Lock()
				s.boundaries[key] = boundary.HostSeq
				s.changeLocked(key)
				s.mu.Unlock()
			}
			got := <-done
			if got.err != nil || len(got.snap.Messages) != 2 || got.snap.Cursor != 8 {
				t.Fatal(got.snap, got.err)
			}
			if action == "release" {
				if got.snap.ActiveRun != nil {
					t.Fatal(got.snap)
				}
			} else if got.snap.ActiveRun == nil || got.snap.ActiveRun.BoundaryHostSeq != 9 {
				t.Fatal(got.snap)
			}
			s.release(key)
		})
	}
}

type observationReadFailure struct {
	sessionrecorder.EventRecorder
	reads  atomic.Int32
	failAt int32
}

func (r *observationReadFailure) Since(ctx context.Context, key string, after uint64) ([]sessionrecorder.EventRecord, error) {
	if r.reads.Add(1) >= r.failAt {
		return nil, errors.New("storage unavailable")
	}
	return r.EventRecorder.Since(ctx, key, after)
}
func TestObservationHTTPReadFailures(t *testing.T) {
	for _, established := range []bool{false, true} {
		t.Run(fmt.Sprint(established), func(t *testing.T) {
			base := recorder(t)
			failAt := int32(1)
			if established {
				failAt = 2
			}
			rec := &observationReadFailure{EventRecorder: base, failAt: failAt}
			s := New(fakeRunner{}, Options{Recorder: rec, KeepAlivePing: time.Millisecond})
			key := ThreadKey("t")
			if established {
				boundary := recordEvent(t, base, key, adaptor.Notice{Kind: BoundaryKind, Data: map[string]any{"threadId": "t", "runId": "r"}})
				s.mu.Lock()
				s.active[key] = true
				s.boundaries[key] = boundary.HostSeq
				s.mu.Unlock()
				defer s.release(key)
			}
			w := httptest.NewRecorder()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			s.ObserveHandler().ServeHTTP(w, httptest.NewRequest("GET", "/?thread_id=t", nil).WithContext(ctx))
			if !established {
				if w.Code != 500 || !json.Valid(w.Body.Bytes()) || w.Header().Get("Content-Type") != "application/json" {
					t.Fatal(w.Code, w.Header(), w.Body)
				}
			} else {
				es := wireEvents(t, w)
				if w.Code != 200 || len(es) != 2 || str(es[0], "type") != "RUN_STARTED" || str(es[1], "code") != "agui.observation_failed" || strings.Contains(w.Body.String(), `{"error":`) {
					t.Fatal(w.Code, w.Body)
				}
				if w.Header().Get("Content-Type") != "text/event-stream" || w.Header().Get("X-Accel-Buffering") != "no" || w.Header().Get("Cache-Control") != "no-cache" {
					t.Fatal(w.Header())
				}
				if w.Header().Get("Connection") != "keep-alive" {
					t.Fatal(w.Header())
				}
			}
		})
	}
}

func TestUnattendedConcurrentAdmissionAndRelease(t *testing.T) {
	const capacity, requests = 3, 16
	started := make(chan struct{}, requests)
	runner := fakeRunner{func(ctx context.Context, _ string) adaptor.Stream {
		started <- struct{}{}
		ch := make(chan adaptor.Event)
		go func() { <-ctx.Done(); close(ch) }()
		return &pacedStream{ctx: ctx, ch: ch}
	}}
	s := New(runner, Options{Recorder: recorder(t), Unattended: true, BaseContext: context.Background(), MaxConcurrentRuns: capacity})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	type response struct {
		thread string
		w      *httptest.ResponseRecorder
	}
	replies := make(chan response, requests)
	for i := 0; i < requests; i++ {
		go func() {
			thread := fmt.Sprintf("t%d", i)
			replies <- response{thread, request(s, thread, "r", []Message{user("u", "go")})}
		}()
	}
	for i := 0; i < capacity; i++ {
		select {
		case <-started:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	for i := 0; i < requests-capacity; i++ {
		select {
		case got := <-replies:
			if got.w.Code != 429 || !strings.Contains(got.w.Body.String(), "agui.capacity") {
				t.Fatal(got.w.Code, got.w.Body)
			}
			records, err := s.opts.Recorder.Since(ctx, RecorderKey(ThreadKey(got.thread)), 0)
			if err != nil || len(records) != 0 {
				t.Fatal("capacity rejection recorded a boundary", records, err)
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	if err := s.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < capacity; i++ {
		select {
		case got := <-replies:
			if got.w.Code != 200 || !strings.Contains(got.w.Body.String(), "run.cancelled") {
				t.Fatal(got.w.Code, got.w.Body)
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	// An invalid request must release its admission slot without consuming a key.
	fresh := New(fakeRunner{func(context.Context, string) adaptor.Stream { return script(nil, nil) }}, Options{Recorder: recorder(t), Unattended: true, BaseContext: context.Background(), MaxConcurrentRuns: 1})
	w := httptest.NewRecorder()
	fresh.Handler().ServeHTTP(w, httptest.NewRequest("POST", "/", strings.NewReader("{")))
	if w.Code != 400 {
		t.Fatal(w.Code, w.Body)
	}
	if w := request(fresh, "t", "r", []Message{user("u", "go")}); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	if err := fresh.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestObserverSeqTypedNamespaceAndInvalidMetadata(t *testing.T) {
	for _, ns := range []any{map[string]string{"business": "retained"}, json.RawMessage(`{"business":"retained","number":9007199254740993}`)} {
		ev := events.NewTextMessageStartEvent("a")
		ev.GetBaseEvent().Metadata = map[string]any{"agentAdaptor": ns}
		if err := stampRecord(ProjectedRecord{HostSeq: 7, Events: []events.Event{ev}}); err != nil {
			t.Fatal(err)
		}
		raw, err := ev.ToJSON()
		if err != nil || !strings.Contains(string(raw), `"business":"retained"`) {
			t.Fatal(string(raw), err)
		}
		if _, ok := ns.(json.RawMessage); ok && !strings.Contains(string(raw), "9007199254740993") {
			t.Fatal(string(raw))
		}
	}
	for _, ns := range []any{"scalar", map[string]chan int{"bad": make(chan int)}} {
		ev := events.NewTextMessageStartEvent("a")
		ev.GetBaseEvent().Metadata = map[string]any{"agentAdaptor": ns}
		if err := stampRecord(ProjectedRecord{HostSeq: 7, Events: []events.Event{ev}}); err == nil {
			t.Fatal("silently replaced invalid cursor namespace")
		}
	}
}

func TestSnapshotRejectsCorruptActiveBoundary(t *testing.T) {
	for _, mode := range []string{"missing", "wrong_kind", "wrong_version", "no_identity"} {
		t.Run(mode, func(t *testing.T) {
			rec := recorder(t)
			s := New(fakeRunner{}, Options{Recorder: rec})
			key := ThreadKey("t")
			if mode != "missing" {
				var ev adaptor.Event = adaptor.Notice{Kind: BoundaryKind, Data: map[string]any{"threadId": "t", "runId": "r", "projectionVersion": ProjectionVersion}}
				switch mode {
				case "wrong_kind":
					ev = adaptor.Notice{Kind: "other"}
				case "wrong_version":
					ev = adaptor.Notice{Kind: BoundaryKind, Data: map[string]any{"threadId": "t", "runId": "r", "projectionVersion": "unknown"}}
				case "no_identity":
					ev = adaptor.Notice{Kind: BoundaryKind, Data: map[string]any{"projectionVersion": ProjectionVersion}}
				}
				if _, err := rec.Record(context.Background(), RecorderKey(key), ev); err != nil {
					t.Fatal(err)
				}
			}
			s.mu.Lock()
			s.active[key] = true
			s.boundaries[key] = 1
			s.mu.Unlock()
			if _, err := s.Snapshot(context.Background(), key); err == nil {
				t.Fatal("accepted corrupt active boundary")
			}
			s.release(key)
		})
	}
}

func TestEmptyProjectedRecordsDoNotCommitSSE(t *testing.T) {
	base := recorder(t)
	key := ThreadKey("t")
	recordEvent(t, base, key, adaptor.Notice{Kind: BoundaryKind, Data: map[string]any{
		"threadId": "t", "runId": "r", "userMessageId": "u", "userMessage": map[string]any{"id": "u", "role": "user", "content": "go"},
	}})
	for _, phase := range []adaptor.Phase{adaptor.PhaseStart, adaptor.PhaseContent, adaptor.PhaseEnd} {
		recordEvent(t, base, key, adaptor.TextDelta{MessageID: "u", Role: adaptor.RoleUser, Phase: phase, Text: "go"})
	}
	rec := &observationReadFailure{EventRecorder: base, failAt: 2}
	s := New(fakeRunner{}, Options{Recorder: rec, KeepAlivePing: time.Millisecond})
	s.mu.Lock()
	s.active[key] = true
	s.boundaries[key] = 1
	s.mu.Unlock()
	defer s.release(key)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	w := httptest.NewRecorder()
	s.ObserveHandler().ServeHTTP(w, httptest.NewRequest("GET", "/?thread_id=t&after=1", nil).WithContext(ctx))
	if w.Code != 500 || !json.Valid(w.Body.Bytes()) || w.Flushed || w.Header().Get("Content-Type") != "application/json" {
		t.Fatal(w.Code, w.Flushed, w.Body)
	}
}

func TestRevokeCancelsAuthorizationContext(t *testing.T) {
	entered := make(chan struct{})
	var calls atomic.Int32
	s := New(fakeRunner{}, Options{Recorder: recorder(t), Resolve: func(r *http.Request, a Access) (string, error) {
		if a.Op != OpFollow {
			t.Error(a.Op)
		}
		if calls.Add(1) == 2 {
			close(entered)
			<-r.Context().Done()
			return "", r.Context().Err()
		}
		return ThreadKey(a.ThreadID), nil
	}})
	w := httptest.NewRecorder()
	done := make(chan struct{})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	go func() {
		defer close(done)
		s.ObserveHandler().ServeHTTP(w, httptest.NewRequest("GET", "/?thread_id=t", nil).WithContext(ctx))
	}()
	await(t, entered)
	s.Revoke(ThreadKey("t"))
	await(t, done)
	if ctx.Err() != nil || w.Body.Len() != 0 {
		t.Fatal("revocation did not cleanly cancel authorization", ctx.Err(), w.Body)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.observers) != 0 {
		t.Fatal("observer registration leaked")
	}
}

func TestUnattendedFailsClosedAtEveryAppend(t *testing.T) {
	// boundary + three user source records + three Result.Text records + terminal
	for position := 1; position <= 8; position++ {
		t.Run(fmt.Sprint(position), func(t *testing.T) {
			base := recorder(t)
			stream := script(&adaptor.Result{Text: "answer"}, nil)
			s := New(fakeRunner{func(context.Context, string) adaptor.Stream { return stream }}, Options{
				Recorder:   notifyingFailure{&failingRecorder{EventRecorder: base, remaining: position}},
				Unattended: true, BaseContext: context.Background(),
			})
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			w := request(s, "t", "r", []Message{user("u", "go")})
			if err := s.Shutdown(ctx); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(w.Body.String(), "agui.recording_failed") || strings.Contains(w.Body.String(), "RUN_FINISHED") {
				t.Fatal(w.Code, w.Body)
			}
			if position == 1 {
				if w.Code != 500 || !json.Valid(w.Body.Bytes()) {
					t.Fatal(w.Code, w.Body)
				}
			} else if !stream.cancel.Load() {
				t.Fatal("storage failure did not cancel stream")
			}
			records, err := base.Since(ctx, RecorderKey(ThreadKey("t")), 0)
			if err != nil || len(records) != position-1 {
				t.Fatal(len(records), err)
			}
			for _, rec := range records {
				if n, ok := rec.Event.(adaptor.Notice); ok && n.Kind == TerminalKind {
					t.Fatal("persisted false terminal", n)
				}
			}
			if s.Active(ThreadKey("t")) {
				t.Fatal("failed run retained reservation")
			}
		})
	}
}

type subscribeBarrierRecorder struct {
	sessionrecorder.EventRecorder
	entered, release chan struct{}
	subscribed       atomic.Bool
}

func (r *subscribeBarrierRecorder) Subscribe(ctx context.Context, key string) (<-chan struct{}, func(), error) {
	changes, stop, err := r.EventRecorder.(sessionrecorder.Follower).Subscribe(ctx, key)
	if err != nil {
		return nil, nil, err
	}
	close(r.entered)
	select {
	case <-r.release:
	case <-ctx.Done():
		stop()
		return nil, nil, ctx.Err()
	}
	r.subscribed.Store(true)
	return changes, stop, nil
}
func (r *subscribeBarrierRecorder) Since(ctx context.Context, key string, after uint64) ([]sessionrecorder.EventRecord, error) {
	if !r.subscribed.Load() {
		return nil, errors.New("read happened before subscription became effective")
	}
	return r.EventRecorder.Since(ctx, key, after)
}
func TestObserveSubscribeBeforeReadAndCoalescedNotifications(t *testing.T) {
	base := recorder(t)
	rec := &subscribeBarrierRecorder{EventRecorder: base, entered: make(chan struct{}), release: make(chan struct{})}
	s := New(fakeRunner{}, Options{Recorder: rec})
	key := ThreadKey("t")
	boundary := recordEvent(t, base, key, adaptor.Notice{Kind: BoundaryKind, Data: map[string]any{"threadId": "t", "runId": "r"}})
	s.mu.Lock()
	s.active[key] = true
	s.boundaries[key] = boundary.HostSeq
	s.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	type result struct {
		records []ProjectedRecord
		err     error
	}
	done := make(chan result, 1)
	go func() {
		var records []ProjectedRecord
		err := s.Observe(ctx, key, 0, func(rec ProjectedRecord) error { records = append(records, rec); return nil })
		done <- result{records, err}
	}()
	await(t, rec.entered)
	for i := 0; i < 1000; i++ {
		recordEvent(t, base, key, adaptor.TextDelta{MessageID: "a", Text: "x"})
	}
	recordEvent(t, base, key, adaptor.Notice{Kind: TerminalKind})
	s.release(key)
	close(rec.release)
	got := <-done
	if got.err != nil || len(got.records) != 1002 {
		t.Fatal(len(got.records), got.err)
	}
	for i, rec := range got.records {
		if rec.HostSeq != uint64(i+1) {
			t.Fatalf("duplicate/gap at %d: %d", i, rec.HostSeq)
		}
	}
	es := flattened(t, got.records)
	if str(es[0], "type") != "RUN_STARTED" || str(es[len(es)-1], "type") != "RUN_FINISHED" {
		t.Fatal("coalescing lost boundary/terminal")
	}
}

func TestShutdownAlreadyDrainedIsStable(t *testing.T) {
	s := New(fakeRunner{}, Options{Recorder: recorder(t)})
	if err := s.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for i := 0; i < 100; i++ {
		if err := s.Shutdown(ctx); err != nil {
			t.Fatal("completed shutdown changed result", err)
		}
	}
}

func TestUnattendedDisconnectDuringAdmission(t *testing.T) {
	base := recorder(t)
	key := ThreadKey("t")
	rec := &staleReadRecorder{EventRecorder: base, key: RecorderKey(key), entered: make(chan struct{}), release: make(chan struct{})}
	s := New(fakeRunner{func(context.Context, string) adaptor.Stream {
		return script(&adaptor.Result{Text: "survived admission"}, nil)
	}}, Options{Recorder: rec, Unattended: true, BaseContext: context.Background()})
	disconnect, post := launchRequest(s, "t", "r")
	defer disconnect()
	await(t, rec.entered)
	disconnect()
	close(rec.release)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	select {
	case <-post:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	waitForRecord(t, base, key, func(rec sessionrecorder.EventRecord) bool {
		n, ok := rec.Event.(adaptor.Notice)
		if !ok || n.Kind != TerminalKind {
			return false
		}
		if str(n.Data, "code") != "" {
			t.Fatal(n.Data)
		}
		return true
	})
	if err := s.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	history, err := s.Messages(ctx, key)
	if err != nil || len(history) != 2 || str(history[1], "content") != "survived admission" {
		t.Fatal(history, err)
	}
}

type transitionReadRecorder struct {
	sessionrecorder.EventRecorder
	once          atomic.Bool
	before, after func()
}

func (r *transitionReadRecorder) Since(ctx context.Context, key string, after uint64) ([]sessionrecorder.EventRecord, error) {
	if r.once.Swap(true) {
		return r.EventRecorder.Since(ctx, key, after)
	}
	r.before()
	records, err := r.EventRecorder.Since(ctx, key, after)
	r.after()
	return records, err
}
func TestObserveDoesNotSynthesizeIncompleteFromStaleActivePrefix(t *testing.T) {
	base := recorder(t)
	rec := &transitionReadRecorder{EventRecorder: base}
	s := New(fakeRunner{}, Options{Recorder: rec})
	key := ThreadKey("t")
	rec.before = func() {
		if err := s.reserve(context.Background(), key, "r", nil); err != nil {
			t.Fatal(err)
		}
		boundary := recordEvent(t, base, key, adaptor.Notice{Kind: BoundaryKind, Data: map[string]any{"threadId": "t", "runId": "r"}})
		s.mu.Lock()
		s.boundaries[key] = boundary.HostSeq
		s.changeLocked(key)
		s.mu.Unlock()
		recordEvent(t, base, key, adaptor.TextDelta{MessageID: "a", Text: "healthy"})
	}
	rec.after = func() { recordEvent(t, base, key, adaptor.Notice{Kind: TerminalKind}); s.release(key) }
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var records []ProjectedRecord
	if err := s.Observe(ctx, key, 0, func(rec ProjectedRecord) error { records = append(records, rec); return nil }); err != nil {
		t.Fatal(err)
	}
	if len(records) != 3 {
		t.Fatalf("missed committed terminal: %d records", len(records))
	}
	es := flattened(t, records)
	for _, ev := range es {
		if str(ev, "type") == "RUN_ERROR" {
			t.Fatal("fabricated incomplete failure for a healthy run", ev)
		}
	}
	if str(es[len(es)-1], "type") != "RUN_FINISHED" {
		t.Fatal(es)
	}
}

func TestCancelReleasesAdmissionCapacity(t *testing.T) {
	for iteration := 0; iteration < 20; iteration++ {
		t.Run(fmt.Sprint(iteration), func(t *testing.T) {
			started := make(chan struct{})
			var calls atomic.Int32
			s := New(fakeRunner{func(ctx context.Context, _ string) adaptor.Stream {
				if calls.Add(1) > 1 {
					return script(nil, nil)
				}
				ch := make(chan adaptor.Event)
				go func() { close(started); <-ctx.Done(); close(ch) }()
				return &pacedStream{ctx: ctx, ch: ch}
			}}, Options{Recorder: recorder(t), Unattended: true, BaseContext: context.Background(), MaxConcurrentRuns: 1})
			disconnect, post := launchRequest(s, "first", "r")
			defer disconnect()
			await(t, started)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if err := s.Cancel(ctx, ThreadKey("first")); err != nil {
				t.Fatal(err)
			}
			if w := request(s, "second", "r2", []Message{user("u2", "go")}); w.Code != 200 {
				t.Fatal("cancel returned before its admission was released", w.Code, w.Body)
			}
			if err := s.Shutdown(ctx); err != nil {
				t.Fatal(err)
			}
			select {
			case <-post:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
		})
	}
}

type fixedObservationDriver struct {
	continuityDriver
	started chan struct{}
}

func (d fixedObservationDriver) Run(ctx context.Context, _ driver.Request, _ driver.EventSink) (driver.Response, error) {
	close(d.started)
	<-ctx.Done()
	return driver.Response{}, ctx.Err()
}
func TestFixedThreadObservationAndCancellationUseHostKey(t *testing.T) {
	started := make(chan struct{})
	agent := adaptor.New(&fixedObservationDriver{started: started}, adaptor.WithThreadStore(memory.NewStore()))
	defer agent.Close(context.Background())
	fixed := agent.Thread("already-bound-host-key")
	s := New(fixed, Options{Recorder: recorder(t), Unattended: true, BaseContext: context.Background()})
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = s.Shutdown(ctx)
	}()
	disconnect, post := launchRequest(s, "public", "r")
	defer disconnect()
	select {
	case <-started:
	case response := <-post:
		t.Fatal("run did not reach driver", response.Code, response.Body)
	case <-time.After(time.Second):
		t.Fatal("driver did not start")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	snapshot := httptest.NewRecorder()
	s.SnapshotHandler().ServeHTTP(snapshot, httptest.NewRequest("GET", "/?thread_id=public", nil).WithContext(ctx))
	var cut Snapshot
	if snapshot.Code != 200 || json.Unmarshal(snapshot.Body.Bytes(), &cut) != nil || cut.ActiveRun == nil || cut.ActiveRun.ThreadID != "public" {
		t.Fatal(snapshot.Code, snapshot.Body)
	}
	w := httptest.NewRecorder()
	s.CancelHandler().ServeHTTP(w, httptest.NewRequest("POST", "/?thread_id=public", nil).WithContext(ctx))
	if w.Code != 204 {
		t.Fatal(w.Code, w.Body)
	}
	select {
	case response := <-post:
		es := wireEvents(t, response)
		if len(es) < 2 || str(es[0], "type") != "RUN_STARTED" || str(es[0], "threadId") != "public" || str(es[len(es)-1], "code") != "run.cancelled" {
			t.Fatal("first observer was mapped away from fixed Thread", response.Body)
		}
	case <-ctx.Done():
		t.Fatal("cancel handler missed fixed Thread", ctx.Err())
	}
	read := httptest.NewRecorder()
	s.ObserveHandler().ServeHTTP(read, httptest.NewRequest("GET", "/?thread_id=public", nil).WithContext(ctx))
	if !strings.Contains(read.Body.String(), "run.cancelled") {
		t.Fatal(read.Body)
	}
	history := httptest.NewRecorder()
	s.MessagesHandler().ServeHTTP(history, httptest.NewRequest("GET", "/?thread_id=public", nil).WithContext(ctx))
	var messages []Message
	if history.Code != 200 || json.Unmarshal(history.Body.Bytes(), &messages) != nil || len(messages) != 1 || str(messages[0], "id") != "u" {
		t.Fatal(history.Code, history.Body)
	}
	if err := s.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
}

type revokeAfterFrameWriter struct {
	*httptest.ResponseRecorder
	once   sync.Once
	revoke func()
}

func (w *revokeAfterFrameWriter) Write(p []byte) (int, error) {
	n, err := w.ResponseRecorder.Write(p)
	if strings.Contains(w.Body.String(), "\n\n") {
		w.once.Do(w.revoke)
	}
	return n, err
}
func TestHTTPRevocationMayEndInsideProjectedRecord(t *testing.T) {
	rec := recorder(t)
	key := ThreadKey("t")
	recordEvent(t, rec, key, adaptor.Notice{Kind: BoundaryKind, Data: map[string]any{
		"threadId": "t", "runId": "r", "diagnostics": []agui.Diagnostic{{Code: "agui.newer_minor", Message: "warning"}},
	}})
	s := New(fakeRunner{}, Options{Recorder: rec})
	// A single durable record projects RUN_STARTED + diagnostic + incomplete
	// terminal. Revoke after the first complete frame must prevent further frames.
	w := &revokeAfterFrameWriter{ResponseRecorder: httptest.NewRecorder(), revoke: func() { s.Revoke(key) }}
	s.ObserveHandler().ServeHTTP(w, httptest.NewRequest("GET", "/?thread_id=t", nil))
	es := wireEvents(t, w.ResponseRecorder)
	if len(es) != 1 || str(es[0], "type") != "RUN_STARTED" {
		t.Fatal("revoke fabricated a terminal or delivered another event", w.Body)
	}
	// A clean EOF still cannot acknowledge seq=1: that record has more events.
	all, err := s.Events(context.Background(), key, 0)
	if err != nil || len(all) != 1 || len(all[0].Events) < 3 {
		t.Fatal(all, err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.observers) != 0 {
		t.Fatal("observer leaked")
	}
}

func TestUnattendedFirstObserverUsesAuthorizedBranchKey(t *testing.T) {
	s := New(fakeRunner{func(context.Context, string) adaptor.Stream {
		return script(&adaptor.Result{Text: "branch answer"}, nil)
	}}, Options{
		Recorder: recorder(t), Unattended: true, BaseContext: context.Background(),
		Input:             agui.InputOptions{HistoryFallback: true},
		ResolveDivergence: func(context.Context, *agui.RunAgentInput, string) (string, error) { return "fresh-branch", nil },
	})
	w := request(s, "public", "r", []Message{user("prior", "earlier"), user("submitted", "continue")})
	if w.Code != 200 || !strings.Contains(w.Body.String(), "RUN_STARTED") || !strings.Contains(w.Body.String(), "RUN_FINISHED") {
		t.Fatal("first observer ignored the authorized resolved key", w.Code, w.Body)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := s.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	history, err := s.Messages(ctx, "fresh-branch")
	if err != nil || len(history) != 2 || str(history[0], "id") != "submitted" || str(history[1], "content") != "branch answer" {
		t.Fatal(history, err)
	}
}
