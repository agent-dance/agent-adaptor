package agentadaptor

import "context"

// RunResult is the normalized outcome returned by Runner.Run or RunHandle.Wait.
//
// Output layering rules (see docs/workstream-transcript-contract.md):
//   - Output: final assistant-facing text only; empty is valid when the
//     adapter never produced assistant text.
//   - RawStreams: raw stdout/stderr for auditing and debugging.
//   - Transcript: structured semantic entries produced by the adapter from its
//     protocol events. Must equal the sequence of RunEventItem items collected
//     by Seq order.
//   - Summary: short host-facing label; never equals Output and must come from
//     a terminal result event (or adapter-generated short text).
//   - Result: raw JSON payload of the terminal result event when the adapter
//     protocol defines one. May be nil.
type RunResult struct {
	RunID      string
	DriverType string
	// Output is final assistant-facing text only. It must not contain raw
	// stdout/stderr dumps, Summary text, or provider terminal JSON.
	Output string
	// RawStreams carries complete raw stdout/stderr for audit and debugging.
	RawStreams *RawStreams
	// Transcript is the normalized semantic item stream parsed by the adapter.
	Transcript []TranscriptItem
	ExitCode   int
	Signal     string
	TimedOut   bool
	Usage      *Usage
	// Session is populated when a valid checkpoint was produced and persisted
	// (or when a stateless/no-store run can still report the provider handle).
	Session  *SessionRef
	Metadata map[string]string
	Provider string
	Biller   string
	Model    string
	// BillingType and CostUSD are best-effort adapter metadata for hosts that
	// want to render cost/audit summaries; absence does not imply free usage.
	BillingType string
	CostUSD     *float64
	// Summary is a short host-facing label suitable for lists, logs, or issue
	// comments. It is deliberately separate from Output.
	Summary string
	// Result is the adapter-recognized terminal result event payload. Hosts
	// should treat it as provider-specific audit data, not portable text.
	Result           map[string]any
	StructuredOutput *StructuredOutput
	RuntimeServices  []RuntimeServiceReport
	Question         *RunQuestion
	// Failure is a structured business-level failure from a completed run.
	// Check Wait's returned error first, then Failure, then success.
	Failure *RunFailure
}

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
	// lost. See docs/v0.5.0-host-integration-plan.md §A3 for the
	// design rationale and why a host-driven Reconcile(aliveRunIDs)
	// was deliberately NOT added.
	ReleaseByLabels(ctx context.Context, labels map[string]string) error
}

type resolvedInvocation struct {
	runID          string
	prompt         string
	adapter        DriverAdapter
	config         any
	agent          AgentIdentity
	workspace      WorkspaceLease
	runtime        RuntimePayload
	skills         ResolvedSkills
	mcp            MCPPayload
	profilePayload ProfilePayload
	profile        *ProfileSelection
	policy         RunPolicy
	handlers       decisionHandlers
	instructions   *InstructionsBundleRef
	session        SessionRequest
	metadata       map[string]string
	outputSchema   *OutputSchema
	outputSource   StructuredOutputSource
	fingerprint    string
	streaming      bool
	model          string
}
