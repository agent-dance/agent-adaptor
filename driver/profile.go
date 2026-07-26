package driver

import "time"

// ProfileMode describes how a built-in driver should choose its local
// provider profile directory for auth, config, MCP, and skill state.
type ProfileMode string

const (
	// ProfileModeUnset means "use the driver default behavior".
	ProfileModeUnset ProfileMode = ""
	// ProfileModeNative uses the provider's native profile/home resolution.
	ProfileModeNative ProfileMode = "native"
	// ProfileModeDedicated uses Dir as the provider home/profile directory.
	ProfileModeDedicated ProfileMode = "dedicated"
	// ProfileModeClone creates or refreshes a managed profile copied from From.
	ProfileModeClone ProfileMode = "clone"
)

// CloneProfileOptions controls which parts of a source provider profile are
// copied when WithCloneProfile or WithCloneProfileFrom is used.
type CloneProfileOptions struct {
	IncludeSettings bool
	IncludeMCP      bool
	IncludeSkills   bool
	// IncludeAuth keeps the original clone behavior: auth files are copied
	// into the target profile when they are missing. Prefer AuthMode for
	// OAuth-backed CLIs where duplicated refresh-token state is unsafe.
	IncludeAuth bool
	// AuthMode controls auth-file handling independently from IncludeAuth.
	// The zero value keeps auth out of the clone unless IncludeAuth is true.
	AuthMode CloneProfileAuthMode
}

// CloneProfileAuthMode controls how WithCloneProfile and WithCloneProfileFrom
// seed auth files from the source provider profile.
type CloneProfileAuthMode string

const (
	// CloneProfileAuthNone leaves auth files out of the cloned profile.
	CloneProfileAuthNone CloneProfileAuthMode = ""
	// CloneProfileAuthCopy copies auth files into the cloned profile. This is
	// suitable for static API-key style auth, but can duplicate OAuth refresh
	// token state for CLIs that rotate tokens in-place.
	CloneProfileAuthCopy CloneProfileAuthMode = "copy"
	// CloneProfileAuthLink shares auth files with the source profile by
	// symlink, falling back to a hardlink when symlinks are unavailable. It
	// fails rather than silently copying if neither shared-file strategy works.
	CloneProfileAuthLink CloneProfileAuthMode = "link"
)

// ProfileSelection is the normalized binding-level profile request. Hosts
// usually construct it through WithNativeProfile, WithDedicatedProfile,
// WithCloneProfile, or WithCloneProfileFrom rather than setting fields
// directly.
type ProfileSelection struct {
	Mode  ProfileMode
	Dir   string
	From  string
	Clone *CloneProfileOptions
}

// InstructionsBundleRef points at host-supplied instruction material. The SDK
// treats it as desired state; drivers decide whether to materialize it as a
// provider-native file/rule or inject it into the prompt as a fallback.
type InstructionsBundleRef struct {
	ID          string
	Path        string
	Content     string
	Fingerprint string
	Scope       InstructionScope
	Mode        InstructionMode
	Native      map[string]any
}

type InstructionScope string

const (
	InstructionScopeDefault InstructionScope = ""
	InstructionScopeUser    InstructionScope = "user"
	InstructionScopeProject InstructionScope = "project"
	InstructionScopeLocal   InstructionScope = "local"
	InstructionScopeRun     InstructionScope = "run"
)

type InstructionMode string

const (
	InstructionModeAdditive InstructionMode = ""
	InstructionModeReplace  InstructionMode = "replace"
)

// ProfilePayload is the driver-facing normalized profile desired state for a
// single Run/Start invocation. Fingerprint covers every provider-visible
// resource kind so drivers can use it as the session resume guard.
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
// profile agent. Drivers map it to provider-native tool/sandbox/permission
// fields when they declare support.
type AgentToolPolicy struct {
	Allow []string
	Deny  []string
}

// AgentPayload is the normalized driver-facing agent resource state.
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

// HookEvent is the SDK-level lifecycle event intent. Drivers translate these
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
// uses. Drivers may use provider-native matchers or script-side filtering.
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
// core; the other handler types are portable extended and require driver
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

// HookPayload is the normalized driver-facing hook resource state.
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
// drivers/reconcilers own provider-native encoding.
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

// ProfileConfigPayload is the normalized driver-facing config patch state.
type ProfileConfigPayload struct {
	Patches     []ProfileConfigPatch
	Fingerprint string
	Warnings    []string
}
