package agentadaptor

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
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

	Declared    ProfileResourceDeclarations
	Fingerprint string
	Warnings    []string
}

// ProfileResourceDeclarations records which optional profile resource kinds
// were explicitly declared by the host. Empty declared resources mean "clear
// managed entries"; undeclared resources must not be reconciled as empty.
type ProfileResourceDeclarations struct {
	Agents       bool
	Hooks        bool
	Instructions bool
	Config       bool
}

// AgentSpec describes one host-declared sub-agent/profile agent entry.
type AgentSpec struct {
	Key          string
	RuntimeName  string
	Description  string
	Instructions string
	// Content is a backward-compatible alias for Instructions. New callers
	// should set Instructions so Content can remain an unambiguous migration
	// bridge rather than a provider-native blob.
	Content           string
	SourcePath        string
	SourceFingerprint string

	Model           string
	ReasoningEffort string
	ToolPolicy      *AgentToolPolicy
	PermissionMode  string
	SandboxMode     string
	MCPServers      []string
	Skills          []string
	Hooks           []HookSpec

	Native   map[string]any
	Metadata map[string]string
}

// AgentToolPolicy captures provider-neutral tool allow/deny intent for a
// profile agent. Adapters map it to provider-native tool/sandbox/permission
// fields when they declare support.
type AgentToolPolicy struct {
	Allow []string
	Deny  []string
}

// AgentPayload is the normalized adapter-facing agent resource state.
type AgentPayload struct {
	Agents      []AgentSpec
	Fingerprint string
	Warnings    []string
}

// HookSpec describes one host-declared provider hook.
type HookSpec struct {
	Key         string
	Event       HookEvent
	MatcherSpec HookMatcher
	Handler     HookHandler

	// Matcher, Command, Args, and Env are backward-compatible command hook
	// fields. New callers should prefer MatcherSpec and Handler.
	Matcher string
	Command string
	Args    []string
	Env     map[string]string

	Timeout       time.Duration
	FailPolicy    HookFailPolicy
	StatusMessage string
	Disabled      bool

	Native   map[string]any
	Metadata map[string]string
}

// HookEvent is the SDK-level lifecycle event intent. Adapters translate these
// values into provider-native event names.
type HookEvent string

const (
	HookEventSessionStart      HookEvent = "session_start"
	HookEventSessionEnd        HookEvent = "session_end"
	HookEventPromptSubmit      HookEvent = "prompt_submit"
	HookEventPromptExpand      HookEvent = "prompt_expand"
	HookEventPreTool           HookEvent = "pre_tool"
	HookEventPostTool          HookEvent = "post_tool"
	HookEventToolFailure       HookEvent = "tool_failure"
	HookEventPermissionRequest HookEvent = "permission_request"
	HookEventPreShell          HookEvent = "pre_shell"
	HookEventPostShell         HookEvent = "post_shell"
	HookEventPreMCP            HookEvent = "pre_mcp"
	HookEventPostMCP           HookEvent = "post_mcp"
	HookEventPreFileRead       HookEvent = "pre_file_read"
	HookEventPostFileEdit      HookEvent = "post_file_edit"
	HookEventSubagentStart     HookEvent = "subagent_start"
	HookEventSubagentStop      HookEvent = "subagent_stop"
	HookEventPreCompact        HookEvent = "pre_compact"
	HookEventPostCompact       HookEvent = "post_compact"
	HookEventStop              HookEvent = "stop"
	HookEventStopFailure       HookEvent = "stop_failure"
)

// HookMatcher describes what a hook filters on and which syntax the pattern
// uses. Adapters may use provider-native matchers or script-side filtering.
type HookMatcher struct {
	Subject HookMatcherSubject
	Syntax  HookMatcherSyntax
	Pattern string
}

type HookMatcherSubject string

const (
	HookMatcherSubjectDefault  HookMatcherSubject = ""
	HookMatcherSubjectTool     HookMatcherSubject = "tool"
	HookMatcherSubjectCommand  HookMatcherSubject = "command"
	HookMatcherSubjectMCP      HookMatcherSubject = "mcp"
	HookMatcherSubjectPath     HookMatcherSubject = "path"
	HookMatcherSubjectPrompt   HookMatcherSubject = "prompt"
	HookMatcherSubjectSubagent HookMatcherSubject = "subagent"
	HookMatcherSubjectSource   HookMatcherSubject = "source"
)

type HookMatcherSyntax string

const (
	HookMatcherSyntaxProvider HookMatcherSyntax = ""
	HookMatcherSyntaxExact    HookMatcherSyntax = "exact"
	HookMatcherSyntaxRegex    HookMatcherSyntax = "regex"
	HookMatcherSyntaxPrefix   HookMatcherSyntax = "prefix"
	HookMatcherSyntaxContains HookMatcherSyntax = "contains"
)

// HookHandler describes the action a hook runs. Command hooks are portable
// core; the other handler types are portable extended and require adapter
// support.
type HookHandler struct {
	Type    HookHandlerType
	Command string
	Args    []string
	Env     map[string]string

	Prompt string
	URL    string
	Server string
	Tool   string
	Input  map[string]any
	Agent  string
}

type HookHandlerType string

const (
	HookHandlerCommand HookHandlerType = "command"
	HookHandlerPrompt  HookHandlerType = "prompt"
	HookHandlerHTTP    HookHandlerType = "http"
	HookHandlerMCPTool HookHandlerType = "mcp_tool"
	HookHandlerAgent   HookHandlerType = "agent"
)

type HookFailPolicy string

const (
	HookFailPolicyProviderDefault HookFailPolicy = ""
	HookFailPolicyOpen            HookFailPolicy = "open"
	HookFailPolicyClosed          HookFailPolicy = "closed"
)

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
	Key        string
	Capability string
	Values     map[string]any

	// Native is an explicit provider-native escape hatch. FileKind, Path, and
	// Section remain for backward compatibility and are interpreted as a native
	// patch when Capability is empty.
	Native   *NativeConfigPatch
	FileKind ProfileConfigFileKind
	Path     string
	Section  string
}

// NativeConfigPatch identifies a provider-native structured config patch.
type NativeConfigPatch struct {
	Provider string
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
		Skills:       cloneResolvedSkills(payload.Skills),
		MCP:          cloneMCPPayload(payload.MCP),
		Agents:       cloneAgentPayload(payload.Agents),
		Hooks:        cloneHookPayload(payload.Hooks),
		Instructions: cloneInstructions(payload.Instructions),
		Config:       cloneProfileConfigPayload(payload.Config),
		Declared:     payload.Declared,
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
		out = append(out, AgentSpec{
			Key:               value.Key,
			RuntimeName:       value.RuntimeName,
			Description:       value.Description,
			Instructions:      value.Instructions,
			Content:           value.Content,
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
			Matcher:       value.Matcher,
			Command:       value.Command,
			Args:          cloneStrings(value.Args),
			Env:           cloneStringMap(value.Env),
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
			FileKind:   value.FileKind,
			Path:       value.Path,
			Section:    value.Section,
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
		spec.Content = strings.TrimSpace(spec.Content)
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
		if spec.Instructions != "" && spec.Content != "" && spec.Instructions != spec.Content {
			return AgentPayload{}, fmt.Errorf("agentadaptor: invalid profile resources: agent %q cannot set different instructions and legacy content", spec.Key)
		}
		if spec.Instructions == "" && spec.Content != "" {
			spec.Instructions = spec.Content
		}
		if spec.SourcePath != "" && (spec.Instructions != "" || spec.Content != "") {
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
		spec.Matcher = strings.TrimSpace(spec.Matcher)
		spec.MatcherSpec.Pattern = strings.TrimSpace(spec.MatcherSpec.Pattern)
		spec.Command = strings.TrimSpace(spec.Command)
		spec.Handler = normalizeHookHandler(spec.Handler)
		if spec.Handler.Type == "" && spec.Command != "" {
			spec.Handler = HookHandler{Type: HookHandlerCommand, Command: spec.Command, Args: cloneStrings(spec.Args), Env: cloneStringMap(spec.Env)}
		}
		if spec.Command == "" && spec.Handler.Type == HookHandlerCommand {
			spec.Command = spec.Handler.Command
			spec.Args = cloneStrings(spec.Handler.Args)
			spec.Env = cloneStringMap(spec.Handler.Env)
		}
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
		patch.Path = strings.TrimSpace(patch.Path)
		patch.Section = strings.TrimSpace(patch.Section)
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
			if hasNativeConfigTarget(*patch) {
				return ProfileConfigPayload{}, fmt.Errorf("agentadaptor: invalid profile resources: config patch %q cannot set both capability and native target", patch.Key)
			}
			continue
		}
		nativePatch := effectiveNativeConfigPatch(*patch)
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

func hasNativeConfigTarget(patch ProfileConfigPatch) bool {
	if patch.Native != nil {
		return patch.Native.FileKind != "" || patch.Native.Path != "" || patch.Native.Section != "" || patch.Native.Provider != ""
	}
	return patch.FileKind != "" || patch.Path != "" || patch.Section != ""
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

func effectiveNativeConfigPatch(patch ProfileConfigPatch) *NativeConfigPatch {
	if patch.Native != nil {
		out := cloneNativeConfigPatch(patch.Native)
		if len(out.Values) == 0 {
			out.Values = cloneAnyMap(patch.Values)
		}
		return out
	}
	return &NativeConfigPatch{
		FileKind: patch.FileKind,
		Path:     patch.Path,
		Section:  patch.Section,
		Values:   cloneAnyMap(patch.Values),
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
	return ProfilePayload{Skills: cloneResolvedSkills(skills), MCP: cloneMCPPayload(mcp), Agents: cloneAgentPayload(agents), Hooks: cloneHookPayload(hooks), Instructions: cloneInstructions(instructions), Config: cloneProfileConfigPayload(config), Declared: declared, Fingerprint: fingerprint, Warnings: warnings}
}

func profileDeclarationsFromDefaults(defaults AgentDefaults) ProfileResourceDeclarations {
	declared := defaults.profileDeclared
	if len(defaults.Agents) > 0 {
		declared.Agents = true
	}
	if len(defaults.Hooks) > 0 {
		declared.Hooks = true
	}
	if len(defaults.ProfileConfig) > 0 {
		declared.Config = true
	}
	if defaults.Instructions != nil {
		declared.Instructions = true
	}
	return declared
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
