package driver

// WorkspaceStrategyType names the host/workspace provisioning strategy.
type WorkspaceStrategyType string

// WorkspaceMode names the isolation/reuse semantics of a workspace lease.
type WorkspaceMode string

const (
	// WorkspaceStrategyProjectPrimary uses the existing project directory.
	WorkspaceStrategyProjectPrimary WorkspaceStrategyType = "project_primary"
	// WorkspaceStrategyGitWorktree provisions a separate git worktree.
	WorkspaceStrategyGitWorktree WorkspaceStrategyType = "git_worktree"
	// WorkspaceStrategyDriverManaged delegates workspace selection to the Driver.
	WorkspaceStrategyDriverManaged WorkspaceStrategyType = "driver_managed"
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
	// WorkspaceModeAgentDefault lets the driver choose its default workspace.
	WorkspaceModeAgentDefault WorkspaceMode = "agent_default"
)

// WorkspaceLease is the concrete working directory and metadata returned by a
// WorkspaceManager. Drivers should use CWD as the process working directory
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

// EnvBinding is one explicit environment variable override passed to a
// driver process or used during profile resolution.
type EnvBinding struct {
	Name  string
	Value string
}

// RuntimeServiceSpec declares one service a run may need. Hosts can describe
// either an already-known endpoint (URL) or a command/port the runtime manager
// should start before invoking the driver.
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

// RuntimeServiceRef is a concrete service endpoint returned by the runtime
// manager and passed to drivers. URL is the primary connection string hosts
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
	// MCP, when non-nil, declares the MCP server this runtime service exposes
	// to the run. Metadata is opaque and is never interpreted as an MCP
	// declaration. MCPServerSpec's consumer-facing alias is mcp.Server, so
	// hosts typically assign a value built with the mcp package constructors.
	// An empty Key defaults to the ref's Name (then ID); an empty URL/Command
	// defaults from the ref's URL/Command according to the transport.
	MCP      *MCPServerSpec
	Metadata map[string]string
	// SecretEnv carries subprocess-only runtime-issued secrets, such
	// as per-run MCP bearer tokens. The SDK strips these bindings from public
	// runtime refs/reports and injects them only into driver process env.
	SecretEnv []EnvBinding
}

// RuntimePayload is the runtime-service equivalent of ResolvedSkills.
//
// Requested contains the desired runtime services produced by Agent defaults,
// construction config, and CallOption overrides. Ensured contains the concrete
// service endpoints returned by the host ServiceManager.
type RuntimePayload struct {
	Requested   []RuntimeServiceSpec
	Ensured     []RuntimeServiceRef
	SecretEnv   []EnvBinding
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

// RuntimeServiceReport is the driver-facing execution report for one ensured
// runtime service. It records state actually observed during the invocation;
// an input declaration alone must not be reported as successful execution.
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
