package adaptor

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"maps"
	"sync"

	"github.com/agent-dance/agent-adaptor/driver"
)

// Driver is the provider integration SPI implemented by built-in and
// third-party agent integrations. It aliases the driver package interface so
// hosts can reference the type (struct fields, function signatures) without
// importing the SPI package.
type Driver = driver.Driver

// Runner is the single execution contract shared by Agent (stateless runs)
// and Thread (stateful conversations). Bridges, RunAs[T], and host
// decorators accept a Runner so both are interchangeable.
type Runner interface {
	// Run executes one prompt to completion through the unified event pipeline.
	Run(ctx context.Context, prompt string, opts ...CallOption) (*Result, error)
	// Stream starts one prompt and returns its live typed event stream.
	Stream(ctx context.Context, prompt string, opts ...CallOption) Stream
}

// Agent is a configured, ready-to-talk agent: one driver plus agent-level
// default settings. Construct with New; multiple agents are multiple Go
// variables (there is no central SDK object or named registry).
type Agent struct {
	driver   driver.Driver
	defaults AgentSettings

	lifecycleMu   sync.Mutex
	closeStarted  bool
	closeRunning  bool
	closeComplete bool
	closeDone     chan struct{}
	closeErr      error
	activeRuns    map[string]context.CancelFunc
	activeDone    chan struct{}

	// toolRuntime is an Agent-owned local runtime, while toolProvider projects
	// its stable endpoint into the existing run-service/MCP resolution path.
	// The interface keeps lifecycle testing independent of the transport
	// implementation hidden in internal/toolruntime.
	toolRuntime           ownedToolRuntime
	toolProvider          RunServiceProvider
	toolConfigErr         error
	toolThreadErr         error
	toolProfileMu         sync.Mutex
	toolProfiles          map[string]hostedToolProfileClaim
	toolProfileSelections map[string]hostedToolProfileSelection

	// mu guards skillSelection, the process-local skill selection override
	// installed by SelectSkills: nil means no override, while a non-nil
	// slice (including an empty slice) replaces the default skill refs for
	// every subsequent resolution.
	mu             sync.Mutex
	skillSelection []string
}

var _ Runner = (*Agent)(nil)

// New constructs an Agent from a driver and agent-level default options.
// It is the single construction entry point for built-in and third-party
// drivers alike.
//
// New panics when d is nil: a nil driver is a programmer error best caught
// at startup, not on the first Run.
func New(d driver.Driver, opts ...Option) *Agent {
	if d == nil {
		panic("adaptor.New: driver must not be nil")
	}
	a := &Agent{driver: d}
	for _, o := range opts {
		if o == nil {
			continue
		}
		o.ApplyNew(&a.defaults)
	}
	a.configureTools()
	return a
}

// Run executes one prompt to completion and returns the Result. It is
// Stream + drain + Result() — there is no separate batch execution path.
// Run consumes the same unified event pipeline and discards the events.
// Approvals still work through the OnApproval callback;
// without a handler, an "ask" approval times out into the Policy.Approvals
// fallback, since Run has no event consumer to answer it.
//
// Per-call options override the agent defaults for this invocation only
// ("nearer scope wins; skills append, everything else replaces"); the agent
// defaults are never mutated, so concurrent and successive runs do not
// pollute each other.
//
// Business failures return *RunError carrying
// the full Result; infrastructure failures (context cancellation/deadline,
// process crash, protocol breakage) return plain wrapped errors. Both travel
// the single err path.
func (a *Agent) Run(ctx context.Context, prompt string, opts ...CallOption) (*Result, error) {
	st := a.Stream(ctx, prompt, opts...)
	for range st.Events() {
		// Drain: Run has no event consumer. Default drop-mode
		// backpressure means an unread buffer never blocks the driver;
		// draining keeps blocking mode (WithBlockingEvents) live too.
	}
	return st.Result()
}

// Close prevents new runs, cancels and drains admitted runs, closes every
// persistent provider process owned by the configured Driver, then closes the
// Agent-owned Tool runtime. It is idempotent; concurrent callers wait for the
// first close attempt or return ctx.Err() when their own deadline wins.
// Drivers without persistent-process support skip the process-close phase.
func (a *Agent) Close(ctx context.Context) error {
	if a == nil {
		return ErrAgentClosed
	}
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		a.lifecycleMu.Lock()
		if a.closeComplete {
			err := a.closeErr
			a.lifecycleMu.Unlock()
			return err
		}
		if a.closeRunning {
			done := a.closeDone
			a.lifecycleMu.Unlock()
			select {
			case <-done:
				continue
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		a.closeStarted = true
		a.closeRunning = true
		a.closeDone = make(chan struct{})
		activeDone := a.activeDone
		cancels := make([]context.CancelFunc, 0, len(a.activeRuns))
		for _, cancel := range a.activeRuns {
			cancels = append(cancels, cancel)
		}
		a.lifecycleMu.Unlock()

		for _, cancel := range cancels {
			cancel()
		}
		err, complete := a.closeAttempt(ctx, activeDone)

		a.lifecycleMu.Lock()
		a.closeRunning = false
		if complete {
			a.closeComplete = true
			a.closeErr = err
		}
		close(a.closeDone)
		a.lifecycleMu.Unlock()
		return err
	}
}

func (a *Agent) closeAttempt(ctx context.Context, activeDone <-chan struct{}) (error, bool) {
	// Closing once before the drain actively unblocks Drivers whose Run waits
	// on their provider process after cancellation. The Tool listener remains
	// available while cancellation propagates through an in-flight Tool call.
	var processErr error
	if closer, ok := a.driver.(driver.ProcessLifecycleDriver); ok {
		processErr = closer.CloseProcesses(ctx)
		if ctx.Err() != nil {
			return errors.Join(processErr, ctx.Err()), false
		}
		if processErr != nil {
			// The SPI does not provide a separate "all processes are gone"
			// acknowledgement. Only nil proves that profile credentials and the
			// Tool endpoint may be revoked safely; keep cleanup retryable on any
			// other process-close result.
			return processErr, false
		}
	}

	if activeDone != nil {
		select {
		case <-activeDone:
		case <-ctx.Done():
			return ctx.Err(), false
		}
	}

	// An admitted goroutine can begin Driver.Run immediately after the first
	// process close. Draining proves it can no longer create work; a second
	// idempotent close then reaps any late-created persistent writer and
	// restores the strict post-Close no-process guarantee.
	if closer, ok := a.driver.(driver.ProcessLifecycleDriver); ok && activeDone != nil {
		processErr = errors.Join(processErr, closer.CloseProcesses(ctx))
		if ctx.Err() != nil {
			return errors.Join(processErr, ctx.Err()), false
		}
		if processErr != nil {
			return processErr, false
		}
	}

	// Remove the native profile projection while its endpoint is still live.
	// A failed removal leaves the runtime alive and Close retryable, avoiding a
	// dead endpoint in a shared provider profile.
	if profileErr := a.releaseHostedToolProfiles(ctx); profileErr != nil {
		return errors.Join(processErr, profileErr), false
	}

	var toolErr error
	if a.toolRuntime != nil {
		toolErr = a.toolRuntime.Close(ctx)
	}
	if ctx.Err() != nil {
		return errors.Join(processErr, toolErr, ctx.Err()), false
	}
	return errors.Join(processErr, toolErr), true
}

type ownedToolRuntime interface {
	Close(context.Context) error
}

func (a *Agent) ensureOpen() error {
	if a == nil {
		return ErrAgentClosed
	}
	a.lifecycleMu.Lock()
	defer a.lifecycleMu.Unlock()
	if a.closeStarted {
		return ErrAgentClosed
	}
	return nil
}

// registerRun atomically admits a fully validated invocation into the Agent
// lifecycle. Close cannot slip between an open check and goroutine start: it
// either observes and cancels this run, or registration observes Close and
// rejects the run before resource acquisition.
func (a *Agent) registerRun(runID string, cancel context.CancelFunc) error {
	if a == nil {
		return ErrAgentClosed
	}
	a.lifecycleMu.Lock()
	defer a.lifecycleMu.Unlock()
	if a.closeStarted {
		return ErrAgentClosed
	}
	if a.activeRuns == nil {
		a.activeRuns = make(map[string]context.CancelFunc)
	}
	if len(a.activeRuns) == 0 {
		a.activeDone = make(chan struct{})
	}
	a.activeRuns[runID] = cancel
	return nil
}

func (a *Agent) unregisterRun(runID string) {
	if a == nil {
		return
	}
	a.lifecycleMu.Lock()
	defer a.lifecycleMu.Unlock()
	if _, exists := a.activeRuns[runID]; !exists {
		return
	}
	delete(a.activeRuns, runID)
	if len(a.activeRuns) == 0 && a.activeDone != nil {
		close(a.activeDone)
		a.activeDone = nil
	}
}

// buildRequest maps the effective settings onto the driver SPI request.
func buildRequest(runID, prompt string, eff *RunSettings) driver.Request {
	req := driver.Request{
		RunID:         runID,
		Prompt:        prompt,
		Metadata:      maps.Clone(eff.metadata),
		ModelOverride: eff.model,
		Spawn:         eff.spawn,
		// Config stays nil: configured Drivers carry their own config
		// (codex.Driver(codex.Config{...}) captures it at construction).
		// Session stays nil here: the Thread path (thread.go) attaches
		// the per-turn session context; plain Agent runs are stateless.
	}
	if eff.identity != nil {
		req.Agent = eff.identity.driverIdentity()
	}
	if eff.workspace != "" {
		// Direct lease synthesis for the plain "run here" case. A host that
		// installs a WorkspaceManager or names a WorkspaceSpec gets a
		// managed lease instead, overlaid by runResources.applyRequest
		// after this — the directory then becomes the manager's base CWD.
		req.Workspace = driver.WorkspaceLease{
			Mode:         driver.WorkspaceModeShared,
			StrategyType: driver.WorkspaceStrategyProjectPrimary,
			CWD:          eff.workspace,
		}
	}
	// Instructions are attached by resolveRun after the merged bundle went
	// through engine.PrepareInstructionsBundle (trim, exclusivity, file
	// fingerprint) — not here, so the prepared form is the only form the
	// driver ever sees.
	if eff.policy != nil {
		req.Policy = eff.policy.driverPolicy()
	}
	return req
}

// newRunID mints the SDK-assigned execution identifier.
func newRunID() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return "run_" + hex.EncodeToString(b[:]), nil
}
