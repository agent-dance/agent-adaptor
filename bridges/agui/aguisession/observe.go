package aguisession

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
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
	check func(context.Context) error, yield func(ProjectedRecord) error,
) error {
	ctx, detach := s.observerContext(ctx, key)
	defer detach()
	return s.follow(ctx, key, after, check, yield)
}

// Registration covers all deliveries, including an HTTP transport error after
// follow returns. Revoke can therefore close every delivery path cleanly.
func (s *Session) observerContext(ctx context.Context, key string) (context.Context, func()) {
	ctx, cancel := context.WithCancel(ctx)
	s.mu.Lock()
	s.observerID++
	id := s.observerID
	if s.observers[key] == nil {
		s.observers[key] = map[uint64]context.CancelFunc{}
	}
	s.observers[key][id] = cancel
	s.mu.Unlock()
	return ctx, func() {
		cancel()
		s.mu.Lock()
		delete(s.observers[key], id)
		if len(s.observers[key]) == 0 {
			delete(s.observers, key)
		}
		s.mu.Unlock()
	}
}

func (s *Session) follow(
	ctx context.Context, key string, after sessionrecorder.HostSeq,
	check func(context.Context) error, yield func(ProjectedRecord) error,
) error {
	var changes <-chan struct{}
	if follower, ok := s.opts.Recorder.(sessionrecorder.Follower); ok {
		ch, stop, err := follower.Subscribe(ctx, RecorderKey(key))
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		defer stop()
		changes = ch
	}
	// Polling also observes completion without a terminal (recording failure),
	// and provides periodic authorization rechecks while a run is idle.
	interval := 100 * time.Millisecond
	if s.opts.KeepAlivePing > 0 && s.opts.KeepAlivePing < interval {
		interval = s.opts.KeepAlivePing
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	// Reconstruct the prefix once, then retain translator state and read only
	// after the last raw record. Delivery cursors may be ahead of or behind it.
	p := newProjector(s.opts.Events)
	for {
		if ctx.Err() != nil {
			return nil
		}
		if check != nil {
			if check(ctx) != nil {
				return nil
			}
		}
		// Subscribe precedes this read; notifications are hints, storage is authority.
		hadPrefix := p.last.HostSeq != 0
		raw, state, err := s.readRecords(ctx, key, p.last.HostSeq, false)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		code := s.incompleteCodeFor(state)
		if state.failed {
			code = "agui.recording_failed"
		}
		records := p.project(raw, after, !state.active, code)
		if hadPrefix && p.lateIncomplete {
			// A late diagnosis cannot amend an acknowledged record. Report it
			// before sending a later boundary with an unmatched prior lifecycle.
			return &agui.InputError{Code: code, Message: "run has no recorded terminal"}
		}
		terminal := false
		for _, rec := range records {
			if ctx.Err() != nil {
				return nil
			}
			if check != nil {
				if check(ctx) != nil {
					return nil
				}
			}
			if rec.HostSeq <= after {
				continue
			}
			if err := stampRecord(rec); err != nil {
				return err
			}
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
		// A run may start/finish during delivery (including inside yield). Re-read
		// its suffix before deciding whether this observation is complete.
		if s.state(key) != state {
			continue
		}
		if terminal && (!state.active || state.boundary > 0 && after >= state.boundary) {
			return nil
		}
		if !state.active {
			if !terminal && p.t != nil && p.incomplete {
				return &agui.InputError{Code: code, Message: "run has no recorded terminal"}
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

func stampRecord(rec ProjectedRecord) error {
	for _, ev := range rec.Events {
		base := ev.GetBaseEvent()
		if base.Metadata == nil {
			base.Metadata = map[string]any{}
		}
		// Only the metadata envelope and our namespace are modified. Copy those
		// maps without serializing opaque JSON values or losing numeric precision.
		metadata := make(map[string]any, len(base.Metadata))
		for k, v := range base.Metadata {
			metadata[k] = v
		}
		ns := make(map[string]any)
		if original, ok := metadata["agentAdaptor"].(map[string]any); ok {
			for k, v := range original {
				ns[k] = v
			}
		} else if original := metadata["agentAdaptor"]; original != nil {
			// Hooks may use another JSON-object representation, e.g. a typed map
			// or RawMessage. Preserve its fields and exact JSON numbers as well.
			raw, err := json.Marshal(original)
			if err != nil {
				return fmt.Errorf("aguisession: observer metadata: %w", err)
			}
			decoder := json.NewDecoder(bytes.NewReader(raw))
			decoder.UseNumber()
			if err := decoder.Decode(&ns); err != nil || ns == nil {
				return fmt.Errorf("aguisession: agentAdaptor observer metadata must be a JSON object")
			}
		}
		base.Metadata = metadata
		metadata["agentAdaptor"] = ns
		ns["seq"] = rec.HostSeq
	}
	return nil
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
			writeError(w, &agui.InputError{Code: "agui.identity_required", Message: "thread_id required"})
			return
		}
		after := uint64(0)
		var err error
		if raw := r.URL.Query().Get("after"); raw != "" {
			after, err = strconv.ParseUint(raw, 10, 64)
		}
		if err != nil {
			writeError(w, &agui.InputError{Code: "agui.invalid_cursor", Message: "after must be an unsigned integer"})
			return
		}
		key, err := s.resolve(r, Access{Op: OpFollow, ThreadID: id})
		if err != nil {
			writeError(w, err)
			return
		}
		check := func(ctx context.Context) error {
			next, err := s.resolve(r.WithContext(ctx), Access{Op: OpFollow, ThreadID: id})
			if err != nil {
				return err
			}
			if next != key {
				return fmt.Errorf("authorization mapping changed")
			}
			return nil
		}
		s.writeObservation(w, r, key, after, check)
	})
}

func (s *Session) writeObservation(
	w http.ResponseWriter, r *http.Request, key string, after uint64,
	check func(context.Context) error,
) {
	ctx, detach := s.observerContext(r.Context(), key)
	defer detach()
	r = r.WithContext(ctx)
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
	sent := false
	deliveryCheck := func(ctx context.Context) error {
		if check != nil {
			if err := check(ctx); err != nil {
				return err
			}
		}
		if sent && s.opts.KeepAlivePing > 0 && time.Since(lastWrite) >= s.opts.KeepAlivePing {
			if _, err := io.WriteString(writer, ":keep-alive\n\n"); err != nil {
				return err
			}
			writer.Flush()
			if err := writer.Err(); err != nil {
				return err
			}
			lastWrite = time.Now()
		}
		return nil
	}
	err := s.follow(r.Context(), key, after, deliveryCheck, func(rec ProjectedRecord) error {
		if len(rec.Events) == 0 {
			return nil
		}
		for _, ev := range rec.Events {
			sent = true
			if err := encoder.WriteEvent(r.Context(), writer, ev); err != nil {
				return err
			}
		}
		writer.Flush()
		lastWrite = time.Now()
		return writer.Err()
	})
	if err == nil || r.Context().Err() != nil || writer.Err() != nil {
		return
	}
	var inputErr *agui.InputError
	isDiagnosis := errors.As(err, &inputErr) &&
		(inputErr.Code == "agui.recording_failed" || inputErr.Code == "run.incomplete")
	if !sent && !isDiagnosis {
		writeError(w, err)
		return
	}
	// Storage/read failure cannot allocate a durable cursor. An established
	// SSE response receives an unsequenced transport error, never appended JSON.
	code, message := "agui.observation_failed", "event observation failed"
	if isDiagnosis {
		code, message = inputErr.Code, inputErr.Message
	}
	ev := events.NewRunErrorEvent(message)
	ev.Code = &code
	_ = encoder.WriteEvent(r.Context(), writer, ev)
	writer.Flush()
}

func (s *Session) unattendedResponse(
	w http.ResponseWriter, r *http.Request, key string,
	prepared *agui.PreparedRequest, boundary sessionrecorder.EventRecord, run *ownedRun,
) {
	go func() {
		defer close(run.done)
		defer s.finishAdmission()
		defer run.cancel()
		defer s.release(key)
		stream := prepared.Target.Stream(run.ctx, prepared.Prompt, s.opts.CallOptions...)
		defer stream.Cancel()
		s.drain(run.ctx, key, prepared.Input, stream, boundary, nil)
	}()
	// The POST is merely the first observer; its disconnect never owns execution.
	check := func(ctx context.Context) error {
		// Nil Resolve permits all access, including a fresh key selected by the
		// authorized divergence hook. The POST already owns that resolved key.
		if s.opts.Resolve == nil {
			return nil
		}
		next, err := s.resolve(r.WithContext(ctx), Access{Op: OpFollow, ThreadID: prepared.Input.ThreadID})
		if err != nil {
			return err
		}
		if next != key {
			return fmt.Errorf("authorization mapping changed")
		}
		return nil
	}
	s.writeObservation(w, r, key, boundary.HostSeq-1, check)
}
