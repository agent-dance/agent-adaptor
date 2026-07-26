package engine

// WorkspaceReleaseMode tells a WorkspaceManager what to do after the run.
type WorkspaceReleaseMode string

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
