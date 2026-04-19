package agentadaptor

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

type runnerImpl struct {
	sdk       *sdkImpl
	name      string
	isDefault bool
	binding   AgentBinding
}

func (r *runnerImpl) Run(ctx context.Context, prompt string, opts ...RunOption) (RunResult, error) {
	return r.run(ctx, prompt, noopEventSink{}, opts...)
}

func (r *runnerImpl) Start(ctx context.Context, prompt string, opts ...RunOption) (RunHandle, error) {
	runCtx, cancel := context.WithCancel(ctx)
	eventSink := newChannelEventSink()
	handle := &asyncRunHandle{
		events: eventSink.events,
		cancel: cancel,
		done:   make(chan asyncRunResult, 1),
	}

	go func() {
		result, err := r.run(runCtx, prompt, eventSink, opts...)
		eventSink.close()
		handle.done <- asyncRunResult{result: result, err: err}
		close(handle.done)
	}()

	return handle, nil
}

func (r *runnerImpl) run(ctx context.Context, prompt string, sink EventSink, opts ...RunOption) (RunResult, error) {
	invocation, cleanup, err := r.resolveInvocation(ctx, prompt, opts...)
	if cleanup != nil {
		defer cleanup()
	}
	if err != nil {
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
		return RunResult{}, err
	}
	result.Session = sessionRef
	return result, nil
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

	skillRefs := r.sdk.desiredSkillsFor(r.name, defaults.Skills)
	if len(resolvedOpts.skills) > 0 {
		skillRefs = cloneStrings(resolvedOpts.skills)
	}

	permissions := PermissionProfile{}
	if defaults.Permissions != nil {
		permissions = *defaults.Permissions
	}
	if resolvedOpts.permissions != nil {
		permissions = *resolvedOpts.permissions
	}

	instructions := cloneInstructions(defaults.Instructions)
	if resolvedOpts.instructions != nil {
		instructions = cloneInstructions(resolvedOpts.instructions)
	}

	metadata := mergeStringMaps(defaults.Metadata, resolvedOpts.metadata)
	config := r.binding.Config()
	common := extractCommonConfig(config)

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

	runID := newRunID(r.binding.Adapter().Descriptor().Type)

	runtimePayload, err := r.sdk.prepareRuntime(ctx, runID, r.binding, identity, workspace, metadata, runtimeOverride)
	if err != nil {
		_ = r.sdk.workspaceManager.Release(context.Background(), workspace, WorkspaceReleaseKeep)
		return resolvedInvocation{}, nil, err
	}

	cleanup := func() {
		if runID != "" {
			_ = r.sdk.runtimeManager.ReleaseByRun(context.Background(), runID)
		}
		_ = r.sdk.workspaceManager.Release(context.Background(), workspace, WorkspaceReleaseKeep)
	}

	skillPayload, err := r.sdk.prepareSkills(ctx, r.binding, identity, workspace, skillRefs)
	if err != nil {
		cleanup()
		return resolvedInvocation{}, nil, err
	}

	sessionReq := SessionRequest{}
	if resolvedOpts.session != nil {
		sessionReq = *resolvedOpts.session
	}

	fingerprint := stableHash(
		r.binding.Adapter().Descriptor().Type,
		identity,
		extractDriverFingerprint(config),
		workspace.Fingerprint,
		runtimePayload.Fingerprint,
		instructionFingerprint(instructions),
		skillPayload.Fingerprint,
	)

	return resolvedInvocation{
		runID:        runID,
		prompt:       prompt,
		adapter:      r.binding.Adapter(),
		config:       config,
		agent:        identity,
		workspace:    workspace,
		runtime:      runtimePayload,
		skills:       skillPayload,
		permissions:  permissions,
		instructions: instructions,
		session:      sessionReq,
		metadata:     cloneStringMap(metadata),
		fingerprint:  fingerprint,
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
		RunID:        invocation.runID,
		Prompt:       invocation.prompt,
		Config:       invocation.config,
		Agent:        invocation.agent,
		Workspace:    invocation.workspace,
		Runtime:      cloneRuntimePayload(invocation.runtime),
		Skills:       invocation.skills,
		Permissions:  invocation.permissions,
		Instructions: invocation.instructions,
		Metadata:     cloneStringMap(invocation.metadata),
	}
	if plan != nil {
		var state *DriverSessionState
		if plan.record != nil {
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
		Transcript:      cloneTranscriptItems(runResult.Transcript),
		ExitCode:        runResult.ExitCode,
		Signal:          runResult.Signal,
		TimedOut:        runResult.TimedOut,
		Usage:           runResult.Usage,
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

type noopEventSink struct{}

func (noopEventSink) Emit(RunEvent) error { return nil }

type channelEventSink struct {
	events  chan RunEvent
	once    sync.Once
	mu      sync.Mutex
	dropped int
	closed  bool
}

func newChannelEventSink() *channelEventSink {
	return &channelEventSink{
		events: make(chan RunEvent, 64),
	}
}

func (s *channelEventSink) Emit(event RunEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return nil
	}
	s.flushDroppedLocked()
	select {
	case s.events <- event:
		return nil
	default:
		s.dropped++
		return nil
	}
}

func (s *channelEventSink) close() {
	s.once.Do(func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.closed {
			return
		}
		s.flushDroppedLocked()
		s.closed = true
		close(s.events)
	})
}

func (s *channelEventSink) flushDroppedLocked() {
	if s.dropped == 0 {
		return
	}
	summary := newEvent(RunEventLifecycle, fmt.Sprintf("dropped %d events because consumer was slow", s.dropped))
	summary.Metadata = map[string]string{
		"reason":        "overflow",
		"dropped_count": fmt.Sprintf("%d", s.dropped),
	}
	select {
	case s.events <- summary:
		s.dropped = 0
	default:
	}
}

type asyncRunResult struct {
	result RunResult
	err    error
}

type asyncRunHandle struct {
	events <-chan RunEvent
	cancel context.CancelFunc
	done   chan asyncRunResult
}

func (h *asyncRunHandle) Events() <-chan RunEvent {
	return h.events
}

func (h *asyncRunHandle) Wait(ctx context.Context) (RunResult, error) {
	select {
	case <-ctx.Done():
		return RunResult{}, ctx.Err()
	case result, ok := <-h.done:
		if !ok {
			return RunResult{}, context.Canceled
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
	return stableHash(ref.ID, ref.Path)
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
