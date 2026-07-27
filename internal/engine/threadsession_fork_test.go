package engine_test

import (
	"context"
	"errors"
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

func (forkDriver) Descriptor() driver.Descriptor { return driver.Descriptor{Type: testDriverType} }
func (forkDriver) ValidateConfig(any) error      { return nil }
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

func TestStructuredForkPersistFailureAndStartNewMissingCheckpointAreNonMutating(t *testing.T) {
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

	newPlan, err := engine.PrepareThreadSessionForDriver(ctx, engineStoreAdapter{store: store}, engine.SessionRequest{
		Namespace: testNamespace, Key: parent.Key, Mode: driver.SessionStartNew,
	}, testIdentity, forkDriver{}, testFingerprint)
	if err != nil {
		t.Fatalf("prepare start-new: %v", err)
	}
	_, err = newPlan.Persist(ctx, testIdentity, forkDriver{}, testFingerprint, nil)
	newPlan.Release()
	if !errors.Is(err, engine.ErrSessionCheckpointMissing) {
		t.Fatalf("missing checkpoint: err=%v", err)
	}
	if got, _ := store.Resolve(ctx, threadstore.Query{Key: parent.Key}); got == nil || got.ID != parent.ID || got.Status != threadstore.StatusActive {
		t.Fatalf("start-new failure changed old record: %#v", got)
	}
}

func TestStartNewArchivesAndRebindsOnlyAfterValidCheckpoint(t *testing.T) {
	ctx := context.Background()
	store := memory.NewStore()
	old := validParent("same-raw-key\x00一")
	seedRecord(t, store, old)

	plan, err := engine.PrepareThreadSessionForDriver(ctx, engineStoreAdapter{store: store}, engine.SessionRequest{
		Namespace: testNamespace, Key: old.Key, Mode: driver.SessionStartNew,
	}, testIdentity, forkDriver{}, testFingerprint)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	ref, err := plan.Persist(ctx, testIdentity, forkDriver{}, testFingerprint, &driver.Checkpoint{
		Valid: true, State: &driver.SessionState{ResumeID: "new-provider-state"},
	})
	plan.Release()
	if err != nil {
		t.Fatalf("persist: %v", err)
	}
	active, _ := store.Resolve(ctx, threadstore.Query{Key: old.Key})
	archived, _ := store.Resolve(ctx, threadstore.Query{ID: old.ID, IncludeArchived: true})
	if active == nil || active.ID != ref.ID || active.Key != old.Key || active.State.ResumeID != "new-provider-state" {
		t.Fatalf("active = %#v", active)
	}
	if archived == nil || archived.Status != threadstore.StatusArchived {
		t.Fatalf("old record = %#v, want archived", archived)
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
