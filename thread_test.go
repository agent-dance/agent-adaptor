package adaptor_test

// P2 contract migration: the semantic assertions of the legacy session
// baseline tests (sdk_session_test.go, sdk_start_session_test.go), replayed
// against the v1 Thread surface. Mapping:
//
//	TestSessionStoreRequired                        → TestThreadRequiresStore
//	mode → method matrix (implicit in legacy modes) → TestThreadModeMethodMatrix
//	TestContinueOrStartReuse                        → TestThreadContinueOrStartReuse
//	TestContinueOnlyNotFound                        → TestThreadResumeOnlyNotFound
//	TestStartNewRebindsKeyAndKeepsOldSession        → TestNewThreadArchivesPreviousConversation
//	TestContinueOnlyDetectsIncompatibility          → TestThreadFingerprintGuard
//	TestSessionBusyOnConcurrentKey                  → TestThreadBusyOnConcurrentRuns
//	TestContinueOrStartFallsBackAfterResumeRejected → TestThreadFallsBackAfterResumeRejected
//	TestContinueOnlyKeepsResumeRejectedFailure      → TestThreadResumeOnlyKeepsResumeRejected
//	TestStatefulRunRequiresValidCheckpoint          → TestThreadRunRequiresValidCheckpoint
//	TestHumanDecisionFailureWithoutCheckpoint...    → TestThreadHumanRejectWithoutCheckpointDoesNotPoisonThread
//	  (+ Start() mirror in sdk_start_session_test)  →   (Stream leg of the same test)
//
// The legacy tests stay untouched; these replicate their semantics over
// Agent.Thread / Agent.NewThread / Thread.Fork / ResumeOnly.

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"strings"
	"sync"
	"testing"

	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/engine"
	"github.com/agent-dance/agent-adaptor/memory"
	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/threadstore"
)

// sessionFake extends the programmable fakeDriver with the resume /
// checkpoint behavior of the legacy session baseline fake: fresh runs mint
// "<label>-resume-N" checkpoints, resumed runs echo the incoming state, and
// three switches reproduce the failure scenarios (reject the resume, omit
// the checkpoint, human-reject without checkpoint).
type sessionFake struct {
	*fakeDriver

	mu              sync.Mutex
	counter         int
	rejectResume    bool
	omitCheckpoint  bool
	humanRejectNoCP bool
}

type configuredSessionFake struct {
	*sessionFake
	configFingerprint string
}

type noFingerprintDriver struct{ inner *fakeDriver }

func (d *noFingerprintDriver) Descriptor() driver.Descriptor  { return d.inner.Descriptor() }
func (d *noFingerprintDriver) ValidateConfig(value any) error { return d.inner.ValidateConfig(value) }
func (d *noFingerprintDriver) Run(ctx context.Context, req driver.Request, sink driver.EventSink) (driver.Response, error) {
	return d.inner.Run(ctx, req, sink)
}

func (d *configuredSessionFake) SessionConfigFingerprint() (string, error) {
	return d.configFingerprint, nil
}

func newSessionFake(label string) *sessionFake {
	sf := &sessionFake{fakeDriver: newFakeDriver()}
	sf.runFunc = func(_ context.Context, req driver.Request, _ driver.EventSink) (driver.Response, error) {
		sf.mu.Lock()
		rejectResume, omitCheckpoint, humanReject := sf.rejectResume, sf.omitCheckpoint, sf.humanRejectNoCP
		sf.mu.Unlock()

		if humanReject {
			return driver.Response{
				Output:   label + ":human-reject",
				ExitCode: 1,
				Failure: &driver.RunFailure{
					Code:          driver.FailureReject,
					Message:       "user rejected mid-run",
					HumanDecision: &driver.HumanDecisionFailure{Kind: driver.HumanDecisionQuestion},
				},
			}, nil
		}

		if req.Session != nil && req.Session.State != nil && req.Session.State.ResumeID != "" {
			if rejectResume {
				return driver.Response{}, &engine.ResumeRejectedError{Reason: "stale checkpoint"}
			}
			state := req.Session.State
			copyState := *state
			copyState.Data = maps.Clone(state.Data)
			return driver.Response{
				Output:     label + ":reused:" + state.ResumeID,
				Checkpoint: &driver.Checkpoint{State: &copyState, Valid: true},
			}, nil
		}

		sf.mu.Lock()
		sf.counter++
		n := sf.counter
		sf.mu.Unlock()
		if omitCheckpoint {
			return driver.Response{Output: label + ":created-without-checkpoint"}, nil
		}
		state := &driver.SessionState{
			ResumeID:  fmt.Sprintf("%s-resume-%d", label, n),
			DisplayID: fmt.Sprintf("%s-display-%d", label, n),
		}
		return driver.Response{
			Output:     label + ":created:" + state.ResumeID,
			Checkpoint: &driver.Checkpoint{State: state, Valid: true},
		}, nil
	}
	return sf
}

func (sf *sessionFake) set(field *bool, v bool) {
	sf.mu.Lock()
	*field = v
	sf.mu.Unlock()
}

// activeRecord resolves the active store record for key, failing the test
// when it is missing.
func activeRecord(t *testing.T, store threadstore.Store, key string) *threadstore.Record {
	t.Helper()
	rec, err := store.Resolve(context.Background(), threadstore.Query{Key: key})
	if err != nil {
		t.Fatalf("resolve %q: %v", key, err)
	}
	if rec == nil {
		t.Fatalf("no active record for thread %q", key)
	}
	return rec
}

func TestThreadRequiresStore(t *testing.T) {
	agent := adaptor.New(newSessionFake("s").fakeDriver)
	th := agent.Thread("tenant-1/issue-1")

	if _, err := th.Run(context.Background(), "hi"); !errors.Is(err, adaptor.ErrThreadStoreRequired) {
		t.Fatalf("Run without store: err = %v, want ErrThreadStoreRequired", err)
	}
	// The Stream form travels the same contract (closed events + Result err).
	stream := th.Stream(context.Background(), "hi")
	for range stream.Events() {
	}
	if _, err := stream.Result(); !errors.Is(err, adaptor.ErrThreadStoreRequired) {
		t.Fatalf("Stream without store: err = %v, want ErrThreadStoreRequired", err)
	}
	if _, err := th.Checkpoint(context.Background()); !errors.Is(err, adaptor.ErrThreadStoreRequired) {
		t.Fatalf("Checkpoint without store: err = %v, want ErrThreadStoreRequired", err)
	}
}

// TestThreadModeMethodMatrix pins the §2.4 mapping of the four stateful
// session modes onto Thread methods, asserted at the driver SPI boundary
// (the mode the driver actually sees) and at the store.
func TestThreadModeMethodMatrix(t *testing.T) {
	ctx := context.Background()

	t.Run("Thread is continue_or_start", func(t *testing.T) {
		fake := newSessionFake("cos")
		agent := adaptor.New(fake.fakeDriver, adaptor.WithThreadStore(memory.NewStore()))
		if _, err := agent.Thread("k").Run(ctx, "p"); err != nil {
			t.Fatalf("run: %v", err)
		}
		sess := fake.request(t, 0).Session
		if sess == nil || sess.Mode != driver.SessionContinueOrStart {
			t.Fatalf("driver session = %+v, want mode continue_or_start", sess)
		}
		if sess.State != nil {
			t.Fatalf("first run carried state %+v, want none", sess.State)
		}
		if sess.EngineSessionID == "" {
			t.Fatal("driver session missing engine session id")
		}
	})

	t.Run("Thread(ResumeOnly) is continue_only", func(t *testing.T) {
		fake := newSessionFake("co")
		store := memory.NewStore()
		agent := adaptor.New(fake.fakeDriver, adaptor.WithThreadStore(store))
		if _, err := agent.Thread("k").Run(ctx, "seed"); err != nil {
			t.Fatalf("seed: %v", err)
		}
		res, err := agent.Thread("k", adaptor.ResumeOnly()).Run(ctx, "reply")
		if err != nil {
			t.Fatalf("resume-only run: %v", err)
		}
		sess := fake.request(t, 1).Session
		if sess == nil || sess.Mode != driver.SessionContinueOnly {
			t.Fatalf("driver session = %+v, want mode continue_only", sess)
		}
		if sess.State == nil || sess.State.ResumeID != "co-resume-1" {
			t.Fatalf("resume state = %+v, want co-resume-1", sess.State)
		}
		if res.Text != "co:reused:co-resume-1" {
			t.Fatalf("res.Text = %q, want the resumed output", res.Text)
		}
	})

	t.Run("NewThread is start_new then continue_or_start", func(t *testing.T) {
		fake := newSessionFake("sn")
		store := memory.NewStore()
		agent := adaptor.New(fake.fakeDriver, adaptor.WithThreadStore(store))
		if _, err := agent.Thread("k").Run(ctx, "seed"); err != nil {
			t.Fatalf("seed: %v", err)
		}
		th := agent.NewThread("k")
		if _, err := th.Run(ctx, "fresh"); err != nil {
			t.Fatalf("start-new run: %v", err)
		}
		sess := fake.request(t, 1).Session
		if sess == nil || sess.Mode != driver.SessionStartNew {
			t.Fatalf("driver session = %+v, want mode start_new", sess)
		}
		if sess.State != nil {
			t.Fatalf("start_new carried state %+v, want none", sess.State)
		}
		// Once established, the same handle continues its own conversation.
		if _, err := th.Run(ctx, "follow-up"); err != nil {
			t.Fatalf("follow-up run: %v", err)
		}
		sess = fake.request(t, 2).Session
		if sess == nil || sess.Mode != driver.SessionContinueOrStart {
			t.Fatalf("driver session = %+v, want mode continue_or_start after establishment", sess)
		}
		if sess.State == nil || sess.State.ResumeID != "sn-resume-2" {
			t.Fatalf("follow-up state = %+v, want the fresh conversation sn-resume-2", sess.State)
		}
	})

	t.Run("Fork is fork then continue_or_start", func(t *testing.T) {
		fake := newSessionFake("fk")
		store := memory.NewStore()
		agent := adaptor.New(fake.fakeDriver, adaptor.WithThreadStore(store))
		parent := agent.Thread("k")
		if _, err := parent.Run(ctx, "seed"); err != nil {
			t.Fatalf("seed: %v", err)
		}
		parentRec := activeRecord(t, store, "k")

		branch := parent.Fork("k/alt")
		if _, err := branch.Run(ctx, "branch off"); err != nil {
			t.Fatalf("fork run: %v", err)
		}
		sess := fake.request(t, 1).Session
		if sess == nil || sess.Mode != driver.SessionFork {
			t.Fatalf("driver session = %+v, want mode fork", sess)
		}
		if sess.State == nil || sess.State.ResumeID != "fk-resume-1" {
			t.Fatalf("fork state = %+v, want the parent checkpoint fk-resume-1", sess.State)
		}
		if sess.EngineSessionID == parentRec.ID {
			t.Fatal("fork reused the parent engine session id, want a fresh one")
		}

		// Fork boundary: the parent conversation stays intact and active.
		parentAfter := activeRecord(t, store, "k")
		if parentAfter.ID != parentRec.ID || parentAfter.Status != threadstore.StatusActive {
			t.Fatalf("parent record changed after fork: %+v", parentAfter)
		}
		branchRec := activeRecord(t, store, "k/alt")
		if branchRec.ID == parentRec.ID {
			t.Fatal("fork stored under the parent record id, want its own")
		}
		if branchRec.State == nil || branchRec.State.ResumeID != "fk-resume-1" {
			t.Fatalf("fork record state = %+v, want the inherited checkpoint", branchRec.State)
		}

		// Established fork continues independently.
		if _, err := branch.Run(ctx, "continue branch"); err != nil {
			t.Fatalf("fork follow-up: %v", err)
		}
		sess = fake.request(t, 2).Session
		if sess == nil || sess.Mode != driver.SessionContinueOrStart {
			t.Fatalf("driver session = %+v, want continue_or_start after fork establishment", sess)
		}
	})
}

func TestThreadContinueOrStartReuse(t *testing.T) {
	ctx := context.Background()
	fake := newSessionFake("reuse")
	store := memory.NewStore()
	agent := adaptor.New(fake.fakeDriver, adaptor.WithThreadStore(store))

	th := agent.Thread("tenant-1/issue-9")
	res1, err := th.Run(ctx, "first")
	if err != nil {
		t.Fatalf("first run: %v", err)
	}
	if res1.Text != "reuse:created:reuse-resume-1" {
		t.Fatalf("res1.Text = %q, want a fresh conversation", res1.Text)
	}
	rec1 := activeRecord(t, store, "tenant-1/issue-9")
	if rec1.State == nil || rec1.State.ResumeID != "reuse-resume-1" {
		t.Fatalf("stored state = %+v, want reuse-resume-1", rec1.State)
	}

	// Same handle: continues.
	res2, err := th.Run(ctx, "second")
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if res2.Text != "reuse:reused:reuse-resume-1" {
		t.Fatalf("res2.Text = %q, want the resumed conversation", res2.Text)
	}

	// A brand-new handle for the same key: state lives in the store, not
	// in the handle, so it continues too (cross-request/process shape).
	res3, err := agent.Thread("tenant-1/issue-9").Run(ctx, "third")
	if err != nil {
		t.Fatalf("third run: %v", err)
	}
	if res3.Text != "reuse:reused:reuse-resume-1" {
		t.Fatalf("res3.Text = %q, want the resumed conversation", res3.Text)
	}

	// The engine session identity is stable across the reuse runs.
	first, second := fake.request(t, 0).Session, fake.request(t, 1).Session
	if first.EngineSessionID != second.EngineSessionID {
		t.Fatalf("engine session id changed on reuse: %q → %q", first.EngineSessionID, second.EngineSessionID)
	}
	if rec2 := activeRecord(t, store, "tenant-1/issue-9"); rec2.ID != rec1.ID {
		t.Fatalf("active record id changed on reuse: %q → %q", rec1.ID, rec2.ID)
	}
}

func TestThreadResumeOnlyNotFound(t *testing.T) {
	ctx := context.Background()
	fake := newSessionFake("nf")
	agent := adaptor.New(fake.fakeDriver, adaptor.WithThreadStore(memory.NewStore()))

	if _, err := agent.Thread("missing", adaptor.ResumeOnly()).Run(ctx, "p"); !errors.Is(err, adaptor.ErrThreadNotFound) {
		t.Fatalf("resume-only on missing thread: err = %v, want ErrThreadNotFound", err)
	}
	// Fork parent missing behaves the same way.
	if _, err := agent.Thread("missing").Fork("missing/alt").Run(ctx, "p"); !errors.Is(err, adaptor.ErrThreadNotFound) {
		t.Fatalf("fork from missing parent: err = %v, want ErrThreadNotFound", err)
	}
	if got := fake.runCount(); got != 0 {
		t.Fatalf("driver ran %d time(s), want 0", got)
	}
}

func TestThreadForkRejectsExistingTargetWithoutMutation(t *testing.T) {
	ctx := context.Background()
	fake := newSessionFake("conflict")
	store := memory.NewStore()
	agent := adaptor.New(fake.fakeDriver, adaptor.WithThreadStore(store))

	if _, err := agent.Thread("parent").Run(ctx, "seed parent"); err != nil {
		t.Fatalf("seed parent: %v", err)
	}
	if _, err := agent.Thread("target").Run(ctx, "seed target"); err != nil {
		t.Fatalf("seed target: %v", err)
	}
	parentBefore := activeRecord(t, store, "parent")
	targetBefore := activeRecord(t, store, "target")

	if _, err := agent.Thread("parent").Fork("target").Run(ctx, "must conflict"); !errors.Is(err, adaptor.ErrThreadAlreadyExists) {
		t.Fatalf("fork existing target: err=%v, want ErrThreadAlreadyExists", err)
	}
	if got := fake.runCount(); got != 2 {
		t.Fatalf("driver ran %d times, want two seeds only", got)
	}
	parentAfter := activeRecord(t, store, "parent")
	targetAfter := activeRecord(t, store, "target")
	if parentAfter.ID != parentBefore.ID || parentAfter.State.ResumeID != parentBefore.State.ResumeID {
		t.Fatalf("parent mutated by conflicting fork: before=%+v after=%+v", parentBefore, parentAfter)
	}
	if targetAfter.ID != targetBefore.ID || targetAfter.State.ResumeID != targetBefore.State.ResumeID {
		t.Fatalf("target mutated by conflicting fork: before=%+v after=%+v", targetBefore, targetAfter)
	}
}

func TestNewThreadArchivesPreviousConversation(t *testing.T) {
	ctx := context.Background()
	fake := newSessionFake("arch")
	store := memory.NewStore()
	agent := adaptor.New(fake.fakeDriver, adaptor.WithThreadStore(store))

	if _, err := agent.Thread("k").Run(ctx, "seed"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	oldRec := activeRecord(t, store, "k")

	res, err := agent.NewThread("k").Run(ctx, "start over")
	if err != nil {
		t.Fatalf("start-new run: %v", err)
	}
	if res.Text != "arch:created:arch-resume-2" {
		t.Fatalf("res.Text = %q, want a brand-new conversation", res.Text)
	}

	newRec := activeRecord(t, store, "k")
	if newRec.ID == oldRec.ID {
		t.Fatal("key still bound to the old record after NewThread")
	}
	// The old conversation is archived, not deleted: gone from the active
	// view, still resolvable for audit.
	gone, err := store.Resolve(ctx, threadstore.Query{ID: oldRec.ID})
	if err != nil {
		t.Fatalf("resolve archived (active view): %v", err)
	}
	if gone != nil {
		t.Fatalf("old record still active: %+v", gone)
	}
	archived, err := store.Resolve(ctx, threadstore.Query{ID: oldRec.ID, IncludeArchived: true})
	if err != nil {
		t.Fatalf("resolve archived: %v", err)
	}
	if archived == nil || archived.Status != threadstore.StatusArchived {
		t.Fatalf("old record = %+v, want archived", archived)
	}
}

// TestThreadFingerprintGuard replays the fingerprint match/mismatch matrix:
// matching configuration resumes; a changed configuration (model here) is
// rejected for resume-only threads and silently rolls to a fresh archived-
// old conversation for continue-or-start threads.
func TestThreadFingerprintGuard(t *testing.T) {
	ctx := context.Background()
	fake := newSessionFake("fp")
	store := memory.NewStore()
	agent := adaptor.New(fake.fakeDriver, adaptor.WithThreadStore(store))

	if _, err := agent.Thread("k").Run(ctx, "seed", adaptor.WithModel("m1")); err != nil {
		t.Fatalf("seed: %v", err)
	}
	rec1 := activeRecord(t, store, "k")

	// Match: same configuration resumes.
	res, err := agent.Thread("k", adaptor.ResumeOnly()).Run(ctx, "same model", adaptor.WithModel("m1"))
	if err != nil {
		t.Fatalf("matching resume: %v", err)
	}
	if res.Text != "fp:reused:fp-resume-1" {
		t.Fatalf("res.Text = %q, want the resumed conversation", res.Text)
	}

	// Mismatch + resume-only: refused, nothing changes.
	if _, err := agent.Thread("k", adaptor.ResumeOnly()).Run(ctx, "new model", adaptor.WithModel("m2")); !errors.Is(err, adaptor.ErrThreadIncompatible) {
		t.Fatalf("incompatible resume-only: err = %v, want ErrThreadIncompatible", err)
	}
	if rec := activeRecord(t, store, "k"); rec.ID != rec1.ID {
		t.Fatalf("record changed after refused resume: %q → %q", rec1.ID, rec.ID)
	}

	// Mismatch + continue-or-start: fresh conversation, old one archived.
	res, err = agent.Thread("k").Run(ctx, "new model", adaptor.WithModel("m2"))
	if err != nil {
		t.Fatalf("incompatible continue-or-start: %v", err)
	}
	if !strings.HasPrefix(res.Text, "fp:created:") {
		t.Fatalf("res.Text = %q, want a fresh conversation", res.Text)
	}
	sess := fake.lastRequest(t).Session
	if sess == nil || sess.State != nil {
		t.Fatalf("incompatible rollover carried state %+v, want none", sess)
	}
	rec2 := activeRecord(t, store, "k")
	if rec2.ID == rec1.ID {
		t.Fatal("key still bound to the incompatible record")
	}
	archived, err := store.Resolve(ctx, threadstore.Query{ID: rec1.ID, IncludeArchived: true})
	if err != nil {
		t.Fatalf("resolve archived: %v", err)
	}
	if archived == nil || archived.Status != threadstore.StatusArchived {
		t.Fatalf("old record = %+v, want archived", archived)
	}
}

func TestThreadFingerprintIncludesDriverConstructionConfig(t *testing.T) {
	store := memory.NewStore()
	first := &configuredSessionFake{sessionFake: newSessionFake("cfg-a"), configFingerprint: "configured/v1:a"}
	firstAgent := adaptor.New(first, adaptor.WithThreadStore(store))
	if _, err := firstAgent.Thread("configured").Run(context.Background(), "seed"); err != nil {
		t.Fatalf("seed: %v", err)
	}

	second := &configuredSessionFake{sessionFake: newSessionFake("cfg-b"), configFingerprint: "configured/v1:b"}
	secondAgent := adaptor.New(second, adaptor.WithThreadStore(store))
	if _, err := secondAgent.Thread("configured", adaptor.ResumeOnly()).Run(context.Background(), "resume"); !errors.Is(err, adaptor.ErrThreadIncompatible) {
		t.Fatalf("changed construction config: err=%v, want ErrThreadIncompatible", err)
	}
	if second.runCount() != 0 {
		t.Fatalf("incompatible configured driver ran %d times", second.runCount())
	}
}

func TestThreadRejectsDriverWithoutStableConfigFingerprint(t *testing.T) {
	inner := newFakeDriver()
	driverWithoutFingerprint := &noFingerprintDriver{inner: inner}
	agent := adaptor.New(driverWithoutFingerprint, adaptor.WithThreadStore(memory.NewStore()))
	if _, err := agent.Thread("no-config-fingerprint").Run(context.Background(), "go"); !errors.Is(err, adaptor.ErrThreadIncompatible) {
		t.Fatalf("missing config fingerprint: err=%v, want ErrThreadIncompatible", err)
	}
	if inner.runCount() != 0 {
		t.Fatalf("driver ran %d times despite missing stable config fingerprint", inner.runCount())
	}

	empty := &configuredSessionFake{sessionFake: newSessionFake("empty-config"), configFingerprint: "  "}
	agent = adaptor.New(empty, adaptor.WithThreadStore(memory.NewStore()))
	if _, err := agent.Thread("empty-config-fingerprint").Run(context.Background(), "go"); !errors.Is(err, adaptor.ErrThreadIncompatible) {
		t.Fatalf("empty config fingerprint: err=%v, want ErrThreadIncompatible", err)
	}
	if empty.runCount() != 0 {
		t.Fatalf("driver ran %d times despite empty stable config fingerprint", empty.runCount())
	}
}

func TestThreadFingerprintUsesResolvedWorkspace(t *testing.T) {
	store := memory.NewStore()
	fake := newSessionFake("workspace")
	manager := &fakeWorkspaceManager{
		log: &callLog{},
		lease: adaptor.WorkspaceLease{
			ID: "workspace-1", CWD: "C:/resolved/repo", Fingerprint: "resolved-workspace/v1",
			Mode: driver.WorkspaceModeShared, StrategyType: driver.WorkspaceStrategyProjectPrimary,
		},
	}
	first := adaptor.New(fake.fakeDriver,
		adaptor.WithThreadStore(store), adaptor.WithWorkspaceManager(manager), adaptor.WithWorkspace("C:/raw/input-a"),
	)
	if _, err := first.Thread("workspace").Run(context.Background(), "seed"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	second := adaptor.New(fake.fakeDriver,
		adaptor.WithThreadStore(store), adaptor.WithWorkspaceManager(manager), adaptor.WithWorkspace("C:/raw/input-b"),
	)
	if _, err := second.Thread("workspace", adaptor.ResumeOnly()).Run(context.Background(), "same resolved workspace"); err != nil {
		t.Fatalf("raw input changed but resolved lease stayed identical: %v", err)
	}

	manager.lease = adaptor.WorkspaceLease{
		ID: "workspace-2", CWD: "C:/resolved/other", Fingerprint: "resolved-workspace/v2",
		Mode: driver.WorkspaceModeShared, StrategyType: driver.WorkspaceStrategyProjectPrimary,
	}
	if _, err := second.Thread("workspace", adaptor.ResumeOnly()).Run(context.Background(), "different resolved workspace"); !errors.Is(err, adaptor.ErrThreadIncompatible) {
		t.Fatalf("changed resolved workspace: err=%v, want ErrThreadIncompatible", err)
	}
}

func TestThreadFingerprintIncludesResolvedRuntimeAttachments(t *testing.T) {
	store := memory.NewStore()
	fake := newSessionFake("runtime")
	log := &callLog{}
	endpoint := "http://127.0.0.1:4101"
	manager := &fakeServiceManager{
		log: log,
		ensure: func(context.Context, adaptor.ServiceRequest) ([]adaptor.ServiceRef, error) {
			return []adaptor.ServiceRef{{ID: "sidecar", Name: "sidecar", URL: endpoint, Status: driver.RuntimeServiceRunning}}, nil
		},
	}
	agent := adaptor.New(fake.fakeDriver,
		adaptor.WithThreadStore(store), adaptor.WithServiceManager(manager), adaptor.WithServices(adaptor.ServiceSpec{ID: "sidecar"}),
	)
	if _, err := agent.Thread("runtime").Run(context.Background(), "seed"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	endpoint = "http://127.0.0.1:4202"
	if _, err := agent.Thread("runtime", adaptor.ResumeOnly()).Run(context.Background(), "resume"); !errors.Is(err, adaptor.ErrThreadIncompatible) {
		t.Fatalf("changed resolved runtime attachment: err=%v, want ErrThreadIncompatible", err)
	}
}

func TestThreadRuntimeSecretRotationDoesNotBreakResume(t *testing.T) {
	store := memory.NewStore()
	fake := newSessionFake("runtime-secret")
	log := &callLog{}
	endpoint := "http://127.0.0.1:4303"
	secret := "sk-first-runtime-secret"
	manager := &fakeServiceManager{
		log: log,
		ensure: func(context.Context, adaptor.ServiceRequest) ([]adaptor.ServiceRef, error) {
			return []adaptor.ServiceRef{{
				ID:        "sidecar",
				Name:      "sidecar",
				URL:       endpoint,
				Status:    driver.RuntimeServiceRunning,
				Lifecycle: driver.RuntimeLifecycleShared,
				SecretEnv: []driver.EnvBinding{{Name: "RUNTIME_TOKEN", Value: secret}},
			}}, nil
		},
	}
	agent := adaptor.New(fake.fakeDriver,
		adaptor.WithThreadStore(store),
		adaptor.WithServiceManager(manager),
		adaptor.WithServices(adaptor.ServiceSpec{ID: "sidecar"}),
	)

	if _, err := agent.Thread("runtime-secret").Run(context.Background(), "seed"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	seedRecord := activeRecord(t, store, "runtime-secret")
	if strings.Contains(seedRecord.Fingerprint, secret) || strings.Contains(seedRecord.CompatibilityFingerprint, secret) {
		t.Fatalf("durable fingerprint leaked the first runtime secret: %+v", seedRecord)
	}

	secret = "sk-rotated-runtime-secret"
	result, err := agent.Thread("runtime-secret", adaptor.ResumeOnly()).Run(context.Background(), "resume")
	if err != nil {
		t.Fatalf("resume after secret rotation: %v", err)
	}
	if result.Text != "runtime-secret:reused:runtime-secret-resume-1" {
		t.Fatalf("resume text = %q, want the existing provider session", result.Text)
	}
	if got := fake.runCount(); got != 2 {
		t.Fatalf("driver ran %d times, want seed plus resumed turn", got)
	}
	resumeRequest := fake.request(t, 1)
	if len(resumeRequest.Runtime.SecretEnv) != 1 ||
		resumeRequest.Runtime.SecretEnv[0].Name != "RUNTIME_TOKEN" ||
		resumeRequest.Runtime.SecretEnv[0].Value != secret {
		t.Fatalf("resumed driver did not receive exactly one RUNTIME_TOKEN binding with the rotated credential")
	}
	resumedRecord := activeRecord(t, store, "runtime-secret")
	if resumedRecord.Fingerprint != seedRecord.Fingerprint || resumedRecord.CompatibilityFingerprint != seedRecord.CompatibilityFingerprint {
		t.Fatalf("secret rotation changed compatibility fingerprint: before=%+v after=%+v", seedRecord, resumedRecord)
	}
	if strings.Contains(resumedRecord.Fingerprint, secret) || strings.Contains(resumedRecord.CompatibilityFingerprint, secret) {
		t.Fatalf("durable fingerprint leaked the rotated runtime secret: %+v", resumedRecord)
	}

	// A real compatibility change still fails, and the diagnostic must not
	// serialize the credential that accompanied the incompatible attachment.
	endpoint = "http://127.0.0.1:4404"
	secret = "sk-incompatible-runtime-secret"
	_, err = agent.Thread("runtime-secret", adaptor.ResumeOnly()).Run(context.Background(), "changed endpoint")
	if !errors.Is(err, adaptor.ErrThreadIncompatible) {
		t.Fatalf("changed endpoint: err=%v, want ErrThreadIncompatible", err)
	}
	if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "sk-first-runtime-secret") || strings.Contains(err.Error(), "sk-rotated-runtime-secret") {
		t.Fatalf("thread compatibility error leaked a runtime secret: %v", err)
	}
	if got := fake.runCount(); got != 2 {
		t.Fatalf("incompatible runtime reached driver: run count=%d, want 2", got)
	}
}

// TestThreadBusyOnConcurrentRuns proves the lease guard: while one run
// holds the thread, a second run on the same key fails fast with
// ErrThreadBusy. Synchronization is channel-based (no sleeps).
func TestThreadBusyOnConcurrentRuns(t *testing.T) {
	ctx := context.Background()
	fake := newFakeDriver()
	started := make(chan struct{})
	release := make(chan struct{})
	var startOnce sync.Once
	fake.runFunc = func(runCtx context.Context, _ driver.Request, _ driver.EventSink) (driver.Response, error) {
		startOnce.Do(func() { close(started) })
		select {
		case <-release:
		case <-runCtx.Done():
			return driver.Response{}, runCtx.Err()
		}
		state := &driver.SessionState{ResumeID: "busy-resume-1"}
		return driver.Response{Output: "done", Checkpoint: &driver.Checkpoint{State: state, Valid: true}}, nil
	}
	agent := adaptor.New(fake, adaptor.WithThreadStore(memory.NewStore()))

	stream := agent.Thread("k").Stream(ctx, "long turn")
	<-started

	if _, err := agent.Thread("k").Run(ctx, "concurrent turn"); !errors.Is(err, adaptor.ErrThreadBusy) {
		t.Fatalf("concurrent run: err = %v, want ErrThreadBusy", err)
	}
	if got := fake.runCount(); got != 1 {
		t.Fatalf("driver ran %d time(s), want 1 (loser must fail before the driver)", got)
	}

	close(release)
	for range stream.Events() {
	}
	if _, err := stream.Result(); err != nil {
		t.Fatalf("winner run: %v", err)
	}

	// The lease is released with the run: the same key works again.
	if _, err := agent.Thread("k").Run(ctx, "after"); err != nil {
		t.Fatalf("run after release: %v", err)
	}
}

func TestThreadFallsBackAfterResumeRejected(t *testing.T) {
	ctx := context.Background()
	fake := newSessionFake("fb")
	store := memory.NewStore()
	agent := adaptor.New(fake.fakeDriver, adaptor.WithThreadStore(store))

	if _, err := agent.Thread("k").Run(ctx, "seed"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	rec1 := activeRecord(t, store, "k")

	fake.set(&fake.rejectResume, true)
	res, err := agent.Thread("k").Run(ctx, "resume attempt")
	if err != nil {
		t.Fatalf("continue-or-start after rejection: %v", err)
	}
	if res.Text != "fb:created:fb-resume-2" {
		t.Fatalf("res.Text = %q, want the fresh fallback conversation", res.Text)
	}

	// Three driver calls: seed, rejected resume, fresh retry.
	if got := fake.runCount(); got != 3 {
		t.Fatalf("driver ran %d time(s), want 3", got)
	}
	attempt, retry := fake.request(t, 1).Session, fake.request(t, 2).Session
	if attempt.State == nil || attempt.State.ResumeID != "fb-resume-1" {
		t.Fatalf("rejected attempt state = %+v, want fb-resume-1", attempt.State)
	}
	if retry.State != nil {
		t.Fatalf("fresh retry carried state %+v, want none", retry.State)
	}
	if retry.EngineSessionID == attempt.EngineSessionID {
		t.Fatal("fresh retry kept the rejected engine session id")
	}

	// The key rebinds to the fresh conversation; the rejected one archives.
	rec2 := activeRecord(t, store, "k")
	if rec2.ID == rec1.ID {
		t.Fatal("key still bound to the rejected record")
	}
	archived, err := store.Resolve(ctx, threadstore.Query{ID: rec1.ID, IncludeArchived: true})
	if err != nil {
		t.Fatalf("resolve archived: %v", err)
	}
	if archived == nil || archived.Status != threadstore.StatusArchived {
		t.Fatalf("rejected record = %+v, want archived", archived)
	}
}

func TestThreadResumeOnlyKeepsResumeRejected(t *testing.T) {
	ctx := context.Background()
	fake := newSessionFake("keep")
	store := memory.NewStore()
	agent := adaptor.New(fake.fakeDriver, adaptor.WithThreadStore(store))

	if _, err := agent.Thread("k").Run(ctx, "seed"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	rec1 := activeRecord(t, store, "k")

	fake.set(&fake.rejectResume, true)
	_, err := agent.Thread("k", adaptor.ResumeOnly()).Run(ctx, "resume attempt")
	if !errors.Is(err, adaptor.ErrResumeRejected) {
		t.Fatalf("resume-only rejection: err = %v, want ErrResumeRejected", err)
	}
	// No silent fallback: exactly one resume attempt, mapping unchanged.
	if got := fake.runCount(); got != 2 {
		t.Fatalf("driver ran %d time(s), want 2 (no retry)", got)
	}
	if rec := activeRecord(t, store, "k"); rec.ID != rec1.ID {
		t.Fatalf("record changed after kept rejection: %q → %q", rec1.ID, rec.ID)
	}
}

func TestThreadRunRequiresValidCheckpoint(t *testing.T) {
	ctx := context.Background()
	fake := newSessionFake("nocp")
	store := memory.NewStore()
	agent := adaptor.New(fake.fakeDriver, adaptor.WithThreadStore(store))

	fake.set(&fake.omitCheckpoint, true)
	if _, err := agent.Thread("k").Run(ctx, "p"); !errors.Is(err, adaptor.ErrThreadCheckpointMissing) {
		t.Fatalf("run without checkpoint: err = %v, want ErrThreadCheckpointMissing", err)
	}
	rec, err := store.Resolve(ctx, threadstore.Query{Key: "k", IncludeArchived: true})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if rec != nil {
		t.Fatalf("record persisted without checkpoint: %+v", rec)
	}
}

func TestThreadRejectsCheckpointWithoutResumeID(t *testing.T) {
	fake := newFakeDriver()
	fake.runFunc = func(context.Context, driver.Request, driver.EventSink) (driver.Response, error) {
		return driver.Response{
			Output: "nominal success",
			Checkpoint: &driver.Checkpoint{
				Valid: true,
				State: &driver.SessionState{DisplayID: "display-only", Data: map[string]string{"nested_session": "not-authoritative"}},
			},
		}, nil
	}
	store := memory.NewStore()
	agent := adaptor.New(fake, adaptor.WithThreadStore(store))
	if _, err := agent.Thread("empty-resume").Run(context.Background(), "go"); !errors.Is(err, adaptor.ErrThreadCheckpointMissing) {
		t.Fatalf("empty resume ID: err=%v, want ErrThreadCheckpointMissing", err)
	}
	if rec, err := store.Resolve(context.Background(), threadstore.Query{Key: "empty-resume", IncludeArchived: true}); err != nil || rec != nil {
		t.Fatalf("invalid checkpoint persisted: rec=%+v err=%v", rec, err)
	}
}

// TestThreadHumanRejectWithoutCheckpointDoesNotPoisonThread replays the
// legacy three-run scenario on both Runner forms: a healthy seed, a
// human-rejected run without checkpoint (business failure, stored state
// untouched), then a healthy run that resumes the original conversation.
func TestThreadHumanRejectWithoutCheckpointDoesNotPoisonThread(t *testing.T) {
	ctx := context.Background()
	fake := newSessionFake("hr")
	store := memory.NewStore()
	agent := adaptor.New(fake.fakeDriver, adaptor.WithThreadStore(store))

	if _, err := agent.Thread("k").Run(ctx, "seed"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	rec1 := activeRecord(t, store, "k")

	assertRejected := func(t *testing.T, err error) {
		t.Helper()
		var runErr *adaptor.RunError
		if !errors.As(err, &runErr) {
			t.Fatalf("err = %v (%T), want *RunError", err, err)
		}
		if !errors.Is(err, adaptor.ErrApprovalDenied) {
			t.Fatalf("err = %v, want ErrApprovalDenied", err)
		}
		if errors.Is(err, adaptor.ErrThreadCheckpointMissing) {
			t.Fatal("business rejection leaked the checkpoint-missing infrastructure error")
		}
		if runErr.Result == nil || runErr.Result.Text != "hr:human-reject" {
			t.Fatalf("RunError.Result = %+v, want the failed run's output", runErr.Result)
		}
	}

	// Run form.
	fake.set(&fake.humanRejectNoCP, true)
	_, err := agent.Thread("k").Run(ctx, "please deploy")
	assertRejected(t, err)

	// Stream form (legacy Start() mirror).
	stream := agent.Thread("k").Stream(ctx, "please deploy")
	for range stream.Events() {
	}
	_, err = stream.Result()
	assertRejected(t, err)

	// The stored conversation never moved.
	if rec := activeRecord(t, store, "k"); rec.ID != rec1.ID || rec.State == nil || rec.State.ResumeID != "hr-resume-1" {
		t.Fatalf("record changed after tolerated rejections: %+v", rec)
	}

	// And a healthy run resumes the original conversation.
	fake.set(&fake.humanRejectNoCP, false)
	res, err := agent.Thread("k").Run(ctx, "try again")
	if err != nil {
		t.Fatalf("healthy run: %v", err)
	}
	if res.Text != "hr:reused:hr-resume-1" {
		t.Fatalf("res.Text = %q, want the original conversation resumed", res.Text)
	}
}
