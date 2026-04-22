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

func WithSkillCatalog(catalog SkillCatalog) Option {
	return func(s *sdkImpl) error {
		s.skillCatalog = catalog
		return nil
	}
}

func WithSkillAssembler(assembler SkillAssembler) Option {
	return func(s *sdkImpl) error {
		s.skillAssembler = assembler
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

func WithDefaultSkills(keys ...string) AgentOption {
	return func(defaults *AgentDefaults) {
		defaults.Skills = append([]string(nil), keys...)
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
	skills       []string
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

func WithSkills(keys ...string) RunOption {
	return func(ro *runOptions) {
		ro.skills = append([]string(nil), keys...)
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
