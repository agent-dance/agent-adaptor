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
// treats it as opaque metadata; adapters decide how to inject Path/ID into the
// provider profile or prompt.
type InstructionsBundleRef struct {
	ID          string
	Path        string
	Fingerprint string
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

// ReasoningEffort is the Codex reasoning effort flag value.
type ReasoningEffort string

// ThinkingEffort is the Claude thinking effort flag value.
type ThinkingEffort string

// CursorMode is the Cursor Agent mode flag value.
type CursorMode string
