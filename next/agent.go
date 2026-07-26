package adaptor

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"maps"

	"github.com/agent-dance/agent-adaptor/driver"
)

// Driver is the adapter SPI implemented by built-in and third-party agent
// integrations. It aliases the driver package interface so hosts can
// reference the type (struct fields, function signatures) without importing
// the SPI package.
type Driver = driver.Driver

// Runner is the single execution contract shared by Agent and, from P2 on,
// Thread. Bridges, RunAs[T], and host decorators accept a Runner so both are
// interchangeable.
//
// Stream joins the interface in P1 (decision D4: Stream is a small
// interface, one event channel, one Result() close-out).
type Runner interface {
	Run(ctx context.Context, prompt string, opts ...CallOption) (*Result, error)
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

// Run executes one prompt to completion and returns the Result.
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
	eff := a.defaults.RunSettings.clone()
	for _, o := range opts {
		if o == nil {
			continue
		}
		o.ApplyRun(&eff)
	}

	// P0 minimal execution path: merge settings → build driver.Request →
	// drive → translate Response/RunFailure into Result/RunError.
	// TODO(P1–P3): session coordination, skill materialization, profile &
	// MCP resolution, and workspace management are taken over by
	// internal/engine; this thin pipeline must not grow them.

	if eff.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, eff.timeout)
		defer cancel()
	}
	if eff.identity != nil {
		ctx = contextWithIdentity(ctx, *eff.identity)
	}

	runID, err := newRunID()
	if err != nil {
		return nil, fmt.Errorf("adaptor: generate run id: %w", err)
	}

	resp, err := a.driver.Run(ctx, buildRequest(runID, prompt, &eff), noopSink{})
	if err != nil {
		// Infrastructure failure: ctx cancellation/deadline, process
		// crash, protocol breakage. Wrap and pass through so
		// errors.Is/As reach the cause.
		return nil, fmt.Errorf("adaptor: run %s: %w", runID, err)
	}

	res := resultFromResponse(runID, resp)
	if resp.Failure != nil {
		return nil, &RunError{
			Reason:  failureReason(resp.Failure.Code),
			Message: resp.Failure.Message,
			Details: maps.Clone(resp.Failure.Metadata),
			Result:  res,
		}
	}
	return res, nil
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
		// Session stays nil in P0 (stateless). TODO(P2): Thread wiring.
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

// noopSink is the minimal EventSink for the P0 batch path. The typed event
// pipeline (with WithEventBuffer backpressure) replaces it in P1.
type noopSink struct{}

func (noopSink) Emit(driver.RunEvent) error            { return nil }
func (noopSink) EmitStream(driver.StreamPayload) error { return nil }

// newRunID mints the SDK-assigned execution identifier.
func newRunID() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return "run_" + hex.EncodeToString(b[:]), nil
}
