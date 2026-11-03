package aguisession

import (
	"context"
	"fmt"
	"net/http"

	adaptor "github.com/agent-dance/agent-adaptor/v2"
	"github.com/agent-dance/agent-adaptor/v2/bridges/agui"
	"github.com/agent-dance/agent-adaptor/v2/hosttools/sessionrecorder"
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
	records, state, err := s.readRecords(ctx, key, 0, true)
	if err != nil {
		return Snapshot{}, err
	}
	return s.snapshotRecords(records, state)
}

type runState struct {
	active   bool
	boundary sessionrecorder.HostSeq
	failed   bool
	revision uint64
}

func (s *Session) state(key string) runState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stateLocked(key)
}

func (s *Session) stateLocked(key string) runState {
	return runState{s.active[key], s.boundaries[key], s.recordingFailures[key], s.revisions[key]}
}

func (s *Session) changeLocked(key string) {
	s.revisions[key]++
	close(s.changed)
	s.changed = make(chan struct{})
}

func (s *Session) waitBoundary(ctx context.Context, key string, state runState) error {
	s.mu.Lock()
	changed := s.changed
	current := s.stateLocked(key)
	s.mu.Unlock()
	if current != state {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-changed:
		return nil
	}
}

func (s *Session) incompleteCodeFor(state runState) string {
	if s.opts.Unattended && state.failed {
		return "agui.recording_failed"
	}
	return "run.incomplete"
}

// readRecords pairs a committed prefix with a stable admission/completion cut.
// On a transition it extends that prefix from its last raw cursor, never from 0.
// No Session lock is held across recorder I/O or host projection hooks.
func (s *Session) readRecords(
	ctx context.Context, key string, after sessionrecorder.HostSeq, waitBoundary bool,
) ([]sessionrecorder.EventRecord, runState, error) {
	var records []sessionrecorder.EventRecord
	for {
		if err := ctx.Err(); err != nil {
			return nil, runState{}, err
		}
		before := s.state(key)
		if waitBoundary && before.active && before.boundary == 0 {
			// Admission publication is short, but storage and host hooks may block.
			// Use a cancellable wait; no busy loop and no mutex held over I/O.
			if err := s.waitBoundary(ctx, key, before); err != nil {
				return nil, runState{}, err
			}
			continue
		}
		batch, err := s.opts.Recorder.Since(ctx, RecorderKey(key), after)
		if err != nil {
			return nil, runState{}, err
		}
		for _, rec := range batch {
			if rec.HostSeq > after {
				records = append(records, rec)
				after = rec.HostSeq
			}
		}
		if current := s.state(key); current == before {
			return records, current, nil
		}
	}
}

func (s *Session) snapshotRecords(
	records []sessionrecorder.EventRecord, state runState,
) (Snapshot, error) {
	out := Snapshot{Messages: []Message{}}
	if len(records) > 0 {
		out.Cursor = records[len(records)-1].HostSeq
	}
	if state.active {
		found := false
		for i, rec := range records {
			if rec.HostSeq != state.boundary {
				continue
			}
			n, ok := rec.Event.(adaptor.Notice)
			if !ok || n.Kind != BoundaryKind {
				break
			}
			if !supportedProjection(str(n.Data, "projectionVersion")) {
				return Snapshot{}, &agui.InputError{Status: http.StatusInternalServerError,
					Code: "agui.projection_version_unsupported", Message: "unsupported or missing recording projection version"}
			}
			if str(n.Data, "threadId") == "" || str(n.Data, "runId") == "" {
				return Snapshot{}, fmt.Errorf("aguisession: active boundary %d has no public identity", state.boundary)
			}
			out.ActiveRun = &ActiveRun{
				ThreadID: str(n.Data, "threadId"),
				RunID:    str(n.Data, "runId"), BoundaryHostSeq: state.boundary,
			}
			out.Cursor = state.boundary - 1
			records = records[:i]
			found = true
			break
		}
		if !found {
			return Snapshot{}, fmt.Errorf("aguisession: active boundary %d missing from history", state.boundary)
		}
	}
	p := newProjector(s.opts.Events)
	out.Messages = foldRecords(p.project(records, 0, true, s.incompleteCodeFor(state)))
	return out, nil
}

// SnapshotHandler returns JSON {messages, cursor, activeRun} for ?thread_id=T.
// Resolve authorizes and maps the read as OpRead, just like MessagesHandler.
func (s *Session) SnapshotHandler() http.Handler {
	return s.historyHandler(historySnapshot)
}
