package sessionrecorder

import (
	"testing"
	"time"

	agentadaptor "github.com/agent-dance/agent-adaptor"
)

// helper: build one Record carrying a StreamHITLRequested payload.
func requestedRec(seq HostSeq, requestID string, kind agentadaptor.HumanDecisionKind, attempt int, prompt string) Record {
	return Record{
		HostSeq:    seq,
		RecordedAt: time.Unix(int64(seq), 0).UTC(),
		Payload: agentadaptor.StreamPayload{
			Kind:     agentadaptor.StreamHITLRequested,
			Sequence: uint64(seq),
			Seq:      uint64(seq),
			RunID:    "run-1",
			ThreadID: "thread-1",
			HITLRequested: &agentadaptor.HITLRequestedPayload{
				RequestID:    requestID,
				Kind:         kind,
				Source:       "test.source",
				Prompt:       prompt,
				CreatedAt:    time.Unix(int64(seq), 0).UTC(),
				Deadline:     time.Unix(int64(seq)+30, 0).UTC(),
				RetryAttempt: attempt,
			},
		},
	}
}

// helper: build one Record carrying a StreamHITLResolved payload.
func resolvedRec(seq HostSeq, requestID string, attempt int, result agentadaptor.DecisionResult) Record {
	return Record{
		HostSeq:    seq,
		RecordedAt: time.Unix(int64(seq), 0).UTC(),
		Payload: agentadaptor.StreamPayload{
			Kind:     agentadaptor.StreamHITLResolved,
			Sequence: uint64(seq),
			Seq:      uint64(seq),
			RunID:    "run-1",
			ThreadID: "thread-1",
			HITLResolved: &agentadaptor.HITLResolvedPayload{
				RequestID:    requestID,
				Kind:         agentadaptor.HumanDecisionPermission,
				Source:       "test.source",
				RetryAttempt: attempt,
				Result:       result,
				ResolvedAt:   time.Unix(int64(seq), 0).UTC(),
			},
		},
	}
}

func TestPendingDecisions_Empty(t *testing.T) {
	t.Parallel()
	if got := PendingDecisions(nil); got != nil {
		t.Fatalf("nil records: want nil, got %v", got)
	}
	if got := PendingDecisions([]Record{}); got != nil {
		t.Fatalf("empty records: want nil, got %v", got)
	}
}

func TestPendingDecisions_AllResolved(t *testing.T) {
	t.Parallel()
	records := []Record{
		requestedRec(1, "req-A", agentadaptor.HumanDecisionPermission, 0, "P-A"),
		resolvedRec(2, "req-A", 0, agentadaptor.DecisionApproved),
	}
	if got := PendingDecisions(records); got != nil {
		t.Fatalf("all resolved: want nil, got %v", got)
	}
}

func TestPendingDecisions_SinglePending(t *testing.T) {
	t.Parallel()
	records := []Record{
		requestedRec(1, "req-A", agentadaptor.HumanDecisionPermission, 0, "P-A"),
	}
	got := PendingDecisions(records)
	if len(got) != 1 {
		t.Fatalf("want 1 pending, got %d", len(got))
	}
	if got[0].RequestID != "req-A" || got[0].Prompt != "P-A" || got[0].RetryAttempt != 0 {
		t.Fatalf("unexpected pending: %+v", got[0])
	}
}

func TestPendingDecisions_MultiplePendingHostSeqOrder(t *testing.T) {
	t.Parallel()
	records := []Record{
		requestedRec(3, "req-C", agentadaptor.HumanDecisionPlanReview, 0, "P-C"),
		requestedRec(1, "req-A", agentadaptor.HumanDecisionPermission, 0, "P-A"),
		requestedRec(2, "req-B", agentadaptor.HumanDecisionQuestion, 0, "P-B"),
	}
	// Note: records is intentionally fed out of HostSeq order to verify
	// PendingDecisions does its own ordering by the Requested HostSeq.
	got := PendingDecisions(records)
	if len(got) != 3 {
		t.Fatalf("want 3 pending, got %d", len(got))
	}
	wantOrder := []string{"req-A", "req-B", "req-C"}
	for i, want := range wantOrder {
		if got[i].RequestID != want {
			t.Errorf("pending[%d]: want RequestID=%q, got %q", i, want, got[i].RequestID)
		}
	}
}

func TestPendingDecisions_RetryLatestOnlyResolved(t *testing.T) {
	t.Parallel()
	// req-A retry sequence: attempt 0 rejected → attempt 1 approved.
	// All attempts resolved; nothing is pending.
	records := []Record{
		requestedRec(1, "req-A", agentadaptor.HumanDecisionPermission, 0, "P-A"),
		resolvedRec(2, "req-A", 0, agentadaptor.DecisionRejected),
		requestedRec(3, "req-A", agentadaptor.HumanDecisionPermission, 1, "P-A retry"),
		resolvedRec(4, "req-A", 1, agentadaptor.DecisionApproved),
	}
	if got := PendingDecisions(records); got != nil {
		t.Fatalf("retry fully resolved: want nil, got %v", got)
	}
}

func TestPendingDecisions_RetryLatestPending(t *testing.T) {
	t.Parallel()
	// req-A retry sequence: attempt 0 rejected → attempt 1 still
	// pending. PendingDecisions surfaces only the latest attempt.
	records := []Record{
		requestedRec(1, "req-A", agentadaptor.HumanDecisionPermission, 0, "P-A v0"),
		resolvedRec(2, "req-A", 0, agentadaptor.DecisionRejected),
		requestedRec(3, "req-A", agentadaptor.HumanDecisionPermission, 1, "P-A v1"),
	}
	got := PendingDecisions(records)
	if len(got) != 1 {
		t.Fatalf("want 1 pending (latest retry), got %d", len(got))
	}
	if got[0].RetryAttempt != 1 {
		t.Errorf("want RetryAttempt=1 (latest), got %d", got[0].RetryAttempt)
	}
	if got[0].Prompt != "P-A v1" {
		t.Errorf("want Prompt of latest attempt, got %q", got[0].Prompt)
	}
}

func TestPendingDecisions_OutOfOrderResolveFirst(t *testing.T) {
	t.Parallel()
	// Defensive: a Resolved-only fragment (no matching Requested in the
	// supplied window) must not crash and must not surface as pending.
	records := []Record{
		resolvedRec(1, "req-orphan", 0, agentadaptor.DecisionApproved),
		requestedRec(2, "req-A", agentadaptor.HumanDecisionPermission, 0, "P-A"),
	}
	got := PendingDecisions(records)
	if len(got) != 1 || got[0].RequestID != "req-A" {
		t.Fatalf("want only req-A pending, got %+v", got)
	}
}

func TestPendingDecisions_NilHITLPayload(t *testing.T) {
	t.Parallel()
	// Defensive: a record with the HITL Kind but a nil typed payload
	// must be skipped, not crash the scan.
	records := []Record{
		{
			HostSeq:    1,
			RecordedAt: time.Now(),
			Payload: agentadaptor.StreamPayload{
				Kind: agentadaptor.StreamHITLRequested,
				// HITLRequested intentionally nil
			},
		},
		requestedRec(2, "req-A", agentadaptor.HumanDecisionPermission, 0, "P-A"),
	}
	got := PendingDecisions(records)
	if len(got) != 1 || got[0].RequestID != "req-A" {
		t.Fatalf("want only req-A pending, got %+v", got)
	}
}

func TestPendingDecisions_ReturnedSliceIsIndependent(t *testing.T) {
	t.Parallel()
	// Caller must be able to mutate the returned slice without touching
	// the underlying records (ensures Payload / Choices were cloned).
	rec := requestedRec(1, "req-A", agentadaptor.HumanDecisionQuestion, 0, "P-A")
	rec.Payload.HITLRequested.Payload = map[string]any{"k": "v"}
	rec.Payload.HITLRequested.Choices = []agentadaptor.DecisionChoice{{Key: "yes"}}

	got := PendingDecisions([]Record{rec})
	if len(got) != 1 {
		t.Fatalf("want 1, got %d", len(got))
	}
	got[0].Payload["k"] = "MUTATED"
	got[0].Choices[0].Key = "MUTATED"

	if rec.Payload.HITLRequested.Payload["k"] == "MUTATED" {
		t.Error("PendingDecisions did not clone Payload map; mutation leaked")
	}
	if rec.Payload.HITLRequested.Choices[0].Key == "MUTATED" {
		t.Error("PendingDecisions did not clone Choices slice; mutation leaked")
	}
}
