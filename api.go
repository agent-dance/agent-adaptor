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
	// Events streams operational RunEvents (chunks, lifecycle, spawn, etc.).
	Events() <-chan RunEvent
	// StreamEvents streams normalized StreamPayloads produced by
	// stream-aware adapters. When streaming was not enabled for the run (or
	// the adapter does not emit them) the returned channel is closed and
	// empty; consumers can `for range` it safely.
	StreamEvents() <-chan StreamPayload
	// RunID returns the stable run identifier assigned by the SDK. It is
	// available as soon as Start() returns, before Wait() completes.
	RunID() string

	// Wait blocks until the run terminates or ctx is cancelled.
	//
	// The error model is two-layered (see docs/workstream-hitl-v2.md §3.5):
	//
	//   - The returned error is an infrastructure-layer error — the agent
	//     did not finish (ctx cancel, adapter crash, protocol break, SDK
	//     panic recovery). RunResult fields may be incomplete.
	//   - RunResult.Failure is the business-layer failure — the agent ran
	//     to completion but the result is a failure (HITL reject / timeout,
	//     adapter-declared error, policy validation). RunResult fields are
	//     complete.
	//
	// Hosts should check err first, then Failure, then success.
	Wait(ctx context.Context) (RunResult, error)
	Cancel(ctx context.Context) error

	// DecisionRequests returns the channel of HITL DecisionRequest envelopes
	// destined for host-side resolution. It only carries requests whose Kind
	// does not have a typed handler mounted (see WithPermissionHandler /
	// WithPlanReviewHandler / WithQuestionHandler). Consumers can `for range`
	// it safely; the channel is closed when the run ends.
	DecisionRequests() <-chan DecisionRequest

	// ResolveDecision resolves a DecisionRequest delivered through
	// DecisionRequests(). Returns ErrDecisionRequestExpired if the request is
	// unknown or already consumed; ErrDecisionResultKindMismatch if Result is
	// incompatible with the request Kind; ErrRunEnded when the run has
	// already terminated.
	ResolveDecision(requestID string, resp DecisionResponse) error
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
	// Emit publishes a RunEvent on the run-scoped event channel.
	Emit(event RunEvent) error
	// EmitStream publishes a structured StreamPayload on the run-scoped
	// stream channel. When streaming is disabled for the enclosing run the
	// sink silently discards the payload; adapters may call EmitStream
	// unconditionally. Sequence and Timestamp are backfilled by the SDK.
	EmitStream(payload StreamPayload) error
}

// StreamAwareDriver is the optional contract implemented by adapters that can
// produce normalized StreamPayload events. Hosts can advertise the adapter's
// streaming capabilities (e.g. whether it supports token-level text deltas)
// through Admin introspection without changing the execution contract.
type StreamAwareDriver interface {
	StreamCapability() StreamCapability
}

// StreamCapability describes what kinds of streaming fidelity an adapter can
// deliver. Every field is additive: bridges should degrade gracefully when a
// capability is false (e.g. synthesize a single TOOL_CALL_START when
// ToolCallArgs is false).
type StreamCapability struct {
	// Native reports whether the adapter speaks a natively event-based
	// protocol with the underlying CLI or service (as opposed to parsing
	// free-form stdout).
	Native bool
	// TokenLevel reports whether text deltas arrive at character granularity
	// or finer (i.e. multiple deltas per assistant message).
	TokenLevel bool
	// Reasoning reports whether reasoning / thinking deltas are exposed.
	Reasoning bool
	// ToolCallArgs reports whether tool-call argument streaming is exposed
	// (StreamToolCallArgs events). When false, StreamToolCallStart carries a
	// complete Args snapshot instead.
	ToolCallArgs bool
	// HITL reports whether human-in-the-loop approval / user-input events
	// are exposed. In v1 these are audit-only (see StreamHITLRequested).
	HITL bool
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
	RunPolicy    *RunPolicy
	Skills       []string
	Instructions *InstructionsBundleRef
	Metadata     map[string]string
	// Streaming marks the binding as streaming-by-default when non-nil and
	// true. Per-call WithStreaming / WithoutStreaming still wins. Using a
	// pointer keeps the three states (nil / true / false) distinct so that
	// clones do not accidentally downgrade an opt-out to a default.
	Streaming *bool

	// per-Kind typed HITL handlers bound at agent level. Per-call
	// WithPermissionHandler / WithPlanReviewHandler / WithQuestionHandler
	// override these.
	PermissionHandler PermissionHandler
	PlanReviewHandler PlanReviewHandler
	QuestionHandler   QuestionHandler
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
	RunPolicyCaps RunPolicyCapabilities
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
