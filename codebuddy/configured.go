package codebuddy

import (
	"context"

	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/engine"
)

// CommonConfig carries the provider-independent CLI and process defaults embedded
// by Config: command, cwd, env, instructions, prompt templates, workspace
// strategy/runtime, timeouts, and extra args.
type CommonConfig = driver.CommonConfig

// ThinkingEffort is the CodeBuddy reasoning effort value: "minimal", "low",
// "medium", "high", "xhigh", or "max". The empty value leaves the CLI
// default in place.
type ThinkingEffort string

// PermissionMode is the CodeBuddy `--permission-mode` flag value.
type PermissionMode string

const (
	// PermissionUnset lets the driver derive the mode from run policy.
	PermissionUnset PermissionMode = ""
	// PermissionDefault prompts on first use of each tool.
	PermissionDefault PermissionMode = "default"
	// PermissionAcceptEdits auto-accepts file edit permissions.
	PermissionAcceptEdits PermissionMode = "acceptEdits"
	// PermissionPlan restricts the agent to analysis / planning.
	PermissionPlan PermissionMode = "plan"
	// PermissionAuto lets an AI classifier auto-approve safe actions.
	PermissionAuto PermissionMode = "auto"
	// PermissionDontAsk runs pre-approved actions and denies the rest.
	PermissionDontAsk PermissionMode = "dontAsk"
	// PermissionBypass skips all permission prompts.
	PermissionBypass PermissionMode = "bypassPermissions"
)

// Config configures the CodeBuddy driver. PermissionMode is the CodeBuddy
// headless permission mode; when empty the driver derives it from the run
// policy. MaxTurnsPerRun limits one invocation, not the lifetime of a
// resumable thread.
type Config struct {
	CommonConfig
	// Model selects the CodeBuddy model. Empty uses the driver's default.
	Model string
	// Effort overrides the CodeBuddy reasoning effort.
	Effort ThinkingEffort
	// PermissionMode selects the CodeBuddy headless permission policy.
	PermissionMode PermissionMode
	// MaxTurnsPerRun limits agent turns within one invocation. Zero uses the CLI default.
	MaxTurnsPerRun int
}

// Driver returns the CodeBuddy driver with cfg captured at construction. Pass
// the result to adaptor.New. Config validation stays
// deferred to run/probe time: Driver never panics or validates eagerly.
func Driver(cfg Config) driver.Driver {
	return configuredDriver{adapter: adapter{persistent: newPersistentPool()}, cfg: cloneConfig(cfg)}
}

// configuredDriver couples the stateless low-level adapter with the config it
// was constructed with. Embedding adapter keeps every optional capability
// interface (environment, models, profile, skills, session codec, streaming,
// profile resources) promoted onto the configured driver value.
type configuredDriver struct {
	adapter
	cfg Config
}

var _ driver.Driver = configuredDriver{}
var _ driver.SessionConfigFingerprinter = configuredDriver{}

const sessionConfigFingerprintDomain = DriverType + "/configured-driver/v1;session-codec/v1"

// SessionConfigFingerprint identifies every provider-visible construction
// setting together with the CodeBuddy session-codec contract. It intentionally
// hashes the captured snapshot rather than exposing it through Config() any.
func (d configuredDriver) SessionConfigFingerprint() (string, error) {
	return driver.CanonicalSessionConfigFingerprint(sessionConfigFingerprintDomain, d.cfg)
}

// Run injects the captured config when the request does not carry one (the
// root execution path sends req.Config == nil) and delegates to the adapter. An
// explicit package Config supplied by a direct SPI caller wins untouched.
func (d configuredDriver) Run(ctx context.Context, req driver.Request, sink driver.EventSink) (driver.Response, error) {
	return d.adapter.Run(ctx, d.requestWithConfig(req), sink)
}

// ValidateConfig validates the captured config when cfg is nil, so hosts and
// the engine can probe a configured driver without re-supplying configuration.
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
