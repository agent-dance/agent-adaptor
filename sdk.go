package agentadaptor

import (
	"context"
	"strings"
	"sync"
)

const (
	defaultAgentName = "default"

	// defaultRunEventBuffer matches the legacy channelEventSink capacity.
	defaultRunEventBuffer = 64
	// defaultStreamEventBuffer is sized for interactive chat loads with
	// typical token cadence of 50–100 events per second.
	defaultStreamEventBuffer = 1024
)

type sdkImpl struct {
	defaultBinding    AgentBinding
	namedBindings     map[string]AgentBinding
	sessionStore      SessionStore
	workspaceManager  WorkspaceManager
	skillProvider     SkillProvider
	skillMaterializer SkillMaterializer
	runtimeManager    RuntimeServiceManager
	mu                sync.RWMutex
	// skillSelections tracks process-local SetSelectedSkills overrides per
	// agent name. The value is the list of keys the admin picked; Required
	// skills from the SkillProvider are unioned in at Run time.
	skillSelections map[string][]string

	eventRunBuf    int
	eventStreamBuf int
	eventPolicy    EventBackpressure
}

// Build constructs an SDK and returns configuration errors to the caller.
// Prefer it when the SDK is embedded into a larger application or service.
func Build(opts ...Option) (SDK, error) {
	s := &sdkImpl{
		namedBindings:     map[string]AgentBinding{},
		workspaceManager:  passthroughWorkspaceManager{},
		skillProvider:     emptySkillProvider{},
		skillMaterializer: newDefaultSkillMaterializer(),
		runtimeManager:    noopRuntimeManager{},
		skillSelections:   map[string][]string{},
		eventRunBuf:       defaultRunEventBuffer,
		eventStreamBuf:    defaultStreamEventBuffer,
		eventPolicy:       BackpressureDropStream,
	}
	for _, opt := range opts {
		if opt == nil {
			continue
		}
		if err := opt(s); err != nil {
			return nil, err
		}
	}
	if s.defaultBinding == nil {
		return nil, ErrDefaultAgentMissing
	}
	return s, nil
}

// eventSinkSettings returns a snapshot of the runtime sink configuration used
// by Start() when building a dualSink. Values <= 0 fall back to the SDK
// defaults.
func (s *sdkImpl) eventSinkSettings() (runBuf, streamBuf int, policy EventBackpressure) {
	runBuf = s.eventRunBuf
	if runBuf <= 0 {
		runBuf = defaultRunEventBuffer
	}
	streamBuf = s.eventStreamBuf
	if streamBuf <= 0 {
		streamBuf = defaultStreamEventBuffer
	}
	policy = s.eventPolicy
	return
}

// New is the panic-on-invalid-config convenience constructor for applications
// and tests that want fail-fast setup.
func New(opts ...Option) SDK {
	sdk, err := Build(opts...)
	if err != nil {
		panic(err)
	}
	return sdk
}

func (s *sdkImpl) Run(ctx context.Context, prompt string, opts ...RunOption) (RunResult, error) {
	return s.Default().Run(ctx, prompt, opts...)
}

func (s *sdkImpl) Start(ctx context.Context, prompt string, opts ...RunOption) (RunHandle, error) {
	return s.Default().Start(ctx, prompt, opts...)
}

func (s *sdkImpl) Default() Runner {
	return s.runnerFor(defaultAgentName, s.defaultBinding, true)
}

func (s *sdkImpl) Agent(name string) (Runner, error) {
	if name == "" {
		return nil, ErrAgentNameRequired
	}
	if name == defaultAgentName {
		return s.Default(), nil
	}
	binding, ok := s.namedBindings[name]
	if !ok {
		return nil, ErrAgentNotFound
	}
	return s.runnerFor(name, binding, false), nil
}

func (s *sdkImpl) Admin() AdminAPI {
	return &adminImpl{sdk: s}
}

func (s *sdkImpl) runnerFor(name string, binding AgentBinding, isDefault bool) Runner {
	return &runnerImpl{
		sdk:       s,
		name:      name,
		isDefault: isDefault,
		binding:   binding,
	}
}

// selectedRefsFor returns the effective default SkillRef list for agent name.
// If SetSelectedSkills has installed a process-local override, the override
// keys are returned as SkillKey refs (the admin picks by key, not by full
// Skill value). Otherwise the binding's WithDefaultSkills values are cloned
// and returned.
func (s *sdkImpl) selectedRefsFor(name string, bindingDefaults []SkillRef) []SkillRef {
	s.mu.RLock()
	override, ok := s.skillSelections[name]
	s.mu.RUnlock()
	if ok {
		out := make([]SkillRef, 0, len(override))
		for _, key := range override {
			key = strings.TrimSpace(key)
			if key == "" {
				continue
			}
			out = append(out, SkillKey(key))
		}
		return out
	}
	return cloneSkillRefs(bindingDefaults)
}

// setSelectedSkillsFor installs a process-local selection override for the
// given agent name. Callers must have already validated that each key
// exists in the SkillProvider.
func (s *sdkImpl) setSelectedSkillsFor(name string, selected []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.skillSelections[name] = cloneStrings(selected)
}
