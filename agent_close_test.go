package adaptor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/hostedprofile"
	"github.com/agent-dance/agent-adaptor/internal/mcpruntime"
	"github.com/agent-dance/agent-adaptor/internal/toolidentity"
	"github.com/agent-dance/agent-adaptor/profile"
	"github.com/agent-dance/agent-adaptor/tool"
)

type closeTestToolInput struct {
	Value string `json:"value" jsonschema:"required"`
}

type closeTestToolOutput struct {
	Value string `json:"value"`
}

func TestHostedToolProfileAllocationIsPrivateAndAtomic(t *testing.T) {
	source := t.TempDir()
	d := &profileLockTestDriver{profileDir: source}
	agent := &Agent{driver: d, toolProvider: &hostedToolProvider{}}
	eff := &RunSettings{}
	if err := agent.prepareHostedToolProfile(context.Background(), eff); err != nil {
		t.Fatal(err)
	}
	if eff.effectiveProfile == nil || eff.effectiveProfile.Mode != driver.ProfileModeClone {
		t.Fatalf("effective profile = %#v, want private clone", eff.effectiveProfile)
	}
	dir := eff.effectiveProfile.Dir
	t.Cleanup(func() { _ = removeHostedToolProfileDir(dir) })
	if filepath.Dir(filepath.Clean(dir)) != filepath.Clean(os.TempDir()) ||
		!strings.HasPrefix(filepath.Base(dir), "agent-adaptor-tool-profile-") {
		t.Fatalf("private profile path = %q", dir)
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("private profile = (%v, %v)", info, err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o700 {
		t.Fatalf("private profile mode = %o, want 700", info.Mode().Perm())
	}
}

func TestHostedToolProfileClaimSerializesFirstMaterialization(t *testing.T) {
	d := &profileLockTestDriver{
		profileDir:        t.TempDir(),
		firstEntered:      make(chan struct{}),
		releaseFirst:      make(chan struct{}),
		concurrentEntered: make(chan struct{}),
	}
	agent := &Agent{driver: d, toolProvider: &hostedToolProvider{}}
	selection := &driver.ProfileSelection{Mode: driver.ProfileModeClone, Dir: d.profileDir, From: t.TempDir()}
	errs := make(chan error, 2)
	go func() { errs <- agent.claimHostedToolProfile(context.Background(), driver.AgentIdentity{}, selection) }()
	<-d.firstEntered
	go func() { errs <- agent.claimHostedToolProfile(context.Background(), driver.AgentIdentity{}, selection) }()
	select {
	case <-d.concurrentEntered:
		t.Fatal("concurrent first runs entered profile materialization together")
	case <-time.After(30 * time.Millisecond):
	}
	close(d.releaseFirst)
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	if d.maxActive != 1 {
		t.Fatalf("max concurrent GetProfile calls = %d, want 1", d.maxActive)
	}
}

func TestAgentCloseBoundsAdmittedHostedToolProfileResolutionAndCleansAllocation(t *testing.T) {
	d := &profileLockTestDriver{
		profileDir:   t.TempDir(),
		firstEntered: make(chan struct{}),
		releaseFirst: make(chan struct{}),
	}
	agent := New(d, WithTools(tool.Define("echo", "Echo input.",
		func(_ context.Context, input closeTestToolInput) (closeTestToolOutput, error) {
			return closeTestToolOutput{Value: input.Value}, nil
		},
		tool.Revision("echo/v1"),
	)))

	stream := agent.Stream(context.Background(), "use echo")
	run := stream.(*runStream)
	select {
	case <-d.firstEntered:
	case <-run.done:
		_, err := stream.Result()
		t.Fatalf("run ended before profile resolution: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("profile resolution did not start")
	}

	closeCtx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	started := time.Now()
	if err := agent.Close(closeCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Close while profile resolution is blocked = %v, want deadline", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("bounded Close took %v", elapsed)
	}

	close(d.releaseFirst)
	_, _ = stream.Result()

	agent.toolProfileMu.Lock()
	var ownedDir string
	for _, selection := range agent.toolProfileSelections {
		ownedDir = selection.ownedDir
		break
	}
	agent.toolProfileMu.Unlock()
	if ownedDir == "" {
		t.Fatal("admitted profile resolution did not record its owned directory")
	}
	if _, err := os.Stat(ownedDir); err != nil {
		t.Fatalf("owned profile before Close retry: %v", err)
	}
	if err := agent.Close(context.Background()); err != nil {
		t.Fatalf("Close retry: %v", err)
	}
	if _, err := os.Stat(ownedDir); !os.IsNotExist(err) {
		t.Fatalf("owned profile after Close = %v, want removed", err)
	}
}

func TestHostedToolMaterializedProfileFingerprintTracksResourcesNotAuth(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "skills", "lookup"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "mcp.json"), []byte(`{"mcpServers":{"docs":{}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "skills", "lookup", "SKILL.md"), []byte("v1"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "auth.json"), []byte("secret-v1"), 0o600); err != nil {
		t.Fatal(err)
	}
	first, err := hostedToolMaterializedProfileFingerprint("cursor", dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "auth.json"), []byte("secret-v2"), 0o600); err != nil {
		t.Fatal(err)
	}
	second, err := hostedToolMaterializedProfileFingerprint("cursor", dir)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatal("auth rotation changed durable materialized-profile fingerprint")
	}
	if err := os.WriteFile(filepath.Join(dir, "skills", "lookup", "SKILL.md"), []byte("v2"), 0o600); err != nil {
		t.Fatal(err)
	}
	third, err := hostedToolMaterializedProfileFingerprint("cursor", dir)
	if err != nil {
		t.Fatal(err)
	}
	if second == third {
		t.Fatal("materialized skill change did not change profile fingerprint")
	}
}

type profileLockTestDriver struct {
	profileDir        string
	firstEntered      chan struct{}
	releaseFirst      chan struct{}
	concurrentEntered chan struct{}
	mu                sync.Mutex
	active            int
	maxActive         int
	firstOnce         sync.Once
	concurrentOnce    sync.Once
}

func (*profileLockTestDriver) Descriptor() driver.Descriptor {
	return driver.Descriptor{Type: "cursor", MCP: driver.MCPCapability{Supported: true, HTTP: true}}
}
func (*profileLockTestDriver) ValidateConfig(any) error { return nil }
func (*profileLockTestDriver) Run(context.Context, driver.Request, driver.EventSink) (driver.Response, error) {
	return driver.Response{}, nil
}
func (d *profileLockTestDriver) GetProfile(context.Context, any, driver.AgentIdentity, *driver.ProfileSelection) (driver.AgentProfile, error) {
	d.mu.Lock()
	d.active++
	if d.active > d.maxActive {
		d.maxActive = d.active
	}
	active := d.active
	d.mu.Unlock()
	if active > 1 && d.concurrentEntered != nil {
		d.concurrentOnce.Do(func() { close(d.concurrentEntered) })
	}
	if d.firstEntered != nil {
		first := false
		d.firstOnce.Do(func() {
			first = true
			close(d.firstEntered)
		})
		if first {
			<-d.releaseFirst
		}
	}
	d.mu.Lock()
	d.active--
	d.mu.Unlock()
	return driver.AgentProfile{DriverType: "cursor", Supported: true, Dir: d.profileDir}, nil
}

func TestAgentCloseIsBoundedIdempotentAndRejectsNewRuns(t *testing.T) {
	closeErr := errors.New("close failed")
	d := &lifecycleTestDriver{
		started:  make(chan struct{}),
		release:  make(chan struct{}),
		closeErr: closeErr,
	}
	agent := New(d)

	firstDone := make(chan error, 1)
	go func() { firstDone <- agent.Close(context.Background()) }()
	<-d.started

	stream := agent.Stream(context.Background(), "must not run")
	if _, err := stream.Result(); !errors.Is(err, ErrAgentClosed) {
		t.Fatalf("run after Close started = %v, want ErrAgentClosed", err)
	}
	waitCtx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := agent.Close(waitCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("concurrent bounded Close = %v, want context deadline", err)
	}

	close(d.release)
	if err := <-firstDone; !errors.Is(err, closeErr) {
		t.Fatalf("first Close = %v, want configured error", err)
	}
	if err := agent.Close(context.Background()); !errors.Is(err, closeErr) {
		t.Fatalf("retry Close = %v, want same configured error", err)
	}
	d.mu.Lock()
	calls := d.closeCalls
	runs := d.runCalls
	d.mu.Unlock()
	if calls != 2 || runs != 0 {
		t.Fatalf("lifecycle calls: Close=%d Run=%d, want 2 retry attempts/0 runs", calls, runs)
	}
}

func TestNilAgentCloseReturnsErrAgentClosed(t *testing.T) {
	var agent *Agent
	if err := agent.Close(context.Background()); !errors.Is(err, ErrAgentClosed) {
		t.Fatalf("nil Agent.Close = %v, want ErrAgentClosed", err)
	}
}

func TestAgentCloseStopsProviderBeforeOwnedToolRuntime(t *testing.T) {
	toolErr := errors.New("tool close failed")
	d := &lifecycleTestDriver{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	runtime := &lifecycleToolRuntime{
		closed:   make(chan struct{}),
		closeErr: toolErr,
	}
	agent := New(d)
	agent.toolRuntime = runtime

	done := make(chan error, 1)
	go func() { done <- agent.Close(context.Background()) }()
	<-d.started
	select {
	case <-runtime.closed:
		t.Fatal("tool runtime closed before provider process cleanup completed")
	default:
	}
	close(d.release)

	err := <-done
	if !errors.Is(err, toolErr) {
		t.Fatalf("Close error = %v, want tool runtime error", err)
	}
	select {
	case <-runtime.closed:
	default:
		t.Fatal("tool runtime was not closed after provider process cleanup")
	}
	if err := agent.Close(context.Background()); !errors.Is(err, toolErr) {
		t.Fatalf("idempotent Close = %v, want original joined error", err)
	}
	runtime.mu.Lock()
	closeCalls := runtime.closeCalls
	runtime.mu.Unlock()
	if closeCalls != 2 {
		t.Fatalf("tool runtime Close calls = %d, want 2 retry attempts", closeCalls)
	}
}

func TestAgentCloseProcessFailureDoesNotRevokeToolRuntime(t *testing.T) {
	processErr := errors.New("transient process close failure")
	d := &lifecycleTestDriver{started: make(chan struct{}), release: make(chan struct{}), closeErr: processErr}
	runtime := &lifecycleToolRuntime{closed: make(chan struct{})}
	agent := New(d)
	agent.toolRuntime = runtime
	close(d.release)
	if err := agent.Close(context.Background()); !errors.Is(err, processErr) {
		t.Fatalf("first Close = %v, want process failure", err)
	}
	select {
	case <-runtime.closed:
		t.Fatal("process failure revoked Tool runtime")
	default:
	}
	d.mu.Lock()
	d.closeErr = nil
	d.mu.Unlock()
	if err := agent.Close(context.Background()); err != nil {
		t.Fatalf("retry Close: %v", err)
	}
	select {
	case <-runtime.closed:
	default:
		t.Fatal("successful retry did not close Tool runtime")
	}
}

func TestAgentCloseCancelsAndDrainsAcceptedRunsBeforeToolRuntime(t *testing.T) {
	d := &drainingLifecycleDriver{
		runStarted:  make(chan struct{}),
		allowReturn: make(chan struct{}),
		runReturned: make(chan struct{}),
	}
	runtime := &orderingToolRuntime{
		runReturned: d.runReturned,
		closed:      make(chan struct{}),
	}
	agent := New(d)
	agent.toolRuntime = runtime

	stream := agent.Stream(context.Background(), "block")
	<-d.runStarted
	if err := agent.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	select {
	case <-runtime.closed:
	default:
		t.Fatal("tool runtime was not closed")
	}
	if runtime.closedBeforeRunReturned {
		t.Fatal("tool runtime closed before an accepted run drained")
	}
	if _, err := stream.Result(); !errors.Is(err, context.Canceled) {
		t.Fatalf("accepted run result = %v, want context.Canceled", err)
	}
}

func TestAgentCloseDeadlineLeavesCleanupRetryable(t *testing.T) {
	d := &lifecycleTestDriver{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	runtime := &lifecycleToolRuntime{closed: make(chan struct{})}
	agent := New(d)
	agent.toolRuntime = runtime

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := agent.Close(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("bounded Close = %v, want deadline", err)
	}
	select {
	case <-runtime.closed:
		t.Fatal("partial Close revoked Tool runtime before provider cleanup")
	default:
	}
	close(d.release)
	if err := agent.Close(context.Background()); err != nil {
		t.Fatalf("retry Close: %v", err)
	}
	select {
	case <-runtime.closed:
	default:
		t.Fatal("retry Close did not finish Tool runtime cleanup")
	}
	d.mu.Lock()
	calls := d.closeCalls
	d.mu.Unlock()
	if calls != 2 {
		t.Fatalf("CloseProcesses calls = %d, want one timed-out attempt plus retry", calls)
	}
}

type lifecycleTestDriver struct {
	mu         sync.Mutex
	started    chan struct{}
	release    chan struct{}
	closeErr   error
	closeCalls int
	runCalls   int
	startOnce  sync.Once
}

type lifecycleToolRuntime struct {
	mu         sync.Mutex
	closed     chan struct{}
	closeErr   error
	closeCalls int
}

type drainingLifecycleDriver struct {
	runStarted  chan struct{}
	allowReturn chan struct{}
	runReturned chan struct{}
	closeOnce   sync.Once
}

func (*drainingLifecycleDriver) Descriptor() driver.Descriptor {
	return driver.Descriptor{Type: "draining-lifecycle", Process: driver.ProcessCapability{Persistent: true}}
}

func (*drainingLifecycleDriver) ValidateConfig(any) error { return nil }

func (d *drainingLifecycleDriver) Run(ctx context.Context, _ driver.Request, _ driver.EventSink) (driver.Response, error) {
	close(d.runStarted)
	<-ctx.Done()
	<-d.allowReturn
	close(d.runReturned)
	return driver.Response{}, ctx.Err()
}

func (d *drainingLifecycleDriver) CloseProcesses(context.Context) error {
	d.closeOnce.Do(func() { close(d.allowReturn) })
	return nil
}

type orderingToolRuntime struct {
	runReturned             chan struct{}
	closed                  chan struct{}
	closedBeforeRunReturned bool
}

func (r *orderingToolRuntime) Close(context.Context) error {
	select {
	case <-r.runReturned:
	default:
		r.closedBeforeRunReturned = true
	}
	close(r.closed)
	return nil
}

func (r *lifecycleToolRuntime) Close(context.Context) error {
	r.mu.Lock()
	r.closeCalls++
	r.mu.Unlock()
	select {
	case <-r.closed:
	default:
		close(r.closed)
	}
	return r.closeErr
}

func (d *lifecycleTestDriver) Descriptor() driver.Descriptor {
	return driver.Descriptor{
		Type:    "lifecycle-test",
		Process: driver.ProcessCapability{Persistent: true},
	}
}

func (*lifecycleTestDriver) ValidateConfig(any) error { return nil }

func (d *lifecycleTestDriver) Run(context.Context, driver.Request, driver.EventSink) (driver.Response, error) {
	d.mu.Lock()
	d.runCalls++
	d.mu.Unlock()
	return driver.Response{}, nil
}

func (d *lifecycleTestDriver) CloseProcesses(ctx context.Context) error {
	d.mu.Lock()
	d.closeCalls++
	d.mu.Unlock()
	d.startOnce.Do(func() { close(d.started) })
	select {
	case <-d.release:
		return d.closeErr
	case <-ctx.Done():
		return ctx.Err()
	}
}

func TestAlignmentProfileCloseRetryKeepsClaimUntilGatewayClosed(t *testing.T) {
	source := filepath.Join(t.TempDir(), "source")
	if err := os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	spec := hostedprofile.Spec{DriverType: "claude", SourceDir: source}
	claim, err := hostedprofile.Acquire(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = claim.ReleaseClean(context.Background()) })
	if err := claim.Initialize(context.Background(), func(context.Context, string) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(claim.Dir(), "session"), []byte("keep session"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := claim.BeginUse(context.Background()); err != nil {
		t.Fatal(err)
	}
	failure := errors.New("gateway failure")
	rt := &lifecycleToolRuntime{closed: make(chan struct{}), closeErr: failure}
	a := New(&profileLockTestDriver{profileDir: source})
	a.toolRuntime = rt
	a.toolProfileSelections = map[string]hostedToolProfileSelection{"owned": {persistent: claim}}
	if err := a.Close(context.Background()); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	if _, err := a.Run(context.Background(), "no new run"); !errors.Is(err, ErrAgentClosed) {
		t.Fatal(err)
	}
	if _, err := hostedprofile.Acquire(context.Background(), spec); !errors.Is(err, profile.ErrInUse) {
		t.Fatalf("gateway failure released claim: %v", err)
	}
	rt.closeErr = nil
	if err := a.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	next, err := hostedprofile.Acquire(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	defer next.ReleaseUnused(context.Background())
	if raw, err := os.ReadFile(filepath.Join(next.Dir(), "session")); err != nil || string(raw) != "keep session" {
		t.Fatal("Close lost provider state")
	}
}

func TestAlignmentProfileClaimWaitIsCancellationSafe(t *testing.T) {
	gate := &hostedProfileGate{}
	gate.Lock()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- gate.LockContext(ctx) }()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("claim wait ignored cancellation")
	}
	gate.Unlock()
}

func TestAlignmentProfileCloseRetryProcessDrainAndProjection(t *testing.T) {
	for _, stage := range []string{"process", "drain", "projection"} {
		t.Run(stage, func(t *testing.T) {
			source := filepath.Join(t.TempDir(), "source")
			if err := os.Mkdir(source, 0700); err != nil {
				t.Fatal(err)
			}
			spec := hostedprofile.Spec{DriverType: "claude", SourceDir: source}
			claim, err := hostedprofile.Acquire(context.Background(), spec)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = claim.ReleaseClean(context.Background()) })
			if err := claim.Initialize(context.Background(), func(context.Context, string) error { return nil }); err != nil {
				t.Fatal(err)
			}
			if err := claim.BeginUse(context.Background()); err != nil {
				t.Fatal(err)
			}
			rt := &lifecycleToolRuntime{closed: make(chan struct{})}
			a := New(&profileLockTestDriver{profileDir: source})
			a.toolRuntime = rt
			a.toolProfileSelections = map[string]hostedToolProfileSelection{"owned": {persistent: claim}}
			a.toolProfiles = map[string]hostedToolProfileClaim{"owned": {driverType: "claude", dir: claim.Dir()}}
			var fix func()
			ctx := context.Background()
			var cancel context.CancelFunc
			switch stage {
			case "process":
				d := &lifecycleTestDriver{started: make(chan struct{}), release: make(chan struct{}), closeErr: errors.New("injected process failure")}
				close(d.release)
				a.driver = d
				fix = func() { d.closeErr = nil }
			case "drain":
				a.activeDone = make(chan struct{})
				ctx, cancel = context.WithTimeout(ctx, 10*time.Millisecond)
				defer cancel()
				fix = func() { close(a.activeDone) }
			case "projection":
				server := driver.MCPServerSpec{Key: toolidentity.ServerKey, Transport: driver.MCPTransportHTTP, URL: "http://127.0.0.1:1/mcp", BearerTokenEnvVar: toolidentity.BearerTokenEnvVarPrefix + strings.Repeat("A", 32), Required: true, RequiredReason: toolidentity.RequiredReason}
				if _, err := mcpruntime.SyncResource(ctx, "claude", claim.Dir(), mcpruntime.ProfileKindHostManaged, driver.MCPPayload{Servers: []driver.MCPServerSpec{server}}); err != nil {
					t.Fatal(err)
				}
				path := filepath.Join(claim.Dir(), ".claude.json")
				raw, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("{"), 0600); err != nil {
					t.Fatal(err)
				}
				fix = func() {
					if err := os.WriteFile(path, raw, 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
			if err := a.Close(ctx); err == nil {
				t.Fatal("injected Close phase succeeded")
			}
			select {
			case <-rt.closed:
				t.Fatal("gateway revoked before prior phase completed")
			default:
			}
			if _, err := hostedprofile.Acquire(context.Background(), spec); !errors.Is(err, profile.ErrInUse) {
				t.Fatalf("failed phase relinquished claim: %v", err)
			}
			fix()
			if err := a.Close(context.Background()); err != nil {
				t.Fatal(err)
			}
			next, err := hostedprofile.Acquire(context.Background(), spec)
			if err != nil {
				t.Fatal(err)
			}
			if err := next.ReleaseUnused(context.Background()); err != nil {
				t.Fatal(err)
			}
		})
	}
}
