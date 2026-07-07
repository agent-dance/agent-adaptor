package agentadaptor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type runnerImpl struct {
	sdk       *sdkImpl
	name      string
	isDefault bool
	binding   AgentBinding
}

// decisionHandlers carries the per-Kind typed handlers resolved for one run
// (RunOption-level beats AgentOption-level; see resolveInvocation).
type decisionHandlers struct {
	Permission PermissionHandler
	PlanReview PlanReviewHandler
	Question   QuestionHandler
}

func (r *runnerImpl) Run(ctx context.Context, prompt string, opts ...RunOption) (RunResult, error) {
	// Even Run() needs a DecisionCapableSink so typed HITL handlers (mode B)
	// work in the blocking invocation style. Streaming is disabled so
	// StreamPayloads are discarded; the decision dispatcher still routes
	// per-Kind handlers and channel dispatch the same way Start() does.
	runBuf, streamBuf, policy := r.sdk.eventSinkSettings()
	sink := newDualSink("", false, runBuf, streamBuf, policy)
	// Drain the RunEvent channel in the background so Emit() never blocks.
	drainer := make(chan struct{})
	go func() {
		defer close(drainer)
		for range sink.runEvents {
		}
	}()
	// Drain DecisionRequests when unconsumed so channel dispatch (for Kinds
	// without a handler) unblocks via timeout rather than deadlocking on the
	// buffered send.
	go func() {
		for range sink.decisionRequests {
		}
	}()

	result, err := r.run(ctx, prompt, wrapWithSeq(sink), sink, opts...)
	sink.close()
	<-drainer

	if errors.Is(err, ErrSessionCheckpointMissing) {
		if f := sink.pendingFailure(); f != nil {
			if result.Failure == nil || (result.Failure.HumanDecision == nil && f.HumanDecision != nil) {
				result.Failure = cloneRunFailure(f)
			}
		}
		if shouldSkipSessionPersistOnFailure(result) {
			err = nil
		}
	}

	// Surface any HITL failure the runner stashed but the adapter did not
	// overlay onto its DriverRunResult.Failure. The same two-layer overlay
	// lives in asyncRunHandle.Wait so Run() and Start() produce identical
	// RunResult.Failure shapes.
	if f := sink.pendingFailure(); f != nil {
		if result.Failure == nil || (result.Failure.HumanDecision == nil && f.HumanDecision != nil) {
			result.Failure = cloneRunFailure(f)
		}
	}
	pendingFailuresByRunMu.Lock()
	delete(pendingFailuresByRun, sink)
	pendingFailuresByRunMu.Unlock()
	return result, err
}

func (r *runnerImpl) Start(ctx context.Context, prompt string, opts ...RunOption) (RunHandle, error) {
	runCtx, cancel := context.WithCancel(ctx)

	streaming := r.resolveStreamingForStart(opts...)
	runBuf, streamBuf, policy := r.sdk.eventSinkSettings()

	runID := newRunID(r.binding.Adapter().Descriptor().Type)

	sink := newDualSink(runID, streaming, runBuf, streamBuf, policy)
	handle := &asyncRunHandle{
		runID:          runID,
		events:         sink.runEvents,
		stream:         sink.stream,
		decisionReqsCh: sink.decisionRequests,
		sink:           sink,
		cancel:         cancel,
		done:           make(chan asyncRunResult, 1),
	}

	boundOpts := append([]RunOption{withPresetRunID(runID)}, opts...)

	go func() {
		result, err := r.run(runCtx, prompt, wrapWithSeq(sink), sink, boundOpts...)
		sink.close()
		handle.done <- asyncRunResult{result: result, err: err}
		close(handle.done)
	}()

	return handle, nil
}

func (r *runnerImpl) resolveStreamingForStart(opts ...RunOption) bool {
	var ro runOptions
	for _, opt := range opts {
		if opt != nil {
			opt(&ro)
		}
	}
	if ro.streaming != nil {
		return *ro.streaming
	}
	if r.binding == nil {
		return false
	}
	defaults := r.binding.Defaults()
	if defaults.Streaming != nil {
		return *defaults.Streaming
	}
	return false
}

// run is the single execution entry point used by both Run and Start. If
// decisionSink is non-nil the runner installs resolved typed handlers on it
// so adapter-side RequestDecision calls route through the correct typed
// handler.
func (r *runnerImpl) run(ctx context.Context, prompt string, sink EventSink, decisionSink *dualSink, opts ...RunOption) (RunResult, error) {
	invocation, cleanup, err := r.resolveInvocation(ctx, prompt, opts...)
	if cleanup != nil {
		defer cleanup()
	}
	if err != nil {
		return RunResult{}, err
	}

	if decisionSink != nil {
		decisionSink.bindRun(invocation.runID, invocation.policy.HumanDecision, invocation.handlers, invocation.adapter.Descriptor().RunPolicyCaps)
	}

	if err := r.validateDecisionCapabilities(invocation); err != nil {
		return RunResult{}, err
	}

	driverType := invocation.adapter.Descriptor().Type
	sessionPlan, err := r.sdk.prepareSession(ctx, invocation.session, invocation.agent, driverType, invocation.fingerprint)
	if err != nil {
		return RunResult{}, err
	}
	runCtx := ctx
	var runCancel context.CancelFunc
	if sessionPlan != nil {
		runCtx, runCancel = context.WithCancel(ctx)
		sessionPlan.startLeaseRenewal(runCtx, r.sdk.sessionStore, runCancel)
		defer runCancel()
		defer sessionPlan.release()
	}

	result, checkpoint, err := r.executeWithSessionPlan(runCtx, sink, invocation, sessionPlan)
	if sessionPlan != nil {
		sessionPlan.stopLeaseRenewal()
		if renewErr := sessionPlan.renewalError(); renewErr != nil {
			return RunResult{}, renewErr
		}
	}
	if err != nil {
		return RunResult{}, err
	}

	sessionRef, err := r.sdk.persistSession(
		runCtx,
		sessionPlan,
		invocation.agent,
		invocation.adapter,
		invocation.fingerprint,
		checkpoint,
	)
	if err != nil {
		if errors.Is(err, ErrSessionCheckpointMissing) {
			if result.Failure == nil {
				if f := pendingFailureFromSink(sink); f != nil {
					result.Failure = cloneRunFailure(f)
				}
			}
			if shouldSkipSessionPersistOnFailure(result) {
				return result, nil
			}
		}
		return RunResult{}, err
	}
	result.Session = sessionRef
	return result, nil
}

func shouldSkipSessionPersistOnFailure(result RunResult) bool {
	if result.Failure == nil {
		return false
	}
	// A human decision reject/timeout may intentionally abort the run before
	// the provider emits a new resumable checkpoint. Per the session contract,
	// failed runs must not persist unhealthy state; treating the missing
	// checkpoint as a hard SDK error would misclassify an expected business
	// outcome as infrastructure failure.
	if result.Failure.IsHumanDecision() {
		return true
	}
	return false
}

func pendingFailureFromSink(sink EventSink) *RunFailure {
	switch typed := sink.(type) {
	case *dualSink:
		return typed.pendingFailure()
	case *seqSink:
		return pendingFailureFromSink(typed.inner)
	default:
		return nil
	}
}

// validateDecisionCapabilities cross-checks the resolved policy against the
// adapter's declared HumanDecision / Question support matrix. Unsupported
// Ask modes are hard errors. Retry support is enforced later, when a specific
// Kind actually tries to use FailureRetry.
func (r *runnerImpl) validateDecisionCapabilities(inv resolvedInvocation) error {
	caps := inv.adapter.Descriptor().RunPolicyCaps
	p := inv.policy.HumanDecision

	checkMode := func(kind HumanDecisionKind, mode string, support HumanDecisionSupport, modeAsk, modeAutoApprove, modeAutoReject string) error {
		switch mode {
		case modeAsk:
			if !support.Ask {
				return fmt.Errorf("%w: adapter=%s kind=%s mode=%s", ErrHumanDecisionModeUnsupported, caps.adapterLabel(), kind, mode)
			}
		case modeAutoApprove:
			if !support.AutoApprove {
				return fmt.Errorf("%w: adapter=%s kind=%s mode=%s", ErrHumanDecisionModeUnsupported, caps.adapterLabel(), kind, mode)
			}
		case modeAutoReject:
			if !support.AutoReject {
				return fmt.Errorf("%w: adapter=%s kind=%s mode=%s", ErrHumanDecisionModeUnsupported, caps.adapterLabel(), kind, mode)
			}
		}
		return nil
	}

	if err := checkMode(HumanDecisionPermission, string(p.Permission), caps.Permission,
		string(HumanDecisionAsk), string(HumanDecisionAutoApprove), string(HumanDecisionAutoReject)); err != nil {
		return err
	}
	if err := checkMode(HumanDecisionPlanReview, string(p.PlanReview), caps.PlanReview,
		string(HumanDecisionAsk), string(HumanDecisionAutoApprove), string(HumanDecisionAutoReject)); err != nil {
		return err
	}

	// Question uses its own support type (no AutoApprove).
	switch p.Question {
	case QuestionAsk:
		if !caps.Question.Ask {
			return fmt.Errorf("%w: adapter=%s kind=%s mode=%s", ErrHumanDecisionModeUnsupported, caps.adapterLabel(), HumanDecisionQuestion, p.Question)
		}
	case QuestionAutoReject:
		if !caps.Question.AutoReject {
			// Spec §3.8: when the adapter does not model Question at all
			// (all QuestionSupport fields false), QuestionAutoReject is a
			// no-op — treat it as Unset to avoid breaking the portable
			// safe-default policy (see §5.4.3).
			if !caps.Question.Ask && !caps.Question.Retry {
				break
			}
			return fmt.Errorf("%w: adapter=%s kind=%s mode=%s", ErrHumanDecisionModeUnsupported, caps.adapterLabel(), HumanDecisionQuestion, p.Question)
		}
	}

	return nil
}

func (r *runnerImpl) resolveInvocation(ctx context.Context, prompt string, opts ...RunOption) (resolvedInvocation, func(), error) {
	if err := validateAgentBinding(r.binding); err != nil {
		return resolvedInvocation{}, nil, err
	}

	resolvedOpts := runOptions{}
	for _, opt := range opts {
		if opt != nil {
			opt(&resolvedOpts)
		}
	}

	defaults := r.binding.Defaults()
	identity := defaults.Agent
	if resolvedOpts.agent != nil {
		identity = *resolvedOpts.agent
	}

	workspaceSpec := defaults.Workspace
	if resolvedOpts.workspace != nil {
		workspaceSpec = resolvedOpts.workspace
	}
	runtimeOverride := resolvedOpts.runtime

	defaultRefs := r.sdk.selectedRefsFor(r.name, defaults.Skills)
	runRefs := cloneSkillRefs(resolvedOpts.skills)

	policy, err := mergeRunPolicy(defaults.RunPolicy, resolvedOpts.runPolicy)
	if err != nil {
		return resolvedInvocation{}, nil, err
	}

	handlers := decisionHandlers{
		Permission: defaults.PermissionHandler,
		PlanReview: defaults.PlanReviewHandler,
		Question:   defaults.QuestionHandler,
	}
	if resolvedOpts.permissionHandler != nil {
		handlers.Permission = resolvedOpts.permissionHandler
	}
	if resolvedOpts.planReviewHandler != nil {
		handlers.PlanReview = resolvedOpts.planReviewHandler
	}
	if resolvedOpts.questionHandler != nil {
		handlers.Question = resolvedOpts.questionHandler
	}

	instructions := cloneInstructions(defaults.Instructions)
	declared := profileDeclarationsFromDefaults(defaults)
	if resolvedOpts.instructionsSet {
		instructions = cloneInstructions(resolvedOpts.instructions)
		declared.Instructions = true
	}
	instructions, err = prepareInstructionsBundle(instructions)
	if err != nil {
		return resolvedInvocation{}, nil, err
	}

	metadata := mergeStringMaps(defaults.Metadata, resolvedOpts.metadata)
	config := r.binding.Config()
	common := extractCommonConfig(config)

	streaming := false
	if defaults.Streaming != nil {
		streaming = *defaults.Streaming
	}
	if resolvedOpts.streaming != nil {
		streaming = *resolvedOpts.streaming
	}

	workspace, err := r.sdk.workspaceManager.Resolve(ctx, WorkspaceRequest{
		BaseCWD: common.CWD,
		Spec:    workspaceSpec,
		Metadata: mergeStringMaps(
			commonConfigMetadata(common),
			metadata,
		),
	})
	if err != nil {
		return resolvedInvocation{}, nil, err
	}

	runID := resolvedOpts.runIDPreset
	if runID == "" {
		runID = newRunID(r.binding.Adapter().Descriptor().Type)
	}

	runtimePayload, err := r.sdk.prepareRuntime(ctx, runID, r.binding, identity, workspace, metadata, runtimeOverride)
	if err != nil {
		// Use a detached ctx so a cancelled parent still allows the
		// release to finish (otherwise we can leak workspaces when a
		// user cancels Run early). Values propagate so tracing / tenant
		// bindings are preserved.
		_ = r.sdk.workspaceManager.Release(context.WithoutCancel(ctx), workspace, WorkspaceReleaseKeep)
		return resolvedInvocation{}, nil, err
	}

	cleanup := func() {
		releaseCtx := context.WithoutCancel(ctx)
		if runID != "" {
			_ = r.sdk.runtimeManager.ReleaseByRun(releaseCtx, runID)
		}
		_ = r.sdk.workspaceManager.Release(releaseCtx, workspace, WorkspaceReleaseKeep)
	}

	mcpPayload, err := resolveMCPPayloadWithRuntime(defaults.MCP, resolvedOpts.mcp, runtimePayload.Ensured, r.binding.Adapter().Descriptor().MCP)
	if err != nil {
		cleanup()
		return resolvedInvocation{}, nil, err
	}

	skillPayload, _, _, err := r.sdk.resolveSkills(ctx, identity, defaultRefs, runRefs, defaults.Skills)
	if err != nil {
		cleanup()
		return resolvedInvocation{}, nil, err
	}

	if injector, ok := r.binding.Adapter().(SkillAwareDriver); ok {
		if err := injector.InjectSkills(ctx, r.binding.Config(), cloneResolvedSkills(skillPayload), cloneProfileSelection(defaults.Profile)); err != nil {
			cleanup()
			return resolvedInvocation{}, nil, err
		}
	}

	agentPayload, err := prepareAgentPayload(defaults.Agents)
	if err != nil {
		cleanup()
		return resolvedInvocation{}, nil, err
	}
	if resolvedOpts.agents != nil {
		declared.Agents = true
		agentPayload, err = prepareAgentPayload(resolvedOpts.agents.Agents)
		if err != nil {
			cleanup()
			return resolvedInvocation{}, nil, err
		}
	}
	hookPayload, err := prepareHookPayload(defaults.Hooks)
	if err != nil {
		cleanup()
		return resolvedInvocation{}, nil, err
	}
	if resolvedOpts.hooks != nil {
		declared.Hooks = true
		hookPayload, err = prepareHookPayload(resolvedOpts.hooks.Hooks)
		if err != nil {
			cleanup()
			return resolvedInvocation{}, nil, err
		}
	}
	configPayload, err := prepareProfileConfigPayload(defaults.ProfileConfig)
	if err != nil {
		cleanup()
		return resolvedInvocation{}, nil, err
	}
	if resolvedOpts.profileConfig != nil {
		declared.Config = true
		configPayload, err = prepareProfileConfigPayload(resolvedOpts.profileConfig.Patches)
		if err != nil {
			cleanup()
			return resolvedInvocation{}, nil, err
		}
	}
	profilePayload := buildProfilePayload(skillPayload, mcpPayload, agentPayload, hookPayload, instructions, configPayload, declared)

	sessionReq := SessionRequest{}
	if resolvedOpts.session != nil {
		sessionReq = *resolvedOpts.session
	}

	fingerprint := stableHash(
		r.binding.Adapter().Descriptor().Type,
		identity,
		extractDriverFingerprint(config),
		strings.TrimSpace(resolvedOpts.model),
		workspace.Fingerprint,
		runtimePayload.Fingerprint,
		profilePayload.Fingerprint,
	)

	return resolvedInvocation{
		runID:          runID,
		prompt:         prompt,
		adapter:        r.binding.Adapter(),
		config:         config,
		agent:          identity,
		workspace:      workspace,
		runtime:        runtimePayload,
		skills:         skillPayload,
		mcp:            mcpPayload,
		profilePayload: profilePayload,
		profile:        cloneProfileSelection(defaults.Profile),
		policy:         policy,
		handlers:       handlers,
		instructions:   instructions,
		session:        sessionReq,
		metadata:       cloneStringMap(metadata),
		fingerprint:    fingerprint,
		streaming:      streaming,
		model:          strings.TrimSpace(resolvedOpts.model),
	}, cleanup, nil
}

func (r *runnerImpl) executeWithSessionPlan(
	ctx context.Context,
	sink EventSink,
	invocation resolvedInvocation,
	plan *resolvedSessionPlan,
) (RunResult, *DriverCheckpoint, error) {
	if len(invocation.runtime.Ensured) > 0 {
		serviceNames := make([]string, 0, len(invocation.runtime.Ensured))
		for _, service := range invocation.runtime.Ensured {
			if service.Name != "" {
				serviceNames = append(serviceNames, service.Name)
			}
		}
		_ = sink.Emit(RunEvent{
			Type:      RunEventRuntime,
			Text:      "runtime services ready",
			Timestamp: time.Now().UTC(),
			Data: map[string]any{
				"services": cloneRuntimeServiceRefs(invocation.runtime.Ensured),
				"names":    serviceNames,
			},
		})
	}

	driverReq := DriverRunRequest{
		RunID:          invocation.runID,
		Prompt:         invocation.prompt,
		Config:         invocation.config,
		ModelOverride:  invocation.model,
		Agent:          invocation.agent,
		Workspace:      invocation.workspace,
		Runtime:        cloneRuntimePayload(invocation.runtime),
		Skills:         cloneResolvedSkills(invocation.skills),
		MCP:            cloneMCPPayload(invocation.mcp),
		ProfilePayload: cloneProfilePayload(invocation.profilePayload),
		Profile:        cloneProfileSelection(invocation.profile),
		Policy:         invocation.policy,
		Instructions:   invocation.instructions,
		Metadata:       cloneStringMap(invocation.metadata),
		Streaming:      invocation.streaming,
	}
	if plan != nil {
		var state *DriverSessionState
		if plan.record != nil && (plan.reused || plan.request.Mode == SessionFork) {
			state = normalizeSessionState(invocation.adapter, plan.record.DriverState)
		}
		driverReq.Session = &DriverSessionContext{
			EngineSessionID: plan.engineID,
			Mode:            plan.request.Mode,
			State:           state,
			PreviousID:      plan.previousID,
		}
	}

	runResult, err := invocation.adapter.Run(ctx, driverReq, sink)
	if err != nil {
		var rejected *ResumeRejectedError
		if errors.As(err, &rejected) && plan != nil && plan.reused && plan.request.Mode == SessionContinueOrStart {
			if err := plan.prepareFresh(ctx, r.sdk.sessionStore, invocation.adapter.Descriptor().Type, invocation.fingerprint); err != nil {
				return RunResult{}, nil, err
			}
			return r.executeWithSessionPlan(ctx, sink, invocation, plan)
		}
		return RunResult{}, nil, err
	}

	runtimeReports := cloneRuntimeServiceReports(runResult.RuntimeServices)
	if len(runtimeReports) == 0 {
		runtimeReports = runtimeReportsFromRefs(invocation.runtime.Ensured, invocation.agent)
	}

	return RunResult{
		RunID:           invocation.runID,
		DriverType:      invocation.adapter.Descriptor().Type,
		Output:          runResult.Output,
		RawStreams:      cloneRawStreams(runResult.RawStreams),
		Transcript:      cloneTranscriptItems(runResult.Transcript),
		ExitCode:        runResult.ExitCode,
		Signal:          runResult.Signal,
		TimedOut:        runResult.TimedOut,
		Usage:           cloneUsagePointer(runResult.Usage),
		Metadata:        cloneStringMap(runResult.Metadata),
		Provider:        runResult.Provider,
		Biller:          runResult.Biller,
		Model:           runResult.Model,
		BillingType:     runResult.BillingType,
		CostUSD:         cloneFloat64Pointer(runResult.CostUSD),
		Summary:         runResult.Summary,
		Result:          cloneAnyMap(runResult.Result),
		RuntimeServices: runtimeReports,
		Question:        cloneRunQuestion(runResult.Question),
		Failure:         cloneRunFailure(runResult.Failure),
	}, runResult.Checkpoint, nil
}

// adapterLabel returns a best-effort diagnostic label for error messages.
// RunPolicyCapabilities is a value type without an adapter name, so callers
// fall back to a generic label.
func (RunPolicyCapabilities) adapterLabel() string { return "adapter" }

// -----------------------------------------------------------------------------
// EventSink wrappers.
// -----------------------------------------------------------------------------

// seqSink wraps an EventSink and assigns monotonically increasing sequence
// numbers for the enclosing run. RunEvent.Seq uses its own counter; Stream
// events delegate to the underlying sink which owns the shared stream
// counter (see dualSink.streamCounter) so dualSink-internal emission
// (HITL requested/resolved) and adapter-side emission share one run-local
// monotonic cursor.
//
// When the wrapped sink also implements DecisionCapableSink, the wrapper
// forwards RequestDecision so adapters can block through it.
type seqSink struct {
	inner         EventSink
	eventCounter  atomic.Uint64
	streamCounter atomic.Uint64 // only used for non-dualSink inner (tests / custom sinks)
}

func wrapWithSeq(inner EventSink) EventSink {
	return &seqSink{inner: inner}
}

func (s *seqSink) Emit(event RunEvent) error {
	event.Seq = s.eventCounter.Add(1)
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now().UTC()
	}
	return s.inner.Emit(event)
}

func (s *seqSink) EmitStream(payload StreamPayload) error {
	if payload.Timestamp.IsZero() {
		payload.Timestamp = time.Now().UTC()
	}
	// When the underlying sink is dualSink, let it own the stream counter so
	// runner-internal HITL emissions share the same sequence. For any other
	// sink (tests, custom implementations), assign Sequence here so callers
	// still see monotonic numbering.
	if _, ok := s.inner.(*dualSink); !ok && payload.Sequence == 0 {
		n := s.streamCounter.Add(1)
		payload.Sequence = n
		payload.Seq = n - 1
	}
	return s.inner.EmitStream(payload)
}

// RequestDecision forwards to the wrapped sink when it implements
// DecisionCapableSink. Otherwise the call returns a DecisionTimedOut result
// synthesized synchronously — this matches the "no channel available"
// behaviour described in §3.4.
func (s *seqSink) RequestDecision(ctx context.Context, req DecisionRequest) (DecisionResponse, error) {
	if ic, ok := s.inner.(DecisionCapableSink); ok {
		return ic.RequestDecision(ctx, req)
	}
	return DecisionResponse{RequestID: req.RequestID, Result: DecisionTimedOut}, nil
}

type noopEventSink struct{}

func (noopEventSink) Emit(RunEvent) error            { return nil }
func (noopEventSink) EmitStream(StreamPayload) error { return nil }

// EventBackpressure selects how the SDK reacts when a host cannot keep up
// with StreamPayload delivery. RunEvent delivery always falls back to the
// legacy drop-with-marker behaviour and is not affected by this setting.
type EventBackpressure int

const (
	// BackpressureDropStream drops StreamPayloads when the stream channel is
	// full and emits a single StreamDropped marker (carrying the lost count)
	// as soon as capacity returns. This is the default and guarantees that
	// adapter sub-processes never block on a slow host.
	BackpressureDropStream EventBackpressure = iota
	// BackpressureBlock blocks the adapter goroutine until the host consumes
	// a StreamPayload. Use this when the host cannot tolerate any gaps (for
	// example a strict AG-UI conformance client).
	BackpressureBlock
)

// -----------------------------------------------------------------------------
// dualSink: concrete sink used by Start(). Implements DecisionCapableSink.
// -----------------------------------------------------------------------------

// dualSink carries the operational RunEvent channel, the structured
// StreamPayload channel, and the HITL DecisionRequest channel. Consumers may
// leave any of these unread — the sink applies backpressure / drop markers on
// the stream channel, forwards DecisionRequests by timeout when unread, and
// accepts ResolveDecision calls to unblock adapters.
//
// See docs/run-policy.md for the public contract; workstream-hitl-v2.md keeps
// the detailed dispatcher rationale.
type dualSink struct {
	runEvents        chan RunEvent
	stream           chan StreamPayload
	decisionRequests chan DecisionRequest
	streaming        bool
	policy           EventBackpressure

	done         chan struct{}
	activeStream sync.WaitGroup

	once       sync.Once
	mu         sync.Mutex
	closed     bool
	droppedRun int
	droppedStm int

	// Run-scoped state populated by bindRun before adapter.Run starts.
	runID         string
	threadID      string
	policyHD      HumanDecisionPolicy
	handlers      decisionHandlers
	caps          RunPolicyCapabilities
	decSeq        atomic.Uint64
	streamCounter atomic.Uint64 // shared by adapter-side and runner-side stream emissions
	pending       map[string]*pendingDecision
	pendingMu     sync.Mutex
	retryWarnings map[HumanDecisionKind]struct{}
	// decisionSerial guards the "one HITL at a time per run" invariant so
	// adapters that emit concurrent tool_use frames still see serialized
	// RequestDecision returns.
	decisionSerial sync.Mutex
}

type pendingDecision struct {
	req  DecisionRequest
	kind HumanDecisionKind
	done chan DecisionResponse
}

func newDualSink(runID string, streaming bool, runBuf, streamBuf int, policy EventBackpressure) *dualSink {
	if runBuf <= 0 {
		runBuf = defaultRunEventBuffer
	}
	if streamBuf <= 0 {
		streamBuf = defaultStreamEventBuffer
	}
	s := &dualSink{
		runEvents:        make(chan RunEvent, runBuf),
		stream:           make(chan StreamPayload, streamBuf),
		decisionRequests: make(chan DecisionRequest, 16),
		streaming:        streaming,
		policy:           policy,
		done:             make(chan struct{}),
		runID:            runID,
		pending:          map[string]*pendingDecision{},
		retryWarnings:    map[HumanDecisionKind]struct{}{},
	}
	if !streaming {
		close(s.stream)
	}
	return s
}

// bindRun is called once resolveInvocation has produced a policy, capability
// matrix, and typed handler set so the sink can route RequestDecision
// correctly.
func (s *dualSink) bindRun(runID string, policy HumanDecisionPolicy, handlers decisionHandlers, caps RunPolicyCapabilities) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if runID != "" {
		s.runID = runID
	}
	s.policyHD = EffectiveHumanDecisionPolicy(policy)
	s.handlers = handlers
	s.caps = caps
}

func (s *dualSink) Emit(event RunEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.flushDroppedRunLocked()
	select {
	case s.runEvents <- event:
		return nil
	default:
		s.droppedRun++
		return nil
	}
}

func (s *dualSink) EmitStream(payload StreamPayload) error {
	if !s.streaming {
		return nil
	}
	// Assign the shared run-local stream cursor. §3.4.2 mandates Seq is
	// zero-based and monotonic — both adapter-side emissions (through
	// seqSink) and runner-side emissions (HITL lifecycle) share this counter.
	n := s.streamCounter.Add(1)
	payload.Sequence = n
	payload.Seq = n - 1
	if payload.Timestamp.IsZero() {
		payload.Timestamp = time.Now().UTC()
	}

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.activeStream.Add(1)
	s.mu.Unlock()
	defer s.activeStream.Done()

	if s.policy == BackpressureBlock {
		return s.emitStreamBlocking(payload)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.flushDroppedStreamLocked()
	select {
	case s.stream <- payload:
		return nil
	default:
		s.droppedStm++
		return nil
	}
}

func (s *dualSink) emitStreamBlocking(payload StreamPayload) error {
	select {
	case <-s.done:
		return nil
	case s.stream <- payload:
		return nil
	}
}

func (s *dualSink) close() {
	s.once.Do(func() {
		s.mu.Lock()
		s.closed = true
		close(s.done)
		s.mu.Unlock()

		s.activeStream.Wait()

		// Fail any pending decision waiters with DecisionAborted so the
		// adapter goroutine can unwind.
		s.pendingMu.Lock()
		for _, p := range s.pending {
			select {
			case p.done <- DecisionResponse{RequestID: p.req.RequestID, Result: DecisionAborted}:
			default:
			}
			close(p.done)
		}
		s.pending = map[string]*pendingDecision{}
		s.pendingMu.Unlock()

		s.mu.Lock()
		defer s.mu.Unlock()
		s.flushDroppedRunLocked()
		s.flushDroppedStreamLocked()
		close(s.runEvents)
		if s.streaming {
			close(s.stream)
		}
		close(s.decisionRequests)
	})
}

func (s *dualSink) flushDroppedRunLocked() {
	if s.droppedRun == 0 {
		return
	}
	summary := newEvent(RunEventLifecycle, fmt.Sprintf("dropped %d events because consumer was slow", s.droppedRun))
	summary.Metadata = map[string]string{
		"reason":        "overflow",
		"dropped_count": fmt.Sprintf("%d", s.droppedRun),
	}
	select {
	case s.runEvents <- summary:
		s.droppedRun = 0
	default:
	}
}

func (s *dualSink) flushDroppedStreamLocked() {
	if s.droppedStm == 0 || !s.streaming {
		return
	}
	marker := StreamPayload{
		Kind:      StreamDropped,
		Timestamp: time.Now().UTC(),
		Raw: map[string]any{
			"dropped_count": s.droppedStm,
		},
	}
	select {
	case s.stream <- marker:
		s.droppedStm = 0
	default:
	}
}

// -----------------------------------------------------------------------------
// DecisionCapableSink implementation (main HITL v2 dispatcher).
// -----------------------------------------------------------------------------

// RequestDecision is the single entry point adapters use to block on a HITL
// decision. The runner normalizes the request, emits StreamHITLRequested,
// dispatches to typed handler or host channel, emits StreamHITLResolved,
// and applies OnReject / OnTimeout retry / continue / abort policy before
// returning.
//
// Return contract:
//   - (resp, nil) — adapter proceeds using resp.Result (approved / rejected /
//     answered). resp.Result may be DecisionRejected when OnReject=Continue.
//   - (_, abortErr) — run must end; the runner has already stashed the
//     failure context on the sink via setPendingFailure. The adapter should
//     stop its protocol loop and return a DriverRunResult whose Failure is
//     the previously recorded one.
func (s *dualSink) RequestDecision(ctx context.Context, req DecisionRequest) (DecisionResponse, error) {
	s.decisionSerial.Lock()
	defer s.decisionSerial.Unlock()

	req = s.normalizeRequest(req)
	kind := req.Kind

	// Policy-level short-circuit: AutoApprove / AutoReject / QuestionAutoReject
	// resolve without reaching a handler or host channel.
	if resp, decided := s.tryAutoResolve(req); decided {
		s.emitRequested(req)
		s.emitResolved(req, resp, time.Now().UTC())
		return s.applyReject(ctx, req, resp, 1)
	}

	// Otherwise: Ask path (handler / channel / timeout).
	var attempts int
	for {
		attempts++
		req.RetryAttempt = attempts - 1

		s.emitRequested(req)

		decisionCtx, cancel := s.withDeadline(ctx, req.Deadline)
		resp, decision, runErr := s.dispatchOnce(decisionCtx, req)
		cancel()

		s.emitResolved(req, resp, time.Now().UTC())

		switch decision {
		case DecisionApproved, DecisionAnswered:
			return resp, nil

		case DecisionRejected:
			resolved, abortErr := s.applyRejectWithRetry(ctx, req, resp, attempts, kind)
			if abortErr == nil && resolved.retry {
				req = resolved.next
				continue
			}
			return resolved.resp, abortErr

		case DecisionTimedOut:
			resolved, abortErr := s.applyTimeoutWithRetry(ctx, req, attempts, kind)
			if abortErr == nil && resolved.retry {
				req = resolved.next
				continue
			}
			return resolved.resp, abortErr

		case DecisionAborted:
			// Handler returned error, panicked, or ctx cancelled → treat as
			// abort. Preserve any more specific failure recorded by the
			// inner handler stage (panic → FailureAgentError, nil resp →
			// FailureAgentError) so the caller sees the root cause.
			if s.pendingFailure() == nil {
				s.setPendingFailure(&RunFailure{
					Code:    FailureCancelled,
					Message: decisionCancelMessage(runErr),
					HumanDecision: &HumanDecisionFailure{
						Kind:     kind,
						Source:   req.Source,
						Decision: DecisionAborted,
						Request:  cloneDecisionRequest(&req),
						Attempts: attempts,
					},
				})
			}
			return resp, runErr
		}

		// Unknown DecisionResult — defensive return.
		return resp, nil
	}
}

// tryAutoResolve synthesizes an Approved / Rejected response for modes that
// do not require a human. Returns (resp, true) when the mode applies.
func (s *dualSink) tryAutoResolve(req DecisionRequest) (DecisionResponse, bool) {
	switch req.Kind {
	case HumanDecisionPermission:
		switch s.policyHD.Permission {
		case HumanDecisionAutoApprove:
			return DecisionResponse{RequestID: req.RequestID, Result: DecisionApproved}, true
		case HumanDecisionAutoReject:
			return DecisionResponse{RequestID: req.RequestID, Result: DecisionRejected}, true
		}
	case HumanDecisionPlanReview:
		switch s.policyHD.PlanReview {
		case HumanDecisionAutoApprove:
			return DecisionResponse{RequestID: req.RequestID, Result: DecisionApproved}, true
		case HumanDecisionAutoReject:
			return DecisionResponse{RequestID: req.RequestID, Result: DecisionRejected}, true
		}
	case HumanDecisionQuestion:
		if s.policyHD.Question == QuestionAutoReject {
			return DecisionResponse{RequestID: req.RequestID, Result: DecisionRejected}, true
		}
	}
	return DecisionResponse{}, false
}

// dispatchOnce routes req to the typed handler when one is installed,
// otherwise to the host channel.
func (s *dualSink) dispatchOnce(ctx context.Context, req DecisionRequest) (DecisionResponse, DecisionResult, error) {
	switch req.Kind {
	case HumanDecisionPermission:
		if s.handlers.Permission != nil {
			return s.runPermissionHandler(ctx, req)
		}
	case HumanDecisionPlanReview:
		if s.handlers.PlanReview != nil {
			return s.runPlanReviewHandler(ctx, req)
		}
	case HumanDecisionQuestion:
		if s.handlers.Question != nil {
			return s.runQuestionHandler(ctx, req)
		}
	}
	return s.runChannelDispatch(ctx, req)
}

type handlerOutcome struct {
	approvalResult ApprovalResult
	questionResult QuestionResult
	resp           DecisionResponse
	err            error
	panicked       bool
	panicMessage   string
}

func (s *dualSink) runPermissionHandler(ctx context.Context, req DecisionRequest) (DecisionResponse, DecisionResult, error) {
	typed := PermissionRequest{
		decisionRequestBase: req.toBase(),
		Tool:                stringFrom(req.Payload, "tool"),
		Prompt:              req.Prompt,
		Args:                req.Payload,
	}

	ch := make(chan handlerOutcome, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				ch <- handlerOutcome{panicked: true, panicMessage: fmt.Sprintf("%v", r)}
			}
		}()
		resp, err := s.handlers.Permission(ctx, typed)
		ch <- handlerOutcome{approvalResult: resp.Result, resp: DecisionResponse{RequestID: resp.RequestID, Result: approvalToDecision(resp.Result), Text: resp.Text}, err: err}
	}()
	return s.waitHandlerOutcome(ctx, req, ch, approvalWantKinds)
}

func (s *dualSink) runPlanReviewHandler(ctx context.Context, req DecisionRequest) (DecisionResponse, DecisionResult, error) {
	typed := PlanReviewRequest{
		decisionRequestBase: req.toBase(),
		Prompt:              req.Prompt,
		Plan:                stringFrom(req.Payload, "plan"),
		Extra:               req.Payload,
	}

	ch := make(chan handlerOutcome, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				ch <- handlerOutcome{panicked: true, panicMessage: fmt.Sprintf("%v", r)}
			}
		}()
		resp, err := s.handlers.PlanReview(ctx, typed)
		ch <- handlerOutcome{approvalResult: resp.Result, resp: DecisionResponse{RequestID: resp.RequestID, Result: approvalToDecision(resp.Result), Text: resp.Text}, err: err}
	}()
	return s.waitHandlerOutcome(ctx, req, ch, approvalWantKinds)
}

func (s *dualSink) runQuestionHandler(ctx context.Context, req DecisionRequest) (DecisionResponse, DecisionResult, error) {
	typed := QuestionRequest{
		decisionRequestBase: req.toBase(),
		Prompt:              req.Prompt,
		Schema:              mapFrom(req.Payload, "schema"),
		Choices:             req.Choices,
	}

	ch := make(chan handlerOutcome, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				ch <- handlerOutcome{panicked: true, panicMessage: fmt.Sprintf("%v", r)}
			}
		}()
		resp, err := s.handlers.Question(ctx, typed)
		ch <- handlerOutcome{
			questionResult: resp.Result,
			resp: DecisionResponse{
				RequestID: resp.RequestID,
				Result:    questionToDecision(resp.Result),
				Choice:    resp.Choice,
				Answer:    resp.Answer,
				Text:      resp.Text,
			},
			err: err,
		}
	}()
	return s.waitHandlerOutcome(ctx, req, ch, questionWantKinds)
}

// approvalWantKinds / questionWantKinds declare which typed Result values
// each handler family legitimately produces. waitHandlerOutcome uses them
// to detect "returned nil response without error" and panic cases.
var (
	approvalWantKinds = []DecisionResult{DecisionApproved, DecisionRejected}
	questionWantKinds = []DecisionResult{DecisionAnswered, DecisionRejected}
)

func (s *dualSink) waitHandlerOutcome(ctx context.Context, req DecisionRequest, ch <-chan handlerOutcome, want []DecisionResult) (DecisionResponse, DecisionResult, error) {
	select {
	case out := <-ch:
		if out.panicked {
			err := fmt.Errorf("handler panic: %s", out.panicMessage)
			s.setPendingFailure(&RunFailure{
				Code:    FailureAgentError,
				Message: err.Error(),
			})
			return DecisionResponse{RequestID: req.RequestID}, DecisionAborted, err
		}
		if out.err != nil {
			return out.resp, DecisionAborted, out.err
		}
		// Validate the handler produced a legal Result for its Kind.
		if !containsDecision(want, out.resp.Result) {
			err := errors.New("handler returned nil response without error")
			s.setPendingFailure(&RunFailure{Code: FailureAgentError, Message: err.Error()})
			return DecisionResponse{RequestID: req.RequestID}, DecisionAborted, err
		}
		out.resp.RequestID = req.RequestID
		return out.resp, out.resp.Result, nil
	case <-ctx.Done():
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return DecisionResponse{RequestID: req.RequestID, Result: DecisionTimedOut}, DecisionTimedOut, nil
		}
		return DecisionResponse{RequestID: req.RequestID}, DecisionAborted, ctx.Err()
	}
}

func (s *dualSink) runChannelDispatch(ctx context.Context, req DecisionRequest) (DecisionResponse, DecisionResult, error) {
	p := &pendingDecision{req: req, kind: req.Kind, done: make(chan DecisionResponse, 1)}
	s.pendingMu.Lock()
	s.pending[req.RequestID] = p
	s.pendingMu.Unlock()
	defer func() {
		s.pendingMu.Lock()
		delete(s.pending, req.RequestID)
		s.pendingMu.Unlock()
	}()

	select {
	case <-s.done:
		return DecisionResponse{RequestID: req.RequestID}, DecisionAborted, context.Canceled
	case s.decisionRequests <- req:
	case <-ctx.Done():
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return DecisionResponse{RequestID: req.RequestID, Result: DecisionTimedOut}, DecisionTimedOut, nil
		}
		return DecisionResponse{RequestID: req.RequestID}, DecisionAborted, ctx.Err()
	}

	select {
	case resp, ok := <-p.done:
		if !ok {
			return DecisionResponse{RequestID: req.RequestID}, DecisionAborted, context.Canceled
		}
		resp.RequestID = req.RequestID
		if resp.Result == DecisionTimedOut {
			return resp, DecisionTimedOut, nil
		}
		if resp.Result == DecisionAborted {
			return resp, DecisionAborted, context.Canceled
		}
		return resp, resp.Result, nil
	case <-ctx.Done():
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return DecisionResponse{RequestID: req.RequestID, Result: DecisionTimedOut}, DecisionTimedOut, nil
		}
		return DecisionResponse{RequestID: req.RequestID}, DecisionAborted, ctx.Err()
	}
}

// resolveDecisionFromHandle is called by asyncRunHandle.ResolveDecision.
func (s *dualSink) resolveDecisionFromHandle(requestID string, resp DecisionResponse) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return ErrRunEnded
	}
	s.mu.Unlock()

	s.pendingMu.Lock()
	p, ok := s.pending[requestID]
	if ok {
		delete(s.pending, requestID)
	}
	s.pendingMu.Unlock()

	if !ok {
		return ErrDecisionRequestExpired
	}
	if !resultCompatibleWithKind(p.kind, resp.Result) {
		// Per §3.11.4: re-register the pending entry so the runner can keep
		// waiting for a compatible resolve.
		s.pendingMu.Lock()
		s.pending[requestID] = p
		s.pendingMu.Unlock()
		return ErrDecisionResultKindMismatch
	}
	resp.RequestID = requestID
	select {
	case p.done <- resp:
	default:
	}
	return nil
}

type retryOutcome struct {
	retry bool
	next  DecisionRequest
	resp  DecisionResponse
}

// applyRejectWithRetry applies OnReject to a DecisionRejected outcome.
func (s *dualSink) applyRejectWithRetry(ctx context.Context, req DecisionRequest, resp DecisionResponse, attempts int, kind HumanDecisionKind) (retryOutcome, error) {
	return s.applyFailureAction(ctx, req, resp, attempts, kind, s.policyHD.OnReject, FailureReject, DecisionRejected, s.retrySupported(kind))
}

// applyTimeoutWithRetry applies OnTimeout to a DecisionTimedOut outcome.
func (s *dualSink) applyTimeoutWithRetry(ctx context.Context, req DecisionRequest, attempts int, kind HumanDecisionKind) (retryOutcome, error) {
	resp := DecisionResponse{RequestID: req.RequestID, Result: DecisionTimedOut}
	return s.applyFailureAction(ctx, req, resp, attempts, kind, s.policyHD.OnTimeout, FailureTimeout, DecisionTimedOut, s.retrySupported(kind))
}

// applyFailureAction centralises OnReject / OnTimeout handling. retryAllowed
// gates FailureRetry on adapter support; adapters that cannot retry the same
// decision are degraded to FailureAbort with a lifecycle warning.
func (s *dualSink) applyFailureAction(_ context.Context, req DecisionRequest, resp DecisionResponse, attempts int, kind HumanDecisionKind, action FailureAction, code FailureCode, decision DecisionResult, retryAllowed bool) (retryOutcome, error) {
	switch action {
	case FailureContinue:
		return retryOutcome{retry: false, resp: resp}, nil
	case FailureRetry:
		if !retryAllowed {
			s.emitRetryUnsupportedWarning(kind, action)
			s.setPendingFailure(&RunFailure{
				Code:    code,
				Message: decisionFailureMessage(decision, kind, req.Source),
				HumanDecision: &HumanDecisionFailure{
					Kind:     kind,
					Source:   req.Source,
					Decision: decision,
					Request:  cloneDecisionRequest(&req),
					Attempts: attempts,
				},
			})
			return retryOutcome{resp: resp}, errAbortFromFailure(code)
		}
		// Bound by MaxRetries (attempts >= max → abort).
		if attempts > s.policyHD.MaxRetries {
			s.setPendingFailure(&RunFailure{
				Code:    code,
				Message: fmt.Sprintf("human decision %s exhausted retries (%d attempts)", decision, attempts),
				HumanDecision: &HumanDecisionFailure{
					Kind:     kind,
					Source:   req.Source,
					Decision: decision,
					Request:  cloneDecisionRequest(&req),
					Attempts: attempts,
				},
			})
			return retryOutcome{resp: resp}, errAbortFromFailure(code)
		}
		next := s.renewForRetry(req)
		return retryOutcome{retry: true, next: next}, nil
	case FailureAbort, FailureActionUnset:
		fallthrough
	default:
		s.setPendingFailure(&RunFailure{
			Code:    code,
			Message: decisionFailureMessage(decision, kind, req.Source),
			HumanDecision: &HumanDecisionFailure{
				Kind:     kind,
				Source:   req.Source,
				Decision: decision,
				Request:  cloneDecisionRequest(&req),
				Attempts: attempts,
			},
		})
		return retryOutcome{resp: resp}, errAbortFromFailure(code)
	}
}

func (s *dualSink) retrySupported(kind HumanDecisionKind) bool {
	switch kind {
	case HumanDecisionPermission:
		return s.caps.Permission.Retry
	case HumanDecisionPlanReview:
		return s.caps.PlanReview.Retry
	case HumanDecisionQuestion:
		return s.caps.Question.Retry
	default:
		return false
	}
}

func (s *dualSink) emitRetryUnsupportedWarning(kind HumanDecisionKind, action FailureAction) {
	s.mu.Lock()
	if _, exists := s.retryWarnings[kind]; exists {
		s.mu.Unlock()
		return
	}
	s.retryWarnings[kind] = struct{}{}
	s.mu.Unlock()

	_ = s.Emit(RunEvent{
		Type:      RunEventLifecycle,
		Text:      fmt.Sprintf("human decision %s does not support %s; degrading to abort", kind, action),
		Timestamp: time.Now().UTC(),
		Data: map[string]any{
			"kind":    string(kind),
			"action":  string(action),
			"warning": "human_decision_retry_unsupported",
		},
	})
}

// applyReject handles auto-resolved (AutoReject / AutoApprove) paths. Approved
// paths return immediately; Rejected paths route through OnReject.
func (s *dualSink) applyReject(ctx context.Context, req DecisionRequest, resp DecisionResponse, attempts int) (DecisionResponse, error) {
	if resp.Result == DecisionApproved {
		return resp, nil
	}
	out, err := s.applyRejectWithRetry(ctx, req, resp, attempts, req.Kind)
	if err != nil {
		return out.resp, err
	}
	// Auto-resolve is deterministic — retry is a no-op and will just
	// re-synthesize the same result. The runner degrades to Abort by asking
	// adapters for Retry support; here we detect and immediately collapse to
	// the action's terminal outcome.
	if out.retry {
		s.setPendingFailure(&RunFailure{
			Code:    FailureReject,
			Message: "AutoReject cannot be retried; degrading to abort",
			HumanDecision: &HumanDecisionFailure{
				Kind:     req.Kind,
				Source:   req.Source,
				Decision: DecisionRejected,
				Request:  cloneDecisionRequest(&req),
				Attempts: attempts,
			},
		})
		return out.resp, errAbortFromFailure(FailureReject)
	}
	return out.resp, nil
}

func (s *dualSink) renewForRetry(req DecisionRequest) DecisionRequest {
	next := req
	next.RequestID = s.nextDecisionID()
	next.CreatedAt = time.Now().UTC()
	next.Deadline = next.CreatedAt.Add(s.effectiveTimeout())
	next.RetryAttempt = req.RetryAttempt + 1
	return next
}

func (s *dualSink) normalizeRequest(req DecisionRequest) DecisionRequest {
	if req.RequestID == "" {
		req.RequestID = s.nextDecisionID()
	}
	if req.RunID == "" {
		req.RunID = s.runID
	}
	if req.CreatedAt.IsZero() {
		req.CreatedAt = time.Now().UTC()
	}
	if req.Deadline.IsZero() {
		req.Deadline = req.CreatedAt.Add(s.effectiveTimeout())
	}
	return req
}

func (s *dualSink) effectiveTimeout() time.Duration {
	d := s.policyHD.Timeout
	if d < 0 {
		// Negative means "never time out"; use a far-future deadline.
		return 100 * 365 * 24 * time.Hour
	}
	if d == 0 {
		return DefaultHumanDecisionTimeout
	}
	return d
}

func (s *dualSink) nextDecisionID() string {
	n := s.decSeq.Add(1)
	return fmt.Sprintf("%s-dec-%d", s.runID, n)
}

func (s *dualSink) withDeadline(ctx context.Context, deadline time.Time) (context.Context, context.CancelFunc) {
	if deadline.IsZero() {
		return context.WithCancel(ctx)
	}
	return context.WithDeadline(ctx, deadline)
}

// emitRequested pushes a StreamHITLRequested payload (if streaming enabled).
func (s *dualSink) emitRequested(req DecisionRequest) {
	if !s.streaming {
		return
	}
	payload := StreamPayload{
		Kind:     StreamHITLRequested,
		RunID:    req.RunID,
		ThreadID: s.threadID,
		Name:     req.Source,
		HITLRequested: &HITLRequestedPayload{
			RequestID:    req.RequestID,
			Kind:         req.Kind,
			Source:       req.Source,
			ToolCallID:   req.ToolCallID,
			Prompt:       req.Prompt,
			Payload:      cloneAnyMap(req.Payload),
			Choices:      append([]DecisionChoice(nil), req.Choices...),
			CreatedAt:    req.CreatedAt,
			Deadline:     req.Deadline,
			RetryAttempt: req.RetryAttempt,
		},
	}
	_ = s.EmitStream(payload)
}

// emitResolved pushes a StreamHITLResolved payload (if streaming enabled).
func (s *dualSink) emitResolved(req DecisionRequest, resp DecisionResponse, at time.Time) {
	if !s.streaming {
		return
	}
	latency := time.Duration(0)
	if !req.CreatedAt.IsZero() {
		latency = at.Sub(req.CreatedAt)
	}
	payload := StreamPayload{
		Kind:     StreamHITLResolved,
		RunID:    req.RunID,
		ThreadID: s.threadID,
		Name:     req.Source,
		HITLResolved: &HITLResolvedPayload{
			RequestID:    req.RequestID,
			Kind:         req.Kind,
			Source:       req.Source,
			RetryAttempt: req.RetryAttempt,
			Result:       resp.Result,
			Choice:       resp.Choice,
			Answer:       cloneAnyMap(resp.Answer),
			ResolvedAt:   at,
			Latency:      latency,
		},
	}
	_ = s.EmitStream(payload)
}

// setPendingFailure stashes the failure so the adapter, on its next
// DriverRunResult return, can surface it via runner propagation. Adapters
// read this via (*dualSink).pendingFailure().
var pendingFailuresByRunMu sync.Mutex
var pendingFailuresByRun = map[*dualSink]*RunFailure{}

func (s *dualSink) setPendingFailure(f *RunFailure) {
	pendingFailuresByRunMu.Lock()
	defer pendingFailuresByRunMu.Unlock()
	pendingFailuresByRun[s] = f
}

// pendingFailure exposes the recorded failure for runner-side propagation.
// Used by runner.executeWithSessionPlan to overlay the HITL failure on top
// of the adapter's own DriverRunResult.Failure when present.
func (s *dualSink) pendingFailure() *RunFailure {
	pendingFailuresByRunMu.Lock()
	defer pendingFailuresByRunMu.Unlock()
	return pendingFailuresByRun[s]
}

// -----------------------------------------------------------------------------
// asyncRunHandle.
// -----------------------------------------------------------------------------

type asyncRunResult struct {
	result RunResult
	err    error
}

type asyncRunHandle struct {
	runID          string
	events         <-chan RunEvent
	stream         <-chan StreamPayload
	decisionReqsCh <-chan DecisionRequest
	sink           *dualSink
	cancel         context.CancelFunc
	done           chan asyncRunResult
}

func (h *asyncRunHandle) Events() <-chan RunEvent { return h.events }

func (h *asyncRunHandle) StreamEvents() <-chan StreamPayload { return h.stream }

func (h *asyncRunHandle) RunID() string { return h.runID }

func (h *asyncRunHandle) DecisionRequests() <-chan DecisionRequest { return h.decisionReqsCh }

func (h *asyncRunHandle) ResolveDecision(requestID string, resp DecisionResponse) error {
	if h.sink == nil {
		return ErrRunEnded
	}
	return h.sink.resolveDecisionFromHandle(requestID, resp)
}

func (h *asyncRunHandle) Wait(ctx context.Context) (RunResult, error) {
	select {
	case <-ctx.Done():
		return RunResult{}, ctx.Err()
	case result, ok := <-h.done:
		if !ok {
			return RunResult{}, context.Canceled
		}
		// If the runner accumulated a HITL failure that the adapter did not
		// already overlay on its DriverRunResult.Failure, promote it here so
		// hosts always see the structured attribution.
		if h.sink != nil {
			if f := h.sink.pendingFailure(); f != nil {
				if result.result.Failure == nil {
					result.result.Failure = cloneRunFailure(f)
				} else if result.result.Failure.HumanDecision == nil && f.HumanDecision != nil {
					result.result.Failure = cloneRunFailure(f)
				}
			}
			// Mirror Run()'s ErrSessionCheckpointMissing tolerance so Start()
			// and Run() collapse to the same RunResult shape. The run()-
			// internal check already handles the common case; this second
			// pass catches the race where pendingFailure (HITL attribution)
			// was committed AFTER run() observed sink.pendingFailure() but
			// BEFORE Wait drained it here. Without this branch, Start()
			// callers would see ErrSessionCheckpointMissing as infrastructure
			// failure for a run that should surface as a structured HITL
			// RunFailure, contradicting the documented Run/Start symmetry.
			if errors.Is(result.err, ErrSessionCheckpointMissing) && shouldSkipSessionPersistOnFailure(result.result) {
				result.err = nil
			}
			// Drop the pending failure reference to avoid leaking across runs.
			pendingFailuresByRunMu.Lock()
			delete(pendingFailuresByRun, h.sink)
			pendingFailuresByRunMu.Unlock()
		}
		return result.result, result.err
	}
}

func (h *asyncRunHandle) Cancel(_ context.Context) error {
	if h.cancel != nil {
		h.cancel()
	}
	return nil
}

// -----------------------------------------------------------------------------
// helpers.
// -----------------------------------------------------------------------------

func extractDriverFingerprint(cfg any) string {
	return stableHash(cfg)
}

func instructionFingerprint(ref *InstructionsBundleRef) string {
	if ref == nil {
		return ""
	}
	if ref.Fingerprint != "" {
		return ref.Fingerprint
	}
	content := ref.Content
	if ref.Path != "" && content == "" {
		if raw, err := os.ReadFile(ref.Path); err == nil {
			content = string(raw)
		}
	}
	return stableHash("instructions", ref.ID, ref.Path, content, ref.Scope, ref.Mode, ref.Native)
}

func commonConfigMetadata(cfg CommonConfig) map[string]string {
	if cfg.CWD == "" {
		return nil
	}
	return map[string]string{
		"cwd": cfg.CWD,
	}
}

func newEvent(kind RunEventType, text string) RunEvent {
	return RunEvent{
		Type:      kind,
		Text:      text,
		Timestamp: time.Now().UTC(),
	}
}

func newItemEvent(item TranscriptItem) RunEvent {
	clone := cloneTranscriptItem(item)
	return RunEvent{
		Type:      RunEventItem,
		Timestamp: time.Now().UTC(),
		Item:      &clone,
	}
}

// (*DecisionRequest).toBase extracts the common fields for typed handler
// requests.
func (r DecisionRequest) toBase() decisionRequestBase {
	return decisionRequestBase{
		RequestID:    r.RequestID,
		RunID:        r.RunID,
		ThreadID:     r.ThreadID,
		Source:       r.Source,
		ToolCallID:   r.ToolCallID,
		CreatedAt:    r.CreatedAt,
		Deadline:     r.Deadline,
		RetryAttempt: r.RetryAttempt,
	}
}

func approvalToDecision(r ApprovalResult) DecisionResult {
	switch r {
	case ApprovalApproved:
		return DecisionApproved
	case ApprovalRejected:
		return DecisionRejected
	default:
		// Treated as a protocol violation upstream (waitHandlerOutcome).
		return ""
	}
}

func questionToDecision(r QuestionResult) DecisionResult {
	switch r {
	case QuestionAnswered:
		return DecisionAnswered
	case QuestionRejected:
		return DecisionRejected
	default:
		return ""
	}
}

func containsDecision(list []DecisionResult, r DecisionResult) bool {
	for _, v := range list {
		if v == r {
			return true
		}
	}
	return false
}

func resultCompatibleWithKind(kind HumanDecisionKind, result DecisionResult) bool {
	switch kind {
	case HumanDecisionPermission, HumanDecisionPlanReview:
		return result == DecisionApproved || result == DecisionRejected || result == DecisionTimedOut || result == DecisionAborted
	case HumanDecisionQuestion:
		return result == DecisionAnswered || result == DecisionRejected || result == DecisionTimedOut || result == DecisionAborted
	}
	return false
}

func stringFrom(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

func mapFrom(m map[string]any, key string) map[string]any {
	if m == nil {
		return nil
	}
	if v, ok := m[key].(map[string]any); ok {
		return v
	}
	return nil
}

// errAbortFromFailure returns a stable sentinel used to indicate that an
// adapter's RequestDecision should unwind due to an abort decision. Adapters
// propagate it upward; the runner translates it into RunResult.Failure via
// pendingFailure.
var errDecisionAbort = errors.New("agentadaptor: human decision aborted")

func errAbortFromFailure(FailureCode) error { return errDecisionAbort }

func decisionCancelMessage(err error) string {
	if err == nil {
		return "decision aborted"
	}
	return err.Error()
}

func decisionFailureMessage(decision DecisionResult, kind HumanDecisionKind, source string) string {
	src := source
	if src == "" {
		src = string(kind)
	}
	switch decision {
	case DecisionRejected:
		return fmt.Sprintf("human decision rejected: kind=%s source=%s", kind, src)
	case DecisionTimedOut:
		return fmt.Sprintf("human decision timed out: kind=%s source=%s", kind, src)
	default:
		return fmt.Sprintf("human decision failed (%s): kind=%s source=%s", decision, kind, src)
	}
}
