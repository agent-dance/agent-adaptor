package engine

import (
	"context"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

type renewTimingObservation struct {
	ttl     time.Duration
	timeout time.Duration
}

type renewTimingStore struct {
	SessionStore
	observed chan renewTimingObservation
}

func (s renewTimingStore) RenewLease(ctx context.Context, _ SessionLease, ttl time.Duration) error {
	deadline, _ := ctx.Deadline()
	select {
	case s.observed <- renewTimingObservation{ttl: ttl, timeout: time.Until(deadline)}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func TestLeaseRenewalKeepsStartTimeTTLAndTimeout(t *testing.T) {
	previousTTL, previousInterval := LeaseTTL, LeaseRenewInterval
	t.Cleanup(func() { LeaseTTL, LeaseRenewInterval = previousTTL, previousInterval })
	synctest.Test(t, func(t *testing.T) {
		var ttl atomic.Int64
		ttl.Store(int64(2 * time.Second))
		LeaseTTL = func() time.Duration { return time.Duration(ttl.Load()) }
		LeaseRenewInterval = func() time.Duration { return 100 * time.Millisecond }
		observed := make(chan renewTimingObservation)
		plan := &resolvedSessionPlan{leases: []SessionLease{{Target: "owned", Owner: "owner", Token: "token"}}}
		plan.startLeaseRenewal(context.Background(), renewTimingStore{observed: observed}, nil)
		defer plan.stopLeaseRenewal()

		// A timing source changing after startup must not alter this loop's
		// lease arguments or hook deadline. A detached hook may outlive the
		// caller that supplied those sources altogether.
		ttl.Store(int64(20 * time.Second))
		got := <-observed
		if got.ttl != 2*time.Second || got.timeout != time.Second {
			t.Fatalf("renewal read timing after startup: ttl=%v timeout=%v", got.ttl, got.timeout)
		}
	})
}
