package main

import (
	"context"
	"errors"
	"testing"

	"github.com/agent-dance/agent-adaptor/bridges/sse"
	adaptor "github.com/agent-dance/agent-adaptor/next"
)

// TestThreadStoreHistoryAfterUsesHostSeqCursor checks the cross-run recovery
// contract: unified events carry no sequence number of their own, so the host
// cursor assigned by the recorder is the only thing that can stay monotonic
// when one browser thread spans two runs.
func TestThreadStoreHistoryAfterUsesHostSeqCursor(t *testing.T) {
	store := newThreadStore()

	// Run A: five assistant deltas.
	for i := 0; i < 5; i++ {
		store.appendHistory("thread-1", adaptor.TextDelta{MessageID: "runA", Text: "a", Phase: adaptor.PhaseContent})
	}
	// Run B for the same thread: four more.
	for i := 0; i < 4; i++ {
		store.appendHistory("thread-1", adaptor.TextDelta{MessageID: "runB", Text: "b", Phase: adaptor.PhaseContent})
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
	store := newThreadStore()
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

// TestThreadStoreResolveDecisionRejectsWrongVerb pins the v1 approval
// contract: the responder lives on the request, and using the wrong verb for
// the request kind is an ErrApprovalKindMismatch the host maps to HTTP 400.
func TestThreadStoreResolveDecisionRejectsWrongVerb(t *testing.T) {
	store := newThreadStore()
	store.addPending("thread-1", &adaptor.ApprovalRequest{ID: "req-1", Kind: adaptor.ApprovalQuestion})

	err := store.resolveDecision(context.Background(), "thread-1", sse.DecisionResolveRequest{
		RequestID: "req-1",
		Result:    "approved", // Approve() is invalid for a Question
	})
	if !errors.Is(err, adaptor.ErrApprovalKindMismatch) {
		t.Fatalf("err = %v, want ErrApprovalKindMismatch", err)
	}
}

func TestThreadStoreResolveDecisionExpiresUnknownRequest(t *testing.T) {
	store := newThreadStore()
	err := store.resolveDecision(context.Background(), "thread-1", sse.DecisionResolveRequest{
		RequestID: "missing",
		Result:    "approved",
	})
	if !errors.Is(err, adaptor.ErrApprovalResolved) {
		t.Fatalf("err = %v, want ErrApprovalResolved", err)
	}
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
