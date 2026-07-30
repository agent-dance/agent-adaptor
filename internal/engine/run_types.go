package engine

import "context"

// WorkspaceManager is the host hook that turns a WorkspaceSpec into a concrete
// working directory lease. Implementations may return the project directory
// unchanged, create git worktrees, provision sandboxes, or delegate to an
// external workspace service. The SDK releases the lease after the run using
// the requested WorkspaceReleaseMode.
type WorkspaceManager interface {
	Resolve(ctx context.Context, req WorkspaceRequest) (WorkspaceLease, error)
	Release(ctx context.Context, lease WorkspaceLease, mode WorkspaceReleaseMode) error
}

// RuntimeServiceManager is the host hook for preparing services a run depends
// on, such as local dev servers, databases, or tool sidecars. The SDK only
// coordinates lifecycle; the host owns process/container orchestration.
type RuntimeServiceManager interface {
	// Ensure starts or locates the runtime services needed for one run and
	// returns concrete refs the adapter can inject into the prompt/profile.
	Ensure(ctx context.Context, req RuntimeServiceRequest) ([]RuntimeServiceRef, error)
	// ReleaseByRun releases services scoped to one SDK RunID. The SDK calls it
	// during normal cleanup for run-scoped services.
	ReleaseByRun(ctx context.Context, runID string) error

	// ReleaseByLabels releases every service whose Metadata contains
	// every key-value pair in labels. The semantics match a logical
	// AND across the labels map, NOT individual matches.
	//
	// An empty labels map releases nothing — callers must explicitly
	// opt-in to broad releases by passing at least one key-value
	// pair. This is a deliberate guard against accidental "release
	// everything you have" calls (compare ReleaseByRun, which always
	// scopes to one runID).
	//
	// Implementations should not error when no service matches; the
	// invariant after a successful call is "no service whose
	// Metadata covers labels remains running", which is trivially
	// satisfied by an empty match. Backend errors (Docker daemon
	// down, permission denied, etc.) propagate as-is.
	//
	// This method exists primarily to support host-side cleanup by
	// task / tenant / workspace label after a process restart, when
	// the runID-scoped index from a previous incarnation has been
	// lost. It is deliberately narrower than a host-driven global
	// Reconcile(aliveRunIDs) contract. See docs/api-reference.md §12.
	ReleaseByLabels(ctx context.Context, labels map[string]string) error
}
