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
const ProjectionVersion = "agui-session/1"

// BoundaryKind is the reserved Notice kind carrying run identity and diagnostics.
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
	// Resolve authorizes and maps starts and reads. Production hosts must install
	// it: a client thread ID is not authorization. Nil permits all access.
	Resolve func(*http.Request, Access) (string, error)
	// WriteTimeout bounds each SSE write; the default is 30 seconds.
	WriteTimeout time.Duration
	// KeepAlivePing sends periodic SSE comments when positive.
	KeepAlivePing time.Duration
	// ResolveDivergence may select a different key after history mismatches.
	// Returning the same key does not waive verification.
	ResolveDivergence func(context.Context, *agui.RunAgentInput, string) (string, error)
	// Input configures prompt preparation, including explicit history rehydration.
	Input agui.InputOptions
	// Events configures deterministic projection, including ProjectionHook.
	Events []agui.EventTranslatorOption
	// CallOptions are passed to Runner.Stream.
	CallOptions []adaptor.CallOption
	// Observe receives successfully recorded source events, including live approvals.
	// It runs synchronously in the sole drain loop and must return promptly.
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
	work              sync.WaitGroup
	runs              map[string]*ownedRun
	observers         map[string]map[uint64]context.CancelFunc
	observerID        uint64
	recordingFailures map[string]bool
	boundaries        map[string]sessionrecorder.HostSeq
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
	}
}

func conflict(code, message string) error {
	return &agui.InputError{Code: code, Message: message, Status: 409}
}

// VerifyHistory implements the sse.Options.VerifyHistory extension point.
// It compares IDs and roles, in order, excluding activities from both sides.
// @ag-ui/client 1.0.3 prepareRunAgentInput filters only activity messages;
// reasoning, tool results and tool-only assistants retain their IDs and roles.
// Content and error are intentionally not compared. The trailing user turn is excluded.
func (s *Session) VerifyHistory(ctx context.Context, in *agui.RunAgentInput, runner adaptor.Runner, key string) (string, bool, error) {
	messages, err := s.Messages(ctx, key)
	if err != nil {
		return key, false, err
	}
	prior := in.Messages
	if len(prior) > 0 {
		prior = prior[:len(prior)-1]
	}
	pairs := func(ms []agui.Message) []string {
		out := []string{}
		for _, m := range ms {
			if m.Role != "activity" {
				out = append(out, m.ID+"\x00"+m.Role)
			}
		}
		return out
	}
	recorded := []agui.Message{}
	for _, m := range messages {
		recorded = append(recorded, agui.Message{ID: str(m, "id"), Role: str(m, "role")})
	}
	a, b := pairs(prior), pairs(recorded)
	equal := len(a) == len(b)
	if equal {
		for i := range a {
			if a[i] != b[i] {
				equal = false
				break
			}
		}
	}
	if !equal {
		if s.opts.ResolveDivergence != nil {
			newKey, e := s.opts.ResolveDivergence(ctx, in, key)
			if e != nil {
				return key, false, e
			}
			if newKey != key {
				return newKey, false, nil
			}
		}
		return key, false, conflict("agui.history_diverged", "client message IDs/roles differ from recorded history")
	}
	usable, err := agui.CheckpointUsable(ctx, runner)
	return key, usable, err
}

// Handler returns the POST RunAgentInput endpoint. Disconnect cancels execution
// unless Unattended is enabled; then it detaches only this observer.
func (s *Session) Handler() http.Handler { return http.HandlerFunc(s.serveRun) }

func writeError(w http.ResponseWriter, err error) {
	status := 400
	var in *agui.InputError
	if errors.As(err, &in) {
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
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": in})
}

func (s *Session) resolve(r *http.Request, access Access) (string, error) {
	if s.opts.Resolve != nil {
		return s.opts.Resolve(r, access)
	}
	return ThreadKey(access.ThreadID), nil
}

func (s *Session) release(key string) {
	s.mu.Lock()
	delete(s.active, key)
	s.mu.Unlock()
}

func (s *Session) reserve(ctx context.Context, key, runID string) error {
	s.mu.Lock()
	if s.active[key] {
		s.mu.Unlock()
		return conflict("agui.run_active", "thread already has an active run")
	}
	s.active[key] = true
	s.boundaries[key] = 0
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
	defer func() {
		if !handedOff {
			s.finishAdmission()
		}
	}()
	reserved := ""
	defer func() {
		if reserved != "" {
			s.release(reserved)
		}
	}()
	prepared, err := agui.PrepareHTTPRequest(r, s.runner, agui.RequestOptions{
		Input: s.opts.Input,
		ResolveThreadKey: func(r *http.Request, in *agui.RunAgentInput) (string, error) {
			key, err := s.resolve(r, Access{Op: OpStart, ThreadID: in.ThreadID, Input: in})
			if err != nil {
				return "", err
			}
			if in.ThreadID == "" {
				return "", nil
			}
			if err := s.reserve(r.Context(), key, in.RunID); err != nil {
				return "", err
			}
			reserved = key
			return key, nil
		},
		VerifyHistory: func(ctx context.Context, in *agui.RunAgentInput, runner adaptor.Runner, key string) (string, bool, error) {
			next, usable, err := s.VerifyHistory(ctx, in, runner, key)
			if err != nil {
				return key, false, err
			}
			if next != key {
				if err := s.reserve(ctx, next, in.RunID); err != nil {
					return key, false, err
				}
				s.release(reserved)
				reserved = next
			}
			return next, usable, nil
		},
	})
	if err != nil {
		writeError(w, err)
		return
	}
	key := prepared.ThreadKey
	if key == "" {
		key = ThreadKey(prepared.Input.ThreadID)
		if err := s.reserve(r.Context(), key, prepared.Input.RunID); err != nil {
			writeError(w, err)
			return
		}
		reserved = key
	}
	boundary, err := s.recordBoundary(r.Context(), key, prepared)
	if err != nil {
		writeError(w, err)
		return
	}
	if s.opts.Unattended {
		handedOff = true
		reserved = ""
		s.unattendedResponse(w, r, key, prepared, boundary)
		return
	}
	s.streamResponse(w, r, key, prepared, boundary)
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
	rec, err := s.opts.Recorder.Record(ctx, RecorderKey(key), boundary)
	if err == nil {
		s.mu.Lock()
		s.boundaries[key] = rec.HostSeq
		s.mu.Unlock()
	}
	return rec, err
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
		case ev, ok := <-output:
			if !ok {
				return
			}
			if err := writer.WriteEvent(ctx, deadline, ev); err != nil {
				return
			}
			deadline.Flush()
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
	record := func(ev adaptor.Event) {
		if failed {
			return
		}
		// Persist terminal state even after request cancellation, with bounded I/O.
		writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		rec, err := s.opts.Recorder.Record(writeCtx, RecorderKey(key), ev)
		if err != nil {
			failed = true
			s.mu.Lock()
			s.recordingFailures[key] = true
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
		if t, ok := ev.(adaptor.TextDelta); ok && t.Role == adaptor.RoleUser {
			return
		}
		send(es)
	}
	for _, ev := range in.UserTurnEvents(in.RunID) {
		record(ev)
	}
	text := false
	for ev := range stream.Events() {
		if t, ok := ev.(adaptor.TextDelta); ok && t.Role != adaptor.RoleUser && strings.TrimSpace(t.Text) != "" {
			text = true
		}
		record(ev)
	}
	result, runErr := stream.Result()
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
			record(ev)
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
			message = business.Message
		}
	}
	record(adaptor.Notice{Kind: TerminalKind, Data: map[string]any{"code": code, "message": message}})
}

// ThreadKey returns the default collision-free public-thread mapping.
func ThreadKey(threadID string) string { return bridgekey.Encode("agui", threadID) }

// Active reports whether this Session currently owns a run for key.
func (s *Session) Active(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.active[key]
}

// RecorderKey maps an opaque Thread key to a portable recorder storage key.
// SHA-256 keeps the key within the recorder backend filename contract.
func RecorderKey(key string) string { return fmt.Sprintf("agui-%x", sha256.Sum256([]byte(key))) }
