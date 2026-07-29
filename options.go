package adaptor

import (
	"maps"
	"reflect"
	"strings"
	"time"

	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/engine"
	"github.com/agent-dance/agent-adaptor/mcp"
	"github.com/agent-dance/agent-adaptor/profile"
	"github.com/agent-dance/agent-adaptor/skill"
	"github.com/agent-dance/agent-adaptor/threadstore"
)

// One option vocabulary, two scopes: the same WithX used in New(...) is the
// agent-level default, used in Run/Stream(...) it is a per-call override.
// The merge rule is a single sentence: the nearer scope wins; skills append,
// everything else replaces. Scope-illegal combinations are compile errors in
// both directions.

// Option is the full set New accepts. It writes agent-level defaults.
//
// Passing an Option-only value (WithThreadStore, WithEventBuffer, ...) to
// Run/Stream does not compile: "adaptor.Option does not implement
// adaptor.CallOption (missing method ApplyRun)" means the option is
// construction-scope only.
type Option interface {
	// ApplyNew writes the option into the agent-level default settings.
	ApplyNew(*AgentSettings)
}

// CallOption is the set Run/Stream accept. It writes the effective settings
// of one invocation (a clone of the agent defaults).
//
// CallOption intentionally does NOT embed Option: call-scope-only options
// passed to New fail to compile too ("missing method ApplyNew"), keeping the
// misuse feedback symmetric in both directions.
type CallOption interface {
	// ApplyRun writes the option into this call's effective settings.
	ApplyRun(*RunSettings)
}

// SharedOption is the return type of dual-scope options: used in New it is
// the Agent's default, used in Run/Stream it overrides this call only. Most
// options that configure execution values return it.
type SharedOption interface {
	Option
	CallOption
}

// ============ Option write targets (the controlled ecosystem surface) ============

// RunSettings collects every setting that can be overridden at the call
// site. Fields are unexported; ecosystem packages write through the exported
// methods below, whose semantics encode the merge rule (Set* replaces,
// Add* appends). The root package's own options go through the same methods
// so the extension surface stays self-validating.
type RunSettings struct {
	model     string
	timeout   time.Duration
	workspace string
	metadata  map[string]string
	identity  *Identity
	policy    *Policy
	approval  ApprovalHandler
	spawn     bool

	// instructions is the extra instruction bundle handed to the driver.
	// instructionsSet records an explicit write (even a clearing one), which
	// is what marks the resource as host-declared in the profile payload.
	instructions    *driver.InstructionsBundleRef
	instructionsSet bool

	// skills is the single append-merged option family in the "nearer scope
	// wins; skills append, everything else replaces" rule. clone() deep-
	// copies the slice and records defaultSkillBoundary = len(skills), so
	// entries below the boundary are agent defaults and entries appended by
	// per-call options are invocation-specific refs.
	skills               []driver.SkillRef
	defaultSkillBoundary int

	// mcpServers is root-owned option state. Pointer-to-slice preserves an
	// unset declaration versus an explicit clear; conversion to the internal
	// envelope happens only at the engine boundary.
	mcpServers *[]mcp.Server

	// agents/hooks/configPatches use pointer-to-slice so "never set" (nil
	// pointer) is distinguishable from "explicitly declared empty" (non-nil
	// pointer, empty slice).
	agents        *[]driver.AgentSpec
	hooks         *[]driver.HookSpec
	configPatches *[]driver.ProfileConfigPatch

	// outputSchema is the structured output request for this run;
	// outputSchemaErr records a schema generation failure at option-build
	// time and is surfaced before the driver launches.
	outputSchema    *driver.OutputSchema
	outputSchemaErr error

	// workspaceSpec selects the workspace provisioning strategy. It
	// replaces as a whole value and, together with WithWorkspaceManager,
	// switches the run from direct WithWorkspace(dir) lease synthesis to
	// managed lease resolution.
	workspaceSpec WorkspaceSpec

	// services is the declared runtime-service set, replaced as a whole
	// value (an empty declaration clears the agent default). They are
	// ensured through the installed ServiceManager before the driver
	// launches.
	services []driver.RuntimeServiceSpec

	// runServices are the run-scoped service providers attached to every
	// invocation (delegation.Service.Option() and friends). This family
	// appends rather than replaces — an ecosystem option must compose with
	// the agent's other providers, not silently displace them — and
	// de-duplicates by provider identity so passing the same option in both
	// New and Run attaches it once.
	runServices []RunServiceProvider
}

// SetModel replaces the effective model for the target scope. Empty and
// whitespace-only values mean no override, matching the Driver Request
// contract and preventing an all-space model name from reaching providers.
func (s *RunSettings) SetModel(m string) { s.model = strings.TrimSpace(m) }

// SetTimeout replaces the wall-clock budget for one run. Zero means no
// SDK-imposed deadline.
func (s *RunSettings) SetTimeout(d time.Duration) { s.timeout = d }

// SetSpawn forces a fresh provider process for the target scope.
func (s *RunSettings) SetSpawn() { s.spawn = true }

// SetInstructions replaces the extra instruction text handed to the driver
// and declares the instructions resource as host-managed. Empty text clears
// the effective instructions (an explicit clear still declares).
func (s *RunSettings) SetInstructions(text string) {
	if text == "" {
		s.instructions = nil
	} else {
		s.instructions = &driver.InstructionsBundleRef{Content: text}
	}
	s.instructionsSet = true
}

// SetInstructionsBundle replaces the full instruction bundle (path- or
// content-based) and declares the instructions resource. A nil ref clears
// the effective bundle while still declaring the resource.
func (s *RunSettings) SetInstructionsBundle(ref *driver.InstructionsBundleRef) {
	s.instructions = engine.CloneInstructions(ref)
	s.instructionsSet = true
}

// AddSkills appends skill references for the target scope — the single
// append-merged option family: call-site refs never displace the agent
// defaults, they extend them.
func (s *RunSettings) AddSkills(refs ...skill.Ref) {
	s.skills = append(s.skills, engine.CloneSkillRefs(refs)...)
}

// SetMCPServers replaces the MCP server set as a whole value. An empty
// (or nil) slice is an explicit clear: it substitutes the agent default
// with "no servers" rather than inheriting it.
func (s *RunSettings) SetMCPServers(servers []mcp.Server) {
	cloned := engine.CloneMCPServerSpecs(servers)
	s.mcpServers = &cloned
}

func (s RunSettings) engineMCPConfig() *engine.MCPConfig {
	if s.mcpServers == nil {
		return nil
	}
	return &engine.MCPConfig{Servers: engine.CloneMCPServerSpecs(*s.mcpServers)}
}

// SetAgents replaces the sub-agent spec set and declares the resource.
// An empty slice declares "explicitly no sub-agents".
func (s *RunSettings) SetAgents(specs []driver.AgentSpec) {
	cp := engine.CloneAgentSpecs(specs)
	s.agents = &cp
}

// SetHooks replaces the hook spec set and declares the resource. An empty
// slice declares "explicitly no hooks".
func (s *RunSettings) SetHooks(specs []driver.HookSpec) {
	cp := engine.CloneHookSpecs(specs)
	s.hooks = &cp
}

// SetConfigPatches replaces the profile config patch set and declares the
// resource. An empty slice declares "explicitly no patches".
func (s *RunSettings) SetConfigPatches(patches []driver.ProfileConfigPatch) {
	cp := engine.CloneProfileConfigPatches(patches)
	s.configPatches = &cp
}

// SetOutputSchema replaces the structured output request for this run.
func (s *RunSettings) SetOutputSchema(schema driver.OutputSchema) {
	s.outputSchema = engine.CloneOutputSchema(&schema)
}

// SetOutputSchemaError records a schema construction failure. The run fails
// with this error before the driver launches — schema bugs are programmer
// errors that must not silently degrade into unvalidated output. The error
// is sticky: a valid schema set later in the option list does not clear it.
func (s *RunSettings) SetOutputSchemaError(err error) {
	s.outputSchemaErr = err
}

// SetWorkspace replaces the working directory for the target scope.
func (s *RunSettings) SetWorkspace(dir string) { s.workspace = dir }

// SetWorkspaceSpec replaces the workspace provisioning strategy. A non-nil
// spec routes the run through the WorkspaceManager (the passthrough manager
// when none is installed) instead of the direct lease synthesis.
func (s *RunSettings) SetWorkspaceSpec(spec WorkspaceSpec) { s.workspaceSpec = spec }

// SetServices replaces the declared runtime-service set as a whole value. An
// empty (or nil) slice is an explicit clear: it substitutes the agent default
// with "no services" rather than inheriting it.
func (s *RunSettings) SetServices(specs []ServiceSpec) {
	s.services = engine.CloneRuntimeServiceSpecs(specs)
}

// AddRunServiceProvider appends a run-scoped service provider — the controlled
// extension surface behind ecosystem options such as
// delegation.Service.Option(). Providers append rather than replace, and a
// provider already present is not added twice: the same option value used in
// both New and Run attaches exactly once, which is what keeps its MCP server
// key unique (a duplicate would fail the run before launch).
func (s *RunSettings) AddRunServiceProvider(p RunServiceProvider) {
	if p == nil {
		return
	}
	if t := reflect.TypeOf(p); t != nil && t.Comparable() {
		for _, existing := range s.runServices {
			// Interface comparison short-circuits on differing dynamic
			// types, so a non-comparable neighbour cannot panic here.
			if existing == p {
				return
			}
		}
	}
	s.runServices = append(s.runServices, p)
}

// SetMetadata sets one audit metadata key. Keys merge per key: a call-site
// value overrides the same key from the agent defaults and leaves the other
// default keys intact.
func (s *RunSettings) SetMetadata(k, v string) {
	if s.metadata == nil {
		s.metadata = make(map[string]string)
	}
	s.metadata[k] = v
}

// SetIdentity replaces the caller identity propagated to host hooks and the
// driver.
func (s *RunSettings) SetIdentity(id Identity) { s.identity = &id }

// SetPolicy replaces the whole execution policy ("everything else replaces":
// a call-site policy substitutes the agent-default policy as one value, it
// does not merge field-wise).
func (s *RunSettings) SetPolicy(p Policy) { s.policy = &p }

// SetApprovalHandler replaces the approval callback (form A of approval
// consumption). A nil handler restores event-form consumption.
func (s *RunSettings) SetApprovalHandler(h ApprovalHandler) { s.approval = h }

// clone returns a deep copy so per-call overrides never leak back into the
// agent defaults (and one run never pollutes the next). It also stamps the
// default/run skill boundary: everything present at clone time is an agent
// default; everything a CallOption appends afterwards is a run ref.
func (s RunSettings) clone() RunSettings {
	out := s
	out.metadata = maps.Clone(s.metadata)
	if s.identity != nil {
		id := *s.identity
		out.identity = &id
	}
	if s.policy != nil {
		p := *s.policy
		out.policy = &p
	}
	out.instructions = engine.CloneInstructions(s.instructions)
	out.skills = engine.CloneSkillRefs(s.skills)
	out.defaultSkillBoundary = len(out.skills)
	if s.mcpServers != nil {
		cloned := engine.CloneMCPServerSpecs(*s.mcpServers)
		out.mcpServers = &cloned
	}
	if s.agents != nil {
		cp := engine.CloneAgentSpecs(*s.agents)
		out.agents = &cp
	}
	if s.hooks != nil {
		cp := engine.CloneHookSpecs(*s.hooks)
		out.hooks = &cp
	}
	if s.configPatches != nil {
		cp := engine.CloneProfileConfigPatches(*s.configPatches)
		out.configPatches = &cp
	}
	out.outputSchema = engine.CloneOutputSchema(s.outputSchema)
	out.services = engine.CloneRuntimeServiceSpecs(s.services)
	out.runServices = append([]RunServiceProvider(nil), s.runServices...)
	return out
}

// AgentSettings = RunSettings (dual-scope fields) + construction-scope-only
// fields. The subset relation is expressed by struct embedding: a CallOption
// receives *RunSettings, on which the construction-only fields simply do not
// exist — the writable field set is the scope boundary.
type AgentSettings struct {
	// RunSettings contains the defaults inherited by each invocation.
	RunSettings

	// threadStore backs Agent.Thread (stateful conversations).
	// Nil is valid: Threads then fail their runs with
	// ErrThreadStoreRequired while the stateless Agent paths stay
	// unaffected.
	threadStore threadstore.Store

	// eventBuffer sizes the per-run event channel (0 = default 1024).
	eventBuffer int

	// blockingEvents switches the event pipeline from the default
	// drop-with-aggregated-marker strategy to blocking delivery.
	blockingEvents bool

	// profile selects the driver-native profile strategy (shared /
	// dedicated / clone). Construction scope only: the profile identity of
	// an Agent is part of what the Agent *is*, and it participates in
	// session fingerprints.
	profile *driver.ProfileSelection

	// skillProvider resolves bare skill keys to full Skill descriptions
	// and, when it implements skill.Catalog, enumerates the inspection
	// catalogue. Nil means inline Skill values are the only source.
	skillProvider SkillProvider

	// skillMaterializer overrides how non-path skill sources are
	// materialized to disk. Nil uses the process-default materializer.
	skillMaterializer SkillMaterializer

	// workspaceManager turns a WorkspaceSpec into a concrete lease. Nil
	// means the passthrough manager when a spec is set, and no managed
	// resolution at all when none is.
	workspaceManager WorkspaceManager

	// serviceManager starts/locates the services declared with
	// WithServices. Nil means declared services are not ensured and no
	// endpoints are invented.
	serviceManager ServiceManager
}

// SetThreadStore injects the thread storage backend (stateful conversations).
func (s *AgentSettings) SetThreadStore(store threadstore.Store) { s.threadStore = store }

// SetEventBuffer sets the per-run ordinary-event buffer size. Terminal
// delivery uses separate internal reserve capacity.
func (s *AgentSettings) SetEventBuffer(n int) { s.eventBuffer = n }

// SetBlockingEvents switches event delivery to blocking (no-drop) mode.
func (s *AgentSettings) SetBlockingEvents() { s.blockingEvents = true }

// SetProfile replaces the driver-native profile selection.
func (s *AgentSettings) SetProfile(sel profile.Selection) {
	s.profile = engine.CloneProfileSelection(&sel)
}

// SetSkillProvider injects the skill provider used to resolve bare keys.
func (s *AgentSettings) SetSkillProvider(p SkillProvider) { s.skillProvider = p }

// SetSkillMaterializer overrides the skill materialization strategy.
func (s *AgentSettings) SetSkillMaterializer(m SkillMaterializer) { s.skillMaterializer = m }

// SetWorkspaceManager injects the workspace provisioning backend.
func (s *AgentSettings) SetWorkspaceManager(m WorkspaceManager) { s.workspaceManager = m }

// SetServiceManager injects the runtime-service orchestration backend.
func (s *AgentSettings) SetServiceManager(m ServiceManager) { s.serviceManager = m }

// ============ In-package function adapters (one per scope) ============

// sharedOptionFunc backs dual-scope options: one func, two forwarding methods.
type sharedOptionFunc func(*RunSettings)

func (f sharedOptionFunc) ApplyNew(s *AgentSettings) { f(&s.RunSettings) }
func (f sharedOptionFunc) ApplyRun(s *RunSettings)   { f(s) }

// newOptionFunc backs construction-scope-only options.
type newOptionFunc func(*AgentSettings)

func (f newOptionFunc) ApplyNew(s *AgentSettings) { f(s) }

// callOptionFunc backs call-scope-only options such as WithSchema[T].
type callOptionFunc func(*RunSettings)

func (f callOptionFunc) ApplyRun(s *RunSettings) { f(s) }

// WithModel selects the model. In New it is the Agent's default model; in
// Run/Stream it overrides this invocation only (delivered to the driver as
// the per-run model override).
func WithModel(m string) SharedOption {
	return sharedOptionFunc(func(s *RunSettings) { s.SetModel(m) })
}

// WithTimeout bounds one run's wall-clock time. In New it is the default
// budget for every run; in Run/Stream it overrides this invocation only.
// The SDK enforces it via context deadline; a run that exceeds it fails with
// context.DeadlineExceeded.
func WithTimeout(d time.Duration) SharedOption {
	return sharedOptionFunc(func(s *RunSettings) { s.SetTimeout(d) })
}

// WithSpawn forces a fresh provider process instead of reusing the driver's
// default persistent process. In New it applies to every invocation; in
// Run/Stream it overrides this invocation only. Stateless Agent runs and
// drivers without persistent-process support already spawn regardless.
func WithSpawn() SharedOption {
	return sharedOptionFunc(func(s *RunSettings) { s.SetSpawn() })
}

// WithInstructions supplies extra instruction text alongside the prompt.
// Nearer scope replaces: a call-site value substitutes the agent default.
func WithInstructions(text string) SharedOption {
	return sharedOptionFunc(func(s *RunSettings) { s.SetInstructions(text) })
}

// WithWorkspace sets the working directory the agent operates in.
func WithWorkspace(dir string) SharedOption {
	return sharedOptionFunc(func(s *RunSettings) { s.SetWorkspace(dir) })
}

// WithMetadata attaches one audit metadata key/value to runs. Metadata
// merges per key: call-site keys override same-named default keys and leave
// the rest of the defaults intact.
func WithMetadata(k, v string) SharedOption {
	return sharedOptionFunc(func(s *RunSettings) { s.SetMetadata(k, v) })
}

// WithIdentity sets the caller identity (tenant / user / profile / agent
// scoping) propagated to host hooks and the driver. See Identity.
func WithIdentity(id Identity) SharedOption {
	return sharedOptionFunc(func(s *RunSettings) { s.SetIdentity(id) })
}

// WithPolicy sets the execution policy: sandbox, optional feature levels,
// and approvals. The policy replaces as a whole value; it does
// not merge field-wise with the agent default.
func WithPolicy(p Policy) SharedOption {
	return sharedOptionFunc(func(s *RunSettings) { s.SetPolicy(p) })
}

// OnApproval installs the approval callback — form A of approval
// consumption. Every human-in-the-loop request whose policy mode is "ask"
// invokes the handler with a live *ApprovalRequest; the handler resolves it
// (Approve / Deny / Answer) and returns nil, or returns an error to abort
// the run. When no handler is installed the request arrives as a
// *ApprovalRequest event on the Stream instead (form B); either way an
// unconsumed request times out into the Policy.Approvals fallback.
//
// In New the handler is the agent default; in Run/Stream it overrides this
// invocation only ("nearer scope wins").
func OnApproval(h ApprovalHandler) SharedOption {
	return sharedOptionFunc(func(s *RunSettings) { s.SetApprovalHandler(h) })
}

// WithThreadStore injects the thread storage backend that enables stateful
// conversations: with it, Agent.Thread persists and resumes
// driver checkpoints across runs and processes (memory.NewStore() for
// single-process hosts, a durable implementation for services). Without it
// Threads fail their runs with ErrThreadStoreRequired. Construction scope
// only; passing it to Run/Stream is a compile error (missing method
// ApplyRun).
func WithThreadStore(store threadstore.Store) Option {
	return newOptionFunc(func(s *AgentSettings) { s.SetThreadStore(store) })
}

// WithEventBuffer sets the per-run ordinary-event buffer size used by the
// streaming pipeline (default 1024). The SDK keeps separate internal capacity
// for the terminal event, so RunFinished remains deliverable when cancellation
// occurs while the ordinary buffer is full. When the consumer falls behind and
// the ordinary buffer fills, droppable events are surfaced as one aggregated
// Dropped{Count} marker. Construction scope only.
func WithEventBuffer(n int) Option {
	return newOptionFunc(func(s *AgentSettings) { s.SetEventBuffer(n) })
}

// WithBlockingEvents switches ordinary event delivery from the default
// drop-with-marker strategy to blocking: during normal execution EmitStream
// and Emit wait for the consumer and do not drop events. Cancel still releases
// blocked producers and may abandon pending ordinary events; the terminal
// event remains reserved. Construction scope only.
func WithBlockingEvents() Option {
	return newOptionFunc(func(s *AgentSettings) { s.SetBlockingEvents() })
}

// SkillRef references a skill for WithSkills: either a bare key resolved
// through the SkillProvider (skill.Key) or a fully described inline skill
// (skill.Dir / skill.FS / skill.Inline / skill.Require). Alias of the
// driver SPI type — skill package constructors produce values of exactly
// this type.
type SkillRef = skill.Ref

// SkillProvider resolves bare skill keys to full skill descriptions.
// Implementations that also implement skill.Catalog (a Catalogue
// method) additionally power Inspect().Skills enumeration.
type SkillProvider = skill.Provider

// SkillMaterializer converts non-path skill sources into on-disk skill
// directories before the driver launches.
type SkillMaterializer = skill.Materializer

// WithSkills appends skill references. This is the single append-merged
// option family: in New the refs are the agent's default skills, in
// Run/Stream they extend (never displace) the defaults for this invocation
// only. Bare keys (skill.Key) are resolved through the SkillProvider;
// inline values (skill.Dir / skill.FS / skill.Inline) are taken at face
// value. Duplicate keys must be structurally equal — conflicting
// duplicates fail the run with ErrSkillKeyConflict.
func WithSkills(refs ...SkillRef) SharedOption {
	return sharedOptionFunc(func(s *RunSettings) { s.AddSkills(refs...) })
}

// WithSkillProvider installs the skill provider that resolves bare keys
// (and, when it implements a Catalogue method, feeds Inspect().Skills).
// Construction scope only: the provider is part of the Agent's identity,
// not a per-call knob.
func WithSkillProvider(p SkillProvider) Option {
	return newOptionFunc(func(s *AgentSettings) { s.SetSkillProvider(p) })
}

// WithSkillMaterializer overrides how non-path skill sources are staged to
// disk. Construction scope only.
func WithSkillMaterializer(m SkillMaterializer) Option {
	return newOptionFunc(func(s *AgentSettings) { s.SetSkillMaterializer(m) })
}

// WithMCP replaces the MCP server set as a whole value ("everything else
// replaces"): in New it is the agent default, in Run/Stream it substitutes
// the default for this invocation only. Calling WithMCP() with no servers
// is an explicit clear — the run sees no MCP servers even when the agent
// default has some. Server specs are validated against the driver's
// declared MCP capability before the driver launches; unsupported
// transports fail the run with ErrMCPTransportUnsupported and the driver
// is never started.
func WithMCP(servers ...mcp.Server) SharedOption {
	return sharedOptionFunc(func(s *RunSettings) { s.SetMCPServers(servers) })
}

// WithProfile selects the driver-native profile strategy (profile.Native /
// profile.Dedicated / profile.CloneNative / profile.Default). Construction
// scope only: the profile is part of what the Agent is, participates in
// session fingerprints, and cannot be swapped per call.
func WithProfile(sel profile.Selection) Option {
	return newOptionFunc(func(s *AgentSettings) { s.SetProfile(sel) })
}

// WithProfileResources declares the desired profile-shaped resource set in
// one value. Each resource keeps its own merge rule (the same rules as the
// dedicated options):
//
//   - Skills append (like WithSkills);
//   - MCP replaces when non-nil (like WithMCP);
//   - Agents / Hooks / Config replace and declare when the field is
//     non-nil — an explicitly empty slice declares "none";
//   - Instructions replace and declare when non-nil.
//
// In New the resources are agent defaults; in Run/Stream they override
// this invocation only. Every declared resource lands in the run's
// ProfilePayload, and ProfileState reports truthfully whether the Driver
// actually materialized it.
func WithProfileResources(res profile.Resources) SharedOption {
	return sharedOptionFunc(func(s *RunSettings) {
		cp := profileResourcesToEngine(res)
		if len(cp.Skills) > 0 {
			s.AddSkills(cp.Skills...)
		}
		if cp.MCP != nil {
			s.SetMCPServers(cp.MCP.Servers)
		}
		if res.Agents != nil {
			s.SetAgents(cp.Agents)
		}
		if res.Hooks != nil {
			s.SetHooks(cp.Hooks)
		}
		if res.Config != nil {
			s.SetConfigPatches(cp.Config)
		}
		if cp.Instructions != nil {
			s.SetInstructionsBundle(cp.Instructions)
		}
	})
}

// WithWorkspaceSpec selects how the run's workspace is provisioned —
// adaptor.SharedWorkspace{} to reuse the project directory,
// adaptor.GitWorktreeWorkspace{...} for an isolated worktree,
// adaptor.DriverManagedWorkspace{} to let the Driver choose. It replaces as a
// whole value: in New it is the agent default, in Run/Stream it overrides this
// invocation only.
//
// WithWorkspace(dir) and WithWorkspaceSpec compose: the directory is the base
// CWD handed to the WorkspaceManager, the spec is the strategy. Setting either
// a spec or a manager routes the run through managed lease resolution; setting
// neither keeps the plain "run here" behavior.
func WithWorkspaceSpec(spec WorkspaceSpec) SharedOption {
	return sharedOptionFunc(func(s *RunSettings) { s.SetWorkspaceSpec(spec) })
}

// WithWorkspaceManager installs the backend that turns a WorkspaceSpec into a
// concrete working-directory lease (git worktrees, sandboxes, an external
// workspace service). Without one, specs resolve through the SDK's passthrough
// manager, which leases the base directory unchanged. Construction scope only:
// the manager is infrastructure the Agent is built on, not a per-call knob.
func WithWorkspaceManager(m WorkspaceManager) Option {
	return newOptionFunc(func(s *AgentSettings) { s.SetWorkspaceManager(m) })
}

// WithServices declares the runtime services a run needs — dev servers,
// databases, tool sidecars. They are ensured through the installed
// ServiceManager before the driver launches, and the resulting endpoints reach
// the driver in the run's runtime payload; a service that publishes a typed
// ServiceRef.MCP additionally joins the run's MCP server set alongside (never
// in place of) WithMCP.
//
// The declaration replaces as a whole value: calling WithServices() with no
// specs is an explicit clear. Without a ServiceManager the declaration is
// inert — the SDK never invents endpoints for services nobody manages.
func WithServices(specs ...ServiceSpec) SharedOption {
	return sharedOptionFunc(func(s *RunSettings) { s.SetServices(specs) })
}

// WithServiceManager installs the backend that starts or locates the services
// declared with WithServices, and releases the run-scoped ones afterwards.
// Construction scope only.
func WithServiceManager(m ServiceManager) Option {
	return newOptionFunc(func(s *AgentSettings) { s.SetServiceManager(m) })
}

// WithRunServices attaches run-scoped service providers to every invocation:
// the generic form of what ecosystem packages ship as their own one-liner
// option (delegation.Service.Option()). Each provider is attached after the run
// ID is minted and before the driver is dispatched, contributes its endpoints
// to the run's runtime/MCP payload, may stream its own events into the run's
// event channel, and is detached once the run's events are done.
//
// Providers append rather than replace, and the same provider is never attached
// twice — passing one option value in both New and Run is safe.
func WithRunServices(providers ...RunServiceProvider) SharedOption {
	return sharedOptionFunc(func(s *RunSettings) {
		for _, p := range providers {
			s.AddRunServiceProvider(p)
		}
	})
}
