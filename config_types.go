package agentadaptor

import "time"

type CommonConfig struct {
	Command string
	CWD     string
	Env     []EnvBinding
	// AgentProfileDir maps to the adapter's native local profile location when
	// no adapter-specific profile env override is present. Built-in adapters map
	// it to CODEX_HOME, CLAUDE_CONFIG_DIR, or CURSOR_HOME respectively.
	AgentProfileDir         string
	Instructions            *InstructionsBundleRef
	PromptTemplate          string
	BootstrapPromptTemplate string
	WorkspaceStrategy       *WorkspaceStrategy
	WorkspaceRuntime        *WorkspaceRuntimeConfig
	Timeout                 time.Duration
	GracePeriod             time.Duration
	ExtraArgs               []string
}

type EnvBinding struct {
	Name  string
	Value string
}

type InstructionsBundleRef struct {
	ID          string
	Path        string
	Fingerprint string
}

type CodexConfig struct {
	CommonConfig
	Model           string
	ReasoningEffort ReasoningEffort
	FastMode          bool
}

type ClaudeConfig struct {
	CommonConfig
	Model          string
	Effort         ThinkingEffort
	MaxTurnsPerRun int
}

type CursorConfig struct {
	CommonConfig
	Model string
	Mode  CursorMode
}

type ReasoningEffort string
type ThinkingEffort string
type CursorMode string
