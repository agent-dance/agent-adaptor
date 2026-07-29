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
	"github.com/agent-dance/agent-adaptor/threadstore"
)

const (
	testNamespace   = "thread"
	testDriverType  = "fork-test"
	testFingerprint = "fp:v1"
	testCodecName   = "codec:v1"
)

var testIdentity = driver.AgentIdentity{ID: "agent", TenantID: "tenant", ProfileID: "profile"}

type engineStoreAdapter struct {
	store       threadstore.Store
	finalizeErr error
}

type releaseTestStore struct {
	engineStoreAdapter
	gate chan struct{}
	err  error
}

type combinedReleaseStore struct {
	engineStoreAdapter
	keyTarget string
	gate      chan struct{}
	entered   chan string
	errorDone chan struct{}
	err       error
	once      sync.Once
}

type nthAcquireErrorStore struct {
	engineStoreAdapter
	failAt int
	err    error
	calls  int
}

func (s *nthAcquireErrorStore) AcquireLease(ctx context.Context, target, owner string, ttl time.Duration) (engine.SessionLease, error) {
	s.calls++
	if s.calls == s.failAt {
		return engine.SessionLease{}, s.err
	}
	return s.engineStoreAdapter.AcquireLease(ctx, target, owner, ttl)
}

func (s *combinedReleaseStore) ReleaseLease(ctx context.Context, lease engine.SessionLease) error {
	s.entered <- lease.Target
	if lease.Target != s.keyTarget {
		<-s.gate // deliberately ignore ctx for the first (record) release
		return s.engineStoreAdapter.ReleaseLease(ctx, lease)
	}
	err := s.engineStoreAdapter.ReleaseLease(ctx, lease)
	s.once.Do(func() { close(s.errorDone) })
	if err != nil {
		return errors.Join(s.err, err)
	}
	return s.err
}

func (s releaseTestStore) ReleaseLease(ctx context.Context, lease engine.SessionLease) error {
	if s.gate != nil {
		<-s.gate // deliberately models a broken Store that ignores ctx
	}
	if s.err != nil {
		return s.err
	}
	return s.engineStoreAdapter.ReleaseLease(ctx, lease)
}

func (s engineStoreAdapter) Resolve(ctx context.Context, q engine.SessionQuery) (*engine.SessionRecord, error) {
	record, err := s.store.Resolve(ctx, threadstore.Query{ID: q.ID, Key: q.Key, IncludeArchived: q.IncludeArchived})
	if err != nil || record == nil {
		return nil, err
	}
	return &engine.SessionRecord{
		ID: record.ID, Namespace: testNamespace, Key: record.Key, Status: engine.SessionStatus(record.Status),
		DriverType: record.DriverType, Agent: record.Agent, Fingerprint: record.Fingerprint,
		CompatibilityFingerprint: record.CompatibilityFingerprint, SessionCodec: record.SessionCodec,
		DriverState: record.State, CreatedAt: record.CreatedAt, UpdatedAt: record.UpdatedAt,
	}, nil
}

func (s engineStoreAdapter) Finalize(ctx context.Context, req engine.SessionFinalizeRequest) error {
	if s.finalizeErr != nil {
		return s.finalizeErr
	}
	leases := make([]threadstore.Lease, len(req.HeldLeases))
	for i, lease := range req.HeldLeases {
		leases[i] = threadstore.Lease{Target: lease.Target, Owner: lease.Owner, Token: lease.Token}
	}
	return s.store.Finalize(ctx, threadstore.FinalizeRequest{
		Record: threadstore.Record{
			ID: req.Record.ID, Key: req.Record.Key, Status: threadstore.Status(req.Record.Status),
			DriverType: req.Record.DriverType, Agent: req.Record.Agent, Fingerprint: req.Record.Fingerprint,
			CompatibilityFingerprint: req.Record.CompatibilityFingerprint, SessionCodec: req.Record.SessionCodec,
			State: req.Record.DriverState, CreatedAt: req.Record.CreatedAt, UpdatedAt: req.Record.UpdatedAt,
		},
		PreviousID: req.PreviousID, Key: req.Key, HeldLeases: leases,
		ArchiveOld: req.ArchiveOld, RebindActive: req.RebindActive, RequireKeyAbsent: req.RequireKeyAbsent,
	})
}

func (s engineStoreAdapter) AcquireLease(ctx context.Context, target, owner string, ttl time.Duration) (engine.SessionLease, error) {
	lease, err := s.store.AcquireLease(ctx, target, owner, ttl)
	if errors.Is(err, threadstore.ErrBusy) {
		return engine.SessionLease{}, &engine.SessionBusyError{Target: target}
	}
	return engine.SessionLease{Target: lease.Target, Owner: lease.Owner, Token: lease.Token}, err
}

func (s engineStoreAdapter) RenewLease(ctx context.Context, lease engine.SessionLease, ttl time.Duration) error {
	return s.store.RenewLease(ctx, threadstore.Lease{Target: lease.Target, Owner: lease.Owner, Token: lease.Token}, ttl)
}

func (s engineStoreAdapter) ReleaseLease(ctx context.Context, lease engine.SessionLease) error {
	return s.store.ReleaseLease(ctx, threadstore.Lease{Target: lease.Target, Owner: lease.Owner, Token: lease.Token})
}

type namedCodec string

func (c namedCodec) Name() string { return string(c) }
func (namedCodec) ToParams(state *driver.SessionState) driver.SessionParams {
	if state == nil {
		return driver.SessionParams{}
	}
	return driver.SessionParams{ResumeID: state.ResumeID, DisplayID: state.DisplayID, Values: state.Data}
}
func (namedCodec) FromParams(params driver.SessionParams) *driver.SessionState {
	if params.ResumeID == "" && params.DisplayID == "" && len(params.Values) == 0 {
		return nil
	}
	return &driver.SessionState{ResumeID: params.ResumeID, DisplayID: params.DisplayID, Data: params.Values}
}
func (namedCodec) GuardFingerprint(driver.SessionParams) string { return "guard" }

type forkDriver struct{}

func (forkDriver) Descriptor() driver.Descriptor {
	return driver.Descriptor{
		Type:     testDriverType,
		Sessions: driver.SessionCapability{SupportsResume: true},
	}
}
func (forkDriver) ValidateConfig(any) error { return nil }
func (forkDriver) Run(context.Context, driver.Request, driver.EventSink) (driver.Response, error) {
	return driver.Response{}, nil
}
func (forkDriver) SessionCodec() driver.SessionCodec { return namedCodec(testCodecName) }

type rejectingCodec struct{ namedCodec }

func (rejectingCodec) FromParams(driver.SessionParams) *driver.SessionState { return nil }

type rejectingCodecDriver struct{ forkDriver }

func (rejectingCodecDriver) SessionCodec() driver.SessionCodec {
	return rejectingCodec{namedCodec(testCodecName)}
}

func seedRecord(t *testing.T, store *memory.Store, record threadstore.Record) {
	t.Helper()
	lease, err := store.AcquireLease(context.Background(), "seed:"+record.ID, "seed-owner:"+record.ID, time.Minute)
	if err != nil {
		t.Fatalf("seed acquire: %v", err)
	}
	if err := store.Finalize(context.Background(), threadstore.FinalizeRequest{
		Record: record, Key: record.Key, HeldLeases: []threadstore.Lease{lease}, RebindActive: record.Status == threadstore.StatusActive,
	}); err != nil {
		t.Fatalf("seed finalize: %v", err)
	}
}

func validParent(key string) threadstore.Record {
	now := time.Now().UTC()
	return threadstore.Record{
		ID: "parent-id", Key: key, Status: threadstore.StatusActive, DriverType: testDriverType,
		Agent: testIdentity, Fingerprint: testFingerprint, CompatibilityFingerprint: testFingerprint,
		SessionCodec: testCodecName, State: &driver.SessionState{ResumeID: "provider-parent"},
		CreatedAt: now, UpdatedAt: now,
	}
}

func forkRequest(parentKey, childKey string) engine.SessionRequest {
	return engine.SessionRequest{
		Namespace: testNamespace, Key: childKey, Mode: driver.SessionFork,
		ForkFromKey: parentKey, SessionCodec: testCodecName,
	}
}

func TestStructuredForkPersistsChildWithoutMutatingParent(t *testing.T) {
	ctx := context.Background()
	store := memory.NewStore()
	parentKey := "父\x00:thread"
	childKey := "子/线程\x00"
	parent := validParent(parentKey)
	seedRecord(t, store, parent)

	plan, err := engine.PrepareThreadSessionForDriver(ctx, engineStoreAdapter{store: store}, forkRequest(parentKey, childKey), testIdentity, forkDriver{}, testFingerprint)
	if err != nil {
		t.Fatalf("prepare fork: %v", err)
	}
	defer plan.Release()
	if session := plan.DriverSession(forkDriver{}); session == nil || session.State == nil || session.State.ResumeID != "provider-parent" || session.Mode != driver.SessionFork {
		t.Fatalf("driver session = %#v", session)
	}
	ref, err := plan.Persist(ctx, testIdentity, forkDriver{}, testFingerprint, &driver.Checkpoint{
		Valid: true, State: &driver.SessionState{ResumeID: "provider-child"},
	})
	if err != nil {
		t.Fatalf("persist fork: %v", err)
	}
	if ref.Key != childKey || ref.PreviousID != "" {
		t.Fatalf("fork ref = %#v", ref)
	}
	gotParent, _ := store.Resolve(ctx, threadstore.Query{Key: parentKey})
	if gotParent == nil || gotParent.ID != parent.ID || gotParent.Status != threadstore.StatusActive || gotParent.State.ResumeID != "provider-parent" {
		t.Fatalf("parent mutated: %#v", gotParent)
	}
	child, _ := store.Resolve(ctx, threadstore.Query{Key: childKey})
	if child == nil || child.ID != ref.ID || child.Key != childKey || child.State.ResumeID != "provider-child" || child.SessionCodec != testCodecName {
		t.Fatalf("child = %#v", child)
	}
}

func TestStructuredForkRejectsExistingTargetWithoutMutation(t *testing.T) {
	ctx := context.Background()
	store := memory.NewStore()
	parent := validParent("parent")
	target := validParent("target")
	target.ID = "target-id"
	target.State = &driver.SessionState{ResumeID: "target-state"}
	seedRecord(t, store, parent)
	seedRecord(t, store, target)

	_, err := engine.PrepareThreadSessionForDriver(ctx, engineStoreAdapter{store: store}, forkRequest(parent.Key, target.Key), testIdentity, forkDriver{}, testFingerprint)
	if !errors.Is(err, engine.ErrThreadAlreadyExists) {
		t.Fatalf("prepare existing target: err=%v, want ErrThreadAlreadyExists", err)
	}
	gotParent, _ := store.Resolve(ctx, threadstore.Query{Key: parent.Key})
	gotTarget, _ := store.Resolve(ctx, threadstore.Query{Key: target.Key})
	if gotParent == nil || gotParent.ID != parent.ID || gotTarget == nil || gotTarget.ID != target.ID || gotTarget.State.ResumeID != "target-state" {
		t.Fatalf("records mutated: parent=%#v target=%#v", gotParent, gotTarget)
	}
}

func TestStructuredForkParentCompatibilityMatrix(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*threadstore.Record)
		want   error
	}{
		{name: "missing checkpoint", mutate: func(r *threadstore.Record) { r.State = nil }, want: engine.ErrSessionCheckpointMissing},
		{name: "driver", mutate: func(r *threadstore.Record) { r.DriverType = "other" }, want: engine.ErrSessionIncompatible},
		{name: "identity", mutate: func(r *threadstore.Record) { r.Agent.ProfileID = "other" }, want: engine.ErrSessionIncompatible},
		{name: "fingerprint", mutate: func(r *threadstore.Record) { r.CompatibilityFingerprint = "other" }, want: engine.ErrSessionIncompatible},
		{name: "missing fingerprint", mutate: func(r *threadstore.Record) { r.CompatibilityFingerprint = "" }, want: engine.ErrSessionIncompatible},
		{name: "codec", mutate: func(r *threadstore.Record) { r.SessionCodec = "other" }, want: engine.ErrSessionIncompatible},
		{name: "missing codec", mutate: func(r *threadstore.Record) { r.SessionCodec = "" }, want: engine.ErrSessionIncompatible},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := memory.NewStore()
			parent := validParent("parent")
			tc.mutate(&parent)
			seedRecord(t, store, parent)
			_, err := engine.PrepareThreadSessionForDriver(context.Background(), engineStoreAdapter{store: store}, forkRequest(parent.Key, "child"), testIdentity, forkDriver{}, testFingerprint)
			if !errors.Is(err, tc.want) {
				t.Fatalf("err=%v, want %v", err, tc.want)
			}
			got, _ := store.Resolve(context.Background(), threadstore.Query{Key: parent.Key})
			if got == nil || got.Status != threadstore.StatusActive || got.ID != parent.ID {
				t.Fatalf("parent mutated: %#v", got)
			}
		})
	}
}

func TestReusedThreadRequiresUsableCheckpointAndMatchingCodec(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*threadstore.Record)
		want   error
	}{
		{name: "nil state", mutate: func(r *threadstore.Record) { r.State = nil }, want: engine.ErrSessionCheckpointMissing},
		{name: "empty resume id", mutate: func(r *threadstore.Record) {
			r.State = &driver.SessionState{DisplayID: "display-only"}
		}, want: engine.ErrSessionIncompatible},
		{name: "codec mismatch", mutate: func(r *threadstore.Record) { r.SessionCodec = "other/codec" }, want: engine.ErrSessionIncompatible},
	}
	for _, tc := range tests {
		t.Run(tc.name+"/continue_only", func(t *testing.T) {
			store := memory.NewStore()
			record := validParent("thread-key")
			tc.mutate(&record)
			seedRecord(t, store, record)
			_, err := engine.PrepareThreadSessionForDriver(context.Background(), engineStoreAdapter{store: store}, engine.SessionRequest{
				Namespace: testNamespace, Key: record.Key, Mode: driver.SessionContinueOnly,
			}, testIdentity, forkDriver{}, testFingerprint)
			if !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
			active, _ := store.Resolve(context.Background(), threadstore.Query{Key: record.Key})
			if active == nil || active.ID != record.ID || active.Status != threadstore.StatusActive {
				t.Fatalf("rejected resume mutated record: %+v", active)
			}
		})

		t.Run(tc.name+"/continue_or_start", func(t *testing.T) {
			store := memory.NewStore()
			record := validParent("thread-key")
			tc.mutate(&record)
			seedRecord(t, store, record)
			plan, err := engine.PrepareThreadSessionForDriver(context.Background(), engineStoreAdapter{store: store}, engine.SessionRequest{
				Namespace: testNamespace, Key: record.Key, Mode: driver.SessionContinueOrStart,
			}, testIdentity, forkDriver{}, testFingerprint)
			if !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
			if plan != nil {
				plan.Release()
				t.Fatal("invalid durable checkpoint returned a runnable plan")
			}
			active, _ := store.Resolve(context.Background(), threadstore.Query{Key: record.Key})
			if active == nil || active.ID != record.ID || active.Status != threadstore.StatusActive {
				t.Fatalf("rejected invalid checkpoint mutated record: %+v", active)
			}
		})
	}
}

func TestSessionAcquireLeasePreservesBackendErrorAtEveryAcquirePosition(t *testing.T) {
	backendErr := errors.New("test session store: acquire backend unavailable")
	tests := []struct {
		name        string
		request     engine.SessionRequest
		fingerprint string
		failAt      int
		seed        func(*testing.T, *memory.Store)
	}{
		{
			name: "thread key", request: engine.SessionRequest{Namespace: testNamespace, Key: "new", Mode: driver.SessionContinueOrStart},
			fingerprint: testFingerprint, failAt: 1,
		},
		{
			name: "continue current record", request: engine.SessionRequest{Namespace: testNamespace, Key: "existing", Mode: driver.SessionContinueOnly},
			fingerprint: testFingerprint, failAt: 2,
			seed: func(t *testing.T, store *memory.Store) { seedRecord(t, store, validParent("existing")) },
		},
		{
			name: "continue new record", request: engine.SessionRequest{Namespace: testNamespace, Key: "new", Mode: driver.SessionContinueOrStart},
			fingerprint: testFingerprint, failAt: 2,
		},
		{
			name: "incompatible replacement record", request: engine.SessionRequest{Namespace: testNamespace, Key: "existing", Mode: driver.SessionContinueOrStart},
			fingerprint: testFingerprint, failAt: 3,
			seed: func(t *testing.T, store *memory.Store) {
				record := validParent("existing")
				record.CompatibilityFingerprint = "old-fingerprint"
				seedRecord(t, store, record)
			},
		},
		{
			name: "fork parent key", request: forkRequest("parent", "child"),
			fingerprint: testFingerprint, failAt: 2,
			seed: func(t *testing.T, store *memory.Store) { seedRecord(t, store, validParent("parent")) },
		},
		{
			name: "fork parent key resolved by id", request: engine.SessionRequest{
				Namespace: testNamespace, Key: "child", Mode: driver.SessionFork, ForkFrom: "parent-id",
			},
			fingerprint: testFingerprint, failAt: 2,
			seed: func(t *testing.T, store *memory.Store) { seedRecord(t, store, validParent("parent")) },
		},
		{
			name: "fork parent record", request: forkRequest("parent", "child"),
			fingerprint: testFingerprint, failAt: 3,
			seed: func(t *testing.T, store *memory.Store) { seedRecord(t, store, validParent("parent")) },
		},
		{
			name: "fork child record", request: forkRequest("parent", "child"),
			fingerprint: testFingerprint, failAt: 4,
			seed: func(t *testing.T, store *memory.Store) { seedRecord(t, store, validParent("parent")) },
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			memoryStore := memory.NewStore()
			if tc.seed != nil {
				tc.seed(t, memoryStore)
			}
			store := &nthAcquireErrorStore{
				engineStoreAdapter: engineStoreAdapter{store: memoryStore},
				failAt:             tc.failAt,
				err:                backendErr,
			}
			plan, err := engine.PrepareThreadSessionForDriver(
				context.Background(), store, tc.request, testIdentity, forkDriver{}, tc.fingerprint,
			)
			if plan != nil {
				plan.Release()
				t.Fatal("failed acquisition returned a plan")
			}
			if !errors.Is(err, backendErr) {
				t.Fatalf("error = %v, want backend identity", err)
			}
			if errors.Is(err, engine.ErrSessionBusy) {
				t.Fatalf("backend error was misclassified as busy: %v", err)
			}
			if store.calls != tc.failAt {
				t.Fatalf("AcquireLease calls = %d, want %d", store.calls, tc.failAt)
			}
		})
	}
}

func TestPrepareFreshPreservesAcquireBackendError(t *testing.T) {
	backendErr := errors.New("test session store: fresh acquire unavailable")
	memoryStore := memory.NewStore()
	seedRecord(t, memoryStore, validParent("existing"))
	store := &nthAcquireErrorStore{
		engineStoreAdapter: engineStoreAdapter{store: memoryStore},
		failAt:             3,
		err:                backendErr,
	}
	plan, err := engine.PrepareThreadSessionForDriver(context.Background(), store, engine.SessionRequest{
		Namespace: testNamespace, Key: "existing", Mode: driver.SessionContinueOrStart,
	}, testIdentity, forkDriver{}, testFingerprint)
	if err != nil {
		t.Fatalf("prepare reused plan: %v", err)
	}
	defer plan.Release()
	if err := plan.PrepareFresh(context.Background(), testDriverType, testFingerprint); !errors.Is(err, backendErr) {
		t.Fatalf("PrepareFresh error = %v, want backend identity", err)
	} else if errors.Is(err, engine.ErrSessionBusy) {
		t.Fatalf("PrepareFresh backend error was misclassified as busy: %v", err)
	}
	if store.calls != 3 {
		t.Fatalf("AcquireLease calls = %d, want 3", store.calls)
	}
}

func TestStructuredForkRejectsCheckpointTheNamedCodecCannotNormalize(t *testing.T) {
	store := memory.NewStore()
	parent := validParent("parent")
	seedRecord(t, store, parent)
	_, err := engine.PrepareThreadSessionForDriver(
		context.Background(), engineStoreAdapter{store: store}, forkRequest(parent.Key, "child"),
		testIdentity, rejectingCodecDriver{}, testFingerprint,
	)
	if !errors.Is(err, engine.ErrSessionIncompatible) {
		t.Fatalf("err=%v, want ErrSessionIncompatible", err)
	}
	if child, _ := store.Resolve(context.Background(), threadstore.Query{Key: "child"}); child != nil {
		t.Fatalf("invalid checkpoint created child: %#v", child)
	}
}

func TestStructuredForkParentBusyAndTargetLeaseSerialize(t *testing.T) {
	ctx := context.Background()
	store := memory.NewStore()
	parent := validParent("parent")
	seedRecord(t, store, parent)

	busy, err := store.AcquireLease(ctx, keycodec.Encode("session-record", parent.ID), "other-owner", time.Minute)
	if err != nil {
		t.Fatalf("occupy parent: %v", err)
	}
	_, err = engine.PrepareThreadSessionForDriver(ctx, engineStoreAdapter{store: store}, forkRequest(parent.Key, "child"), testIdentity, forkDriver{}, testFingerprint)
	if !errors.Is(err, engine.ErrSessionBusy) {
		t.Fatalf("busy parent: err=%v, want ErrSessionBusy", err)
	}
	_ = store.ReleaseLease(ctx, busy)
	busyKey, err := store.AcquireLease(ctx, keycodec.Encode("session-key", testNamespace, parent.Key), "other-owner", time.Minute)
	if err != nil {
		t.Fatalf("occupy parent key: %v", err)
	}
	_, err = engine.PrepareThreadSessionForDriver(ctx, engineStoreAdapter{store: store}, forkRequest(parent.Key, "child"), testIdentity, forkDriver{}, testFingerprint)
	if !errors.Is(err, engine.ErrSessionBusy) {
		t.Fatalf("busy parent key: err=%v, want ErrSessionBusy", err)
	}
	_ = store.ReleaseLease(ctx, busyKey)

	first, err := engine.PrepareThreadSessionForDriver(ctx, engineStoreAdapter{store: store}, forkRequest(parent.Key, "child"), testIdentity, forkDriver{}, testFingerprint)
	if err != nil {
		t.Fatalf("first prepare: %v", err)
	}
	defer first.Release()
	_, err = engine.PrepareThreadSessionForDriver(ctx, engineStoreAdapter{store: store}, forkRequest(parent.Key, "child"), testIdentity, forkDriver{}, testFingerprint)
	if !errors.Is(err, engine.ErrSessionBusy) {
		t.Fatalf("same target concurrent prepare: err=%v, want ErrSessionBusy", err)
	}
}

func TestStructuredForkFinalizeCASRejectsRacingTarget(t *testing.T) {
	ctx := context.Background()
	store := memory.NewStore()
	parent := validParent("parent")
	seedRecord(t, store, parent)
	plan, err := engine.PrepareThreadSessionForDriver(ctx, engineStoreAdapter{store: store}, forkRequest(parent.Key, "target"), testIdentity, forkDriver{}, testFingerprint)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	defer plan.Release()

	// Model a stale/non-cooperating backend writer that ignores the target
	// lease. Finalize's RequireKeyAbsent compare-and-set must still prevent
	// this prepared fork from overwriting that newly active record.
	racer := validParent("target")
	racer.ID = "racing-target"
	racer.State = &driver.SessionState{ResumeID: "racing-state"}
	if err := store.Finalize(ctx, threadstore.FinalizeRequest{Record: racer, Key: racer.Key, RebindActive: true}); err != nil {
		t.Fatalf("install racing target: %v", err)
	}
	_, err = plan.Persist(ctx, testIdentity, forkDriver{}, testFingerprint, &driver.Checkpoint{
		Valid: true, State: &driver.SessionState{ResumeID: "fork-state"},
	})
	if !errors.Is(err, engine.ErrThreadAlreadyExists) || !errors.Is(err, threadstore.ErrAlreadyExists) {
		t.Fatalf("persist: err=%v, want engine and store already-exists sentinels", err)
	}
	gotTarget, _ := store.Resolve(ctx, threadstore.Query{Key: racer.Key})
	gotParent, _ := store.Resolve(ctx, threadstore.Query{Key: parent.Key})
	if gotTarget == nil || gotTarget.ID != racer.ID || gotTarget.State.ResumeID != "racing-state" {
		t.Fatalf("racing target overwritten: %#v", gotTarget)
	}
	if gotParent == nil || gotParent.ID != parent.ID || gotParent.Status != threadstore.StatusActive {
		t.Fatalf("parent mutated: %#v", gotParent)
	}
}

func TestStructuredForkPersistFailureIsNonMutating(t *testing.T) {
	ctx := context.Background()
	store := memory.NewStore()
	parent := validParent("parent")
	seedRecord(t, store, parent)

	finalizeFailure := errors.New("durable store unavailable")
	plan, err := engine.PrepareThreadSessionForDriver(ctx, engineStoreAdapter{store: store, finalizeErr: finalizeFailure}, forkRequest(parent.Key, "child"), testIdentity, forkDriver{}, testFingerprint)
	if err != nil {
		t.Fatalf("prepare fork: %v", err)
	}
	_, err = plan.Persist(ctx, testIdentity, forkDriver{}, testFingerprint, &driver.Checkpoint{Valid: true, State: &driver.SessionState{ResumeID: "child"}})
	plan.Release()
	if !errors.Is(err, finalizeFailure) {
		t.Fatalf("persist err=%v, want injected failure", err)
	}
	if child, _ := store.Resolve(ctx, threadstore.Query{Key: "child"}); child != nil {
		t.Fatalf("child persisted after failure: %#v", child)
	}
	if got, _ := store.Resolve(ctx, threadstore.Query{Key: parent.Key}); got == nil || got.Status != threadstore.StatusActive {
		t.Fatalf("parent changed after failure: %#v", got)
	}
}

func TestStructuredForkRejectsIncompleteSelectorsBeforeAcquiringResources(t *testing.T) {
	store := memory.NewStore()
	cases := []engine.SessionRequest{
		{Namespace: testNamespace, Key: "child", Mode: driver.SessionFork, SessionCodec: testCodecName},
		{Namespace: testNamespace, Key: "child", Mode: driver.SessionFork, ForkFrom: "id", ForkFromKey: "key", SessionCodec: testCodecName},
		{Namespace: testNamespace, Key: "child", Mode: driver.SessionFork, ForkFromKey: "parent"},
		{Mode: driver.SessionFork, ForkFromKey: "parent", SessionCodec: testCodecName},
	}
	for i, req := range cases {
		if _, err := engine.PrepareThreadSession(context.Background(), engineStoreAdapter{store: store}, req, testIdentity, testDriverType, testFingerprint); !errors.Is(err, engine.ErrInvalidSessionRequest) {
			t.Fatalf("case %d: err=%v, want ErrInvalidSessionRequest", i, err)
		}
	}
	// Invalid requests acquired no target lease: a valid request can take it.
	parent := validParent("parent")
	seedRecord(t, store, parent)
	plan, err := engine.PrepareThreadSessionForDriver(context.Background(), engineStoreAdapter{store: store}, forkRequest(parent.Key, "child"), testIdentity, forkDriver{}, testFingerprint)
	if err != nil {
		t.Fatalf("valid request after invalid requests: %v", err)
	}
	plan.Release()
}

func TestSessionSelectorsAreValidatedBeforeStoreRequirements(t *testing.T) {
	cases := []engine.SessionRequest{
		{Key: "orphan"},
		{Namespace: "orphan"},
		{ID: "id", Namespace: "ns", Key: "key", Mode: driver.SessionContinueOnly},
		{ForkFromKey: "parent"},
		{Namespace: "ns", Key: "key", Mode: driver.SessionMode("unknown")},
	}
	for i, req := range cases {
		_, err := engine.PrepareThreadSession(context.Background(), nil, req, testIdentity, testDriverType, testFingerprint)
		if !errors.Is(err, engine.ErrInvalidSessionRequest) {
			t.Fatalf("case %d: err=%v, want ErrInvalidSessionRequest before store access", i, err)
		}
		if errors.Is(err, engine.ErrSessionStoreRequired) {
			t.Fatalf("case %d: invalid selector was misclassified as missing store: %v", i, err)
		}
	}
}

func TestThreadSessionReleaseContextIsObservableAndBounded(t *testing.T) {
	ctx := context.Background()
	base := memory.NewStore()
	releaseFailure := errors.New("release failed")
	plan, err := engine.PrepareThreadSessionForDriver(ctx, releaseTestStore{
		engineStoreAdapter: engineStoreAdapter{store: base}, err: releaseFailure,
	}, engine.SessionRequest{Namespace: testNamespace, Key: "release-error", Mode: driver.SessionContinueOrStart}, testIdentity, forkDriver{}, testFingerprint)
	if err != nil {
		t.Fatalf("prepare error case: %v", err)
	}
	if err := plan.ReleaseContext(ctx); !errors.Is(err, releaseFailure) {
		t.Fatalf("ReleaseContext err=%v, want store failure", err)
	}

	gate := make(chan struct{})
	plan, err = engine.PrepareThreadSessionForDriver(ctx, releaseTestStore{
		engineStoreAdapter: engineStoreAdapter{store: base}, gate: gate,
	}, engine.SessionRequest{Namespace: testNamespace, Key: "release-timeout", Mode: driver.SessionContinueOrStart}, testIdentity, forkDriver{}, testFingerprint)
	if err != nil {
		t.Fatalf("prepare timeout case: %v", err)
	}
	releaseCtx, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
	defer cancel()
	err = plan.ReleaseContext(releaseCtx)
	close(gate)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("ReleaseContext err=%v, want deadline exceeded", err)
	}
}

func TestThreadSessionReleaseAttemptsEveryLeaseAndAggregatesBeforeTimeout(t *testing.T) {
	ctx := context.Background()
	key := "release-combined"
	base := memory.NewStore()
	immediateFailure := errors.New("key release failed")
	gate := make(chan struct{})
	store := &combinedReleaseStore{
		engineStoreAdapter: engineStoreAdapter{store: base},
		keyTarget:          keycodec.Encode("session-key", testNamespace, key),
		gate:               gate,
		entered:            make(chan string, 2),
		errorDone:          make(chan struct{}),
		err:                immediateFailure,
	}
	plan, err := engine.PrepareThreadSessionForDriver(ctx, store,
		engine.SessionRequest{Namespace: testNamespace, Key: key, Mode: driver.SessionContinueOrStart},
		testIdentity, forkDriver{}, testFingerprint)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}

	releaseCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- plan.ReleaseContext(releaseCtx) }()

	seen := map[string]bool{}
	for len(seen) < 2 {
		select {
		case target := <-store.entered:
			seen[target] = true
		case <-time.After(time.Second):
			close(gate)
			cancel()
			t.Fatal("not every held lease release hook was attempted")
		}
	}
	if !seen[store.keyTarget] {
		close(gate)
		cancel()
		t.Fatalf("key lease release was not attempted: %v", seen)
	}
	<-store.errorDone
	cancel()

	select {
	case err := <-done:
		close(gate)
		if !errors.Is(err, immediateFailure) {
			t.Fatalf("ReleaseContext err=%v, want immediate release failure", err)
		}
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("ReleaseContext err=%v, want cancellation timeout for hung release", err)
		}
	case <-time.After(time.Second):
		close(gate)
		t.Fatal("ReleaseContext remained blocked behind a context-ignoring lease hook")
	}
}
