package engine_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/engine"
	"github.com/agent-dance/agent-adaptor/internal/keycodec"
	"github.com/agent-dance/agent-adaptor/memory"
)

type prelaunchUnwindStore struct {
	engineStoreAdapter

	mu             sync.Mutex
	resolveCalls   int
	resolveFailAt  int
	resolveErr     error
	acquireCalls   int
	acquireFailAt  int
	acquireErr     error
	releaseErr     error
	releaseTargets []string
}

func (s *prelaunchUnwindStore) Resolve(ctx context.Context, query engine.SessionQuery) (*engine.SessionRecord, error) {
	s.mu.Lock()
	s.resolveCalls++
	call := s.resolveCalls
	s.mu.Unlock()
	if call == s.resolveFailAt {
		return nil, s.resolveErr
	}
	return s.engineStoreAdapter.Resolve(ctx, query)
}

func (s *prelaunchUnwindStore) AcquireLease(ctx context.Context, target, owner string, ttl time.Duration) (engine.SessionLease, error) {
	s.mu.Lock()
	s.acquireCalls++
	call := s.acquireCalls
	s.mu.Unlock()
	if call == s.acquireFailAt {
		return engine.SessionLease{}, s.acquireErr
	}
	return s.engineStoreAdapter.AcquireLease(ctx, target, owner, ttl)
}

func (s *prelaunchUnwindStore) ReleaseLease(ctx context.Context, lease engine.SessionLease) error {
	s.mu.Lock()
	s.releaseTargets = append(s.releaseTargets, lease.Target)
	s.mu.Unlock()
	return errors.Join(s.releaseErr, s.engineStoreAdapter.ReleaseLease(ctx, lease))
}

func (s *prelaunchUnwindStore) released() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.releaseTargets...)
}

func assertReleasedTargets(t *testing.T, got []string, want ...string) {
	t.Helper()
	wantSet := make(map[string]int, len(want))
	for _, target := range want {
		wantSet[target]++
	}
	for _, target := range got {
		wantSet[target]--
	}
	for target, remaining := range wantSet {
		if remaining != 0 {
			t.Fatalf("released targets = %v, want %v (target %q delta %d)", got, want, target, remaining)
		}
	}
	if len(got) != len(want) {
		t.Fatalf("released targets = %v, want %v", got, want)
	}
}

func TestThreadPrelaunchFailureJoinsPrimaryAndReleaseErrors(t *testing.T) {
	releaseErr := errors.New("test prelaunch release failed")
	resolveErr := errors.New("test prelaunch resolve failed")
	keyTarget := func(key string) string { return keycodec.Encode("session-key", testNamespace, key) }
	recordTarget := func(id string) string { return keycodec.Encode("session-record", id) }

	tests := []struct {
		name            string
		seed            func(*testing.T, *memory.Store)
		request         engine.SessionRequest
		driver          driver.Driver
		fingerprint     string
		resolveFailAt   int
		resolveErr      error
		wantPrimary     error
		wantReleaseKeys []string
	}{
		{
			name: "resolve after target key acquire",
			request: engine.SessionRequest{
				Namespace: testNamespace, Key: "resolve", Mode: driver.SessionContinueOrStart,
			},
			driver: forkDriver{}, fingerprint: testFingerprint,
			resolveFailAt: 1, resolveErr: resolveErr, wantPrimary: resolveErr,
			wantReleaseKeys: []string{keyTarget("resolve")},
		},
		{
			name: "fingerprint validation after current record acquire",
			seed: func(t *testing.T, store *memory.Store) { seedRecord(t, store, validParent("fingerprint")) },
			request: engine.SessionRequest{
				Namespace: testNamespace, Key: "fingerprint", Mode: driver.SessionContinueOnly,
			},
			driver: forkDriver{}, fingerprint: "changed-fingerprint", wantPrimary: engine.ErrSessionIncompatible,
			wantReleaseKeys: []string{keyTarget("fingerprint"), recordTarget("parent-id")},
		},
		{
			name: "fork parent validation after all parent acquires",
			seed: func(t *testing.T, store *memory.Store) {
				record := validParent("parent")
				record.DriverType = "other-driver"
				seedRecord(t, store, record)
			},
			request: forkRequest("parent", "child-validation"),
			driver:  forkDriver{}, fingerprint: testFingerprint, wantPrimary: engine.ErrSessionIncompatible,
			wantReleaseKeys: []string{
				keyTarget("child-validation"), keyTarget("parent"), recordTarget("parent-id"),
			},
		},
		{
			name: "missing durable checkpoint after reusable plan",
			seed: func(t *testing.T, store *memory.Store) {
				record := validParent("missing-state")
				record.State = nil
				seedRecord(t, store, record)
			},
			request: engine.SessionRequest{
				Namespace: testNamespace, Key: "missing-state", Mode: driver.SessionContinueOrStart,
			},
			driver: forkDriver{}, fingerprint: testFingerprint, wantPrimary: engine.ErrSessionCheckpointMissing,
			wantReleaseKeys: []string{keyTarget("missing-state"), recordTarget("parent-id")},
		},
		{
			name: "codec rejects durable checkpoint after reusable plan",
			seed: func(t *testing.T, store *memory.Store) { seedRecord(t, store, validParent("codec-reject")) },
			request: engine.SessionRequest{
				Namespace: testNamespace, Key: "codec-reject", Mode: driver.SessionContinueOrStart,
			},
			driver: rejectingCodecDriver{}, fingerprint: testFingerprint, wantPrimary: engine.ErrSessionIncompatible,
			wantReleaseKeys: []string{keyTarget("codec-reject"), recordTarget("parent-id")},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			base := memory.NewStore()
			if tc.seed != nil {
				tc.seed(t, base)
			}
			store := &prelaunchUnwindStore{
				engineStoreAdapter: engineStoreAdapter{store: base},
				resolveFailAt:      tc.resolveFailAt,
				resolveErr:         tc.resolveErr,
				releaseErr:         releaseErr,
			}
			plan, err := engine.PrepareThreadSessionForDriver(
				context.Background(), store, tc.request, testIdentity, tc.driver, tc.fingerprint,
			)
			if plan != nil {
				plan.Release()
				t.Fatal("prelaunch failure returned a plan")
			}
			if !errors.Is(err, tc.wantPrimary) {
				t.Fatalf("error = %v, want primary identity %v", err, tc.wantPrimary)
			}
			if !errors.Is(err, releaseErr) {
				t.Fatalf("error = %v, want release failure identity", err)
			}
			assertReleasedTargets(t, store.released(), tc.wantReleaseKeys...)
		})
	}
}

func TestThreadPrelaunchMidAcquireFailureJoinsAllPriorReleaseErrors(t *testing.T) {
	acquireErr := errors.New("test child record acquire failed")
	releaseErr := errors.New("test unwind release failed")
	base := memory.NewStore()
	seedRecord(t, base, validParent("parent"))
	store := &prelaunchUnwindStore{
		engineStoreAdapter: engineStoreAdapter{store: base},
		acquireFailAt:      4,
		acquireErr:         acquireErr,
		releaseErr:         releaseErr,
	}

	plan, err := engine.PrepareThreadSessionForDriver(
		context.Background(), store, forkRequest("parent", "child-acquire"),
		testIdentity, forkDriver{}, testFingerprint,
	)
	if plan != nil {
		plan.Release()
		t.Fatal("mid-acquire failure returned a plan")
	}
	if !errors.Is(err, acquireErr) {
		t.Fatalf("error = %v, want acquire identity", err)
	}
	if !errors.Is(err, releaseErr) {
		t.Fatalf("error = %v, want release identity", err)
	}
	if store.acquireCalls != 4 {
		t.Fatalf("AcquireLease calls = %d, want 4", store.acquireCalls)
	}
	assertReleasedTargets(t, store.released(),
		keycodec.Encode("session-key", testNamespace, "child-acquire"),
		keycodec.Encode("session-key", testNamespace, "parent"),
		keycodec.Encode("session-record", "parent-id"),
	)
}
