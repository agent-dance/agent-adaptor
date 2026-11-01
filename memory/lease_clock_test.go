package memory

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/agent-dance/agent-adaptor/v2/threadstore"
)

type controlledStoreClock struct {
	wall time.Time
	tick time.Duration
}

func (c *controlledStoreClock) now() time.Time { return c.wall }
func (c *controlledStoreClock) leaseNow() time.Time {
	return time.Unix(0, 0).Add(c.tick)
}

func TestStoreLeaseDeadlinesRetainMonotonicTime(t *testing.T) {
	store := NewStore()
	ctx := context.Background()
	lease, err := store.AcquireLease(ctx, "target", "writer", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	assertMonotonic := func() {
		t.Helper()
		deadline := store.leases[lease.Target].Until
		// Round(0) removes only the monotonic reading. A deadline without one
		// would compare wall time even when the renewal supplies monotonic time.
		if deadline == deadline.Round(0) {
			t.Fatal("lease deadline lost its monotonic clock reading")
		}
	}
	assertMonotonic()
	if err := store.RenewLease(ctx, lease, time.Minute); err != nil {
		t.Fatal(err)
	}
	assertMonotonic()
}

func TestStoreLeaseExpiryUsesElapsedTime(t *testing.T) {
	const ttl = 250 * time.Millisecond
	for _, scenario := range []struct {
		name    string
		wall    time.Duration
		elapsed time.Duration
		live    bool
	}{
		{name: "forward_wall_jump", wall: time.Second, elapsed: 10 * time.Millisecond, live: true},
		{name: "backward_wall_jump_at_expiry", wall: -time.Second, elapsed: ttl},
	} {
		for _, operation := range []string{"acquire", "renew", "finalize"} {
			t.Run(scenario.name+"/"+operation, func(t *testing.T) {
				ctx := context.Background()
				clock := &controlledStoreClock{wall: time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)}
				store := NewStore()
				store.clock = clock
				lease, err := store.AcquireLease(ctx, "target", "writer", ttl)
				if err != nil {
					t.Fatal(err)
				}
				clock.wall = clock.wall.Add(scenario.wall)
				clock.tick += scenario.elapsed

				switch operation {
				case "acquire":
					other, err := store.AcquireLease(ctx, "target", "other", ttl)
					if scenario.live {
						if !errors.Is(err, threadstore.ErrBusy) {
							t.Fatalf("live lease after wall jump: AcquireLease = %+v, %v; want ErrBusy", other, err)
						}
					} else {
						if err != nil {
							t.Fatalf("expired lease after wall jump: AcquireLease = %v", err)
						}
						if other.Token == lease.Token {
							t.Fatal("expired lease takeover reused the old token")
						}
						if err := store.RenewLease(ctx, lease, ttl); !errors.Is(err, threadstore.ErrLeaseLost) {
							t.Fatalf("old writer renewed after takeover: %v", err)
						}
					}
				case "renew":
					err := store.RenewLease(ctx, lease, ttl)
					if scenario.live {
						if err != nil {
							t.Fatalf("live lease after wall jump: RenewLease = %v", err)
						}
						// The renewed TTL starts at this renewal's elapsed time.
						clock.tick += ttl - time.Nanosecond
						if _, err := store.AcquireLease(ctx, "target", "other", ttl); !errors.Is(err, threadstore.ErrBusy) {
							t.Fatalf("renewed lease expired early: %v", err)
						}
						clock.tick++
						if err := store.RenewLease(ctx, lease, ttl); !errors.Is(err, threadstore.ErrLeaseLost) {
							t.Fatalf("renew at exact expiry = %v, want ErrLeaseLost", err)
						}
					} else if !errors.Is(err, threadstore.ErrLeaseLost) {
						t.Fatalf("expired lease after wall jump: RenewLease = %v, want ErrLeaseLost", err)
					}
				case "finalize":
					err := store.Finalize(ctx, threadstore.FinalizeRequest{
						Record: threadstore.Record{ID: "record", Key: "key", Status: threadstore.StatusActive},
						Key:    "key", RebindActive: true, HeldLeases: []threadstore.Lease{lease},
					})
					if scenario.live {
						if err != nil {
							t.Fatalf("live lease after wall jump: Finalize = %v", err)
						}
					} else {
						if !errors.Is(err, threadstore.ErrLeaseLost) {
							t.Fatalf("expired lease after wall jump: Finalize = %v, want ErrLeaseLost", err)
						}
						if record, err := store.Resolve(ctx, threadstore.Query{Key: "key"}); err != nil || record != nil {
							t.Fatalf("expired writer changed state: record=%+v err=%v", record, err)
						}
					}
				}
			})
		}
	}
}
