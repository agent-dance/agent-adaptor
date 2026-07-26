package profile

import (
	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/engine"
)

// Resources is the host-facing desired-state bundle for an effective
// profile: sub-agents, hooks, an instruction bundle, structured config
// patches, plus skill and MCP entries. It is an alias for the existing
// public contract type (root ProfileResources), so values flow into
// WithDefaultProfileResources / WithProfileResources unchanged; declared
// kinds are reconciled (an empty declared slice clears managed entries,
// an undeclared kind is left alone) and materialization is truthfully
// reported through ProfileState / SyncProfile.
//
// Skills and MCP element types keep their own vocabulary packages; until
// those land use the root aliases (SkillRef, MCPConfig).
type Resources = engine.ProfileResources

// --- Sub-agents -------------------------------------------------------------

// SubAgent describes one host-declared sub-agent/profile agent entry. It is
// an alias for [driver.AgentSpec] and is the element type of
// Resources.Agents.
type SubAgent = driver.AgentSpec

// ToolPolicy captures provider-neutral tool allow/deny intent for a
// SubAgent. It is an alias for [driver.AgentToolPolicy].
type ToolPolicy = driver.AgentToolPolicy

// --- Hooks -------------------------------------------------------------------

// Hook describes one host-declared provider hook. It is an alias for
// [driver.HookSpec] and is the element type of Resources.Hooks.
type Hook = driver.HookSpec

// HookEvent is the SDK-level lifecycle event a Hook fires on. Alias for
// [driver.HookEvent].
type HookEvent = driver.HookEvent

const (
	HookEventSessionStart      = driver.HookEventSessionStart
	HookEventSessionEnd        = driver.HookEventSessionEnd
	HookEventPromptSubmit      = driver.HookEventPromptSubmit
	HookEventPromptExpand      = driver.HookEventPromptExpand
	HookEventPreTool           = driver.HookEventPreTool
	HookEventPostTool          = driver.HookEventPostTool
	HookEventToolFailure       = driver.HookEventToolFailure
	HookEventPermissionRequest = driver.HookEventPermissionRequest
	HookEventPreShell          = driver.HookEventPreShell
	HookEventPostShell         = driver.HookEventPostShell
	HookEventPreMCP            = driver.HookEventPreMCP
	HookEventPostMCP           = driver.HookEventPostMCP
	HookEventPreFileRead       = driver.HookEventPreFileRead
	HookEventPostFileEdit      = driver.HookEventPostFileEdit
	HookEventSubagentStart     = driver.HookEventSubagentStart
	HookEventSubagentStop      = driver.HookEventSubagentStop
	HookEventPreCompact        = driver.HookEventPreCompact
	HookEventPostCompact       = driver.HookEventPostCompact
	HookEventStop              = driver.HookEventStop
	HookEventStopFailure       = driver.HookEventStopFailure
)

// HookMatcher describes what a Hook filters on and which pattern syntax it
// uses. Alias for [driver.HookMatcher].
type HookMatcher = driver.HookMatcher

// HookMatcherSubject is an alias for [driver.HookMatcherSubject].
type HookMatcherSubject = driver.HookMatcherSubject

const (
	HookMatcherSubjectDefault  = driver.HookMatcherSubjectDefault
	HookMatcherSubjectTool     = driver.HookMatcherSubjectTool
	HookMatcherSubjectCommand  = driver.HookMatcherSubjectCommand
	HookMatcherSubjectMCP      = driver.HookMatcherSubjectMCP
	HookMatcherSubjectPath     = driver.HookMatcherSubjectPath
	HookMatcherSubjectPrompt   = driver.HookMatcherSubjectPrompt
	HookMatcherSubjectSubagent = driver.HookMatcherSubjectSubagent
	HookMatcherSubjectSource   = driver.HookMatcherSubjectSource
)

// HookMatcherSyntax is an alias for [driver.HookMatcherSyntax].
type HookMatcherSyntax = driver.HookMatcherSyntax

const (
	HookMatcherSyntaxProvider = driver.HookMatcherSyntaxProvider
	HookMatcherSyntaxExact    = driver.HookMatcherSyntaxExact
	HookMatcherSyntaxRegex    = driver.HookMatcherSyntaxRegex
	HookMatcherSyntaxPrefix   = driver.HookMatcherSyntaxPrefix
	HookMatcherSyntaxContains = driver.HookMatcherSyntaxContains
)

// HookHandler describes the action a Hook runs. Alias for
// [driver.HookHandler].
type HookHandler = driver.HookHandler

// HookHandlerType is an alias for [driver.HookHandlerType].
type HookHandlerType = driver.HookHandlerType

const (
	HookHandlerCommand = driver.HookHandlerCommand
	HookHandlerPrompt  = driver.HookHandlerPrompt
	HookHandlerHTTP    = driver.HookHandlerHTTP
	HookHandlerMCPTool = driver.HookHandlerMCPTool
	HookHandlerAgent   = driver.HookHandlerAgent
)

// HookFailPolicy is an alias for [driver.HookFailPolicy].
type HookFailPolicy = driver.HookFailPolicy

const (
	HookFailPolicyProviderDefault = driver.HookFailPolicyProviderDefault
	HookFailPolicyOpen            = driver.HookFailPolicyOpen
	HookFailPolicyClosed          = driver.HookFailPolicyClosed
)

// --- Instructions -------------------------------------------------------------

// Instructions points at host-supplied instruction material. It is an alias
// for [driver.InstructionsBundleRef] and is the type behind
// Resources.Instructions. Drivers decide whether to materialize it as a
// provider-native file/rule or inject it into the prompt as a fallback.
type Instructions = driver.InstructionsBundleRef

// InstructionScope is an alias for [driver.InstructionScope].
type InstructionScope = driver.InstructionScope

const (
	InstructionScopeDefault = driver.InstructionScopeDefault
	InstructionScopeUser    = driver.InstructionScopeUser
	InstructionScopeProject = driver.InstructionScopeProject
	InstructionScopeLocal   = driver.InstructionScopeLocal
	InstructionScopeRun     = driver.InstructionScopeRun
)

// InstructionMode is an alias for [driver.InstructionMode].
type InstructionMode = driver.InstructionMode

const (
	InstructionModeAdditive = driver.InstructionModeAdditive
	InstructionModeReplace  = driver.InstructionModeReplace
)

// Text builds an inline instruction bundle from literal content. It is the
// one-line spelling used by Resources declarations:
//
//	profile.Resources{Instructions: profile.Text("Follow ACME coding standards.")}
//
// For file-backed or scoped bundles construct Instructions directly.
func Text(content string) *Instructions {
	return &Instructions{Content: content}
}

// --- Structured config patches -------------------------------------------------

// ConfigPatch is one structured profile config update. It is an alias for
// [driver.ProfileConfigPatch] and is the element type of Resources.Config.
type ConfigPatch = driver.ProfileConfigPatch

// NativeConfigPatch identifies a provider-native structured config patch —
// the explicit escape hatch inside ConfigPatch. Alias for
// [driver.NativeConfigPatch].
type NativeConfigPatch = driver.NativeConfigPatch

// ConfigFileKind identifies the structured config format a native patch
// targets. Alias for [driver.ProfileConfigFileKind].
type ConfigFileKind = driver.ProfileConfigFileKind

const (
	ConfigFileJSON = driver.ProfileConfigFileJSON
	ConfigFileTOML = driver.ProfileConfigFileTOML
)
