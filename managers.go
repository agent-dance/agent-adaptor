package agentadaptor

import (
	"context"
	"fmt"
	"strings"
)

type passthroughWorkspaceManager struct{}

func (passthroughWorkspaceManager) Resolve(_ context.Context, req WorkspaceRequest) (WorkspaceLease, error) {
	baseCWD := ensureBaseCWD(req.BaseCWD)
	data := WorkspaceRequestData{
		Mode:         WorkspaceModeShared,
		StrategyType: WorkspaceStrategyProjectPrimary,
	}
	if req.Spec != nil {
		data = req.Spec.workspaceRequest()
	}
	metadata := cloneStringMap(req.Metadata)
	if metadata == nil {
		metadata = map[string]string{}
	}
	metadata["workspace_manager"] = "passthrough"
	return WorkspaceLease{
		ID:           stableHash("workspace", baseCWD, data, metadata),
		Mode:         data.Mode,
		StrategyType: data.StrategyType,
		CWD:          baseCWD,
		Fingerprint:  stableHash("workspace_fingerprint", baseCWD, data),
		Metadata:     metadata,
	}, nil
}

func (passthroughWorkspaceManager) Release(_ context.Context, _ WorkspaceLease, _ WorkspaceReleaseMode) error {
	return nil
}

type passthroughSkillCatalog struct{}

func (passthroughSkillCatalog) Resolve(_ context.Context, _ string, refs []string) ([]Skill, error) {
	out := make([]Skill, 0, len(refs))
	for _, ref := range refs {
		trimmed := strings.TrimSpace(ref)
		if trimmed == "" {
			continue
		}
		skill := Skill{Key: trimmed}
		if looksLikePathReference(trimmed) {
			skill.PathHint = trimmed
		}
		out = append(out, skill)
	}
	return out, nil
}

type defaultSkillAssembler struct{}

func (defaultSkillAssembler) Prepare(_ context.Context, req SkillAssemblyRequest) (SkillPayload, error) {
	return prepareSkillPayload(req), nil
}

type noopRuntimeManager struct{}

func (noopRuntimeManager) Ensure(_ context.Context, _ RuntimeServiceRequest) ([]RuntimeServiceRef, error) {
	return nil, nil
}

func (noopRuntimeManager) ReleaseByRun(_ context.Context, _ string) error {
	return nil
}

func buildSkillSnapshot(driver DriverAdapter, config any, payload SkillPayload, desired []string, profile *ProfileSelection) (SkillSnapshot, error) {
	skillDriver, ok := driver.(SkillAwareDriver)
	if !ok {
		return SkillSnapshot{
			DriverType: driver.Descriptor().Type,
			Supported:  false,
			Mode:       SkillSyncUnsupported,
			Desired:    cloneStrings(payload.Requested),
			Resolved:   cloneSkills(payload.Resolved),
			Warnings:   cloneStrings(payload.Warnings),
		}, nil
	}
	return skillDriver.ListSkills(context.Background(), config, payload, profile)
}

func syncSkillSnapshot(driver DriverAdapter, config any, payload SkillPayload, desired []string, profile *ProfileSelection) (SkillSnapshot, error) {
	skillDriver, ok := driver.(SkillAwareDriver)
	if !ok {
		return SkillSnapshot{
			DriverType: driver.Descriptor().Type,
			Supported:  false,
			Mode:       SkillSyncUnsupported,
			Desired:    cloneStrings(payload.Requested),
			Resolved:   cloneSkills(payload.Resolved),
			Warnings:   cloneStrings(payload.Warnings),
		}, nil
	}
	return skillDriver.SyncSkills(context.Background(), config, payload, desired, profile)
}

func validateAdapterConfig(driver DriverAdapter, cfg any) error {
	if err := driver.ValidateConfig(cfg); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidDriverConfig, err)
	}
	return nil
}
