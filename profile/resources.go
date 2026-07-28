package profile

import (
	"time"

	"github.com/agent-dance/agent-adaptor/mcp"
	"github.com/agent-dance/agent-adaptor/skill"
)

// Resources is the host-facing desired-state bundle for an effective
// provider profile. It deliberately owns the consumer vocabulary instead of
// aliasing the Driver SPI or the internal engine representation.
//
// A nil MCP, Agents, Hooks, or Config slice means that resource family was not
// declared. A non-nil empty slice explicitly declares an empty family and
// clears SDK-managed entries. Skills are additive; an empty Skills slice is a
// no-op. Instructions is declared when non-nil.
type Resources struct {
	Skills       []skill.Ref
	MCP          []mcp.Server
	Agents       []SubAgent
	Hooks        []Hook
	Instructions *Instructions
	Config       []ConfigPatch
}

// SubAgent describes one host-declared sub-agent/profile agent entry.
type SubAgent struct {
	Key               string
	RuntimeName       string
	Description       string
	Instructions      string
	SourcePath        string
	SourceFingerprint string

	Model           string
	ReasoningEffort string
	ToolPolicy      *ToolPolicy
	PermissionMode  string
	SandboxMode     string
	MCPServers      []string
	Skills          []string
	Hooks           []Hook

	Native   map[string]any
	Metadata map[string]string
}

// ToolPolicy captures provider-neutral tool allow/deny intent for a
// SubAgent. Drivers translate this intent to provider-native controls.
type ToolPolicy struct {
	Allow []string
	Deny  []string
}

// Hook describes one host-declared provider hook.
type Hook struct {
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

// HookEvent is the SDK-level lifecycle event a Hook fires on.
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

// HookMatcher describes what a Hook filters on and which syntax the pattern
// uses.
type HookMatcher struct {
	Subject HookMatcherSubject
	Syntax  HookMatcherSyntax
	Pattern string
}

// HookMatcherSubject identifies the value matched by a HookMatcher.
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

// HookMatcherSyntax identifies how a HookMatcher pattern is interpreted.
type HookMatcherSyntax string

const (
	HookMatcherSyntaxProvider HookMatcherSyntax = ""
	HookMatcherSyntaxExact    HookMatcherSyntax = "exact"
	HookMatcherSyntaxRegex    HookMatcherSyntax = "regex"
	HookMatcherSyntaxPrefix   HookMatcherSyntax = "prefix"
	HookMatcherSyntaxContains HookMatcherSyntax = "contains"
)

// HookHandler describes the action a Hook runs. Command hooks are portable
// core; other handler types require explicit Driver support.
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

// HookHandlerType identifies a hook action.
type HookHandlerType string

const (
	HookHandlerCommand HookHandlerType = "command"
	HookHandlerPrompt  HookHandlerType = "prompt"
	HookHandlerHTTP    HookHandlerType = "http"
	HookHandlerMCPTool HookHandlerType = "mcp_tool"
	HookHandlerAgent   HookHandlerType = "agent"
)

// HookFailPolicy controls how a failed hook affects the provider operation.
type HookFailPolicy string

const (
	HookFailPolicyProviderDefault HookFailPolicy = ""
	HookFailPolicyOpen            HookFailPolicy = "open"
	HookFailPolicyClosed          HookFailPolicy = "closed"
)

// Instructions points at host-supplied instruction material. Drivers decide
// whether to materialize it as a provider-native file/rule or inject it into
// the prompt as a fallback.
type Instructions struct {
	ID          string
	Path        string
	Content     string
	Fingerprint string
	Scope       InstructionScope
	Mode        InstructionMode
	Native      map[string]any
}

// InstructionScope identifies the provider profile layer for instructions.
type InstructionScope string

const (
	InstructionScopeDefault InstructionScope = ""
	InstructionScopeUser    InstructionScope = "user"
	InstructionScopeProject InstructionScope = "project"
	InstructionScopeLocal   InstructionScope = "local"
	InstructionScopeRun     InstructionScope = "run"
)

// InstructionMode controls whether instructions add to or replace the
// provider's existing instruction set.
type InstructionMode string

const (
	InstructionModeAdditive InstructionMode = ""
	InstructionModeReplace  InstructionMode = "replace"
)

// Text builds an inline instruction bundle from literal content.
func Text(content string) *Instructions {
	return &Instructions{Content: content}
}

// ConfigPatch is one structured profile configuration update.
type ConfigPatch struct {
	Key        string
	Capability string
	Values     map[string]any
	Native     *NativeConfigPatch
}

// NativeConfigPatch identifies a provider-native structured config patch.
type NativeConfigPatch struct {
	Provider string
	FileKind ConfigFileKind
	Path     string
	Section  string
	Values   map[string]any
}

// ConfigFileKind identifies the serialized structured config format.
type ConfigFileKind string

const (
	ConfigFileJSON ConfigFileKind = "json"
	ConfigFileTOML ConfigFileKind = "toml"
)
