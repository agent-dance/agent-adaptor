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
		return detector.DetectModel(ctx, a.binding.Config(), a.binding.Defaults().Profile)
	}
	return nil, nil
}

func (a *agentAdminImpl) GetProfile(ctx context.Context) (AgentProfile, error) {
	if provider, ok := a.binding.Adapter().(ProfileAwareDriver); ok {
		return provider.GetProfile(ctx, a.binding.Config(), a.binding.Defaults().Agent, a.binding.Defaults().Profile)
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
		return provider.GetQuota(ctx, a.binding.Config(), a.binding.Defaults().Profile)
	}
	return QuotaReport{
		DriverType: a.binding.Adapter().Descriptor().Type,
		Available:  false,
		Error:      "agent does not expose live quota data",
	}, nil
}

func (a *agentAdminImpl) ListSkills(ctx context.Context) (SkillSnapshot, error) {
	defaults := a.binding.Defaults()
	defaultRefs := a.sdk.selectedRefsFor(a.name, defaults.Skills)
	// Expose the binding's inline Skill values + the upstream
	// SkillCatalog (when available) as non-selected candidates so the
	// merged catalogue participates in the snapshot's Resolved set
	// even when an Admin override is active. When the provider does
	// NOT implement SkillCatalog, only inline candidates participate;
	// SkillSyncMode propagates as Unsupported and the host UI is
	// expected to fall back to the store's own discovery surface.
	candidates, err := a.sdk.collectAdminCandidates(ctx, defaults)
	if err != nil {
		return SkillSnapshot{}, err
	}
	payload, selected, resolved, err := a.sdk.resolveSkills(ctx, defaults.Agent, defaultRefs, nil, candidates)
	if err != nil {
		return SkillSnapshot{}, err
	}
	return buildSkillSnapshot(ctx, a.binding.Adapter(), a.binding.Config(), payload, selected, resolved, defaults.Profile)
}

// SetSelectedSkills records a process-local selection override for this
// agent. The keys must reference skills visible through the SkillProvider
// (bare SkillKey refs); inline Skill values can only be supplied through
// WithDefaultSkills or WithSkills because SetSelectedSkills is explicitly a
// "select from the catalogue" operation.
func (a *agentAdminImpl) SetSelectedSkills(ctx context.Context, keys []string) (SkillSnapshot, error) {
	defaults := a.binding.Defaults()
	refs := make([]SkillRef, 0, len(keys))
	normalized := make([]string, 0, len(keys))
	for _, key := range keys {
		trimmed := normalizeSkillKey(key)
		if trimmed == "" {
			continue
		}
		refs = append(refs, SkillKey(trimmed))
		normalized = append(normalized, trimmed)
	}
	// Binding default inline Skill values + the upstream SkillCatalog
	// are re-introduced as non-selected candidates so that
	// SetSelectedSkills can target them by key. (User-supplied keys
	// that don't appear in either pool are reported as ErrSkillNotFound.)
	candidates, err := a.sdk.collectAdminCandidates(ctx, defaults)
	if err != nil {
		return SkillSnapshot{}, err
	}
	payload, selected, resolved, err := a.sdk.resolveSkills(ctx, defaults.Agent, refs, nil, candidates)
	if err != nil {
		return SkillSnapshot{}, err
	}
	snapshot, err := syncSkillSnapshot(ctx, a.binding.Adapter(), a.binding.Config(), payload, selected, resolved, defaults.Profile)
	if err != nil {
		return SkillSnapshot{}, err
	}
	a.sdk.setSelectedSkillsFor(a.name, normalized)
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
