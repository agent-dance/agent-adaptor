// Package aguisession provides optional recorded AG-UI execution and history.
// It consumes public Runner and EventRecorder contracts; storage and authorization
// remain host decisions. One Session must own all runs for its recorder keys.
package aguisession

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	events "github.com/ag-ui-protocol/ag-ui/sdks/community/go/pkg/core/events"
	wire "github.com/ag-ui-protocol/ag-ui/sdks/community/go/pkg/encoding/sse"
	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/bridges/agui"
	"github.com/agent-dance/agent-adaptor/bridges/internal/bridgekey"
	"github.com/agent-dance/agent-adaptor/bridges/internal/ssewire"
	"github.com/agent-dance/agent-adaptor/hosttools/sessionrecorder"
)

// ProjectionVersion identifies the persisted boundary and projection contract.
// Unknown and older versions fail closed; recorded history requires migration.
const ProjectionVersion = "agui-session/2"

// BoundaryKind is the reserved Notice kind carrying identity, diagnostics and user input.
const BoundaryKind = "agentAdaptor.agui.boundary"

// TerminalKind is the reserved Notice kind carrying the authoritative outcome.
const TerminalKind = "agentAdaptor.agui.terminal"

// Op identifies an authorized session operation.
type Op string

const (
	// OpStart authorizes a run.
	OpStart Op = "start"
	// OpRead authorizes history access.
	OpRead Op = "read"
	// OpFollow authorizes catch-up and active observation.
	OpFollow Op = "follow"
	// OpCancel authorizes cancellation.
	OpCancel Op = "cancel"
)

// Access describes the public resource being accessed.
type Access struct {
	// Op is the requested operation.
	Op Op
	// ThreadID is the public client identity.
	ThreadID string
	// Input is populated only for starts.
	Input *agui.RunAgentInput
}

// Options configures recording, projection, and host policy.
type Options struct {
	// Unattended binds runs to BaseContext instead of the POST request.
	Unattended bool
	// BaseContext is required in unattended mode and owns worker lifetime.
	BaseContext context.Context
	// MaxConcurrentRuns bounds admitted unattended runs; default is 8.
	MaxConcurrentRuns int

	// Recorder is required; Session never substitutes ephemeral storage.
	Recorder sessionrecorder.EventRecorder
	// Resolve authorizes and maps starts, reads, follows and cancellations.
	// Production hosts must install it: a client thread ID is not authorization.
	// Nil permits all access.
	Resolve func(*http.Request, Access) (string, error)
	// WriteTimeout bounds each SSE write; the default is 30 seconds.
	WriteTimeout time.Duration
	// KeepAlivePing sends periodic SSE comments when positive.
	KeepAlivePing time.Duration
	// ResolveDivergence may select a different key after history mismatches or
	// a checkpoint cannot attest the latest recorded turn.
	// The destination must have no recorded history. Returning the same key
	// does not waive verification.
	ResolveDivergence func(context.Context, *agui.RunAgentInput, string) (string, error)
	// Input configures prompt preparation, including explicit history rehydration.
	Input agui.InputOptions
	// Events configures deterministic projection, including ProjectionHook.
	Events []agui.EventTranslatorOption
	// CallOptions are passed to Runner.Stream.
	CallOptions []adaptor.CallOption
	// Observe receives successfully recorded source events, including live approvals.
	// It runs synchronously in each run's sole drain and must return promptly.
	// Different keys may invoke it concurrently.
	Observe func(key string, event adaptor.Event)
	// OnRunEnd releases host runtime state after draining, including recording failure.
	OnRunEnd func(key string)
}

// Session owns recorded runs and optional host-lifetime unattended execution.
type Session struct {
	runner            adaptor.Runner
	opts              Options
	mu                sync.Mutex
	active            map[string]bool
	lifetime          context.Context
	stop              context.CancelFunc
	closed            bool
	running           int
	shutdownDone      chan struct{}
	runs              map[string]*ownedRun
	observers         map[string]map[uint64]context.CancelFunc
	observerID        uint64
	recordingFailures map[string]bool
	boundaries        map[string]sessionrecorder.HostSeq
	revisions         map[string]uint64
	changed           chan struct{}
}

// New creates a session. Nil runner/recorder, or unattended mode without a
// BaseContext, is a programming error.
func New(runner adaptor.Runner, opts Options) *Session {
	if runner == nil || opts.Recorder == nil {
		panic("aguisession: runner and recorder required")
	}
	opts.Events = append([]agui.EventTranslatorOption(nil), opts.Events...)
	opts.CallOptions = append([]adaptor.CallOption(nil), opts.CallOptions...)
	if opts.Unattended && opts.BaseContext == nil {
		panic("aguisession: unattended mode requires BaseContext")
	}
	if opts.MaxConcurrentRuns <= 0 {
		opts.MaxConcurrentRuns = 8
	}
	base := opts.BaseContext
	if base == nil {
		base = context.Background()
	}
	lifetime, stop := context.WithCancel(base)
	return &Session{
		runner: runner, opts: opts, active: map[string]bool{},
		lifetime: lifetime, stop: stop, runs: map[string]*ownedRun{},
		observers:         map[string]map[uint64]context.CancelFunc{},
		recordingFailures: map[string]bool{}, boundaries: map[string]sessionrecorder.HostSeq{},
		revisions: map[string]uint64{}, shutdownDone: make(chan struct{}), changed: make(chan struct{}),
	}
}

func conflict(code, message string) error {
	return &agui.InputError{Code: code, Message: message, Status: 409}
}

// VerifyHistory implements the sse.Options.VerifyHistory extension point.
// It compares IDs and roles, in order, excluding activities and reasoning.
// Reasoning is optional presentation state: assistant-ui 0.1.1 drops empty
// reasoning on round-trip and all plain reasoning when showThinking is false.
// User/assistant/tool identities still match strictly, including tool-only assistants.
// Content and error are intentionally not compared. The trailing user turn is
// excluded from comparison, but its ID must be absent from all recorded messages.
func (s *Session) VerifyHistory(ctx context.Context, in *agui.RunAgentInput, runner adaptor.Runner, key string) (string, bool, error) {
	records, err := s.Events(ctx, key, 0)
	if err != nil {
		return key, false, err
	}
	messages := foldRecords(records)
	if len(in.Messages) > 0 {
		submitted := in.Messages[len(in.Messages)-1]
		for _, message := range messages {
			if submitted.ID == str(message, "id") {
				return key, false, &agui.InputError{Code: "agui.duplicate_message", Status: 409,
					Path:    fmt.Sprintf("/messages/%d/id", len(in.Messages)-1),
					Message: "submitted message ID already appears in recorded history"}
			}
		}
	}
	seenRun, healthy := false, false
	for _, record := range records {
		for _, event := range record.Events {
			switch event.Type() {
			case events.EventTypeRunStarted:
				seenRun, healthy = true, false
			case events.EventTypeRunFinished:
				healthy = true
			case events.EventTypeRunError:
				healthy = false
			}
		}
	}
	prior := in.Messages
	if len(prior) > 0 {
		prior = prior[:len(prior)-1]
	}
	type identity struct{ id, role string }
	pairs := func(ms []agui.Message) []identity {
		out := []identity{}
		for _, m := range ms {
			if m.Role != "activity" && m.Role != "reasoning" {
				out = append(out, identity{m.ID, m.Role})
			}
		}
		return out
	}
	recorded := []agui.Message{}
	for _, m := range messages {
		recorded = append(recorded, agui.Message{ID: str(m, "id"), Role: str(m, "role")})
	}
	equal := slices.Equal(pairs(prior), pairs(recorded))
	usable := false
	if equal {
		usable, err = agui.CheckpointUsable(ctx, runner)
		if err != nil {
			return key, false, err
		}
	}
	if !equal || usable && seenRun && !healthy {
		if s.opts.ResolveDivergence != nil {
			newKey, e := s.opts.ResolveDivergence(ctx, in, key)
			if e != nil {
				return key, false, e
			}
			if newKey != key {
				// A branch must not append onto a different conversation. Admission
				// reserves this destination before checking it again in serveRun.
				if err := s.requireFresh(ctx, newKey); err != nil {
					return key, false, err
				}
				return newKey, false, nil
			}
		}
		if equal {
			// Failed executions normally preserve the previous healthy checkpoint.
			// It cannot attest that the newly recorded failed/partial turn is in
			// provider context. Rehydrating on that same checkpoint is also unsafe.
			return key, false, conflict("agui.history_unrecoverable",
				"checkpoint cannot attest the latest failed or incomplete turn; resolve to a fresh key")
		}
		return key, false, conflict("agui.history_diverged", "client message IDs/roles differ from recorded history")
	}
	return key, usable, nil
}

func (s *Session) requireFresh(ctx context.Context, key string) error {
	records, err := s.opts.Recorder.Since(ctx, RecorderKey(key), 0)
	if err != nil {
		return err
	}
	if len(records) != 0 {
		return conflict("agui.history_diverged", "history branch requires a fresh destination key")
	}
	return nil
}

// Handler returns the POST RunAgentInput endpoint. Disconnect cancels execution
// unless Unattended is enabled; then it detaches only this observer.
func (s *Session) Handler() http.Handler { return http.HandlerFunc(s.serveRun) }

func writeError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	var in *agui.InputError
	if errors.As(err, &in) {
		status = http.StatusBadRequest
		if in.Status != 0 {
			status = in.Status
		}
	} else {
		in = &agui.InputError{Code: "agui.request_failed", Message: err.Error()}
	}
	var large *http.MaxBytesError
	if errors.As(err, &large) {
		status = 413
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Del("Connection")
	w.Header().Del("X-Accel-Buffering")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": in})
}

func (s *Session) resolve(r *http.Request, access Access) (string, error) {
	if s.opts.Resolve != nil {
		return s.opts.Resolve(r, access)
	}
	// A fixed Thread is already host-bound. Reads, first-observer authorization
	// and cancellation must address the same key used by execution admission.
	if fixed, ok := s.runner.(*adaptor.Thread); ok {
		return fixed.Key(), nil
	}
	if access.ThreadID == "" {
		return "", nil
	}
	return ThreadKey(access.ThreadID), nil
}

func (s *Session) release(key string) {
	s.mu.Lock()
	delete(s.active, key)
	delete(s.runs, key)
	s.changeLocked(key)
	s.mu.Unlock()
}

func (s *Session) reserve(ctx context.Context, key, runID string, run *ownedRun) error {
	s.mu.Lock()
	if s.active[key] {
		s.mu.Unlock()
		return conflict("agui.run_active", "thread already has an active run")
	}
	s.active[key] = true
	s.boundaries[key] = 0
	s.changeLocked(key)
	if run != nil {
		s.runs[key] = run
	}
	s.mu.Unlock()
	records, err := s.opts.Recorder.Since(ctx, RecorderKey(key), 0)
	if err == nil {
		for _, rec := range records {
			if n, ok := rec.Event.(adaptor.Notice); ok && n.Kind == BoundaryKind && str(n.Data, "runId") == runID {
				err = conflict("agui.duplicate_run", "runId already recorded")
				break
			}
		}
	}
	if err != nil {
		s.release(key)
	}
	return err
}

func methodError(w http.ResponseWriter, allow string) {
	w.Header().Set("Allow", allow)
	writeError(w, &agui.InputError{Status: http.StatusMethodNotAllowed,
		Code: "agui.method_not_allowed", Message: "method not allowed"})
}

func (s *Session) serveRun(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodError(w, http.MethodPost)
		return
	}
	if err := s.admit(); err != nil {
		writeError(w, err)
		return
	}
	handedOff := false
	reservation := runReservation{}
	defer func() {
		if handedOff {
			return
		}
		if reservation.held {
			s.release(reservation.key)
		}
		if reservation.run != nil {
			reservation.run.cancel()
		}
		s.finishAdmission()
		if reservation.run != nil {
			close(reservation.run.done)
		}
	}()
	prepareRequest := r
	if s.opts.Unattended {
		runCtx, cancel := context.WithCancel(s.lifetime)
		reservation.run = &ownedRun{ctx: runCtx, cancel: cancel, done: make(chan struct{})}
		// Preserve authorization/request values while binding admission, including
		// preparation, to the worker. Disconnect owns only the first observer.
		prepareCtx, prepareCancel := context.WithCancel(context.WithoutCancel(r.Context()))
		stop := context.AfterFunc(runCtx, prepareCancel)
		defer func() { stop(); prepareCancel() }()
		prepareRequest = r.WithContext(prepareCtx)
	}
	prepared, err := s.prepareRun(prepareRequest, &reservation)
	if err != nil {
		writeError(w, err)
		return
	}
	key := reservation.key
	boundary, err := s.recordBoundary(prepareRequest.Context(), key, prepared)
	if err != nil {
		writeError(w, err)
		return
	}
	if s.opts.Unattended {
		handedOff = true
		reservation.held = false
		s.unattendedResponse(w, r, key, prepared, boundary, reservation.run)
		return
	}
	s.streamResponse(w, r, key, prepared, boundary)
}

type runReservation struct {
	key  string
	held bool
	run  *ownedRun
}

// prepareRun uses the shared preparation path while retaining admission ownership
// through history verification and any authorized switch to a fresh key.
func (s *Session) prepareRun(r *http.Request, reservation *runReservation) (*agui.PreparedRequest, error) {
	prepared, err := agui.PrepareHTTPRequest(r, s.runner, agui.RequestOptions{
		Input: s.opts.Input,
		ResolveThreadKey: func(r *http.Request, in *agui.RunAgentInput) (string, error) {
			key, err := s.resolve(r, Access{Op: OpStart, ThreadID: in.ThreadID, Input: in})
			if err != nil {
				return "", err
			}
			if fixed, ok := s.runner.(*adaptor.Thread); ok {
				key = fixed.Key()
			}
			if key != "" {
				if err := s.reserve(r.Context(), key, in.RunID, reservation.run); err != nil {
					return "", err
				}
				reservation.key, reservation.held = key, true
			}
			return key, nil
		},
		VerifyHistory: func(ctx context.Context, in *agui.RunAgentInput, runner adaptor.Runner, key string) (string, bool, error) {
			next, usable, err := s.VerifyHistory(ctx, in, runner, key)
			if err != nil {
				return key, false, err
			}
			if next != key {
				if _, fixed := s.runner.(*adaptor.Thread); fixed {
					return key, false, conflict("agui.history_diverged", "a fixed Thread cannot switch keys; supply an Agent")
				}
				if err := s.reserve(ctx, next, in.RunID, reservation.run); err != nil {
					return key, false, err
				}
				if err := s.requireFresh(ctx, next); err != nil {
					s.release(next)
					return key, false, err
				}
				s.release(reservation.key)
				reservation.key, reservation.held = next, true
			}
			return next, usable, nil
		},
	})

	if err != nil {
		return nil, err
	}
	if !reservation.held {
		// Missing public identity stays stateless. Its generated wire ID supplies
		// only a private recorder/admission key, never a provider checkpoint key.
		key := prepared.ThreadKey
		if key == "" {
			key = ThreadKey(prepared.Input.ThreadID)
		}
		if err := s.reserve(r.Context(), key, prepared.Input.RunID, reservation.run); err != nil {
			return nil, err
		}
		reservation.key, reservation.held = key, true
	}
	return prepared, nil
}

func (s *Session) recordBoundary(ctx context.Context, key string, prepared *agui.PreparedRequest) (sessionrecorder.EventRecord, error) {
	in := prepared.Input
	s.mu.Lock()
	delete(s.recordingFailures, key)
	s.mu.Unlock()
	boundary := adaptor.Notice{Kind: BoundaryKind, Data: map[string]any{
		"threadId": in.ThreadID, "runId": in.RunID,
		"parentRunId": in.ParentRunID, "protocolVersion": in.ProtocolVersion,
		"projectionVersion": ProjectionVersion, "diagnostics": prepared.Diagnostics,
	}}
	if user := in.UserTurnEvents(in.RunID); len(user) > 0 {
		boundary.Data["userMessageId"] = user[0].(adaptor.TextDelta).MessageID
		submitted := in.Messages[len(in.Messages)-1]
		submitted.ID = str(boundary.Data, "userMessageId")
		boundary.Data["userMessage"] = submitted
	}
	frozen, err := freezeEvent(boundary)
	var rec sessionrecorder.EventRecord
	if err == nil {
		rec, err = s.opts.Recorder.Record(ctx, RecorderKey(key), frozen)
	}
	if err != nil {
		return rec, &agui.InputError{Status: http.StatusInternalServerError,
			Code: "agui.recording_failed", Message: "run boundary recording failed"}
	}
	s.mu.Lock()
	s.boundaries[key] = rec.HostSeq
	s.changeLocked(key)
	s.mu.Unlock()
	return rec, nil
}

func (s *Session) streamResponse(w http.ResponseWriter, r *http.Request, key string, prepared *agui.PreparedRequest, boundary sessionrecorder.EventRecord) {
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	stream := prepared.Target.Stream(ctx, prepared.Prompt, s.opts.CallOptions...)
	defer stream.Cancel()
	output := make(chan events.Event, 32)
	go s.drain(ctx, key, prepared.Input, stream, boundary, output)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.Header().Set("Connection", "keep-alive")
	timeout := s.opts.WriteTimeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	deadline := ssewire.NewDeadlineWriter(w, timeout)
	defer deadline.ClearDeadline()
	writer := wire.NewSSEWriter()
	var ticks <-chan time.Time
	if s.opts.KeepAlivePing > 0 {
		ticker := time.NewTicker(s.opts.KeepAlivePing)
		defer ticker.Stop()
		ticks = ticker.C
	}
	// A failed write cancels the drain's send context before waiting for cleanup.
	defer func() {
		cancel()
		stream.Cancel()
		for range output {
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticks:
			if _, err := io.WriteString(deadline, ":keep-alive\n\n"); err != nil {
				return
			}
			deadline.Flush()
			if deadline.Err() != nil {
				return
			}
		case ev, ok := <-output:
			if !ok {
				return
			}
			if err := writer.WriteEvent(ctx, deadline, ev); err != nil {
				return
			}
			deadline.Flush()
			if deadline.Err() != nil {
				return
			}
		}
	}
}

func (s *Session) drain(ctx context.Context, key string, in *agui.RunAgentInput, stream adaptor.Stream, boundary sessionrecorder.EventRecord, out chan<- events.Event) {
	if out != nil {
		defer close(out)
	}
	if s.opts.OnRunEnd != nil {
		defer s.opts.OnRunEnd(key)
	}
	p := newProjector(s.opts.Events)
	send := func(es []events.Event) {
		if out == nil {
			return
		}
		for _, e := range es {
			select {
			case out <- e:
			case <-ctx.Done():
				stream.Cancel()
			}
		}
	}
	send(p.apply(boundary))
	failed := false
	record := func(ev adaptor.Event, submittedUser bool) {
		if failed {
			return
		}
		// Persist terminal state even after request cancellation, with bounded I/O.
		writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		// Freeze the recorder's typed JSON representation before giving the host
		// the live approval responder. In-memory and reopened JSONL projections
		// must see the same values, and Observe must not mutate stored history.
		frozen, err := freezeEvent(ev)
		var rec sessionrecorder.EventRecord
		if err == nil {
			rec, err = s.opts.Recorder.Record(writeCtx, RecorderKey(key), frozen)
		}
		if err != nil {
			failed = true
			s.mu.Lock()
			s.recordingFailures[key] = true
			s.changeLocked(key)
			s.mu.Unlock()
			stream.Cancel()
			send(p.fail("agui.recording_failed", "event recording failed"))
			return
		}
		es := p.apply(rec)
		if s.opts.Observe != nil {
			s.opts.Observe(key, ev)
		}
		// @ag-ui/client 1.0.3 default apply (Se): START reuses an existing ID;
		// CONTENT appends to its content. The POST client already owns this user
		// message, so echoing the recorded triple would double its text.
		if submittedUser {
			return
		}
		send(es)
	}
	for _, ev := range in.UserTurnEvents(in.RunID) {
		ev = adaptor.WithEventMeta(ev, adaptor.EventMeta{
			RunID: stream.RunID(), ThreadKey: key, Time: boundary.RecordedAt,
		})
		record(ev, true)
	}
	text := false
	var sourceErr error
	for ev := range stream.Events() {
		if n, ok := ev.(adaptor.Notice); ok && (n.Kind == BoundaryKind || n.Kind == TerminalKind) {
			sourceErr = errors.New("source event used a reserved AG-UI session Notice kind")
			stream.Cancel()
		}
		if sourceErr != nil {
			continue
		}
		if t, ok := ev.(adaptor.TextDelta); ok && t.Role != adaptor.RoleUser &&
			t.MessageID != "" && t.Phase != adaptor.PhaseStart && t.Phase != adaptor.PhaseEnd &&
			strings.TrimSpace(t.Text) != "" {
			text = true
		}
		record(ev, false)
	}
	result, runErr := stream.Result()
	if sourceErr != nil {
		runErr = &adaptor.RunError{Reason: "agui.reserved_notice", Message: sourceErr.Error()}
	}
	var business *adaptor.RunError
	if result == nil && errors.As(runErr, &business) {
		result = business.Result
	}
	if !text && result != nil && strings.TrimSpace(result.Text) != "" {
		id := in.RunID + ":result"
		for _, ev := range []adaptor.Event{
			adaptor.TextDelta{MessageID: id, Phase: adaptor.PhaseStart},
			adaptor.TextDelta{MessageID: id, Phase: adaptor.PhaseContent, Text: result.Text},
			adaptor.TextDelta{MessageID: id, Phase: adaptor.PhaseEnd},
		} {
			record(ev, false)
		}
	}
	code, message := "", ""
	if runErr != nil {
		code = "run.error"
		message = runErr.Error()
		if errors.Is(runErr, context.Canceled) {
			code = "run.cancelled"
		} else if errors.As(runErr, &business) {
			code = string(business.Reason)
			if code == "" {
				code = "run.error"
			}
			if business.Message != "" {
				message = business.Message
			}
		}
	}
	// This context belongs to the session/request, not the Driver. A host
	// cancel, shutdown or request disconnect remains cancellation even if the
	// killed provider reports an agent error. Retain its message for diagnosis.
	if cancellation := ctx.Err(); cancellation != nil {
		code = "run.cancelled"
		if message == "" {
			message = cancellation.Error()
		}
	}
	record(adaptor.WithEventMeta(
		adaptor.Notice{Kind: TerminalKind, Data: map[string]any{"code": code, "message": message}},
		adaptor.EventMeta{RunID: stream.RunID(), ThreadKey: key},
	), false)
}

func freezeEvent(ev adaptor.Event) (adaptor.Event, error) {
	raw, err := json.Marshal(sessionrecorder.EventRecord{Event: ev})
	if err != nil {
		return nil, err
	}
	var frozen sessionrecorder.EventRecord
	if err := json.Unmarshal(raw, &frozen); err != nil {
		return nil, err
	}
	return frozen.Event, nil
}

// ThreadKey returns the default collision-free public-thread mapping.
func ThreadKey(threadID string) string {
	return bridgekey.Encode("agui", threadID)
}

// Active reports whether this Session currently owns a run for key.
func (s *Session) Active(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.active[key]
}

// RecorderKey maps an opaque Thread key to a portable recorder storage key.
// This stable version-1 mapping is independent of ProjectionVersion. SHA-256
// keeps the key within the recorder backend filename contract.
func RecorderKey(key string) string { return fmt.Sprintf("agui-%x", sha256.Sum256([]byte(key))) }
