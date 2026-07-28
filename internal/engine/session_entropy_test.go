package engine

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"
)

type entropySessionStore struct {
	acquires  int
	resolves  int
	finalizes int
	releases  int
}

func (s *entropySessionStore) Resolve(context.Context, SessionQuery) (*SessionRecord, error) {
	s.resolves++
	return nil, nil
}

func (s *entropySessionStore) Finalize(context.Context, SessionFinalizeRequest) error {
	s.finalizes++
	return nil
}

func (s *entropySessionStore) AcquireLease(_ context.Context, target, owner string, _ time.Duration) (SessionLease, error) {
	s.acquires++
	return SessionLease{Target: target, Owner: owner, Token: "token"}, nil
}

func (*entropySessionStore) RenewLease(context.Context, SessionLease, time.Duration) error {
	return nil
}

func (s *entropySessionStore) ReleaseLease(context.Context, SessionLease) error {
	s.releases++
	return nil
}

func TestSessionIdentifiersFailClosedWhenEntropyUnavailable(t *testing.T) {
	original := sessionEntropyRead
	defer func() { sessionEntropyRead = original }()

	entropyErr := errors.New("test session entropy unavailable")
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
			sessionEntropyRead = tc.read
			if id, err := newEngineSessionID("driver", "fingerprint"); id != "" || !errors.Is(err, tc.want) {
				t.Fatalf("newEngineSessionID = (%q, %v), want empty ID and errors.Is(_, %v)", id, err, tc.want)
			}
			if owner, err := newLeaseOwner(AgentIdentity{ID: "agent"}, "driver", SessionRequest{Mode: SessionContinueOrStart}); owner != "" || !errors.Is(err, tc.want) {
				t.Fatalf("newLeaseOwner = (%q, %v), want empty owner and errors.Is(_, %v)", owner, err, tc.want)
			}
		})
	}
}

func TestPrepareSessionPlanDoesNotTouchStoreWhenLeaseOwnerEntropyFails(t *testing.T) {
	original := sessionEntropyRead
	defer func() { sessionEntropyRead = original }()
	entropyErr := errors.New("test lease owner entropy unavailable")
	sessionEntropyRead = func([]byte) (int, error) { return 0, entropyErr }
	store := &entropySessionStore{}

	plan, err := prepareSessionPlan(context.Background(), store, SessionRequest{
		Namespace: "thread", Key: "key", Mode: SessionContinueOrStart,
	}, AgentIdentity{ID: "agent"}, "driver", "fingerprint")
	if plan != nil {
		t.Fatal("lease-owner entropy failure returned a plan")
	}
	if !errors.Is(err, entropyErr) {
		t.Fatalf("error = %v, want entropy identity", err)
	}
	if store.acquires != 0 || store.resolves != 0 || store.finalizes != 0 || store.releases != 0 {
		t.Fatalf("store touched after lease-owner entropy failure: %+v", store)
	}
}

func TestPrepareSessionPlanReleasesKeyLeaseWhenSessionIDEntropyFails(t *testing.T) {
	original := sessionEntropyRead
	defer func() { sessionEntropyRead = original }()
	entropyErr := errors.New("test session ID entropy unavailable")
	reads := 0
	sessionEntropyRead = func(buf []byte) (int, error) {
		reads++
		if reads == 1 {
			for index := range buf {
				buf[index] = byte(index + 1)
			}
			return len(buf), nil
		}
		return 0, entropyErr
	}
	store := &entropySessionStore{}

	plan, err := prepareSessionPlan(context.Background(), store, SessionRequest{
		Namespace: "thread", Key: "key", Mode: SessionContinueOrStart,
	}, AgentIdentity{ID: "agent"}, "driver", "fingerprint")
	if plan != nil {
		t.Fatal("session-ID entropy failure returned a plan")
	}
	if !errors.Is(err, entropyErr) {
		t.Fatalf("error = %v, want entropy identity", err)
	}
	if store.acquires != 1 || store.resolves != 1 || store.releases != 1 || store.finalizes != 0 {
		t.Fatalf("store lifecycle after session-ID entropy failure = %+v, want one key acquire/resolve/release and no finalize", store)
	}
}

func TestPrepareFreshEntropyFailureLeavesReusedPlanIntact(t *testing.T) {
	original := sessionEntropyRead
	defer func() { sessionEntropyRead = original }()
	entropyErr := errors.New("test fresh session entropy unavailable")
	sessionEntropyRead = func([]byte) (int, error) { return 0, entropyErr }
	store := &entropySessionStore{}
	record := &SessionRecord{ID: "existing"}
	plan := &resolvedSessionPlan{
		engineID:      "existing",
		record:        record,
		reused:        true,
		compatibility: SessionCompatibility{Status: SessionCompatibilityCompatible},
	}

	err := plan.prepareFresh(context.Background(), store, "driver", "fingerprint")
	if !errors.Is(err, entropyErr) {
		t.Fatalf("prepareFresh error = %v, want entropy identity", err)
	}
	if plan.engineID != "existing" || plan.previousID != "" || plan.record != record || !plan.reused || plan.created {
		t.Fatalf("entropy failure mutated reused plan: %+v", plan)
	}
	if store.acquires != 0 {
		t.Fatalf("fresh record lease acquired without a secure session ID: %d", store.acquires)
	}
}
