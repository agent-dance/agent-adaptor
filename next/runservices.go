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

// ============ Host-facing aliases (engine truth, no engine import) ============

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
	// ServiceRequest is what a ServiceManager receives for one run.
	ServiceRequest = engine.RuntimeServiceRequest
	// ServiceManager is the host hook that starts/locates runtime services.
	// Install it with WithServiceManager.
	ServiceManager = engine.RuntimeServiceManager

	// WorkspaceSpec is a workspace provisioning request: SharedWorkspace,
	// GitWorktreeWorkspace, or AdapterManagedWorkspace.
	WorkspaceSpec = engine.WorkspaceSpec
	// SharedWorkspace requests direct reuse of the project workspace.
	SharedWorkspace = engine.SharedWorkspace
	// GitWorktreeWorkspace requests an isolated git worktree for the run.
	GitWorktreeWorkspace = engine.GitWorktreeWorkspace
	// AdapterManagedWorkspace lets the driver choose its own workspace.
	AdapterManagedWorkspace = engine.AdapterManagedWorkspace
	// WorkspaceRequest is what a WorkspaceManager receives for one run.
	WorkspaceRequest = engine.WorkspaceRequest
	// WorkspaceLease is the concrete working directory a manager returns.
	WorkspaceLease = driver.WorkspaceLease
	// WorkspaceReleaseMode tells a WorkspaceManager what to do after a run.
	WorkspaceReleaseMode = engine.WorkspaceReleaseMode
	// WorkspaceManager is the host hook that turns a WorkspaceSpec into a
	// concrete lease. Install it with WithWorkspaceManager.
	WorkspaceManager = engine.WorkspaceManager
)

const (
	// WorkspaceReleaseKeep leaves the workspace available after the run.
	WorkspaceReleaseKeep = engine.WorkspaceReleaseKeep
	// WorkspaceReleaseStop asks the manager to tear down run-scoped state.
	WorkspaceReleaseStop = engine.WorkspaceReleaseStop
)

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

	var identity driver.AgentIdentity
	if eff.identity != nil {
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
		lease, err := engine.ResolveWorkspaceLease(ctx, r.workspaceManager, WorkspaceRequest{
			BaseCWD:  eff.workspace,
			Spec:     eff.workspaceSpec,
			Metadata: maps.Clone(eff.metadata),
		})
		if err != nil {
			return nil, err
		}
		r.workspace, r.workspaceLeased = lease, true
	}

	// 2. Runtime services declared with WithServices, ensured through the
	// installed ServiceManager (nil manager = the legacy noop: declared but
	// unmanaged services do not invent endpoints).
	if needsRuntime || len(providers) > 0 {
		payload, err := engine.PrepareRuntimePayload(ctx, r.serviceManager, ServiceRequest{
			RunID:      runID,
			DriverType: a.driver.Descriptor().Type,
			Agent:      identity,
			Workspace:  r.workspace,
			Metadata:   maps.Clone(eff.metadata),
		}, eff.services)
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

// backfillServices makes Result.Services() honest for SDK-ensured services:
// when the driver reports none, the reports are derived from the ensured refs
// exactly like the legacy execute.go fallback. It never overrides a driver that
// reported its own.
func (r *runResources) backfillServices(res *Result) {
	if r == nil || res == nil || len(res.services) > 0 || len(r.runtime.Ensured) == 0 {
		return
	}
	res.services = engine.RuntimeReportsFromRefs(r.runtime.Ensured, r.identity)
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
		_ = engine.ReleaseRuntimeServicesByRun(relCtx, r.serviceManager, r.runID)
		r.runtimeEnsured = false
	}
	if r.workspaceLeased {
		_ = engine.ReleaseWorkspaceLease(relCtx, r.workspaceManager, r.workspace, WorkspaceReleaseStop)
		r.workspaceLeased = false
	}
}
