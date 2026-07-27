package driver

import "context"

// Driver is the adapter SPI implemented by built-in and third-party agent
// integrations. The SDK owns default merging, session coordination,
// workspace/runtime/skill resolution, and result archiving; drivers own
// provider-specific validation, process/protocol execution, transcript
// parsing, and checkpoint extraction.
//
// The root package exposes this interface as agentadaptor.DriverAdapter.
type Driver interface {
	Descriptor() Descriptor
	ValidateConfig(cfg any) error
	Run(ctx context.Context, req Request, sink EventSink) (Response, error)
}

// EnvironmentProbe is implemented by drivers that can perform preflight
// checks against local CLIs, auth files, profile directories, or other
// dependencies. Admin.CheckEnvironment uses it when present.
//
// The root package exposes this interface as agentadaptor.EnvironmentAwareDriver.
type EnvironmentProbe interface {
	CheckEnvironment(ctx context.Context, cfg any) (EnvironmentReport, error)
}

// ModelLister is implemented by drivers that can list visible model choices.
// Drivers may return static descriptor models or inspect local provider
// state when a live list is available.
//
// The root package exposes this interface as agentadaptor.ModelAwareDriver.
type ModelLister interface {
	ListModels(ctx context.Context, cfg any) ([]ModelInfo, error)
}

// ModelDetector is implemented by drivers that can infer the effective model
// from config files, CLI defaults, profile state, or the supplied config.
//
// The root package exposes this interface as agentadaptor.ModelDetectorDriver.
type ModelDetector interface {
	DetectModel(ctx context.Context, cfg any, profile *ProfileSelection) (*DetectedModel, error)
}

// ProfileReporter lets drivers expose the effective local profile directory
// used for auth/config/skill semantics through the control plane.
//
// Built-in drivers use this to report effective CODEX_HOME,
// CLAUDE_CONFIG_DIR, or CURSOR_HOME resolution, including managed homes when
// the SDK synthesizes one.
//
// The root package exposes this interface as agentadaptor.ProfileAwareDriver.
type ProfileReporter interface {
	GetProfile(ctx context.Context, cfg any, agent AgentIdentity, profile *ProfileSelection) (AgentProfile, error)
}

// SessionCodecProvider exposes the stable, deterministic session mapping used
// for resume compatibility. A Driver MUST implement this interface with a
// non-nil codec if and only if Descriptor.Sessions.SupportsResume is true.
//
// The root package exposes this interface as agentadaptor.SessionCodecAwareDriver.
type SessionCodecProvider interface {
	SessionCodec() SessionCodec
}

// ConfigSchemaProvider lets drivers expose a runtime-hydrated config schema
// through the control plane without changing the execution contract.
//
// The root package exposes this interface as agentadaptor.ConfigSchemaAwareDriver.
type ConfigSchemaProvider interface {
	ConfigSchema(ctx context.Context, cfg any) (*ConfigSchema, error)
}

// QuotaProbe lets drivers expose provider quota or credit windows when the
// underlying CLI or local auth files support that probe.
//
// The root package exposes this interface as agentadaptor.QuotaAwareDriver.
type QuotaProbe interface {
	GetQuota(ctx context.Context, cfg any, profile *ProfileSelection) (QuotaReport, error)
}

// SkillSupport is the optional driver contract for skill-capable drivers.
// Drivers that do not implement it simply ignore skills; the SDK still
// reports an unsupported snapshot through the Admin surface.
//
// The design splits concerns across three methods:
//   - ListSkills reports the Admin-layer snapshot. selected is the SDK's
//     final selection set (Required ∪ WithDefaultSkills ∪ WithSkills) and
//     matches payload.Keys(); resolved is the full merged catalogue
//     (provider + binding-only candidates + selected skills). Drivers
//     should pass resolved through to SkillSnapshot.Resolved so the
//     Admin API can render the "available but unselected" view without
//     re-enumerating the provider.
//   - InjectSkills is invoked exactly once per Run() invocation after skill
//     resolution and before the driver starts. It is a compatibility hook
//     for third-party drivers and should stay non-destructive unless the
//     driver can prove the run cannot later be rejected. Built-in drivers
//     treat it as a no-op and reconcile profile-local resources inside Run()
//     after resume guards pass, because the effective profile directory is
//     only known there.
//   - SyncSkills is invoked by AgentAdmin.SetSelectedSkills to reconcile
//     the persistent / ephemeral host-side layout with the newly-chosen
//     set. It receives both the selected keys and the full resolved
//     catalogue for the same reason as ListSkills.
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
//
// The root package exposes this interface as agentadaptor.SkillAwareDriver.
type SkillSupport interface {
	ListSkills(ctx context.Context, cfg any, payload ResolvedSkills, selected []string, resolved []Skill, profile *ProfileSelection) (SkillSnapshot, error)
	InjectSkills(ctx context.Context, cfg any, payload ResolvedSkills, profile *ProfileSelection) error
	SyncSkills(ctx context.Context, cfg any, payload ResolvedSkills, selected []string, resolved []Skill, profile *ProfileSelection) (SkillSnapshot, error)
}

// EventSink is the per-run event surface drivers write into while executing.
// Emit carries operational RunEvent data; EmitStream carries normalized
// token/tool/reasoning/HITL payloads when streaming is enabled. Drivers should
// not retain the sink after Run returns. Every RunEventItem emitted through
// Emit MUST appear in Response.Transcript in the same order, with no hidden or
// recomputed entries.
type EventSink interface {
	// Emit publishes a RunEvent on the run-scoped event channel.
	Emit(event RunEvent) error
	// EmitStream publishes a structured StreamPayload on the run-scoped
	// stream channel. When the resolved provider transport is non-streaming the
	// sink may discard the payload; this is independent of the public Run versus
	// Stream method. Drivers MUST leave Sequence, Seq, and Timestamp zero; core
	// assigns all three in receiver order.
	EmitStream(payload StreamPayload) error
}

// StreamSupport is the optional contract implemented by drivers that can
// produce normalized StreamPayload events. Hosts can advertise the driver's
// streaming capabilities (e.g. whether it supports token-level text deltas)
// through Admin introspection without changing the execution contract.
//
// The root package exposes this interface as agentadaptor.StreamAwareDriver.
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
	// are exposed on the stream channel. The stream event is the broadcast
	// channel; the decision path, when supported, still goes through
	// DecisionCapableSink and the host-facing decision surfaces.
	HITL bool
}

// Descriptor is the driver's static capability declaration. The SDK uses it
// to validate host requests before launching a driver; hosts can use it to
// disable unsupported UI controls instead of discovering failures late.
//
// The root package exposes this type as agentadaptor.DriverDescriptor.
type Descriptor struct {
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

// SessionCapability declares whether a driver can resume provider sessions.
// SupportsResume MUST be true if and only if the Driver implements
// SessionCodecProvider and returns a non-nil stable codec.
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

// RuntimeCapability declares whether a driver reports runtime-service state
// back in the run result's RuntimeServices.
type RuntimeCapability struct {
	ReportsServices bool
}

// StructuredOutputCapability declares which runtime structured-output modes
// the driver can honor. JSONSchemaNative means a provider/CLI-native schema
// surface exists; JSONSchemaPromptValidate means the driver can accept core's
// explicit prompt+local-validation fallback. WorksWithRun covers the single
// v1 execution pipeline used by both consumer Run and Stream. The historical
// WorksWithStreaming name refers only to compatibility with the provider-native
// streaming transport selected in Request.Streaming; it does not describe the
// consumer Stream method.
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
