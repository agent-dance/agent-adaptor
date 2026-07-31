package engine

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
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
// WithProfileResources at agent construction or per-call scope. Existing sugar
// options (WithSkills, WithMCP, WithInstructions, etc.) continue to work and
// are folded into the same ProfilePayload before invoking a driver.
type ProfileResources struct {
	Skills       []SkillRef
	MCP          *MCPConfig
	Agents       []AgentSpec
	Hooks        []HookSpec
	Instructions *InstructionsBundleRef
	Config       []ProfileConfigPatch
}

// ResourceSnapshot reports the observed state for one profile resource kind.
type ResourceSnapshot struct {
	Kind            ProfileResourceKind
	Fingerprint     string
	Managed         []string
	External        []string
	Support         ProfileResourceSupport
	Materialization ProfileResourceMaterialization
	Warnings        []string
	Error           string
}

type ProfileResourceSupport string

const (
	ProfileResourceSupportPortableCore     ProfileResourceSupport = "portable_core"
	ProfileResourceSupportPortableExtended ProfileResourceSupport = "portable_extended"
	ProfileResourceSupportNativeEscape     ProfileResourceSupport = "native_escape"
	ProfileResourceSupportFallback         ProfileResourceSupport = "fallback"
	ProfileResourceSupportUnsupported      ProfileResourceSupport = "unsupported"
)

type ProfileResourceMaterialization string

const (
	ProfileResourceMaterializationNativeManaged   ProfileResourceMaterialization = "native_managed"
	ProfileResourceMaterializationFileManaged     ProfileResourceMaterialization = "file_managed"
	ProfileResourceMaterializationPromptInjected  ProfileResourceMaterialization = "prompt_injected"
	ProfileResourceMaterializationFallback        ProfileResourceMaterialization = "fallback"
	ProfileResourceMaterializationNotMaterialized ProfileResourceMaterialization = "not_materialized"
)

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
		Skills:                          cloneResolvedSkills(payload.Skills),
		MCP:                             cloneMCPPayload(payload.MCP),
		Agents:                          cloneAgentPayload(payload.Agents),
		Hooks:                           cloneHookPayload(payload.Hooks),
		Instructions:                    cloneInstructions(payload.Instructions),
		Config:                          cloneProfileConfigPayload(payload.Config),
		Declared:                        payload.Declared,
		Fingerprint:                     payload.Fingerprint,
		SessionCompatibilityFingerprint: payload.SessionCompatibilityFingerprint,
		Warnings:                        cloneStrings(payload.Warnings),
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
		out = append(out, AgentSpec{
			Key:               value.Key,
			RuntimeName:       value.RuntimeName,
			Description:       value.Description,
			Instructions:      value.Instructions,
			SourcePath:        value.SourcePath,
			SourceFingerprint: value.SourceFingerprint,
			Model:             value.Model,
			ReasoningEffort:   value.ReasoningEffort,
			ToolPolicy:        cloneAgentToolPolicy(value.ToolPolicy),
			PermissionMode:    value.PermissionMode,
			SandboxMode:       value.SandboxMode,
			MCPServers:        cloneStrings(value.MCPServers),
			Skills:            cloneStrings(value.Skills),
			Hooks:             cloneHookSpecs(value.Hooks),
			Native:            cloneAnyMap(value.Native),
			Metadata:          cloneStringMap(value.Metadata),
		})
	}
	return out
}

func cloneAgentToolPolicy(policy *AgentToolPolicy) *AgentToolPolicy {
	if policy == nil {
		return nil
	}
	return &AgentToolPolicy{Allow: cloneStrings(policy.Allow), Deny: cloneStrings(policy.Deny)}
}

func cloneHookSpecs(values []HookSpec) []HookSpec {
	if len(values) == 0 {
		return nil
	}
	out := make([]HookSpec, 0, len(values))
	for _, value := range values {
		out = append(out, HookSpec{
			Key:           value.Key,
			Event:         value.Event,
			MatcherSpec:   value.MatcherSpec,
			Handler:       cloneHookHandler(value.Handler),
			Timeout:       value.Timeout,
			FailPolicy:    value.FailPolicy,
			StatusMessage: value.StatusMessage,
			Disabled:      value.Disabled,
			Native:        cloneAnyMap(value.Native),
			Metadata:      cloneStringMap(value.Metadata),
		})
	}
	return out
}

func cloneHookHandler(handler HookHandler) HookHandler {
	handler.Args = cloneStrings(handler.Args)
	handler.Env = cloneStringMap(handler.Env)
	handler.Input = cloneAnyMap(handler.Input)
	return handler
}

func cloneProfileConfigPatches(values []ProfileConfigPatch) []ProfileConfigPatch {
	if len(values) == 0 {
		return nil
	}
	out := make([]ProfileConfigPatch, 0, len(values))
	for _, value := range values {
		out = append(out, ProfileConfigPatch{
			Key:        value.Key,
			Capability: value.Capability,
			Values:     cloneAnyMap(value.Values),
			Native:     cloneNativeConfigPatch(value.Native),
		})
	}
	return out
}

func cloneNativeConfigPatch(patch *NativeConfigPatch) *NativeConfigPatch {
	if patch == nil {
		return nil
	}
	return &NativeConfigPatch{
		Provider: patch.Provider,
		FileKind: patch.FileKind,
		Path:     patch.Path,
		Section:  patch.Section,
		Values:   cloneAnyMap(patch.Values),
	}
}

func prepareAgentPayload(specs []AgentSpec) (AgentPayload, error) {
	normalized := cloneAgentSpecs(specs)
	sort.SliceStable(normalized, func(i, j int) bool { return normalized[i].Key < normalized[j].Key })
	seen := map[string]struct{}{}
	for i := range normalized {
		spec := &normalized[i]
		spec.Key = strings.TrimSpace(spec.Key)
		spec.RuntimeName = strings.TrimSpace(spec.RuntimeName)
		spec.Description = strings.TrimSpace(spec.Description)
		spec.Instructions = strings.TrimSpace(spec.Instructions)
		spec.SourcePath = strings.TrimSpace(spec.SourcePath)
		spec.SourceFingerprint = strings.TrimSpace(spec.SourceFingerprint)
		spec.Model = strings.TrimSpace(spec.Model)
		spec.ReasoningEffort = strings.TrimSpace(spec.ReasoningEffort)
		spec.PermissionMode = strings.TrimSpace(spec.PermissionMode)
		spec.SandboxMode = strings.TrimSpace(spec.SandboxMode)
		spec.MCPServers = normalizeStringSlice(spec.MCPServers)
		spec.Skills = normalizeStringSlice(spec.Skills)
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
		if spec.SourcePath != "" && spec.Instructions != "" {
			return AgentPayload{}, fmt.Errorf("agentadaptor: invalid profile resources: agent %q cannot set both source path and instructions", spec.Key)
		}
		if spec.SourcePath != "" && spec.SourceFingerprint == "" {
			fingerprint, err := sourcePathFingerprint(spec.SourcePath)
			if err != nil {
				return AgentPayload{}, fmt.Errorf("agentadaptor: invalid profile resources: agent %q source path: %w", spec.Key, err)
			}
			spec.SourceFingerprint = fingerprint
		}
		hooks, err := prepareHookPayload(spec.Hooks)
		if err != nil {
			return AgentPayload{}, fmt.Errorf("agentadaptor: invalid profile resources: agent %q: %w", spec.Key, err)
		}
		spec.Hooks = hooks.Hooks
	}
	return AgentPayload{Agents: normalized, Fingerprint: stableHash("profile_agents", normalized)}, nil
}

type sourceFingerprintEntry struct {
	Path        string
	Fingerprint string
}

func sourcePathFingerprint(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		raw, err := os.ReadFile(path)
		if err != nil {
			return "", err
		}
		return stableHash("source_file", path, string(raw)), nil
	}

	entries := make([]sourceFingerprintEntry, 0)
	if err := filepath.WalkDir(path, func(current string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeType != 0 {
			return nil
		}
		raw, err := os.ReadFile(current)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(path, current)
		if err != nil {
			return err
		}
		entries = append(entries, sourceFingerprintEntry{Path: rel, Fingerprint: stableHash(string(raw))})
		return nil
	}); err != nil {
		return "", err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	return stableHash("source_dir", path, entries), nil
}

func prepareHookPayload(specs []HookSpec) (HookPayload, error) {
	normalized := cloneHookSpecs(specs)
	sort.SliceStable(normalized, func(i, j int) bool { return normalized[i].Key < normalized[j].Key })
	seen := map[string]struct{}{}
	for i := range normalized {
		spec := &normalized[i]
		spec.Key = strings.TrimSpace(spec.Key)
		spec.Event = normalizeHookEvent(spec.Event)
		spec.MatcherSpec.Pattern = strings.TrimSpace(spec.MatcherSpec.Pattern)
		spec.Handler = normalizeHookHandler(spec.Handler)
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
		if !spec.Disabled && !hasHookHandler(spec.Handler) {
			return HookPayload{}, fmt.Errorf("agentadaptor: invalid profile resources: hook %q requires handler unless disabled", spec.Key)
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
		patch.Capability = strings.TrimSpace(patch.Capability)
		if patch.Native != nil {
			patch.Native.Provider = strings.TrimSpace(patch.Native.Provider)
			patch.Native.Path = strings.TrimSpace(patch.Native.Path)
			patch.Native.Section = strings.TrimSpace(patch.Native.Section)
		}
		if patch.Key == "" {
			return ProfileConfigPayload{}, fmt.Errorf("agentadaptor: invalid profile resources: config patch key is required")
		}
		if _, exists := seen[patch.Key]; exists {
			return ProfileConfigPayload{}, fmt.Errorf("agentadaptor: invalid profile resources: duplicate config patch key %q", patch.Key)
		}
		seen[patch.Key] = struct{}{}
		if patch.Capability != "" {
			if patch.Native != nil {
				return ProfileConfigPayload{}, fmt.Errorf("agentadaptor: invalid profile resources: config patch %q cannot set both capability and native target", patch.Key)
			}
			continue
		}
		if patch.Native == nil {
			return ProfileConfigPayload{}, fmt.Errorf("agentadaptor: invalid profile resources: config patch %q requires capability or native target", patch.Key)
		}
		nativePatch := cloneNativeConfigPatch(patch.Native)
		if len(nativePatch.Values) == 0 {
			nativePatch.Values = cloneAnyMap(patch.Values)
		}
		switch nativePatch.FileKind {
		case ProfileConfigFileJSON, ProfileConfigFileTOML:
		case "":
			return ProfileConfigPayload{}, fmt.Errorf("agentadaptor: invalid profile resources: config patch %q requires file kind", patch.Key)
		default:
			return ProfileConfigPayload{}, fmt.Errorf("agentadaptor: invalid profile resources: config patch %q has unsupported file kind %q", patch.Key, nativePatch.FileKind)
		}
		if nativePatch.Path == "" {
			return ProfileConfigPayload{}, fmt.Errorf("agentadaptor: invalid profile resources: config patch %q requires path", patch.Key)
		}
		patch.Native = nativePatch
	}
	return ProfileConfigPayload{Patches: normalized, Fingerprint: stableHash("profile_config", normalized)}, nil
}

func prepareInstructionsBundle(ref *InstructionsBundleRef) (*InstructionsBundleRef, error) {
	if ref == nil {
		return nil, nil
	}
	normalized := cloneInstructions(ref)
	normalized.ID = strings.TrimSpace(normalized.ID)
	normalized.Path = strings.TrimSpace(normalized.Path)
	normalized.Content = strings.TrimSpace(normalized.Content)
	normalized.Fingerprint = strings.TrimSpace(normalized.Fingerprint)
	normalized.Scope = InstructionScope(strings.TrimSpace(string(normalized.Scope)))
	normalized.Mode = InstructionMode(strings.TrimSpace(string(normalized.Mode)))
	if normalized.Path != "" && normalized.Content != "" {
		return nil, fmt.Errorf("agentadaptor: invalid profile resources: instructions cannot set both path and content")
	}
	if normalized.Path != "" && normalized.Fingerprint == "" {
		info, err := os.Stat(normalized.Path)
		if err != nil {
			return nil, err
		}
		if info.IsDir() {
			return nil, fmt.Errorf("agentadaptor: invalid profile resources: instructions path %s is a directory", normalized.Path)
		}
		raw, err := os.ReadFile(normalized.Path)
		if err != nil {
			return nil, err
		}
		normalized.Fingerprint = stableHash("instructions", normalized.ID, normalized.Path, string(raw), normalized.Scope, normalized.Mode, normalized.Native)
	}
	return normalized, nil
}

func normalizeStringSlice(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			out = append(out, value)
		}
	}
	sort.Strings(out)
	return out
}

func normalizeHookEvent(event HookEvent) HookEvent {
	switch strings.TrimSpace(string(event)) {
	case "PreToolUse":
		return HookEventPreTool
	case "PostToolUse":
		return HookEventPostTool
	case "Notification", "UserPromptSubmit":
		return HookEventPromptSubmit
	case "SessionStart":
		return HookEventSessionStart
	case "SessionEnd":
		return HookEventSessionEnd
	case "Stop":
		return HookEventStop
	case "SubagentStop":
		return HookEventSubagentStop
	case "PreCompact":
		return HookEventPreCompact
	default:
		return HookEvent(strings.TrimSpace(string(event)))
	}
}

func normalizeHookHandler(handler HookHandler) HookHandler {
	handler.Type = HookHandlerType(strings.TrimSpace(string(handler.Type)))
	handler.Command = strings.TrimSpace(handler.Command)
	handler.Prompt = strings.TrimSpace(handler.Prompt)
	handler.URL = strings.TrimSpace(handler.URL)
	handler.Server = strings.TrimSpace(handler.Server)
	handler.Tool = strings.TrimSpace(handler.Tool)
	handler.Agent = strings.TrimSpace(handler.Agent)
	handler.Args = normalizeStringSlicePreserveOrder(handler.Args)
	handler.Env = cloneStringMap(handler.Env)
	handler.Input = cloneAnyMap(handler.Input)
	if handler.Type == "" && handler.Command != "" {
		handler.Type = HookHandlerCommand
	}
	return handler
}

func normalizeStringSlicePreserveOrder(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			out = append(out, value)
		}
	}
	return out
}

func hasHookHandler(handler HookHandler) bool {
	switch handler.Type {
	case HookHandlerCommand:
		return handler.Command != ""
	case HookHandlerPrompt:
		return handler.Prompt != ""
	case HookHandlerHTTP:
		return handler.URL != ""
	case HookHandlerMCPTool:
		return handler.Server != "" && handler.Tool != ""
	case HookHandlerAgent:
		return handler.Agent != ""
	default:
		return false
	}
}

func buildProfilePayload(skills ResolvedSkills, mcp MCPPayload, agents AgentPayload, hooks HookPayload, instructions *InstructionsBundleRef, config ProfileConfigPayload, declared ProfileResourceDeclarations) ProfilePayload {
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
		declared,
	)
	return ProfilePayload{
		Skills:                          cloneResolvedSkills(skills),
		MCP:                             cloneMCPPayload(mcp),
		Agents:                          cloneAgentPayload(agents),
		Hooks:                           cloneHookPayload(hooks),
		Instructions:                    cloneInstructions(instructions),
		Config:                          cloneProfileConfigPayload(config),
		Declared:                        declared,
		Fingerprint:                     fingerprint,
		SessionCompatibilityFingerprint: fingerprint,
		Warnings:                        warnings,
	}
}

// instructionFingerprint hashes the effective instruction bundle. It moved
// here from the root runner.go together with buildProfilePayload, which is
// its primary caller.
func instructionFingerprint(ref *InstructionsBundleRef) string {
	if ref == nil {
		return ""
	}
	if ref.Fingerprint != "" {
		return ref.Fingerprint
	}
	content := ref.Content
	if ref.Path != "" && content == "" {
		if raw, err := os.ReadFile(ref.Path); err == nil {
			content = string(raw)
		}
	}
	return stableHash("instructions", ref.ID, ref.Path, content, ref.Scope, ref.Mode, ref.Native)
}

// cloneInstructions and cloneBool support the profile-payload copy helpers.
func cloneInstructions(ref *InstructionsBundleRef) *InstructionsBundleRef {
	if ref == nil {
		return nil
	}
	copyRef := *ref
	copyRef.Native = cloneAnyMap(ref.Native)
	return &copyRef
}

func cloneBool(b *bool) *bool {
	if b == nil {
		return nil
	}
	v := *b
	return &v
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
	if strings.TrimSpace(ref.Content) != "" {
		return []string{"inline-instructions"}
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
