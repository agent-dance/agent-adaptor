package aguisession

import (
	"context"
	"fmt"
	"net/http"
	"time"

	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/hosttools/sessionrecorder"
)

// ActiveRun identifies the recorded run excluded from snapshot messages.
// Its boundary is a safe starting point for a fresh AG-UI observer.
type ActiveRun struct {
	// ThreadID is the public AG-UI thread identity.
	ThreadID string `json:"threadId"`
	// RunID is the public AG-UI run identity.
	RunID string `json:"runId"`
	// BoundaryHostSeq is the HostSeq of the run-boundary record.
	BoundaryHostSeq sessionrecorder.HostSeq `json:"boundaryHostSeq"`
}

// Snapshot pairs materialized history with a cursor for joining an active run.
type Snapshot struct {
	// Messages contains history strictly before ActiveRun, or all history if idle.
	Messages []Message `json:"messages"`
	// Cursor is immediately before ActiveRun's boundary, or the last record if idle.
	Cursor sessionrecorder.HostSeq `json:"cursor"`
	// ActiveRun is nil when the Session owns no recorded active run at the read.
	ActiveRun *ActiveRun `json:"activeRun"`
}

// Snapshot reads an already-authorized opaque Thread key. If a run is active,
// its messages are excluded and Observe from Cursor begins with its RUN_STARTED,
// including the recorded user turn. This supports fresh AG-UI client state.
// A concurrent completion or new admission can make the snapshot stale, but the
// recorded prefix and cursor still describe the same cut of the thread history.
func (s *Session) Snapshot(ctx context.Context, key string) (Snapshot, error) {
	for {
		if err := ctx.Err(); err != nil {
			return Snapshot{}, err
		}
		active, boundary := s.runBoundary(key)
		if active && boundary == 0 {
			// Admission has not published its boundary yet. Wait for publication
			// or release without holding the Session lock or rereading history.
			timer := time.NewTimer(time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return Snapshot{}, ctx.Err()
			case <-timer.C:
			}
			continue
		}
		records, err := s.opts.Recorder.Since(ctx, RecorderKey(key), 0)
		if err != nil {
			return Snapshot{}, err
		}
		currentActive, currentBoundary := s.runBoundary(key)
		if active != currentActive || boundary != currentBoundary {
			// Retry across admission/completion rather than pairing the messages
			// from one run with the boundary of another. Recorder I/O stays unlocked.
			continue
		}
		return s.snapshotRecords(key, records, active, boundary)
	}
}

func (s *Session) runBoundary(key string) (bool, sessionrecorder.HostSeq) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.active[key], s.boundaries[key]
}

func (s *Session) snapshotRecords(
	key string, records []sessionrecorder.EventRecord,
	active bool, boundary sessionrecorder.HostSeq,
) (Snapshot, error) {
	out := Snapshot{Messages: []Message{}}
	if len(records) > 0 {
		out.Cursor = records[len(records)-1].HostSeq
	}
	if active {
		found := false
		for i, rec := range records {
			if rec.HostSeq != boundary {
				continue
			}
			n, ok := rec.Event.(adaptor.Notice)
			if !ok || n.Kind != BoundaryKind {
				break
			}
			out.ActiveRun = &ActiveRun{
				ThreadID: str(n.Data, "threadId"),
				RunID:    str(n.Data, "runId"), BoundaryHostSeq: boundary,
			}
			out.Cursor = boundary - 1
			records = records[:i]
			found = true
			break
		}
		if !found {
			return Snapshot{}, fmt.Errorf("aguisession: active boundary %d missing from history", boundary)
		}
	}
	p := newProjector(s.opts.Events)
	out.Messages = foldRecords(p.project(records, 0, true, s.incompleteCode(key)))
	return out, nil
}

// SnapshotHandler returns JSON {messages, cursor, activeRun} for ?thread_id=T.
// Resolve authorizes and maps the read as OpRead, just like MessagesHandler.
func (s *Session) SnapshotHandler() http.Handler {
	return s.historyHandler(historySnapshot)
}
