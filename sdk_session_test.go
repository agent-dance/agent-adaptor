package agentadaptor_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	agentadaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/memory"
)

type fakeConfig struct {
	Label string
}

type fakeDriver struct {
	mu                sync.Mutex
	counter           int
	rejectResume      bool
	omitCheckpoint    bool
	blockPrompt       string
	blockCh           chan struct{}
	startedCh         chan struct{}
	supportedModels   []agentadaptor.ModelInfo
	lastSkillSyncWant []string
	lastSkills        agentadaptor.SkillPayload
}

func (d *fakeDriver) Descriptor() agentadaptor.DriverDescriptor {
	return agentadaptor.DriverDescriptor{
		Type:        "fake",
		DisplayName: "Fake",
		Sessions:    agentadaptor.SessionCapability{SupportsResume: true},
		Skills:      agentadaptor.SkillCapability{Supported: true, Mode: agentadaptor.SkillSyncEphemeral},
	}
}

func (d *fakeDriver) ValidateConfig(cfg any) error {
	switch cfg.(type) {
	case fakeConfig, *fakeConfig:
		return nil
	default:
		return fmt.Errorf("unexpected config type %T", cfg)
	}
}

func (d *fakeDriver) CheckEnvironment(_ context.Context, _ any) (agentadaptor.EnvironmentReport, error) {
	return agentadaptor.EnvironmentReport{
		DriverType: d.Descriptor().Type,
		Healthy:    true,
	}, nil
}

func (d *fakeDriver) ListModels(_ context.Context, _ any) ([]agentadaptor.ModelInfo, error) {
	if len(d.supportedModels) == 0 {
		return []agentadaptor.ModelInfo{{ID: "fake-model", Label: "fake-model"}}, nil
	}
	return append([]agentadaptor.ModelInfo(nil), d.supportedModels...), nil
}

func (d *fakeDriver) ListSkills(_ context.Context, _ any, payload agentadaptor.SkillPayload) (agentadaptor.SkillSnapshot, error) {
	return agentadaptor.SkillSnapshot{
		DriverType: d.Descriptor().Type,
		Supported:  true,
		Mode:       agentadaptor.SkillSyncEphemeral,
		Desired:    append([]string(nil), payload.Requested...),
		Resolved:   append([]agentadaptor.Skill(nil), payload.Resolved...),
	}, nil
}

func (d *fakeDriver) SyncSkills(_ context.Context, _ any, payload agentadaptor.SkillPayload, desired []string) (agentadaptor.SkillSnapshot, error) {
	d.lastSkillSyncWant = append([]string(nil), desired...)
	return agentadaptor.SkillSnapshot{
		DriverType: d.Descriptor().Type,
		Supported:  true,
		Mode:       agentadaptor.SkillSyncEphemeral,
		Desired:    append([]string(nil), desired...),
		Resolved:   append([]agentadaptor.Skill(nil), payload.Resolved...),
	}, nil
}

func (d *fakeDriver) Run(ctx context.Context, req agentadaptor.DriverRunRequest, sink agentadaptor.EventSink) (agentadaptor.DriverRunResult, error) {
	d.lastSkills = req.Skills
	if d.startedCh != nil {
		select {
		case d.startedCh <- struct{}{}:
		default:
		}
	}
	if d.blockCh != nil && req.Prompt == d.blockPrompt {
		select {
		case <-ctx.Done():
			return agentadaptor.DriverRunResult{}, ctx.Err()
		case <-d.blockCh:
		}
	}

	cfg := readFakeConfig(req.Config)

	if req.Session != nil && req.Session.State != nil && req.Session.State.ResumeID != "" {
		if d.rejectResume {
			return agentadaptor.DriverRunResult{}, &agentadaptor.ResumeRejectedError{Reason: "resume rejected for test"}
		}
		_ = sink.Emit(agentadaptor.RunEvent{Type: agentadaptor.RunEventSystem, Text: "reused"})
		return agentadaptor.DriverRunResult{
			Output:   cfg.Label + ":reused:" + req.Session.State.ResumeID,
			ExitCode: 0,
			Checkpoint: &agentadaptor.DriverCheckpoint{
				State: req.Session.State,
				Valid: true,
			},
		}, nil
	}

	d.mu.Lock()
	d.counter++
	next := d.counter
	d.mu.Unlock()

	if d.omitCheckpoint {
		return agentadaptor.DriverRunResult{
			Output:   cfg.Label + ":created-without-checkpoint",
			ExitCode: 0,
		}, nil
	}

	state := &agentadaptor.DriverSessionState{
		ResumeID:  fmt.Sprintf("%s-driver-session-%d", cfg.Label, next),
		DisplayID: fmt.Sprintf("%s-display-%d", cfg.Label, next),
	}
	return agentadaptor.DriverRunResult{
		Output:   cfg.Label + ":created:" + state.ResumeID,
		ExitCode: 0,
		Checkpoint: &agentadaptor.DriverCheckpoint{
			State: state,
			Valid: true,
		},
	}, nil
}

func newSDK(
	store agentadaptor.SessionStore,
	defaultBinding agentadaptor.AgentBinding,
	named map[string]agentadaptor.AgentBinding,
) agentadaptor.SDK {
	opts := []agentadaptor.Option{
		agentadaptor.WithDefaultAgent(defaultBinding),
	}
	if store != nil {
		opts = append(opts, agentadaptor.WithSessionStore(store))
	}
	for name, binding := range named {
		opts = append(opts, agentadaptor.WithAgent(name, binding))
	}
	return agentadaptor.New(opts...)
}

func fakeBinding(label string, driver *fakeDriver, opts ...agentadaptor.AgentOption) agentadaptor.AgentBinding {
	return agentadaptor.Bind(driver, fakeConfig{Label: label}, opts...)
}

func readFakeConfig(cfg any) fakeConfig {
	switch typed := cfg.(type) {
	case fakeConfig:
		return typed
	case *fakeConfig:
		if typed != nil {
			return *typed
		}
	}
	return fakeConfig{}
}

func TestBuildReturnsErrorWithoutDefaultAgent(t *testing.T) {
	_, err := agentadaptor.Build()
	if !errors.Is(err, agentadaptor.ErrDefaultAgentMissing) {
		t.Fatalf("expected ErrDefaultAgentMissing, got %v", err)
	}
}

func TestBindTypedPreservesConcreteConfig(t *testing.T) {
	driver := &fakeDriver{}
	binding := agentadaptor.BindTyped(driver, fakeConfig{Label: "typed"})

	if binding.TypedConfig().Label != "typed" {
		t.Fatalf("expected typed config label to round-trip, got %#v", binding.TypedConfig())
	}
	if readFakeConfig(binding.Config()).Label != "typed" {
		t.Fatalf("expected generic config path to round-trip, got %#v", binding.Config())
	}
}

func TestSDKRunUsesDefaultAgentBinding(t *testing.T) {
	sdk := newSDK(nil, fakeBinding("default", &fakeDriver{}), nil)

	result, err := sdk.Run(context.Background(), "hello")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.Output != "default:created:default-driver-session-1" {
		t.Fatalf("unexpected output: %q", result.Output)
	}
}

func TestNamedAgentUsesConfiguredBinding(t *testing.T) {
	sdk := newSDK(
		nil,
		fakeBinding("default", &fakeDriver{}),
		map[string]agentadaptor.AgentBinding{
			"review": fakeBinding("review", &fakeDriver{}),
		},
	)

	runner, err := sdk.Agent("review")
	if err != nil {
		t.Fatalf("agent runner: %v", err)
	}

	result, err := runner.Run(context.Background(), "review it")
	if err != nil {
		t.Fatalf("run review: %v", err)
	}
	if result.Output != "review:created:review-driver-session-1" {
		t.Fatalf("unexpected output: %q", result.Output)
	}
}

func TestSessionStoreRequired(t *testing.T) {
	sdk := newSDK(nil, fakeBinding("default", &fakeDriver{}), nil)

	_, err := sdk.Run(context.Background(), "hello", agentadaptor.WithSessionKey("company", "issue-1"))
	if !errors.Is(err, agentadaptor.ErrSessionStoreRequired) {
		t.Fatalf("expected ErrSessionStoreRequired, got %v", err)
	}
}

func TestContinueOrStartReuse(t *testing.T) {
	store := memory.NewSessionStore()
	sdk := newSDK(store, fakeBinding("default", &fakeDriver{}), nil)

	first, err := sdk.Run(context.Background(), "hello", agentadaptor.WithSessionKey("company", "issue-1"))
	if err != nil {
		t.Fatalf("first run: %v", err)
	}
	if first.Session == nil || !first.Session.Created || first.Session.Reused {
		t.Fatalf("expected created session, got %#v", first.Session)
	}

	second, err := sdk.Run(context.Background(), "again", agentadaptor.WithSessionKey("company", "issue-1"))
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if second.Session == nil || !second.Session.Reused || second.Session.ID != first.Session.ID {
		t.Fatalf("expected reused session id %s, got %#v", first.Session.ID, second.Session)
	}
}

func TestContinueOnlyNotFound(t *testing.T) {
	store := memory.NewSessionStore()
	sdk := newSDK(store, fakeBinding("default", &fakeDriver{}), nil)

	_, err := sdk.Run(context.Background(), "hello", agentadaptor.WithContinueSession("missing"))
	if !errors.Is(err, agentadaptor.ErrSessionNotFound) {
		t.Fatalf("expected ErrSessionNotFound, got %v", err)
	}
}

func TestStartNewRebindsKeyAndKeepsOldSession(t *testing.T) {
	store := memory.NewSessionStore()
	sdk := newSDK(store, fakeBinding("default", &fakeDriver{}), nil)

	first, err := sdk.Run(context.Background(), "hello", agentadaptor.WithSessionKey("company", "issue-1"))
	if err != nil {
		t.Fatalf("first run: %v", err)
	}

	second, err := sdk.Run(context.Background(), "restart", agentadaptor.WithNewSession("company", "issue-1"))
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if second.Session == nil || !second.Session.Created || second.Session.PreviousID != first.Session.ID || second.Session.ID == first.Session.ID {
		t.Fatalf("expected new session with previous id %s, got %#v", first.Session.ID, second.Session)
	}

	legacy, err := sdk.Run(context.Background(), "legacy", agentadaptor.WithContinueSession(first.Session.ID))
	if err != nil {
		t.Fatalf("legacy run: %v", err)
	}
	if legacy.Session == nil || legacy.Session.ID != first.Session.ID || !legacy.Session.Reused {
		t.Fatalf("expected archived session to remain addressable, got %#v", legacy.Session)
	}
}

func TestContinueOnlyDetectsIncompatibility(t *testing.T) {
	store := memory.NewSessionStore()
	sdk := newSDK(store, fakeBinding("default", &fakeDriver{}), nil)

	first, err := sdk.Run(
		context.Background(),
		"hello",
		agentadaptor.WithSessionKey("company", "issue-1"),
		agentadaptor.WithAgentIdentity(agentadaptor.AgentIdentity{ID: "agent-a"}),
	)
	if err != nil {
		t.Fatalf("first run: %v", err)
	}

	_, err = sdk.Run(
		context.Background(),
		"again",
		agentadaptor.WithContinueSession(first.Session.ID),
		agentadaptor.WithAgentIdentity(agentadaptor.AgentIdentity{ID: "agent-b"}),
	)
	if !errors.Is(err, agentadaptor.ErrSessionIncompatible) {
		t.Fatalf("expected ErrSessionIncompatible, got %v", err)
	}
}

func TestSessionBusyOnConcurrentKey(t *testing.T) {
	store := memory.NewSessionStore()
	driver := &fakeDriver{
		blockPrompt: "block",
		blockCh:     make(chan struct{}),
		startedCh:   make(chan struct{}, 1),
	}
	sdk := newSDK(store, fakeBinding("default", driver), nil)

	handle, err := sdk.Start(context.Background(), "block", agentadaptor.WithSessionKey("company", "issue-1"))
	if err != nil {
		t.Fatalf("start run: %v", err)
	}

	select {
	case <-driver.startedCh:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for first run to start")
	}

	_, err = sdk.Run(context.Background(), "again", agentadaptor.WithSessionKey("company", "issue-1"))
	if !errors.Is(err, agentadaptor.ErrSessionBusy) {
		t.Fatalf("expected ErrSessionBusy, got %v", err)
	}

	close(driver.blockCh)
	if _, err := handle.Wait(context.Background()); err != nil {
		t.Fatalf("wait: %v", err)
	}
}

func TestContinueOrStartFallsBackAfterResumeRejected(t *testing.T) {
	store := memory.NewSessionStore()
	driver := &fakeDriver{}
	sdk := newSDK(store, fakeBinding("default", driver), nil)

	first, err := sdk.Run(context.Background(), "hello", agentadaptor.WithSessionKey("company", "issue-1"))
	if err != nil {
		t.Fatalf("first run: %v", err)
	}

	driver.rejectResume = true
	second, err := sdk.Run(context.Background(), "again", agentadaptor.WithSessionKey("company", "issue-1"))
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if second.Session == nil || !second.Session.Created || second.Session.ID == first.Session.ID || second.Session.PreviousID != first.Session.ID {
		t.Fatalf("expected fallback fresh session from %s, got %#v", first.Session.ID, second.Session)
	}
}

func TestContinueOnlyKeepsResumeRejectedFailure(t *testing.T) {
	store := memory.NewSessionStore()
	driver := &fakeDriver{}
	sdk := newSDK(store, fakeBinding("default", driver), nil)

	first, err := sdk.Run(context.Background(), "hello", agentadaptor.WithSessionKey("company", "issue-1"))
	if err != nil {
		t.Fatalf("first run: %v", err)
	}

	driver.rejectResume = true
	_, err = sdk.Run(context.Background(), "again", agentadaptor.WithContinueSession(first.Session.ID))
	if !errors.Is(err, agentadaptor.ErrResumeRejected) {
		t.Fatalf("expected ErrResumeRejected, got %v", err)
	}
}

func TestStatefulRunRequiresValidCheckpoint(t *testing.T) {
	store := memory.NewSessionStore()
	driver := &fakeDriver{omitCheckpoint: true}
	sdk := newSDK(store, fakeBinding("default", driver), nil)

	_, err := sdk.Run(context.Background(), "hello", agentadaptor.WithSessionKey("company", "issue-1"))
	if !errors.Is(err, agentadaptor.ErrSessionCheckpointMissing) {
		t.Fatalf("expected ErrSessionCheckpointMissing, got %v", err)
	}
}

func TestAdminExposesBoundAgents(t *testing.T) {
	driver := &fakeDriver{
		supportedModels: []agentadaptor.ModelInfo{
			{ID: "fake-a", Label: "fake-a"},
			{ID: "fake-b", Label: "fake-b"},
		},
	}
	sdk := newSDK(
		nil,
		fakeBinding("default", driver, agentadaptor.WithDefaultSkills("core/default")),
		map[string]agentadaptor.AgentBinding{
			"review": fakeBinding("review", &fakeDriver{}, agentadaptor.WithDefaultSkills("core/review")),
		},
	)

	admin := sdk.Admin()
	agents := admin.Agents()
	if len(agents) != 2 {
		t.Fatalf("expected 2 agents, got %d", len(agents))
	}

	defaultAdmin := admin.Default()
	info := defaultAdmin.Info()
	if !info.Default || info.Name != "default" || info.DriverType != "fake" {
		t.Fatalf("unexpected default info: %#v", info)
	}

	models, err := defaultAdmin.ListModels(context.Background())
	if err != nil {
		t.Fatalf("list models: %v", err)
	}
	if len(models) != 2 || models[0].ID != "fake-a" {
		t.Fatalf("unexpected models: %#v", models)
	}

	reviewAdmin, err := admin.Agent("review")
	if err != nil {
		t.Fatalf("review admin: %v", err)
	}
	snapshot, err := reviewAdmin.SyncSkills(context.Background(), []string{"core/review", "extra/checks"})
	if err != nil {
		t.Fatalf("sync skills: %v", err)
	}
	if len(snapshot.Desired) != 2 || snapshot.Desired[1] != "extra/checks" {
		t.Fatalf("unexpected desired skills: %#v", snapshot.Desired)
	}
}

func TestAdminGetProfileFallsBackToUnsupportedReport(t *testing.T) {
	sdk := newSDK(nil, fakeBinding("default", &fakeDriver{}), nil)

	profile, err := sdk.Admin().Default().GetProfile(context.Background())
	if err != nil {
		t.Fatalf("get profile: %v", err)
	}
	if profile.Supported {
		t.Fatalf("expected unsupported profile report, got %#v", profile)
	}
	if profile.DriverType != "fake" || profile.Source != agentadaptor.AgentProfileSourceUnsupported {
		t.Fatalf("unexpected unsupported profile report: %#v", profile)
	}
	if profile.Error == "" {
		t.Fatalf("expected unsupported profile error message, got %#v", profile)
	}
}
