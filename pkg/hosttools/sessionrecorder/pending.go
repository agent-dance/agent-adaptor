package sessionrecorder

import (
	"sort"
	"sync"

	agentadaptor "github.com/agent-dance/agent-adaptor"
)

// PendingTracker maintains a running view of which DecisionRequests on
// a session are still pending (no matching Resolved event observed
// yet). Hosts feed it Record events as they arrive and call Snapshot
// whenever the UI needs the current pending list.
//
// # Why this exists
//
// PendingDecisions is a one-shot snapshot helper that walks the full
// record slice each call. In long-running conversations the natural
// "every new record → re-derive pending" pattern degrades to O(n²).
// PendingTracker keeps the per-session state behind a small surface so
// hosts get O(1) per event amortized:
//
//	tracker := sessionrecorder.NewPendingTracker()
//	for rec := range incomingRecords {
//	    tracker.Apply(rec)
//	    if isHITL(rec.Payload.Kind) {
//	        broadcastPending(tracker.Snapshot())
//	    }
//	}
//
// PendingDecisions remains the right tool for "give me the pending
// list once on demand" (admin dumps, REST handler responses), and is
// implemented internally as NewPendingTracker + Apply-all + Snapshot.
//
// # Retry semantics
//
// HITL retries emit a fresh StreamHITLRequested with an incremented
// RetryAttempt. PendingTracker only considers the LATEST RetryAttempt
// per RequestID: earlier attempts that already have a Resolved are
// considered done. The Snapshot reports the latest unresolved attempt's
// DecisionRequest.
//
// # Concurrency
//
// All PendingTracker methods are safe for concurrent use. A typical
// host has one writer goroutine (Record stream consumer) calling Apply
// and many readers (UI handlers) calling Snapshot.
//
// # Lifetime
//
// PendingTracker is per-session: hosts that aggregate by ThreadID
// should keep one tracker per ThreadID. Crossing session boundaries
// without Reset() will mix Pending sets and is a host bug.
type PendingTracker struct {
	mu sync.Mutex

	requested     map[trackerKey]trackerEntry
	resolved      map[trackerKey]struct{}
	latestAttempt map[string]int
	hasAttempt    map[string]bool
}

// trackerKey identifies one (RequestID, RetryAttempt) tuple, the unit
// at which Requested ↔ Resolved events match.
type trackerKey struct {
	requestID string
	attempt   int
}

// trackerEntry is the cached DecisionRequest envelope plus the HostSeq
// at which the Requested event was first observed (used for ordering
// the Snapshot).
type trackerEntry struct {
	seq     HostSeq
	request agentadaptor.DecisionRequest
}

// NewPendingTracker returns an empty tracker ready to receive Records.
func NewPendingTracker() *PendingTracker {
	return &PendingTracker{
		requested:     make(map[trackerKey]trackerEntry),
		resolved:      make(map[trackerKey]struct{}),
		latestAttempt: make(map[string]int),
		hasAttempt:    make(map[string]bool),
	}
}

// Apply applies one record's HITL events to the tracker. Records of
// non-HITL kinds (text content, tool calls, lifecycle markers, ...)
// are silently ignored, so callers that don't pre-filter their stream
// pay no cost beyond a single map-key lookup.
//
// Apply is O(1) amortized.
func (t *PendingTracker) Apply(rec Record) {
	switch rec.Payload.Kind {
	case agentadaptor.StreamHITLRequested:
		p := rec.Payload.HITLRequested
		if p == nil {
			return
		}
		key := trackerKey{requestID: p.RequestID, attempt: p.RetryAttempt}
		entry := trackerEntry{
			seq: rec.HostSeq,
			request: agentadaptor.DecisionRequest{
				RequestID:    p.RequestID,
				RunID:        rec.Payload.RunID,
				ThreadID:     rec.Payload.ThreadID,
				Kind:         p.Kind,
				Source:       p.Source,
				ToolCallID:   p.ToolCallID,
				Prompt:       p.Prompt,
				Payload:      cloneAny(p.Payload),
				Choices:      cloneChoices(p.Choices),
				CreatedAt:    p.CreatedAt,
				Deadline:     p.Deadline,
				RetryAttempt: p.RetryAttempt,
			},
		}
		t.mu.Lock()
		t.requested[key] = entry
		if !t.hasAttempt[p.RequestID] || p.RetryAttempt > t.latestAttempt[p.RequestID] {
			t.latestAttempt[p.RequestID] = p.RetryAttempt
			t.hasAttempt[p.RequestID] = true
		}
		t.mu.Unlock()

	case agentadaptor.StreamHITLResolved:
		p := rec.Payload.HITLResolved
		if p == nil {
			return
		}
		key := trackerKey{requestID: p.RequestID, attempt: p.RetryAttempt}
		t.mu.Lock()
		t.resolved[key] = struct{}{}
		t.mu.Unlock()
	}
}

// Snapshot returns the current pending DecisionRequests, in the order
// the latest unresolved attempt was first observed (HostSeq ascending).
// The returned slice is freshly allocated; callers may mutate it.
//
// Snapshot is O(n) in the number of distinct RequestIDs the tracker
// has seen, NOT in the number of records applied.
func (t *PendingTracker) Snapshot() []agentadaptor.DecisionRequest {
	t.mu.Lock()
	type ordered struct {
		seq HostSeq
		req agentadaptor.DecisionRequest
	}
	out := make([]ordered, 0, len(t.latestAttempt))
	for reqID, attempt := range t.latestAttempt {
		key := trackerKey{requestID: reqID, attempt: attempt}
		if _, done := t.resolved[key]; done {
			continue
		}
		entry, ok := t.requested[key]
		if !ok {
			continue
		}
		// Each Snapshot must return its own copies of Payload /
		// Choices so a caller mutating snapshot[i].Payload doesn't
		// pollute either the tracker state or future snapshots.
		req := entry.request
		req.Payload = cloneAny(req.Payload)
		req.Choices = cloneChoices(req.Choices)
		out = append(out, ordered{seq: entry.seq, req: req})
	}
	t.mu.Unlock()

	if len(out) == 0 {
		return nil
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].seq < out[j].seq
	})
	result := make([]agentadaptor.DecisionRequest, len(out))
	for i, e := range out {
		result[i] = e.req
	}
	return result
}

// Reset clears all tracked state. Use after closing a session or when
// re-loading from a fresh backend cursor (e.g. after Recorder restart).
func (t *PendingTracker) Reset() {
	t.mu.Lock()
	t.requested = make(map[trackerKey]trackerEntry)
	t.resolved = make(map[trackerKey]struct{})
	t.latestAttempt = make(map[string]int)
	t.hasAttempt = make(map[string]bool)
	t.mu.Unlock()
}

// PendingDecisions scans records (typically from Recorder.Since or a
// fresh Recorder.Load) and returns the DecisionRequests whose matching
// Resolved event has not been observed yet, in HostSeq order.
//
// This is a one-shot snapshot helper, suitable for "give me pending
// once" use cases like admin dumps or REST handler responses. For
// long-running session loops where the host re-derives pending on
// every new record, prefer PendingTracker — it gives O(1) amortized
// per Apply versus the O(n²) PendingDecisions would degrade to under
// the same loop. See PendingTracker's godoc for the cookbook.
//
// The returned slice is freshly allocated and safe for the caller to
// mutate. Empty input or input with no HITL events returns nil.
func PendingDecisions(records []Record) []agentadaptor.DecisionRequest {
	if len(records) == 0 {
		return nil
	}
	t := NewPendingTracker()
	for _, rec := range records {
		t.Apply(rec)
	}
	return t.Snapshot()
}

// cloneAny makes a shallow copy of a map[string]any so the returned
// DecisionRequest doesn't alias the recorder's internal record.
func cloneAny(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// cloneChoices makes a shallow copy of the choices slice.
func cloneChoices(in []agentadaptor.DecisionChoice) []agentadaptor.DecisionChoice {
	if len(in) == 0 {
		return nil
	}
	out := make([]agentadaptor.DecisionChoice, len(in))
	copy(out, in)
	return out
}
