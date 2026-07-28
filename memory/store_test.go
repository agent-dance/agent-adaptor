package memory

// Contract tests for the threadstore.Store implementation.

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/agent-dance/agent-adaptor/threadstore"
)

func TestStoreAcquireLeaseFailsClosedWhenEntropyUnavailable(t *testing.T) {
	original := leaseEntropyRead
	defer func() { leaseEntropyRead = original }()

	entropyErr := errors.New("test entropy unavailable")
	tests := []struct {
		name string
		read func([]byte) (int, error)
		want error
	}{
		{name: "read error", read: func([]byte) (int, error) { return 0, entropyErr }, want: entropyErr},
		{name: "short read", read: func(buf []byte) (int, error) {
			if len(buf) > 0 {
				buf[0] = 1
			}
			return 1, nil
		}, want: io.ErrUnexpectedEOF},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			leaseEntropyRead = tc.read
			store := NewStore()
			lease, err := store.AcquireLease(context.Background(), "target", "owner", time.Minute)
			if !errors.Is(err, tc.want) {
				t.Fatalf("AcquireLease error = %v, want errors.Is(_, %v)", err, tc.want)
			}
			if lease != (threadstore.Lease{}) {
				t.Fatalf("lease = %+v, want zero lease", lease)
			}
			if len(store.leases) != 0 {
				t.Fatalf("entropy failure mutated lease state: %+v", store.leases)
			}
		})
	}

	leaseEntropyRead = original
	store := NewStore()
	lease, err := store.AcquireLease(context.Background(), "target", "owner", time.Minute)
	if err != nil {
		t.Fatalf("AcquireLease after restoring entropy source: %v", err)
	}
	if lease.Token == "" {
		t.Fatal("AcquireLease returned an empty token after restoring entropy source")
	}
}

func TestStoreAcquireLeaseHonorsCanceledContextWithoutMutation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	store := NewStore()

	lease, err := store.AcquireLease(ctx, "target", "owner", time.Minute)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("AcquireLease error = %v, want context.Canceled", err)
	}
	if lease != (threadstore.Lease{}) {
		t.Fatalf("lease = %+v, want zero lease", lease)
	}
	if len(store.leases) != 0 {
		t.Fatalf("canceled acquire mutated lease state: %+v", store.leases)
	}
}

func TestStoreFinalizeRejectsStaleLeaseToken(t *testing.T) {
	store := NewStore()
	ctx := context.Background()

	lease, err := store.AcquireLease(ctx, "session-1", "owner-1", time.Minute)
	if err != nil {
		t.Fatalf("acquire lease: %v", err)
	}
	if err := store.ReleaseLease(ctx, lease); err != nil {
		t.Fatalf("release lease: %v", err)
	}
	if _, err := store.AcquireLease(ctx, "session-1", "owner-1", time.Minute); err != nil {
		t.Fatalf("reacquire lease: %v", err)
	}

	err = store.Finalize(ctx, threadstore.FinalizeRequest{
		Record: threadstore.Record{
			ID:        "session-1",
			Status:    threadstore.StatusActive,
			CreatedAt: time.Now().UTC(),
			UpdatedAt: time.Now().UTC(),
		},
		HeldLeases: []threadstore.Lease{lease},
	})
	if !errors.Is(err, threadstore.ErrLeaseLost) {
		t.Fatalf("expected ErrLeaseLost, got %v", err)
	}

	record, err := store.Resolve(ctx, threadstore.Query{ID: "session-1", IncludeArchived: true})
	if err != nil {
		t.Fatalf("resolve session: %v", err)
	}
	if record != nil {
		t.Fatalf("expected no record after stale finalize, got %#v", record)
	}
}

func TestStoreFinalizeRejectsWrongLeaseOwnerWithoutWriting(t *testing.T) {
	store := NewStore()
	ctx := context.Background()
	lease, err := store.AcquireLease(ctx, "session-owner", "owner-1", time.Minute)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	forged := lease
	forged.Owner = "owner-2"
	err = store.Finalize(ctx, threadstore.FinalizeRequest{
		Record: threadstore.Record{ID: "session-owner", Key: "key", Status: threadstore.StatusActive},
		Key:    "key", HeldLeases: []threadstore.Lease{forged}, RebindActive: true,
	})
	if !errors.Is(err, threadstore.ErrLeaseLost) {
		t.Fatalf("finalize: err=%v, want ErrLeaseLost", err)
	}
	if record, err := store.Resolve(ctx, threadstore.Query{ID: "session-owner", IncludeArchived: true}); err != nil || record != nil {
		t.Fatalf("wrong-owner finalize wrote record=%#v err=%v", record, err)
	}
}

func TestStoreFinalizeArchivesPreviousAndRebindsKey(t *testing.T) {
	store := NewStore()
	ctx := context.Background()

	oldLease, err := store.AcquireLease(ctx, "session-old", "owner-old", time.Minute)
	if err != nil {
		t.Fatalf("acquire old lease: %v", err)
	}
	now := time.Now().UTC()
	if err := store.Finalize(ctx, threadstore.FinalizeRequest{
		Record: threadstore.Record{
			ID:        "session-old",
			Key:       "tenant-1/issue-1",
			Status:    threadstore.StatusActive,
			CreatedAt: now,
			UpdatedAt: now,
		},
		Key:          "tenant-1/issue-1",
		HeldLeases:   []threadstore.Lease{oldLease},
		RebindActive: true,
	}); err != nil {
		t.Fatalf("finalize old record: %v", err)
	}

	newLease, err := store.AcquireLease(ctx, "session-new", "owner-new", time.Minute)
	if err != nil {
		t.Fatalf("acquire new lease: %v", err)
	}
	if err := store.Finalize(ctx, threadstore.FinalizeRequest{
		Record: threadstore.Record{
			ID:        "session-new",
			Key:       "tenant-1/issue-1",
			Status:    threadstore.StatusActive,
			CreatedAt: now.Add(time.Second),
			UpdatedAt: now.Add(time.Second),
		},
		PreviousID:   "session-old",
		Key:          "tenant-1/issue-1",
		HeldLeases:   []threadstore.Lease{newLease},
		ArchiveOld:   true,
		RebindActive: true,
	}); err != nil {
		t.Fatalf("finalize new record: %v", err)
	}

	active, err := store.Resolve(ctx, threadstore.Query{Key: "tenant-1/issue-1"})
	if err != nil {
		t.Fatalf("resolve active session: %v", err)
	}
	if active == nil || active.ID != "session-new" {
		t.Fatalf("expected session-new to be active, got %#v", active)
	}

	archived, err := store.Resolve(ctx, threadstore.Query{ID: "session-old", IncludeArchived: true})
	if err != nil {
		t.Fatalf("resolve archived session: %v", err)
	}
	if archived == nil || archived.Status != threadstore.StatusArchived {
		t.Fatalf("expected archived old session, got %#v", archived)
	}
	// The archived record stays hidden from the active view.
	hidden, err := store.Resolve(ctx, threadstore.Query{ID: "session-old"})
	if err != nil {
		t.Fatalf("resolve archived session (active view): %v", err)
	}
	if hidden != nil {
		t.Fatalf("archived record leaked into the active view: %#v", hidden)
	}
}

func TestStoreLeaseConflictRenewalAndIdempotentRelease(t *testing.T) {
	store := NewStore()
	ctx := context.Background()

	lease, err := store.AcquireLease(ctx, "target-1", "owner-1", time.Minute)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}

	// A different owner cannot take a held target.
	if _, err := store.AcquireLease(ctx, "target-1", "owner-2", time.Minute); !errors.Is(err, threadstore.ErrBusy) {
		t.Fatalf("conflicting acquire: err = %v, want ErrBusy", err)
	}
	// The holder renews freely.
	if err := store.RenewLease(ctx, lease, time.Minute); err != nil {
		t.Fatalf("renew: %v", err)
	}

	// Release + reacquire rotates the token; the old lease is dead.
	if err := store.ReleaseLease(ctx, lease); err != nil {
		t.Fatalf("release: %v", err)
	}
	fresh, err := store.AcquireLease(ctx, "target-1", "owner-1", time.Minute)
	if err != nil {
		t.Fatalf("reacquire: %v", err)
	}
	if fresh.Token == lease.Token {
		t.Fatal("reacquired lease kept the released token")
	}
	if err := store.RenewLease(ctx, lease, time.Minute); !errors.Is(err, threadstore.ErrLeaseLost) {
		t.Fatalf("renew with stale token: err = %v, want ErrLeaseLost", err)
	}

	// Release is idempotent and ignores stale tokens.
	if err := store.ReleaseLease(ctx, lease); err != nil {
		t.Fatalf("stale release: %v", err)
	}
	if err := store.ReleaseLease(ctx, fresh); err != nil {
		t.Fatalf("release: %v", err)
	}
	if err := store.ReleaseLease(ctx, fresh); err != nil {
		t.Fatalf("second release: %v", err)
	}
}

func TestStoreFinalizeRequireKeyAbsentIsAtomicAndPreservesRawKey(t *testing.T) {
	store := NewStore()
	ctx := context.Background()
	key := "tenant\x00一:issue/1"
	now := time.Now().UTC()

	firstLease, err := store.AcquireLease(ctx, "record:first", "owner:first", time.Minute)
	if err != nil {
		t.Fatalf("acquire first: %v", err)
	}
	if err := store.Finalize(ctx, threadstore.FinalizeRequest{
		Record: threadstore.Record{ID: "first", Key: key, Status: threadstore.StatusActive, CreatedAt: now, UpdatedAt: now},
		Key:    key, HeldLeases: []threadstore.Lease{firstLease}, RebindActive: true,
	}); err != nil {
		t.Fatalf("finalize first: %v", err)
	}

	secondLease, err := store.AcquireLease(ctx, "record:second", "owner:second", time.Minute)
	if err != nil {
		t.Fatalf("acquire second: %v", err)
	}
	err = store.Finalize(ctx, threadstore.FinalizeRequest{
		Record:     threadstore.Record{ID: "second", Key: key, Status: threadstore.StatusActive, CreatedAt: now, UpdatedAt: now},
		PreviousID: "first", Key: key, HeldLeases: []threadstore.Lease{secondLease},
		ArchiveOld: true, RebindActive: true, RequireKeyAbsent: true,
	})
	if !errors.Is(err, threadstore.ErrAlreadyExists) {
		t.Fatalf("conditional finalize: err = %v, want ErrAlreadyExists", err)
	}
	active, err := store.Resolve(ctx, threadstore.Query{Key: key})
	if err != nil || active == nil || active.ID != "first" || active.Key != key || active.Status != threadstore.StatusActive {
		t.Fatalf("active changed after rejected finalize: record=%#v err=%v", active, err)
	}
	if leaked, err := store.Resolve(ctx, threadstore.Query{ID: "second", IncludeArchived: true}); err != nil || leaked != nil {
		t.Fatalf("rejected child was partially saved: record=%#v err=%v", leaked, err)
	}
}
