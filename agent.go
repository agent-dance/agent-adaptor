package adaptor

import (
	"context"
	"crypto/rand"
	"encoding/hex"
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

	lifecycleMu  sync.Mutex
	closeStarted bool
	closeDone    chan struct{}
	closeErr     error

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

// Close prevents new runs and closes every persistent provider process owned
// by the Agent's configured Driver. It is idempotent; concurrent callers wait
// for the first close attempt or return ctx.Err() when their own deadline wins.
// Drivers without persistent-process support make Close a successful no-op.
func (a *Agent) Close(ctx context.Context) error {
	if a == nil {
		return ErrAgentClosed
	}
	if ctx == nil {
		ctx = context.Background()
	}
	a.lifecycleMu.Lock()
	if a.closeStarted {
		done := a.closeDone
		a.lifecycleMu.Unlock()
		select {
		case <-done:
			a.lifecycleMu.Lock()
			err := a.closeErr
			a.lifecycleMu.Unlock()
			return err
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	a.closeStarted = true
	a.closeDone = make(chan struct{})
	a.lifecycleMu.Unlock()

	var err error
	if closer, ok := a.driver.(driver.ProcessLifecycleDriver); ok {
		err = closer.CloseProcesses(ctx)
	}

	a.lifecycleMu.Lock()
	a.closeErr = err
	close(a.closeDone)
	a.lifecycleMu.Unlock()
	return err
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
