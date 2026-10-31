package aguisession

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	events "github.com/ag-ui-protocol/ag-ui/sdks/community/go/pkg/core/events"
	wire "github.com/ag-ui-protocol/ag-ui/sdks/community/go/pkg/encoding/sse"
	"github.com/agent-dance/agent-adaptor/bridges/agui"
	"github.com/agent-dance/agent-adaptor/bridges/internal/ssewire"
	"github.com/agent-dance/agent-adaptor/hosttools/sessionrecorder"
)

// Observe delivers whole projected records after a cursor, then follows the
// active run. It is a trusted host API using an already-authorized opaque key.
// Delivery is synchronous and cancellation-aware; callbacks must return promptly.
// A cursor is an application-level prefix contract, not SSE resumption.
func (s *Session) Observe(
	ctx context.Context, key string, after sessionrecorder.HostSeq,
	yield func(ProjectedRecord) error,
) error {
	return s.observe(ctx, key, after, nil, yield)
}

func (s *Session) observe(
	ctx context.Context, key string, after sessionrecorder.HostSeq,
	check func() error, yield func(ProjectedRecord) error,
) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	s.mu.Lock()
	s.observerID++
	id := s.observerID
	if s.observers[key] == nil {
		s.observers[key] = map[uint64]context.CancelFunc{}
	}
	s.observers[key][id] = cancel
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.observers[key], id)
		if len(s.observers[key]) == 0 {
			delete(s.observers, key)
		}
		s.mu.Unlock()
	}()
	var changes <-chan struct{}
	if follower, ok := s.opts.Recorder.(sessionrecorder.Follower); ok {
		ch, stop, err := follower.Subscribe(ctx, RecorderKey(key))
		if err != nil {
			return err
		}
		defer stop()
		changes = ch
	}
	// Polling also observes completion without a terminal (recording failure),
	// and provides periodic authorization rechecks while a run is idle.
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	// Reconstruct the prefix once, then retain translator state and read only
	// after the last raw record. Delivery cursors may be ahead of or behind it.
	p := newProjector(s.opts.Events)
	for {
		if ctx.Err() != nil {
			return nil
		}
		if check != nil {
			if check() != nil {
				return nil
			}
		}
		// Subscribe precedes this read; notifications are hints, storage is authority.
		wasActive := s.Active(key)
		hadPrefix := p.last.HostSeq != 0
		raw, err := s.opts.Recorder.Since(ctx, RecorderKey(key), p.last.HostSeq)
		if err != nil {
			return err
		}
		records := p.project(raw, after, !wasActive && !s.Active(key), s.incompleteCode(key))
		if hadPrefix && p.lateIncomplete {
			// A late diagnosis cannot amend an acknowledged record. Report it
			// before sending a later boundary with an unmatched prior lifecycle.
			return &agui.InputError{Code: s.incompleteCode(key), Message: "run has no recorded terminal"}
		}
		terminal := false
		for _, rec := range records {
			if ctx.Err() != nil {
				return nil
			}
			if check != nil {
				if check() != nil {
					return nil
				}
			}
			if rec.HostSeq <= after {
				continue
			}
			stampRecord(rec)
			if err := yield(rec); err != nil {
				return err
			}
			after = rec.HostSeq
			for _, ev := range rec.Events {
				kind := string(ev.GetBaseEvent().Type())
				if kind == "RUN_STARTED" {
					terminal = false
				}
				if kind == "RUN_FINISHED" || kind == "RUN_ERROR" {
					terminal = true
				}
			}
		}
		if terminal {
			s.mu.Lock()
			boundary := s.boundaries[key]
			active := s.active[key]
			s.mu.Unlock()
			// A prior terminal must not close an observer while admission is
			// preparing a new boundary on the same key.
			if !active || boundary > 0 && after >= boundary {
				return nil
			}
		}
		if !s.Active(key) {
			s.mu.Lock()
			failed := s.recordingFailures[key]
			s.mu.Unlock()
			if failed && !terminal {
				return &agui.InputError{Code: "agui.recording_failed", Message: "event recording failed"}
			}
			if !terminal && p.t != nil && p.incomplete {
				return &agui.InputError{Code: "run.incomplete", Message: "run has no recorded terminal"}
			}
			if wasActive {
				continue
			}
			return nil
		}
		select {
		case <-ctx.Done():
			return nil
		case _, ok := <-changes:
			if !ok {
				changes = nil
			}
		case <-ticker.C:
		}
	}
}

func stampRecord(rec ProjectedRecord) {
	for _, ev := range rec.Events {
		base := ev.GetBaseEvent()
		if base.Metadata == nil {
			base.Metadata = map[string]any{}
		}
		// Detach nested metadata: projection hooks may share maps across events.
		raw, _ := json.Marshal(base.Metadata)
		var metadata map[string]any
		_ = json.Unmarshal(raw, &metadata)
		if metadata == nil {
			metadata = map[string]any{}
		}
		base.Metadata = metadata
		ns, _ := metadata["agentAdaptor"].(map[string]any)
		if ns == nil {
			ns = map[string]any{}
			metadata["agentAdaptor"] = ns
		}
		ns["seq"] = rec.HostSeq
	}
}

// ObserveHandler authorizes OpFollow for ?thread_id=T&after=N and emits SSE.
// It rechecks Resolve before every batch and at least every 100ms while idle.
// Revocation closes the connection cleanly without inventing a terminal event.
func (s *Session) ObserveHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			methodError(w, http.MethodGet)
			return
		}
		id := r.URL.Query().Get("thread_id")
		if id == "" {
			writeError(w, fmt.Errorf("thread_id required"))
			return
		}
		after := uint64(0)
		var err error
		if raw := r.URL.Query().Get("after"); raw != "" {
			after, err = strconv.ParseUint(raw, 10, 64)
		}
		if err != nil {
			writeError(w, err)
			return
		}
		key, err := s.resolve(r, Access{Op: OpFollow, ThreadID: id})
		if err != nil {
			writeError(w, err)
			return
		}
		check := func() error {
			next, err := s.resolve(r, Access{Op: OpFollow, ThreadID: id})
			if err != nil {
				return err
			}
			if next != key {
				return fmt.Errorf("authorization mapping changed")
			}
			return nil
		}
		s.writeObservation(w, r, key, after, check, 0)
	})
}

func (s *Session) writeObservation(
	w http.ResponseWriter, r *http.Request, key string, after uint64,
	check func() error, omitUserAfter uint64,
) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.Header().Set("Connection", "keep-alive")
	timeout := s.opts.WriteTimeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	writer := ssewire.NewDeadlineWriter(w, timeout)
	defer writer.ClearDeadline()
	encoder := wire.NewSSEWriter()
	lastWrite := time.Now()
	deliveryCheck := func() error {
		if check != nil {
			if err := check(); err != nil {
				return err
			}
		}
		if s.opts.KeepAlivePing > 0 && time.Since(lastWrite) >= s.opts.KeepAlivePing {
			if _, err := io.WriteString(writer, ":keep-alive\n\n"); err != nil {
				return err
			}
			writer.Flush()
			lastWrite = time.Now()
		}
		return nil
	}
	sent := false
	err := s.observe(r.Context(), key, after, deliveryCheck, func(rec ProjectedRecord) error {
		for _, ev := range rec.Events {
			isSubmittedUser := omitUserAfter > 0 && rec.HostSeq > omitUserAfter &&
				rec.HostSeq <= omitUserAfter+3
			if isSubmittedUser && strings.HasPrefix(string(ev.GetBaseEvent().Type()), "TEXT_MESSAGE_") {
				continue
			}
			sent = true
			if err := encoder.WriteEvent(r.Context(), writer, ev); err != nil {
				return err
			}
		}
		writer.Flush()
		lastWrite = time.Now()
		return nil
	})
	if err != nil && r.Context().Err() == nil {
		var inputErr *agui.InputError
		if errors.As(err, &inputErr) &&
			(inputErr.Code == "agui.recording_failed" || inputErr.Code == "run.incomplete") {
			// Storage failure cannot allocate a new durable cursor. This is a
			// transport failure terminal, deliberately without a sequence claim.
			ev := events.NewRunErrorEvent(inputErr.Message)
			ev.Code = &inputErr.Code
			_ = encoder.WriteEvent(r.Context(), writer, ev)
			writer.Flush()
		} else if !sent {
			writeError(w, err)
		}
	}
}

func (s *Session) unattendedResponse(
	w http.ResponseWriter, r *http.Request, key string,
	prepared *agui.PreparedRequest, boundary sessionrecorder.EventRecord,
) {
	ctx, cancel := context.WithCancel(s.lifetime)
	run := &ownedRun{cancel: cancel, done: make(chan struct{})}
	s.mu.Lock()
	s.runs[key] = run
	s.mu.Unlock()
	go func() {
		defer s.finishAdmission()
		defer cancel()
		defer func() {
			s.mu.Lock()
			delete(s.runs, key)
			delete(s.active, key)
			close(run.done)
			s.mu.Unlock()
		}()
		stream := prepared.Target.Stream(ctx, prepared.Prompt, s.opts.CallOptions...)
		defer stream.Cancel()
		s.drain(ctx, key, prepared.Input, stream, boundary, nil)
	}()
	// The POST is merely the first observer; its disconnect never owns execution.
	check := func() error {
		next, err := s.resolve(r, Access{Op: OpFollow, ThreadID: prepared.Input.ThreadID})
		if err != nil {
			return err
		}
		if next != key {
			return fmt.Errorf("authorization mapping changed")
		}
		return nil
	}
	s.writeObservation(w, r, key, boundary.HostSeq-1, check, boundary.HostSeq)
}
