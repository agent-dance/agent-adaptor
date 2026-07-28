package engine

import "context"

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

// emptySkillProvider is the default SkillProvider installed when the host
// does not call WithSkillProvider. GetSkills returns an empty map for
// every key set, which makes per-run WithSkills (with inline Skill
// values) the only source of skills for the SDK.
//
// emptySkillProvider does NOT implement SkillCatalog, so
// Inspector.Skills correctly reports SkillSyncUnsupported when no
// real provider is wired up.
type emptySkillProvider struct{}

func (emptySkillProvider) GetSkills(_ context.Context, _ []string) (map[string]Skill, error) {
	return nil, nil
}

type noopRuntimeManager struct{}

func (noopRuntimeManager) Ensure(_ context.Context, _ RuntimeServiceRequest) ([]RuntimeServiceRef, error) {
	return nil, nil
}

func (noopRuntimeManager) ReleaseByRun(_ context.Context, _ string) error {
	return nil
}

func (noopRuntimeManager) ReleaseByLabels(_ context.Context, _ map[string]string) error {
	return nil
}

// buildSkillSnapshot routes a resolved payload to the Driver's ListSkills
// when available, or synthesises an "unsupported" snapshot otherwise. The
// incoming ctx is propagated so Drivers can respect cancellation of the
// Inspector.Skills or Run call that triggered the snapshot.
func buildSkillSnapshot(ctx context.Context, driver Driver, config any, payload ResolvedSkills, selected []string, resolved []Skill, profile *ProfileSelection) (SkillSnapshot, error) {
	skillDriver, ok := driver.(SkillSupport)
	if !ok {
		return SkillSnapshot{
			DriverType:  driver.Descriptor().Type,
			Supported:   false,
			Mode:        SkillSyncUnsupported,
			Selected:    cloneStrings(selected),
			Resolved:    cloneSkills(resolved),
			Warnings:    cloneStrings(payload.Warnings),
			Fingerprint: payload.Fingerprint,
		}, nil
	}
	return skillDriver.ListSkills(ctx, config, payload, selected, resolved, profile)
}

// syncSkillSnapshot routes a resolved payload to the Driver's SyncSkills
// when available. ctx is propagated so cancellation / deadlines set by
// Agent.SelectSkills flow through to the Driver's sync work.
func syncSkillSnapshot(ctx context.Context, driver Driver, config any, payload ResolvedSkills, selected []string, resolved []Skill, profile *ProfileSelection) (SkillSnapshot, error) {
	skillDriver, ok := driver.(SkillSupport)
	if !ok {
		return SkillSnapshot{
			DriverType:  driver.Descriptor().Type,
			Supported:   false,
			Mode:        SkillSyncUnsupported,
			Selected:    cloneStrings(selected),
			Resolved:    cloneSkills(resolved),
			Warnings:    cloneStrings(payload.Warnings),
			Fingerprint: payload.Fingerprint,
		}, nil
	}
	return skillDriver.SyncSkills(ctx, config, payload, selected, resolved, profile)
}
