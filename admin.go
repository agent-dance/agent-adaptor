package agentadaptor

import (
	"context"
	"sort"
)

type adminImpl struct {
	sdk *sdkImpl
}

type agentAdminImpl struct {
	sdk       *sdkImpl
	name      string
	isDefault bool
	binding   AgentBinding
}

func (a *adminImpl) Default() AgentAdmin {
	return &agentAdminImpl{
		sdk:       a.sdk,
		name:      defaultAgentName,
		isDefault: true,
		binding:   a.sdk.defaultBinding,
	}
}

func (a *adminImpl) Agent(name string) (AgentAdmin, error) {
	if name == "" {
		return nil, ErrAgentNameRequired
	}
	if name == defaultAgentName {
		return a.Default(), nil
	}
	binding, ok := a.sdk.namedBindings[name]
	if !ok {
		return nil, ErrAgentNotFound
	}
	return &agentAdminImpl{
		sdk:       a.sdk,
		name:      name,
		isDefault: false,
		binding:   binding,
	}, nil
}

func (a *adminImpl) Agents() []AgentInfo {
	names := make([]string, 0, len(a.sdk.namedBindings))
	for name := range a.sdk.namedBindings {
		names = append(names, name)
	}
	sort.Strings(names)

	out := make([]AgentInfo, 0, len(names)+1)
	out = append(out, newAgentInfo(defaultAgentName, a.sdk.defaultBinding, true))
	for _, name := range names {
		out = append(out, newAgentInfo(name, a.sdk.namedBindings[name], false))
	}
	return out
}

func (a *agentAdminImpl) Info() AgentInfo {
	return newAgentInfo(a.name, a.binding, a.isDefault)
}

func (a *agentAdminImpl) CheckEnvironment(ctx context.Context) (EnvironmentReport, error) {
	if checker, ok := a.binding.Adapter().(EnvironmentAwareDriver); ok {
		return checker.CheckEnvironment(ctx, a.binding.Config())
	}
	descriptor := a.binding.Adapter().Descriptor()
	return summarizeEnvironment(descriptor.Type, []EnvironmentCheck{
		{Code: "noop", Level: "info", Message: "agent does not expose environment checks"},
	}), nil
}

func (a *agentAdminImpl) ListModels(ctx context.Context) ([]ModelInfo, error) {
	if modelDriver, ok := a.binding.Adapter().(ModelAwareDriver); ok {
		return modelDriver.ListModels(ctx, a.binding.Config())
	}
	return append([]ModelInfo(nil), a.binding.Adapter().Descriptor().Models...), nil
}

func (a *agentAdminImpl) DetectModel(ctx context.Context) (*DetectedModel, error) {
	if detector, ok := a.binding.Adapter().(ModelDetectorDriver); ok {
		return detector.DetectModel(ctx, a.binding.Config())
	}
	return nil, nil
}

func (a *agentAdminImpl) GetProfile(ctx context.Context) (AgentProfile, error) {
	if provider, ok := a.binding.Adapter().(ProfileAwareDriver); ok {
		return provider.GetProfile(ctx, a.binding.Config(), a.binding.Defaults().Agent)
	}
	return AgentProfile{
		DriverType: a.binding.Adapter().Descriptor().Type,
		Supported:  false,
		Source:     AgentProfileSourceUnsupported,
		Error:      "agent does not expose local profile semantics",
	}, nil
}

func (a *agentAdminImpl) ConfigSchema(ctx context.Context) (*ConfigSchema, error) {
	if provider, ok := a.binding.Adapter().(ConfigSchemaAwareDriver); ok {
		return provider.ConfigSchema(ctx, a.binding.Config())
	}
	return cloneConfigSchema(a.binding.Adapter().Descriptor().ConfigSchema), nil
}

func (a *agentAdminImpl) GetQuota(ctx context.Context) (QuotaReport, error) {
	if provider, ok := a.binding.Adapter().(QuotaAwareDriver); ok {
		return provider.GetQuota(ctx, a.binding.Config())
	}
	return QuotaReport{
		DriverType: a.binding.Adapter().Descriptor().Type,
		Available:  false,
		Error:      "agent does not expose live quota data",
	}, nil
}

func (a *agentAdminImpl) ListSkills(ctx context.Context) (SkillSnapshot, error) {
	defaults := a.binding.Defaults()
	desired := a.sdk.desiredSkillsFor(a.name, defaults.Skills)
	payload, err := a.sdk.prepareSkills(ctx, a.binding, defaults.Agent, WorkspaceLease{}, desired)
	if err != nil {
		return SkillSnapshot{}, err
	}
	return buildSkillSnapshot(a.binding.Adapter(), a.binding.Config(), payload, desired)
}

func (a *agentAdminImpl) SyncSkills(ctx context.Context, desired []string) (SkillSnapshot, error) {
	defaults := a.binding.Defaults()
	payload, err := a.sdk.prepareSkills(ctx, a.binding, defaults.Agent, WorkspaceLease{}, desired)
	if err != nil {
		return SkillSnapshot{}, err
	}
	snapshot, err := syncSkillSnapshot(a.binding.Adapter(), a.binding.Config(), payload, desired)
	if err != nil {
		return SkillSnapshot{}, err
	}
	a.sdk.setDesiredSkillsFor(a.name, snapshot.Desired)
	return snapshot, nil
}

func newAgentInfo(name string, binding AgentBinding, isDefault bool) AgentInfo {
	descriptor := binding.Adapter().Descriptor()
	return AgentInfo{
		Name:        name,
		Default:     isDefault,
		DriverType:  descriptor.Type,
		DisplayName: descriptor.DisplayName,
		Descriptor:  descriptor,
	}
}
