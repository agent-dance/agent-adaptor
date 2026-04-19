package agentadaptor

import (
	"context"
	"sync"
)

const defaultAgentName = "default"

type sdkImpl struct {
	defaultBinding   AgentBinding
	namedBindings    map[string]AgentBinding
	sessionStore     SessionStore
	workspaceManager WorkspaceManager
	skillCatalog     SkillCatalog
	skillAssembler   SkillAssembler
	runtimeManager   RuntimeServiceManager
	mu               sync.RWMutex
	skillSelections  map[string][]string
}

// Build constructs an SDK and returns configuration errors to the caller.
// Prefer it when the SDK is embedded into a larger application or service.
func Build(opts ...Option) (SDK, error) {
	s := &sdkImpl{
		namedBindings:    map[string]AgentBinding{},
		workspaceManager: passthroughWorkspaceManager{},
		skillCatalog:     passthroughSkillCatalog{},
		skillAssembler:   defaultSkillAssembler{},
		runtimeManager:   noopRuntimeManager{},
		skillSelections:  map[string][]string{},
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

func (s *sdkImpl) desiredSkillsFor(name string, defaults []string) []string {
	s.mu.RLock()
	override, ok := s.skillSelections[name]
	s.mu.RUnlock()
	if ok {
		return cloneStrings(override)
	}
	return cloneStrings(defaults)
}

func (s *sdkImpl) setDesiredSkillsFor(name string, desired []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.skillSelections[name] = cloneStrings(desired)
}
