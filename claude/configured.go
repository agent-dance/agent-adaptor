package claude

import (
	"context"

	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/engine"
)

// CommonConfig carries the provider-independent CLI and process defaults embedded
// by Config: command, cwd, env, instructions, prompt templates, workspace
// strategy/runtime, timeouts, and extra args.
type CommonConfig = driver.CommonConfig

// ThinkingEffort is the Claude thinking effort flag value ("low", "medium",
// "high"). The empty value leaves the CLI default in place.
type ThinkingEffort string

// Config configures the Claude driver. CommonConfig controls process,
// profile, and workspace defaults. Model, Effort, and MaxTurnsPerRun control
// provider-specific execution settings.
type Config struct {
	CommonConfig
	// Model selects the Claude model. Empty uses the driver's default.
	Model string
	// Effort overrides the Claude thinking effort. Empty uses the CLI default.
	Effort ThinkingEffort
	// MaxTurnsPerRun limits agent turns within one invocation. Zero uses the CLI default.
	MaxTurnsPerRun int
}

// engineConfig converts the package-owned Config into the engine config the
// adapter path consumes. The conversion is field-by-field on purpose: a new
// engine field cannot silently appear on the public Config, and a new Config
// field must be routed here explicitly.
func (c Config) engineConfig() engine.ClaudeConfig {
	return engine.ClaudeConfig{
		CommonConfig:   engineCommonConfig(c.CommonConfig),
		Model:          c.Model,
		Effort:         engine.ThinkingEffort(c.Effort),
		MaxTurnsPerRun: c.MaxTurnsPerRun,
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

// Driver returns the Claude driver with cfg captured at construction. Pass
// the result to adaptor.New. Validation remains deferred to run/probe time;
// construction only snapshots configuration and performs no environment I/O.
func Driver(cfg Config) driver.Driver {
	return configuredDriver{adapter: adapter{persistent: newPersistentPool()}, cfg: cloneConfig(cfg)}
}

// configuredDriver couples the stateless low-level adapter with the config it
// was constructed with. Embedding adapter keeps every optional capability
// interface (environment, models, profile, quota, skills, session codec,
// streaming, profile resources) promoted onto the configured driver value.
type configuredDriver struct {
	adapter
	cfg Config
}

var _ driver.Driver = configuredDriver{}
var _ driver.SessionConfigFingerprinter = configuredDriver{}

const sessionConfigFingerprintDomain = DriverType + "/configured-driver/v1;session-codec/v1"

// SessionConfigFingerprint identifies every provider-visible construction
// setting together with the Claude session-codec contract. It intentionally
// hashes the captured snapshot rather than exposing it through Config() any.
func (d configuredDriver) SessionConfigFingerprint() (string, error) {
	return driver.CanonicalSessionConfigFingerprint(sessionConfigFingerprintDomain, d.cfg)
}

// Run injects the captured config when the request does not carry one (the
// root execution path sends req.Config == nil) and delegates to the adapter.
// A direct SPI caller's explicit non-nil request config wins untouched.
func (d configuredDriver) Run(ctx context.Context, req driver.Request, sink driver.EventSink) (driver.Response, error) {
	return d.adapter.Run(ctx, d.requestWithConfig(req), sink)
}

// ValidateConfig validates the captured config when cfg is nil, so hosts and
// the engine can probe a configured driver without re-supplying configuration.
// Explicit non-nil values remain the direct SPI caller's responsibility.
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
