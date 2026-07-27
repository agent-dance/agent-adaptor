package cursor

import (
	"context"

	agentadaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/engine"
)

// CommonConfig carries the adapter-independent CLI/process defaults embedded
// by Config: command, cwd, env, instructions, prompt templates, workspace
// strategy/runtime, timeouts, and extra args.
type CommonConfig = driver.CommonConfig

// Mode is the Cursor Agent mode flag value. The empty value leaves the CLI
// default in place.
type Mode string

// Config configures the Cursor driver for the v1 consumer API:
//
//	agent := adaptor.New(cursor.Driver(cursor.Config{Model: "gpt-5"}))
//
// This package owns the concrete type; translating it into the execution
// engine's config shape is an internal detail of this package
// (docs/p5.2-recon.md D-P5.2-2).
type Config struct {
	CommonConfig
	Model string
	Mode  Mode
}

// engineConfig converts the package-owned Config into the engine config the
// adapter path consumes. The conversion is field-by-field on purpose: a new
// engine field cannot silently appear on the public Config, and a new Config
// field must be routed here explicitly.
func (c Config) engineConfig() engine.CursorConfig {
	return engine.CursorConfig{
		CommonConfig: engineCommonConfig(c.CommonConfig),
		Model:        c.Model,
		Mode:         engine.CursorMode(c.Mode),
	}
}

func engineCommonConfig(c CommonConfig) engine.CommonConfig {
	c = c.Clone()
	var workspaceStrategy *engine.WorkspaceStrategy
	if c.WorkspaceStrategy != nil {
		workspaceStrategy = &engine.WorkspaceStrategy{
			Type:              c.WorkspaceStrategy.Type,
			BaseRef:           c.WorkspaceStrategy.BaseRef,
			BranchTemplate:    c.WorkspaceStrategy.BranchTemplate,
			WorktreeParentDir: c.WorkspaceStrategy.WorktreeParentDir,
		}
	}
	var workspaceRuntime *engine.WorkspaceRuntimeConfig
	if c.WorkspaceRuntime != nil {
		workspaceRuntime = &engine.WorkspaceRuntimeConfig{
			Services: append([]driver.RuntimeServiceSpec(nil), c.WorkspaceRuntime.Services...),
		}
	}
	return engine.CommonConfig{
		Command:                 c.Command,
		CWD:                     c.CWD,
		Env:                     append([]driver.EnvBinding(nil), c.Env...),
		Instructions:            c.Instructions,
		PromptTemplate:          c.PromptTemplate,
		BootstrapPromptTemplate: c.BootstrapPromptTemplate,
		WorkspaceStrategy:       workspaceStrategy,
		WorkspaceRuntime:        workspaceRuntime,
		Timeout:                 c.Timeout,
		GracePeriod:             c.GracePeriod,
		ExtraArgs:               append([]string(nil), c.ExtraArgs...),
	}
}

// Driver returns the Cursor driver with cfg captured at construction. Pass
// the result to the v1 root constructor (adaptor.New). The legacy
// New/NewAdapter entry points are unchanged. Config validation stays deferred
// to run/probe time, matching New: Driver never panics or validates eagerly.
func Driver(cfg Config) driver.Driver {
	return configuredDriver{cfg: cloneConfig(cfg)}
}

// configuredDriver couples the stateless low-level adapter with the config it
// was constructed with. Embedding adapter keeps every optional capability
// interface (environment, models, profile, quota, skills, session codec,
// profile resources) promoted onto the v1 driver value.
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

func (d configuredDriver) SnapshotProfileResources(ctx context.Context, cfg any, agent driver.AgentIdentity, profile *driver.ProfileSelection, payload driver.ProfilePayload, selected []string, resolved []driver.Skill) (agentadaptor.ProfileSnapshot, error) {
	return d.adapter.SnapshotProfileResources(ctx, d.configOrCaptured(cfg), agent, profile, payload, selected, resolved)
}

func (d configuredDriver) SyncProfileResources(ctx context.Context, cfg any, agent driver.AgentIdentity, profile *driver.ProfileSelection, payload driver.ProfilePayload, selected []string, resolved []driver.Skill) (agentadaptor.ProfileSnapshot, error) {
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
