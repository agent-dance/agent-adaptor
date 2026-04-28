package agentadaptor

import (
	"fmt"
	"sort"
	"strings"
)

// ProfileKind is the SDK's internal effective-profile classification.
// It separates where the provider profile lives from the public
// ProfileSelection option that requested it.
type ProfileKind string

const (
	// ProfileKindShared means the effective profile is the provider-native
	// shared profile such as ~/.claude, ~/.codex, or ~/.cursor.
	ProfileKindShared ProfileKind = "shared"
	// ProfileKindHostManaged means the effective profile is isolated or
	// managed by the host/adapter, such as a dedicated, cloned, or managed home.
	ProfileKindHostManaged ProfileKind = "host_managed"
)

// ProfileResourceKind names one provider-visible resource family managed as
// part of the effective profile desired state.
type ProfileResourceKind string

const (
	ProfileResourceSkills       ProfileResourceKind = "skills"
	ProfileResourceMCP          ProfileResourceKind = "mcp"
	ProfileResourceAgents       ProfileResourceKind = "agents"
	ProfileResourceHooks        ProfileResourceKind = "hooks"
	ProfileResourceInstructions ProfileResourceKind = "instructions"
	ProfileResourceConfig       ProfileResourceKind = "config"
)

// ProfileResources is the host-facing desired-state bundle accepted by
// WithDefaultProfileResources and WithProfileResources. Existing sugar options
// (WithDefaultSkills, WithMCP, WithInstructions, etc.) continue to work and are
// folded into the same ProfilePayload before invoking an adapter.
type ProfileResources struct {
	Skills       []SkillRef
	MCP          *MCPConfig
	Agents       []AgentSpec
	Hooks        []HookSpec
	Instructions *InstructionsBundleRef
	Config       []ProfileConfigPatch
}

// ProfilePayload is the adapter-facing normalized profile desired state for a
// single Run/Start invocation. Fingerprint covers every provider-visible
// resource kind so adapters can use it as the session resume guard.
type ProfilePayload struct {
	Skills       ResolvedSkills
	MCP          MCPPayload
	Agents       AgentPayload
	Hooks        HookPayload
	Instructions *InstructionsBundleRef
	Config       ProfileConfigPayload

	Fingerprint string
	Warnings    []string
}

// AgentSpec describes one host-declared sub-agent/profile agent entry.
type AgentSpec struct {
	Key         string
	RuntimeName string
	SourcePath  string
	Content     string
	Metadata    map[string]string
}

// AgentPayload is the normalized adapter-facing agent resource state.
type AgentPayload struct {
	Agents      []AgentSpec
	Fingerprint string
	Warnings    []string
}

// HookSpec describes one host-declared provider hook.
type HookSpec struct {
	Key      string
	Event    string
	Matcher  string
	Command  string
	Args     []string
	Env      map[string]string
	Disabled bool
	Metadata map[string]string
}

// HookPayload is the normalized adapter-facing hook resource state.
type HookPayload struct {
	Hooks       []HookSpec
	Fingerprint string
	Warnings    []string
}

// ProfileConfigFileKind identifies the structured profile config format a
// patch targets.
type ProfileConfigFileKind string

const (
	ProfileConfigFileJSON ProfileConfigFileKind = "json"
	ProfileConfigFileTOML ProfileConfigFileKind = "toml"
)

// ProfileConfigPatch is a structured config update. Hosts supply typed values;
// adapters/reconcilers own provider-native encoding.
type ProfileConfigPatch struct {
	Key      string
	FileKind ProfileConfigFileKind
	Path     string
	Section  string
	Values   map[string]any
}

// ProfileConfigPayload is the normalized adapter-facing config patch state.
type ProfileConfigPayload struct {
	Patches     []ProfileConfigPatch
	Fingerprint string
	Warnings    []string
}

// ResourceSnapshot reports the observed state for one profile resource kind.
type ResourceSnapshot struct {
	Kind        ProfileResourceKind
	Fingerprint string
	Managed     []string
	External    []string
	Warnings    []string
	Error       string
}

// ProfileSnapshot reports the control-plane view of an effective profile.
type ProfileSnapshot struct {
	DriverType  string
	Profile     AgentProfile
	Kind        ProfileKind
	Fingerprint string
	Resources   []ResourceSnapshot
	Warnings    []string
}

func cloneProfileResources(resources ProfileResources) ProfileResources {
	return ProfileResources{
		Skills:       cloneSkillRefs(resources.Skills),
		MCP:          cloneMCPConfig(resources.MCP),
		Agents:       cloneAgentSpecs(resources.Agents),
		Hooks:        cloneHookSpecs(resources.Hooks),
		Instructions: cloneInstructions(resources.Instructions),
		Config:       cloneProfileConfigPatches(resources.Config),
	}
}

func cloneProfilePayload(payload ProfilePayload) ProfilePayload {
	return ProfilePayload{
		Skills:       cloneResolvedSkills(payload.Skills),
		MCP:          cloneMCPPayload(payload.MCP),
		Agents:       cloneAgentPayload(payload.Agents),
		Hooks:        cloneHookPayload(payload.Hooks),
		Instructions: cloneInstructions(payload.Instructions),
		Config:       cloneProfileConfigPayload(payload.Config),
		Fingerprint:  payload.Fingerprint,
		Warnings:     cloneStrings(payload.Warnings),
	}
}

func cloneAgentPayload(payload AgentPayload) AgentPayload {
	return AgentPayload{Agents: cloneAgentSpecs(payload.Agents), Fingerprint: payload.Fingerprint, Warnings: cloneStrings(payload.Warnings)}
}

func cloneHookPayload(payload HookPayload) HookPayload {
	return HookPayload{Hooks: cloneHookSpecs(payload.Hooks), Fingerprint: payload.Fingerprint, Warnings: cloneStrings(payload.Warnings)}
}

func cloneProfileConfigPayload(payload ProfileConfigPayload) ProfileConfigPayload {
	return ProfileConfigPayload{Patches: cloneProfileConfigPatches(payload.Patches), Fingerprint: payload.Fingerprint, Warnings: cloneStrings(payload.Warnings)}
}

func cloneAgentSpecs(values []AgentSpec) []AgentSpec {
	if len(values) == 0 {
		return nil
	}
	out := make([]AgentSpec, 0, len(values))
	for _, value := range values {
		out = append(out, AgentSpec{Key: value.Key, RuntimeName: value.RuntimeName, SourcePath: value.SourcePath, Content: value.Content, Metadata: cloneStringMap(value.Metadata)})
	}
	return out
}

func cloneHookSpecs(values []HookSpec) []HookSpec {
	if len(values) == 0 {
		return nil
	}
	out := make([]HookSpec, 0, len(values))
	for _, value := range values {
		out = append(out, HookSpec{Key: value.Key, Event: value.Event, Matcher: value.Matcher, Command: value.Command, Args: cloneStrings(value.Args), Env: cloneStringMap(value.Env), Disabled: value.Disabled, Metadata: cloneStringMap(value.Metadata)})
	}
	return out
}

func cloneProfileConfigPatches(values []ProfileConfigPatch) []ProfileConfigPatch {
	if len(values) == 0 {
		return nil
	}
	out := make([]ProfileConfigPatch, 0, len(values))
	for _, value := range values {
		out = append(out, ProfileConfigPatch{Key: value.Key, FileKind: value.FileKind, Path: value.Path, Section: value.Section, Values: cloneAnyMap(value.Values)})
	}
	return out
}

func prepareAgentPayload(specs []AgentSpec) (AgentPayload, error) {
	normalized := cloneAgentSpecs(specs)
	sort.SliceStable(normalized, func(i, j int) bool { return normalized[i].Key < normalized[j].Key })
	seen := map[string]struct{}{}
	for i := range normalized {
		spec := &normalized[i]
		spec.Key = strings.TrimSpace(spec.Key)
		spec.RuntimeName = strings.TrimSpace(spec.RuntimeName)
		spec.SourcePath = strings.TrimSpace(spec.SourcePath)
		if spec.Key == "" {
			return AgentPayload{}, fmt.Errorf("agentadaptor: invalid profile resources: agent key is required")
		}
		if _, exists := seen[spec.Key]; exists {
			return AgentPayload{}, fmt.Errorf("agentadaptor: invalid profile resources: duplicate agent key %q", spec.Key)
		}
		seen[spec.Key] = struct{}{}
		if spec.RuntimeName == "" {
			spec.RuntimeName = spec.Key
		}
		if spec.SourcePath != "" && spec.Content != "" {
			return AgentPayload{}, fmt.Errorf("agentadaptor: invalid profile resources: agent %q cannot set both source path and content", spec.Key)
		}
	}
	return AgentPayload{Agents: normalized, Fingerprint: stableHash("profile_agents", normalized)}, nil
}

func prepareHookPayload(specs []HookSpec) (HookPayload, error) {
	normalized := cloneHookSpecs(specs)
	sort.SliceStable(normalized, func(i, j int) bool { return normalized[i].Key < normalized[j].Key })
	seen := map[string]struct{}{}
	for i := range normalized {
		spec := &normalized[i]
		spec.Key = strings.TrimSpace(spec.Key)
		spec.Event = strings.TrimSpace(spec.Event)
		spec.Matcher = strings.TrimSpace(spec.Matcher)
		spec.Command = strings.TrimSpace(spec.Command)
		if spec.Key == "" {
			return HookPayload{}, fmt.Errorf("agentadaptor: invalid profile resources: hook key is required")
		}
		if _, exists := seen[spec.Key]; exists {
			return HookPayload{}, fmt.Errorf("agentadaptor: invalid profile resources: duplicate hook key %q", spec.Key)
		}
		seen[spec.Key] = struct{}{}
		if spec.Event == "" {
			return HookPayload{}, fmt.Errorf("agentadaptor: invalid profile resources: hook %q requires event", spec.Key)
		}
		if spec.Command == "" && !spec.Disabled {
			return HookPayload{}, fmt.Errorf("agentadaptor: invalid profile resources: hook %q requires command unless disabled", spec.Key)
		}
	}
	return HookPayload{Hooks: normalized, Fingerprint: stableHash("profile_hooks", normalized)}, nil
}

func prepareProfileConfigPayload(patches []ProfileConfigPatch) (ProfileConfigPayload, error) {
	normalized := cloneProfileConfigPatches(patches)
	sort.SliceStable(normalized, func(i, j int) bool { return normalized[i].Key < normalized[j].Key })
	seen := map[string]struct{}{}
	for i := range normalized {
		patch := &normalized[i]
		patch.Key = strings.TrimSpace(patch.Key)
		patch.Path = strings.TrimSpace(patch.Path)
		patch.Section = strings.TrimSpace(patch.Section)
		if patch.Key == "" {
			return ProfileConfigPayload{}, fmt.Errorf("agentadaptor: invalid profile resources: config patch key is required")
		}
		if _, exists := seen[patch.Key]; exists {
			return ProfileConfigPayload{}, fmt.Errorf("agentadaptor: invalid profile resources: duplicate config patch key %q", patch.Key)
		}
		seen[patch.Key] = struct{}{}
		switch patch.FileKind {
		case ProfileConfigFileJSON, ProfileConfigFileTOML:
		case "":
			return ProfileConfigPayload{}, fmt.Errorf("agentadaptor: invalid profile resources: config patch %q requires file kind", patch.Key)
		default:
			return ProfileConfigPayload{}, fmt.Errorf("agentadaptor: invalid profile resources: config patch %q has unsupported file kind %q", patch.Key, patch.FileKind)
		}
		if patch.Path == "" {
			return ProfileConfigPayload{}, fmt.Errorf("agentadaptor: invalid profile resources: config patch %q requires path", patch.Key)
		}
	}
	return ProfileConfigPayload{Patches: normalized, Fingerprint: stableHash("profile_config", normalized)}, nil
}

func buildProfilePayload(skills ResolvedSkills, mcp MCPPayload, agents AgentPayload, hooks HookPayload, instructions *InstructionsBundleRef, config ProfileConfigPayload) ProfilePayload {
	warnings := append(cloneStrings(skills.Warnings), mcp.Warnings...)
	warnings = append(warnings, agents.Warnings...)
	warnings = append(warnings, hooks.Warnings...)
	warnings = append(warnings, config.Warnings...)
	instructionFP := instructionFingerprint(instructions)
	fingerprint := stableHash(
		"profile_payload",
		skills.Fingerprint,
		mcp.Fingerprint,
		agents.Fingerprint,
		hooks.Fingerprint,
		instructionFP,
		config.Fingerprint,
	)
	return ProfilePayload{Skills: cloneResolvedSkills(skills), MCP: cloneMCPPayload(mcp), Agents: cloneAgentPayload(agents), Hooks: cloneHookPayload(hooks), Instructions: cloneInstructions(instructions), Config: cloneProfileConfigPayload(config), Fingerprint: fingerprint, Warnings: warnings}
}

func mcpKeys(payload MCPPayload) []string {
	out := make([]string, 0, len(payload.Servers))
	for _, server := range payload.Servers {
		out = append(out, server.Key)
	}
	sort.Strings(out)
	return out
}

func agentKeys(payload AgentPayload) []string {
	out := make([]string, 0, len(payload.Agents))
	for _, agent := range payload.Agents {
		out = append(out, agent.Key)
	}
	sort.Strings(out)
	return out
}

func hookKeys(payload HookPayload) []string {
	out := make([]string, 0, len(payload.Hooks))
	for _, hook := range payload.Hooks {
		out = append(out, hook.Key)
	}
	sort.Strings(out)
	return out
}

func instructionKeys(ref *InstructionsBundleRef) []string {
	if ref == nil {
		return nil
	}
	if strings.TrimSpace(ref.ID) != "" {
		return []string{strings.TrimSpace(ref.ID)}
	}
	if strings.TrimSpace(ref.Path) != "" {
		return []string{strings.TrimSpace(ref.Path)}
	}
	if strings.TrimSpace(ref.Fingerprint) != "" {
		return []string{strings.TrimSpace(ref.Fingerprint)}
	}
	return []string{"instructions"}
}

func configPatchKeys(payload ProfileConfigPayload) []string {
	out := make([]string, 0, len(payload.Patches))
	for _, patch := range payload.Patches {
		out = append(out, patch.Key)
	}
	sort.Strings(out)
	return out
}
