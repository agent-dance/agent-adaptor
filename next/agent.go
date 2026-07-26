package adaptor

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"maps"

	"github.com/agent-dance/agent-adaptor/driver"
)

// Driver is the adapter SPI implemented by built-in and third-party agent
// integrations. It aliases the driver package interface so hosts can
// reference the type (struct fields, function signatures) without importing
// the SPI package.
type Driver = driver.Driver

// Runner is the single execution contract shared by Agent (stateless runs)
// and Thread (stateful conversations). Bridges, RunAs[T], and host
// decorators accept a Runner so both are interchangeable.
type Runner interface {
	Run(ctx context.Context, prompt string, opts ...CallOption) (*Result, error)
	Stream(ctx context.Context, prompt string, opts ...CallOption) Stream
}

// Agent is a configured, ready-to-talk agent: one driver plus agent-level
// default settings. Construct with New; multiple agents are multiple Go
// variables (there is no central SDK object or named registry).
type Agent struct {
	driver   driver.Driver
	defaults AgentSettings
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
// Stream + drain + Result() — there is no separate batch execution path
// (P1.4): Run consumes the same unified event pipeline and discards the
// events. Approvals still work through the OnApproval callback (form A);
// without a handler, an "ask" approval times out into the Policy.Approvals
// fallback, since Run has no event consumer to answer it.
//
// Per-call options override the agent defaults for this invocation only
// ("nearer scope wins; skills append, everything else replaces"); the agent
// defaults are never mutated, so concurrent and successive runs do not
// pollute each other.
//
// Error contract (decision D1): business failures return *RunError carrying
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

// buildRequest maps the effective settings onto the driver SPI request.
func buildRequest(runID, prompt string, eff *RunSettings) driver.Request {
	req := driver.Request{
		RunID:         runID,
		Prompt:        prompt,
		Metadata:      maps.Clone(eff.metadata),
		ModelOverride: eff.model,
		// Config stays nil in P0: v1 drivers carry their own config
		// (codex.Driver(codex.Config{...}) captures it at construction).
		// TODO(P3.1): drop the field hand-off entirely when driver
		// configs move home.
		// Session stays nil here: the Thread path (thread.go) attaches
		// the per-turn session context; plain Agent runs are stateless.
	}
	if eff.identity != nil {
		req.Agent = eff.identity.driverIdentity()
	}
	if eff.workspace != "" {
		// TODO(P1–P3): WorkspaceManager resolution replaces this direct
		// lease synthesis in internal/engine.
		req.Workspace = driver.WorkspaceLease{
			Mode:         driver.WorkspaceModeShared,
			StrategyType: driver.WorkspaceStrategyProjectPrimary,
			CWD:          eff.workspace,
		}
	}
	if eff.instructions != "" {
		req.Instructions = &driver.InstructionsBundleRef{Content: eff.instructions}
	}
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
