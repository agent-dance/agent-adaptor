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
// under the thread key and persists the new checkpoint afterwards
// (docs/api-v1-redesign.md §2.4).
//
// v1 keeps exactly two consumer-visible identity layers: the thread key
// (host-owned business string) and the run ID. The internal session ID and
// the driver's native resume handle stay demoted to storage details,
// reachable only through Checkpoint for audit.
//
// Mode mapping (legacy session modes → Thread methods):
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

// ThreadOption tweaks how a Thread binds to its key. The only P2 option is
// ResumeOnly.
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
// (continue-or-start). key is the host's own business string — compose
// tenant scoping into it ("tenant-1/issue-123") as needed.
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
// checkpoint. Same drain-the-stream semantics and D1 error contract as
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
	st, eff, runCtx, ok := t.agent.openStream(ctx, opts)
	if !ok {
		return st
	}

	go func() {
		defer st.cancel()
		res, err := t.execute(runCtx, st.runID, prompt, &eff, st.sink)
		// Same close-timing contract as Agent.Stream: outcome first, then
		// the event channel, done last.
		st.res, st.err = res, err
		st.sink.close()
		close(st.done)
	}()
	return st
}

// Checkpoint returns the driver resume handle currently stored under the
// thread key, normalized through the driver's session codec (audit /
// debugging use — the SDK resumes threads by itself). It fails with
// ErrThreadNotFound when the key has no active conversation and with
// ErrThreadStoreRequired when the agent has no store.
func (t *Thread) Checkpoint(ctx context.Context) (*Checkpoint, error) {
	store := t.agent.defaults.threadStore
	if store == nil {
		return nil, fmt.Errorf("%w (thread %q)", ErrThreadStoreRequired, t.key)
	}
	rec, err := store.Resolve(ctx, threadstore.Query{Key: t.key})
	if err != nil {
		return nil, fmt.Errorf("adaptor: thread %q: checkpoint: %w", t.key, err)
	}
	if rec == nil || rec.State == nil {
		return nil, fmt.Errorf("%w (thread %q)", ErrThreadNotFound, t.key)
	}
	state := engine.NormalizeSessionState(t.agent.driver, rec.State)
	return &Checkpoint{State: state, Valid: state != nil}, nil
}

// execute is the session-coordinated run pipeline: prepare the session plan
// (leases + mode logic + fingerprint gating), run the driver with the
// resume context, and persist the new checkpoint. It reuses the engine's
// session logic verbatim through the ThreadSessionPlan entry, so the
// semantics match the legacy session path item by item.
func (t *Thread) execute(ctx context.Context, runID, prompt string, eff *RunSettings, sink *eventSink) (*Result, error) {
	store := t.agent.defaults.threadStore
	if store == nil {
		return nil, fmt.Errorf("%w (thread %q)", ErrThreadStoreRequired, t.key)
	}
	es := engineStore{store: store}
	drv := t.agent.driver
	driverType := drv.Descriptor().Type

	mode, forkParent := t.planMode()
	req := engine.SessionRequest{Namespace: threadNamespace, Key: t.key, Mode: mode}
	if mode == driver.SessionFork {
		parent, err := es.Resolve(ctx, engine.SessionQuery{Namespace: threadNamespace, Key: forkParent})
		if err != nil {
			return nil, fmt.Errorf("adaptor: thread %q: resolve fork parent: %w", t.key, err)
		}
		if parent == nil {
			return nil, fmt.Errorf("%w (fork parent %q)", ErrThreadNotFound, forkParent)
		}
		req.ForkFrom = parent.ID
	}

	var identity driver.AgentIdentity
	if eff.identity != nil {
		identity = eff.identity.driverIdentity()
	}

	// Resolution (skills, MCP, profile payload, structured output
	// negotiation) precedes session planning, exactly like the legacy
	// Execute order, because the profile payload fingerprint participates
	// in the thread compatibility recipe.
	rr, err := t.agent.resolveRun(ctx, runID, prompt, eff)
	if err != nil {
		return nil, fmt.Errorf("adaptor: run %s: %w", runID, err)
	}
	fingerprint := threadFingerprint(driverType, identity, eff, rr.payloadFingerprint)

	plan, err := engine.PrepareThreadSession(ctx, es, req, identity, driverType, fingerprint)
	if err != nil {
		return nil, t.threadError(err)
	}
	if plan == nil {
		// Unreachable: every thread mode is stateful. Guard anyway.
		return nil, fmt.Errorf("adaptor: thread %q: internal: no session plan", t.key)
	}
	runCtx, runCancel := context.WithCancel(ctx)
	defer runCancel()
	defer plan.Release()
	plan.StartLeaseRenewal(runCtx, runCancel)

	dreq := rr.req
	dreq.Streaming = true

	var resp driver.Response
	var runErr error
	for {
		dreq.Session = plan.DriverSession(drv)
		resp, runErr = drv.Run(runCtx, dreq, sink)
		if runErr != nil {
			var rejected *engine.ResumeRejectedError
			if errors.As(runErr, &rejected) {
				if plan.Reused() && plan.Mode() == driver.SessionContinueOrStart {
					// Same fallback as the legacy path: a rejected
					// continue-or-start resume retries once against a
					// fresh session (old one archived on persist).
					if freshErr := plan.PrepareFresh(runCtx, driverType, fingerprint); freshErr != nil {
						return nil, t.threadError(freshErr)
					}
					continue
				}
				// Resume-only (or non-reused) rejections are final;
				// expose them through the consumer sentinel while
				// keeping the driver's chain intact.
				runErr = fmt.Errorf("%w: %w", ErrResumeRejected, runErr)
			}
		}
		break
	}

	// Renewal failures take precedence over the run outcome: the renewal
	// goroutine cancelled runCtx, so whatever the driver returned is just
	// collateral of losing exclusivity.
	plan.StopLeaseRenewal()
	if renewErr := plan.RenewalError(); renewErr != nil {
		return nil, t.threadError(renewErr)
	}

	if runErr == nil {
		// Post-run structured output contract, applied before persistence
		// exactly like the legacy path (an escalated FailurePolicyError is
		// not a human decision, so it does not enable the missing-
		// checkpoint tolerance below).
		resp.StructuredOutput, resp.Failure = engine.FinalizeStructuredOutput(
			rr.schema, rr.source, resp.Output, resp.StructuredOutput, resp.Failure)
	}

	if runErr == nil {
		if _, perr := plan.Persist(runCtx, identity, drv, fingerprint, resp.Checkpoint); perr != nil {
			failure := resp.Failure
			if pending := sink.pendingFailure(); pending != nil {
				failure = pending
			}
			if !errors.Is(perr, engine.ErrSessionCheckpointMissing) || !failure.IsHumanDecision() {
				return nil, t.threadError(perr)
			}
			// Tolerated: a human decision ended the run before the driver
			// produced a resumable checkpoint. The thread's stored state
			// stays untouched; the failure itself surfaces through
			// finalizeRun as the normal *RunError.
		} else {
			t.markEstablished()
		}
	}

	return finalizeRun(runID, sink, resp, runErr)
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

// threadFingerprint computes the compatibility fingerprint guarding thread
// resumes. Recipe parity with the legacy session fingerprint: identity,
// driver type, model, workspace, and the profile payload fingerprint
// (which folds skills, MCP, agents, hooks, instructions, config, and the
// declaration flags — replacing the P2 instructions-only term) participate;
// policy and metadata deliberately do not (tuning them must not orphan
// conversations). The output schema is deliberately absent, matching the
// legacy recipe: changing the schema or mode reuses the session.
func threadFingerprint(driverType string, identity driver.AgentIdentity, eff *RunSettings, payloadFingerprint string) string {
	return engine.StableHash("thread", driverType, identity, eff.model, eff.workspace, payloadFingerprint)
}

// ============ Thread error vocabulary ============
//
// Session-coordination failures are infrastructure-side (decision D1: plain
// wrapped errors, no Result), classified by sentinel so hosts can branch
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
	// current configuration (identity/model/workspace/instructions) and
	// the thread is resume-only, so silently starting over is not allowed.
	ErrThreadIncompatible = errors.New("adaptor: thread incompatible with current configuration")
	// ErrThreadLeaseLost: the run lost its exclusivity lease mid-flight
	// (store outage or takeover); its state was not persisted.
	ErrThreadLeaseLost = errors.New("adaptor: thread lease lost")
	// ErrThreadCheckpointMissing: the driver finished without producing a
	// resumable checkpoint, so the thread state could not be persisted.
	ErrThreadCheckpointMissing = errors.New("adaptor: driver returned no resumable checkpoint")
	// ErrResumeRejected: the driver refused to resume from the stored
	// checkpoint and the thread mode does not allow a fresh start.
	ErrResumeRejected = errors.New("adaptor: driver rejected thread resume")
)

// threadError translates engine/store session errors into the consumer
// vocabulary, keeping the original chain wrapped for diagnostics.
func (t *Thread) threadError(err error) error {
	var incompatible *engine.SessionIncompatibleError
	switch {
	case errors.Is(err, engine.ErrSessionStoreRequired):
		return fmt.Errorf("%w (thread %q)", ErrThreadStoreRequired, t.key)
	case errors.Is(err, engine.ErrSessionNotFound):
		return fmt.Errorf("%w (thread %q)", ErrThreadNotFound, t.key)
	case errors.Is(err, engine.ErrSessionBusy), errors.Is(err, threadstore.ErrBusy):
		return fmt.Errorf("%w (thread %q)", ErrThreadBusy, t.key)
	case errors.As(err, &incompatible):
		reason := incompatible.Reason
		if reason == "" {
			reason = "fingerprint mismatch"
		}
		return fmt.Errorf("%w (thread %q): %s", ErrThreadIncompatible, t.key, reason)
	case errors.Is(err, engine.ErrSessionIncompatible):
		return fmt.Errorf("%w (thread %q)", ErrThreadIncompatible, t.key)
	case errors.Is(err, engine.ErrSessionLeaseLost), errors.Is(err, threadstore.ErrLeaseLost):
		return fmt.Errorf("%w (thread %q)", ErrThreadLeaseLost, t.key)
	case errors.Is(err, engine.ErrSessionCheckpointMissing):
		return fmt.Errorf("%w (thread %q)", ErrThreadCheckpointMissing, t.key)
	default:
		return fmt.Errorf("adaptor: thread %q: %w", t.key, err)
	}
}
