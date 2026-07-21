package main

import (
	"context"
	"errors"
	"testing"

	agentadaptor "github.com/agent-dance/agent-adaptor"
)

// TestThreadStoreHistoryAfterUsesHostSeqCursor checks the fix for the
// cross-run recovery bug: StreamPayload.Seq is per-run monotonic and
// restarts at zero on every new run, so a naive `ev.Seq > afterSeq`
// filter would either drop or replay events when a thread spans two
// runs. The store MUST assign a host-scoped cursor that stays monotonic
// across that boundary.
func TestThreadStoreHistoryAfterUsesHostSeqCursor(t *testing.T) {
	store := newThreadStore()

	// Run A: the adapter hands us Seq 1..5.
	for i := 1; i <= 5; i++ {
		store.appendHistory("thread-1", agentadaptor.StreamPayload{RunID: "runA", Seq: uint64(i)})
	}
	// Run B for the same thread: Seq resets to 0..3.
	for i := 0; i <= 3; i++ {
		store.appendHistory("thread-1", agentadaptor.StreamPayload{RunID: "runB", Seq: uint64(i)})
	}

	all := store.historyAfter("thread-1", 0)
	if got := len(all); got != 9 {
		t.Fatalf("history len = %d, want 9", got)
	}
	// HostSeq must be 1..9 regardless of the adapter's per-run Seq.
	for i, r := range all {
		if r.HostSeq != uint64(i+1) {
			t.Fatalf("record[%d].HostSeq = %d, want %d", i, r.HostSeq, i+1)
		}
	}

	// Cursor at HostSeq=5 after run A — run B's 4 records come back,
	// with no leakage of run A events that happen to have Payload.Seq > 5.
	incremental := store.historyAfter("thread-1", 5)
	if got := len(incremental); got != 4 {
		t.Fatalf("incremental len = %d, want 4 (run B only)", got)
	}
	for i, r := range incremental {
		if r.Payload.RunID != "runB" {
			t.Fatalf("incremental[%d].RunID = %q, want runB", i, r.Payload.RunID)
		}
		if r.HostSeq != uint64(6+i) {
			t.Fatalf("incremental[%d].HostSeq = %d, want %d", i, r.HostSeq, 6+i)
		}
	}
}

func TestThreadStoreUnregisterRunDropsOwnedPendingRequests(t *testing.T) {
	store := newThreadStore()
	handle := &fakeRunHandle{runID: "run-1"}

	store.registerRun("thread-1", handle)
	store.addPending("thread-1", agentadaptor.DecisionRequest{RequestID: "req-owned", RunID: "run-1"})
	store.addPending("thread-1", agentadaptor.DecisionRequest{RequestID: "req-other", RunID: "run-2"})

	store.unregisterRun("thread-1", handle)

	pending := store.pendingRequests("thread-1")
	if got := len(pending); got != 1 {
		t.Fatalf("pending len = %d, want 1", got)
	}
	if pending[0].RequestID != "req-other" {
		t.Fatalf("remaining pending = %q, want req-other", pending[0].RequestID)
	}
}

func TestThreadStoreResolveDecisionFallsBackToLiveRun(t *testing.T) {
	store := newThreadStore()
	expired := &fakeRunHandle{
		runID:      "run-expired",
		resolveErr: agentadaptor.ErrDecisionRequestExpired,
	}
	live := &fakeRunHandle{runID: "run-live"}

	store.registerRun("thread-1", expired)
	store.registerRun("thread-1", live)
	store.addPending("thread-1", agentadaptor.DecisionRequest{RequestID: "req-1"})

	resp := agentadaptor.DecisionResponse{
		RequestID: "req-1",
		Result:    agentadaptor.DecisionAnswered,
		Text:      "male",
	}
	if err := store.resolveDecision("thread-1", "req-1", resp); err != nil {
		t.Fatalf("resolveDecision: %v", err)
	}
	if live.resolveCalls == 0 {
		t.Fatal("live handle was not asked to resolve the decision")
	}
	if live.lastRequestID != "req-1" {
		t.Fatalf("live handle requestID = %q, want req-1", live.lastRequestID)
	}
	if live.lastResponse.Text != "male" {
		t.Fatalf("live handle response text = %q, want male", live.lastResponse.Text)
	}
}

func TestThreadStoreResolveDecisionExpiresUnknownRequest(t *testing.T) {
	store := newThreadStore()
	err := store.resolveDecision("thread-1", "missing", agentadaptor.DecisionResponse{})
	if !errors.Is(err, agentadaptor.ErrDecisionRequestExpired) {
		t.Fatalf("err = %v, want ErrDecisionRequestExpired", err)
	}
}

type fakeRunHandle struct {
	runID         string
	resolveErr    error
	resolveCalls  int
	lastRequestID string
	lastResponse  agentadaptor.DecisionResponse

	// streamCh / decisionCh / waitErr are optional. When set, the fake
	// behaves like a real RunHandle: tests push scripted payloads onto
	// streamCh, close it to signal "adapter done", and Wait blocks until
	// then.
	streamCh   chan agentadaptor.StreamPayload
	decisionCh chan agentadaptor.DecisionRequest
	waitResult agentadaptor.RunResult
	waitErr    error
}

func (f *fakeRunHandle) Events() <-chan agentadaptor.RunEvent { return nil }

func (f *fakeRunHandle) StreamEvents() <-chan agentadaptor.StreamPayload { return f.streamCh }

func (f *fakeRunHandle) RunID() string { return f.runID }

func (f *fakeRunHandle) Wait(_ context.Context) (agentadaptor.RunResult, error) {
	return f.waitResult, f.waitErr
}

func (f *fakeRunHandle) Cancel(context.Context) error { return nil }

func (f *fakeRunHandle) DecisionRequests() <-chan agentadaptor.DecisionRequest { return f.decisionCh }

func (f *fakeRunHandle) ResolveDecision(requestID string, resp agentadaptor.DecisionResponse) error {
	f.resolveCalls++
	f.lastRequestID = requestID
	f.lastResponse = resp
	return f.resolveErr
}
