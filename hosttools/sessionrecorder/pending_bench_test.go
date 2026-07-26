package sessionrecorder

import (
	"fmt"
	"testing"

	agentadaptor "github.com/agent-dance/agent-adaptor"
)

// genHITLRecords builds a sequence of n records, half Requested and
// half (matching) Resolved, suitable for stressing both code paths.
func genHITLRecords(n int) []Record {
	out := make([]Record, 0, n)
	for i := 0; i < n; i++ {
		seq := HostSeq(i + 1)
		reqID := fmt.Sprintf("req-%d", i/2)
		if i%2 == 0 {
			out = append(out, requestedRec(seq, reqID, agentadaptor.HumanDecisionPermission, 0, "p"))
		} else {
			out = append(out, resolvedRec(seq, reqID, 0, agentadaptor.DecisionApproved))
		}
	}
	return out
}

// BenchmarkPendingDecisions_Snapshot benchmarks the one-shot helper
// against ever-growing record histories. We expect O(n) per call.
func BenchmarkPendingDecisions_Snapshot(b *testing.B) {
	for _, size := range []int{100, 1000, 10000} {
		size := size
		b.Run(fmt.Sprintf("n=%d", size), func(b *testing.B) {
			records := genHITLRecords(size)
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_ = PendingDecisions(records)
			}
		})
	}
}

// BenchmarkPendingTracker_Apply benchmarks the incremental Apply +
// Snapshot path. Apply is expected to be O(1) per call regardless of
// total history size; the per-Snapshot cost depends only on the count
// of distinct unresolved RequestIDs (typically tiny).
func BenchmarkPendingTracker_Apply(b *testing.B) {
	for _, size := range []int{100, 1000, 10000} {
		size := size
		b.Run(fmt.Sprintf("n=%d", size), func(b *testing.B) {
			records := genHITLRecords(size)
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				tr := NewPendingTracker()
				for _, rec := range records {
					tr.Apply(rec)
				}
				_ = tr.Snapshot()
			}
		})
	}
}

// BenchmarkLongChat_PendingPerEvent simulates the host pattern that
// motivated this change: every new HITL record triggers a
// "give me the current pending list" call. The Tracker variant is
// expected to scale linearly in n (each event costs O(1) to apply +
// O(pending) to snapshot), while the Snapshot variant scales O(n²).
func BenchmarkLongChat_PendingPerEvent(b *testing.B) {
	const sessionLen = 2000

	b.Run("tracker (O(n))", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			tr := NewPendingTracker()
			records := genHITLRecords(sessionLen)
			for _, rec := range records {
				tr.Apply(rec)
				_ = tr.Snapshot() // host re-renders on every event
			}
		}
	})

	b.Run("snapshot helper (O(n^2))", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			history := make([]Record, 0, sessionLen)
			records := genHITLRecords(sessionLen)
			for _, rec := range records {
				history = append(history, rec)
				_ = PendingDecisions(history) // host re-derives on every event
			}
		}
	})
}
