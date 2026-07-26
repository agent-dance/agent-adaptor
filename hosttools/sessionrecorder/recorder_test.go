package sessionrecorder_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	agentadaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/hosttools/sessionrecorder"
)

// TestHostSeqMonotonicAcrossRunsWithResettingPayloadSeq is the
// regression test for the exact footgun that motivated this package:
// StreamPayload.Seq is per-run monotonic and restarts at zero on every
// new run, so a naive `ev.Seq > afterSeq` filter over a session that
// contains two runs will fold old-run events into the "new" window.
// HostSeq must keep growing across that boundary.
func TestHostSeqMonotonicAcrossRunsWithResettingPayloadSeq(t *testing.T) {
	t.Parallel()
	rec := sessionrecorder.New(sessionrecorder.NewMemoryBackend())
	t.Cleanup(func() { _ = rec.Close() })

	ctx := context.Background()
	const key = "thread-1"

	// Run A: Seq 1..5 under the same host session key.
	for i := 1; i <= 5; i++ {
		if _, err := rec.Record(ctx, key, agentadaptor.StreamPayload{RunID: "runA", Seq: uint64(i)}); err != nil {
			t.Fatalf("record runA#%d: %v", i, err)
		}
	}
	// Run B for the same thread: Seq restarts at 0..3 — the adapter does
	// not know the host considers these part of the same session.
	for i := 0; i <= 3; i++ {
		if _, err := rec.Record(ctx, key, agentadaptor.StreamPayload{RunID: "runB", Seq: uint64(i)}); err != nil {
			t.Fatalf("record runB#%d: %v", i, err)
		}
	}

	all, err := rec.Since(ctx, key, 0)
	if err != nil {
		t.Fatalf("Since(0): %v", err)
	}
	if got := len(all); got != 9 {
		t.Fatalf("total records = %d, want 9", got)
	}
	for i, r := range all {
		if r.HostSeq != sessionrecorder.HostSeq(i+1) {
			t.Fatalf("record[%d].HostSeq = %d, want %d", i, r.HostSeq, i+1)
		}
	}

	// The critical assertion: after consuming run A, the cursor is at
	// HostSeq=5. Since(5) MUST only return run B's 4 records, not any
	// of run A's events whose Payload.Seq happens to be > 5-inside-itself.
	incremental, err := rec.Since(ctx, key, 5)
	if err != nil {
		t.Fatalf("Since(5): %v", err)
	}
	if got := len(incremental); got != 4 {
		t.Fatalf("incremental after HostSeq=5 = %d, want 4 (only run B)", got)
	}
	for i, r := range incremental {
		if r.Payload.RunID != "runB" {
			t.Fatalf("record[%d].RunID = %q, want runB", i, r.Payload.RunID)
		}
		if r.HostSeq != sessionrecorder.HostSeq(6+i) {
			t.Fatalf("record[%d].HostSeq = %d, want %d", i, r.HostSeq, 6+i)
		}
	}
}

func TestHostSeqIsPerSessionScoped(t *testing.T) {
	t.Parallel()
	rec := sessionrecorder.New(sessionrecorder.NewMemoryBackend())
	t.Cleanup(func() { _ = rec.Close() })
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		if _, err := rec.Record(ctx, "thread-a", agentadaptor.StreamPayload{Kind: "text.content"}); err != nil {
			t.Fatalf("record a#%d: %v", i, err)
		}
	}
	for i := 0; i < 2; i++ {
		if _, err := rec.Record(ctx, "thread-b", agentadaptor.StreamPayload{Kind: "text.content"}); err != nil {
			t.Fatalf("record b#%d: %v", i, err)
		}
	}

	recsA, _ := rec.Since(ctx, "thread-a", 0)
	recsB, _ := rec.Since(ctx, "thread-b", 0)
	if len(recsA) != 3 || len(recsB) != 2 {
		t.Fatalf("per-session counts = %d/%d, want 3/2", len(recsA), len(recsB))
	}
	if recsA[0].HostSeq != 1 || recsB[0].HostSeq != 1 {
		t.Fatalf("each session must start HostSeq at 1; got a=%d b=%d", recsA[0].HostSeq, recsB[0].HostSeq)
	}
}

func TestSinceCursorSemantics(t *testing.T) {
	t.Parallel()
	rec := sessionrecorder.New(sessionrecorder.NewMemoryBackend())
	t.Cleanup(func() { _ = rec.Close() })
	ctx := context.Background()

	for i := 0; i < 10; i++ {
		if _, err := rec.Record(ctx, "t", agentadaptor.StreamPayload{Delta: fmt.Sprintf("%d", i)}); err != nil {
			t.Fatalf("record: %v", err)
		}
	}

	cases := []struct {
		after    sessionrecorder.HostSeq
		wantLen  int
		wantHead sessionrecorder.HostSeq
		wantTail sessionrecorder.HostSeq
	}{
		{after: 0, wantLen: 10, wantHead: 1, wantTail: 10},
		{after: 3, wantLen: 7, wantHead: 4, wantTail: 10},
		{after: 9, wantLen: 1, wantHead: 10, wantTail: 10},
		{after: 10, wantLen: 0},
		{after: 999, wantLen: 0},
	}
	for _, tc := range cases {
		recs, err := rec.Since(ctx, "t", tc.after)
		if err != nil {
			t.Fatalf("Since(%d): %v", tc.after, err)
		}
		if len(recs) != tc.wantLen {
			t.Fatalf("Since(%d) len = %d, want %d", tc.after, len(recs), tc.wantLen)
		}
		if tc.wantLen == 0 {
			continue
		}
		if recs[0].HostSeq != tc.wantHead || recs[len(recs)-1].HostSeq != tc.wantTail {
			t.Fatalf("Since(%d) window = [%d..%d], want [%d..%d]",
				tc.after, recs[0].HostSeq, recs[len(recs)-1].HostSeq, tc.wantHead, tc.wantTail)
		}
	}
}

func TestConcurrentRecordAssignsContiguousHostSeq(t *testing.T) {
	t.Parallel()
	rec := sessionrecorder.New(sessionrecorder.NewMemoryBackend())
	t.Cleanup(func() { _ = rec.Close() })
	ctx := context.Background()

	const N = 200
	var wg sync.WaitGroup
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func() {
			defer wg.Done()
			if _, err := rec.Record(ctx, "t", agentadaptor.StreamPayload{}); err != nil {
				t.Errorf("concurrent record: %v", err)
			}
		}()
	}
	wg.Wait()

	recs, err := rec.Since(ctx, "t", 0)
	if err != nil {
		t.Fatalf("Since: %v", err)
	}
	if len(recs) != N {
		t.Fatalf("total records = %d, want %d", len(recs), N)
	}
	for i, r := range recs {
		if r.HostSeq != sessionrecorder.HostSeq(i+1) {
			t.Fatalf("record[%d].HostSeq = %d, want %d — concurrent assignment lost monotonicity", i, r.HostSeq, i+1)
		}
	}
}

func TestRecordRollsBackHostSeqOnBackendFailure(t *testing.T) {
	t.Parallel()
	backend := &failingBackend{Backend: sessionrecorder.NewMemoryBackend()}
	rec := sessionrecorder.New(backend)
	t.Cleanup(func() { _ = rec.Close() })
	ctx := context.Background()

	// Successful write, HostSeq=1.
	if _, err := rec.Record(ctx, "t", agentadaptor.StreamPayload{}); err != nil {
		t.Fatalf("first record: %v", err)
	}
	backend.fail.Store(true)
	if _, err := rec.Record(ctx, "t", agentadaptor.StreamPayload{}); err == nil {
		t.Fatal("expected failing record to return error")
	}
	backend.fail.Store(false)

	// After the backend recovers, the next Record must get HostSeq=2,
	// NOT HostSeq=3. Otherwise a hypothetical Load after restart would
	// observe a gap at HostSeq=2.
	rec2, err := rec.Record(ctx, "t", agentadaptor.StreamPayload{})
	if err != nil {
		t.Fatalf("retry record: %v", err)
	}
	if rec2.HostSeq != 2 {
		t.Fatalf("retry HostSeq = %d, want 2 (no gap after failed append)", rec2.HostSeq)
	}
}

func TestJSONLBackendSurvivesReopen(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeN := func(n int, runID string, startPayloadSeq int) {
		be, err := sessionrecorder.NewJSONLBackend(dir)
		if err != nil {
			t.Fatalf("open backend: %v", err)
		}
		rec := sessionrecorder.New(be)
		defer func() { _ = rec.Close() }()
		ctx := context.Background()
		for i := 0; i < n; i++ {
			if _, err := rec.Record(ctx, "abc-123", agentadaptor.StreamPayload{
				RunID: runID,
				Seq:   uint64(startPayloadSeq + i),
			}); err != nil {
				t.Fatalf("record: %v", err)
			}
		}
	}
	// Session 1: run A, 5 events.
	writeN(5, "runA", 1)
	// Simulated restart — different run, Seq restarts at 0.
	writeN(4, "runB", 0)

	be, err := sessionrecorder.NewJSONLBackend(dir)
	if err != nil {
		t.Fatalf("reopen backend: %v", err)
	}
	rec := sessionrecorder.New(be)
	t.Cleanup(func() { _ = rec.Close() })

	all, err := rec.Since(context.Background(), "abc-123", 0)
	if err != nil {
		t.Fatalf("Since: %v", err)
	}
	if len(all) != 9 {
		t.Fatalf("after reopen total = %d, want 9", len(all))
	}
	for i, r := range all {
		if r.HostSeq != sessionrecorder.HostSeq(i+1) {
			t.Fatalf("after reopen: record[%d].HostSeq = %d, want %d", i, r.HostSeq, i+1)
		}
	}

	// New Record after reopen must resume from HostSeq=10.
	ctx := context.Background()
	next, err := rec.Record(ctx, "abc-123", agentadaptor.StreamPayload{RunID: "runC"})
	if err != nil {
		t.Fatalf("post-reopen record: %v", err)
	}
	if next.HostSeq != 10 {
		t.Fatalf("post-reopen HostSeq = %d, want 10", next.HostSeq)
	}

	sessions, err := rec.Sessions(ctx)
	if err != nil {
		t.Fatalf("Sessions: %v", err)
	}
	if len(sessions) != 1 || sessions[0].Key != "abc-123" || sessions[0].LastSeq != 10 {
		t.Fatalf("Sessions = %+v, want single {abc-123 last=10}", sessions)
	}
}

func TestJSONLBackendSkipsBadLinesByDefault(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "t.jsonl")
	const good = `{"host_seq":1,"recorded_at":"2026-04-24T12:00:00Z","payload":{"Kind":"text.content"}}`
	const bad = `{"host_seq":2,"payload":broken`
	if err := os.WriteFile(path, []byte(good+"\n"+bad+"\n"), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}

	be, err := sessionrecorder.NewJSONLBackend(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	rec := sessionrecorder.New(be)
	t.Cleanup(func() { _ = rec.Close() })

	recs, err := rec.Since(context.Background(), "t", 0)
	if err != nil {
		t.Fatalf("Since: %v", err)
	}
	if len(recs) != 1 || recs[0].HostSeq != 1 {
		t.Fatalf("Since after bad tail = %+v, want one HostSeq=1 record", recs)
	}
}

func TestRefusesInvalidSessionKey(t *testing.T) {
	t.Parallel()
	rec := sessionrecorder.New(sessionrecorder.NewMemoryBackend())
	t.Cleanup(func() { _ = rec.Close() })
	ctx := context.Background()

	badKeys := []string{"", "../boom", "hello/world", "  "}
	for _, k := range badKeys {
		if _, err := rec.Record(ctx, k, agentadaptor.StreamPayload{}); !errors.Is(err, sessionrecorder.ErrInvalidSessionKey) {
			t.Fatalf("Record(%q) err = %v, want ErrInvalidSessionKey", k, err)
		}
		if _, err := rec.Since(ctx, k, 0); !errors.Is(err, sessionrecorder.ErrInvalidSessionKey) {
			t.Fatalf("Since(%q) err = %v, want ErrInvalidSessionKey", k, err)
		}
	}
}

func TestWithClockIsUsedForRecordedAt(t *testing.T) {
	t.Parallel()
	fixed := time.Date(2026, 4, 24, 12, 0, 0, 0, time.UTC)
	rec := sessionrecorder.New(
		sessionrecorder.NewMemoryBackend(),
		sessionrecorder.WithClock(func() time.Time { return fixed }),
	)
	t.Cleanup(func() { _ = rec.Close() })

	got, err := rec.Record(context.Background(), "t", agentadaptor.StreamPayload{})
	if err != nil {
		t.Fatalf("record: %v", err)
	}
	if !got.RecordedAt.Equal(fixed) {
		t.Fatalf("RecordedAt = %v, want %v", got.RecordedAt, fixed)
	}
}

func TestSessionsOrderedByMostRecent(t *testing.T) {
	t.Parallel()
	var now int64
	clock := func() time.Time {
		return time.Unix(atomic.AddInt64(&now, 1), 0).UTC()
	}
	rec := sessionrecorder.New(sessionrecorder.NewMemoryBackend(), sessionrecorder.WithClock(clock))
	t.Cleanup(func() { _ = rec.Close() })
	ctx := context.Background()

	// oldest first, most-recent last
	keys := []string{"t-a", "t-b", "t-c"}
	for _, k := range keys {
		if _, err := rec.Record(ctx, k, agentadaptor.StreamPayload{}); err != nil {
			t.Fatalf("record %s: %v", k, err)
		}
	}

	list, err := rec.Sessions(ctx)
	if err != nil {
		t.Fatalf("Sessions: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("sessions len = %d, want 3", len(list))
	}
	gotOrder := []string{list[0].Key, list[1].Key, list[2].Key}
	wantOrder := []string{"t-c", "t-b", "t-a"}
	if !equalStrings(gotOrder, wantOrder) {
		t.Fatalf("sessions order = %v, want %v", gotOrder, wantOrder)
	}
}

// failingBackend wraps a Backend and toggles Append failure via `fail`.
// Used to check HostSeq rollback on backend errors.
type failingBackend struct {
	sessionrecorder.Backend
	fail atomic.Bool
}

func (b *failingBackend) Append(ctx context.Context, key string, r sessionrecorder.Record) error {
	if b.fail.Load() {
		return errors.New("backend boom")
	}
	return b.Backend.Append(ctx, key, r)
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	c := append([]string(nil), a...)
	d := append([]string(nil), b...)
	// We want exact order here, but guard against flakiness from the
	// clock granularity test case above by stable-comparing:
	sort.SliceStable(c, func(i, j int) bool { return c[i] < c[j] })
	sort.SliceStable(d, func(i, j int) bool { return d[i] < d[j] })
	for i := range c {
		if c[i] != d[i] {
			return false
		}
	}
	// and finally confirm the positional order.
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
