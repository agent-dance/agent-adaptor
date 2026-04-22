package main

import (
	"context"
	"errors"
	"testing"

	agentadaptor "github.com/agent-dance/agent-adaptor"
)

func TestThreadStoreHistoryAfterUsesExclusiveCursorAndCap(t *testing.T) {
	store := newThreadStore()
	for i := 1; i <= historyCap+5; i++ {
		store.appendHistory("thread-1", agentadaptor.StreamPayload{Seq: uint64(i)})
	}

	all := store.historyAfter("thread-1", 0)
	if got := len(all); got != historyCap {
		t.Fatalf("history len = %d, want %d", got, historyCap)
	}
	if all[0].Seq != 6 || all[len(all)-1].Seq != 505 {
		t.Fatalf("history window = [%d..%d], want [6..505]", all[0].Seq, all[len(all)-1].Seq)
	}

	incremental := store.historyAfter("thread-1", 500)
	if got := len(incremental); got != 5 {
		t.Fatalf("incremental len = %d, want 5", got)
	}
	if incremental[0].Seq != 501 || incremental[len(incremental)-1].Seq != 505 {
		t.Fatalf("incremental window = [%d..%d], want [501..505]", incremental[0].Seq, incremental[len(incremental)-1].Seq)
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
}

func (f *fakeRunHandle) Events() <-chan agentadaptor.RunEvent { return nil }

func (f *fakeRunHandle) StreamEvents() <-chan agentadaptor.StreamPayload { return nil }

func (f *fakeRunHandle) RunID() string { return f.runID }

func (f *fakeRunHandle) Wait(context.Context) (agentadaptor.RunResult, error) {
	return agentadaptor.RunResult{}, nil
}

func (f *fakeRunHandle) Cancel(context.Context) error { return nil }

func (f *fakeRunHandle) DecisionRequests() <-chan agentadaptor.DecisionRequest { return nil }

func (f *fakeRunHandle) ResolveDecision(requestID string, resp agentadaptor.DecisionResponse) error {
	f.resolveCalls++
	f.lastRequestID = requestID
	f.lastResponse = resp
	return f.resolveErr
}
