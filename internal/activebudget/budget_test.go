package activebudget

import (
	"context"
	"errors"
	"sort"
	"sync"
	"testing"
	"time"
)

type fakeTimer struct {
	clock *fakeClock
	id    int
}

func (t *fakeTimer) Stop() bool {
	t.clock.mu.Lock()
	defer t.clock.mu.Unlock()
	entry := t.clock.timers[t.id]
	was := entry.active
	entry.active = false
	return was
}

type timerEntry struct {
	at       time.Time
	callback func()
	active   bool
}
type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	next   int
	timers map[int]*timerEntry
}

func newFakeClock() *fakeClock      { return &fakeClock{now: time.Now(), timers: map[int]*timerEntry{}} }
func (c *fakeClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *fakeClock) AfterFunc(d time.Duration, f func()) Timer {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.next++
	c.timers[c.next] = &timerEntry{c.now.Add(d), f, true}
	return &fakeTimer{c, c.next}
}
func (c *fakeClock) Advance(d time.Duration) {
	if d < 0 {
		panic("negative advance")
	}
	c.mu.Lock()
	end := c.now.Add(d)
	c.mu.Unlock()
	for {
		c.mu.Lock()
		id := 0
		for k, v := range c.timers {
			if v.active && !v.at.After(end) && (id == 0 || v.at.Before(c.timers[id].at) || v.at.Equal(c.timers[id].at) && k < id) {
				id = k
			}
		}
		if id == 0 {
			c.now = end
			c.mu.Unlock()
			return
		}
		v := c.timers[id]
		c.now = v.at
		v.active = false
		c.mu.Unlock()
		v.callback()
	}
}
func (c *fakeClock) Fire(id int) { c.mu.Lock(); f := c.timers[id].callback; c.mu.Unlock(); f() }
func (c *fakeClock) Pending() []int {
	c.mu.Lock()
	defer c.mu.Unlock()
	var ids []int
	for id, v := range c.timers {
		if v.active {
			ids = append(ids, id)
		}
	}
	sort.Ints(ids)
	return ids
}

var errBudget = errors.New("test active budget")

func TestBudgetExactActiveTime(t *testing.T) {
	clock := newFakeClock()
	ctx, b := New(context.Background(), 100*time.Millisecond, errBudget, clock)
	defer b.Cancel(nil)
	if _, ok := ctx.Deadline(); ok {
		t.Fatal("active limit is not a fixed deadline")
	}
	clock.Advance(40 * time.Millisecond)
	release := b.Pause()
	clock.Advance(300 * time.Millisecond)
	release()
	clock.Advance(50 * time.Millisecond)
	if context.Cause(ctx) != nil {
		t.Fatal(context.Cause(ctx))
	}
	clock.Advance(10 * time.Millisecond)
	if !errors.Is(context.Cause(ctx), errBudget) {
		t.Fatal(context.Cause(ctx))
	}
}
func TestBudgetTokensAndGeneration(t *testing.T) {
	c := newFakeClock()
	ctx, b := New(context.Background(), 100*time.Millisecond, errBudget, c)
	defer b.Cancel(nil)
	initial := c.Pending()[0]
	c.Advance(40 * time.Millisecond)
	a := b.Pause()
	btoken := b.Pause()
	a()
	a()
	c.Advance(time.Second)
	c.Fire(initial)
	if ctx.Err() != nil || len(c.Pending()) != 0 {
		t.Fatal("first release/stale callback resumed overlapping Ask")
	}
	btoken()
	armed := c.Pending()[0]
	c.Fire(initial)
	c.Fire(armed) // deliberately early callback re-arms the remaining time
	if ctx.Err() != nil {
		t.Fatal("stale/early callback exhausted budget")
	}
	c.Advance(59 * time.Millisecond)
	if ctx.Err() != nil {
		t.Fatal(ctx.Err())
	}
	c.Advance(time.Millisecond)
	if context.Cause(ctx) != errBudget {
		t.Fatal(context.Cause(ctx))
	}
	btoken()
	b.Pause()()
}
func TestBudgetPauseAtBoundary(t *testing.T) {
	c := newFakeClock()
	ctx, b := New(context.Background(), 100*time.Millisecond, errBudget, c)
	defer b.Cancel(nil)
	c.mu.Lock()
	c.now = c.now.Add(100 * time.Millisecond)
	c.mu.Unlock() // leave due callback queued
	b.Pause()()
	if context.Cause(ctx) != errBudget {
		t.Fatal("pause rescued exhausted budget")
	}
}
func TestBudgetParentAndStop(t *testing.T) {
	for _, stop := range []bool{false, true} {
		t.Run(map[bool]string{false: "paused", true: "stopped"}[stop], func(t *testing.T) {
			c := newFakeClock()
			parent, cancel := context.WithCancelCause(context.Background())
			ctx, b := New(parent, time.Second, errBudget, c)
			id := c.Pending()[0]
			release := b.Pause()
			if stop {
				b.Stop()
				b.Stop()
			}
			cause := errors.New("parent cause")
			cancel(cause)
			<-ctx.Done()
			c.Fire(id)
			release()
			if context.Cause(ctx) != cause {
				t.Fatal(context.Cause(ctx))
			}
			b.Cancel(errBudget)
		})
	}
	c := newFakeClock()
	ctx, b := New(context.Background(), time.Second, errBudget, c)
	id := c.Pending()[0]
	b.Stop()
	c.Advance(10 * time.Second)
	c.Fire(id)
	if ctx.Err() != nil {
		t.Fatal("Stop cancelled child")
	}
	b.Cancel(nil)
}
func TestBudgetZeroAndArguments(t *testing.T) {
	c := newFakeClock()
	ctx, b := New(context.Background(), 0, nil, c)
	b.Pause()()
	c.Advance(time.Hour)
	if ctx.Err() != nil || len(c.Pending()) != 0 {
		t.Fatal("zero armed a timer")
	}
	b.Cancel(nil)
	for _, fn := range []func(){func() { New(nil, 0, nil, nil) }, func() { New(context.Background(), -1, nil, nil) }, func() { New(context.Background(), 1, nil, nil) }, func() { newFakeClock().Advance(-1) }} {
		func() {
			defer func() {
				if recover() == nil {
					t.Error("invalid programming input did not panic")
				}
			}()
			fn()
		}()
	}
}
func TestBudgetTimerCancelRace(t *testing.T) {
	for range 100 {
		ctx, b := New(context.Background(), time.Microsecond, errBudget, nil)
		var wg sync.WaitGroup
		wg.Go(func() {
			for range 10 {
				b.Pause()()
			}
		})
		wg.Go(func() { b.Stop(); b.Cancel(nil) })
		wg.Go(func() { b.Cancel(errors.New("cancel")) })
		wg.Wait()
		<-ctx.Done()
		if context.Cause(ctx) == nil {
			t.Fatal("missing cause")
		}
	}
}
func TestBudgetParentDeadlineWhilePaused(t *testing.T) {
	p, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	ctx, b := New(p, time.Hour, errBudget, nil)
	defer b.Cancel(nil)
	release := b.Pause()
	defer release()
	<-ctx.Done()
	if !errors.Is(context.Cause(ctx), context.DeadlineExceeded) {
		t.Fatal(context.Cause(ctx))
	}
}

func TestBudgetFinishExecution(t *testing.T) {
	t.Run("late timer cannot rescue exhausted execution", func(t *testing.T) {
		c := newFakeClock()
		ctx, b := New(context.Background(), 100*time.Millisecond, errBudget, c)
		defer b.Cancel(nil)
		c.mu.Lock()
		c.now = c.now.Add(100 * time.Millisecond)
		c.mu.Unlock()
		if !errors.Is(b.FinishExecution(), errBudget) || context.Cause(ctx) != errBudget {
			t.Fatal("sealed overdrawn execution")
		}
		if !errors.Is(b.FinishExecution(), errBudget) {
			t.Fatal("repeated finish changed error")
		}
	})
	t.Run("sealed execution ignores delayed callbacks", func(t *testing.T) {
		c := newFakeClock()
		p, cancel := context.WithCancel(context.Background())
		ctx, b := New(p, 100*time.Millisecond, errBudget, c)
		defer b.Cancel(nil)
		id := c.Pending()[0]
		c.Advance(90 * time.Millisecond)
		if err := b.FinishExecution(); err != nil {
			t.Fatal(err)
		}
		c.Advance(time.Hour)
		c.Fire(id)
		if err := b.FinishExecution(); err != nil || ctx.Err() != nil {
			t.Fatal(err, ctx.Err())
		}
		cancel()
		<-ctx.Done()
		if err := b.FinishExecution(); err != nil {
			t.Fatal("finish result changed after parent cancellation", err)
		}
	})
	t.Run("error stop never retroactively charges", func(t *testing.T) {
		c := newFakeClock()
		ctx, b := New(context.Background(), time.Millisecond, errBudget, c)
		defer b.Cancel(nil)
		id := c.Pending()[0]
		b.Stop()
		c.Advance(time.Hour)
		if err := b.FinishExecution(); err != nil {
			t.Fatal(err)
		}
		c.Fire(id)
		if ctx.Err() != nil {
			t.Fatal(context.Cause(ctx))
		}
	})
	t.Run("concurrent release and sealing", func(t *testing.T) {
		for range 100 {
			c := newFakeClock()
			ctx, b := New(context.Background(), 100*time.Millisecond, errBudget, c)
			c.Advance(40 * time.Millisecond)
			release := b.Pause()
			c.Advance(time.Hour)
			var wg sync.WaitGroup
			wg.Go(release)
			wg.Go(func() {
				if err := b.FinishExecution(); err != nil {
					t.Error(err)
				}
			})
			wg.Wait()
			c.Advance(time.Hour)
			if ctx.Err() != nil {
				t.Fatal(context.Cause(ctx))
			}
			b.Cancel(nil)
		}
	})
}

// A valid parent whose registered AfterFunc has been scheduled but cannot yet
// deliver its callback. This deterministically models parent-to-child scheduler
// delay without modifying the controller or relying on a sleep race.
type alignmentDeferredParent struct {
	mu            sync.Mutex
	done, deliver chan struct{}
	err           error
	wg            sync.WaitGroup
}

func (*alignmentDeferredParent) Deadline() (time.Time, bool) { return time.Time{}, false }
func (p *alignmentDeferredParent) Done() <-chan struct{}     { return p.done }
func (p *alignmentDeferredParent) Err() error                { p.mu.Lock(); defer p.mu.Unlock(); return p.err }
func (*alignmentDeferredParent) Value(any) any               { return nil }
func (p *alignmentDeferredParent) AfterFunc(fn func()) func() bool {
	var mu sync.Mutex
	active := true
	p.wg.Go(func() {
		<-p.done
		<-p.deliver
		mu.Lock()
		run := active
		active = false
		mu.Unlock()
		if run {
			fn()
		}
	})
	return func() bool { mu.Lock(); defer mu.Unlock(); was := active; active = false; return was }
}
func (p *alignmentDeferredParent) cancel() {
	p.mu.Lock()
	p.err = context.Canceled
	close(p.done)
	p.mu.Unlock()
}

func TestBudgetFinishObservesAlreadyCancelledParent(t *testing.T) {
	p := &alignmentDeferredParent{done: make(chan struct{}), deliver: make(chan struct{})}
	c := newFakeClock()
	ctx, b := New(p, 100*time.Millisecond, errBudget, c)
	defer func() { close(p.deliver); p.wg.Wait(); b.Cancel(nil) }()
	c.Advance(40 * time.Millisecond)
	p.cancel()
	if ctx.Err() != nil {
		t.Fatal("fixture lost the deliberate parent-delivery barrier")
	}
	err := b.FinishExecution()
	if !errors.Is(err, context.Canceled) {
		t.Errorf("FinishExecution=%v with parent.Err=%v: already-cancelled parent must reject the seal", err, p.Err())
	}
}

func TestBudgetSelectedExpiryPrecedesChildCause(t *testing.T) {
	parent, cancelParent := context.WithCancel(context.Background())
	defer cancelParent()
	c := newFakeClock()
	ctx, b := New(parent, 100*time.Millisecond, errBudget, c)
	defer b.Cancel(nil)
	// Delay only the lock-free notification after the controller selected its
	// own expiry. The standard child remains free to receive parent cancellation.
	selected, propagate := make(chan struct{}), make(chan struct{})
	var once sync.Once
	contextCancel := b.cancel
	b.cancel = func(cause error) {
		once.Do(func() { close(selected) })
		<-propagate
		contextCancel(cause)
	}
	advanced := make(chan struct{})
	go func() { c.Advance(100 * time.Millisecond); close(advanced) }()
	<-selected
	if ctx.Err() != nil {
		t.Error("fixture failed to hold child propagation")
	}
	if b.SelectedCause() != errBudget {
		t.Error("selected cause was hidden until context propagation")
	}
	cancelParent()
	<-ctx.Done()
	close(propagate)
	<-advanced
	if err := b.FinishExecution(); !errors.Is(err, errBudget) {
		t.Fatalf("selected own expiry replaced by child cause: %v", err)
	}
	if b.SelectedCause() != errBudget || b.FinishExecution() != errBudget {
		t.Fatal("selection or cached finish changed after parent notification")
	}
}

func TestBudgetSelectedCauseDoesNotRecompute(t *testing.T) {
	var nilBudget *Controller
	if nilBudget.SelectedCause() != nil {
		t.Fatal("nil controller selected a cause")
	}
	c := newFakeClock()
	parent, cancel := context.WithCancel(context.Background())
	_, b := New(parent, 100*time.Millisecond, errBudget, c)
	defer b.Cancel(nil)
	defer cancel()
	// The clock is past the limit but neither a callback nor a seal ran.
	c.mu.Lock()
	c.now = c.now.Add(time.Hour)
	c.mu.Unlock()
	if b.SelectedCause() != nil {
		t.Fatal("reading selection charged previously unsettled time")
	}
	b.Stop()
	cancel()
	if b.SelectedCause() != nil {
		t.Fatal("reading selection inferred a new parent/Stop cause")
	}
}
