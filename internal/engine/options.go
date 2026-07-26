package engine

// Option configures SDK construction. Options install the default agent,
// named agents, and host-provided managers used by every run. The root
// package re-exports the type and wraps every constructor, so the public
// With* surface is unchanged.
type Option func(*Core) error

// AgentOption configures a single AgentBinding. These defaults are merged into
// every run on that binding before per-call RunOptions are applied.
type AgentOption func(*AgentDefaults)

// WithDefaultAgent binds the agent used by SDK.Run, SDK.Start, and
// SDK.Default. It is required exactly once.
func WithDefaultAgent(binding AgentBinding) Option {
	return func(s *Core) error {
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

// WithAgent registers an additional named agent binding. The name "default"
// is reserved for WithDefaultAgent.
func WithAgent(name string, binding AgentBinding) Option {
	return func(s *Core) error {
		if name == "" {
			return ErrAgentNameRequired
		}
		if name == DefaultAgentName {
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

// WithSessionStore enables stateful session modes.
func WithSessionStore(store SessionStore) Option {
	return func(s *Core) error {
		s.sessionStore = store
		return nil
	}
}

// WithWorkspaceManager installs the host component that resolves workspace
// specs into concrete working directories.
func WithWorkspaceManager(manager WorkspaceManager) Option {
	return func(s *Core) error {
		s.workspaceManager = manager
		return nil
	}
}

// WithSkillProvider installs the host-side SkillProvider that backs
// WithSkills / WithDefaultSkills.
func WithSkillProvider(provider SkillProvider) Option {
	return func(s *Core) error {
		s.skillProvider = provider
		return nil
	}
}

// WithSkillSet is sugar for WithSkillProvider when the catalogue is
// fully known at SDK construction time.
func WithSkillSet(set SkillSet) Option {
	return WithSkillProvider(set)
}

// WithSkillMaterializer overrides how SkillFromFS / SkillFromInline sources
// are written to disk before an adapter consumes them.
func WithSkillMaterializer(materializer SkillMaterializer) Option {
	return func(s *Core) error {
		s.skillMaterializer = materializer
		return nil
	}
}

// WithRuntimeServiceManager installs the host component that starts, finds,
// and releases runtime services requested by bindings or individual runs.
func WithRuntimeServiceManager(manager RuntimeServiceManager) Option {
	return func(s *Core) error {
		s.runtimeManager = manager
		return nil
	}
}

// WithEventBuffer configures the per-run event-sink capacity and backpressure
// policy.
func WithEventBuffer(runBuf, streamBuf int, policy EventBackpressure) Option {
	return func(s *Core) error {
		s.eventRunBuf = runBuf
		s.eventStreamBuf = streamBuf
		s.eventPolicy = policy
		return nil
	}
}

// --- agent-level options ----------------------------------------------------

// WithDefaultPermissionHandler binds a PermissionHandler at the agent level.
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

// WithDefaultIdentity sets the binding-level identity visible to host hooks.
func WithDefaultIdentity(identity AgentIdentity) AgentOption {
	return func(defaults *AgentDefaults) {
		defaults.Agent = identity
	}
}

// WithDefaultWorkspace sets the binding-level workspace request.
func WithDefaultWorkspace(spec WorkspaceSpec) AgentOption {
	return func(defaults *AgentDefaults) {
		defaults.Workspace = spec
	}
}

// WithDefaultSkills installs per-agent default skill references.
func WithDefaultSkills(refs ...SkillRef) AgentOption {
	return func(defaults *AgentDefaults) {
		defaults.Skills = append(defaults.Skills, cloneSkillRefs(refs)...)
	}
}

// WithDefaultMCP sets the binding-level MCP server configuration.
func WithDefaultMCP(cfg MCPConfig) AgentOption {
	return func(defaults *AgentDefaults) {
		copyCfg := MCPConfig{Servers: cloneMCPServerSpecs(cfg.Servers)}
		defaults.MCP = &copyCfg
	}
}

// WithDefaultProfileResources installs a binding-level profile desired state.
func WithDefaultProfileResources(resources ProfileResources) AgentOption {
	return func(defaults *AgentDefaults) {
		copyResources := cloneProfileResources(resources)
		if len(copyResources.Skills) > 0 {
			defaults.Skills = append(defaults.Skills, copyResources.Skills...)
		}
		if copyResources.MCP != nil {
			defaults.MCP = copyResources.MCP
		}
		if resources.Agents != nil {
			defaults.Agents = copyResources.Agents
			defaults.profileDeclared.Agents = true
		}
		if resources.Hooks != nil {
			defaults.Hooks = copyResources.Hooks
			defaults.profileDeclared.Hooks = true
		}
		if resources.Config != nil {
			defaults.ProfileConfig = copyResources.Config
			defaults.profileDeclared.Config = true
		}
		if copyResources.Instructions != nil {
			defaults.Instructions = copyResources.Instructions
			defaults.profileDeclared.Instructions = true
		}
	}
}

// WithDefaultAgents sets the binding-level desired sub-agent resources.
func WithDefaultAgents(specs ...AgentSpec) AgentOption {
	return func(defaults *AgentDefaults) {
		defaults.Agents = cloneAgentSpecs(specs)
		defaults.profileDeclared.Agents = true
	}
}

// WithDefaultHooks sets the binding-level desired hook resources.
func WithDefaultHooks(specs ...HookSpec) AgentOption {
	return func(defaults *AgentDefaults) {
		defaults.Hooks = cloneHookSpecs(specs)
		defaults.profileDeclared.Hooks = true
	}
}

// WithDefaultProfileConfig sets the binding-level structured profile config
// patches.
func WithDefaultProfileConfig(patches ...ProfileConfigPatch) AgentOption {
	return func(defaults *AgentDefaults) {
		defaults.ProfileConfig = cloneProfileConfigPatches(patches)
		defaults.profileDeclared.Config = true
	}
}

// WithDefaultRuntimeServices attaches default runtime-service requirements to
// an agent binding.
func WithDefaultRuntimeServices(services ...RuntimeServiceSpec) AgentOption {
	return func(defaults *AgentDefaults) {
		if len(services) == 0 {
			defaults.Runtime = nil
			return
		}
		defaults.Runtime = &WorkspaceRuntimeConfig{Services: cloneRuntimeServiceSpecs(services)}
	}
}

// WithDefaultRunPolicy sets binding-level defaults.
func WithDefaultRunPolicy(p RunPolicy) AgentOption {
	return func(defaults *AgentDefaults) {
		copyP := p
		defaults.RunPolicy = &copyP
	}
}

// WithDefaultInstructions sets the binding-level instruction bundle.
func WithDefaultInstructions(ref *InstructionsBundleRef) AgentOption {
	return func(defaults *AgentDefaults) {
		defaults.Instructions = cloneInstructions(ref)
		defaults.profileDeclared.Instructions = true
	}
}

// WithDefaultMetadata attaches binding-level metadata copied into every
// DriverRunRequest.
func WithDefaultMetadata(key, value string) AgentOption {
	return func(defaults *AgentDefaults) {
		if defaults.Metadata == nil {
			defaults.Metadata = map[string]string{}
		}
		defaults.Metadata[key] = value
	}
}

// WithDefaultStreaming marks the bound agent as streaming-by-default.
func WithDefaultStreaming() AgentOption {
	return func(defaults *AgentDefaults) {
		t := true
		defaults.Streaming = &t
	}
}
