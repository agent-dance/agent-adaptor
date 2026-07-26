package sessionrecorder

// This file is the v1-API twin of recorder.go: the same HostSeq bookkeeping
// and session semantics, recording the new unified event family
// (adaptor.Event, decision D3) instead of the legacy StreamPayload. The
// legacy Recorder stays untouched until P5 deletes it; both entries coexist
// additively.
//
// Because adaptor.Event is a sealed interface, EventRecord carries its own
// stable JSON envelope ({host_seq, recorded_at, kind, event}) so durable
// backends can serialize records without knowing the event vocabulary.
// *adaptor.ApprovalRequest records serialize their descriptive fields only:
// a replayed approval request carries no live responder (replay is
// observational, not interactive).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"sync"
	"time"

	adaptor "github.com/agent-dance/agent-adaptor/next"
)

// EventRecord is one persisted unified event together with the HostSeq
// assigned to it. It marshals to a stable JSON envelope (kind + event
// payload) and unmarshals back to the typed event.
type EventRecord struct {
	HostSeq    HostSeq
	RecordedAt time.Time
	Event      adaptor.Event
}

type eventRecordWire struct {
	HostSeq    HostSeq         `json:"host_seq"`
	RecordedAt time.Time       `json:"recorded_at"`
	Kind       string          `json:"kind"`
	Event      json.RawMessage `json:"event"`
}

// MarshalJSON encodes the record as {host_seq, recorded_at, kind, event}.
func (r EventRecord) MarshalJSON() ([]byte, error) {
	kind, payload, err := encodeEventV1(r.Event)
	if err != nil {
		return nil, err
	}
	return json.Marshal(eventRecordWire{
		HostSeq:    r.HostSeq,
		RecordedAt: r.RecordedAt,
		Kind:       kind,
		Event:      payload,
	})
}

// UnmarshalJSON restores the typed event from the envelope.
func (r *EventRecord) UnmarshalJSON(data []byte) error {
	var wire eventRecordWire
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	ev, err := decodeEventV1(wire.Kind, wire.Event)
	if err != nil {
		return err
	}
	r.HostSeq = wire.HostSeq
	r.RecordedAt = wire.RecordedAt
	r.Event = ev
	return nil
}

// EventRecorder is the host-facing v1 recording API — Recorder's contract
// verbatim, typed on the unified event family. Implementations MUST be
// safe for concurrent use.
type EventRecorder interface {
	// Record appends an event under sessionKey and returns the record
	// with the HostSeq it was assigned. HostSeq values strictly increase
	// within one sessionKey; a rejected backend write rolls the number
	// back so a retry gets the same one.
	Record(ctx context.Context, sessionKey string, ev adaptor.Event) (EventRecord, error)

	// Since returns records whose HostSeq is strictly greater than
	// afterHostSeq, in ascending order. afterHostSeq == 0 fetches the
	// whole known history.
	Since(ctx context.Context, sessionKey string, afterHostSeq HostSeq) ([]EventRecord, error)

	// Sessions enumerates known session keys, most recent first.
	Sessions(ctx context.Context) ([]SessionInfo, error)

	io.Closer
}

// EventBackend is the low-level storage interface behind an EventRecorder
// — Backend's contract verbatim, typed on EventRecord.
type EventBackend interface {
	// Load returns all known records for sessionKey in HostSeq order.
	Load(ctx context.Context, sessionKey string) ([]EventRecord, error)

	// Append persists exactly one record for sessionKey.
	Append(ctx context.Context, sessionKey string, r EventRecord) error

	// Sessions enumerates keys the backend has any records for.
	Sessions(ctx context.Context) ([]SessionInfo, error)

	io.Closer
}

// EventOption configures an EventRecorder constructed via NewEventRecorder.
type EventOption func(*eventRecorder)

// WithEventClock overrides the time source used to stamp RecordedAt.
func WithEventClock(fn func() time.Time) EventOption {
	return func(r *eventRecorder) {
		if fn != nil {
			r.clock = fn
		}
	}
}

// WithEventKeyValidator overrides the session-key validator. The default
// is DefaultKeyValidator, shared with the legacy Recorder.
func WithEventKeyValidator(v KeyValidator) EventOption {
	return func(r *eventRecorder) {
		if v != nil {
			r.validate = v
		}
	}
}

// NewEventRecorder wraps an EventBackend into an EventRecorder. Same
// single-process HostSeq contract as New: route all access for a given
// sessionKey through one process, or plug a coordinator-aware backend.
//
// Panics only if backend is nil.
func NewEventRecorder(backend EventBackend, opts ...EventOption) EventRecorder {
	if backend == nil {
		panic("sessionrecorder: NewEventRecorder requires a non-nil EventBackend")
	}
	r := &eventRecorder{
		backend:  backend,
		clock:    defaultClock,
		validate: DefaultKeyValidator,
		sessions: map[string]*eventSessionState{},
	}
	for _, opt := range opts {
		if opt != nil {
			opt(r)
		}
	}
	return r
}

type eventRecorder struct {
	backend  EventBackend
	clock    func() time.Time
	validate KeyValidator

	mu       sync.Mutex
	sessions map[string]*eventSessionState

	closed bool
}

type eventSessionState struct {
	mu        sync.Mutex
	loaded    bool
	lastSeq   HostSeq
	history   []EventRecord
	updatedAt time.Time
}

func (r *eventRecorder) Record(ctx context.Context, sessionKey string, ev adaptor.Event) (EventRecord, error) {
	if err := r.checkKey(sessionKey); err != nil {
		return EventRecord{}, err
	}
	st, err := r.loadedSession(ctx, sessionKey)
	if err != nil {
		return EventRecord{}, err
	}
	st.mu.Lock()
	defer st.mu.Unlock()

	next := st.lastSeq + 1
	rec := EventRecord{
		HostSeq:    next,
		RecordedAt: r.clock(),
		Event:      ev,
	}
	if err := r.backend.Append(ctx, sessionKey, rec); err != nil {
		// Roll back: the next Record call must retry the same HostSeq.
		return EventRecord{}, err
	}
	st.lastSeq = next
	st.history = append(st.history, rec)
	st.updatedAt = rec.RecordedAt
	return rec, nil
}

func (r *eventRecorder) Since(ctx context.Context, sessionKey string, afterHostSeq HostSeq) ([]EventRecord, error) {
	if err := r.checkKey(sessionKey); err != nil {
		return nil, err
	}
	st, err := r.loadedSession(ctx, sessionKey)
	if err != nil {
		return nil, err
	}
	st.mu.Lock()
	defer st.mu.Unlock()

	if len(st.history) == 0 || afterHostSeq >= st.lastSeq {
		return nil, nil
	}
	idx := sort.Search(len(st.history), func(i int) bool {
		return st.history[i].HostSeq > afterHostSeq
	})
	if idx >= len(st.history) {
		return nil, nil
	}
	out := make([]EventRecord, len(st.history)-idx)
	copy(out, st.history[idx:])
	return out, nil
}

func (r *eventRecorder) Sessions(ctx context.Context) ([]SessionInfo, error) {
	list, err := r.backend.Sessions(ctx)
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	overlay := make(map[string]*eventSessionState, len(r.sessions))
	for k, st := range r.sessions {
		overlay[k] = st
	}
	r.mu.Unlock()

	for i := range list {
		if st, ok := overlay[list[i].Key]; ok {
			st.mu.Lock()
			if st.lastSeq > list[i].LastSeq {
				list[i].LastSeq = st.lastSeq
			}
			if st.updatedAt.After(list[i].RecordedAt) {
				list[i].RecordedAt = st.updatedAt
			}
			st.mu.Unlock()
		}
	}
	sort.SliceStable(list, func(i, j int) bool {
		return list[i].RecordedAt.After(list[j].RecordedAt)
	})
	return list, nil
}

func (r *eventRecorder) Close() error {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil
	}
	r.closed = true
	r.mu.Unlock()
	return r.backend.Close()
}

func (r *eventRecorder) checkKey(sessionKey string) error {
	if r.validate == nil {
		return nil
	}
	if err := r.validate(sessionKey); err != nil {
		return fmt.Errorf("%w: %s", ErrInvalidSessionKey, err)
	}
	return nil
}

func (r *eventRecorder) loadedSession(ctx context.Context, sessionKey string) (*eventSessionState, error) {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil, errors.New("sessionrecorder: recorder closed")
	}
	st, ok := r.sessions[sessionKey]
	if !ok {
		st = &eventSessionState{}
		r.sessions[sessionKey] = st
	}
	r.mu.Unlock()

	st.mu.Lock()
	defer st.mu.Unlock()
	if st.loaded {
		return st, nil
	}
	records, err := r.backend.Load(ctx, sessionKey)
	if err != nil {
		return nil, err
	}
	st.history = records
	if n := len(records); n > 0 {
		st.lastSeq = records[n-1].HostSeq
		st.updatedAt = records[n-1].RecordedAt
	}
	st.loaded = true
	return st, nil
}

// NewMemoryEventBackend returns an EventBackend that keeps records in
// process memory — the v1 twin of NewMemoryBackend.
func NewMemoryEventBackend() EventBackend {
	return &memoryEventBackend{sessions: map[string][]EventRecord{}}
}

type memoryEventBackend struct {
	mu       sync.Mutex
	sessions map[string][]EventRecord
}

func (b *memoryEventBackend) Load(_ context.Context, key string) ([]EventRecord, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	src := b.sessions[key]
	if len(src) == 0 {
		return nil, nil
	}
	out := make([]EventRecord, len(src))
	copy(out, src)
	return out, nil
}

func (b *memoryEventBackend) Append(_ context.Context, key string, r EventRecord) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.sessions[key] = append(b.sessions[key], r)
	return nil
}

func (b *memoryEventBackend) Sessions(_ context.Context) ([]SessionInfo, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]SessionInfo, 0, len(b.sessions))
	for k, recs := range b.sessions {
		if len(recs) == 0 {
			continue
		}
		last := recs[len(recs)-1]
		out = append(out, SessionInfo{
			Key:        k,
			LastSeq:    last.HostSeq,
			RecordedAt: last.RecordedAt,
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].RecordedAt.After(out[j].RecordedAt)
	})
	return out, nil
}

func (b *memoryEventBackend) Close() error { return nil }

// ---------------------------------------------------------------------------
// Event envelope registry
// ---------------------------------------------------------------------------

// Wire kind tags for the 11 unified event types. Stable: backends persist
// these strings.
const (
	eventKindTextDelta       = "text.delta"
	eventKindThinking        = "thinking"
	eventKindToolCall        = "tool.call"
	eventKindToolResult      = "tool.result"
	eventKindRunStarted      = "run.started"
	eventKindRunFinished     = "run.finished"
	eventKindProcessInfo     = "process.info"
	eventKindNotice          = "notice"
	eventKindDropped         = "dropped"
	eventKindSubagentUpdate  = "subagent.update"
	eventKindApprovalRequest = "approval.request"
)

func encodeEventV1(ev adaptor.Event) (string, json.RawMessage, error) {
	var kind string
	switch ev.(type) {
	case adaptor.TextDelta:
		kind = eventKindTextDelta
	case adaptor.Thinking:
		kind = eventKindThinking
	case adaptor.ToolCall:
		kind = eventKindToolCall
	case adaptor.ToolResult:
		kind = eventKindToolResult
	case adaptor.RunStarted:
		kind = eventKindRunStarted
	case adaptor.RunFinished:
		kind = eventKindRunFinished
	case adaptor.ProcessInfo:
		kind = eventKindProcessInfo
	case adaptor.Notice:
		kind = eventKindNotice
	case adaptor.Dropped:
		kind = eventKindDropped
	case adaptor.SubagentUpdate:
		kind = eventKindSubagentUpdate
	case *adaptor.ApprovalRequest:
		kind = eventKindApprovalRequest
	case nil:
		return "", nil, errors.New("sessionrecorder: cannot record a nil event")
	default:
		return "", nil, fmt.Errorf("sessionrecorder: unknown event type %T", ev)
	}
	payload, err := json.Marshal(ev)
	if err != nil {
		return "", nil, fmt.Errorf("sessionrecorder: encode %s event: %w", kind, err)
	}
	return kind, payload, nil
}

func decodeEventV1(kind string, payload json.RawMessage) (adaptor.Event, error) {
	unmarshal := func(v any) error {
		if len(payload) == 0 {
			return fmt.Errorf("sessionrecorder: %s record has no event payload", kind)
		}
		return json.Unmarshal(payload, v)
	}
	switch kind {
	case eventKindTextDelta:
		var ev adaptor.TextDelta
		return ev, unmarshal(&ev)
	case eventKindThinking:
		var ev adaptor.Thinking
		return ev, unmarshal(&ev)
	case eventKindToolCall:
		var ev adaptor.ToolCall
		return ev, unmarshal(&ev)
	case eventKindToolResult:
		var ev adaptor.ToolResult
		return ev, unmarshal(&ev)
	case eventKindRunStarted:
		var ev adaptor.RunStarted
		return ev, unmarshal(&ev)
	case eventKindRunFinished:
		var ev adaptor.RunFinished
		return ev, unmarshal(&ev)
	case eventKindProcessInfo:
		var ev adaptor.ProcessInfo
		return ev, unmarshal(&ev)
	case eventKindNotice:
		var ev adaptor.Notice
		return ev, unmarshal(&ev)
	case eventKindDropped:
		var ev adaptor.Dropped
		return ev, unmarshal(&ev)
	case eventKindSubagentUpdate:
		var ev adaptor.SubagentUpdate
		return ev, unmarshal(&ev)
	case eventKindApprovalRequest:
		// Descriptive fields only: a replayed request has no live
		// responder (Approve/Deny/Answer are meaningless after the fact).
		ev := &adaptor.ApprovalRequest{}
		return ev, unmarshal(ev)
	default:
		return nil, fmt.Errorf("sessionrecorder: unknown event kind %q", kind)
	}
}
