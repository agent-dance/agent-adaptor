package adaptor_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	adaptor "github.com/agent-dance/agent-adaptor/v2"
	"github.com/agent-dance/agent-adaptor/v2/driver"
	"github.com/agent-dance/agent-adaptor/v2/memory"
	"github.com/agent-dance/agent-adaptor/v2/threadstore"
)

// The ownership key renews normally; the session-record hook ignores the
// cancellation caused by StopLeaseRenewal until the test releases it.
type stoppingRenewStore struct {
	threadstore.Store
	mu        sync.Mutex
	keyTarget string
	started   chan struct{}
	stopping  chan struct{}
	gate      chan struct{}
	startOnce sync.Once
	stopOnce  sync.Once
}

func (s *stoppingRenewStore) AcquireLease(ctx context.Context, target, owner string, ttl time.Duration) (threadstore.Lease, error) {
	lease, err := s.Store.AcquireLease(ctx, target, owner, ttl)
	if err == nil {
		s.mu.Lock()
		if s.keyTarget == "" {
			s.keyTarget = target
		}
		s.mu.Unlock()
	}
	return lease, err
}

func (s *stoppingRenewStore) RenewLease(ctx context.Context, lease threadstore.Lease, ttl time.Duration) error {
	s.mu.Lock()
	ownership := lease.Target == s.keyTarget
	s.mu.Unlock()
	if ownership {
		return s.Store.RenewLease(ctx, lease, ttl)
	}
	s.startOnce.Do(func() { close(s.started) })
	<-ctx.Done()
	s.stopOnce.Do(func() { close(s.stopping) })
	<-s.gate
	return ctx.Err()
}

func TestThreadCancelDuringRenewStopKeepsCancellationAndLeaseDiagnostic(t *testing.T) {
	shortLeases(t, 2*time.Second, time.Millisecond)
	store := &stoppingRenewStore{Store: memory.NewStore(), started: make(chan struct{}), stopping: make(chan struct{}), gate: make(chan struct{})}
	t.Cleanup(func() { close(store.gate) })
	fake := newFakeDriver()
	fake.runFunc = func(ctx context.Context, _ driver.Request, _ driver.EventSink) (driver.Response, error) {
		select {
		case <-store.started:
			return messageCheckpoint("provider completed"), nil
		case <-ctx.Done():
			return driver.Response{}, ctx.Err()
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stream := adaptor.New(fake, adaptor.WithThreadStore(store)).Thread("cancel-renew-stop").Send(ctx, "work")
	select {
	case <-store.stopping:
		// The renewal stop cause is already established; cancellation must
		// retain its identity even when the abandoned hook adds lease-lost.
		stream.Cancel()
	case <-ctx.Done():
		t.Fatal("session renewal did not reach its stop handshake")
	}
	result, err := stream.Result()
	if result != nil || !errors.Is(err, context.Canceled) || !errors.Is(err, adaptor.ErrThreadLeaseLost) {
		t.Fatalf("cancel during renewal stop: result=%+v error=%v", result, err)
	}
	if record, err := store.Resolve(context.Background(), threadstore.Query{Key: "cancel-renew-stop", IncludeArchived: true}); err != nil || record != nil {
		t.Fatalf("abandoned renewal persisted state: record=%+v error=%v", record, err)
	}
}
