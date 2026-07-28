package engine

import (
	"context"
	"fmt"
	"strings"
)

// profileResourceDriver is the optional driver extension for reporting and
// synchronizing the complete provider profile resource set.
type profileResourceDriver interface {
	SnapshotProfileResources(ctx context.Context, cfg any, identity AgentIdentity, profile *ProfileSelection, payload ProfilePayload, selected []string, resolved []Skill) (ProfileSnapshot, error)
	SyncProfileResources(ctx context.Context, cfg any, identity AgentIdentity, profile *ProfileSelection, payload ProfilePayload, selected []string, resolved []Skill) (ProfileSnapshot, error)
}

func snapshotFromPayload(driverType string, profile AgentProfile, selection *ProfileSelection, payload ProfilePayload, synced bool) ProfileSnapshot {
	return ProfileSnapshot{
		DriverType:  driverType,
		Profile:     profile,
		Kind:        profileKindForSnapshot(profile, selection),
		Fingerprint: payload.Fingerprint,
		Warnings:    cloneStrings(payload.Warnings),
		Resources: []ResourceSnapshot{
			{Kind: ProfileResourceSkills, Fingerprint: payload.Skills.Fingerprint, Managed: payload.Skills.Keys(), Support: ProfileResourceSupportPortableCore, Materialization: ProfileResourceMaterializationFileManaged, Warnings: cloneStrings(payload.Skills.Warnings)},
			unsupportedResourceSnapshot(ProfileResourceMCP, payload.MCP.Fingerprint, mcpKeys(payload.MCP), cloneStrings(payload.MCP.Warnings), synced),
			unsupportedResourceSnapshot(ProfileResourceAgents, payload.Agents.Fingerprint, agentKeys(payload.Agents), nil, synced),
			unsupportedResourceSnapshot(ProfileResourceHooks, payload.Hooks.Fingerprint, hookKeys(payload.Hooks), nil, synced),
			unsupportedResourceSnapshot(ProfileResourceInstructions, instructionFingerprint(payload.Instructions), instructionKeys(payload.Instructions), nil, synced),
			unsupportedResourceSnapshot(ProfileResourceConfig, payload.Config.Fingerprint, configPatchKeys(payload.Config), cloneStrings(payload.Config.Warnings), synced),
		},
	}
}

func profileKindForSnapshot(profile AgentProfile, selection *ProfileSelection) ProfileKind {
	if profile.Managed || profile.Source == AgentProfileSourceManaged {
		return ProfileKindHostManaged
	}
	if selection != nil {
		switch selection.Mode {
		case ProfileModeDedicated, ProfileModeClone:
			return ProfileKindHostManaged
		}
	}
	if profile.Source == AgentProfileSourceBindingEnv && strings.TrimSpace(profile.Dir) != "" {
		return ProfileKindHostManaged
	}
	return ProfileKindShared
}

func unsupportedResourceSnapshot(kind ProfileResourceKind, fingerprint string, desired []string, warnings []string, synced bool) ResourceSnapshot {
	out := ResourceSnapshot{Kind: kind, Fingerprint: fingerprint, Support: ProfileResourceSupportUnsupported, Materialization: ProfileResourceMaterializationNotMaterialized, Warnings: cloneStrings(warnings)}
	if len(desired) == 0 {
		if kind == ProfileResourceMCP {
			out.Support = ProfileResourceSupportPortableCore
		}
		return out
	}
	if kind == ProfileResourceMCP {
		out.Support = ProfileResourceSupportPortableCore
	}
	if !synced {
		out.Warnings = append(out.Warnings, fmt.Sprintf("%s resources are desired but not observed by ProfileSnapshot", kind))
		return out
	}
	message := fmt.Sprintf("%s resources are not materialized by this adapter yet", kind)
	out.Warnings = append(out.Warnings, message)
	out.Error = message
	return out
}
