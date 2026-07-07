package agentadaptor

import "context"

// SDK is the host-facing entry point for executing bound local agents.
//
// The SDK is intentionally default-agent-first: Run and Start always target
// the binding supplied by WithDefaultAgent, while Agent(name) returns a Runner
// for an explicitly registered named binding. Hosts should keep routing,
// tenancy, HTTP/RPC concerns, and "which agent should handle this request"
// decisions above this interface.
type SDK interface {
	Run(ctx context.Context, prompt string, opts ...RunOption) (RunResult, error)
	Start(ctx context.Context, prompt string, opts ...RunOption) (RunHandle, error)

	Default() Runner
	Agent(name string) (Runner, error)

	Admin() AdminAPI
}

// Runner is the execution contract shared by the default binding and every
// named binding. It lets workflow code accept "the chosen agent" without
// needing access to SDK construction, Admin APIs, or the named-agent registry.
type Runner interface {
	Run(ctx context.Context, prompt string, opts ...RunOption) (RunResult, error)
	Start(ctx context.Context, prompt string, opts ...RunOption) (RunHandle, error)
}

// RunHandle represents one asynchronous execution returned by Start.
//
// Hosts use Events for operational status, StreamEvents for token/tool/HITL
// streaming when enabled, DecisionRequests/ResolveDecision for async HITL
// channel mode, and Wait for the final RunResult. All channels are closed by
// the SDK when the run ends, so consumers may range over them safely.
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
	// The error model is two-layered (see docs/run-policy.md §5):
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

// AdminAPI is the control-plane view over the same default/named agent model
// used for execution. It never executes prompts; it is for diagnostics,
// settings UIs, skill inventory, profile introspection, and capability probes.
type AdminAPI interface {
	Default() AgentAdmin
	Agent(name string) (AgentAdmin, error)
	Agents() []AgentInfo
}

// AgentAdmin exposes management probes for one bound agent. Implementations
// are backed by adapter capability interfaces, so unsupported probes return
// truthful fallback reports instead of inventing data.
type AgentAdmin interface {
	Info() AgentInfo
	CheckEnvironment(ctx context.Context) (EnvironmentReport, error)
	ListModels(ctx context.Context) ([]ModelInfo, error)
	DetectModel(ctx context.Context) (*DetectedModel, error)
	GetProfile(ctx context.Context) (AgentProfile, error)
	ProfileSnapshot(ctx context.Context) (ProfileSnapshot, error)
	SyncProfile(ctx context.Context) (ProfileSnapshot, error)
	ConfigSchema(ctx context.Context) (*ConfigSchema, error)
	GetQuota(ctx context.Context) (QuotaReport, error)
	ListSkills(ctx context.Context) (SkillSnapshot, error)
	// SetSelectedSkills overrides the process-local default selection for
	// this agent. The override replaces the binding's WithDefaultSkills
	// values for subsequent Run / Start calls on this agent; Required
	// skills from the SkillProvider continue to appear regardless. The
	// override is not persisted across process restarts.
	SetSelectedSkills(ctx context.Context, keys []string) (SkillSnapshot, error)
}

// DriverAdapter is the adapter SPI implemented by built-in and third-party
// agent integrations. The SDK owns default merging, session coordination,
// workspace/runtime/skill resolution, and result archiving; adapters own
// provider-specific validation, process/protocol execution, transcript
// parsing, and checkpoint extraction.
type DriverAdapter interface {
	Descriptor() DriverDescriptor
	ValidateConfig(cfg any) error
	Run(ctx context.Context, req DriverRunRequest, sink EventSink) (DriverRunResult, error)
}

// EnvironmentAwareDriver is implemented by adapters that can perform
// preflight checks against local CLIs, auth files, profile directories, or
// other dependencies. Admin.CheckEnvironment uses it when present.
type EnvironmentAwareDriver interface {
	CheckEnvironment(ctx context.Context, cfg any) (EnvironmentReport, error)
}

// ModelAwareDriver is implemented by adapters that can list visible model
// choices. Adapters may return static descriptor models or inspect local
// provider state when a live list is available.
type ModelAwareDriver interface {
	ListModels(ctx context.Context, cfg any) ([]ModelInfo, error)
}

// ModelDetectorDriver is implemented by adapters that can infer the effective
// model from config files, CLI defaults, profile state, or the supplied config.
type ModelDetectorDriver interface {
	DetectModel(ctx context.Context, cfg any, profile *ProfileSelection) (*DetectedModel, error)
}

// ProfileAwareDriver lets adapters expose the effective local profile
// directory used for auth/config/skill semantics through the control plane.
//
// Built-in adapters use this to report effective CODEX_HOME,
// CLAUDE_CONFIG_DIR, or CURSOR_HOME resolution, including managed homes when
// the SDK synthesizes one.
type ProfileAwareDriver interface {
	GetProfile(ctx context.Context, cfg any, agent AgentIdentity, profile *ProfileSelection) (AgentProfile, error)
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
	GetQuota(ctx context.Context, cfg any, profile *ProfileSelection) (QuotaReport, error)
}

// SkillAwareDriver is the optional adapter contract for skill-capable
// drivers. Adapters that do not implement it simply ignore skills; the SDK
// still reports an unsupported snapshot through the Admin surface.
//
// The design splits concerns across three methods:
//   - ListSkills reports the Admin-layer snapshot. selected is the SDK's
//     final selection set (Required ∪ WithDefaultSkills ∪ WithSkills) and
//     matches payload.Keys(); resolved is the full merged catalogue
//     (provider + binding-only candidates + selected skills). Adapters
//     should pass resolved through to SkillSnapshot.Resolved so the
//     Admin API can render the "available but unselected" view without
//     re-enumerating the provider.
//   - InjectSkills is invoked exactly once per Run() invocation after skill
//     resolution and before the adapter starts. It is a compatibility hook
//     for third-party adapters and should stay non-destructive unless the
//     adapter can prove the run cannot later be rejected. Built-in adapters
//     treat it as a no-op and reconcile profile-local resources inside Run()
//     after resume guards pass, because the effective profile directory is
//     only known there.
//   - SyncSkills is invoked by AgentAdmin.SetSelectedSkills to reconcile
//     the persistent / ephemeral host-side layout with the newly-chosen
//     set. It receives both the selected keys and the full resolved
//     catalogue for the same reason as ListSkills.
//
// Invariants the SDK guarantees to adapters:
//
//   - selected == payload.Keys() for both ListSkills and SyncSkills.
//     Adapters MAY rely on this equality when building snapshots.
//   - resolved is always a superset of payload.Entries (by Key): every
//     materialised entry has a Skill in resolved, but resolved may
//     additionally contain unselected candidates.
//
// Invariants adapters MUST preserve:
//
//   - The Resolved slice returned in SkillSnapshot MUST describe the full
//     merged catalogue the SDK passed in. Adapters are free to clone or
//     reorder it; they MUST NOT silently drop entries.
type SkillAwareDriver interface {
	ListSkills(ctx context.Context, cfg any, payload ResolvedSkills, selected []string, resolved []Skill, profile *ProfileSelection) (SkillSnapshot, error)
	InjectSkills(ctx context.Context, cfg any, payload ResolvedSkills, profile *ProfileSelection) error
	SyncSkills(ctx context.Context, cfg any, payload ResolvedSkills, selected []string, resolved []Skill, profile *ProfileSelection) (SkillSnapshot, error)
}

// EventSink is the per-run event surface adapters write into while executing.
// Emit carries operational RunEvent data; EmitStream carries normalized
// token/tool/reasoning/HITL payloads when streaming is enabled. Adapters should
// not retain the sink after Run returns.
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
	// are exposed on StreamEvents(). The stream event is the broadcast
	// channel; the decision path, when supported, still goes through
	// DecisionCapableSink and RunHandle.DecisionRequests / ResolveDecision.
	HITL bool
}

// AgentBinding couples one DriverAdapter with its validated config and
// binding-level defaults. Built-in packages return AgentBinding values from
// New(cfg, opts...), and custom adapters can use Bind or BindTyped.
type AgentBinding interface {
	Adapter() DriverAdapter
	Config() any
	Defaults() AgentDefaults
}

// TypedAgentBinding is an AgentBinding that also exposes the concrete config
// type. It is useful in tests, admin tooling, or custom adapter plumbing that
// wants to inspect strongly-typed configuration after binding.
type TypedAgentBinding[T any] interface {
	AgentBinding
	TypedConfig() T
}

// AgentDefaults are binding-level defaults merged into every Run/Start call
// before per-call RunOptions are applied. They are copied on binding
// construction and when returned from AgentBinding.Defaults, so callers may
// inspect the value without mutating live SDK state.
type AgentDefaults struct {
	Agent           AgentIdentity
	Workspace       WorkspaceSpec
	Runtime         *WorkspaceRuntimeConfig
	RunPolicy       *RunPolicy
	Skills          []SkillRef
	MCP             *MCPConfig
	Agents          []AgentSpec
	Hooks           []HookSpec
	ProfileConfig   []ProfileConfigPatch
	Profile         *ProfileSelection
	Instructions    *InstructionsBundleRef
	Metadata        map[string]string
	profileDeclared ProfileResourceDeclarations
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

// AgentInfo is the admin/listing view of a bound agent. Hosts commonly use it
// to render settings screens, capability badges, and named-agent pickers.
type AgentInfo struct {
	Name        string
	Default     bool
	DriverType  string
	DisplayName string
	Descriptor  DriverDescriptor
}

// DriverDescriptor is the adapter's static capability declaration. The SDK
// uses it to validate host requests before launching an adapter; hosts can use
// it to disable unsupported UI controls instead of discovering failures late.
type DriverDescriptor struct {
	Type             string
	DisplayName      string
	Models           []ModelInfo
	ConfigSchema     *ConfigSchema
	Sessions         SessionCapability
	Skills           SkillCapability
	MCP              MCPCapability
	Instructions     InstructionsCapability
	Workspace        WorkspaceCapability
	RunPolicyCaps    RunPolicyCapabilities
	Runtime          RuntimeCapability
	StructuredOutput StructuredOutputCapability
}

// SessionCapability declares whether an adapter can resume provider sessions.
type SessionCapability struct {
	SupportsResume bool
}

// SkillCapability declares whether an adapter consumes resolved skills and
// whether its skill state is ephemeral per run or persistent in the profile.
type SkillCapability struct {
	Supported bool
	Mode      SkillSyncMode
}

// InstructionsCapability declares whether the adapter accepts explicit
// instruction bundles in addition to the prompt.
type InstructionsCapability struct {
	Supported bool
}

// WorkspaceCapability declares whether an adapter can honor SDK-resolved
// workspace leases.
type WorkspaceCapability struct {
	Supported bool
}

// RuntimeCapability declares whether an adapter reports runtime-service state
// back in RunResult.RuntimeServices.
type RuntimeCapability struct {
	ReportsServices bool
}

// StructuredOutputCapability declares which runtime structured-output modes
// the adapter can honor. JSONSchemaNative means a provider/CLI-native schema
// surface exists; JSONSchemaPromptValidate means the adapter can accept the
// SDK's explicit prompt+local-validation fallback.
type StructuredOutputCapability struct {
	JSONSchemaNative         bool
	JSONSchemaPromptValidate bool

	WorksWithRun       bool
	WorksWithStart     bool
	WorksWithStreaming bool
	WorksWithHITL      bool

	Notes string
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
