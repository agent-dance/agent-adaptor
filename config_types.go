package agentadaptor

import "time"

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
	FastMode        bool
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
