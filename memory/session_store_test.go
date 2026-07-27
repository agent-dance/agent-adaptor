package memory

import (
	"context"
	"errors"
	"testing"
	"time"

	agentadaptor "github.com/agent-dance/agent-adaptor"
)

func TestFinalizeRejectsStaleLeaseToken(t *testing.T) {
	store := NewSessionStore()
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

	err = store.Finalize(ctx, agentadaptor.SessionFinalizeRequest{
		Record: agentadaptor.SessionRecord{
			ID:        "session-1",
			Status:    agentadaptor.SessionStatusActive,
			CreatedAt: time.Now().UTC(),
			UpdatedAt: time.Now().UTC(),
		},
		HeldLeases: []agentadaptor.SessionLease{lease},
	})
	if !errors.Is(err, agentadaptor.ErrSessionLeaseLost) {
		t.Fatalf("expected ErrSessionLeaseLost, got %v", err)
	}

	record, err := store.Resolve(ctx, agentadaptor.SessionQuery{ID: "session-1", IncludeArchived: true})
	if err != nil {
		t.Fatalf("resolve session: %v", err)
	}
	if record != nil {
		t.Fatalf("expected no record after stale finalize, got %#v", record)
	}
}

func TestFinalizeArchivesPreviousAndRebindsKey(t *testing.T) {
	store := NewSessionStore()
	ctx := context.Background()

	oldLease, err := store.AcquireLease(ctx, "session-old", "owner-old", time.Minute)
	if err != nil {
		t.Fatalf("acquire old lease: %v", err)
	}
	now := time.Now().UTC()
	if err := store.Finalize(ctx, agentadaptor.SessionFinalizeRequest{
		Record: agentadaptor.SessionRecord{
			ID:        "session-old",
			Namespace: "tenant",
			Key:       "issue-1",
			Status:    agentadaptor.SessionStatusActive,
			CreatedAt: now,
			UpdatedAt: now,
		},
		Namespace:    "tenant",
		Key:          "issue-1",
		HeldLeases:   []agentadaptor.SessionLease{oldLease},
		RebindActive: true,
	}); err != nil {
		t.Fatalf("finalize old record: %v", err)
	}

	newLease, err := store.AcquireLease(ctx, "session-new", "owner-new", time.Minute)
	if err != nil {
		t.Fatalf("acquire new lease: %v", err)
	}
	if err := store.Finalize(ctx, agentadaptor.SessionFinalizeRequest{
		Record: agentadaptor.SessionRecord{
			ID:        "session-new",
			Namespace: "tenant",
			Key:       "issue-1",
			Status:    agentadaptor.SessionStatusActive,
			CreatedAt: now.Add(time.Second),
			UpdatedAt: now.Add(time.Second),
		},
		PreviousID:   "session-old",
		Namespace:    "tenant",
		Key:          "issue-1",
		HeldLeases:   []agentadaptor.SessionLease{newLease},
		ArchiveOld:   true,
		RebindActive: true,
	}); err != nil {
		t.Fatalf("finalize new record: %v", err)
	}

	active, err := store.Resolve(ctx, agentadaptor.SessionQuery{Namespace: "tenant", Key: "issue-1"})
	if err != nil {
		t.Fatalf("resolve active session: %v", err)
	}
	if active == nil || active.ID != "session-new" {
		t.Fatalf("expected session-new to be active, got %#v", active)
	}

	archived, err := store.Resolve(ctx, agentadaptor.SessionQuery{ID: "session-old", IncludeArchived: true})
	if err != nil {
		t.Fatalf("resolve archived session: %v", err)
	}
	if archived == nil || archived.Status != agentadaptor.SessionStatusArchived {
		t.Fatalf("expected archived old session, got %#v", archived)
	}
}

func TestSessionStoreCompositeKeyIndexHasNoDelimiterCollisions(t *testing.T) {
	store := NewSessionStore()
	ctx := context.Background()
	now := time.Now().UTC()
	type tuple struct{ namespace, key, id string }
	tuples := []tuple{
		{namespace: "a:b", key: "c", id: "first"},
		{namespace: "a", key: "b:c", id: "second"},
		{namespace: "\x00", key: "你好", id: "third"},
		{namespace: "", key: "not-indexed", id: "ignored"},
	}
	for _, item := range tuples[:3] {
		lease, err := store.AcquireLease(ctx, item.id, "owner:"+item.id, time.Minute)
		if err != nil {
			t.Fatalf("acquire %s: %v", item.id, err)
		}
		if err := store.Finalize(ctx, agentadaptor.SessionFinalizeRequest{
			Record:    agentadaptor.SessionRecord{ID: item.id, Namespace: item.namespace, Key: item.key, Status: agentadaptor.SessionStatusActive, CreatedAt: now, UpdatedAt: now},
			Namespace: item.namespace, Key: item.key, HeldLeases: []agentadaptor.SessionLease{lease}, RebindActive: true,
		}); err != nil {
			t.Fatalf("finalize %s: %v", item.id, err)
		}
	}
	for _, item := range tuples[:3] {
		record, err := store.Resolve(ctx, agentadaptor.SessionQuery{Namespace: item.namespace, Key: item.key})
		if err != nil || record == nil || record.ID != item.id {
			t.Fatalf("resolve (%q,%q): record=%#v err=%v", item.namespace, item.key, record, err)
		}
	}
}
