package adaptor

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/engine"
	"github.com/agent-dance/agent-adaptor/threadstore"
)

// Thread is a stateful conversation handle: the same Runner contract as
// Agent, plus continuity — every run resumes the driver checkpoint stored
// under the thread key and persists the new checkpoint afterwards.
//
// The thread key is the host-owned, opaque conversation identity. The
// internal session ID and the Driver's native resume handle remain storage
// details, reachable only through Checkpoint for audit.
//
//	agent.Thread("tenant-1/issue-123")            // continue_or_start
//	agent.NewThread("tenant-1/issue-123")         // start_new (first run)
//	agent.Thread("k", adaptor.ResumeOnly())       // continue_only
//	th.Fork("tenant-1/issue-123/alt")             // fork (first run)
//
// A Thread handle is cheap: it holds no open resources, and any number of
// handles for the same key are interchangeable (state lives in the
// threadstore.Store). Concurrent runs on the same key are serialized by the
// store lease — the loser fails fast with ErrThreadBusy.
// The configured Driver must declare Sessions.SupportsResume and implement
// both driver.SessionCodecProvider and driver.SessionConfigFingerprinter;
// Run/Stream reject an incomplete contract before acquiring resources or
// touching the store.
type Thread struct {
	agent      *Agent
	key        string
	resumeOnly bool
	forkFrom   string // parent thread key, set by Fork

	mu sync.Mutex
	// pending is the one-shot first-run mode (start_new for NewThread,
	// fork for Fork). It is cleared after the first successful persist;
	// from then on the thread continues like a plain Thread(key).
	pending driver.SessionMode
}

var _ Runner = (*Thread)(nil)

// Checkpoint is the driver resume handle exposed for audit purposes. It
// aliases the driver SPI checkpoint so hosts can inspect it without
// importing the SPI package.
type Checkpoint = driver.Checkpoint

// ThreadOption tweaks how a Thread binds to its key. ResumeOnly is the sole
// Thread option.
type ThreadOption interface {
	applyThread(*Thread)
}

type threadOptionFunc func(*Thread)

func (f threadOptionFunc) applyThread(t *Thread) { f(t) }

// ResumeOnly makes the Thread resume-only: runs fail with ErrThreadNotFound
// when no conversation exists under the key (instead of silently starting a
// fresh one) and with ErrThreadIncompatible when the stored conversation no
// longer matches the current configuration. Use it when starting over would
// be a bug — e.g. replying inside an existing support ticket.
func ResumeOnly() ThreadOption {
	return threadOptionFunc(func(t *Thread) { t.resumeOnly = true })
}

// Thread returns the conversation handle for key, continuing the stored
// conversation when one exists and starting fresh otherwise
// (continue-or-start). key is the host's own opaque business string and is
// stored and compared verbatim.
//
// Runs require a store injected via WithThreadStore; without one they fail
// with ErrThreadStoreRequired. Thread panics on an empty key (programmer
// error, symmetric with New's nil-driver panic).
func (a *Agent) Thread(key string, opts ...ThreadOption) *Thread {
	if key == "" {
		panic("adaptor.Agent.Thread: key must not be empty")
	}
	t := &Thread{agent: a, key: key}
	for _, o := range opts {
		if o == nil {
			continue
		}
		o.applyThread(t)
	}
	return t
}

// NewThread returns a handle that force-starts a fresh conversation under
// key: the first run starts new even when a conversation already exists —
// the old one is archived (still resolvable for audit) and the key rebinds
// to the new conversation. After the first successful run the handle
// behaves exactly like Thread(key).
//
// NewThread panics on an empty key.
func (a *Agent) NewThread(key string) *Thread {
	if key == "" {
		panic("adaptor.Agent.NewThread: key must not be empty")
	}
	return &Thread{agent: a, key: key, pending: driver.SessionStartNew}
}

// Fork branches the conversation to newKey — the "regenerate from here /
// try another direction" button. The first run on the returned Thread forks
// from t's current conversation (the parent stays intact and active under
// its own key); afterwards the fork continues independently under newKey.
//
// The parent conversation is resolved when the fork's first run executes;
// if the parent thread has no stored conversation by then, the run fails
// with ErrThreadNotFound. Fork panics on an empty newKey.
func (t *Thread) Fork(newKey string) *Thread {
	if newKey == "" {
		panic("adaptor.Thread.Fork: key must not be empty")
	}
	return &Thread{
		agent:    t.agent,
		key:      newKey,
		forkFrom: t.key,
		pending:  driver.SessionFork,
	}
}

// Key returns the thread key the handle is bound to.
func (t *Thread) Key() string { return t.key }

// Run executes one prompt on the thread to completion: resume the stored
// conversation per the thread's mode, run the driver, persist the new
// checkpoint. It has the same drain-the-stream and error contracts as
// Agent.Run, plus the thread error vocabulary (ErrThreadBusy,
// ErrThreadNotFound, ErrThreadIncompatible, ...).
func (t *Thread) Run(ctx context.Context, prompt string, opts ...CallOption) (*Result, error) {
	st := t.Stream(ctx, prompt, opts...)
	for range st.Events() {
		// Drain, exactly like Agent.Run: the unified pipeline is the only
		// execution path; Run just has no event consumer.
	}
	return st.Result()
}

// Stream starts one prompt on the thread and returns the live Stream
// immediately — the same consumption contract as Agent.Stream carrying the
// session context per turn. Startup and session-coordination failures
// surface through the normal contract (closed Events channel + Result()
// error), never as a second return value.
func (t *Thread) Stream(ctx context.Context, prompt string, opts ...CallOption) Stream {
	mode, forkFromKey := t.planMode()
	return t.agent.startInvocation(ctx, prompt, opts, &invocationTarget{
		thread:      t,
		mode:        mode,
		forkFromKey: forkFromKey,
	})
}

// Checkpoint returns the driver resume handle currently stored under the
// thread key, normalized through the driver's session codec (audit /
// debugging use — the SDK resumes threads by itself). It fails with
// ErrThreadNotFound when the key has no active conversation and with
// ErrThreadStoreRequired when the agent has no store. Corrupt or unusable
// durable state returns ErrThreadCheckpointMissing or ErrThreadIncompatible;
// Checkpoint never returns an invalid checkpoint with a nil error.
func (t *Thread) Checkpoint(ctx context.Context) (*Checkpoint, error) {
	store := t.agent.defaults.threadStore
	if store == nil {
		return nil, fmt.Errorf("%w (thread %q)", ErrThreadStoreRequired, t.key)
	}
	contract, err := validateThreadDriverContract(t.agent.driver)
	if err != nil {
		return nil, t.threadError(err)
	}
	rec, err := store.Resolve(ctx, threadstore.Query{Key: t.key})
	if err != nil {
		return nil, fmt.Errorf("adaptor: thread %q: checkpoint: %w", t.key, err)
	}
	if rec == nil {
		return nil, fmt.Errorf("%w (thread %q)", ErrThreadNotFound, t.key)
	}
	if rec.SessionCodec == "" || rec.SessionCodec != contract.codecName {
		return nil, t.threadError(&engine.SessionIncompatibleError{Reason: "stored session codec does not match the configured driver"})
	}
	state, err := engine.NormalizeResumableSessionState(t.agent.driver, rec.State)
	if err != nil {
		return nil, t.threadError(err)
	}
	return &Checkpoint{State: state, Valid: true}, nil
}

// planMode picks the session mode for the next run.
func (t *Thread) planMode() (driver.SessionMode, string) {
	if t.resumeOnly {
		return driver.SessionContinueOnly, ""
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.pending != "" {
		return t.pending, t.forkFrom
	}
	return driver.SessionContinueOrStart, ""
}

// markEstablished clears the one-shot first-run mode after a successful
// persist: from now on the handle continues its own conversation.
func (t *Thread) markEstablished() {
	t.mu.Lock()
	t.pending = ""
	t.mu.Unlock()
}

// Session-coordination failures are infrastructure errors: plain wrapped
// errors without a Result, classified by sentinel so hosts can branch
// with errors.Is.
var (
	// ErrThreadStoreRequired: the agent has no threadstore.Store
	// (WithThreadStore) but a Thread run or Checkpoint was requested.
	ErrThreadStoreRequired = errors.New("adaptor: thread store required (use WithThreadStore)")
	// ErrThreadNotFound: a resume-only thread (or a fork parent) has no
	// stored conversation under its key.
	ErrThreadNotFound = errors.New("adaptor: thread not found")
	// ErrThreadBusy: another run holds the thread's exclusivity lease
	// right now; retry after it finishes.
	ErrThreadBusy = errors.New("adaptor: thread busy")
	// ErrThreadIncompatible: the stored conversation no longer matches the
	// current configured Driver, identity, model, resolved workspace,
	// profile/skill/MCP/instructions, or runtime-service environment, and
	// the thread is resume-only, so silently starting over is not allowed.
	ErrThreadIncompatible = errors.New("adaptor: thread incompatible with current configuration")
	// ErrThreadLeaseLost: the run lost its exclusivity lease mid-flight
	// (store outage or takeover); its state was not persisted.
	ErrThreadLeaseLost = errors.New("adaptor: thread lease lost")
	// ErrThreadCheckpointMissing: the driver finished without producing a
	// resumable checkpoint, so the thread state could not be persisted.
	ErrThreadCheckpointMissing = errors.New("adaptor: driver returned no resumable checkpoint")
	// ErrThreadAlreadyExists: Fork's target key already has an active
	// conversation. The parent and existing target remain unchanged.
	ErrThreadAlreadyExists = errors.New("adaptor: thread already exists")
	// ErrResumeRejected: the driver refused to resume from the stored
	// checkpoint and the thread mode does not allow a fresh start.
	ErrResumeRejected = errors.New("adaptor: driver rejected thread resume")
)

// threadError translates engine/store session errors into the consumer
// vocabulary, keeping the original chain wrapped for diagnostics.
func (t *Thread) threadError(err error) error {
	primary := err
	// releaseAfter joins the preparation failure first and cleanup failures
	// after it. Classify that first error so a cleanup error that happens to
	// match another Thread category cannot replace the primary public outcome.
	for {
		joined, ok := primary.(interface{ Unwrap() []error })
		if !ok {
			break
		}
		children := joined.Unwrap()
		if len(children) == 0 || children[0] == nil {
			break
		}
		primary = children[0]
	}
	var incompatible *engine.SessionIncompatibleError
	switch {
	case errors.Is(primary, engine.ErrSessionStoreRequired):
		return errors.Join(fmt.Errorf("%w (thread %q)", ErrThreadStoreRequired, t.key), err)
	case errors.Is(primary, engine.ErrSessionNotFound):
		return errors.Join(fmt.Errorf("%w (thread %q)", ErrThreadNotFound, t.key), err)
	case errors.Is(primary, engine.ErrSessionBusy), errors.Is(primary, threadstore.ErrBusy):
		return errors.Join(fmt.Errorf("%w (thread %q)", ErrThreadBusy, t.key), err)
	case errors.As(primary, &incompatible):
		reason := incompatible.Reason
		if reason == "" {
			reason = "fingerprint mismatch"
		}
		return errors.Join(fmt.Errorf("%w (thread %q): %s", ErrThreadIncompatible, t.key, reason), err)
	case errors.Is(primary, engine.ErrSessionIncompatible):
		return errors.Join(fmt.Errorf("%w (thread %q)", ErrThreadIncompatible, t.key), err)
	case errors.Is(primary, engine.ErrSessionLeaseLost), errors.Is(primary, threadstore.ErrLeaseLost):
		return errors.Join(fmt.Errorf("%w (thread %q)", ErrThreadLeaseLost, t.key), err)
	case errors.Is(primary, engine.ErrSessionCheckpointMissing):
		return errors.Join(fmt.Errorf("%w (thread %q)", ErrThreadCheckpointMissing, t.key), err)
	case errors.Is(primary, engine.ErrThreadAlreadyExists), errors.Is(primary, threadstore.ErrAlreadyExists):
		return errors.Join(fmt.Errorf("%w (thread %q)", ErrThreadAlreadyExists, t.key), err)
	default:
		return fmt.Errorf("adaptor: thread %q: %w", t.key, err)
	}
}
