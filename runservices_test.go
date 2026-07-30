package adaptor_test

// Contract tests for the run-scoped service mount point (RunServiceProvider
// / RunAttachment / RunEventSource) plus the workspace and runtime-service
// option wiring.
//
// Determinism: every hand-off is a channel rendezvous, there is no sleep and
// no wall-clock assertion, so the file is -count=5 stable. The event-ordering
// tests drive the fake driver and the provider event source in lock-step from
// the consuming goroutine, which is what makes "interleaved" an assertable
// property rather than a hope.

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/mcp"
	"github.com/agent-dance/agent-adaptor/memory"
)

// ============ test doubles ============

// fakeProvider is a programmable RunServiceProvider. attach/detach calls are
// recorded in order so teardown sequencing is assertable.
type fakeProvider struct {
	name string

	attachment adaptor.RunAttachment
	attachErr  error
	detach     func(context.Context, string) error

	log *callLog
}

func (p *fakeProvider) AttachRun(_ context.Context, runID string) (adaptor.RunAttachment, error) {
	p.log.add("attach:" + p.name + ":" + runID)
	if p.attachErr != nil {
		return adaptor.RunAttachment{}, p.attachErr
	}
	return p.attachment, nil
}

func (p *fakeProvider) DetachRun(ctx context.Context, runID string) error {
	p.log.add("detach:" + p.name + ":" + runID)
	if p.detach != nil {
		return p.detach(ctx, runID)
	}
	return nil
}

// callLog is an ordered, concurrency-safe record of lifecycle calls.
type callLog struct {
	mu      sync.Mutex
	entries []string
}

func (l *callLog) add(entry string) {
	l.mu.Lock()
	l.entries = append(l.entries, entry)
	l.mu.Unlock()
}

func (l *callLog) snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.entries...)
}

func (l *callLog) has(entry string) bool {
	for _, got := range l.snapshot() {
		if got == entry {
			return true
		}
	}
	return false
}

// scriptedSource turns a test-controlled channel into a RunEventSource with
// the flush-then-close cancellation semantics the SDK contract requires.
func scriptedSource(feed chan adaptor.Event) adaptor.RunEventSource {
	return func(ctx context.Context, _ string) <-chan adaptor.Event {
		out := make(chan adaptor.Event)
		go func() {
			defer close(out)
			for {
				select {
				case ev := <-feed:
					out <- ev
				case <-ctx.Done():
					for {
						select {
						case ev := <-feed:
							out <- ev
						default:
							return
						}
					}
				}
			}
		}()
		return out
	}
}

// fakeServiceManager is a programmable ServiceManager.
type fakeServiceManager struct {
	ensure  func(ctx context.Context, req adaptor.ServiceRequest) ([]adaptor.ServiceRef, error)
	release func(ctx context.Context, runID string) error
	log     *callLog

	mu       sync.Mutex
	requests []adaptor.ServiceRequest
}

func (m *fakeServiceManager) Ensure(ctx context.Context, req adaptor.ServiceRequest) ([]adaptor.ServiceRef, error) {
	m.mu.Lock()
	m.requests = append(m.requests, req)
	m.mu.Unlock()
	m.log.add("ensure:" + req.RunID)
	if m.ensure != nil {
		return m.ensure(ctx, req)
	}
	return nil, nil
}

func (m *fakeServiceManager) ReleaseByRun(ctx context.Context, runID string) error {
	m.log.add("release_run:" + runID)
	if m.release != nil {
		return m.release(ctx, runID)
	}
	return nil
}

func (m *fakeServiceManager) ReleaseByLabels(context.Context, map[string]string) error { return nil }

// fakeWorkspaceManager is a programmable WorkspaceManager.
type fakeWorkspaceManager struct {
	lease   adaptor.WorkspaceLease
	err     error
	release func(context.Context, adaptor.WorkspaceLease, adaptor.WorkspaceReleaseMode) error
	log     *callLog

	mu       sync.Mutex
	requests []adaptor.WorkspaceRequest
	released []adaptor.WorkspaceReleaseMode
}

func (m *fakeWorkspaceManager) Resolve(_ context.Context, req adaptor.WorkspaceRequest) (adaptor.WorkspaceLease, error) {
	m.mu.Lock()
	m.requests = append(m.requests, req)
	m.mu.Unlock()
	m.log.add("workspace_resolve")
	if m.err != nil {
		return adaptor.WorkspaceLease{}, m.err
	}
	return m.lease, nil
}

func (m *fakeWorkspaceManager) Release(ctx context.Context, lease adaptor.WorkspaceLease, mode adaptor.WorkspaceReleaseMode) error {
	m.mu.Lock()
	m.released = append(m.released, mode)
	m.mu.Unlock()
	m.log.add("workspace_release")
	if m.release != nil {
		return m.release(ctx, lease, mode)
	}
	return nil
}

// sidecarRef builds an attachment ref shaped like a real per-run MCP sidecar:
// loopback URL, typed MCP declaration, bearer token in SecretEnv.
func sidecarRef(key, url, tokenEnv, token string) adaptor.ServiceRef {
	server := mcp.HTTP(key, url, mcp.WithBearerTokenEnv(tokenEnv), mcp.Required("test sidecar"))
	return adaptor.ServiceRef{
		ID:        key,
		Name:      key,
		URL:       url,
		Status:    driver.RuntimeServiceRunning,
		Lifecycle: driver.RuntimeLifecycleEphemeral,
		MCP:       &server,
		Metadata:  map[string]string{"tool": "delegate_to_agent"},
		SecretEnv: []driver.EnvBinding{{Name: tokenEnv, Value: token}},
	}
}

func httpCapableDriver() *fakeDriver {
	fake := newFakeDriver()
	fake.descriptor = &driver.Descriptor{
		Type:        "fake",
		DisplayName: "Fake Driver",
		MCP:         driver.MCPCapability{Supported: true, Stdio: true, HTTP: true, SSE: true},
	}
	return fake
}

// ============ 1. attach failure is a pre-launch failure ============

func TestRunServiceAttachFailureIsPreLaunch(t *testing.T) {
	log := &callLog{}
	boom := errors.New("sidecar refused to start")
	first := &fakeProvider{name: "first", log: log}
	failing := &fakeProvider{name: "failing", attachErr: boom, log: log}
	never := &fakeProvider{name: "never", log: log}

	fake := newFakeDriver()
	agent := adaptor.New(fake, adaptor.WithRunServices(first, failing, never))

	res, err := agent.Run(context.Background(), "hi")
	if res != nil {
		t.Fatalf("Result = %+v, want nil on pre-launch failure", res)
	}
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want it to wrap %v", err, boom)
	}
	if fake.runCount() != 0 {
		t.Fatalf("driver ran %d time(s), want 0: attach failure must not launch the driver", fake.runCount())
	}

	entries := log.snapshot()
	if len(entries) != 3 {
		t.Fatalf("lifecycle log = %v, want exactly attach(first), attach(failing), detach(first)", entries)
	}
	if got := entries[2]; got[:7] != "detach:" || entries[2][7:12] != "first" {
		t.Errorf("entries[2] = %q, want the already-attached provider to be unwound", got)
	}
	for _, entry := range entries {
		if len(entry) >= 12 && entry[:13] == "attach:never:" {
			t.Error("providers after the failing one must not be attached")
		}
	}
	if log.has("detach:failing:") {
		t.Error("a provider whose AttachRun failed must not be detached")
	}
}

func TestRunServiceAttachFailureJoinsUnwindFailure(t *testing.T) {
	attachErr := errors.New("second provider refused to attach")
	detachErr := errors.New("first provider failed to detach")
	first := &fakeProvider{
		name: "first",
		log:  &callLog{},
		detach: func(context.Context, string) error {
			return detachErr
		},
	}
	failing := &fakeProvider{name: "failing", log: &callLog{}, attachErr: attachErr}
	fake := newFakeDriver()

	res, err := adaptor.New(fake, adaptor.WithRunServices(first, failing)).Run(context.Background(), "go")
	if res != nil || !errors.Is(err, attachErr) || !errors.Is(err, detachErr) {
		t.Fatalf("Run = (%+v, %v), want both attach and unwind errors", res, err)
	}
	if fake.runCount() != 0 {
		t.Fatalf("driver runs = %d, want pre-launch rejection", fake.runCount())
	}
}

// ============ 2. MCP payload merges host WithMCP with attachment ============

func TestRunServiceMCPMergesWithHostMCP(t *testing.T) {
	log := &callLog{}
	provider := &fakeProvider{
		name: "sidecar",
		log:  log,
		attachment: adaptor.RunAttachment{
			Services: []adaptor.ServiceRef{
				sidecarRef("delegate-a2a", "http://127.0.0.1:65000/mcp", "RUN_TOKEN", "s3cr3t"),
			},
		},
	}

	fake := httpCapableDriver()
	agent := adaptor.New(fake,
		adaptor.WithMCP(mcp.HTTP("docs", "https://example.com/mcp")),
		adaptor.WithRunServices(provider),
	)
	if _, err := agent.Run(context.Background(), "hi"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	req := fake.lastRequest(t)
	keys := map[string]driver.MCPServerSpec{}
	for _, server := range req.MCP.Servers {
		keys[server.Key] = server
	}
	if len(keys) != 2 {
		t.Fatalf("MCP servers = %+v, want the host's own plus the attachment's", req.MCP.Servers)
	}
	if _, ok := keys["docs"]; !ok {
		t.Error("the host's WithMCP server was replaced instead of merged with")
	}
	sidecar, ok := keys["delegate-a2a"]
	if !ok {
		t.Fatal("the attachment's typed MCP server did not reach the driver")
	}
	if sidecar.Transport != driver.MCPTransportHTTP || sidecar.URL != "http://127.0.0.1:65000/mcp" {
		t.Errorf("sidecar server = %+v, want the typed HTTP declaration verbatim", sidecar)
	}
	if sidecar.BearerTokenEnvVar != "RUN_TOKEN" {
		t.Errorf("BearerTokenEnvVar = %q, want RUN_TOKEN", sidecar.BearerTokenEnvVar)
	}
	if !sidecar.Required || sidecar.RequiredReason == "" {
		t.Error("Required/RequiredReason lost in the attachment projection")
	}

	// The runtime payload carries the endpoint and the secret separately:
	// the token reaches driver env through SecretEnv, never the MCP spec.
	if len(req.Runtime.Ensured) != 1 || req.Runtime.Ensured[0].URL != "http://127.0.0.1:65000/mcp" {
		t.Fatalf("Runtime.Ensured = %+v, want the attachment ref", req.Runtime.Ensured)
	}
	if req.Runtime.Ensured[0].Status != driver.RuntimeServiceRunning {
		t.Errorf("ref Status = %q, want the normalized default", req.Runtime.Ensured[0].Status)
	}
	if len(req.Runtime.SecretEnv) != 1 || req.Runtime.SecretEnv[0].Name != "RUN_TOKEN" || req.Runtime.SecretEnv[0].Value != "s3cr3t" {
		t.Errorf("Runtime.SecretEnv = %+v, want the sidecar bearer token", req.Runtime.SecretEnv)
	}
	if req.Runtime.Fingerprint == "" {
		t.Error("Runtime.Fingerprint empty: the normalized runtime payload must be fingerprinted")
	}
}

func TestRuntimeFingerprintCoversAttachmentMCPSemantics(t *testing.T) {
	makeProvider := func(bearerEnv string, required bool) *fakeProvider {
		ref := sidecarRef("same", "http://127.0.0.1:65000/mcp", bearerEnv, "secret-value")
		ref.MCP.Required = required
		ref.MCP.RequiredReason = "required for this test"
		return &fakeProvider{
			name:       "sidecar",
			log:        &callLog{},
			attachment: adaptor.RunAttachment{Services: []adaptor.ServiceRef{ref}},
		}
	}

	firstDriver := httpCapableDriver()
	if _, err := adaptor.New(firstDriver, adaptor.WithRunServices(makeProvider("TOKEN_A", true))).Run(context.Background(), "first"); err != nil {
		t.Fatal(err)
	}
	secondDriver := httpCapableDriver()
	if _, err := adaptor.New(secondDriver, adaptor.WithRunServices(makeProvider("TOKEN_B", false))).Run(context.Background(), "second"); err != nil {
		t.Fatal(err)
	}
	first := firstDriver.lastRequest(t).Runtime.Fingerprint
	second := secondDriver.lastRequest(t).Runtime.Fingerprint
	if first == "" || second == "" || first == second {
		t.Fatalf("runtime fingerprints = %q and %q, want distinct MCP semantic identities", first, second)
	}
}

func TestRunServiceMCPKeyCollisionFailsBeforeLaunch(t *testing.T) {
	provider := &fakeProvider{
		name: "sidecar",
		log:  &callLog{},
		attachment: adaptor.RunAttachment{
			Services: []adaptor.ServiceRef{sidecarRef("docs", "http://127.0.0.1:65000/mcp", "T", "v")},
		},
	}
	fake := httpCapableDriver()
	agent := adaptor.New(fake,
		adaptor.WithMCP(mcp.HTTP("docs", "https://example.com/mcp")),
		adaptor.WithRunServices(provider),
	)
	if _, err := agent.Run(context.Background(), "hi"); err == nil {
		t.Fatal("a duplicate MCP key must fail the run, not silently pick one")
	}
	if fake.runCount() != 0 {
		t.Error("the driver must not launch when MCP assembly fails")
	}
}

// ============ 3. events interleave; terminals survive teardown ============

func TestRunServiceEventsInterleaveWithDriverEvents(t *testing.T) {
	feed := make(chan adaptor.Event, 4)
	resume := make(chan struct{})
	log := &callLog{}
	provider := &fakeProvider{
		name:       "bus",
		log:        log,
		attachment: adaptor.RunAttachment{Events: scriptedSource(feed)},
	}

	fake := newFakeDriver()
	fake.runFunc = func(_ context.Context, req driver.Request, sink driver.EventSink) (driver.Response, error) {
		_ = sink.EmitStream(driver.StreamPayload{Kind: driver.StreamRunStarted, RunID: req.RunID})
		_ = sink.EmitStream(driver.StreamPayload{Kind: driver.StreamTextContent, RunID: req.RunID, MessageID: "m1", Delta: "A"})
		<-resume
		_ = sink.EmitStream(driver.StreamPayload{Kind: driver.StreamTextContent, RunID: req.RunID, MessageID: "m1", Delta: "B"})
		// Published while the driver is still running, exactly like a
		// delegation terminal emitted from inside a tool call.
		feed <- adaptor.SubagentUpdate{Agent: "review", Kind: adaptor.SubagentFinished}
		_ = sink.EmitStream(driver.StreamPayload{Kind: driver.StreamRunFinished, RunID: req.RunID})
		return driver.Response{Output: "done"}, nil
	}

	agent := adaptor.New(fake, adaptor.WithRunServices(provider))
	stream := agent.Stream(context.Background(), "go")
	events := stream.Events()

	// 1. The core-owned envelope is always first, even though the Driver also
	// emits run.started.
	ev := <-events
	if _, ok := ev.(adaptor.RunStarted); !ok {
		t.Fatalf("first event = %T, want RunStarted", ev)
	}
	// 2. The driver's first delta.
	assertTextDelta(t, <-events, "A")
	// 3. A provider event published while the driver is parked: it is the
	// only thing that can arrive next, so its position is deterministic.
	feed <- adaptor.SubagentUpdate{Agent: "review", Kind: adaptor.SubagentStarted}
	assertSubagent(t, <-events, adaptor.SubagentStarted)
	// 4. The driver's second delta, after the provider event.
	close(resume)
	assertTextDelta(t, <-events, "B")

	// 5. The terminal provider event published in the driver's last breath
	// must survive teardown and precede the unique public run.finished.
	var tail []adaptor.Event
	for ev := range events {
		tail = append(tail, ev)
	}
	if len(tail) != 2 {
		t.Fatalf("tail events = %+v, want SubagentUpdate then RunFinished", tail)
	}
	assertSubagent(t, tail[0], adaptor.SubagentFinished)
	if _, ok := tail[1].(adaptor.RunFinished); !ok {
		t.Fatalf("last event = %T, want RunFinished", tail[1])
	}

	res, err := stream.Result()
	if err != nil {
		t.Fatalf("Result: %v", err)
	}
	if res.Text != "done" {
		t.Errorf("Text = %q, want done", res.Text)
	}
	// Detach completes before the event channel closes, so a consumer that got
	// a Result knows the run's services are released.
	if !log.has("detach:bus:" + stream.RunID()) {
		t.Errorf("lifecycle log = %v, want DetachRun to have run by the time Result() returns", log.snapshot())
	}
}

func TestMergedLifecycleStartsBeforeImmediateProviderEventAndFinishesAfterFlush(t *testing.T) {
	provider := &fakeProvider{
		name: "immediate-bus",
		log:  &callLog{},
		attachment: adaptor.RunAttachment{Events: func(ctx context.Context, _ string) <-chan adaptor.Event {
			out := make(chan adaptor.Event, 2)
			out <- adaptor.SubagentUpdate{Agent: "review", Kind: adaptor.SubagentStarted}
			go func() {
				<-ctx.Done()
				out <- adaptor.SubagentUpdate{Agent: "review", Kind: adaptor.SubagentFinished}
				close(out)
			}()
			return out
		}},
	}

	fake := newFakeDriver()
	fake.runFunc = func(_ context.Context, req driver.Request, sink driver.EventSink) (driver.Response, error) {
		_ = sink.EmitStream(driver.StreamPayload{Kind: driver.StreamRunStarted, RunID: req.RunID, ThreadID: "provider-thread", Sequence: 10})
		_ = sink.EmitStream(driver.StreamPayload{Kind: driver.StreamRunFinished, RunID: req.RunID, ThreadID: "provider-thread", Sequence: 11})
		return driver.Response{Output: "done"}, nil
	}

	stream := adaptor.New(fake, adaptor.WithRunServices(provider)).Stream(context.Background(), "go")
	var events []adaptor.Event
	for ev := range stream.Events() {
		events = append(events, ev)
	}
	if _, err := stream.Result(); err != nil {
		t.Fatalf("Result: %v", err)
	}
	if len(events) != 4 {
		t.Fatalf("events = %+v, want start, provider start, provider finish, finish", events)
	}
	if _, ok := events[0].(adaptor.RunStarted); !ok {
		t.Fatalf("first event = %T, want RunStarted", events[0])
	}
	assertSubagent(t, events[1], adaptor.SubagentStarted)
	assertSubagent(t, events[2], adaptor.SubagentFinished)
	if _, ok := events[3].(adaptor.RunFinished); !ok {
		t.Fatalf("last event = %T, want RunFinished", events[3])
	}
	for i, ev := range events {
		if got, want := ev.Meta().Sequence, uint64(i+1); got != want {
			t.Fatalf("events[%d] sequence = %d, want %d", i, got, want)
		}
	}
}

func TestRunEventSourceCannotPublishRunLifecycle(t *testing.T) {
	provider := &fakeProvider{
		name: "lifecycle-injector",
		log:  &callLog{},
		attachment: adaptor.RunAttachment{Events: func(context.Context, string) <-chan adaptor.Event {
			out := make(chan adaptor.Event, 7)
			var nilStart *adaptor.RunStarted
			var nilTerminal *adaptor.RunFinished
			out <- adaptor.RunStarted{RunID: "host-forged-start"}
			out <- &adaptor.RunStarted{RunID: "host-forged-pointer-start"}
			out <- nilStart
			out <- adaptor.Notice{Kind: adaptor.NoticeLifecycle, Text: "host activity"}
			out <- adaptor.RunFinished{RunID: "host-forged-terminal", Failed: true, Message: "forged"}
			out <- &adaptor.RunFinished{RunID: "host-forged-pointer-terminal", Failed: true, Message: "forged"}
			out <- nilTerminal
			close(out)
			return out
		}},
	}

	fake := newFakeDriver()
	fake.runFunc = func(_ context.Context, req driver.Request, sink driver.EventSink) (driver.Response, error) {
		_ = sink.EmitStream(driver.StreamPayload{Kind: driver.StreamRunStarted, RunID: req.RunID})
		_ = sink.EmitStream(driver.StreamPayload{Kind: driver.StreamRunFinished, RunID: req.RunID})
		return driver.Response{Output: "done"}, nil
	}

	events, res, err := collect(adaptor.New(fake, adaptor.WithRunServices(provider)).Stream(context.Background(), "go"))
	if err != nil || res == nil || res.Text != "done" {
		t.Fatalf("run = (%+v, %v)", res, err)
	}
	if len(events) != 3 {
		t.Fatalf("events = %+v, want RunStarted, host Notice, RunFinished", events)
	}
	if started, ok := events[0].(adaptor.RunStarted); !ok || started.RunID == "host-forged-start" {
		t.Fatalf("first event = %#v, want core-owned RunStarted", events[0])
	}
	if notice, ok := events[1].(adaptor.Notice); !ok || notice.Text != "host activity" {
		t.Fatalf("middle event = %#v, want preserved host Notice", events[1])
	}
	if terminal, ok := events[2].(adaptor.RunFinished); !ok || terminal.Failed || terminal.RunID == "host-forged-terminal" {
		t.Fatalf("last event = %#v, want successful core-owned RunFinished", events[2])
	}
	for i, event := range events {
		if got, want := event.Meta().Sequence, uint64(i+1); got != want {
			t.Fatalf("events[%d] sequence = %d, want %d", i, got, want)
		}
	}
}

func TestRunServiceTerminalEventSurvivesCancellation(t *testing.T) {
	feed := make(chan adaptor.Event, 4)
	provider := &fakeProvider{
		name:       "bus",
		log:        &callLog{},
		attachment: adaptor.RunAttachment{Events: scriptedSource(feed)},
	}

	fake := newFakeDriver()
	fake.runFunc = func(ctx context.Context, _ driver.Request, _ driver.EventSink) (driver.Response, error) {
		<-ctx.Done()
		feed <- adaptor.SubagentUpdate{Agent: "review", Kind: adaptor.SubagentFinished}
		return driver.Response{}, ctx.Err()
	}

	agent := adaptor.New(fake, adaptor.WithRunServices(provider))
	stream := agent.Stream(context.Background(), "go")
	stream.Cancel()

	var seen []adaptor.SubagentEventKind
	for ev := range stream.Events() {
		if update, ok := ev.(adaptor.SubagentUpdate); ok {
			seen = append(seen, update.Kind)
		}
	}
	if len(seen) != 1 || seen[0] != adaptor.SubagentFinished {
		t.Fatalf("subagent kinds = %v, want the terminal update to survive a cancelled run", seen)
	}
	if _, err := stream.Result(); err == nil {
		t.Error("a cancelled run must still report its error")
	}
}

func TestEventsCloseOnlyAfterRunServicesAreReleased(t *testing.T) {
	detachStarted := make(chan struct{})
	allowDetach := make(chan struct{})
	provider := &fakeProvider{
		name: "blocking-sidecar",
		log:  &callLog{},
		detach: func(ctx context.Context, _ string) error {
			close(detachStarted)
			select {
			case <-allowDetach:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		},
	}

	fake := newFakeDriver()
	fake.response = driver.Response{Output: "done"}
	stream := adaptor.New(fake, adaptor.WithRunServices(provider)).Stream(context.Background(), "go")

	select {
	case <-detachStarted:
	case <-time.After(time.Second):
		t.Fatal("run service release did not start")
	}

	select {
	case _, ok := <-stream.Events():
		if !ok {
			t.Fatal("Events closed before the run service was released")
		}
		t.Fatal("unexpected event while waiting for run service release")
	default:
	}

	resultDone := make(chan error, 1)
	go func() {
		res, err := stream.Result()
		if err == nil && (res == nil || res.Text != "done") {
			err = fmt.Errorf("unexpected result: %+v", res)
		}
		resultDone <- err
	}()
	select {
	case err := <-resultDone:
		t.Fatalf("Result returned before the run service was released: %v", err)
	default:
	}

	close(allowDetach)
	for range stream.Events() {
	}
	select {
	case err := <-resultDone:
		if err != nil {
			t.Fatalf("Result after Events closed: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Result blocked after Events closed")
	}
}

func TestRunScopedReleaseFailuresAreObservable(t *testing.T) {
	t.Run("provider detach", func(t *testing.T) {
		want := errors.New("detach failed")
		provider := &fakeProvider{
			name: "sidecar",
			log:  &callLog{},
			detach: func(context.Context, string) error {
				return want
			},
		}
		fake := newFakeDriver()
		res, err := adaptor.New(fake, adaptor.WithRunServices(provider)).Run(context.Background(), "go")
		if res != nil || !errors.Is(err, want) {
			t.Fatalf("Run = (%+v, %v), want nil Result and wrapped DetachRun error", res, err)
		}
		if fake.runCount() != 1 {
			t.Fatalf("driver runs = %d, want 1", fake.runCount())
		}
	})

	t.Run("runtime service release", func(t *testing.T) {
		want := errors.New("runtime release failed")
		manager := &fakeServiceManager{
			log: &callLog{},
			release: func(context.Context, string) error {
				return want
			},
		}
		fake := newFakeDriver()
		res, err := adaptor.New(fake,
			adaptor.WithServiceManager(manager),
			adaptor.WithServices(adaptor.ServiceSpec{ID: "db"}),
		).Run(context.Background(), "go")
		if res != nil || !errors.Is(err, want) {
			t.Fatalf("Run = (%+v, %v), want nil Result and wrapped ReleaseByRun error", res, err)
		}
		if fake.runCount() != 1 {
			t.Fatalf("driver runs = %d, want 1", fake.runCount())
		}
	})

	t.Run("workspace release", func(t *testing.T) {
		want := errors.New("workspace release failed")
		manager := &fakeWorkspaceManager{
			lease: adaptor.WorkspaceLease{ID: "lease-1", CWD: t.TempDir()},
			log:   &callLog{},
			release: func(context.Context, adaptor.WorkspaceLease, adaptor.WorkspaceReleaseMode) error {
				return want
			},
		}
		fake := newFakeDriver()
		res, err := adaptor.New(fake,
			adaptor.WithWorkspaceManager(manager),
			adaptor.WithWorkspaceSpec(adaptor.SharedWorkspace{}),
		).Run(context.Background(), "go")
		if res != nil || !errors.Is(err, want) {
			t.Fatalf("Run = (%+v, %v), want nil Result and wrapped WorkspaceManager.Release error", res, err)
		}
		if fake.runCount() != 1 {
			t.Fatalf("driver runs = %d, want 1", fake.runCount())
		}
	})
}

func TestRunScopedTeardownIsBoundedWhenHookIgnoresCancellation(t *testing.T) {
	releaseHook := make(chan struct{})
	provider := &fakeProvider{
		name: "stuck-sidecar",
		log:  &callLog{},
		detach: func(context.Context, string) error {
			<-releaseHook // deliberately violates the cancellation contract
			return nil
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	type outcome struct {
		res *adaptor.Result
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		res, err := adaptor.New(newFakeDriver(), adaptor.WithRunServices(provider)).Run(ctx, "go")
		done <- outcome{res: res, err: err}
	}()

	select {
	case got := <-done:
		close(releaseHook)
		if got.res != nil || !errors.Is(got.err, context.DeadlineExceeded) {
			t.Fatalf("Run = (%+v, %v), want bounded teardown deadline failure", got.res, got.err)
		}
	case <-time.After(2 * time.Second):
		close(releaseHook)
		t.Fatal("Run remained blocked in a teardown hook that ignored cancellation")
	}
}

func TestRunScopedTeardownAttemptsEverythingAfterPumpAndHookTimeouts(t *testing.T) {
	log := &callLog{}
	providerEvents := make(chan adaptor.Event)
	stuckDetach := make(chan struct{})
	defer close(providerEvents)
	defer close(stuckDetach)

	healthyErr := errors.New("healthy provider detach failed")
	serviceErr := errors.New("runtime service release failed")
	workspaceErr := errors.New("workspace release failed")
	healthy := &fakeProvider{
		name: "healthy",
		log:  log,
		detach: func(context.Context, string) error {
			return healthyErr
		},
	}
	// Providers release in reverse acquisition order, so this provider hangs
	// before healthy, runtime, and workspace release are attempted.
	stuck := &fakeProvider{
		name: "stuck",
		log:  log,
		attachment: adaptor.RunAttachment{Events: func(context.Context, string) <-chan adaptor.Event {
			return providerEvents // deliberately ignores pump cancellation
		}},
		detach: func(context.Context, string) error {
			<-stuckDetach // deliberately ignores its cleanup context
			return nil
		},
	}
	serviceManager := &fakeServiceManager{
		log: log,
		release: func(context.Context, string) error {
			return serviceErr
		},
	}
	workspaceManager := &fakeWorkspaceManager{
		lease: adaptor.WorkspaceLease{ID: "hostile-lease", CWD: t.TempDir()},
		log:   log,
		release: func(context.Context, adaptor.WorkspaceLease, adaptor.WorkspaceReleaseMode) error {
			return workspaceErr
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	stream := adaptor.New(
		newFakeDriver(),
		adaptor.WithRunServices(healthy, stuck),
		adaptor.WithServiceManager(serviceManager),
		adaptor.WithServices(adaptor.ServiceSpec{ID: "runtime"}),
		adaptor.WithWorkspaceManager(workspaceManager),
		adaptor.WithWorkspaceSpec(adaptor.SharedWorkspace{}),
	).Stream(ctx, "go")

	type outcome struct {
		events []adaptor.Event
		res    *adaptor.Result
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		var events []adaptor.Event
		for event := range stream.Events() {
			events = append(events, event)
		}
		res, err := stream.Result()
		done <- outcome{events: events, res: res, err: err}
	}()

	var got outcome
	select {
	case got = <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("teardown exceeded its global bound")
	}
	if got.res != nil || !errors.Is(got.err, context.DeadlineExceeded) {
		t.Fatalf("run = (%+v, %v), want pump/detach deadline failure", got.res, got.err)
	}
	for _, want := range []error{healthyErr, serviceErr, workspaceErr} {
		if !errors.Is(got.err, want) {
			t.Errorf("run error %v does not aggregate %v", got.err, want)
		}
	}
	runID := stream.RunID()
	for _, want := range []string{
		"detach:stuck:" + runID,
		"detach:healthy:" + runID,
		"release_run:" + runID,
		"workspace_release",
	} {
		if !log.has(want) {
			t.Errorf("lifecycle log %v does not contain %q", log.snapshot(), want)
		}
	}
	if len(got.events) < 2 {
		t.Fatalf("events = %+v, want authoritative lifecycle", got.events)
	}
	if _, ok := got.events[0].(adaptor.RunStarted); !ok {
		t.Fatalf("first event = %T, want RunStarted", got.events[0])
	}
	if _, ok := got.events[len(got.events)-1].(adaptor.RunFinished); !ok {
		t.Fatalf("last event = %T, want RunFinished", got.events[len(got.events)-1])
	}
}

func assertTextDelta(t *testing.T, ev adaptor.Event, want string) {
	t.Helper()
	delta, ok := ev.(adaptor.TextDelta)
	if !ok {
		t.Fatalf("event = %T (%+v), want adaptor.TextDelta", ev, ev)
	}
	if delta.Text != want {
		t.Fatalf("TextDelta.Text = %q, want %q", delta.Text, want)
	}
}

func assertSubagent(t *testing.T, ev adaptor.Event, want adaptor.SubagentEventKind) {
	t.Helper()
	update, ok := ev.(adaptor.SubagentUpdate)
	if !ok {
		t.Fatalf("event = %T (%+v), want adaptor.SubagentUpdate", ev, ev)
	}
	if update.Kind != want {
		t.Fatalf("SubagentUpdate.Kind = %q, want %q", update.Kind, want)
	}
}

// ============ 4. WithServices / WithServiceManager ============

func TestWithServicesEnsuresThroughManagerAndReleases(t *testing.T) {
	log := &callLog{}
	manager := &fakeServiceManager{
		log: log,
		ensure: func(_ context.Context, req adaptor.ServiceRequest) ([]adaptor.ServiceRef, error) {
			return []adaptor.ServiceRef{{ID: "db", URL: "postgres://127.0.0.1:5432/" + req.RunID}}, nil
		},
	}

	fake := httpCapableDriver()
	agent := adaptor.New(fake,
		adaptor.WithServiceManager(manager),
		adaptor.WithServices(adaptor.ServiceSpec{ID: "db", Name: "db", Description: "test database"}),
	)
	res, err := agent.Run(context.Background(), "hi")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	req := fake.lastRequest(t)
	if len(req.Runtime.Requested) != 1 || req.Runtime.Requested[0].ID != "db" {
		t.Fatalf("Runtime.Requested = %+v, want the declared spec", req.Runtime.Requested)
	}
	if len(req.Runtime.Ensured) != 1 || req.Runtime.Ensured[0].URL == "" {
		t.Fatalf("Runtime.Ensured = %+v, want the manager's endpoint", req.Runtime.Ensured)
	}
	if got := req.Runtime.Ensured[0].Name; got != "db" {
		t.Errorf("ensured Name = %q, want it backfilled from the requested spec", got)
	}
	if got := req.Runtime.Ensured[0].Lifecycle; got != driver.RuntimeLifecycleShared {
		t.Errorf("ensured Lifecycle = %q, want the normalized default", got)
	}

	manager.mu.Lock()
	seen := len(manager.requests)
	driverType := ""
	if seen > 0 {
		driverType = manager.requests[0].DriverType
	}
	manager.mu.Unlock()
	if seen != 1 || driverType != "fake" {
		t.Errorf("Ensure requests = %d (driver type %q), want one carrying the driver type", seen, driverType)
	}
	if !log.has("release_run:" + res.RunID) {
		t.Errorf("lifecycle log = %v, want ReleaseByRun after the run", log.snapshot())
	}

	// The driver reported no runtime services, so Services() falls back to
	// the ensured refs rather than lying about having none.
	reports := res.Services()
	if len(reports) != 1 || reports[0].ID != "db" {
		t.Fatalf("Services() = %+v, want reports derived from the ensured refs", reports)
	}
	if reports[0].Status != driver.RuntimeServiceRunning || reports[0].Health != driver.RuntimeHealthUnknown {
		t.Errorf("report defaults = %+v, want the engine's status/health defaults", reports[0])
	}
}

func TestServiceReportsMergeByStableIDWithDriverPrecedence(t *testing.T) {
	log := &callLog{}
	manager := &fakeServiceManager{
		log: log,
		ensure: func(context.Context, adaptor.ServiceRequest) ([]adaptor.ServiceRef, error) {
			return []adaptor.ServiceRef{
				{ID: "db", Name: "database", URL: "postgres://sdk", Status: driver.RuntimeServiceRunning, Lifecycle: driver.RuntimeLifecycleShared, Metadata: map[string]string{"sdk": "kept", "owner": "sdk"}},
				{ID: "cache", Name: "cache", URL: "redis://sdk", Status: driver.RuntimeServiceRunning},
			}, nil
		},
	}
	fake := newFakeDriver()
	fake.response = driver.Response{
		Output: "done",
		RuntimeServices: []driver.RuntimeServiceReport{
			{ID: "db", Status: driver.RuntimeServiceFailed, Health: driver.RuntimeHealthUnhealthy, Metadata: map[string]string{"owner": "driver", "observed": "yes"}},
			{ID: "driver-only", Name: "provider helper", Status: driver.RuntimeServiceRunning},
		},
	}
	agent := adaptor.New(fake,
		adaptor.WithServiceManager(manager),
		adaptor.WithServices(adaptor.ServiceSpec{ID: "db"}, adaptor.ServiceSpec{ID: "cache"}),
	)
	res, err := agent.Run(context.Background(), "inspect services")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	reports := res.Services()
	if len(reports) != 3 {
		t.Fatalf("Services = %+v, want merged db + driver-only + cache", reports)
	}
	if reports[0].ID != "db" || reports[0].Name != "database" || reports[0].URL != "postgres://sdk" {
		t.Fatalf("matching driver report did not inherit missing SDK fields: %+v", reports[0])
	}
	if reports[0].Status != driver.RuntimeServiceFailed || reports[0].Health != driver.RuntimeHealthUnhealthy {
		t.Fatalf("driver observation did not override matching SDK fields: %+v", reports[0])
	}
	if reports[0].Metadata["sdk"] != "kept" || reports[0].Metadata["owner"] != "driver" || reports[0].Metadata["observed"] != "yes" {
		t.Fatalf("metadata merge lost precedence or fields: %+v", reports[0].Metadata)
	}
	if reports[1].ID != "driver-only" || reports[2].ID != "cache" {
		t.Fatalf("stable order/unique reports lost: %+v", reports)
	}
}

func TestWithServicesWithoutManagerStaysInert(t *testing.T) {
	fake := newFakeDriver()
	agent := adaptor.New(fake, adaptor.WithServices(adaptor.ServiceSpec{ID: "db", Name: "db"}))
	if _, err := agent.Run(context.Background(), "hi"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	req := fake.lastRequest(t)
	if len(req.Runtime.Requested) != 1 {
		t.Fatalf("Runtime.Requested = %+v, want the declaration to still reach the driver", req.Runtime.Requested)
	}
	if len(req.Runtime.Ensured) != 0 {
		t.Errorf("Runtime.Ensured = %+v, want none: the SDK must not invent endpoints", req.Runtime.Ensured)
	}
}

func TestWithServicesReplacesPerCall(t *testing.T) {
	fake := newFakeDriver()
	agent := adaptor.New(fake, adaptor.WithServices(adaptor.ServiceSpec{ID: "default"}))

	if _, err := agent.Run(context.Background(), "hi", adaptor.WithServices(adaptor.ServiceSpec{ID: "override"})); err != nil {
		t.Fatalf("Run: %v", err)
	}
	req := fake.request(t, 0)
	if len(req.Runtime.Requested) != 1 || req.Runtime.Requested[0].ID != "override" {
		t.Fatalf("Runtime.Requested = %+v, want the call-site declaration to replace the default", req.Runtime.Requested)
	}

	// An empty declaration is an explicit clear, not an inherit.
	if _, err := agent.Run(context.Background(), "hi", adaptor.WithServices()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := fake.request(t, 1).Runtime.Requested; len(got) != 0 {
		t.Fatalf("Runtime.Requested = %+v, want WithServices() to clear the default", got)
	}

	// The agent default is never mutated by a per-call override.
	if _, err := agent.Run(context.Background(), "hi"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := fake.request(t, 2).Runtime.Requested; len(got) != 1 || got[0].ID != "default" {
		t.Fatalf("Runtime.Requested = %+v, want the untouched agent default", got)
	}
}

// ============ 5. WithWorkspaceSpec / WithWorkspaceManager ============

func TestWithWorkspaceManagerLeasesAndReleases(t *testing.T) {
	log := &callLog{}
	lease := adaptor.WorkspaceLease{
		ID:           "lease-1",
		Mode:         driver.WorkspaceModeIsolated,
		StrategyType: driver.WorkspaceStrategyGitWorktree,
		CWD:          filepath.Join(t.TempDir(), "wt"),
		Fingerprint:  "fp-1",
	}
	manager := &fakeWorkspaceManager{lease: lease, log: log}

	fake := newFakeDriver()
	agent := adaptor.New(fake,
		adaptor.WithWorkspaceManager(manager),
		adaptor.WithWorkspace("/base"),
		adaptor.WithWorkspaceSpec(adaptor.GitWorktreeWorkspace{BaseRef: "main"}),
		adaptor.WithMetadata("example", "p47"),
	)
	if _, err := agent.Run(context.Background(), "hi"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	got := fake.lastRequest(t).Workspace
	if got.ID != lease.ID || got.CWD != lease.CWD || got.StrategyType != lease.StrategyType ||
		got.Mode != lease.Mode || got.Fingerprint != lease.Fingerprint {
		t.Fatalf("req.Workspace = %+v, want the managed lease %+v", got, lease)
	}
	manager.mu.Lock()
	reqs := append([]adaptor.WorkspaceRequest(nil), manager.requests...)
	released := append([]adaptor.WorkspaceReleaseMode(nil), manager.released...)
	manager.mu.Unlock()

	if len(reqs) != 1 {
		t.Fatalf("Resolve calls = %d, want 1", len(reqs))
	}
	if reqs[0].BaseCWD != "/base" {
		t.Errorf("BaseCWD = %q, want WithWorkspace(dir) to become the manager's base", reqs[0].BaseCWD)
	}
	if spec, ok := reqs[0].Spec.(adaptor.GitWorktreeWorkspace); !ok || spec.BaseRef != "main" {
		t.Errorf("Spec = %+v, want the GitWorktreeWorkspace verbatim", reqs[0].Spec)
	}
	if reqs[0].Metadata["example"] != "p47" {
		t.Errorf("Metadata = %v, want the run metadata forwarded", reqs[0].Metadata)
	}
	if len(released) != 1 || released[0] != adaptor.WorkspaceReleaseStop {
		t.Errorf("Release modes = %v, want one Stop after the run", released)
	}
}

func TestWithWorkspaceSpecWithoutManagerUsesPassthrough(t *testing.T) {
	base := t.TempDir()
	fake := newFakeDriver()
	agent := adaptor.New(fake,
		adaptor.WithWorkspace(base),
		adaptor.WithWorkspaceSpec(adaptor.DriverManagedWorkspace{}),
	)
	if _, err := agent.Run(context.Background(), "hi"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	ws := fake.lastRequest(t).Workspace
	if ws.StrategyType != driver.WorkspaceStrategyDriverManaged || ws.Mode != driver.WorkspaceModeAgentDefault {
		t.Fatalf("lease = %+v, want the spec's strategy through the passthrough manager", ws)
	}
	if ws.CWD != base {
		t.Errorf("CWD = %q, want the base directory %q", ws.CWD, base)
	}
	if ws.Fingerprint == "" || ws.ID == "" {
		t.Error("passthrough lease must carry the engine's stable ID/fingerprint")
	}
	if ws.Metadata["workspace_manager"] != "passthrough" {
		t.Errorf("lease metadata = %v, want the passthrough marker", ws.Metadata)
	}
}

func TestWorkspaceUnmanagedRunKeepsDirectLease(t *testing.T) {
	fake := newFakeDriver()
	agent := adaptor.New(fake, adaptor.WithWorkspace("/plain"))
	if _, err := agent.Run(context.Background(), "hi"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	ws := fake.lastRequest(t).Workspace
	if ws.CWD != "/plain" || ws.StrategyType != driver.WorkspaceStrategyProjectPrimary {
		t.Fatalf("lease = %+v, want the direct WithWorkspace synthesis", ws)
	}
	if ws.ID != "" || ws.Fingerprint != "" {
		t.Errorf("lease = %+v, want no manager-minted identity when no manager is involved", ws)
	}

	// An agent that configures nothing keeps an entirely empty lease: the
	// zero-cost path must not route through the passthrough manager and
	// synthesize a working directory nobody asked for.
	bare := newFakeDriver()
	if _, err := adaptor.New(bare).Run(context.Background(), "hi"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := bare.lastRequest(t).Workspace; got.CWD != "" || got.StrategyType != "" {
		t.Errorf("lease = %+v, want the zero value for an unconfigured agent", got)
	}
}

func TestWorkspaceManagerFailureIsPreLaunch(t *testing.T) {
	boom := errors.New("no worktree available")
	manager := &fakeWorkspaceManager{err: boom, log: &callLog{}}
	fake := newFakeDriver()
	agent := adaptor.New(fake, adaptor.WithWorkspaceManager(manager), adaptor.WithWorkspaceSpec(adaptor.SharedWorkspace{}))

	if _, err := agent.Run(context.Background(), "hi"); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want it to wrap %v", err, boom)
	}
	if fake.runCount() != 0 {
		t.Error("the driver must not launch when the workspace cannot be leased")
	}
}

// ============ 6. option scope + provider merge semantics ============

func TestP47OptionScopes(t *testing.T) {
	// Dual-scope options satisfy both interfaces.
	var _ adaptor.SharedOption = adaptor.WithServices()
	var _ adaptor.SharedOption = adaptor.WithWorkspaceSpec(adaptor.SharedWorkspace{})
	var _ adaptor.SharedOption = adaptor.WithRunServices()

	// Construction-only options must NOT be usable at the call site. The
	// compile-time guarantee is that they return Option; this asserts the
	// dynamic type does not accidentally satisfy CallOption too.
	if _, ok := any(adaptor.WithServiceManager(nil)).(adaptor.CallOption); ok {
		t.Error("WithServiceManager must be construction scope only")
	}
	if _, ok := any(adaptor.WithWorkspaceManager(nil)).(adaptor.CallOption); ok {
		t.Error("WithWorkspaceManager must be construction scope only")
	}
}

func TestRunServiceProvidersAppendAndDeduplicate(t *testing.T) {
	log := &callLog{}
	shared := &fakeProvider{name: "shared", log: log}
	extra := &fakeProvider{name: "extra", log: log}

	fake := newFakeDriver()
	agent := adaptor.New(fake, adaptor.WithRunServices(shared))

	// The same provider in both scopes attaches once (a second attach would
	// duplicate its MCP key and fail the run).
	res, err := agent.Run(context.Background(), "hi", adaptor.WithRunServices(shared, extra))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	attaches := 0
	for _, entry := range log.snapshot() {
		if len(entry) > 7 && entry[:7] == "attach:" {
			attaches++
		}
	}
	if attaches != 2 {
		t.Fatalf("attach calls = %d (%v), want the agent default plus the new call-site provider, deduplicated", attaches, log.snapshot())
	}
	if !log.has("attach:extra:"+res.RunID) || !log.has("attach:shared:"+res.RunID) {
		t.Errorf("lifecycle log = %v, want call-site providers to append to the agent's", log.snapshot())
	}

	// Detach runs in reverse attach order.
	entries := log.snapshot()
	detachOrder := []string{}
	for _, entry := range entries {
		if len(entry) > 7 && entry[:7] == "detach:" {
			detachOrder = append(detachOrder, entry)
		}
	}
	want := []string{"detach:extra:" + res.RunID, "detach:shared:" + res.RunID}
	if fmt.Sprint(detachOrder) != fmt.Sprint(want) {
		t.Errorf("detach order = %v, want %v (reverse acquisition order)", detachOrder, want)
	}
}

func TestRunServiceProviderReachesThreadRuns(t *testing.T) {
	log := &callLog{}
	provider := &fakeProvider{
		name: "sidecar",
		log:  log,
		attachment: adaptor.RunAttachment{
			Services: []adaptor.ServiceRef{sidecarRef("delegate-a2a", "http://127.0.0.1:65001/mcp", "RUN_TOKEN", "tok")},
		},
	}
	sf := newSessionFake("p47")
	sf.descriptor = &driver.Descriptor{
		Type:        "fake",
		DisplayName: "Fake Driver",
		Sessions:    driver.SessionCapability{SupportsResume: true},
		MCP:         driver.MCPCapability{Supported: true, Stdio: true, HTTP: true, SSE: true},
	}
	agent := adaptor.New(sf.fakeDriver,
		adaptor.WithThreadStore(memory.NewStore()),
		adaptor.WithRunServices(provider),
	)
	thread := agent.Thread("tenant/issue-1")
	if _, err := thread.Run(context.Background(), "hi"); err != nil {
		t.Fatalf("Thread.Run: %v", err)
	}
	req := sf.lastRequest(t)
	found := false
	for _, server := range req.MCP.Servers {
		if server.Key == "delegate-a2a" {
			found = true
		}
	}
	if !found {
		t.Fatalf("MCP servers = %+v, want the attachment on the Thread path too", req.MCP.Servers)
	}
	entries := log.snapshot()
	if len(entries) != 2 || entries[0][:7] != "attach:" || entries[1][:7] != "detach:" {
		t.Fatalf("lifecycle log = %v, want one attach/detach pair per thread turn", entries)
	}
}
