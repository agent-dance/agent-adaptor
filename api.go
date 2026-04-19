package agentadaptor

import "context"

type SDK interface {
	Run(ctx context.Context, prompt string, opts ...RunOption) (RunResult, error)
	Start(ctx context.Context, prompt string, opts ...RunOption) (RunHandle, error)

	Default() Runner
	Agent(name string) (Runner, error)

	Admin() AdminAPI
}

type Runner interface {
	Run(ctx context.Context, prompt string, opts ...RunOption) (RunResult, error)
	Start(ctx context.Context, prompt string, opts ...RunOption) (RunHandle, error)
}

type RunHandle interface {
	Events() <-chan RunEvent
	Wait(ctx context.Context) (RunResult, error)
	Cancel(ctx context.Context) error
}

type AdminAPI interface {
	Default() AgentAdmin
	Agent(name string) (AgentAdmin, error)
	Agents() []AgentInfo
}

type AgentAdmin interface {
	Info() AgentInfo
	CheckEnvironment(ctx context.Context) (EnvironmentReport, error)
	ListModels(ctx context.Context) ([]ModelInfo, error)
	DetectModel(ctx context.Context) (*DetectedModel, error)
	GetProfile(ctx context.Context) (AgentProfile, error)
	ConfigSchema(ctx context.Context) (*ConfigSchema, error)
	GetQuota(ctx context.Context) (QuotaReport, error)
	ListSkills(ctx context.Context) (SkillSnapshot, error)
	SyncSkills(ctx context.Context, desired []string) (SkillSnapshot, error)
}

type DriverAdapter interface {
	Descriptor() DriverDescriptor
	ValidateConfig(cfg any) error
	Run(ctx context.Context, req DriverRunRequest, sink EventSink) (DriverRunResult, error)
}

type EnvironmentAwareDriver interface {
	CheckEnvironment(ctx context.Context, cfg any) (EnvironmentReport, error)
}

type ModelAwareDriver interface {
	ListModels(ctx context.Context, cfg any) ([]ModelInfo, error)
}

type ModelDetectorDriver interface {
	DetectModel(ctx context.Context, cfg any) (*DetectedModel, error)
}

// ProfileAwareDriver lets adapters expose the effective local profile
// directory used for auth/config/skill semantics through the control plane.
//
// Built-in adapters use this to report effective CODEX_HOME,
// CLAUDE_CONFIG_DIR, or CURSOR_HOME resolution, including managed homes when
// the SDK synthesizes one.
type ProfileAwareDriver interface {
	GetProfile(ctx context.Context, cfg any, agent AgentIdentity) (AgentProfile, error)
}

// SessionCodecAwareDriver lets resume-capable adapters expose a stable session
// parameter contract instead of requiring hosts to inspect DriverSessionState.Data directly.
type SessionCodecAwareDriver interface {
	SessionCodec() SessionCodec
}

// ConfigSchemaAwareDriver lets adapters expose a runtime-hydrated config schema
// through the control plane without changing the execution contract.
type ConfigSchemaAwareDriver interface {
	ConfigSchema(ctx context.Context, cfg any) (*ConfigSchema, error)
}

// QuotaAwareDriver lets adapters expose provider quota or credit windows when
// the underlying CLI or local auth files support that probe.
type QuotaAwareDriver interface {
	GetQuota(ctx context.Context, cfg any) (QuotaReport, error)
}

type SkillAwareDriver interface {
	ListSkills(ctx context.Context, cfg any, payload SkillPayload) (SkillSnapshot, error)
	SyncSkills(ctx context.Context, cfg any, payload SkillPayload, desired []string) (SkillSnapshot, error)
}

type EventSink interface {
	Emit(event RunEvent) error
}

type AgentBinding interface {
	Adapter() DriverAdapter
	Config() any
	Defaults() AgentDefaults
}

type TypedAgentBinding[T any] interface {
	AgentBinding
	TypedConfig() T
}

type AgentDefaults struct {
	Agent        AgentIdentity
	Workspace    WorkspaceSpec
	Runtime      *WorkspaceRuntimeConfig
	Permissions  *PermissionProfile
	Skills       []string
	Instructions *InstructionsBundleRef
	Metadata     map[string]string
}

type AgentInfo struct {
	Name        string
	Default     bool
	DriverType  string
	DisplayName string
	Descriptor  DriverDescriptor
}

type DriverDescriptor struct {
	Type         string
	DisplayName  string
	Models       []ModelInfo
	ConfigSchema *ConfigSchema
	Sessions     SessionCapability
	Skills       SkillCapability
	Instructions InstructionsCapability
	Workspace    WorkspaceCapability
	Permissions  InvocationPermissionCapability
	Runtime      RuntimeCapability
}

type SessionCapability struct {
	SupportsResume bool
}

type SkillCapability struct {
	Supported bool
	Mode      SkillSyncMode
}

type InstructionsCapability struct {
	Supported bool
}

type WorkspaceCapability struct {
	Supported bool
}

type InvocationPermissionCapability struct {
	Approvals bool
	Sandbox   bool
	Search    bool
	Browser   bool
}

type RuntimeCapability struct {
	ReportsServices bool
}

// ConfigSchema describes the host-facing configuration contract for one bound
// adapter. Hosts can render these fields directly into settings UIs, CLIs, or
// diagnostics pages without changing the execution contract.
type ConfigSchema struct {
	Fields []ConfigField
}

// ConfigOption is one selectable value for a ConfigField with Type "select".
// Value is the serialized config value; Label and Description are display
// hints only.
type ConfigOption struct {
	Value       string
	Label       string
	Description string
}

// ConfigField describes one configurable adapter property.
//
// Built-in adapters currently use these conventions:
//   - Type: "text", "textarea", "number", "toggle", or "select"
//   - Default: host-facing default value when the adapter exposes one
//   - Options: selectable values for "select" fields
//   - Group: stable buckets such as "command", "model", "permissions", or "execution"
//   - Meta: adapter-specific UI hints that do not change runtime semantics
type ConfigField struct {
	Name        string
	Label       string
	Type        string
	Required    bool
	Description string
	Hint        string
	Default     any
	Options     []ConfigOption
	Group       string
	Meta        map[string]string
}
