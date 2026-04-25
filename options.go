package agentadaptor

type Option func(*sdkImpl) error

func WithDefaultAgent(binding AgentBinding) Option {
	return func(s *sdkImpl) error {
		if binding == nil {
			return ErrDefaultAgentMissing
		}
		if s.defaultBinding != nil {
			return ErrDefaultAgentAlreadyConfigured
		}
		if err := validateAgentBinding(binding); err != nil {
			return err
		}
		s.defaultBinding = binding
		return nil
	}
}

func WithAgent(name string, binding AgentBinding) Option {
	return func(s *sdkImpl) error {
		if name == "" {
			return ErrAgentNameRequired
		}
		if name == defaultAgentName {
			return ErrReservedAgentName
		}
		if binding == nil {
			return ErrAgentBindingRequired
		}
		if s.namedBindings == nil {
			s.namedBindings = map[string]AgentBinding{}
		}
		if _, exists := s.namedBindings[name]; exists {
			return &DuplicateAgentError{Name: name}
		}
		if err := validateAgentBinding(binding); err != nil {
			return err
		}
		s.namedBindings[name] = binding
		return nil
	}
}

func WithSessionStore(store SessionStore) Option {
	return func(s *sdkImpl) error {
		s.sessionStore = store
		return nil
	}
}

func WithWorkspaceManager(manager WorkspaceManager) Option {
	return func(s *sdkImpl) error {
		s.workspaceManager = manager
		return nil
	}
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
	return func(s *sdkImpl) error {
		s.skillProvider = provider
		return nil
	}
}

// WithSkillSet is sugar for WithSkillProvider when the catalogue is
// fully known at SDK construction time. The supplied SkillSet
// implements both SkillProvider (per-key fetch) and SkillCatalog
// (full enumeration), so Admin.ListSkills works automatically.
//
// Hosts with a remote skill store should implement SkillProvider
// directly instead of constructing a SkillSet.
func WithSkillSet(set SkillSet) Option {
	return WithSkillProvider(set)
}

// WithSkillMaterializer overrides how SkillFromFS / SkillFromInline sources
// are written to disk before an adapter consumes them. When unset, the SDK
// uses the built-in cache-root materializer documented in
// docs/skill-api-design.md §5.7.
func WithSkillMaterializer(materializer SkillMaterializer) Option {
	return func(s *sdkImpl) error {
		s.skillMaterializer = materializer
		return nil
	}
}

func WithRuntimeServiceManager(manager RuntimeServiceManager) Option {
	return func(s *sdkImpl) error {
		s.runtimeManager = manager
		return nil
	}
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
	return func(s *sdkImpl) error {
		s.eventRunBuf = runBuf
		s.eventStreamBuf = streamBuf
		s.eventPolicy = policy
		return nil
	}
}

// WithDefaultPermissionHandler binds a PermissionHandler at the agent level.
// Per-call WithPermissionHandler still overrides this default.
func WithDefaultPermissionHandler(h PermissionHandler) AgentOption {
	return func(defaults *AgentDefaults) {
		defaults.PermissionHandler = h
	}
}

// WithDefaultPlanReviewHandler binds a PlanReviewHandler at the agent level.
func WithDefaultPlanReviewHandler(h PlanReviewHandler) AgentOption {
	return func(defaults *AgentDefaults) {
		defaults.PlanReviewHandler = h
	}
}

// WithDefaultQuestionHandler binds a QuestionHandler at the agent level.
func WithDefaultQuestionHandler(h QuestionHandler) AgentOption {
	return func(defaults *AgentDefaults) {
		defaults.QuestionHandler = h
	}
}

type AgentOption func(*AgentDefaults)

func WithDefaultIdentity(identity AgentIdentity) AgentOption {
	return func(defaults *AgentDefaults) {
		defaults.Agent = identity
	}
}

func WithDefaultWorkspace(spec WorkspaceSpec) AgentOption {
	return func(defaults *AgentDefaults) {
		defaults.Workspace = spec
	}
}

// WithDefaultSkills installs per-agent default skill references. Each SkillRef
// is either a SkillKey (resolved against the SkillProvider) or an inline
// Skill value. Multiple calls are additive: later SkillRef values are appended
// to the previously-declared set. Duplicate keys must be structurally equal
// (see ErrSkillKeyConflict).
func WithDefaultSkills(refs ...SkillRef) AgentOption {
	return func(defaults *AgentDefaults) {
		defaults.Skills = append(defaults.Skills, cloneSkillRefs(refs)...)
	}
}

func WithDefaultMCP(cfg MCPConfig) AgentOption {
	return func(defaults *AgentDefaults) {
		copyCfg := MCPConfig{Servers: cloneMCPServerSpecs(cfg.Servers)}
		defaults.MCP = &copyCfg
	}
}

// WithDefaultRuntimeServices attaches default runtime-service requirements to
// an agent binding. Per-run WithRuntimeServices(...) overrides these defaults.
func WithDefaultRuntimeServices(services ...RuntimeServiceSpec) AgentOption {
	return func(defaults *AgentDefaults) {
		if len(services) == 0 {
			defaults.Runtime = nil
			return
		}
		defaults.Runtime = &WorkspaceRuntimeConfig{Services: cloneRuntimeServiceSpecs(services)}
	}
}

// WithDefaultRunPolicy sets binding-level defaults. Per-field empty values
// (…Inherit) mean "no default for that field" until a per-call WithRunPolicy
// sets it.
func WithDefaultRunPolicy(p RunPolicy) AgentOption {
	return func(defaults *AgentDefaults) {
		copyP := p
		defaults.RunPolicy = &copyP
	}
}

func WithDefaultInstructions(ref *InstructionsBundleRef) AgentOption {
	return func(defaults *AgentDefaults) {
		if ref == nil {
			defaults.Instructions = nil
			return
		}
		copyRef := *ref
		defaults.Instructions = &copyRef
	}
}

func WithDefaultMetadata(key, value string) AgentOption {
	return func(defaults *AgentDefaults) {
		if defaults.Metadata == nil {
			defaults.Metadata = map[string]string{}
		}
		defaults.Metadata[key] = value
	}
}

// WithDefaultStreaming marks the bound agent as streaming-by-default. Per-call
// WithStreaming / WithoutStreaming still override this default.
func WithDefaultStreaming() AgentOption {
	return func(defaults *AgentDefaults) {
		t := true
		defaults.Streaming = &t
	}
}

type RunOption func(*runOptions)

type runOptions struct {
	session      *SessionRequest
	workspace    WorkspaceSpec
	runtime      *WorkspaceRuntimeConfig
	skills       []SkillRef
	mcp          *MCPConfig
	runPolicy    *RunPolicy
	instructions *InstructionsBundleRef
	metadata     map[string]string
	agent        *AgentIdentity
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

func WithSession(req SessionRequest) RunOption {
	return func(ro *runOptions) {
		copyReq := req
		ro.session = &copyReq
	}
}

func WithSessionKey(namespace, key string) RunOption {
	return WithSession(SessionRequest{
		Namespace: namespace,
		Key:       key,
		Mode:      SessionContinueOrStart,
	})
}

func WithContinueSession(id string) RunOption {
	return WithSession(SessionRequest{
		ID:   id,
		Mode: SessionContinueOnly,
	})
}

func WithNewSession(namespace, key string) RunOption {
	return WithSession(SessionRequest{
		Namespace: namespace,
		Key:       key,
		Mode:      SessionStartNew,
	})
}

func WithForkSession(fromID, namespace, key string) RunOption {
	return WithSession(SessionRequest{
		Namespace: namespace,
		Key:       key,
		Mode:      SessionFork,
		ForkFrom:  fromID,
	})
}

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

func WithMCP(cfg MCPConfig) RunOption {
	return func(ro *runOptions) {
		copyCfg := MCPConfig{Servers: cloneMCPServerSpecs(cfg.Servers)}
		ro.mcp = &copyCfg
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

func WithInstructions(ref *InstructionsBundleRef) RunOption {
	return func(ro *runOptions) {
		if ref == nil {
			ro.instructions = nil
			return
		}
		copyRef := *ref
		ro.instructions = &copyRef
	}
}

func WithMetadata(key, value string) RunOption {
	return func(ro *runOptions) {
		if ro.metadata == nil {
			ro.metadata = map[string]string{}
		}
		ro.metadata[key] = value
	}
}

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
