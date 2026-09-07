// Package activebudget owns a run-local, pausable monotonic execution budget.
// It neither interprets approvals nor discovers controllers through context values.
package activebudget

import (
	"context"
	"sync"
	"time"
)

// Timer supports invalidation; Stop does not promise a queued callback cannot run.
type Timer interface{ Stop() bool }

// Clock is a private injection seam. AfterFunc must not call f inline.
type Clock interface {
	Now() time.Time
	AfterFunc(time.Duration, func()) Timer
}
type realClock struct{}

func (realClock) Now() time.Time                            { return time.Now() }
func (realClock) AfterFunc(d time.Duration, f func()) Timer { return time.AfterFunc(d, f) }

// Controller pairs independent pause tokens and fences stale timer callbacks.
type Controller struct {
	mu         sync.Mutex
	parent     context.Context
	ctx        context.Context
	cancel     context.CancelCauseFunc
	clock      Clock
	expired    error
	remaining  time.Duration
	lastStart  time.Time
	generation uint64
	tokens     map[uint64]struct{}
	nextToken  uint64
	timer      Timer
	stopped    bool
	enabled    bool
	cause      error
	finished   bool
	finishErr  error
	unwatch    func() bool
}

// New returns a cancelable child whose Deadline is only its parent's deadline.
// Zero disables the timer. Invalid private programming inputs panic; public
// callers must validate negative limits before any resource acquisition.
func New(parent context.Context, limit time.Duration, expired error, clock Clock) (context.Context, *Controller) {
	if parent == nil || limit < 0 || limit > 0 && expired == nil {
		panic("activebudget: invalid arguments")
	}
	if clock == nil {
		clock = realClock{}
	}
	ctx, cancel := context.WithCancelCause(parent)
	b := &Controller{parent: parent, ctx: ctx, cancel: cancel, clock: clock, expired: expired, remaining: limit, enabled: limit > 0, tokens: map[uint64]struct{}{}}
	b.mu.Lock()
	b.unwatch = context.AfterFunc(ctx, b.Stop)
	if ctx.Err() != nil {
		b.stopLocked()
	} else if b.enabled {
		b.armLocked()
	}
	b.mu.Unlock()
	return ctx, b
}
func (b *Controller) inactiveLocked() bool {
	if b.stopped {
		return true
	}
	if b.ctx.Err() != nil {
		b.stopLocked()
		return true
	}
	return false
}
func (b *Controller) stopLocked() {
	b.stopped = true
	b.generation++
	if b.timer != nil {
		b.timer.Stop()
		b.timer = nil
	}
	clear(b.tokens)
	if b.unwatch != nil {
		b.unwatch()
		b.unwatch = nil
	}
}
func (b *Controller) armLocked() {
	b.generation++
	generation := b.generation
	b.lastStart = b.clock.Now()
	b.timer = b.clock.AfterFunc(b.remaining, func() { b.onTimer(generation) })
}
func (b *Controller) elapsedLocked() time.Duration {
	elapsed := b.clock.Now().Sub(b.lastStart)
	if elapsed < 0 {
		return 0
	}
	return elapsed
}
func (b *Controller) expireLocked() error {
	// The standard parent cancellation remains authoritative even if its
	// callback has not yet acquired this controller's mutex.
	if b.parent.Err() != nil {
		b.cause = context.Cause(b.parent)
	} else {
		b.cause = b.expired
	}
	b.stopLocked()
	return b.cause
}
func (b *Controller) onTimer(generation uint64) {
	b.mu.Lock()
	if b.inactiveLocked() || len(b.tokens) != 0 || b.generation != generation {
		b.mu.Unlock()
		return
	}
	elapsed := b.elapsedLocked()
	if elapsed < b.remaining {
		b.remaining -= elapsed
		b.armLocked()
		b.mu.Unlock()
		return
	}
	cause := b.expireLocked()
	b.mu.Unlock()
	b.cancel(cause)
}

// Pause returns a non-nil, idempotent release for this token alone. The last
// release resumes the remaining budget; a late pause cannot rescue exhaustion.
func (b *Controller) Pause() (release func()) {
	release = func() {}
	if b == nil {
		return release
	}
	b.mu.Lock()
	if b.inactiveLocked() || !b.enabled {
		b.mu.Unlock()
		return release
	}
	if len(b.tokens) == 0 {
		elapsed := b.elapsedLocked()
		if elapsed >= b.remaining {
			cause := b.expireLocked()
			b.mu.Unlock()
			b.cancel(cause)
			return release
		}
		b.remaining -= elapsed
		b.generation++
		if b.timer != nil {
			b.timer.Stop()
			b.timer = nil
		}
	}
	b.nextToken++
	token := b.nextToken
	b.tokens[token] = struct{}{}
	b.mu.Unlock()
	return func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		if b.inactiveLocked() {
			return
		}
		if _, ok := b.tokens[token]; !ok {
			return
		}
		delete(b.tokens, token)
		if len(b.tokens) == 0 {
			b.armLocked()
		}
	}
}

// Stop freezes timing without cancelling the child or its parent propagation.
func (b *Controller) Stop() {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.stopLocked()
}

// Cancel stops timing and cancels once with the first selected cause. Parent
// cancellation and budget expiry remain visible through context.Cause.
func (b *Controller) Cancel(cause error) {
	if b == nil {
		return
	}
	b.mu.Lock()
	if b.cause == nil {
		if b.parent.Err() != nil {
			b.cause = context.Cause(b.parent)
		} else if cause == nil {
			b.cause = context.Canceled
		} else {
			b.cause = cause
		}
	}
	b.stopLocked()
	selected := b.cause
	b.mu.Unlock()
	b.cancel(selected)
}

// FinishExecution settles the final active segment and permanently seals this
// execution budget before atomic persistence. Nil is budget health, not run
// success. It never invokes a core callback; the caller orders it with its own
// primary-outcome lock. Repeats return the first result without recharging.
func (b *Controller) FinishExecution() error {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	if b.finished {
		err := b.finishErr
		b.mu.Unlock()
		return err
	}
	b.finished = true
	switch {
	case b.ctx.Err() != nil:
		b.finishErr = context.Cause(b.ctx)
	case b.cause != nil:
		b.finishErr = b.cause
	case b.stopped: // Error/cleanup Stop must not create a late budget cause.
	case b.enabled && len(b.tokens) == 0:
		elapsed := b.elapsedLocked()
		if elapsed >= b.remaining {
			b.finishErr = b.expireLocked()
		} else {
			b.remaining -= elapsed
		}
	}
	b.stopLocked()
	err := b.finishErr
	b.mu.Unlock()
	if err != nil {
		b.cancel(err)
	}
	return err
}
