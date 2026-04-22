package agentadaptor

import (
	"context"
	"errors"
	"fmt"
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

func (r *runnerImpl) Run(ctx context.Context, prompt string, opts ...RunOption) (RunResult, error) {
	return r.run(ctx, prompt, wrapWithSeq(noopEventSink{}), opts...)
}

func (r *runnerImpl) Start(ctx context.Context, prompt string, opts ...RunOption) (RunHandle, error) {
	runCtx, cancel := context.WithCancel(ctx)

	// Snapshot the sink settings from the SDK so per-run changes cannot race
	// with config mutation on the SDK struct.
	streaming := r.resolveStreamingForStart(opts...)
	runBuf, streamBuf, policy := r.sdk.eventSinkSettings()
	sink := newDualSink(streaming, runBuf, streamBuf, policy)

	runID := newRunID(r.binding.Adapter().Descriptor().Type)
	handle := &asyncRunHandle{
		runID:  runID,
		events: sink.runEvents,
		stream: sink.stream,
		cancel: cancel,
		done:   make(chan asyncRunResult, 1),
	}

	// Pre-bind runID so the caller has access to it before Wait() completes.
	boundOpts := append([]RunOption{withPresetRunID(runID)}, opts...)

	go func() {
		result, err := r.run(runCtx, prompt, wrapWithSeq(sink), boundOpts...)
		sink.close()
		handle.done <- asyncRunResult{result: result, err: err}
		close(handle.done)
	}()

	return handle, nil
}

// resolveStreamingForStart is a lightweight pre-check used by Start() to size
// the sink. resolveInvocation computes the authoritative streaming flag
// (including binding defaults); this method mirrors that logic but only uses
// information available before the full resolution runs.
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

	policy := mergeRunPolicy(defaults.RunPolicy, resolvedOpts.runPolicy)

	instructions := cloneInstructions(defaults.Instructions)
	if resolvedOpts.instructions != nil {
		instructions = cloneInstructions(resolvedOpts.instructions)
	}

	metadata := mergeStringMaps(defaults.Metadata, resolvedOpts.metadata)
	config := r.binding.Config()
	common := extractCommonConfig(config)

	// Resolve streaming tri-state: per-call > per-binding > default off.
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
		policy:       policy,
		instructions: instructions,
		session:      sessionReq,
		metadata:     cloneStringMap(metadata),
		fingerprint:  fingerprint,
		streaming:    streaming,
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
		Policy:       invocation.policy,
		Instructions: invocation.instructions,
		Metadata:     cloneStringMap(invocation.metadata),
		Streaming:    invocation.streaming,
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

// seqSink wraps an EventSink and assigns monotonically increasing sequence
// numbers for the enclosing run. RunEvent.Seq and StreamPayload.Sequence use
// independent counters so bridges that only consume StreamEvents see a
// contiguous sequence regardless of RunEvent traffic.
type seqSink struct {
	inner         EventSink
	eventCounter  atomic.Uint64
	streamCounter atomic.Uint64
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
	payload.Sequence = s.streamCounter.Add(1)
	if payload.Timestamp.IsZero() {
		payload.Timestamp = time.Now().UTC()
	}
	return s.inner.EmitStream(payload)
}

type noopEventSink struct{}

func (noopEventSink) Emit(RunEvent) error             { return nil }
func (noopEventSink) EmitStream(StreamPayload) error  { return nil }

// EventBackpressure selects how the SDK reacts when a host cannot keep up with
// StreamPayload delivery. RunEvent delivery always falls back to the legacy
// drop-with-marker behaviour and is not affected by this setting.
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

// dualSink is the canonical runtime event sink used by Start(). It carries
// two independent channels: one for operational RunEvents and one for
// structured StreamPayloads. The stream channel is pre-closed when streaming
// is disabled so hosts can `for range handle.StreamEvents()` unconditionally.
//
// The sink is fed by a single adapter goroutine (see the run() loop) and
// drained by at most two host goroutines. close() is called by the run
// goroutine exactly once after adapter.Run returns, so close races with
// in-flight Emit/EmitStream calls only when an adapter violates the
// DriverAdapter contract (i.e. leaves background goroutines alive after
// Run returns). Block mode defends against that violation via the `done`
// channel; Drop mode is naturally safe because of its default branch.
type dualSink struct {
	runEvents chan RunEvent
	stream    chan StreamPayload
	streaming bool
	policy    EventBackpressure

	done chan struct{}
	// activeStream tracks in-flight EmitStream calls. close() waits for it
	// to drain before closing the stream channel, eliminating the race
	// between a blocking sender and channel closure.
	activeStream sync.WaitGroup

	once       sync.Once
	mu         sync.Mutex
	closed     bool
	droppedRun int
	droppedStm int
}

func newDualSink(streaming bool, runBuf, streamBuf int, policy EventBackpressure) *dualSink {
	if runBuf <= 0 {
		runBuf = defaultRunEventBuffer
	}
	if streamBuf <= 0 {
		streamBuf = defaultStreamEventBuffer
	}
	s := &dualSink{
		runEvents: make(chan RunEvent, runBuf),
		stream:    make(chan StreamPayload, streamBuf),
		streaming: streaming,
		policy:    policy,
		done:      make(chan struct{}),
	}
	if !streaming {
		// Pre-close so consumers can range over StreamEvents() immediately.
		close(s.stream)
	}
	return s
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
	// Register as an active sender before doing anything else so close()
	// will wait for us to finish.
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
	// Invariant: caller has already incremented activeStream and will
	// decrement it on return. The select below races against done (closed
	// by close()) so a pending close can unblock this goroutine.
	select {
	case <-s.done:
		return nil
	case s.stream <- payload:
		return nil
	}
}

func (s *dualSink) close() {
	s.once.Do(func() {
		// Flip closed before signalling done so no new EmitStream registers
		// after this point.
		s.mu.Lock()
		s.closed = true
		close(s.done)
		s.mu.Unlock()

		// Wait for in-flight senders to return (done unblocks them). After
		// Wait returns, no goroutine holds a reference to the stream channel
		// for sending, so closing it is safe.
		s.activeStream.Wait()

		s.mu.Lock()
		defer s.mu.Unlock()
		s.flushDroppedRunLocked()
		s.flushDroppedStreamLocked()
		close(s.runEvents)
		if s.streaming {
			close(s.stream)
		}
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

type asyncRunResult struct {
	result RunResult
	err    error
}

type asyncRunHandle struct {
	runID  string
	events <-chan RunEvent
	stream <-chan StreamPayload
	cancel context.CancelFunc
	done   chan asyncRunResult
}

func (h *asyncRunHandle) Events() <-chan RunEvent {
	return h.events
}

func (h *asyncRunHandle) StreamEvents() <-chan StreamPayload {
	return h.stream
}

func (h *asyncRunHandle) RunID() string {
	return h.runID
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

func newItemEvent(item TranscriptItem) RunEvent {
	clone := cloneTranscriptItem(item)
	return RunEvent{
		Type:      RunEventItem,
		Timestamp: time.Now().UTC(),
		Item:      &clone,
	}
}
