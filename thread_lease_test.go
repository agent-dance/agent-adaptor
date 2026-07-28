package adaptor_test

// Thread lease assertions from the
// runner_session_internal_test.go, replayed against the Thread surface.
//
//	TestLeaseRenewalKeepsLongRunningRunExclusive → TestThreadLeaseRenewalKeepsLongRunExclusive
//	TestLeaseRenewalFailureDoesNotPersist...     → TestThreadLeaseRenewalFailureAbortsRunAndSkipsPersist
//
// These tests are fully channel-synchronized: progress is
// gated on observed renewal calls and on context cancellation, never on
// sleeps, so they are deterministic under -count=N and load.

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/engine"
	"github.com/agent-dance/agent-adaptor/memory"
	"github.com/agent-dance/agent-adaptor/threadstore"
)

// shortLeases shrinks the engine lease knobs for the duration of one test.
// The knobs are exported func vars injected by the root package; replacing
// and restoring them wholesale keeps every other test on the defaults.
func shortLeases(t *testing.T, ttl, interval time.Duration) {
	t.Helper()
	prevTTL, prevInterval := engine.LeaseTTL, engine.LeaseRenewInterval
	engine.LeaseTTL = func() time.Duration { return ttl }
	engine.LeaseRenewInterval = func() time.Duration { return interval }
	t.Cleanup(func() {
		engine.LeaseTTL, engine.LeaseRenewInterval = prevTTL, prevInterval
	})
}

// renewCountingStore signals reached once threshold successful renewals
// were observed, then keeps delegating.
type renewCountingStore struct {
	threadstore.Store

	mu        sync.Mutex
	count     int
	threshold int
	reached   chan struct{}
	signaled  bool
}

func (s *renewCountingStore) RenewLease(ctx context.Context, lease threadstore.Lease, ttl time.Duration) error {
	err := s.Store.RenewLease(ctx, lease, ttl)
	if err == nil {
		s.mu.Lock()
		s.count++
		if s.count >= s.threshold && !s.signaled {
			s.signaled = true
			close(s.reached)
		}
		s.mu.Unlock()
	}
	return err
}

// failRenewStore fails every renewal, simulating a store outage / takeover
// after the initial acquisition.
type failRenewStore struct {
	threadstore.Store
}

// blockingRenewStore models a hostile extension hook: it enters RenewLease,
// ignores the supplied context, and returns only when the test opens gate.
// The coordinator must abandon the hook without abandoning the run lifecycle.
type blockingRenewStore struct {
	threadstore.Store
	started chan struct{}
	gate    chan struct{}
	once    sync.Once
}

func (s *blockingRenewStore) RenewLease(ctx context.Context, lease threadstore.Lease, ttl time.Duration) error {
	s.once.Do(func() { close(s.started) })
	<-s.gate
	return s.Store.RenewLease(ctx, lease, ttl)
}

var errReleaseLease = errors.New("test store: release lease failed")

type failReleaseStore struct {
	threadstore.Store
}

type releaseErrorStore struct {
	threadstore.Store
	err error
}

type acquireErrorStore struct {
	threadstore.Store
	err error
}

func (s *acquireErrorStore) AcquireLease(context.Context, string, string, time.Duration) (threadstore.Lease, error) {
	return threadstore.Lease{}, s.err
}

func (s *failReleaseStore) ReleaseLease(ctx context.Context, lease threadstore.Lease) error {
	// Release in the backing store first so the test does not leave a real
	// lease behind; the wrapper then proves coordinator cleanup errors are
	// observable without compromising cleanup progress.
	_ = s.Store.ReleaseLease(ctx, lease)
	return errReleaseLease
}

func (s *releaseErrorStore) ReleaseLease(ctx context.Context, lease threadstore.Lease) error {
	_ = s.Store.ReleaseLease(ctx, lease)
	return s.err
}

func (s *failRenewStore) RenewLease(_ context.Context, lease threadstore.Lease, _ time.Duration) error {
	return &threadstore.LeaseLostError{Target: lease.Target}
}

func TestThreadAcquireLeaseClassifiesOnlyGenuineConflictAsBusy(t *testing.T) {
	backendErr := errors.New("test store: acquire backend unavailable")
	tests := []struct {
		name     string
		storeErr error
		want     error
		busy     bool
	}{
		{name: "busy", storeErr: &threadstore.BusyError{Target: "held"}, want: adaptor.ErrThreadBusy, busy: true},
		{name: "canceled", storeErr: context.Canceled, want: context.Canceled},
		{name: "deadline", storeErr: context.DeadlineExceeded, want: context.DeadlineExceeded},
		{name: "backend", storeErr: backendErr, want: backendErr},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fake := newSessionFake("acquire-error")
			store := &acquireErrorStore{Store: memory.NewStore(), err: tc.storeErr}
			_, err := adaptor.New(fake.fakeDriver, adaptor.WithThreadStore(store)).Thread("thread-key").Run(context.Background(), "go")
			if !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want errors.Is(_, %v)", err, tc.want)
			}
			if got := errors.Is(err, adaptor.ErrThreadBusy); got != tc.busy {
				t.Fatalf("errors.Is(_, ErrThreadBusy) = %t, want %t (error %v)", got, tc.busy, err)
			}
			if fake.runCount() != 0 {
				t.Fatalf("Driver.Run called %d times after lease acquisition failed", fake.runCount())
			}
		})
	}
}

func TestThreadPrelaunchCleanupErrorSurvivesPublicClassification(t *testing.T) {
	ctx := context.Background()
	base := memory.NewStore()
	seedFake := newSessionFake("prelaunch-cleanup-seed")
	if _, err := adaptor.New(seedFake.fakeDriver, adaptor.WithThreadStore(base)).Thread("prelaunch-cleanup").Run(ctx, "seed"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	record := *activeRecord(t, base, "prelaunch-cleanup")
	record.State = nil
	overwriteActiveRecord(t, base, record)

	fake := newSessionFake("prelaunch-cleanup")
	store := &failReleaseStore{Store: base}
	_, err := adaptor.New(fake.fakeDriver, adaptor.WithThreadStore(store)).Thread("prelaunch-cleanup").Run(ctx, "must not launch")
	if !errors.Is(err, adaptor.ErrThreadCheckpointMissing) {
		t.Fatalf("error = %v, want ErrThreadCheckpointMissing", err)
	}
	if !errors.Is(err, errReleaseLease) {
		t.Fatalf("error = %v, want release failure identity", err)
	}
	if fake.runCount() != 0 {
		t.Fatalf("Driver.Run called %d times after prelaunch failure", fake.runCount())
	}

	leaseLost := &threadstore.LeaseLostError{Target: "cleanup"}
	classifiedStore := &releaseErrorStore{Store: base, err: leaseLost}
	_, err = adaptor.New(newSessionFake("prelaunch-cleanup-classification").fakeDriver, adaptor.WithThreadStore(classifiedStore)).Thread("prelaunch-cleanup").Run(ctx, "must not launch")
	if !errors.Is(err, adaptor.ErrThreadCheckpointMissing) {
		t.Fatalf("error = %v, cleanup classification replaced ErrThreadCheckpointMissing", err)
	}
	if !errors.Is(err, threadstore.ErrLeaseLost) {
		t.Fatalf("error = %v, want cleanup lease-lost identity", err)
	}
}

func TestThreadLeaseRenewalKeepsLongRunExclusive(t *testing.T) {
	// TTL 250ms, renewal every 10ms. The run below outlives many TTL
	// windows; only renewal keeps it exclusive.
	shortLeases(t, 250*time.Millisecond, 10*time.Millisecond)

	renewed := make(chan struct{})
	store := &renewCountingStore{
		Store: memory.NewStore(),
		// Two leases (key + session) renew per tick: 60 renewals ≈ 30
		// ticks ≥ 300ms of held run time — past the point where the
		// original 250ms acquisition would have expired without renewal.
		threshold: 60,
		reached:   renewed,
	}

	fake := newFakeDriver()
	started := make(chan struct{})
	release := make(chan struct{})
	fake.runFunc = func(ctx context.Context, _ driver.Request, _ driver.EventSink) (driver.Response, error) {
		close(started)
		select {
		case <-release:
		case <-ctx.Done():
			return driver.Response{}, ctx.Err()
		}
		state := &driver.SessionState{ResumeID: "long-resume-1"}
		return driver.Response{Output: "long done", Checkpoint: &driver.Checkpoint{State: state, Valid: true}}, nil
	}

	agent := adaptor.New(fake, adaptor.WithThreadStore(store))
	stream := agent.Thread("jobs/long-1").Stream(context.Background(), "long job")
	<-started
	<-renewed

	// Well past the original TTL, the thread is still exclusively held.
	if _, err := agent.Thread("jobs/long-1").Run(context.Background(), "concurrent"); !errors.Is(err, adaptor.ErrThreadBusy) {
		t.Fatalf("concurrent run after TTL: err = %v, want ErrThreadBusy", err)
	}

	close(release)
	for range stream.Events() {
	}
	if _, err := stream.Result(); err != nil {
		t.Fatalf("long run: %v", err)
	}
	if rec := activeRecord(t, store, "jobs/long-1"); rec.State == nil || rec.State.ResumeID != "long-resume-1" {
		t.Fatalf("long run state = %+v, want long-resume-1", rec.State)
	}
}

func TestThreadLeaseRenewalFailureAbortsRunAndSkipsPersist(t *testing.T) {
	shortLeases(t, 250*time.Millisecond, 10*time.Millisecond)

	backing := memory.NewStore()
	store := &failRenewStore{Store: backing}

	// The driver behaves like a hung child process: the failed renewal
	// must cancel the run context to end it.
	fake := newFakeDriver()
	fake.blockUntilCancelled()

	agent := adaptor.New(fake, adaptor.WithThreadStore(store))
	_, err := agent.Thread("jobs/flaky-1").Run(context.Background(), "long job")
	if !errors.Is(err, adaptor.ErrThreadLeaseLost) {
		t.Fatalf("run with failing renewal: err = %v, want ErrThreadLeaseLost", err)
	}

	// A run that lost exclusivity must not persist thread state.
	rec, resolveErr := backing.Resolve(context.Background(), threadstore.Query{Key: "jobs/flaky-1", IncludeArchived: true})
	if resolveErr != nil {
		t.Fatalf("resolve: %v", resolveErr)
	}
	if rec != nil {
		t.Fatalf("state persisted after lost lease: %+v", rec)
	}
}

func TestThreadCancelCompletesWhenRenewHookIgnoresContext(t *testing.T) {
	shortLeases(t, 2*time.Second, time.Millisecond)

	backing := memory.NewStore()
	gate := make(chan struct{})
	store := &blockingRenewStore{Store: backing, started: make(chan struct{}), gate: gate}
	t.Cleanup(func() { close(gate) })
	fake := newFakeDriver()
	fake.blockUntilCancelled()

	stream := adaptor.New(fake, adaptor.WithThreadStore(store)).Thread("renew-hang-cancel").Stream(context.Background(), "go")
	<-store.started
	stream.Cancel()

	type outcome struct {
		result *adaptor.Result
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		for range stream.Events() {
		}
		result, err := stream.Result()
		done <- outcome{result: result, err: err}
	}()
	select {
	case got := <-done:
		if got.result != nil {
			t.Fatalf("cancelled run returned Result: %+v", got.result)
		}
		if !errors.Is(got.err, context.Canceled) {
			t.Fatalf("Result error=%v, want context.Canceled", got.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Cancel did not close Events/Result while RenewLease ignored context")
	}
	if rec, err := backing.Resolve(context.Background(), threadstore.Query{Key: "renew-hang-cancel", IncludeArchived: true}); err != nil || rec != nil {
		t.Fatalf("cancelled hostile-renew run persisted state: rec=%+v err=%v", rec, err)
	}
}

func TestThreadRenewWatchdogAbortsContextIgnoringHook(t *testing.T) {
	// The first renewal starts after 1ms; its 40ms safety timeout plus the
	// fixed cancellation acknowledgement grace must end the run well before
	// this test's outer watchdog. No consumer Cancel or driver return assists.
	shortLeases(t, 80*time.Millisecond, time.Millisecond)

	backing := memory.NewStore()
	gate := make(chan struct{})
	store := &blockingRenewStore{Store: backing, started: make(chan struct{}), gate: gate}
	t.Cleanup(func() { close(gate) })
	fake := newFakeDriver()
	fake.blockUntilCancelled()

	stream := adaptor.New(fake, adaptor.WithThreadStore(store)).Thread("renew-hang-watchdog").Stream(context.Background(), "go")
	<-store.started

	type outcome struct {
		result *adaptor.Result
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		for range stream.Events() {
		}
		result, err := stream.Result()
		done <- outcome{result: result, err: err}
	}()
	select {
	case got := <-done:
		if got.result != nil {
			t.Fatalf("lost-renewal run returned Result: %+v", got.result)
		}
		if !errors.Is(got.err, adaptor.ErrThreadLeaseLost) {
			t.Fatalf("Result error=%v, want ErrThreadLeaseLost", got.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("renewal watchdog did not abort context-ignoring RenewLease")
	}
	if rec, err := backing.Resolve(context.Background(), threadstore.Query{Key: "renew-hang-watchdog", IncludeArchived: true}); err != nil || rec != nil {
		t.Fatalf("watchdog-aborted hostile-renew run persisted state: rec=%+v err=%v", rec, err)
	}
}

func TestThreadDriverReturnReportsAbandonedRenewHook(t *testing.T) {
	shortLeases(t, 2*time.Second, time.Millisecond)

	backing := memory.NewStore()
	gate := make(chan struct{})
	store := &blockingRenewStore{Store: backing, started: make(chan struct{}), gate: gate}
	t.Cleanup(func() { close(gate) })
	driverReturn := make(chan struct{})
	fake := newFakeDriver()
	fake.runFunc = func(context.Context, driver.Request, driver.EventSink) (driver.Response, error) {
		<-driverReturn
		return driver.Response{
			Output: "nominally complete",
			Checkpoint: &driver.Checkpoint{
				Valid: true,
				State: &driver.SessionState{ResumeID: "renew-hang-resume"},
			},
		}, nil
	}

	stream := adaptor.New(fake, adaptor.WithThreadStore(store)).Thread("renew-hang-return").Stream(context.Background(), "go")
	<-store.started
	close(driverReturn)

	type outcome struct {
		result *adaptor.Result
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		for range stream.Events() {
		}
		result, err := stream.Result()
		done <- outcome{result: result, err: err}
	}()
	select {
	case got := <-done:
		if got.result != nil {
			t.Fatalf("uncertain renewal returned success Result: %+v", got.result)
		}
		if !errors.Is(got.err, adaptor.ErrThreadLeaseLost) {
			t.Fatalf("Result error=%v, want ErrThreadLeaseLost", got.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Driver return did not close Events/Result while RenewLease ignored context")
	}
	if rec, err := backing.Resolve(context.Background(), threadstore.Query{Key: "renew-hang-return", IncludeArchived: true}); err != nil || rec != nil {
		t.Fatalf("uncertain hostile-renew run persisted state: rec=%+v err=%v", rec, err)
	}
}

func TestThreadLeaseReleaseFailureIsObservable(t *testing.T) {
	backing := memory.NewStore()
	store := &failReleaseStore{Store: backing}
	fake := newFakeDriver()
	fake.runFunc = func(context.Context, driver.Request, driver.EventSink) (driver.Response, error) {
		return driver.Response{
			Output: "completed",
			Checkpoint: &driver.Checkpoint{
				Valid: true,
				State: &driver.SessionState{ResumeID: "release-resume-1"},
			},
		}, nil
	}

	agent := adaptor.New(fake, adaptor.WithThreadStore(store))
	res, err := agent.Thread("release-error").Run(context.Background(), "go")
	if res != nil {
		t.Fatalf("cleanup infrastructure failure returned success Result: %+v", res)
	}
	if !errors.Is(err, errReleaseLease) {
		t.Fatalf("release error=%v, want observable store failure", err)
	}
	if rec := activeRecord(t, backing, "release-error"); rec.State == nil || rec.State.ResumeID != "release-resume-1" {
		t.Fatalf("healthy checkpoint was not atomically finalized before cleanup failed: %+v", rec)
	}
}
