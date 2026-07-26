package agentadaptor

import "github.com/agent-dance/agent-adaptor/internal/engine"

// Option configures SDK construction. Options install the default agent,
// named agents, and host-provided managers used by every run. Use Build when
// you want option errors returned; New panics on invalid construction.
// It is an alias for the engine declaration (the pipeline core moved to
// internal/engine in P0.2).
type Option = engine.Option

// AgentOption configures a single AgentBinding. These defaults are merged into
// every run on that binding before per-call RunOptions are applied. It is an
// alias for the engine declaration.
type AgentOption = engine.AgentOption

// WithDefaultAgent binds the agent used by SDK.Run, SDK.Start, and
// SDK.Default. It is required exactly once; hosts that want to choose between
// agents dynamically should do that before calling the SDK, then use
// WithAgent(name, binding) for explicit named alternatives.
func WithDefaultAgent(binding AgentBinding) Option {
	return engine.WithDefaultAgent(binding)
}

// WithAgent registers an additional named agent binding. The name "default"
// is reserved for WithDefaultAgent. Named agents are reached with SDK.Agent or
// Admin().Agent and follow the same execution semantics as the default agent.
func WithAgent(name string, binding AgentBinding) Option {
	return engine.WithAgent(name, binding)
}

// WithSessionStore enables stateful session modes. Without it the SDK is
// stateless by default; session-aware options such as WithSessionKey,
// WithContinueSession, and WithForkSession require a store.
func WithSessionStore(store SessionStore) Option {
	return engine.WithSessionStore(store)
}

// WithWorkspaceManager installs the host component that resolves workspace
// specs into concrete working directories. If unset, the SDK uses a
// passthrough shared-workspace manager.
func WithWorkspaceManager(manager WorkspaceManager) Option {
	return engine.WithWorkspaceManager(manager)
}

// WithSkillProvider installs the host-side SkillProvider that backs
// WithSkills / WithDefaultSkills. The provider's GetSkills is invoked
// on every Run to translate the user-referenced SkillKey set into
// concrete Skill descriptions (and to inject any tenant-mandatory
// "required" skills the provider chooses to include).
//
// Providers that can also enumerate their full visible catalogue
// (small, in-memory or cached) should additionally implement
// [SkillCatalog]. SDK detects the extension via type assertion and
// uses Catalogue() exclusively for Admin.ListSkills; Run-time
// resolution always goes through GetSkills regardless. Providers
// that cannot enumerate (remote stores, etc.) implement only
// SkillProvider — Admin.ListSkills then reports
// SkillSyncMode = SkillSyncUnsupported.
//
// AgentIdentity (tenant / profile / name) is propagated to the
// provider through ctx; providers that need scoping read it via
// [CallerIdentityFromContext].
func WithSkillProvider(provider SkillProvider) Option {
	return engine.WithSkillProvider(provider)
}

// WithSkillSet is sugar for WithSkillProvider when the catalogue is
// fully known at SDK construction time. The supplied SkillSet
// implements both SkillProvider (per-key fetch) and SkillCatalog
// (full enumeration), so Admin.ListSkills works automatically.
//
// Hosts with a remote skill store should implement SkillProvider
// directly instead of constructing a SkillSet.
func WithSkillSet(set SkillSet) Option {
	return engine.WithSkillSet(set)
}

// WithSkillMaterializer overrides how SkillFromFS / SkillFromInline sources
// are written to disk before an adapter consumes them. When unset, the SDK
// uses the built-in cache-root materializer documented in
// docs/skill-api-design.md §3.
func WithSkillMaterializer(materializer SkillMaterializer) Option {
	return engine.WithSkillMaterializer(materializer)
}

// WithRuntimeServiceManager installs the host component that starts, finds,
// and releases runtime services requested by bindings or individual runs. If
// unset, runtime service requests resolve to an empty set.
func WithRuntimeServiceManager(manager RuntimeServiceManager) Option {
	return engine.WithRuntimeServiceManager(manager)
}

// WithEventBuffer configures the per-run event-sink capacity and backpressure
// policy. runBuf sizes the RunEvent channel (default 64); streamBuf sizes the
// StreamPayload channel (default 1024). Values <= 0 fall back to the defaults.
//
// Policy controls how the SDK reacts when the StreamPayload channel is full:
//   - BackpressureDropStream (default): drop payloads and emit a single
//     StreamDropped marker carrying the lost count as soon as capacity
//     returns; the adapter sub-process never blocks.
//   - BackpressureBlock: block the adapter goroutine until the host consumes
//     a payload; use this when strict AG-UI ordering without gaps is
//     required.
//
// RunEvent backpressure is always drop-with-marker and is unaffected by this
// option.
func WithEventBuffer(runBuf, streamBuf int, policy EventBackpressure) Option {
	return engine.WithEventBuffer(runBuf, streamBuf, policy)
}

// WithDefaultPermissionHandler binds a PermissionHandler at the agent level.
// Per-call WithPermissionHandler still overrides this default.
func WithDefaultPermissionHandler(h PermissionHandler) AgentOption {
	return engine.WithDefaultPermissionHandler(h)
}

// WithDefaultPlanReviewHandler binds a PlanReviewHandler at the agent level.
func WithDefaultPlanReviewHandler(h PlanReviewHandler) AgentOption {
	return engine.WithDefaultPlanReviewHandler(h)
}

// WithDefaultQuestionHandler binds a QuestionHandler at the agent level.
func WithDefaultQuestionHandler(h QuestionHandler) AgentOption {
	return engine.WithDefaultQuestionHandler(h)
}

// WithDefaultIdentity sets the binding-level identity visible to host hooks
// such as SkillProvider and RuntimeServiceManager. Per-call WithAgentIdentity
// overrides it for one run.
func WithDefaultIdentity(identity AgentIdentity) AgentOption {
	return engine.WithDefaultIdentity(identity)
}

// WithDefaultWorkspace sets the binding-level workspace request. Per-call
// WithWorkspace overrides it for one run.
func WithDefaultWorkspace(spec WorkspaceSpec) AgentOption {
	return engine.WithDefaultWorkspace(spec)
}

// WithDefaultSkills installs per-agent default skill references. Each SkillRef
// is either a SkillKey (resolved against the SkillProvider) or an inline
// Skill value. Multiple calls are additive: later SkillRef values are appended
// to the previously-declared set. Duplicate keys must be structurally equal
// (see ErrSkillKeyConflict).
func WithDefaultSkills(refs ...SkillRef) AgentOption {
	return engine.WithDefaultSkills(refs...)
}

// WithDefaultMCP sets the binding-level MCP server configuration. Per-call
// WithMCP replaces the full MCP config for one run; it is not additive.
func WithDefaultMCP(cfg MCPConfig) AgentOption {
	return engine.WithDefaultMCP(cfg)
}

// WithDefaultProfileResources installs a binding-level profile desired state.
// Skills are additive with WithDefaultSkills; MCP, instructions, agents, hooks,
// and config replace the corresponding binding defaults for their resource
// kind.
func WithDefaultProfileResources(resources ProfileResources) AgentOption {
	return engine.WithDefaultProfileResources(resources)
}

// WithDefaultAgents sets the binding-level desired sub-agent resources.
func WithDefaultAgents(specs ...AgentSpec) AgentOption {
	return engine.WithDefaultAgents(specs...)
}

// WithDefaultHooks sets the binding-level desired hook resources.
func WithDefaultHooks(specs ...HookSpec) AgentOption {
	return engine.WithDefaultHooks(specs...)
}

// WithDefaultProfileConfig sets the binding-level structured profile config
// patches.
func WithDefaultProfileConfig(patches ...ProfileConfigPatch) AgentOption {
	return engine.WithDefaultProfileConfig(patches...)
}

// WithDefaultRuntimeServices attaches default runtime-service requirements to
// an agent binding. Per-run WithRuntimeServices(...) overrides these defaults.
func WithDefaultRuntimeServices(services ...RuntimeServiceSpec) AgentOption {
	return engine.WithDefaultRuntimeServices(services...)
}

// WithDefaultRunPolicy sets binding-level defaults. Per-field empty values
// (…Inherit) mean "no default for that field" until a per-call WithRunPolicy
// sets it.
func WithDefaultRunPolicy(p RunPolicy) AgentOption {
	return engine.WithDefaultRunPolicy(p)
}

// WithDefaultInstructions sets the binding-level instruction bundle. Passing
// nil clears the default. Per-call WithInstructions overrides it for one run.
func WithDefaultInstructions(ref *InstructionsBundleRef) AgentOption {
	return engine.WithDefaultInstructions(ref)
}

// WithDefaultMetadata attaches binding-level metadata copied into every
// DriverRunRequest. Hosts commonly use it for audit tags or workflow labels;
// adapters should treat it as opaque.
func WithDefaultMetadata(key, value string) AgentOption {
	return engine.WithDefaultMetadata(key, value)
}

// WithDefaultStreaming marks the bound agent as streaming-by-default. Per-call
// WithStreaming / WithoutStreaming still override this default.
func WithDefaultStreaming() AgentOption {
	return engine.WithDefaultStreaming()
}

// RunOption configures one Run or Start invocation. RunOptions are resolved
// after binding defaults and affect only the current call.
type RunOption func(*runOptions)

type runOptions struct {
	session         *SessionRequest
	workspace       WorkspaceSpec
	runtime         *WorkspaceRuntimeConfig
	skills          []SkillRef
	mcp             *MCPConfig
	agents          *AgentPayload
	hooks           *HookPayload
	profileConfig   *ProfileConfigPayload
	outputSchema    *OutputSchema
	outputSchemaErr error
	model           string
	runPolicy       *RunPolicy
	instructions    *InstructionsBundleRef
	instructionsSet bool
	metadata        map[string]string
	agent           *AgentIdentity
	// streaming is tri-state: nil means "inherit from binding defaults",
	// non-nil wins over the binding default.
	streaming *bool
	// runIDPreset is an internal option set by Start() so the resolved run
	// shares the same ID that the RunHandle exposes before Wait() returns.
	runIDPreset string

	// per-Kind typed HITL handlers (RunOption-level beat AgentOption-level).
	permissionHandler PermissionHandler
	planReviewHandler PlanReviewHandler
	questionHandler   QuestionHandler
}

// applyRunOptions folds opts into a fresh runOptions value, skipping nil
// entries (same semantics the resolver used before the pipeline moved to
// internal/engine).
func applyRunOptions(opts []RunOption) runOptions {
	ro := runOptions{}
	for _, opt := range opts {
		if opt != nil {
			opt(&ro)
		}
	}
	return ro
}

// params converts the applied run options into the engine's exported
// RunParams mirror. It is a field-by-field copy: pointer/nil semantics are
// preserved exactly, so engine-side resolution behaves as before.
func (ro *runOptions) params() engine.RunParams {
	return engine.RunParams{
		Session:           ro.session,
		Workspace:         ro.workspace,
		Runtime:           ro.runtime,
		Skills:            ro.skills,
		MCP:               ro.mcp,
		Agents:            ro.agents,
		Hooks:             ro.hooks,
		ProfileConfig:     ro.profileConfig,
		OutputSchema:      ro.outputSchema,
		OutputSchemaErr:   ro.outputSchemaErr,
		Model:             ro.model,
		RunPolicy:         ro.runPolicy,
		Instructions:      ro.instructions,
		InstructionsSet:   ro.instructionsSet,
		Metadata:          ro.metadata,
		Agent:             ro.agent,
		Streaming:         ro.streaming,
		RunIDPreset:       ro.runIDPreset,
		PermissionHandler: ro.permissionHandler,
		PlanReviewHandler: ro.planReviewHandler,
		QuestionHandler:   ro.questionHandler,
	}
}

// WithPermissionHandler installs a PermissionHandler for this run. Overrides
// any WithDefaultPermissionHandler set on the binding.
func WithPermissionHandler(h PermissionHandler) RunOption {
	return func(ro *runOptions) {
		ro.permissionHandler = h
	}
}

// WithPlanReviewHandler installs a PlanReviewHandler for this run. Overrides
// any WithDefaultPlanReviewHandler set on the binding.
func WithPlanReviewHandler(h PlanReviewHandler) RunOption {
	return func(ro *runOptions) {
		ro.planReviewHandler = h
	}
}

// WithQuestionHandler installs a QuestionHandler for this run. Overrides
// any WithDefaultQuestionHandler set on the binding.
func WithQuestionHandler(h QuestionHandler) RunOption {
	return func(ro *runOptions) {
		ro.questionHandler = h
	}
}

// withPresetRunID is an internal RunOption used by Start() to propagate the
// pre-allocated run identifier into resolveInvocation. It is not part of the
// public API.
func withPresetRunID(runID string) RunOption {
	return func(ro *runOptions) {
		ro.runIDPreset = runID
	}
}

// WithSession supplies the complete session request for one run. Most hosts
// should prefer the helper options WithSessionKey, WithContinueSession,
// WithNewSession, or WithForkSession unless they need exact mode control.
func WithSession(req SessionRequest) RunOption {
	return func(ro *runOptions) {
		copyReq := req
		ro.session = &copyReq
	}
}

// WithSessionKey requests continue_or_start semantics for the stable
// host-facing (namespace, key) pair. The store resolves that pair to the
// current concrete SessionID, creating a new session when none exists.
func WithSessionKey(namespace, key string) RunOption {
	return WithSession(SessionRequest{
		Namespace: namespace,
		Key:       key,
		Mode:      SessionContinueOrStart,
	})
}

// WithContinueSession resumes one exact SessionID and fails if it cannot be
// found or is incompatible. Use this when the host is holding a concrete
// session handle rather than a stable business key.
func WithContinueSession(id string) RunOption {
	return WithSession(SessionRequest{
		ID:   id,
		Mode: SessionContinueOnly,
	})
}

// WithNewSession starts a fresh session and binds it to the supplied
// (namespace, key), replacing the active mapping only after a valid checkpoint
// is produced.
func WithNewSession(namespace, key string) RunOption {
	return WithSession(SessionRequest{
		Namespace: namespace,
		Key:       key,
		Mode:      SessionStartNew,
	})
}

// WithForkSession starts a new session from an existing concrete SessionID and
// binds the fork to the supplied (namespace, key).
func WithForkSession(fromID, namespace, key string) RunOption {
	return WithSession(SessionRequest{
		Namespace: namespace,
		Key:       key,
		Mode:      SessionFork,
		ForkFrom:  fromID,
	})
}

// WithWorkspace overrides the binding-level workspace request for one run.
func WithWorkspace(spec WorkspaceSpec) RunOption {
	return func(ro *runOptions) {
		ro.workspace = spec
	}
}

// WithRuntimeServices overrides the runtime services for a single run.
func WithRuntimeServices(services ...RuntimeServiceSpec) RunOption {
	return func(ro *runOptions) {
		if len(services) == 0 {
			ro.runtime = nil
			return
		}
		ro.runtime = &WorkspaceRuntimeConfig{Services: cloneRuntimeServiceSpecs(services)}
	}
}

// WithSkills adds skill references to the current run's Selected set. It is
// additive: per-run WithSkills values are unioned with the binding's
// WithDefaultSkills and the SkillProvider's Required entries. Duplicate keys
// must be structurally equal; conflicting duplicates return an error via
// ErrSkillKeyConflict before the adapter is invoked.
func WithSkills(refs ...SkillRef) RunOption {
	return func(ro *runOptions) {
		ro.skills = append(ro.skills, cloneSkillRefs(refs)...)
	}
}

// WithMCP replaces the effective MCP server configuration for one run. Use it
// for request-scoped tool access; use WithDefaultMCP for binding-level tools.
func WithMCP(cfg MCPConfig) RunOption {
	return func(ro *runOptions) {
		copyCfg := MCPConfig{Servers: cloneMCPServerSpecs(cfg.Servers)}
		ro.mcp = &copyCfg
	}
}

// WithProfileResources installs a per-run profile desired state. Skills are
// additive; MCP, instructions, agents, hooks, and config replace the effective
// desired state for their resource kind on this run.
func WithProfileResources(resources ProfileResources) RunOption {
	return func(ro *runOptions) {
		copyResources := cloneProfileResources(resources)
		if len(copyResources.Skills) > 0 {
			ro.skills = append(ro.skills, copyResources.Skills...)
		}
		if copyResources.MCP != nil {
			ro.mcp = copyResources.MCP
		}
		if resources.Agents != nil {
			ro.agents = &AgentPayload{Agents: copyResources.Agents}
		}
		if resources.Hooks != nil {
			ro.hooks = &HookPayload{Hooks: copyResources.Hooks}
		}
		if resources.Config != nil {
			ro.profileConfig = &ProfileConfigPayload{Patches: copyResources.Config}
		}
		if copyResources.Instructions != nil {
			ro.instructions = copyResources.Instructions
			ro.instructionsSet = true
		}
	}
}

// WithAgents replaces the effective desired agent resources for this run.
func WithAgents(specs ...AgentSpec) RunOption {
	return func(ro *runOptions) {
		ro.agents = &AgentPayload{Agents: cloneAgentSpecs(specs)}
	}
}

// WithHooks replaces the effective desired hook resources for this run.
func WithHooks(specs ...HookSpec) RunOption {
	return func(ro *runOptions) {
		ro.hooks = &HookPayload{Hooks: cloneHookSpecs(specs)}
	}
}

// WithProfileConfig replaces the effective desired structured config patches
// for this run.
func WithProfileConfig(patches ...ProfileConfigPatch) RunOption {
	return func(ro *runOptions) {
		ro.profileConfig = &ProfileConfigPayload{Patches: cloneProfileConfigPatches(patches)}
	}
}

// WithModel overrides the bound agent's model for one run. It is the per-run
// counterpart to the binding-level model carried by CodexConfig.Model /
// ClaudeConfig.Model / CursorConfig.Model: the value is forwarded to the
// adapter's native model selection (the `--model` flag for the built-in
// codex / claude / cursor adapters) and takes precedence over the binding
// model for this invocation only.
//
// Unlike WithProfileConfig, it does not persist anything to the agent profile
// on disk and works uniformly across drivers regardless of whether the driver
// exposes a "model" profile-config capability. An empty or whitespace-only
// value is ignored, leaving the binding model in effect.
func WithModel(model string) RunOption {
	return func(ro *runOptions) {
		ro.model = model
	}
}

// WithRunPolicy sets per-run policy. Empty fields inherit from the binding
// default (or stay unset for adapter fallbacks).
func WithRunPolicy(p RunPolicy) RunOption {
	return func(ro *runOptions) {
		copyP := p
		ro.runPolicy = &copyP
	}
}

// WithInstructions overrides the binding-level instruction bundle for one
// run. Passing nil clears the effective instructions for that run.
func WithInstructions(ref *InstructionsBundleRef) RunOption {
	return func(ro *runOptions) {
		ro.instructions = cloneInstructions(ref)
		ro.instructionsSet = true
	}
}

// WithMetadata attaches request-scoped metadata to DriverRunRequest. It is
// opaque to the SDK and intended for adapters, audit logs, and host hooks.
func WithMetadata(key, value string) RunOption {
	return func(ro *runOptions) {
		if ro.metadata == nil {
			ro.metadata = map[string]string{}
		}
		ro.metadata[key] = value
	}
}

// WithAgentIdentity overrides the binding-level identity for one run. This is
// useful when one SDK instance serves multiple tenants or profiles but the
// host has already chosen the concrete agent binding.
func WithAgentIdentity(identity AgentIdentity) RunOption {
	return func(ro *runOptions) {
		copyIdentity := identity
		ro.agent = &copyIdentity
	}
}

// WithStreaming requests that the adapter emit normalized StreamPayload
// events for this run. When the adapter implements StreamAwareDriver it will
// switch to its richest token-level transport (e.g. codex app-server,
// claude --include-partial-messages, cursor --stream-partial-output).
//
// Adapters without streaming support will simply produce an empty
// StreamEvents channel; the run still completes normally with standard
// RunEvents and RunResult.
func WithStreaming() RunOption {
	return func(ro *runOptions) {
		t := true
		ro.streaming = &t
	}
}

// WithoutStreaming disables streaming for this run even when the bound agent
// set WithDefaultStreaming. It is the explicit opt-out for batch / scripted
// invocations on a streaming-default binding.
func WithoutStreaming() RunOption {
	return func(ro *runOptions) {
		f := false
		ro.streaming = &f
	}
}
