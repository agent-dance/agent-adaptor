package opencode

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/driverutil"
	"github.com/agent-dance/agent-adaptor/internal/profileinstructions"
	"github.com/agent-dance/agent-adaptor/internal/profilejson"
	"github.com/agent-dance/agent-adaptor/internal/profilekind"
	"github.com/agent-dance/agent-adaptor/internal/profilesnapshot"
	"github.com/agent-dance/agent-adaptor/internal/skillruntime"
)

var _ driver.ProfileResourceDriver = configuredDriver{}

func profileKind(c Config, profile driver.AgentProfile) driver.ProfileKind {
	return profilekind.Classify(profile, nativeProfileDir(c))
}

func (d configuredDriver) SnapshotProfileResources(ctx context.Context, value any, _ driver.AgentIdentity, selection *driver.ProfileSelection, payload driver.ProfilePayload, selected []string, resolved []driver.Skill) (driver.ProfileSnapshot, error) {
	return d.snapshotProfileResources(ctx, value, selection, payload, selected, resolved)
}

func (d configuredDriver) snapshotProfileResources(ctx context.Context, value any, selection *driver.ProfileSelection, payload driver.ProfilePayload, selected []string, resolved []driver.Skill, prepared ...preparedAgentProfile) (driver.ProfileSnapshot, error) {
	c, err := d.config(value)
	if err != nil {
		return driver.ProfileSnapshot{}, err
	}
	p, err := resolveProfile(ctx, c, selection, false)
	if err != nil {
		return driver.ProfileSnapshot{}, err
	}
	skills, err := d.ListSkills(ctx, value, payload.Skills, selected, resolved, selection)
	if err != nil {
		return driver.ProfileSnapshot{}, err
	}
	snapshot := profilesnapshot.Build(DriverType, p.Profile, profileKind(c, p.Profile), payload, skills, false)
	mcpState, err := observeProfileMCP(p.Profile.Dir, payload.MCP)
	if err != nil {
		return driver.ProfileSnapshot{}, err
	}
	agents, err := observeProfileAgents(p.Profile.Dir, payload.Agents, prepared...)
	if err != nil {
		return driver.ProfileSnapshot{}, err
	}
	for i := range snapshot.Resources {
		entry := &snapshot.Resources[i]
		if entry.Kind == driver.ProfileResourceMCP {
			*entry = mcpState.Snapshot
		}
		if entry.Kind == driver.ProfileResourceAgents {
			*entry = agents.Snapshot
		}
	}
	plan, planErr := nativeInstructionPlan(payload.Instructions)
	if planErr != nil && !errors.Is(planErr, errors.ErrUnsupported) {
		return driver.ProfileSnapshot{}, planErr
	}
	instructions, err := profileinstructions.ObserveNativeFiles(DriverType, p.Profile.Dir, nativeInstructionNames(), plan)
	if err != nil {
		return driver.ProfileSnapshot{}, err
	}
	if planErr != nil {
		instructions.Support = driver.ProfileResourceSupportUnsupported
		instructions.Warnings = append(instructions.Warnings, planErr.Error())
	}
	for i := range snapshot.Resources {
		if snapshot.Resources[i].Kind == driver.ProfileResourceInstructions {
			instructions.Fingerprint = snapshot.Resources[i].Fingerprint
			snapshot.Resources[i] = instructions
		}
	}
	return snapshot, nil
}

func (d configuredDriver) SyncProfileResources(ctx context.Context, value any, identity driver.AgentIdentity, selection *driver.ProfileSelection, payload driver.ProfilePayload, selected []string, resolved []driver.Skill) (driver.ProfileSnapshot, error) {
	// Validate the requested shape before cloning or modifying native files.
	// Run also supports invocation-local resources with a separate lifetime.
	if payload.Declared.Hooks || payload.Declared.Config {
		return driver.ProfileSnapshot{}, fmt.Errorf("%s: durable profile synchronization does not support hooks or config patches", DriverType)
	}
	if err := ctx.Err(); err != nil {
		return driver.ProfileSnapshot{}, err
	}
	if _, _, err := mcpProfilePlan("validate", payload.MCP); err != nil {
		return driver.ProfileSnapshot{}, err
	}
	agentLayout, agentEntries, err := agentProfilePlan("validate", payload.Agents)
	if err != nil {
		return driver.ProfileSnapshot{}, err
	}
	plan, err := nativeInstructionPlan(payload.Instructions)
	if err != nil {
		return driver.ProfileSnapshot{}, err
	}
	c, err := d.config(value)
	if err != nil {
		return driver.ProfileSnapshot{}, err
	}
	p, err := resolveProfile(ctx, c, selection, true)
	if err != nil {
		return driver.ProfileSnapshot{}, err
	}
	if payload.Declared.Instructions {
		if _, err := profileinstructions.SyncNativeFiles(ctx, DriverType, p.Profile.Dir, nativeInstructionNames(), plan); err != nil {
			return driver.ProfileSnapshot{}, err
		}
	}
	if payload.Declared.Agents || len(payload.Agents.Agents) > 0 {
		layout, entries := agentLayout, agentEntries
		layout.Root = p.Profile.Dir
		external, err := externalProfileAgents(p.Profile.Dir)
		if err != nil {
			return driver.ProfileSnapshot{}, err
		}
		state, err := profilejson.Observe(layout, nil, "")
		if err != nil {
			return driver.ProfileSnapshot{}, err
		}
		for key := range external {
			if entries[key] != nil || state.Managed[key] {
				return driver.ProfileSnapshot{}, fmt.Errorf("opencode: profile agent %q conflicts with external native configuration", key)
			}
		}
		if _, err := profilejson.Sync(ctx, layout, entries, payload.Agents.Fingerprint); err != nil {
			return driver.ProfileSnapshot{}, err
		}
	}
	if payload.Declared.MCP || len(payload.MCP.Servers) > 0 {
		layout, entries, err := mcpProfilePlan(p.Profile.Dir, payload.MCP)
		if err != nil {
			return driver.ProfileSnapshot{}, err
		}
		base, err := profilejson.ReadEntries(p.Profile.Dir, "opencode.json", "mcp")
		if err != nil {
			return driver.ProfileSnapshot{}, err
		}
		for key := range entries {
			if _, exists := base[key]; exists {
				return driver.ProfileSnapshot{}, fmt.Errorf("opencode: MCP entry %q already exists in external opencode.json", key)
			}
		}
		if _, err := profilejson.Sync(ctx, layout, entries, payload.MCP.Fingerprint); err != nil {
			return driver.ProfileSnapshot{}, err
		}
	}
	if _, err := skillruntime.SyncProfileSkills(ctx, DriverType, p.Profile.Dir, profileKind(c, p.Profile), payload.Skills, selected, resolved); err != nil {
		return driver.ProfileSnapshot{}, err
	}
	return d.snapshotProfileResources(ctx, value, selection, payload, selected, resolved, preparedAgentProfile{agentLayout, agentEntries})
}

func nativeProfileDir(c Config) string {
	configHome, _ := driverutil.ResolvedEnvValue(c.Env, "XDG_CONFIG_HOME")
	if configHome == "" {
		configHome = filepath.Join(skillruntime.ResolveHome(c.Env), ".config")
	}
	return filepath.Join(configHome, "opencode")
}

func nativeInstructionNames() []string { return []string{"AGENTS.md"} }
func nativeInstructionPlan(ref *driver.InstructionsBundleRef) (*profileinstructions.NativeFile, error) {
	if ref == nil {
		return nil, nil
	}
	if ref.Mode == driver.InstructionModeReplace {
		return nil, fmt.Errorf("opencode: replacement instructions are invocation-local; durable sync requires additive mode: %w", errors.ErrUnsupported)
	}
	if ref.Scope != driver.InstructionScopeDefault && ref.Scope != driver.InstructionScopeUser {
		return nil, fmt.Errorf("opencode: durable instruction sync requires user/default scope; use Run or Stream for %q scope: %w", ref.Scope, errors.ErrUnsupported)
	}
	prepared, err := prepareInstructions(ref)
	if err != nil {
		return nil, err
	}
	return profileinstructions.PlanNativeFile(prepared, "AGENTS.md")
}
