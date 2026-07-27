package adaptor

import (
	"context"
	"errors"
	"maps"
	"sync"

	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/engine"
)

// This file is the P4.7 run-scoped environment pipeline: workspace leases,
// runtime services, and the generic run-service mount point that lets an
// ecosystem package (delegation, a database sidecar, a browser pool, ...)
// attach a per-run endpoint to the agent without the root package knowing
// anything about it.
//
// Ordering is the legacy engine order (execute.go): workspace Resolve → run ID
// → runtime Ensure → provider AttachRun → MCP assembly → driver dispatch, and
// the exact reverse on the way out. Every semantic step routes through an
// engine entry so next/ and the legacy Core produce identical leases,
// fingerprints, and normalized refs.

// ============ Host-facing contracts ============

type (
	// ServiceSpec declares one runtime service a run needs — an
	// already-known endpoint (URL) or a command/port a ServiceManager
	// starts before the driver launches.
	ServiceSpec = driver.RuntimeServiceSpec
	// ServiceRef is a concrete runtime service endpoint. Its MCP field is
	// the typed way a service publishes an MCP server into the run, and its
	// SecretEnv field is the subprocess-only channel for run-scoped secrets
	// (bearer tokens) that never reach public reports.
	ServiceRef = driver.RuntimeServiceRef
	// WorkspaceLease is the concrete working directory a manager returns.
	WorkspaceLease = driver.WorkspaceLease
)

// ServiceRequest is the immutable run envelope passed to
// [ServiceManager.Ensure]. Slice and map fields are owned by the SDK for the
// duration of the call; managers must copy values they retain afterwards.
type ServiceRequest struct {
	RunID      string
	DriverType string
	Agent      Identity
	Config     any
	Workspace  WorkspaceLease
	Desired    []ServiceSpec
	Metadata   map[string]string
}

// ServiceManager is the host hook that starts or locates runtime services.
// Install it with [WithServiceManager]. Implementations must be safe for
// concurrent runs of one Agent.
type ServiceManager interface {
	Ensure(ctx context.Context, req ServiceRequest) ([]ServiceRef, error)
	ReleaseByRun(ctx context.Context, runID string) error
	ReleaseByLabels(ctx context.Context, labels map[string]string) error
}

// WorkspaceSpec is the closed set of workspace provisioning requests:
// [SharedWorkspace], [GitWorktreeWorkspace], and [AdapterManagedWorkspace].
type WorkspaceSpec interface {
	workspaceRequest() workspaceRequestData
}

type workspaceRequestData struct {
	mode           driver.WorkspaceMode
	strategyType   driver.WorkspaceStrategyType
	baseRef        string
	branchTemplate string
	parentDir      string
}

// SharedWorkspace requests direct reuse of the project workspace.
type SharedWorkspace struct{}

func (SharedWorkspace) workspaceRequest() workspaceRequestData {
	return workspaceRequestData{
		mode:         driver.WorkspaceModeShared,
		strategyType: driver.WorkspaceStrategyProjectPrimary,
	}
}

// GitWorktreeWorkspace requests an isolated git worktree for the run.
type GitWorktreeWorkspace struct {
	BaseRef           string
	BranchTemplate    string
	WorktreeParentDir string
}

func (w GitWorktreeWorkspace) workspaceRequest() workspaceRequestData {
	return workspaceRequestData{
		mode:           driver.WorkspaceModeIsolated,
		strategyType:   driver.WorkspaceStrategyGitWorktree,
		baseRef:        w.BaseRef,
		branchTemplate: w.BranchTemplate,
		parentDir:      w.WorktreeParentDir,
	}
}

// AdapterManagedWorkspace lets the driver choose or create its own
// workspace according to its native behavior.
type AdapterManagedWorkspace struct{}

func (AdapterManagedWorkspace) workspaceRequest() workspaceRequestData {
	return workspaceRequestData{
		mode:         driver.WorkspaceModeAgentDefault,
		strategyType: driver.WorkspaceStrategyAdapterManaged,
	}
}

// WorkspaceRequest is passed to [WorkspaceManager.Resolve] after agent
// defaults and per-call workspace options have been merged.
type WorkspaceRequest struct {
	BaseCWD  string
	Spec     WorkspaceSpec
	Metadata map[string]string
}

// WorkspaceReleaseMode tells a WorkspaceManager what to do after a run.
type WorkspaceReleaseMode string

// WorkspaceManager is the host hook that turns a [WorkspaceSpec] into a
// concrete lease. Install it with [WithWorkspaceManager]. Implementations must
// be safe for concurrent runs of one Agent.
type WorkspaceManager interface {
	Resolve(ctx context.Context, req WorkspaceRequest) (WorkspaceLease, error)
	Release(ctx context.Context, lease WorkspaceLease, mode WorkspaceReleaseMode) error
}

const (
	// WorkspaceReleaseKeep leaves the workspace available after the run.
	WorkspaceReleaseKeep WorkspaceReleaseMode = "keep"
	// WorkspaceReleaseStop asks the manager to tear down run-scoped state.
	WorkspaceReleaseStop WorkspaceReleaseMode = "stop"
)

// workspaceManagerAdapter is the only place the public workspace contract
// crosses into the migration engine. Keeping the conversion private prevents
// internal types from becoming part of the v1 API while preserving the
// engine's established passthrough and release semantics.
type workspaceManagerAdapter struct{ target WorkspaceManager }

func (a workspaceManagerAdapter) Resolve(ctx context.Context, req engine.WorkspaceRequest) (driver.WorkspaceLease, error) {
	return a.target.Resolve(ctx, workspaceRequestFromEngine(req))
}

func (a workspaceManagerAdapter) Release(ctx context.Context, lease driver.WorkspaceLease, mode engine.WorkspaceReleaseMode) error {
	return a.target.Release(ctx, lease, WorkspaceReleaseMode(mode))
}

func workspaceManagerToEngine(manager WorkspaceManager) engine.WorkspaceManager {
	if manager == nil {
		return nil
	}
	return workspaceManagerAdapter{target: manager}
}

func workspaceRequestToEngine(req WorkspaceRequest) engine.WorkspaceRequest {
	return engine.WorkspaceRequest{
		BaseCWD:  req.BaseCWD,
		Spec:     workspaceSpecToEngine(req.Spec),
		Metadata: maps.Clone(req.Metadata),
	}
}

func workspaceRequestFromEngine(req engine.WorkspaceRequest) WorkspaceRequest {
	return WorkspaceRequest{
		BaseCWD:  req.BaseCWD,
		Spec:     workspaceSpecFromEngine(req.Spec),
		Metadata: maps.Clone(req.Metadata),
	}
}

func workspaceSpecToEngine(spec WorkspaceSpec) engine.WorkspaceSpec {
	if spec == nil {
		return nil
	}
	switch value := spec.(type) {
	case *GitWorktreeWorkspace:
		if value == nil {
			return nil
		}
		spec = *value
	case *SharedWorkspace:
		if value == nil {
			return nil
		}
		spec = *value
	case *AdapterManagedWorkspace:
		if value == nil {
			return nil
		}
		spec = *value
	}
	data := spec.workspaceRequest()
	switch data.strategyType {
	case driver.WorkspaceStrategyGitWorktree:
		return engine.GitWorktreeWorkspace{
			BaseRef:           data.baseRef,
			BranchTemplate:    data.branchTemplate,
			WorktreeParentDir: data.parentDir,
		}
	case driver.WorkspaceStrategyAdapterManaged:
		return engine.AdapterManagedWorkspace{}
	default:
		return engine.SharedWorkspace{}
	}
}

func workspaceSpecFromEngine(spec engine.WorkspaceSpec) WorkspaceSpec {
	switch value := spec.(type) {
	case nil:
		return nil
	case engine.GitWorktreeWorkspace:
		return GitWorktreeWorkspace{
			BaseRef:           value.BaseRef,
			BranchTemplate:    value.BranchTemplate,
			WorktreeParentDir: value.WorktreeParentDir,
		}
	case engine.SharedWorkspace:
		return SharedWorkspace{}
	case engine.AdapterManagedWorkspace:
		return AdapterManagedWorkspace{}
	default:
		// WorkspaceSpec is closed in the engine. A defensive nil keeps an
		// unexpected future engine type from being misrepresented publicly.
		return nil
	}
}

type serviceManagerAdapter struct{ target ServiceManager }

func (a serviceManagerAdapter) Ensure(ctx context.Context, req engine.RuntimeServiceRequest) ([]driver.RuntimeServiceRef, error) {
	return a.target.Ensure(ctx, serviceRequestFromEngine(req))
}

func (a serviceManagerAdapter) ReleaseByRun(ctx context.Context, runID string) error {
	return a.target.ReleaseByRun(ctx, runID)
}

func (a serviceManagerAdapter) ReleaseByLabels(ctx context.Context, labels map[string]string) error {
	return a.target.ReleaseByLabels(ctx, maps.Clone(labels))
}

func serviceManagerToEngine(manager ServiceManager) engine.RuntimeServiceManager {
	if manager == nil {
		return nil
	}
	return serviceManagerAdapter{target: manager}
}

func serviceRequestToEngine(req ServiceRequest) engine.RuntimeServiceRequest {
	return engine.RuntimeServiceRequest{
		RunID:      req.RunID,
		DriverType: req.DriverType,
		Agent:      req.Agent.driverIdentity(),
		Config:     req.Config,
		Workspace:  req.Workspace,
		Desired:    engine.CloneRuntimeServiceSpecs(req.Desired),
		Metadata:   maps.Clone(req.Metadata),
	}
}

func serviceRequestFromEngine(req engine.RuntimeServiceRequest) ServiceRequest {
	return ServiceRequest{
		RunID:      req.RunID,
		DriverType: req.DriverType,
		Agent:      identityFromDriver(req.Agent),
		Config:     req.Config,
		Workspace:  req.Workspace,
		Desired:    engine.CloneRuntimeServiceSpecs(req.Desired),
		Metadata:   maps.Clone(req.Metadata),
	}
}

// ============ The generic run-scoped service mount point ============

// RunServiceProvider is the extension point ecosystem packages implement to
// bind a live, run-scoped service to every invocation of an Agent. It is the
// mechanism behind delegation.Service.Option(): the root package never learns
// the word "team" — it only knows that something asked to be attached to this
// run, and that whatever it returns must reach the driver and the event
// stream.
//
// Lifecycle, per run:
//
//	AttachRun(ctx, runID)  — after the run ID is minted, before anything is
//	                         resolved or dispatched. Returning an error is a
//	                         pre-launch failure: the driver never starts, the
//	                         error surfaces through Result(), and every
//	                         provider already attached for this run is
//	                         detached again.
//	  ... the run ...
//	DetachRun(ctx, runID)  — after the run's event channel is closed. The ctx
//	                         is cancellation-detached (context.WithoutCancel),
//	                         so teardown still happens for cancelled and
//	                         timed-out runs. Errors are ignored: the run's
//	                         outcome is already decided.
//
// Implementations must be safe for concurrent runs of the same Agent: AttachRun
// and DetachRun are keyed by run ID and may overlap.
type RunServiceProvider interface {
	// AttachRun binds the provider's service to one run.
	AttachRun(ctx context.Context, runID string) (RunAttachment, error)
	// DetachRun releases everything AttachRun bound to the run.
	DetachRun(ctx context.Context, runID string) error
}

// RunAttachment is what one provider contributes to one run.
//
// Services are merged into the run's runtime payload and — for every ref
// carrying a typed MCP field — appended to the driver's MCP server set. The
// host's own WithMCP declaration is preserved: attachment servers are added
// alongside it, never in place of it. A ref's URL, MCP.BearerTokenEnvVar, and
// SecretEnv together are how a per-run endpoint publishes an authenticated MCP
// server without the token ever entering a public report: SecretEnv reaches the
// driver process environment only.
//
// Events, when non-nil, is folded straight into the run's single event channel,
// interleaved with the driver's own events — there is no second stream to merge
// and no wrapper goroutine on the driver's hot path.
type RunAttachment struct {
	// Services are the concrete endpoints this provider ensured for the run.
	Services []ServiceRef
	// Events optionally streams provider-side events into the run.
	Events RunEventSource
}

// RunEventSource subscribes to one run's provider-side events, already
// projected onto the SDK event vocabulary (delegation providers emit
// SubagentUpdate).
//
// Contract: the returned channel must be closed once ctx is done, and every
// event already published for the run must be delivered before that close. The
// SDK drains the channel to closure before it closes the run's event channel,
// so a source that abandons its tail on cancellation clips terminal events —
// flush first, then close. A nil channel is treated as "no events".
type RunEventSource func(ctx context.Context, runID string) <-chan Event

// ============ Per-run resource acquisition and release ============

// runResources is the live environment of one run: the workspace lease, the
// ensured runtime services, the attached providers, and the goroutines pumping
// provider events into the run's event channel. A nil *runResources is a valid
// "nothing to do" value so the execution paths need no special-casing.
type runResources struct {
	runID    string
	identity driver.AgentIdentity

	workspaceManager WorkspaceManager
	workspace        WorkspaceLease
	workspaceLeased  bool

	serviceManager ServiceManager
	runtime        driver.RuntimePayload
	runtimeEnsured bool

	attached []RunServiceProvider

	pumpCancel context.CancelFunc
	pumpDone   chan struct{}
}

// acquireRun resolves the run's environment in legacy engine order and returns
// it ready for request assembly. Every failure is a pre-launch failure and
// unwinds whatever was already acquired.
func (a *Agent) acquireRun(ctx context.Context, runID string, eff *RunSettings, sink *eventSink) (*runResources, error) {
	needsWorkspace := a.defaults.workspaceManager != nil || eff.workspaceSpec != nil
	needsRuntime := len(eff.services) > 0
	providers := eff.runServices
	if !needsWorkspace && !needsRuntime && len(providers) == 0 {
		// Zero-cost path: an agent that uses none of this behaves exactly
		// as it did before P4.7, down to the untouched request fields.
		return nil, nil
	}

	var publicIdentity Identity
	var identity driver.AgentIdentity
	if eff.identity != nil {
		publicIdentity = *eff.identity
		identity = eff.identity.driverIdentity()
	}
	r := &runResources{
		runID:            runID,
		identity:         identity,
		workspaceManager: a.defaults.workspaceManager,
		serviceManager:   a.defaults.serviceManager,
	}

	// 1. Workspace lease. Only when the host actually asked for managed
	// workspaces — otherwise WithWorkspace(dir) keeps its direct lease
	// synthesis and an agent with no workspace at all keeps an empty one.
	if needsWorkspace {
		workspaceReq := WorkspaceRequest{
			BaseCWD:  eff.workspace,
			Spec:     eff.workspaceSpec,
			Metadata: maps.Clone(eff.metadata),
		}
		lease, err := engine.ResolveWorkspaceLease(
			ctx,
			workspaceManagerToEngine(r.workspaceManager),
			workspaceRequestToEngine(workspaceReq),
		)
		if err != nil {
			return nil, err
		}
		r.workspace, r.workspaceLeased = lease, true
	}

	// 2. Runtime services declared with WithServices, ensured through the
	// installed ServiceManager (nil manager = the legacy noop: declared but
	// unmanaged services do not invent endpoints).
	if needsRuntime || len(providers) > 0 {
		serviceReq := ServiceRequest{
			RunID:      runID,
			DriverType: a.driver.Descriptor().Type,
			Agent:      publicIdentity,
			Workspace:  r.workspace,
			Metadata:   maps.Clone(eff.metadata),
		}
		payload, err := engine.PrepareRuntimePayload(
			ctx,
			serviceManagerToEngine(r.serviceManager),
			serviceRequestToEngine(serviceReq),
			eff.services,
		)
		if err != nil {
			r.release(ctx)
			return nil, err
		}
		r.runtime, r.runtimeEnsured = payload, needsRuntime
	}

	// 3. Provider attachments. The refs join the runtime payload in the
	// same normalized shape a ServiceManager's would.
	var sources []RunEventSource
	for _, p := range providers {
		att, err := p.AttachRun(ctx, runID)
		if err != nil {
			r.release(ctx)
			return nil, err
		}
		r.attached = append(r.attached, p)
		if len(att.Services) > 0 {
			// Secrets are harvested from the raw refs first and normalization
			// runs second — the same order as the legacy engine path, and for
			// the same reason: the normalized refs deliberately carry no
			// SecretEnv, so the payload-level slice is the only carrier that
			// reaches driver env.
			r.runtime.SecretEnv = append(r.runtime.SecretEnv, engine.CollectRuntimeSecretEnv(att.Services)...)
			r.runtime.Ensured = append(r.runtime.Ensured, engine.NormalizeRuntimeServiceRefs(nil, att.Services, identity)...)
		}
		if att.Events != nil {
			sources = append(sources, att.Events)
		}
	}

	// 4. Event pumps start before the driver does, so no provider event
	// published during the run can be missed.
	r.startPumps(ctx, sink, sources)
	return r, nil
}

// startPumps subscribes every event source and folds it into the run's single
// event channel. The subscription context is detached from the caller's so the
// SDK — not an upstream cancellation — decides when the sources stop; that is
// what keeps terminal provider events from being clipped when a run is
// cancelled or times out.
func (r *runResources) startPumps(ctx context.Context, sink *eventSink, sources []RunEventSource) {
	if len(sources) == 0 || sink == nil {
		return
	}
	pumpCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	done := make(chan struct{})
	r.pumpCancel, r.pumpDone = cancel, done

	var wg sync.WaitGroup
	for _, src := range sources {
		ch := src(pumpCtx, r.runID)
		if ch == nil {
			continue
		}
		wg.Add(1)
		go func(ch <-chan Event) {
			defer wg.Done()
			for ev := range ch {
				sink.push(ev)
			}
		}(ch)
	}
	go func() {
		wg.Wait()
		close(done)
	}()
}

// stopPumps ends the provider subscriptions and waits for every pumped event to
// have been pushed. It returns only once the sources are drained to closure, so
// the caller may close the event channel without losing a tail.
func (r *runResources) stopPumps() {
	if r == nil || r.pumpCancel == nil {
		return
	}
	r.pumpCancel()
	<-r.pumpDone
	r.pumpCancel = nil
}

// runtimeRefs returns the ensured service refs of this run — manager-ensured
// first, provider attachments after — for MCP payload assembly.
func (r *runResources) runtimeRefs() []ServiceRef {
	if r == nil {
		return nil
	}
	return r.runtime.Ensured
}

// applyRequest overlays the resolved environment onto the driver request. It
// runs after buildRequest so a managed workspace lease supersedes the direct
// WithWorkspace(dir) synthesis.
func (r *runResources) applyRequest(req *driver.Request) {
	if r == nil {
		return
	}
	if r.workspaceLeased {
		req.Workspace = r.workspace
	}
	if r.runtimeEnsured || len(r.runtime.Ensured) > 0 {
		req.Runtime = driver.RuntimePayload{
			Requested:   engine.CloneRuntimeServiceSpecs(r.runtime.Requested),
			Ensured:     engine.CloneRuntimeServiceRefs(r.runtime.Ensured),
			SecretEnv:   engine.CloneEnvBindings(r.runtime.SecretEnv),
			Fingerprint: r.runtime.Fingerprint,
		}
	}
}

// backfillServices merges SDK-observed ensured services with driver-observed
// reports by stable service ID. Driver observations win field-by-field for a
// matching ID, missing driver fields are filled from the SDK observation, and
// reports unique to either side are retained. An empty ID is never used for
// deduplication because doing so would conflate unrelated services.
func (r *runResources) backfillServices(res *Result) {
	if r == nil || res == nil || len(r.runtime.Ensured) == 0 {
		return
	}
	ensured := engine.RuntimeReportsFromRefs(r.runtime.Ensured, r.identity)
	res.services = mergeServiceReports(ensured, res.services)
}

func mergeServiceReports(ensured, observed []ServiceReport) []ServiceReport {
	ensuredByID := make(map[string]ServiceReport, len(ensured))
	for _, report := range ensured {
		if report.ID != "" {
			ensuredByID[report.ID] = report
		}
	}

	merged := make([]ServiceReport, 0, len(ensured)+len(observed))
	seen := make(map[string]struct{}, len(ensured)+len(observed))
	for _, report := range observed {
		if report.ID == "" {
			merged = append(merged, cloneServiceReport(report))
			continue
		}
		if _, duplicate := seen[report.ID]; duplicate {
			for i := range merged {
				if merged[i].ID == report.ID {
					merged[i] = mergeServiceReport(merged[i], report)
					break
				}
			}
			continue
		}
		base, ok := ensuredByID[report.ID]
		if ok {
			report = mergeServiceReport(base, report)
		} else {
			report = cloneServiceReport(report)
		}
		merged = append(merged, report)
		seen[report.ID] = struct{}{}
	}
	for _, report := range ensured {
		if report.ID != "" {
			if _, ok := seen[report.ID]; ok {
				continue
			}
			seen[report.ID] = struct{}{}
		}
		merged = append(merged, cloneServiceReport(report))
	}
	return merged
}

func mergeServiceReport(base, override ServiceReport) ServiceReport {
	merged := cloneServiceReport(base)
	if override.ID != "" {
		merged.ID = override.ID
	}
	if override.Name != "" {
		merged.Name = override.Name
	}
	if override.URL != "" {
		merged.URL = override.URL
	}
	if override.Status != "" {
		merged.Status = override.Status
	}
	if override.Lifecycle != "" {
		merged.Lifecycle = override.Lifecycle
	}
	if override.ReuseKey != "" {
		merged.ReuseKey = override.ReuseKey
	}
	if override.Command != "" {
		merged.Command = override.Command
	}
	if override.CWD != "" {
		merged.CWD = override.CWD
	}
	if override.Port != 0 {
		merged.Port = override.Port
	}
	if override.OwnerAgentID != "" {
		merged.OwnerAgentID = override.OwnerAgentID
	}
	if override.Health != "" {
		merged.Health = override.Health
	}
	if len(override.Metadata) > 0 {
		if merged.Metadata == nil {
			merged.Metadata = map[string]string{}
		}
		for key, value := range override.Metadata {
			merged.Metadata[key] = value
		}
	}
	return merged
}

func cloneServiceReport(report ServiceReport) ServiceReport {
	report.Metadata = maps.Clone(report.Metadata)
	return report
}

// backfillRunServices applies the ensured-refs fallback to whichever Result the
// run produced — the success value, or the one carried by a *RunError.
func backfillRunServices(r *runResources, res *Result, err error) {
	if r == nil {
		return
	}
	r.backfillServices(res)
	var runErr *RunError
	if errors.As(err, &runErr) {
		r.backfillServices(runErr.Result)
	}
}

// finish is the single teardown sequence, in the order the contract demands:
// drain the provider event sources, close the run's event channel, then release
// providers, runtime services, and the workspace lease. Closing the channel
// after the drain is what guarantees a terminal SubagentUpdate is delivered;
// detaching after the close is what guarantees a consumer that saw the channel
// end can rely on the sidecar being gone.
//
// The release half runs on a cancellation-detached context so a cancelled or
// timed-out run still tears its environment down.
func (r *runResources) finish(ctx context.Context, sink *eventSink) {
	r.stopPumps()
	if sink != nil {
		sink.close()
	}
	if r == nil {
		return
	}
	r.release(ctx)
}

// release detaches providers and releases managed resources in reverse
// acquisition order. It is also the unwind path for a failed acquireRun.
func (r *runResources) release(ctx context.Context) {
	if r == nil {
		return
	}
	relCtx := context.WithoutCancel(ctx)
	for i := len(r.attached) - 1; i >= 0; i-- {
		// Teardown errors are deliberately dropped: the run's outcome is
		// already decided, and a failed detach must not mask it.
		_ = r.attached[i].DetachRun(relCtx, r.runID)
	}
	r.attached = nil
	if r.runtimeEnsured {
		_ = engine.ReleaseRuntimeServicesByRun(relCtx, serviceManagerToEngine(r.serviceManager), r.runID)
		r.runtimeEnsured = false
	}
	if r.workspaceLeased {
		_ = engine.ReleaseWorkspaceLease(
			relCtx,
			workspaceManagerToEngine(r.workspaceManager),
			r.workspace,
			engine.WorkspaceReleaseMode(WorkspaceReleaseStop),
		)
		r.workspaceLeased = false
	}
}
