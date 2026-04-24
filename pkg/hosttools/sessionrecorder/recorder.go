package sessionrecorder

import (
	"context"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"sync"
	"time"

	agentadaptor "github.com/agent-dance/agent-adaptor"
)

// HostSeq is the host-scoped cursor the Recorder assigns to every
// payload. It is strictly monotonic within one session key and survives
// across runs that share that key. See package doc for why this cannot
// be StreamPayload.Seq.
type HostSeq = uint64

// Record is one persisted payload together with the HostSeq assigned to
// it. It is also the on-the-wire shape for backends that serialise
// records (for example the JSONL backend).
type Record struct {
	HostSeq    HostSeq                    `json:"host_seq"`
	RecordedAt time.Time                  `json:"recorded_at"`
	Payload    agentadaptor.StreamPayload `json:"payload"`
}

// SessionInfo summarises what a Recorder knows about one session key.
// It is the unit hosts render in a "recent sessions" UI.
type SessionInfo struct {
	Key        string    `json:"key"`
	LastSeq    HostSeq   `json:"last_seq"`
	RecordedAt time.Time `json:"recorded_at"`
}

// Recorder is the host-facing API. Implementations MUST be safe for
// concurrent use.
type Recorder interface {
	// Record appends a payload under sessionKey and returns the record
	// with the HostSeq it was assigned.
	//
	// HostSeq values MUST strictly increase within one sessionKey. The
	// assignment is durable: if Record returns without error, the
	// backend has accepted the write; if the backend rejects the write
	// the HostSeq is rolled back so a retry gets the same number.
	Record(ctx context.Context, sessionKey string, p agentadaptor.StreamPayload) (Record, error)

	// Since returns records whose HostSeq is strictly greater than
	// afterHostSeq, in ascending HostSeq order. afterHostSeq == 0
	// fetches the whole known history for sessionKey.
	Since(ctx context.Context, sessionKey string, afterHostSeq HostSeq) ([]Record, error)

	// Sessions enumerates session keys the Recorder knows about,
	// ordered by most-recent RecordedAt first.
	Sessions(ctx context.Context) ([]SessionInfo, error)

	// Close releases any backend resources. Safe to call more than
	// once; subsequent calls are no-ops.
	io.Closer
}

// Backend is the low-level storage interface behind a Recorder. It is
// intentionally narrow so third parties can plug Redis / Postgres / S3
// without reimplementing HostSeq bookkeeping or concurrency control.
//
// Implementations MUST be safe for concurrent use and MUST treat writes
// as append-only: replaying Load after Append must return the existing
// records plus the new one in HostSeq order.
type Backend interface {
	// Load returns all known records for sessionKey in HostSeq order.
	// Returns (nil, nil) when the session has no records.
	Load(ctx context.Context, sessionKey string) ([]Record, error)

	// Append persists exactly one record for sessionKey.
	Append(ctx context.Context, sessionKey string, r Record) error

	// Sessions enumerates keys the backend has any records for.
	// Order is implementation-defined; the Recorder re-sorts before
	// returning to callers.
	Sessions(ctx context.Context) ([]SessionInfo, error)

	io.Closer
}

// KeyValidator returns a non-nil error if sessionKey should be refused.
// Implementations MUST be deterministic and side-effect-free.
type KeyValidator func(sessionKey string) error

// DefaultKeyPattern matches session keys hosts can safely use as a
// filesystem component: an alphanumeric leader followed by up to 127
// alphanumerics, dashes, or underscores. It accepts the common
// shapes — UUIDs, opaque app-id slugs, "anon-<ts>" fallbacks —
// while rejecting path traversal (`../foo`), empty strings, and
// whitespace.
var DefaultKeyPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_\-]{0,127}$`)

// DefaultKeyValidator is the KeyValidator JSONLBackend uses by default.
// Swap it out with Option WithKeyValidator when hosts want a different
// policy (e.g. looser: accept slashes for multi-tenant prefixes;
// tighter: exact UUID shape).
var DefaultKeyValidator KeyValidator = func(key string) error {
	if !DefaultKeyPattern.MatchString(key) {
		return fmt.Errorf("sessionrecorder: refused session key %q: must match %s", key, DefaultKeyPattern)
	}
	return nil
}

// Option configures a Recorder constructed via New.
type Option func(*recorder)

// WithClock overrides the time source used to stamp RecordedAt.
// Intended for tests.
func WithClock(fn func() time.Time) Option {
	return func(r *recorder) {
		if fn != nil {
			r.clock = fn
		}
	}
}

// WithKeyValidator overrides the session-key validator applied before
// every backend call. Record/Since/Sessions refuse keys that fail
// validation. The default rejects anything that wouldn't be safe as a
// filesystem component.
func WithKeyValidator(v KeyValidator) Option {
	return func(r *recorder) {
		if v != nil {
			r.validate = v
		}
	}
}

// ErrInvalidSessionKey is returned by Record / Since / Sessions when the
// configured KeyValidator refuses the key. The original validator error
// is wrapped for inspection.
var ErrInvalidSessionKey = errors.New("sessionrecorder: invalid session key")

// New wraps a Backend into a Recorder. The returned Recorder assigns
// HostSeq in memory, so callers MUST route all access for a given
// sessionKey through the same process: multi-writer fan-in across
// processes against one sessionKey is out of scope. Multi-pod hosts
// should route stickily by sessionKey, or plug a coordinator-aware
// Backend (e.g. one using Redis INCR) below the Recorder.
//
// New panics only if backend is nil; option functions are tolerant of
// nil fields.
func New(backend Backend, opts ...Option) Recorder {
	if backend == nil {
		panic("sessionrecorder: New requires a non-nil Backend")
	}
	r := &recorder{
		backend:  backend,
		clock:    defaultClock,
		validate: DefaultKeyValidator,
		sessions: map[string]*sessionState{},
	}
	for _, opt := range opts {
		if opt != nil {
			opt(r)
		}
	}
	return r
}

type recorder struct {
	backend  Backend
	clock    func() time.Time
	validate KeyValidator

	mu       sync.Mutex
	sessions map[string]*sessionState

	closed bool
}

// sessionState caches per-session bookkeeping so hot writes don't need
// to reload from the backend every call. The `mu` here guards lastSeq /
// history / loaded. The enclosing recorder.mu only guards the sessions
// map itself.
type sessionState struct {
	mu        sync.Mutex
	loaded    bool
	lastSeq   HostSeq
	history   []Record
	updatedAt time.Time
}

func (r *recorder) Record(ctx context.Context, sessionKey string, p agentadaptor.StreamPayload) (Record, error) {
	if err := r.checkKey(sessionKey); err != nil {
		return Record{}, err
	}
	st, err := r.loadedSession(ctx, sessionKey)
	if err != nil {
		return Record{}, err
	}
	st.mu.Lock()
	defer st.mu.Unlock()

	next := st.lastSeq + 1
	rec := Record{
		HostSeq:    next,
		RecordedAt: r.clock(),
		Payload:    p,
	}
	if err := r.backend.Append(ctx, sessionKey, rec); err != nil {
		// Backend refused the write. Do NOT hand out this HostSeq: the
		// next Record call must retry the same number, otherwise a
		// later Load would observe a gap.
		return Record{}, err
	}
	st.lastSeq = next
	st.history = append(st.history, rec)
	st.updatedAt = rec.RecordedAt
	return rec, nil
}

func (r *recorder) Since(ctx context.Context, sessionKey string, afterHostSeq HostSeq) ([]Record, error) {
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
	out := make([]Record, len(st.history)-idx)
	copy(out, st.history[idx:])
	return out, nil
}

func (r *recorder) Sessions(ctx context.Context) ([]SessionInfo, error) {
	list, err := r.backend.Sessions(ctx)
	if err != nil {
		return nil, err
	}
	// Overlay in-memory state so RecordedAt/LastSeq reflect writes that
	// the backend may have persisted but not yet surfaced (for example
	// JSONL file mtime lagging behind an in-flight append).
	r.mu.Lock()
	overlay := make(map[string]*sessionState, len(r.sessions))
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

func (r *recorder) Close() error {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil
	}
	r.closed = true
	r.mu.Unlock()
	return r.backend.Close()
}

func (r *recorder) checkKey(sessionKey string) error {
	if r.validate == nil {
		return nil
	}
	if err := r.validate(sessionKey); err != nil {
		return fmt.Errorf("%w: %s", ErrInvalidSessionKey, err)
	}
	return nil
}

// loadedSession returns the per-session state with its backend history
// already populated. Load happens at most once per session key for the
// lifetime of the Recorder.
func (r *recorder) loadedSession(ctx context.Context, sessionKey string) (*sessionState, error) {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil, errors.New("sessionrecorder: recorder closed")
	}
	st, ok := r.sessions[sessionKey]
	if !ok {
		st = &sessionState{}
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

func defaultClock() time.Time { return time.Now().UTC() }
