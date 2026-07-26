package adaptor_test

// P2 contract migration, lease leg: the semantic assertions of the legacy
// runner_session_internal_test.go, replayed against the Thread surface.
//
//	TestLeaseRenewalKeepsLongRunningRunExclusive → TestThreadLeaseRenewalKeepsLongRunExclusive
//	TestLeaseRenewalFailureDoesNotPersist...     → TestThreadLeaseRenewalFailureAbortsRunAndSkipsPersist
//
// Unlike the legacy tests these are fully channel-synchronized: progress is
// gated on observed renewal calls and on context cancellation, never on
// sleeps, so they are deterministic under -count=N and load.

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/engine"
	"github.com/agent-dance/agent-adaptor/memory"
	adaptor "github.com/agent-dance/agent-adaptor/next"
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

func (s *failRenewStore) RenewLease(_ context.Context, lease threadstore.Lease, _ time.Duration) error {
	return &threadstore.LeaseLostError{Target: lease.Target}
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
