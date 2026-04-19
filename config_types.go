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
	Model                     string
	ReasoningEffort           ReasoningEffort
	Search                    bool
	FastMode                  bool
	BypassApprovalsAndSandbox bool
}

type ClaudeConfig struct {
	CommonConfig
	Model           string
	Effort          ThinkingEffort
	Chrome          bool
	SkipPermissions bool
	MaxTurnsPerRun  int
}

type CursorConfig struct {
	CommonConfig
	Model     string
	Mode      CursorMode
	AutoTrust bool
}

type ReasoningEffort string
type ThinkingEffort string
type CursorMode string

type ApprovalMode string
type SandboxMode string
type FeatureMode string
type TrustMode string

const (
	ApprovalUnset ApprovalMode = ""
	ApprovalAuto  ApprovalMode = "auto"
	ApprovalAsk   ApprovalMode = "ask"
	ApprovalNever ApprovalMode = "never"
)

const (
	SandboxUnset          SandboxMode = ""
	SandboxReadOnly       SandboxMode = "read_only"
	SandboxWorkspaceWrite SandboxMode = "workspace_write"
	SandboxDisabled       SandboxMode = "disabled"
)

const (
	FeatureUnset FeatureMode = ""
	FeatureAllow FeatureMode = "allow"
	FeatureDeny  FeatureMode = "deny"
)

const (
	TrustUnset TrustMode = ""
	TrustAsk   TrustMode = "ask"
	TrustAuto  TrustMode = "auto"
	TrustDeny  TrustMode = "deny"
)

type PermissionProfile struct {
	ApprovalMode ApprovalMode
	SandboxMode  SandboxMode
	SearchMode   FeatureMode
	BrowserMode  FeatureMode
	TrustMode    TrustMode
}
