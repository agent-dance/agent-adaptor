package agentadaptor

import (
	"context"

	"github.com/agent-dance/agent-adaptor/internal/engine"
)

// sdkFacade is the public SDK implementation: a thin wrapper over the
// execution pipeline in internal/engine. Run/Start orchestration that
// internal tests reach into (dualSink, runOptions, lease timing) stays in
// this package; everything else delegates to engine.Core.
type sdkFacade struct {
	core *engine.Core
}

// Build constructs an SDK and returns configuration errors to the caller.
// Prefer it when the SDK is embedded into a larger application or service.
func Build(opts ...Option) (SDK, error) {
	core, err := engine.Build(opts...)
	if err != nil {
		return nil, err
	}
	return &sdkFacade{core: core}, nil
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

func (s *sdkFacade) Run(ctx context.Context, prompt string, opts ...RunOption) (RunResult, error) {
	return s.Default().Run(ctx, prompt, opts...)
}

func (s *sdkFacade) Start(ctx context.Context, prompt string, opts ...RunOption) (RunHandle, error) {
	return s.Default().Start(ctx, prompt, opts...)
}

func (s *sdkFacade) Default() Runner {
	return s.runnerFor(engine.DefaultAgentName, s.core.DefaultBinding(), true)
}

func (s *sdkFacade) Agent(name string) (Runner, error) {
	if name == "" {
		return nil, ErrAgentNameRequired
	}
	if name == engine.DefaultAgentName {
		return s.Default(), nil
	}
	binding, ok := s.core.NamedBinding(name)
	if !ok {
		return nil, ErrAgentNotFound
	}
	return s.runnerFor(name, binding, false), nil
}

func (s *sdkFacade) Admin() AdminAPI {
	return s.core.Admin()
}

func (s *sdkFacade) runnerFor(name string, binding AgentBinding, isDefault bool) Runner {
	return &runnerImpl{
		core:      s.core,
		name:      name,
		isDefault: isDefault,
		binding:   binding,
	}
}
