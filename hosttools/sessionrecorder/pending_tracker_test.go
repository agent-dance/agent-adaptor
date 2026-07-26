package sessionrecorder

import (
	"fmt"
	"sync"
	"testing"

	agentadaptor "github.com/agent-dance/agent-adaptor"
)

func TestPendingTracker_Empty(t *testing.T) {
	t.Parallel()
	tr := NewPendingTracker()
	if got := tr.Snapshot(); got != nil {
		t.Fatalf("empty tracker: want nil snapshot, got %v", got)
	}
}

func TestPendingTracker_ApplySinglePending(t *testing.T) {
	t.Parallel()
	tr := NewPendingTracker()
	tr.Apply(requestedRec(1, "req-A", agentadaptor.HumanDecisionPermission, 0, "P-A"))

	got := tr.Snapshot()
	if len(got) != 1 || got[0].RequestID != "req-A" || got[0].Prompt != "P-A" {
		t.Fatalf("unexpected snapshot: %+v", got)
	}
}

func TestPendingTracker_ResolveRemovesFromPending(t *testing.T) {
	t.Parallel()
	tr := NewPendingTracker()
	tr.Apply(requestedRec(1, "req-A", agentadaptor.HumanDecisionPermission, 0, "P-A"))
	if got := tr.Snapshot(); len(got) != 1 {
		t.Fatalf("after Apply Requested: want 1, got %d", len(got))
	}
	tr.Apply(resolvedRec(2, "req-A", 0, agentadaptor.DecisionApproved))
	if got := tr.Snapshot(); got != nil {
		t.Fatalf("after Apply Resolved: want nil, got %v", got)
	}
}

func TestPendingTracker_RetryShowsLatestUnresolved(t *testing.T) {
	t.Parallel()
	tr := NewPendingTracker()
	tr.Apply(requestedRec(1, "req-A", agentadaptor.HumanDecisionPermission, 0, "P-A v0"))
	tr.Apply(resolvedRec(2, "req-A", 0, agentadaptor.DecisionRejected))
	tr.Apply(requestedRec(3, "req-A", agentadaptor.HumanDecisionPermission, 1, "P-A v1"))

	got := tr.Snapshot()
	if len(got) != 1 {
		t.Fatalf("want 1 pending, got %d", len(got))
	}
	if got[0].RetryAttempt != 1 {
		t.Errorf("want RetryAttempt=1, got %d", got[0].RetryAttempt)
	}
	if got[0].Prompt != "P-A v1" {
		t.Errorf("want latest prompt, got %q", got[0].Prompt)
	}
}

func TestPendingTracker_RetryAllResolved(t *testing.T) {
	t.Parallel()
	tr := NewPendingTracker()
	tr.Apply(requestedRec(1, "req-A", agentadaptor.HumanDecisionPermission, 0, "v0"))
	tr.Apply(resolvedRec(2, "req-A", 0, agentadaptor.DecisionRejected))
	tr.Apply(requestedRec(3, "req-A", agentadaptor.HumanDecisionPermission, 1, "v1"))
	tr.Apply(resolvedRec(4, "req-A", 1, agentadaptor.DecisionApproved))

	if got := tr.Snapshot(); got != nil {
		t.Fatalf("want nil after both attempts resolved, got %v", got)
	}
}

func TestPendingTracker_NonHITLRecordIsNoOp(t *testing.T) {
	t.Parallel()
	tr := NewPendingTracker()
	// A typical text.content record should have no impact on the tracker.
	tr.Apply(Record{
		HostSeq: 1,
		Payload: agentadaptor.StreamPayload{
			Kind:  agentadaptor.StreamTextContent,
			Delta: "hello",
		},
	})
	if got := tr.Snapshot(); got != nil {
		t.Fatalf("non-HITL record should not produce pending; got %v", got)
	}
}

func TestPendingTracker_NilHITLPayload(t *testing.T) {
	t.Parallel()
	tr := NewPendingTracker()
	// Defensive: a record carrying StreamHITLRequested kind but a nil
	// HITLRequested payload must be silently skipped.
	tr.Apply(Record{
		HostSeq: 1,
		Payload: agentadaptor.StreamPayload{Kind: agentadaptor.StreamHITLRequested},
	})
	if got := tr.Snapshot(); got != nil {
		t.Fatalf("nil HITLRequested payload should be skipped; got %v", got)
	}
}

func TestPendingTracker_HostSeqOrder(t *testing.T) {
	t.Parallel()
	tr := NewPendingTracker()
	// Apply out of HostSeq order; Snapshot must still return them
	// in HostSeq order.
	tr.Apply(requestedRec(3, "req-C", agentadaptor.HumanDecisionPlanReview, 0, "P-C"))
	tr.Apply(requestedRec(1, "req-A", agentadaptor.HumanDecisionPermission, 0, "P-A"))
	tr.Apply(requestedRec(2, "req-B", agentadaptor.HumanDecisionQuestion, 0, "P-B"))

	got := tr.Snapshot()
	wantOrder := []string{"req-A", "req-B", "req-C"}
	if len(got) != len(wantOrder) {
		t.Fatalf("want %d, got %d", len(wantOrder), len(got))
	}
	for i, want := range wantOrder {
		if got[i].RequestID != want {
			t.Errorf("[%d]: want %q, got %q", i, want, got[i].RequestID)
		}
	}
}

func TestPendingTracker_Reset(t *testing.T) {
	t.Parallel()
	tr := NewPendingTracker()
	tr.Apply(requestedRec(1, "req-A", agentadaptor.HumanDecisionPermission, 0, "P"))
	tr.Apply(requestedRec(2, "req-B", agentadaptor.HumanDecisionQuestion, 0, "Q"))
	if got := tr.Snapshot(); len(got) != 2 {
		t.Fatalf("pre-Reset: want 2, got %d", len(got))
	}
	tr.Reset()
	if got := tr.Snapshot(); got != nil {
		t.Fatalf("post-Reset: want nil, got %v", got)
	}
	// Reset doesn't break subsequent Apply.
	tr.Apply(requestedRec(3, "req-C", agentadaptor.HumanDecisionPermission, 0, "C"))
	got := tr.Snapshot()
	if len(got) != 1 || got[0].RequestID != "req-C" {
		t.Fatalf("post-Reset Apply: want [req-C], got %+v", got)
	}
}

func TestPendingTracker_SnapshotIsIndependent(t *testing.T) {
	t.Parallel()
	tr := NewPendingTracker()
	rec := requestedRec(1, "req-A", agentadaptor.HumanDecisionQuestion, 0, "P")
	rec.Payload.HITLRequested.Payload = map[string]any{"k": "v"}
	rec.Payload.HITLRequested.Choices = []agentadaptor.DecisionChoice{{Key: "yes"}}
	tr.Apply(rec)

	got := tr.Snapshot()
	got[0].Payload["k"] = "MUTATED"
	got[0].Choices[0].Key = "MUTATED"

	got2 := tr.Snapshot()
	if got2[0].Payload["k"] == "MUTATED" {
		t.Error("Snapshot mutation leaked back into tracker state")
	}
	if got2[0].Choices[0].Key == "MUTATED" {
		t.Error("Snapshot Choices mutation leaked back into tracker state")
	}
}

// --- concurrency ---------------------------------------------------------

// TestPendingTracker_Concurrent fires many Apply / Snapshot goroutines
// against the same tracker. With -race enabled, the test asserts:
//
//   (a) no data race in the Apply / Snapshot critical sections
//   (b) every Snapshot is internally consistent — a half-written entry
//       (e.g. zero-value RequestID) would betray a torn read
//
// The test must pass cleanly under `go test -race`.
func TestPendingTracker_Concurrent(t *testing.T) {
	t.Parallel()
	tr := NewPendingTracker()

	const writers = 8
	const eventsPerWriter = 200

	var writersWg sync.WaitGroup
	writersWg.Add(writers)
	for w := 0; w < writers; w++ {
		w := w
		go func() {
			defer writersWg.Done()
			for i := 0; i < eventsPerWriter; i++ {
				reqID := fmt.Sprintf("w%d-i%d", w, i)
				seq := HostSeq(w*1000 + i*2)
				tr.Apply(requestedRec(seq, reqID, agentadaptor.HumanDecisionPermission, 0, "p"))
				if i%2 == 0 {
					tr.Apply(resolvedRec(seq+1, reqID, 0, agentadaptor.DecisionApproved))
				}
			}
		}()
	}

	stop := make(chan struct{})
	var readersWg sync.WaitGroup
	readersWg.Add(4)
	for r := 0; r < 4; r++ {
		go func() {
			defer readersWg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				snap := tr.Snapshot()
				for _, req := range snap {
					if req.RequestID == "" {
						t.Errorf("snapshot returned entry with empty RequestID — torn read")
						return
					}
				}
			}
		}()
	}

	writersWg.Wait()
	close(stop)
	readersWg.Wait()

	// Final consistency: distinct RequestIDs only.
	final := tr.Snapshot()
	seen := make(map[string]bool, len(final))
	for _, req := range final {
		if seen[req.RequestID] {
			t.Errorf("duplicate RequestID in final snapshot: %q", req.RequestID)
		}
		seen[req.RequestID] = true
	}
}
