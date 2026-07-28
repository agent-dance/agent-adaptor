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
// copied by a clone selection created through package profile.
type CloneProfileOptions struct {
	IncludeSettings bool
	IncludeMCP      bool
	IncludeSkills   bool
	// AuthMode controls auth-file handling. The zero value keeps auth out of
	// the clone.
	AuthMode CloneProfileAuthMode
}

// CloneProfileAuthMode controls how a clone selection seeds auth files from
// the source provider profile.
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

// ProfileSelection is the normalized Agent profile request. Application code
// normally constructs its public profile.Selection alias with package profile
// and supplies it to adaptor.WithProfile.
type ProfileSelection struct {
	// Mode selects native, dedicated, cloned, or driver-default resolution.
	Mode ProfileMode
	// Dir is the dedicated or managed destination profile directory.
	Dir string
	// From is the optional source directory for clone mode.
	From string
	// Clone controls clone contents when Mode is ProfileModeClone.
	Clone *CloneProfileOptions
}

// InstructionsBundleRef points at host-supplied instruction material. The SDK
// treats it as desired state; drivers decide whether to materialize it as a
// provider-native file/rule or inject it into the prompt as a fallback.
type InstructionsBundleRef struct {
	// ID is the stable host identity of the instruction bundle.
	ID string
	// Path locates instruction material already present on disk.
	Path string
	// Content carries inline instruction material.
	Content string
	// Fingerprint identifies the resolved provider-visible contents.
	Fingerprint string
	// Scope selects where the instructions apply.
	Scope InstructionScope
	// Mode selects additive or replacement semantics.
	Mode InstructionMode
	// Native carries provider-specific extensions.
	Native map[string]any
}

// InstructionScope identifies where an instruction bundle applies.
type InstructionScope string

const (
	// InstructionScopeDefault delegates scope selection to the driver.
	InstructionScopeDefault InstructionScope = ""
	// InstructionScopeUser applies instructions to the provider user profile.
	InstructionScopeUser InstructionScope = "user"
	// InstructionScopeProject applies instructions to the resolved project.
	InstructionScopeProject InstructionScope = "project"
	// InstructionScopeLocal applies instructions to the local workspace only.
	InstructionScopeLocal InstructionScope = "local"
	// InstructionScopeRun applies instructions only to the current invocation.
	InstructionScopeRun InstructionScope = "run"
)

// InstructionMode controls whether instructions extend or replace native ones.
type InstructionMode string

const (
	// InstructionModeAdditive adds the bundle to native instructions.
	InstructionModeAdditive InstructionMode = ""
	// InstructionModeReplace replaces native instructions where supported.
	InstructionModeReplace InstructionMode = "replace"
)

// ProfilePayload is the driver-facing normalized profile desired state for a
// single resolved invocation. Fingerprint covers every provider-visible
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
	Key               string
	RuntimeName       string
	Description       string
	Instructions      string
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
	// HookEventSessionStart runs when a provider session starts.
	HookEventSessionStart HookEvent = "session_start"
	// HookEventSessionEnd runs when a provider session ends.
	HookEventSessionEnd HookEvent = "session_end"
	// HookEventPromptSubmit runs when a prompt is submitted.
	HookEventPromptSubmit HookEvent = "prompt_submit"
	// HookEventPromptExpand runs when a provider expands a prompt.
	HookEventPromptExpand HookEvent = "prompt_expand"
	// HookEventPreTool runs before a tool invocation.
	HookEventPreTool HookEvent = "pre_tool"
	// HookEventPostTool runs after a successful tool invocation.
	HookEventPostTool HookEvent = "post_tool"
	// HookEventToolFailure runs after a failed tool invocation.
	HookEventToolFailure HookEvent = "tool_failure"
	// HookEventPermissionRequest runs when a permission decision is requested.
	HookEventPermissionRequest HookEvent = "permission_request"
	// HookEventPreShell runs before a shell command.
	HookEventPreShell HookEvent = "pre_shell"
	// HookEventPostShell runs after a shell command.
	HookEventPostShell HookEvent = "post_shell"
	// HookEventPreMCP runs before an MCP operation.
	HookEventPreMCP HookEvent = "pre_mcp"
	// HookEventPostMCP runs after an MCP operation.
	HookEventPostMCP HookEvent = "post_mcp"
	// HookEventPreFileRead runs before a file read.
	HookEventPreFileRead HookEvent = "pre_file_read"
	// HookEventPostFileEdit runs after a file edit.
	HookEventPostFileEdit HookEvent = "post_file_edit"
	// HookEventSubagentStart runs when a provider sub-agent starts.
	HookEventSubagentStart HookEvent = "subagent_start"
	// HookEventSubagentStop runs when a provider sub-agent stops.
	HookEventSubagentStop HookEvent = "subagent_stop"
	// HookEventPreCompact runs before provider context compaction.
	HookEventPreCompact HookEvent = "pre_compact"
	// HookEventPostCompact runs after provider context compaction.
	HookEventPostCompact HookEvent = "post_compact"
	// HookEventStop runs when provider execution stops normally.
	HookEventStop HookEvent = "stop"
	// HookEventStopFailure runs when provider execution stops with a failure.
	HookEventStopFailure HookEvent = "stop_failure"
)

// HookMatcher describes what a hook filters on and which syntax the pattern
// uses. Drivers may use provider-native matchers or script-side filtering.
type HookMatcher struct {
	Subject HookMatcherSubject
	Syntax  HookMatcherSyntax
	Pattern string
}

// HookMatcherSubject identifies the value matched by a hook filter.
type HookMatcherSubject string

const (
	// HookMatcherSubjectDefault delegates subject selection to the provider.
	HookMatcherSubjectDefault HookMatcherSubject = ""
	// HookMatcherSubjectTool matches tool names.
	HookMatcherSubjectTool HookMatcherSubject = "tool"
	// HookMatcherSubjectCommand matches shell commands.
	HookMatcherSubjectCommand HookMatcherSubject = "command"
	// HookMatcherSubjectMCP matches MCP servers or tools.
	HookMatcherSubjectMCP HookMatcherSubject = "mcp"
	// HookMatcherSubjectPath matches filesystem paths.
	HookMatcherSubjectPath HookMatcherSubject = "path"
	// HookMatcherSubjectPrompt matches prompt text.
	HookMatcherSubjectPrompt HookMatcherSubject = "prompt"
	// HookMatcherSubjectSubagent matches sub-agent identifiers.
	HookMatcherSubjectSubagent HookMatcherSubject = "subagent"
	// HookMatcherSubjectSource matches provider event sources.
	HookMatcherSubjectSource HookMatcherSubject = "source"
)

// HookMatcherSyntax identifies how a HookMatcher pattern is interpreted.
type HookMatcherSyntax string

const (
	// HookMatcherSyntaxProvider delegates pattern syntax to the provider.
	HookMatcherSyntaxProvider HookMatcherSyntax = ""
	// HookMatcherSyntaxExact requires an exact match.
	HookMatcherSyntaxExact HookMatcherSyntax = "exact"
	// HookMatcherSyntaxRegex interprets the pattern as a regular expression.
	HookMatcherSyntaxRegex HookMatcherSyntax = "regex"
	// HookMatcherSyntaxPrefix requires a matching prefix.
	HookMatcherSyntaxPrefix HookMatcherSyntax = "prefix"
	// HookMatcherSyntaxContains requires the value to contain the pattern.
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

// HookHandlerType identifies the action executed for a hook.
type HookHandlerType string

const (
	// HookHandlerCommand executes a local command.
	HookHandlerCommand HookHandlerType = "command"
	// HookHandlerPrompt invokes a provider prompt hook.
	HookHandlerPrompt HookHandlerType = "prompt"
	// HookHandlerHTTP invokes an HTTP endpoint.
	HookHandlerHTTP HookHandlerType = "http"
	// HookHandlerMCPTool invokes an MCP tool.
	HookHandlerMCPTool HookHandlerType = "mcp_tool"
	// HookHandlerAgent invokes a provider sub-agent.
	HookHandlerAgent HookHandlerType = "agent"
)

// HookFailPolicy controls whether a hook failure stops the provider action.
type HookFailPolicy string

const (
	// HookFailPolicyProviderDefault delegates failure handling to the provider.
	HookFailPolicyProviderDefault HookFailPolicy = ""
	// HookFailPolicyOpen allows the provider action after a hook failure.
	HookFailPolicyOpen HookFailPolicy = "open"
	// HookFailPolicyClosed prevents the provider action after a hook failure.
	HookFailPolicyClosed HookFailPolicy = "closed"
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
	// ProfileConfigFileJSON selects a JSON profile configuration file.
	ProfileConfigFileJSON ProfileConfigFileKind = "json"
	// ProfileConfigFileTOML selects a TOML profile configuration file.
	ProfileConfigFileTOML ProfileConfigFileKind = "toml"
)

// ProfileConfigPatch is a structured config update. Hosts supply typed values;
// drivers/reconcilers own provider-native encoding.
type ProfileConfigPatch struct {
	Key        string
	Capability string
	Values     map[string]any
	Native     *NativeConfigPatch
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
