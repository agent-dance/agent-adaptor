package agentadaptor

type Skill struct {
	Key            string
	Runtime        string
	Content        string
	PathHint       string
	Metadata       map[string]string
	Files          []SkillFile
	Required       bool
	RequiredReason string
}

type SkillFile struct {
	Path    string
	Kind    SkillFileKind
	Content string
}

type SkillFileKind string

const (
	SkillFilePrimary   SkillFileKind = "skill"
	SkillFileMarkdown  SkillFileKind = "markdown"
	SkillFileReference SkillFileKind = "reference"
	SkillFileScript    SkillFileKind = "script"
	SkillFileAsset     SkillFileKind = "asset"
	SkillFileOther     SkillFileKind = "other"
)

type SkillPayload struct {
	Mode           SkillSyncMode
	Requested      []string
	Resolved       []Skill
	RuntimeEntries []SkillRuntimeEntry
	Warnings       []string
	Fingerprint    string
}

type SkillSyncMode string

const (
	SkillSyncUnsupported SkillSyncMode = "unsupported"
	SkillSyncEphemeral   SkillSyncMode = "ephemeral"
	SkillSyncPersistent  SkillSyncMode = "persistent"
)

type SkillSnapshot struct {
	DriverType string
	Supported  bool
	Mode       SkillSyncMode
	Desired    []string
	Resolved   []Skill
	Entries    []SkillEntry
	Warnings   []string
}

type SkillAssemblyRequest struct {
	DriverType string
	TenantID   string
	Agent      AgentIdentity
	Config     any
	Workspace  WorkspaceLease
	Requested  []string
	Available  []Skill
	Resolved   []Skill
}

type SkillRuntimeEntry struct {
	Key            string
	RuntimeName    string
	SourcePath     string
	Required       bool
	RequiredReason string
}

type SkillEntry struct {
	Key            string
	RuntimeName    string
	Desired        bool
	Managed        bool
	Required       bool
	RequiredReason string
	State          SkillState
	Origin         SkillOrigin
	OriginLabel    string
	LocationLabel  string
	ReadOnly       bool
	SourcePath     string
	TargetPath     string
	Detail         string
}

type SkillState string
type SkillOrigin string

const (
	SkillStateAvailable  SkillState = "available"
	SkillStateConfigured SkillState = "configured"
	SkillStateInstalled  SkillState = "installed"
	SkillStateMissing    SkillState = "missing"
	SkillStateStale      SkillState = "stale"
	SkillStateExternal   SkillState = "external"
)

const (
	SkillOriginManaged  SkillOrigin = "company_managed"
	SkillOriginRequired SkillOrigin = "paperclip_required"
	SkillOriginUser     SkillOrigin = "user_installed"
	SkillOriginUnknown  SkillOrigin = "external_unknown"
)

type ModelInfo struct {
	ID    string
	Label string
}

type DetectedModel struct {
	Model      string
	Provider   string
	Source     string
	Candidates []string
}

type AgentProfileSource string

const (
	AgentProfileSourceBindingEnv      AgentProfileSource = "binding_env"
	AgentProfileSourceAgentProfileDir AgentProfileSource = "agent_profile_dir"
	AgentProfileSourceProcessEnv      AgentProfileSource = "process_env"
	AgentProfileSourceDefault         AgentProfileSource = "default"
	AgentProfileSourceManaged         AgentProfileSource = "managed"
	AgentProfileSourceUnsupported     AgentProfileSource = "unsupported"
)

// AgentProfile reports the effective local operator profile directory for one
// bound adapter as seen by the SDK control plane.
//
// Dir is the effective directory the built-in adapter will inspect or use for
// local profile semantics. Source tells whether that directory came from an
// explicit CommonConfig.Env override, CommonConfig.AgentProfileDir, process
// environment fallback, an adapter-native default path, or an adapter-managed
// home. Managed is true only when the adapter actively synthesizes an isolated
// profile directory, such as Codex's managed CODEX_HOME.
type AgentProfile struct {
	DriverType string
	Supported  bool
	Dir        string
	EnvVar     string
	Source     AgentProfileSource
	Managed    bool
	Error      string
}

type EnvironmentStatus string

const (
	EnvironmentPass EnvironmentStatus = "pass"
	EnvironmentWarn EnvironmentStatus = "warn"
	EnvironmentFail EnvironmentStatus = "fail"
)

// EnvironmentReport is the normalized admin-facing health report for one
// adapter binding. Healthy remains for backward compatibility; new hosts should
// prefer Status plus the individual Checks.
type EnvironmentReport struct {
	DriverType string
	Status     EnvironmentStatus
	Healthy    bool
	Summary    string
	Checks     []EnvironmentCheck
}

// EnvironmentCheck is one probe result within an EnvironmentReport. Code is the
// stable machine-facing identifier; Message, Detail, and Hint are host-facing
// text that can be rendered directly in diagnostics UIs.
type EnvironmentCheck struct {
	Code    string
	Level   string
	Message string
	Detail  string
	Hint    string
}

type WorkspaceStrategyType string
type WorkspaceMode string
type WorkspaceReleaseMode string

const (
	WorkspaceStrategyProjectPrimary WorkspaceStrategyType = "project_primary"
	WorkspaceStrategyGitWorktree    WorkspaceStrategyType = "git_worktree"
	WorkspaceStrategyAdapterManaged WorkspaceStrategyType = "adapter_managed"
	WorkspaceStrategyCloudSandbox   WorkspaceStrategyType = "cloud_sandbox"
)

const (
	WorkspaceModeShared       WorkspaceMode = "shared_workspace"
	WorkspaceModeIsolated     WorkspaceMode = "isolated_workspace"
	WorkspaceModeOperator     WorkspaceMode = "operator_branch"
	WorkspaceModeReuse        WorkspaceMode = "reuse_existing"
	WorkspaceModeAgentDefault WorkspaceMode = "agent_default"
)

const (
	WorkspaceReleaseKeep WorkspaceReleaseMode = "keep"
	WorkspaceReleaseStop WorkspaceReleaseMode = "stop"
)

type WorkspaceStrategy struct {
	Type              WorkspaceStrategyType
	BaseRef           string
	BranchTemplate    string
	WorktreeParentDir string
}

type WorkspaceRuntimeConfig struct {
	Services []RuntimeServiceSpec
}

type WorkspaceLease struct {
	ID             string
	Mode           WorkspaceMode
	StrategyType   WorkspaceStrategyType
	CWD            string
	Fingerprint    string
	Metadata       map[string]string
	InstructionsID string
}

type WorkspaceRequest struct {
	BaseCWD  string
	Spec     WorkspaceSpec
	Metadata map[string]string
}

type WorkspaceSpec interface {
	workspaceRequest() WorkspaceRequestData
}

type WorkspaceRequestData struct {
	Mode           WorkspaceMode
	StrategyType   WorkspaceStrategyType
	BaseRef        string
	BranchTemplate string
	ParentDir      string
}

type GitWorktreeWorkspace struct {
	BaseRef           string
	BranchTemplate    string
	WorktreeParentDir string
}

func (w GitWorktreeWorkspace) workspaceRequest() WorkspaceRequestData {
	return WorkspaceRequestData{
		Mode:           WorkspaceModeIsolated,
		StrategyType:   WorkspaceStrategyGitWorktree,
		BaseRef:        w.BaseRef,
		BranchTemplate: w.BranchTemplate,
		ParentDir:      w.WorktreeParentDir,
	}
}

type SharedWorkspace struct{}

func (SharedWorkspace) workspaceRequest() WorkspaceRequestData {
	return WorkspaceRequestData{
		Mode:         WorkspaceModeShared,
		StrategyType: WorkspaceStrategyProjectPrimary,
	}
}

type AdapterManagedWorkspace struct{}

func (AdapterManagedWorkspace) workspaceRequest() WorkspaceRequestData {
	return WorkspaceRequestData{
		Mode:         WorkspaceModeAgentDefault,
		StrategyType: WorkspaceStrategyAdapterManaged,
	}
}

type RuntimeServiceSpec struct {
	ID          string
	Name        string
	URL         string
	Description string
	Lifecycle   RuntimeServiceLifecycle
	ReuseKey    string
	Command     string
	CWD         string
	Port        int
	Metadata    map[string]string
}

type RuntimeServiceRequest struct {
	RunID      string
	DriverType string
	Agent      AgentIdentity
	Config     any
	Workspace  WorkspaceLease
	Desired    []RuntimeServiceSpec
	Metadata   map[string]string
}

type RuntimeServiceRef struct {
	ID           string
	Name         string
	URL          string
	Status       RuntimeServiceStatus
	Lifecycle    RuntimeServiceLifecycle
	ReuseKey     string
	Command      string
	CWD          string
	Port         int
	OwnerAgentID string
	Health       RuntimeServiceHealth
	Metadata     map[string]string
}

// RuntimePayload is the runtime-service equivalent of SkillPayload.
//
// Requested contains the desired runtime services declared by binding defaults,
// config defaults, or per-run overrides. Ensured contains the concrete service
// endpoints returned by RuntimeServiceManager.Ensure.
type RuntimePayload struct {
	Requested   []RuntimeServiceSpec
	Ensured     []RuntimeServiceRef
	Fingerprint string
}

type RuntimeServiceStatus string
type RuntimeServiceLifecycle string
type RuntimeServiceHealth string

const (
	RuntimeServiceStarting RuntimeServiceStatus = "starting"
	RuntimeServiceRunning  RuntimeServiceStatus = "running"
	RuntimeServiceStopped  RuntimeServiceStatus = "stopped"
	RuntimeServiceFailed   RuntimeServiceStatus = "failed"
)

const (
	RuntimeLifecycleShared    RuntimeServiceLifecycle = "shared"
	RuntimeLifecycleEphemeral RuntimeServiceLifecycle = "ephemeral"
)

const (
	RuntimeHealthUnknown   RuntimeServiceHealth = "unknown"
	RuntimeHealthHealthy   RuntimeServiceHealth = "healthy"
	RuntimeHealthUnhealthy RuntimeServiceHealth = "unhealthy"
)

// RuntimeServiceReport is the adapter-facing execution report for one ensured
// runtime service. It can simply echo the ensured refs, or carry richer status
// data when the adapter/runtime manager can observe more details.
type RuntimeServiceReport struct {
	ID           string
	Name         string
	URL          string
	Status       RuntimeServiceStatus
	Lifecycle    RuntimeServiceLifecycle
	ReuseKey     string
	Command      string
	CWD          string
	Port         int
	OwnerAgentID string
	Health       RuntimeServiceHealth
	Metadata     map[string]string
}

// QuotaReport is the control-plane shape returned by Admin().GetQuota().
type QuotaReport struct {
	DriverType string
	Provider   string
	Source     string
	Available  bool
	Error      string
	Windows    []QuotaWindow
}

// QuotaWindow describes one quota/rate-limit/credit window reported by an
// adapter-specific quota probe.
type QuotaWindow struct {
	Label       string
	UsedPercent *int
	ResetsAt    string
	ValueLabel  string
	Detail      string
}
