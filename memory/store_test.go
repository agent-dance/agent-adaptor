package memory

// Contract tests for the threadstore.Store implementation, mirroring the
// SessionStore tests in session_store_test.go (P2 migration: the same
// finalize/lease semantics, single thread key instead of namespace+key).

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/agent-dance/agent-adaptor/threadstore"
)

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
