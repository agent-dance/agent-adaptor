package driver

import "context"

// Driver is the canonical SPI implemented by built-in and third-party agent
// integrations. The SDK owns option merging, Thread coordination,
// workspace/runtime/skill resolution, and result archiving; drivers own
// provider-specific validation, process/protocol execution, transcript
// parsing, and checkpoint extraction.
type Driver interface {
	Descriptor() Descriptor
	// ValidateConfig validates the Driver's construction-time configuration.
	// A configured Driver MUST interpret nil as "validate the captured
	// configuration". The root runner calls this before every launch and wraps
	// failures in InvalidDriverConfigError.
	ValidateConfig(cfg any) error
	// Run executes exactly one resolved invocation. When req.Streaming is
	// true, normalized payloads obey the lifecycle contract documented on
	// StreamKind; all RunEventItem values emitted through sink must mirror the
	// returned Response.Transcript. A Driver MUST apply the complete current
	// Request on every invocation, including a resumed provider conversation;
	// it must refresh provider-visible profile, MCP, skill, instruction, and
	// runtime bindings rather than relying on values cached by an earlier
	// process or turn. A non-nil error is an infrastructure or execution error
	// and makes any returned valid Checkpoint invalid.
	Run(ctx context.Context, req Request, sink EventSink) (Response, error)
}

// ProcessLifecycleDriver is the lifecycle contract implemented by every Driver
// whose Descriptor.Process.Persistent is true. CloseProcesses must be
// idempotent, stop every process owned by that configured Driver (including
// child process groups), and treat ctx as a hard upper bound. A persistent
// declaration without this interface is a capability-contract violation.
type ProcessLifecycleDriver interface {
	CloseProcesses(ctx context.Context) error
}

// EnvironmentProbe is implemented by drivers that can perform preflight
// checks against local CLIs, auth files, profile directories, or other
// dependencies. Agent.Inspect().Environment uses it when present.
type EnvironmentProbe interface {
	CheckEnvironment(ctx context.Context, cfg any) (EnvironmentReport, error)
}

// ModelLister is implemented by drivers that can list visible model choices.
// Drivers may return static descriptor models or inspect local provider
// state when a live list is available.
type ModelLister interface {
	ListModels(ctx context.Context, cfg any) ([]ModelInfo, error)
}

// ModelDetector is implemented by drivers that can infer the effective model
// from config files, CLI defaults, profile state, or the supplied config.
type ModelDetector interface {
	DetectModel(ctx context.Context, cfg any, profile *ProfileSelection) (*DetectedModel, error)
}

// ProfileReporter lets drivers report the effective local profile directory
// used for auth, config, MCP, and skill semantics. Agent.ProfileState and
// Agent.SyncProfile use it when the driver does not implement richer profile
// resource inspection.
//
// Built-in drivers use this to report effective CODEX_HOME,
// CLAUDE_CONFIG_DIR, or CURSOR_HOME resolution, including managed homes when
// the SDK synthesizes one.
type ProfileReporter interface {
	GetProfile(ctx context.Context, cfg any, agent AgentIdentity, profile *ProfileSelection) (AgentProfile, error)
}

// SessionCodecProvider exposes the stable, deterministic session mapping used
// for resume compatibility. A Driver MUST implement this interface with a
// non-nil, non-typed-nil codec if and only if
// Descriptor.Sessions.SupportsResume is true. Resume-capable Drivers MUST also
// implement SessionConfigFingerprinter; Thread prelaunch rejects either
// missing contract before acquiring store leases or invoking Driver.Run.
type SessionCodecProvider interface {
	SessionCodec() SessionCodec
}

// ConfigSchemaProvider lets drivers expose a runtime-hydrated config schema
// through Agent.Inspect().ConfigSchema without changing execution semantics.
type ConfigSchemaProvider interface {
	ConfigSchema(ctx context.Context, cfg any) (*ConfigSchema, error)
}

// QuotaProbe lets drivers expose provider quota or credit windows when the
// underlying CLI or local auth files support that probe.
type QuotaProbe interface {
	GetQuota(ctx context.Context, cfg any, profile *ProfileSelection) (QuotaReport, error)
}

// SkillSupport is the optional driver contract for skill-capable drivers.
// Drivers that do not implement it simply ignore skills; the SDK still
// reports an unsupported snapshot through Agent.Inspect().Skills.
//
// The design splits concerns across three methods:
//   - ListSkills reports the read-only inspection snapshot. selected is the
//     final selection set (required skills plus Agent defaults and any active
//     SelectSkills selection) and matches payload.Keys(); resolved is the full
//     merged catalogue (provider candidates plus selected skills). Drivers
//     should pass resolved through to SkillSnapshot.Resolved so the
//     inspection API can render the "available but unselected" view without
//     re-enumerating the provider.
//   - InjectSkills is invoked exactly once per resolved invocation after skill
//     resolution and before the driver starts. It is an optional pre-launch
//     materialization hook and should stay non-destructive unless the
//     driver can prove the run cannot later be rejected. Built-in drivers
//     treat it as a no-op and reconcile profile-local resources inside Run()
//     after resume guards pass, because the effective profile directory is
//     only known there.
//   - SyncSkills is invoked by Agent.SelectSkills and Agent.SyncProfile to
//     reconcile the persistent or ephemeral provider layout with the selected
//     set. It receives both the selected keys and the full resolved catalogue
//     for the same reason as ListSkills.
//
// Invariants the SDK guarantees to drivers:
//
//   - selected == payload.Keys() for both ListSkills and SyncSkills.
//     Drivers MAY rely on this equality when building snapshots.
//   - resolved is always a superset of payload.Entries (by Key): every
//     materialised entry has a Skill in resolved, but resolved may
//     additionally contain unselected candidates.
//
// Invariants drivers MUST preserve:
//
//   - The Resolved slice returned in SkillSnapshot MUST describe the full
//     merged catalogue the SDK passed in. Drivers are free to clone or
//     reorder it; they MUST NOT silently drop entries.
type SkillSupport interface {
	ListSkills(ctx context.Context, cfg any, payload ResolvedSkills, selected []string, resolved []Skill, profile *ProfileSelection) (SkillSnapshot, error)
	InjectSkills(ctx context.Context, cfg any, payload ResolvedSkills, profile *ProfileSelection) error
	SyncSkills(ctx context.Context, cfg any, payload ResolvedSkills, selected []string, resolved []Skill, profile *ProfileSelection) (SkillSnapshot, error)
}

// EventSink is the per-run event surface drivers write into while executing.
// Emit accepts operational RunEvent data; EmitStream accepts normalized
// token, tool, reasoning, and HITL payloads when provider streaming is enabled.
// Both methods feed the same public typed Event stream; they are not separate
// consumer channels. Drivers should not retain the sink after Run returns.
// Every RunEventItem emitted through Emit MUST appear in Response.Transcript
// in the same order, with no hidden or recomputed entries.
type EventSink interface {
	// Emit publishes a RunEvent to the run-scoped Event sink.
	Emit(event RunEvent) error
	// EmitStream publishes a structured StreamPayload on the run-scoped
	// Event sink. When the resolved provider transport is non-streaming the
	// sink may discard the payload; this is independent of the public Run versus
	// Stream method. Drivers MUST leave Sequence, Seq, and Timestamp zero; core
	// assigns all three in receiver order.
	EmitStream(payload StreamPayload) error
}

// StreamSupport is the optional fidelity contract implemented by drivers that
// can produce normalized StreamPayload events. Core and bridges use it to
// understand provider-transport detail such as token-level deltas; it does
// not change the Runner execution contract.
//
// In particular, StreamSupport MUST NOT be interpreted as an A2A transport
// capability. Every Runner has Stream; remote A2A streaming availability is
// negotiated from the remote AgentCard, not by querying a local Driver.
type StreamSupport interface {
	StreamCapability() StreamCapability
}

// StreamCapability describes what kinds of streaming fidelity a driver can
// deliver. Every field is additive: bridges should degrade gracefully when a
// capability is false (e.g. synthesize a single TOOL_CALL_START when
// ToolCallArgs is false).
type StreamCapability struct {
	// Native reports whether the driver speaks a natively event-based
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
	// are exposed as typed events. The response path, when supported, still
	// goes through DecisionCapableSink and the ApprovalRequest carried by the
	// public Event.
	HITL bool
}

// Descriptor is the driver's static capability declaration. The SDK uses it
// to validate host requests before launching a driver; hosts can use it to
// disable unsupported UI controls instead of discovering failures late.
type Descriptor struct {
	// Type is the stable provider/driver identifier.
	Type string
	// DisplayName is the human-readable driver name.
	DisplayName string
	// Models lists statically known models; ModelLister may provide a live list.
	Models []ModelInfo
	// ConfigSchema is the static schema used when ConfigSchemaProvider is absent.
	ConfigSchema *ConfigSchema
	// Sessions declares resume support and its SessionCodec requirement.
	Sessions SessionCapability
	// Skills declares whether and how the driver consumes resolved skills.
	Skills SkillCapability
	// MCP declares the supported MCP transports.
	MCP MCPCapability
	// Instructions declares explicit instruction-bundle support.
	Instructions InstructionsCapability
	// Workspace declares support for SDK-resolved workspaces.
	Workspace WorkspaceCapability
	// Process declares the provider process lifecycle supported by this driver.
	Process ProcessCapability
	// RunPolicyCaps declares the policy dimensions the driver can enforce.
	RunPolicyCaps RunPolicyCapabilities
	// Runtime declares runtime-service reporting support.
	Runtime RuntimeCapability
	// StructuredOutput declares supported structured-output mechanisms.
	StructuredOutput StructuredOutputCapability
}

// SessionCapability declares whether a driver can resume provider sessions.
// SupportsResume MUST be true if and only if the Driver implements
// SessionCodecProvider and returns a non-nil stable codec. When true, the
// Driver MUST additionally implement SessionConfigFingerprinter with a stable,
// non-empty construction-config identity.
type SessionCapability struct {
	SupportsResume bool
}

// SkillCapability declares whether a driver consumes resolved skills and
// whether its skill state is ephemeral per run or persistent in the profile.
type SkillCapability struct {
	Supported bool
	Mode      SkillSyncMode
}

// InstructionsCapability declares whether the driver accepts explicit
// instruction bundles in addition to the prompt.
type InstructionsCapability struct {
	Supported bool
}

// WorkspaceCapability declares whether a driver can honor SDK-resolved
// workspace leases.
type WorkspaceCapability struct {
	Supported bool
}

// ProcessCapability declares provider-process lifecycle support. Persistent
// means a driver can reuse one provider process across turns of a stateful
// Thread and therefore MUST implement ProcessLifecycleDriver. Core still
// requests a one-shot process when Request.Spawn is true.
type ProcessCapability struct {
	Persistent bool
}

// RuntimeCapability declares whether a driver reports runtime-service state
// back in the run result's RuntimeServices.
type RuntimeCapability struct {
	ReportsServices bool
}

// StructuredOutputCapability is a truthful matrix for structured-output
// resolution. JSONSchemaNative means the driver can pass a schema through an
// official provider/CLI surface and return the provider-produced value.
// JSONSchemaPromptValidate means the driver can accept core's explicit
// exact-JSON prompt while core performs local validation. At least one of
// those mechanisms MUST be true before any WorksWith* field is true, and a
// declared mechanism MUST set WorksWithRun because the SDK has one execution
// pipeline shared by consumer Run and Stream.
//
// WorksWithStreaming applies only when Request.Streaming selects the
// provider-native streaming transport; it does not describe the consumer
// Stream method. WorksWithHITL applies when the effective policy contains an
// Ask decision. Core always selects native enforcement when that mechanism is
// eligible, otherwise prompt-validation when it is eligible, and rejects the
// invocation before launch when neither works. Drivers consume the resolved
// Request.StructuredOutputSource and must not renegotiate it. Every mechanism
// additionally requires WorksWithStreaming when Request.Streaming is true and
// WorksWithHITL when any effective decision mode is Ask.
type StructuredOutputCapability struct {
	JSONSchemaNative         bool
	JSONSchemaPromptValidate bool

	WorksWithRun       bool
	WorksWithStreaming bool
	WorksWithHITL      bool

	Notes string
}

// ConfigSchema describes the host-facing configuration contract for one bound
// driver. Hosts can render these fields directly into settings UIs, CLIs, or
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

// ConfigField describes one configurable driver property.
//
// Built-in drivers currently use these conventions:
//   - Type: "text", "textarea", "number", "toggle", or "select"
//   - Default: host-facing default value when the driver exposes one
//   - Options: selectable values for "select" fields
//   - Group: stable buckets such as "command", "model", "permissions", or "execution"
//   - Meta: driver-specific UI hints that do not change runtime semantics
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
