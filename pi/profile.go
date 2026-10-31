package pi

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/profileinstructions"
	"github.com/agent-dance/agent-adaptor/internal/profilejson"
	"github.com/agent-dance/agent-adaptor/internal/profilekind"
	"github.com/agent-dance/agent-adaptor/internal/profilesnapshot"
	"github.com/agent-dance/agent-adaptor/internal/skillruntime"
)

var _ driver.ProfileResourceDriver = configuredDriver{}

func profileKind(c Config, profile driver.AgentProfile) driver.ProfileKind {
	return profilekind.Classify(profile, filepath.Join(skillruntime.ResolveHome(c.Env), ".pi", "agent"))
}

func (d configuredDriver) SnapshotProfileResources(ctx context.Context, value any, _ driver.AgentIdentity, selection *driver.ProfileSelection, payload driver.ProfilePayload, selected []string, resolved []driver.Skill) (driver.ProfileSnapshot, error) {
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
	agentEntries, err := agentProfileEntries(payload.Agents)
	if err != nil {
		return driver.ProfileSnapshot{}, err
	}
	agentState, err := profilejson.Observe(agentProfileLayout(p.Profile.Dir), agentEntries, payload.Agents.Fingerprint)
	if err != nil {
		return driver.ProfileSnapshot{}, err
	}
	mcpEntries, err := mcpProfileEntries(payload.MCP)
	if err != nil {
		return driver.ProfileSnapshot{}, err
	}
	mcpState, err := profilejson.Observe(mcpProfileLayout(p.Profile.Dir), mcpEntries, payload.MCP.Fingerprint)
	if err != nil {
		return driver.ProfileSnapshot{}, err
	}
	for i := range snapshot.Resources {
		entry := &snapshot.Resources[i]
		if entry.Kind == driver.ProfileResourceMCP {
			*entry = mcpState.Snapshot
		}
		if entry.Kind == driver.ProfileResourceAgents {
			*entry = agentState.Snapshot
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
	var err error
	payload.Agents, err = prepareAgentPayload(payload.Agents)
	if err != nil {
		return driver.ProfileSnapshot{}, err
	}
	agentEntries, err := agentProfileEntries(payload.Agents)
	if err != nil {
		return driver.ProfileSnapshot{}, err
	}
	if err := ctx.Err(); err != nil {
		return driver.ProfileSnapshot{}, err
	}
	mcpEntries, err := mcpProfileEntries(payload.MCP)
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
	if payload.Declared.Agents {
		if _, err := profilejson.Sync(ctx, agentProfileLayout(p.Profile.Dir), agentEntries, payload.Agents.Fingerprint); err != nil {
			return driver.ProfileSnapshot{}, err
		}
	}
	if payload.Declared.Instructions {
		if _, err := profileinstructions.SyncNativeFiles(ctx, DriverType, p.Profile.Dir, nativeInstructionNames(), plan); err != nil {
			return driver.ProfileSnapshot{}, err
		}
	}
	if payload.Declared.MCP || len(payload.MCP.Servers) > 0 {
		if _, err := profilejson.Sync(ctx, mcpProfileLayout(p.Profile.Dir), mcpEntries, payload.MCP.Fingerprint); err != nil {
			return driver.ProfileSnapshot{}, err
		}
	}
	if _, err := skillruntime.SyncProfileSkills(ctx, DriverType, p.Profile.Dir, profileKind(c, p.Profile), payload.Skills, selected, resolved); err != nil {
		return driver.ProfileSnapshot{}, err
	}
	return d.SnapshotProfileResources(ctx, value, identity, selection, payload, selected, resolved)
}

func nativeInstructionNames() []string { return []string{"APPEND_SYSTEM.md", "SYSTEM.md"} }
func nativeInstructionPlan(ref *driver.InstructionsBundleRef) (*profileinstructions.NativeFile, error) {
	if ref == nil {
		return nil, nil
	}
	if ref.Scope != driver.InstructionScopeDefault && ref.Scope != driver.InstructionScopeUser {
		return nil, fmt.Errorf("pi: durable instruction sync requires user/default scope; use Run or Stream for %q scope: %w", ref.Scope, errors.ErrUnsupported)
	}
	prepared, err := prepareInstructions(ref)
	if err != nil {
		return nil, err
	}
	name := "APPEND_SYSTEM.md"
	if ref.Mode == driver.InstructionModeReplace {
		name = "SYSTEM.md"
	}
	return profileinstructions.PlanNativeFile(prepared, name)
}
