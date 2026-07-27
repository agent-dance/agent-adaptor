package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/agent-dance/agent-adaptor/bridges/sse"
	"github.com/agent-dance/agent-adaptor/hosttools/sessionrecorder"
	adaptor "github.com/agent-dance/agent-adaptor/next"
)

// TestThreadStoreHistoryAfterUsesHostSeqCursor checks the cross-run recovery
// contract: unified events carry no sequence number of their own, so the host
// cursor assigned by the recorder is the only thing that can stay monotonic
// when one browser thread spans two runs.
func TestThreadStoreHistoryAfterUsesHostSeqCursor(t *testing.T) {
	store := mustNewThreadStore(t)

	// Run A: five assistant deltas.
	for i := 0; i < 5; i++ {
		if err := store.appendHistory("thread-1", adaptor.TextDelta{MessageID: "runA", Text: "a", Phase: adaptor.PhaseContent}); err != nil {
			t.Fatalf("append run A: %v", err)
		}
	}
	// Run B for the same thread: four more.
	for i := 0; i < 4; i++ {
		if err := store.appendHistory("thread-1", adaptor.TextDelta{MessageID: "runB", Text: "b", Phase: adaptor.PhaseContent}); err != nil {
			t.Fatalf("append run B: %v", err)
		}
	}

	all := store.historyAfter("thread-1", 0)
	if got := len(all); got != 9 {
		t.Fatalf("history len = %d, want 9", got)
	}
	for i, r := range all {
		if r.HostSeq != uint64(i+1) {
			t.Fatalf("record[%d].HostSeq = %d, want %d", i, r.HostSeq, i+1)
		}
	}

	// Cursor at HostSeq=5 after run A — run B's 4 records come back, nothing
	// from run A leaks back in.
	incremental := store.historyAfter("thread-1", 5)
	if got := len(incremental); got != 4 {
		t.Fatalf("incremental len = %d, want 4 (run B only)", got)
	}
	for i, r := range incremental {
		delta, ok := r.Event.(adaptor.TextDelta)
		if !ok {
			t.Fatalf("incremental[%d] is %T, want adaptor.TextDelta", i, r.Event)
		}
		if delta.MessageID != "runB" {
			t.Fatalf("incremental[%d].MessageID = %q, want runB", i, delta.MessageID)
		}
		if r.HostSeq != uint64(6+i) {
			t.Fatalf("incremental[%d].HostSeq = %d, want %d", i, r.HostSeq, 6+i)
		}
	}
}

func TestThreadStoreUnregisterRunDropsOwnedPendingRequests(t *testing.T) {
	store := mustNewThreadStore(t)
	stream := &fakeStream{runID: "run-1"}

	store.registerRun("thread-1", stream)
	store.addPending("thread-1", &adaptor.ApprovalRequest{ID: "req-owned", RunID: "run-1"})
	store.addPending("thread-1", &adaptor.ApprovalRequest{ID: "req-other", RunID: "run-2"})

	store.unregisterRun("thread-1", stream)

	pending := store.pendingRequests("thread-1")
	if got := len(pending); got != 1 {
		t.Fatalf("pending len = %d, want 1", got)
	}
	if pending[0].ID != "req-other" {
		t.Fatalf("remaining pending = %q, want req-other", pending[0].ID)
	}
}

// TestThreadStoreResolveDecisionRejectsUnboundRequest pins the v1 approval
// safety contract: a descriptive/replayed request has no live responder and
// fails immediately instead of blocking the HTTP handler.
func TestThreadStoreResolveDecisionRejectsUnboundRequest(t *testing.T) {
	store := mustNewThreadStore(t)
	store.addPending("thread-1", &adaptor.ApprovalRequest{ID: "req-1", Kind: adaptor.ApprovalQuestion})

	err := store.resolveDecision(context.Background(), "thread-1", sse.DecisionResolveRequest{
		RequestID: "req-1",
		Result:    "approved",
	})
	if !errors.Is(err, adaptor.ErrApprovalUnavailable) {
		t.Fatalf("err = %v, want ErrApprovalUnavailable", err)
	}
}

func TestThreadStoreResolveDecisionExpiresUnknownRequest(t *testing.T) {
	store := mustNewThreadStore(t)
	err := store.resolveDecision(context.Background(), "thread-1", sse.DecisionResolveRequest{
		RequestID: "missing",
		Result:    "approved",
	})
	if !errors.Is(err, adaptor.ErrApprovalResolved) {
		t.Fatalf("err = %v, want ErrApprovalResolved", err)
	}
}

func TestThreadStoreConfiguredJSONLPersistsAcrossRestart(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "events")
	t.Setenv("THREAD_STORE_DIR", dir)

	first, err := newThreadStore()
	if err != nil {
		t.Fatalf("newThreadStore(first): %v", err)
	}
	if err := first.appendHistory("thread-durable", adaptor.TextDelta{MessageID: "m1", Text: "saved"}); err != nil {
		t.Fatalf("appendHistory: %v", err)
	}
	if err := first.recorder.Close(); err != nil {
		t.Fatalf("Close(first): %v", err)
	}

	second, err := newThreadStore()
	if err != nil {
		t.Fatalf("newThreadStore(second): %v", err)
	}
	defer second.recorder.Close()
	records := second.historyAfter("thread-durable", 0)
	if len(records) != 1 {
		t.Fatalf("records after restart = %d, want 1", len(records))
	}
	delta, ok := records[0].Event.(adaptor.TextDelta)
	if !ok || delta.Text != "saved" {
		t.Fatalf("replayed event = %#v, want saved TextDelta", records[0].Event)
	}
}

func TestThreadStoreConfiguredPersistenceFailureDoesNotFallback(t *testing.T) {
	path := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(path, []byte("occupied"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	t.Setenv("THREAD_STORE_DIR", path)

	store, err := newThreadStore()
	if err == nil || store != nil {
		t.Fatalf("newThreadStore = (%v, %v), want nil and explicit persistence error", store, err)
	}
}

func TestThreadStoreWriteFailureIsReturned(t *testing.T) {
	t.Setenv("THREAD_STORE_DIR", "")
	writeErr := errors.New("disk full")
	backend := &failingEventBackend{
		EventBackend: sessionrecorder.NewMemoryEventBackend(),
		appendErr:    writeErr,
	}
	store := &threadStore{
		recorder: sessionrecorder.NewEventRecorder(backend),
		threads:  map[string]*threadRuntime{},
	}
	t.Cleanup(func() { _ = store.recorder.Close() })

	err := store.appendHistory("thread-write-failure", adaptor.Dropped{Count: 1})
	if !errors.Is(err, writeErr) {
		t.Fatalf("appendHistory error = %v, want storage write failure", err)
	}
	if records := store.historyAfter("thread-write-failure", 0); len(records) != 0 {
		t.Fatalf("failed append appeared successful: %#v", records)
	}
}

func mustNewThreadStore(t *testing.T) *threadStore {
	t.Helper()
	t.Setenv("THREAD_STORE_DIR", "")
	store, err := newThreadStore()
	if err != nil {
		t.Fatalf("newThreadStore: %v", err)
	}
	t.Cleanup(func() {
		if err := store.recorder.Close(); err != nil {
			t.Errorf("thread store Close: %v", err)
		}
	})
	return store
}

type failingEventBackend struct {
	sessionrecorder.EventBackend
	appendErr error
}

func (b *failingEventBackend) Append(context.Context, string, sessionrecorder.EventRecord) error {
	return b.appendErr
}

// fakeStream is a scripted adaptor.Stream: tests push events onto events,
// close it to signal "driver done", and Result reports the verdict.
type fakeStream struct {
	runID  string
	events chan adaptor.Event
	result *adaptor.Result
	err    error

	cancelled bool
}

func (f *fakeStream) Events() <-chan adaptor.Event { return f.events }

func (f *fakeStream) Result() (*adaptor.Result, error) { return f.result, f.err }

func (f *fakeStream) RunID() string { return f.runID }

func (f *fakeStream) Cancel() { f.cancelled = true }
