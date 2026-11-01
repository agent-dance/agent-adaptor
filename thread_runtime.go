package adaptor

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/agent-dance/agent-adaptor/v2/internal/engine"
	"github.com/agent-dance/agent-adaptor/v2/threadstore"
)

type threadRuntime struct {
	mu                     sync.Mutex
	agent                  *Agent
	key                    string
	task                   bool
	standby                *standbyStream
	executing              bool
	owner                  *engine.ThreadSessionPlan
	ownerCtx               context.Context
	ownerCancel            context.CancelFunc
	ownerID                string
	stopExecution          func() bool
	pending                *threadstore.FinalizeRequest
	durableID              string
	durableVersion         string
	pendingNeedsValidation bool
	recoveryID             string
	wake                   chan struct{}
	changed                chan struct{}
	ownershipErr           error
}

func (a *Agent) threadRuntime(key string) *threadRuntime {
	a.threadMu.Lock()
	defer a.threadMu.Unlock()
	if a.threads == nil {
		a.threads = make(map[string]*threadRuntime)
	}
	if a.threads[key] == nil {
		a.threads[key] = &threadRuntime{agent: a, key: key, wake: make(chan struct{}, 1), changed: make(chan struct{})}
	}
	return a.threads[key]
}

func (r *threadRuntime) signal() {
	close(r.changed)
	r.changed = make(chan struct{})
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

func (r *threadRuntime) reserveTask() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.task || r.standby != nil {
		return ErrThreadBusy
	}
	r.task = true
	return nil
}

func (r *threadRuntime) acquireLocked(ctx context.Context) error {
	if r.ownershipErr != nil {
		return r.ownershipErr
	}
	if r.owner != nil {
		if r.ownerCtx.Err() != nil {
			return ErrThreadLeaseLost
		}
		return nil
	}
	id, err := newRunID()
	if err != nil {
		return err
	}
	ownerCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	if err := r.agent.registerRun(id, cancel); err != nil {
		cancel()
		return err
	}
	acquireCtx, acquireCancel := context.WithCancel(ctx)
	stop := context.AfterFunc(ownerCtx, acquireCancel)
	defer stop()
	defer acquireCancel()
	owner, err := engine.AcquireThreadOwnership(acquireCtx, engineStore{store: r.agent.defaults.threadStore}, threadNamespace, r.key, r.agent.driver.Descriptor().Type)
	if err != nil {
		cancel()
		r.agent.unregisterRun(id)
		return err
	}
	if r.pendingNeedsValidation {
		if err := r.validatePendingLocked(acquireCtx); err != nil {
			cleanupCtx, cleanupCancel := context.WithTimeout(context.WithoutCancel(ctx), invocationCleanupTimeout)
			releaseErr := owner.ReleaseContext(cleanupCtx)
			cleanupCancel()
			cancel()
			r.agent.unregisterRun(id)
			return errors.Join(err, releaseErr)
		}
	}
	r.owner, r.ownerCtx, r.ownerCancel, r.ownerID = owner, ownerCtx, cancel, id
	owner.StartLeaseRenewal(ownerCtx, cancel)
	go r.recoverPersistence(ownerCtx)
	return nil
}

func (r *threadRuntime) begin(ctx context.Context, st *runStream) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	inherited := r.owner != nil
	if err := r.acquireLocked(ctx); err != nil {
		return err
	}
	r.executing = true
	r.stopExecution = context.AfterFunc(r.ownerCtx, st.cancel)
	if inherited {
		lease := r.owner.OwnershipLease()
		attemptCtx, cancel := context.WithTimeout(ctx, invocationCleanupTimeout)
		err, completed := engine.BoundedSessionStoreCall(attemptCtx, func(callCtx context.Context) error {
			return r.agent.defaults.threadStore.RenewLease(callCtx, threadstore.Lease{Target: lease.Target, Owner: lease.Owner, Token: lease.Token}, engine.LeaseTTL())
		})
		cancel()
		if err != nil {
			if completed && ctx.Err() != nil {
				return ctx.Err()
			}
			r.ownershipErr = fmt.Errorf("%w: validate ownership before execution: %w", ErrThreadLeaseLost, err)
			r.ownerCancel()
			return r.ownershipErr
		}
	}
	if err := r.flushLocked(ctx); err != nil {
		if errors.Is(err, threadstore.ErrLeaseLost) {
			return err
		}
		st.sink.push(Notice{Kind: "thread.persistence", Text: "Checkpoint storage is unavailable; continuing from the in-process checkpoint: " + err.Error()})
	}
	return nil
}

func (r *threadRuntime) end(task bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.executing = false
	if task {
		r.task = false
	}
	if r.stopExecution != nil {
		r.stopExecution()
		r.stopExecution = nil
	}
	r.signal()
	if r.standby == nil && r.pending == nil {
		return r.releaseLocked()
	}
	return nil
}

func (r *threadRuntime) verifyOwnership() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.owner.StopLeaseRenewal()
	if err := r.owner.RenewalError(); err != nil {
		r.ownershipErr = fmt.Errorf("%w: %w", ErrThreadLeaseLost, err)
		r.ownerCancel()
		return r.ownershipErr
	}
	if r.ownerCtx.Err() != nil {
		return ErrThreadLeaseLost
	}
	r.owner.StartLeaseRenewal(r.ownerCtx, r.ownerCancel)
	return nil
}

func (r *threadRuntime) releaseLocked() error {
	if r.owner == nil {
		return nil
	}
	r.ownerCancel()
	r.owner.StopLeaseRenewal()
	ctx, cancel := context.WithTimeout(context.Background(), invocationCleanupTimeout)
	err := r.owner.ReleaseContext(ctx)
	cancel()
	r.agent.unregisterRun(r.ownerID)
	r.owner = nil
	if r.pending != nil {
		r.pendingNeedsValidation = true
	}
	if err != nil {
		r.ownershipErr = fmt.Errorf("%w: release ownership: %w", ErrThreadLeaseLost, err)
	}
	return err
}

func (r *threadRuntime) validatePendingLocked(ctx context.Context) error {
	if r.pending == nil {
		r.pendingNeedsValidation = false
		return nil
	}
	record, err := r.agent.defaults.threadStore.Resolve(ctx, threadstore.Query{Key: r.key})
	if err != nil {
		return err
	}
	version := engine.StableHash(record)
	if version == engine.StableHash(&r.pending.Record) {
		r.pending = nil
		r.durableID = record.ID
		r.durableVersion = version
	} else if version != r.durableVersion {
		r.ownershipErr = fmt.Errorf("%w: durable Thread advanced while checkpoint recovery did not own the key", ErrThreadLeaseLost)
		return r.ownershipErr
	}
	r.pendingNeedsValidation = false
	return nil
}

func (r *threadRuntime) startReleasedRecoveryLocked(ctx context.Context) error {
	if r.pending == nil || r.recoveryID != "" || r.ownershipErr != nil {
		return nil
	}
	id, err := newRunID()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	if err := r.agent.registerRun(id, cancel); err != nil {
		cancel()
		return err
	}
	r.recoveryID = id
	go r.recoverReleasedPersistence(ctx, cancel, id)
	return nil
}

func (r *threadRuntime) recoverReleasedPersistence(ctx context.Context, cancel context.CancelFunc, id string) {
	defer cancel()
	defer r.agent.unregisterRun(id)
	defer func() {
		r.mu.Lock()
		if r.recoveryID == id {
			r.recoveryID = ""
		}
		if r.owner == nil && r.pending != nil && r.ownershipErr == nil && ctx.Err() == nil {
			if err := r.startReleasedRecoveryLocked(ctx); err != nil && !errors.Is(err, ErrAgentClosed) {
				r.ownershipErr = err
			}
		}
		r.mu.Unlock()
	}()
	delay := 100 * time.Millisecond
	for {
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		r.mu.Lock()
		if r.owner != nil || r.pending == nil || r.ownershipErr != nil {
			r.mu.Unlock()
			return
		}
		attemptCtx, attemptCancel := context.WithTimeout(ctx, invocationCleanupTimeout)
		err := r.acquireLocked(attemptCtx)
		if err == nil {
			err = r.flushLocked(attemptCtx)
			if errors.Is(err, threadstore.ErrLeaseLost) {
				r.ownershipErr = ErrThreadLeaseLost
			}
			_ = r.releaseLocked()
		}
		attemptCancel()
		finished := r.pending == nil || r.ownershipErr != nil
		r.mu.Unlock()
		if finished {
			return
		}
		delay = min(delay*2, 5*time.Second)
	}
}

func (r *threadRuntime) recoverPersistence(ctx context.Context) {
	delay := 100 * time.Millisecond
	for {
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
		case <-r.wake:
		case <-timer.C:
		}
		timer.Stop()
		r.mu.Lock()
		if r.ownerCtx != ctx || r.owner == nil {
			r.mu.Unlock()
			return
		}
		if ctx.Err() != nil {
			if r.standby != nil {
				r.standby.cancelLifetime()
			}
			if r.executing || r.task || r.standby != nil {
				changed := r.changed
				r.mu.Unlock()
				<-changed
				continue
			}
			if r.pending != nil {
				r.ownershipErr = ErrThreadLeaseLost
			}
			_ = r.releaseLocked()
			r.mu.Unlock()
			return
		}
		if !r.executing && r.pending != nil {
			flushCtx, cancel := context.WithTimeout(ctx, invocationCleanupTimeout)
			err := r.flushLocked(flushCtx)
			cancel()
			if errors.Is(err, threadstore.ErrLeaseLost) {
				r.ownershipErr = ErrThreadLeaseLost
				r.ownerCancel()
			}
		}
		if !r.task && r.standby == nil && r.pending == nil {
			_ = r.releaseLocked()
			r.mu.Unlock()
			return
		}
		r.mu.Unlock()
		if delay < 5*time.Second {
			delay = min(delay*2, 5*time.Second)
		}
	}
}

func (r *threadRuntime) flushLocked(ctx context.Context) error {
	if r.pending == nil {
		return nil
	}
	req := *r.pending
	own := r.owner.OwnershipLease()
	var acquired []threadstore.Lease
	req.HeldLeases = []threadstore.Lease{{Target: own.Target, Owner: own.Owner, Token: own.Token}}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), invocationCleanupTimeout)
		defer cancel()
		for index := len(acquired) - 1; index >= 0; index-- {
			lease := acquired[index]
			if err, _ := engine.BoundedSessionStoreCall(cleanupCtx, func(callCtx context.Context) error {
				return r.agent.defaults.threadStore.ReleaseLease(callCtx, lease)
			}); err != nil {
				r.ownershipErr = fmt.Errorf("%w: release recovery lease: %w", ErrThreadLeaseLost, err)
				r.ownerCancel()
			}
		}
	}()
	seen := map[string]bool{own.Target: true}
	for _, old := range r.pending.HeldLeases {
		if seen[old.Target] {
			continue
		}
		seen[old.Target] = true
		lease, err := r.agent.defaults.threadStore.AcquireLease(ctx, old.Target, own.Owner, engine.LeaseTTL())
		if err != nil {
			return err
		}
		acquired = append(acquired, lease)
		req.HeldLeases = append(req.HeldLeases, lease)
	}
	if err := r.agent.defaults.threadStore.Finalize(ctx, req); err != nil {
		return err
	}
	r.pending = nil
	r.durableID = req.Record.ID
	r.durableVersion = engine.StableHash(&req.Record)
	return nil
}
