package agentadaptor

// ModelInfo is a model option visible through an adapter. ID is the value
// accepted by config; Label is display text for UIs.
type ModelInfo struct {
	ID    string
	Label string
}

// DetectedModel reports the effective model inferred from config/profile
// state. Source names where the decision came from, such as explicit config or
// provider config file; Candidates records fallback values considered.
type DetectedModel struct {
	Model      string
	Provider   string
	Source     string
	Candidates []string
}

// AgentProfileSource identifies where an adapter's effective local profile
// directory came from.
type AgentProfileSource string

const (
	// AgentProfileSourceBindingEnv means an explicit env binding selected the profile.
	AgentProfileSourceBindingEnv AgentProfileSource = "binding_env"
	// AgentProfileSourceProfileOption means a profile AgentOption selected the profile.
	AgentProfileSourceProfileOption AgentProfileSource = "profile_option"
	// AgentProfileSourceProcessEnv means a process environment variable selected the profile.
	AgentProfileSourceProcessEnv AgentProfileSource = "process_env"
	// AgentProfileSourceDefault means the adapter used its native default path.
	AgentProfileSourceDefault AgentProfileSource = "default"
	// AgentProfileSourceManaged means the SDK/adapter synthesized a managed profile.
	AgentProfileSourceManaged AgentProfileSource = "managed"
	// AgentProfileSourceUnsupported means the adapter has no profile semantics.
	AgentProfileSourceUnsupported AgentProfileSource = "unsupported"
)

// AgentProfile reports the effective local operator profile directory for one
// bound adapter as seen by the SDK control plane.
//
// Dir is the effective directory the built-in adapter will inspect or use for
// local profile semantics. Source tells whether that directory came from an
// explicit CommonConfig.Env override, profile option, process
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

// EnvironmentStatus summarizes the highest-severity environment check result.
type EnvironmentStatus string

const (
	// EnvironmentPass means all checks passed.
	EnvironmentPass EnvironmentStatus = "pass"
	// EnvironmentWarn means the adapter may run but host attention is useful.
	EnvironmentWarn EnvironmentStatus = "warn"
	// EnvironmentFail means the adapter is not ready to run.
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

// WorkspaceStrategyType names the host/workspace provisioning strategy.
type WorkspaceStrategyType string

// WorkspaceMode names the isolation/reuse semantics of a workspace lease.
type WorkspaceMode string

// WorkspaceReleaseMode tells a WorkspaceManager what to do after the run.
type WorkspaceReleaseMode string

const (
	// WorkspaceStrategyProjectPrimary uses the existing project directory.
	WorkspaceStrategyProjectPrimary WorkspaceStrategyType = "project_primary"
	// WorkspaceStrategyGitWorktree provisions a separate git worktree.
	WorkspaceStrategyGitWorktree WorkspaceStrategyType = "git_worktree"
	// WorkspaceStrategyAdapterManaged delegates workspace selection to the adapter.
	WorkspaceStrategyAdapterManaged WorkspaceStrategyType = "adapter_managed"
	// WorkspaceStrategyCloudSandbox represents an externally managed sandbox.
	WorkspaceStrategyCloudSandbox WorkspaceStrategyType = "cloud_sandbox"
)

const (
	// WorkspaceModeShared reuses the project workspace directly.
	WorkspaceModeShared WorkspaceMode = "shared_workspace"
	// WorkspaceModeIsolated uses a run-scoped isolated workspace.
	WorkspaceModeIsolated WorkspaceMode = "isolated_workspace"
	// WorkspaceModeOperator uses an operator-owned branch/workspace.
	WorkspaceModeOperator WorkspaceMode = "operator_branch"
	// WorkspaceModeReuse asks the manager to reuse an existing workspace.
	WorkspaceModeReuse WorkspaceMode = "reuse_existing"
	// WorkspaceModeAgentDefault lets the adapter choose its default workspace.
	WorkspaceModeAgentDefault WorkspaceMode = "agent_default"
)

const (
	// WorkspaceReleaseKeep leaves the workspace available after the run.
	WorkspaceReleaseKeep WorkspaceReleaseMode = "keep"
	// WorkspaceReleaseStop asks the manager to tear down run-scoped resources.
	WorkspaceReleaseStop WorkspaceReleaseMode = "stop"
)

// WorkspaceStrategy describes how a host would like a workspace provisioned.
// It is primarily carried through CommonConfig for adapter/default plumbing;
// hosts usually use concrete WorkspaceSpec values with WithWorkspace.
type WorkspaceStrategy struct {
	Type              WorkspaceStrategyType
	BaseRef           string
	BranchTemplate    string
	WorktreeParentDir string
}

// WorkspaceRuntimeConfig declares runtime services associated with a workspace
// or binding. It is resolved by RuntimeServiceManager before adapter launch.
type WorkspaceRuntimeConfig struct {
	Services []RuntimeServiceSpec
}

// WorkspaceLease is the concrete working directory and metadata returned by a
// WorkspaceManager. Adapters should use CWD as the process working directory
// and treat Fingerprint as part of resume compatibility.
type WorkspaceLease struct {
	ID             string
	Mode           WorkspaceMode
	StrategyType   WorkspaceStrategyType
	CWD            string
	Fingerprint    string
	Metadata       map[string]string
	InstructionsID string
}

// WorkspaceRequest is passed to WorkspaceManager.Resolve after SDK defaults
// and per-run workspace options have been merged.
type WorkspaceRequest struct {
	BaseCWD  string
	Spec     WorkspaceSpec
	Metadata map[string]string
}

// WorkspaceSpec is implemented by host-facing workspace request values. The
// unexported method keeps the set of built-in specs closed for now while still
// letting hosts choose between them.
type WorkspaceSpec interface {
	workspaceRequest() WorkspaceRequestData
}

// WorkspaceRequestData is the normalized internal form of a WorkspaceSpec.
type WorkspaceRequestData struct {
	Mode           WorkspaceMode
	StrategyType   WorkspaceStrategyType
	BaseRef        string
	BranchTemplate string
	ParentDir      string
}

// GitWorktreeWorkspace requests an isolated git worktree for the run.
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

// SharedWorkspace requests direct reuse of the project workspace.
type SharedWorkspace struct{}

func (SharedWorkspace) workspaceRequest() WorkspaceRequestData {
	return WorkspaceRequestData{
		Mode:         WorkspaceModeShared,
		StrategyType: WorkspaceStrategyProjectPrimary,
	}
}

// AdapterManagedWorkspace lets the adapter/provider choose or create its own
// workspace according to native behavior.
type AdapterManagedWorkspace struct{}

func (AdapterManagedWorkspace) workspaceRequest() WorkspaceRequestData {
	return WorkspaceRequestData{
		Mode:         WorkspaceModeAgentDefault,
		StrategyType: WorkspaceStrategyAdapterManaged,
	}
}

// RuntimeServiceSpec declares one service a run may need. Hosts can describe
// either an already-known endpoint (URL) or a command/port the runtime manager
// should start before invoking the adapter.
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

// RuntimeServiceRequest is passed to RuntimeServiceManager.Ensure with the
// run identity, resolved workspace, agent identity, and desired services.
type RuntimeServiceRequest struct {
	RunID      string
	DriverType string
	Agent      AgentIdentity
	Config     any
	Workspace  WorkspaceLease
	Desired    []RuntimeServiceSpec
	Metadata   map[string]string
}

// RuntimeServiceRef is a concrete service endpoint returned by the runtime
// manager and passed to adapters. URL is the primary connection string hosts
// expect agents to use.
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

// RuntimePayload is the runtime-service equivalent of ResolvedSkills.
//
// Requested contains the desired runtime services declared by binding defaults,
// config defaults, or per-run overrides. Ensured contains the concrete service
// endpoints returned by RuntimeServiceManager.Ensure.
type RuntimePayload struct {
	Requested   []RuntimeServiceSpec
	Ensured     []RuntimeServiceRef
	Fingerprint string
}

// RuntimeServiceStatus is the lifecycle state of a runtime service process.
type RuntimeServiceStatus string

// RuntimeServiceLifecycle describes who owns service cleanup.
type RuntimeServiceLifecycle string

// RuntimeServiceHealth describes the observed service health when known.
type RuntimeServiceHealth string

const (
	// RuntimeServiceStarting means the service is being prepared.
	RuntimeServiceStarting RuntimeServiceStatus = "starting"
	// RuntimeServiceRunning means the service is ready or already available.
	RuntimeServiceRunning RuntimeServiceStatus = "running"
	// RuntimeServiceStopped means the service has been stopped.
	RuntimeServiceStopped RuntimeServiceStatus = "stopped"
	// RuntimeServiceFailed means preparation or health checking failed.
	RuntimeServiceFailed RuntimeServiceStatus = "failed"
)

const (
	// RuntimeLifecycleShared means the host owns service lifetime beyond one run.
	RuntimeLifecycleShared RuntimeServiceLifecycle = "shared"
	// RuntimeLifecycleEphemeral means the service is scoped to one run.
	RuntimeLifecycleEphemeral RuntimeServiceLifecycle = "ephemeral"
)

const (
	// RuntimeHealthUnknown means no health signal was observed.
	RuntimeHealthUnknown RuntimeServiceHealth = "unknown"
	// RuntimeHealthHealthy means the service passed health checks.
	RuntimeHealthHealthy RuntimeServiceHealth = "healthy"
	// RuntimeHealthUnhealthy means the service failed health checks.
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
