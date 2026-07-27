package codex

import (
	"context"

	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/engine"
)

// CommonConfig carries the adapter-independent CLI/process defaults embedded
// by Config: command, cwd, env, instructions, prompt templates, workspace
// strategy/runtime, timeouts, and extra args.
type CommonConfig = driver.CommonConfig

// ReasoningEffort is the Codex reasoning effort flag value ("low", "medium",
// "high"). The empty value leaves the CLI default in place.
type ReasoningEffort string

// Config configures the Codex driver for the v1 consumer API:
//
//	agent := adaptor.New(codex.Driver(codex.Config{Model: "gpt-5.4"}))
//
// This package owns the concrete type. CommonConfig controls
// process/profile/workspace defaults; Model and ReasoningEffort map to Codex
// model settings. The execution and probe paths consume this concrete value
// directly; it is not an alias for an internal engine configuration.
type Config struct {
	CommonConfig
	Model           string
	ReasoningEffort ReasoningEffort
	FastMode        bool
}

// Driver returns the Codex driver with cfg captured at construction. Pass the
// result to the v1 root constructor (adaptor.New). Config validation stays
// deferred to run/probe time: Driver never panics or validates eagerly.
func Driver(cfg Config) driver.Driver {
	return configuredDriver{cfg: cloneConfig(cfg)}
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
var _ driver.SessionConfigFingerprinter = configuredDriver{}

const sessionConfigFingerprintDomain = DriverType + "/configured-driver/v1;session-codec/v1"

// SessionConfigFingerprint identifies every provider-visible construction
// setting together with the Codex session-codec contract. It intentionally
// hashes the captured snapshot rather than exposing it through Config() any.
func (d configuredDriver) SessionConfigFingerprint() (string, error) {
	return driver.CanonicalSessionConfigFingerprint(sessionConfigFingerprintDomain, d.cfg)
}

// Run injects the captured config when the request does not carry one (the
// v1 execution path sends req.Config == nil) and delegates to the adapter.
// An explicit non-nil SPI request config wins untouched.
func (d configuredDriver) Run(ctx context.Context, req driver.Request, sink driver.EventSink) (driver.Response, error) {
	return d.adapter.Run(ctx, d.requestWithConfig(req), sink)
}

// ValidateConfig validates the captured config when cfg is nil, so hosts and
// the engine can probe a v1 driver without re-supplying configuration.
// Explicit non-nil values are delegated unchanged for direct SPI probes.
func (d configuredDriver) ValidateConfig(cfg any) error {
	return d.adapter.ValidateConfig(d.configOrCaptured(cfg))
}

func (d configuredDriver) CheckEnvironment(ctx context.Context, cfg any) (driver.EnvironmentReport, error) {
	return d.adapter.CheckEnvironment(ctx, d.configOrCaptured(cfg))
}

func (d configuredDriver) ListModels(ctx context.Context, cfg any) ([]driver.ModelInfo, error) {
	return d.adapter.ListModels(ctx, d.configOrCaptured(cfg))
}

func (d configuredDriver) DetectModel(ctx context.Context, cfg any, profile *driver.ProfileSelection) (*driver.DetectedModel, error) {
	return d.adapter.DetectModel(ctx, d.configOrCaptured(cfg), profile)
}

func (d configuredDriver) GetProfile(ctx context.Context, cfg any, agent driver.AgentIdentity, profile *driver.ProfileSelection) (driver.AgentProfile, error) {
	return d.adapter.GetProfile(ctx, d.configOrCaptured(cfg), agent, profile)
}

func (d configuredDriver) ConfigSchema(ctx context.Context, cfg any) (*driver.ConfigSchema, error) {
	return d.adapter.ConfigSchema(ctx, d.configOrCaptured(cfg))
}

func (d configuredDriver) GetQuota(ctx context.Context, cfg any, profile *driver.ProfileSelection) (driver.QuotaReport, error) {
	return d.adapter.GetQuota(ctx, d.configOrCaptured(cfg), profile)
}

func (d configuredDriver) ListSkills(ctx context.Context, cfg any, payload driver.ResolvedSkills, selected []string, resolved []driver.Skill, profile *driver.ProfileSelection) (driver.SkillSnapshot, error) {
	return d.adapter.ListSkills(ctx, d.configOrCaptured(cfg), payload, selected, resolved, profile)
}

func (d configuredDriver) InjectSkills(ctx context.Context, cfg any, payload driver.ResolvedSkills, profile *driver.ProfileSelection) error {
	return d.adapter.InjectSkills(ctx, d.configOrCaptured(cfg), payload, profile)
}

func (d configuredDriver) SyncSkills(ctx context.Context, cfg any, payload driver.ResolvedSkills, selected []string, resolved []driver.Skill, profile *driver.ProfileSelection) (driver.SkillSnapshot, error) {
	return d.adapter.SyncSkills(ctx, d.configOrCaptured(cfg), payload, selected, resolved, profile)
}

func (d configuredDriver) SnapshotProfileResources(ctx context.Context, cfg any, agent driver.AgentIdentity, profile *driver.ProfileSelection, payload driver.ProfilePayload, selected []string, resolved []driver.Skill) (engine.ProfileSnapshot, error) {
	return d.adapter.SnapshotProfileResources(ctx, d.configOrCaptured(cfg), agent, profile, payload, selected, resolved)
}

func (d configuredDriver) SyncProfileResources(ctx context.Context, cfg any, agent driver.AgentIdentity, profile *driver.ProfileSelection, payload driver.ProfilePayload, selected []string, resolved []driver.Skill) (engine.ProfileSnapshot, error) {
	return d.adapter.SyncProfileResources(ctx, d.configOrCaptured(cfg), agent, profile, payload, selected, resolved)
}

func (d configuredDriver) configOrCaptured(cfg any) any {
	if cfg == nil {
		return cloneConfig(d.cfg)
	}
	return cfg
}

func (d configuredDriver) requestWithConfig(req driver.Request) driver.Request {
	if req.Config == nil {
		req.Config = cloneConfig(d.cfg)
	}
	return req
}

func cloneConfig(cfg Config) Config {
	cfg.CommonConfig = cfg.CommonConfig.Clone()
	return cfg
}
