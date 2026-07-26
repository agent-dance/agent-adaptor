package codex

import (
	"context"

	agentadaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/driver"
)

// Config configures the Codex driver for the v1 consumer API:
//
//	agent := adaptor.New(codex.Driver(codex.Config{Model: "gpt-5.4"}))
//
// During the v1 staging period Config is an alias for
// [agentadaptor.CodexConfig], so struct literals remain interchangeable with
// the legacy codex.New entry point; P5 flips the alias direction so this
// package owns the concrete type (docs/api-v1-implementation-plan.md P3.1).
type Config = agentadaptor.CodexConfig

// Driver returns the Codex driver with cfg captured at construction. Pass the
// result to the v1 root constructor (adaptor.New). The legacy New/NewAdapter
// entry points are unchanged. Config validation stays deferred to run/probe
// time, matching New: Driver never panics or validates eagerly.
func Driver(cfg Config) driver.Driver {
	return configuredDriver{cfg: cfg}
}

// configuredDriver couples the stateless low-level adapter with the config it
// was constructed with. Embedding adapter keeps every optional capability
// interface (environment, models, profile, quota, skills, session codec,
// streaming, profile resources) promoted onto the v1 driver value.
type configuredDriver struct {
	adapter
	cfg Config
}

var _ driver.Driver = configuredDriver{}

// Run injects the captured config when the request does not carry one (the
// v1 execution path sends req.Config == nil) and delegates to the adapter. A
// non-nil req.Config — the legacy binding path — wins untouched.
func (d configuredDriver) Run(ctx context.Context, req driver.Request, sink driver.EventSink) (driver.Response, error) {
	return d.adapter.Run(ctx, d.requestWithConfig(req), sink)
}

// ValidateConfig validates the captured config when cfg is nil, so hosts and
// the engine can probe a v1 driver without re-supplying configuration.
// Explicit non-nil values keep the legacy adapter semantics.
func (d configuredDriver) ValidateConfig(cfg any) error {
	if cfg == nil {
		cfg = d.cfg
	}
	return d.adapter.ValidateConfig(cfg)
}

func (d configuredDriver) requestWithConfig(req driver.Request) driver.Request {
	if req.Config == nil {
		req.Config = d.cfg
	}
	return req
}
