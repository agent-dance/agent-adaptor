package agentadaptor

import "time"

// CommonConfig contains adapter-independent CLI/process defaults embedded by
// CodexConfig, ClaudeConfig, and CursorConfig. Adapter packages may interpret a
// subset, but hosts can set common fields without knowing the concrete agent.
type CommonConfig struct {
	Command                 string
	CWD                     string
	Env                     []EnvBinding
	Instructions            *InstructionsBundleRef
	PromptTemplate          string
	BootstrapPromptTemplate string
	WorkspaceStrategy       *WorkspaceStrategy
	WorkspaceRuntime        *WorkspaceRuntimeConfig
	Timeout                 time.Duration
	GracePeriod             time.Duration
	ExtraArgs               []string
}

// EnvBinding is one explicit environment variable override passed to an
// adapter process or used during profile resolution.
type EnvBinding struct {
	Name  string
	Value string
}

// InstructionsBundleRef points at host-supplied instruction material. The SDK
// treats it as desired state; adapters decide whether to materialize it as a
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

// CodexConfig configures the built-in codex adapter. CommonConfig controls
// process/profile/workspace defaults; the remaining fields map to Codex CLI
// settings.
type CodexConfig struct {
	CommonConfig
	Model            string
	ReasoningEffort  ReasoningEffort
	FastMode         bool
	SkipGitRepoCheck bool
}

// ClaudeConfig configures the built-in claude adapter. MaxTurnsPerRun is a
// guardrail for a single adapter invocation, not a session length.
type ClaudeConfig struct {
	CommonConfig
	Model          string
	Effort         ThinkingEffort
	MaxTurnsPerRun int
}

// CursorConfig configures the built-in cursor adapter.
type CursorConfig struct {
	CommonConfig
	Model string
	Mode  CursorMode
}

// CodeBuddyConfig configures the built-in codebuddy adapter. CommonConfig
// controls process/profile/workspace defaults; Model / Effort map to CodeBuddy
// session settings. PermissionMode is the CodeBuddy headless permission mode;
// when empty the driver derives it from the run policy. MaxTurnsPerRun is a
// guardrail for a single adapter invocation, not a session length.
type CodeBuddyConfig struct {
	CommonConfig
	Model          string
	Effort         ThinkingEffort
	PermissionMode CodeBuddyPermissionMode
	MaxTurnsPerRun int
}

// CodeBuddyPermissionMode is the CodeBuddy `--permission-mode` flag value.
type CodeBuddyPermissionMode string

const (
	// CodeBuddyPermissionUnset lets the driver derive the mode from run policy.
	CodeBuddyPermissionUnset CodeBuddyPermissionMode = ""
	// CodeBuddyPermissionDefault prompts on first use of each tool.
	CodeBuddyPermissionDefault CodeBuddyPermissionMode = "default"
	// CodeBuddyPermissionAcceptEdits auto-accepts file edit permissions.
	CodeBuddyPermissionAcceptEdits CodeBuddyPermissionMode = "acceptEdits"
	// CodeBuddyPermissionPlan restricts the agent to analysis / planning.
	CodeBuddyPermissionPlan CodeBuddyPermissionMode = "plan"
	// CodeBuddyPermissionAuto lets an AI classifier auto-approve safe actions.
	CodeBuddyPermissionAuto CodeBuddyPermissionMode = "auto"
	// CodeBuddyPermissionDontAsk runs pre-approved actions and denies the rest.
	CodeBuddyPermissionDontAsk CodeBuddyPermissionMode = "dontAsk"
	// CodeBuddyPermissionBypass skips all permission prompts.
	CodeBuddyPermissionBypass CodeBuddyPermissionMode = "bypassPermissions"
	// CodeBuddyPermissionFullAccess skips ALL permission checks.
	CodeBuddyPermissionFullAccess CodeBuddyPermissionMode = "fullAccess"
)

// ReasoningEffort is the Codex reasoning effort flag value.
type ReasoningEffort string

// ThinkingEffort is the Claude thinking effort flag value.
type ThinkingEffort string

// CursorMode is the Cursor Agent mode flag value.
type CursorMode string
