package engine

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

// CodexConfig configures the built-in codex adapter. CommonConfig controls
// process/profile/workspace defaults; Model and ReasoningEffort map to Codex
// model settings.
type CodexConfig struct {
	CommonConfig
	Model           string
	ReasoningEffort ReasoningEffort
	FastMode        bool
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
)

// ReasoningEffort is the Codex reasoning effort flag value.
type ReasoningEffort string

// ThinkingEffort is the Claude thinking effort flag value.
type ThinkingEffort string

// CursorMode is the Cursor Agent mode flag value.
type CursorMode string
