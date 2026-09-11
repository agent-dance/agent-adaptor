package a2adelegation

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/capability"
	clienta2a "github.com/agent-dance/agent-adaptor/clients/a2a"
	"github.com/agent-dance/agent-adaptor/internal/activebudget"
)

const remoteCancelTimeout = 5 * time.Second
const lifecycleHookTimeout = 5 * time.Second

var delegationIDCounter atomic.Uint64

// A2AStream is the minimal ordered event stream required by Delegator. Close
// must unblock an in-flight Recv. Implementations may additionally provide
// RecvContext(context.Context) for native cancellation.
type A2AStream interface {
	// Recv blocks until the next event. Full Task snapshots are historical;
	// live Status/Message or explicit RecoveredState identify current outcomes.
	// Close must unblock any in-flight Recv.
	Recv() (clienta2a.Event, error)
	Close() error
}

type contextA2AStream interface {
	RecvContext(context.Context) (clienta2a.Event, error)
}

// A2AClient is the protocol-shaped client contract used by Delegator. The
// bundled clients/a2a adapter and Local in-process targets both implement it.
type A2AClient interface {
	AgentCard(ctx context.Context) (clienta2a.AgentCard, error)
	Send(ctx context.Context, req clienta2a.SendRequest) (clienta2a.Task, error)
	SendStream(ctx context.Context, req clienta2a.SendRequest) (A2AStream, error)
	GetTask(ctx context.Context, req clienta2a.GetTaskRequest) (clienta2a.Task, error)
	CancelTask(ctx context.Context, req clienta2a.CancelTaskRequest) (clienta2a.Task, error)
}

// ClientFactory constructs the client for one resolved RemoteAgentSpec.
type ClientFactory func(RemoteAgentSpec) A2AClient

// Delegator resolves curated targets, executes A2A tasks, and publishes their
// normalized lifecycle to an EventBus. Construct it with NewDelegator; Service
// owns the common production wiring.
type Delegator struct {
	Registry       *Registry
	Bus            *EventBus
	NewClient      ClientFactory
	NewID          func() string
	LifecycleHook  DelegationLifecycleHook
	statusDecoders []StatusPartDecoder

	// publishMu gives all producers one acceptance order. beforePublish is a
	// package-private integration seam used by Service to index delegations
	// before their Started event becomes visible to EventBus subscribers.
	publishMu     sync.Mutex
	beforePublish func(DelegationEvent)
	publishRun    func(context.Context, DelegationEvent) error
	recordFinal   func(DelegationRequest, DelegationResult)
	budgetClock   activebudget.Clock
}

// DelegatorOption configures a Delegator during construction.
type DelegatorOption func(*Delegator)

// WithStatusPartDecoder registers a host-owned Status DataPart schema decoder.
func WithStatusPartDecoder(decoder StatusPartDecoder) DelegatorOption {
	return func(d *Delegator) {
		if decoder != nil {
			d.statusDecoders = append(d.statusDecoders, decoder)
		}
	}
}

// NewDelegator constructs a Delegator over registry and bus. Nil options are
// ignored; runtime configuration errors are returned by Delegate.
func NewDelegator(registry *Registry, bus *EventBus, opts ...DelegatorOption) *Delegator {
	d := &Delegator{
		Registry: registry,
		Bus:      bus,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(d)
		}
	}
	return d
}

type delegationRun struct {
	*Delegator
	publishEvent   func(DelegationEvent)
	ctx            context.Context
	parent         context.Context
	budget         *activebudget.Controller
	expired        *adaptor.ActiveExecutionTimeoutError
	client         A2AClient
	taskID, tenant string
	cancelWanted   bool
	mapper         *eventMapper
}

// Delegate executes one curated Local or Remote target, publishes ordered
// DelegationEvent values, and returns its structured terminal result. Failure
// returns both the available result and a *DelegationError. Each call owns a
// fresh ActiveExecutionTimeout budget; transport retries and recovery share
// that budget. Timeout retains its absolute wall-clock meaning. Member Policy
// is unchanged, and Member approval waits do not pause the delegator's budget.
// BeforeDelegate must succeed with a healthy context before invocation facts
// start. AfterDelegate runs after budget settlement and determines the single
// terminal fact. Known-task cancellation and AfterDelegate have independent
// five-second bounds; cleanup failures never replace an established primary.
func (d *Delegator) Delegate(ctx context.Context, req DelegationRequest) (out DelegationResult, err error) {
	if d == nil {
		return DelegationResult{}, &DelegationError{Code: "configuration_error", Message: "delegation registry is required"}
	}
	// 1. Allocate stable identity before resolution so early failures still emit a terminal event.
	delegationID := d.newID()
	if delegationID == "" {
		delegationID = "del-unknown"
	}
	baseEvent := DelegationEvent{
		RunID:            req.RunID,
		ParentToolCallID: req.ParentToolCallID,
		ScopeID:          req.ScopeID, ParentScopeID: req.ParentScopeID,
		DelegationID: delegationID,
		AgentKey:     req.Agent,
		AgentName:    req.Agent,
		Protocol:     ProtocolA2A,
	}
	if d.Registry == nil {
		derr := &DelegationError{Code: "configuration_error", Message: "delegation registry is required"}
		derr.Cause = errors.Join(derr.Cause, d.publishContext(ctx, failedEvent(baseEvent, derr)))
		return DelegationResult{DelegationID: delegationID, Agent: req.Agent, RemoteProtocol: ProtocolA2A, Status: "failed", Error: derr}, derr
	}
	spec, ok := d.Registry.Lookup(req.Agent)
	if !ok {
		derr := &DelegationError{Code: "agent_not_found", Message: fmt.Sprintf("remote agent %q is not registered", req.Agent)}
		derr.Cause = errors.Join(derr.Cause, d.publishContext(ctx, failedEvent(baseEvent, derr)))
		return DelegationResult{DelegationID: delegationID, Agent: req.Agent, RemoteProtocol: ProtocolA2A, Status: "failed", Error: derr}, derr
	}
	baseEvent.AgentKey = spec.Key
	baseEvent.AgentName = displayName(spec)
	baseResult := DelegationResult{DelegationID: delegationID, Agent: spec.Key, RemoteProtocol: ProtocolA2A, Status: "running"}
	if req.ActiveExecutionTimeout < 0 || spec.Policy.MaxActiveExecutionTimeout < 0 {
		derr := &DelegationError{Code: "invalid_policy", Message: "active execution timeout must be nonnegative"}
		derr.Cause = errors.Join(derr.Cause, d.publishContext(ctx, failedEvent(baseEvent, derr)))
		return ensureDelegationError(baseResult, derr), derr
	}
	timeout := clampTimeout(req.Timeout, spec.Policy.MaxTimeout)
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	parent := ctx
	limit := clampTimeout(req.ActiveExecutionTimeout, spec.Policy.MaxActiveExecutionTimeout)
	expired := &adaptor.ActiveExecutionTimeoutError{Limit: limit}
	ctx, budget := activebudget.New(parent, limit, expired, d.budgetClock)
	defer budget.Cancel(context.Canceled)
	publisher := &terminalEventBuffer{parent: d, ctx: ctx}
	run := &delegationRun{Delegator: d, publishEvent: publisher.publish, ctx: ctx, parent: parent, budget: budget, expired: expired, tenant: spec.Tenant}
	if req.Tenant != "" {
		run.tenant = req.Tenant
	}
	if req.Message != nil {
		run.taskID = req.Message.TaskID
	}
	started := false
	var startTime time.Time
	defer func() {
		// Finish and primary selection precede detached cleanup. SelectedCause
		// uses this controller's expired identity, never an inherited Leader limit.
		if primary := run.complete(err); primary != nil {
			err = primary
			out = ensureDelegationError(out, primary)
			publisher.replace(failedEvent(baseEvent, primary))
		}
		budget.Stop()
		if publisher.err != nil && err != nil {
			cp := *delegationErr(err)
			cp.Cause = errors.Join(cp.Cause, publisher.err)
			err = &cp
			out.Error = &cp
		}
		if publisher.err != nil && err == nil {
			err = &DelegationError{Code: "infrastructure_error", Message: "run event publication failed", Cause: publisher.err}
			out = ensureDelegationError(out, err.(*DelegationError))
		}
		if ctx.Err() != nil || run.budget.SelectedCause() == expired {
			run.cancelWanted = true
		}
		if run.cancelWanted && run.taskID != "" {
			if run.client == nil {
				run.client = d.clientFor(spec)
			}
			if run.client != nil {
				run.cancelKnown(ctx, &out)
			}
		}
		if started && d.LifecycleHook != nil {
			afterCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), lifecycleHookTimeout)
			payload := AfterDelegation{DelegationID: delegationID, AgentSpec: cloneRemoteAgentSpec(spec), Request: cloneDelegationRequest(req), Result: cloneDelegationResult(out), Err: err}
			hookErr := boundedHook(afterCtx, func() error { return d.LifecycleHook.AfterDelegate(afterCtx, payload) })
			cancel()
			if hookErr != nil {
				if err == nil {
					derr := lifecycleHookError("workflow_after_failed", hookErr)
					err = derr
					out = ensureDelegationError(out, derr)
				} else {
					derr := delegationErr(err)
					cp := *derr
					cp.Cause = errors.Join(derr.Cause, hookErr)
					err = &cp
					out.Error = &cp
				}
			}
		}
		if d.recordFinal != nil {
			d.recordFinal(req, cloneDelegationResult(out))
		}
		// Tail publication has an independent bound, and the core revocation
		// fence still rejects events after Detach. No EventBus replay feeds it.
		tailCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), lifecycleHookTimeout)
		defer cancel()
		publisher.ctx = tailCtx
		if started {
			v := outerInvocation(baseEvent, capability.Completed, time.Now())
			elapsed := time.Since(startTime)
			v.Duration = &elapsed
			if err != nil {
				v.Phase = capability.Failed
				v.ErrorCode = capability.DelegationFailed
				if out.Error != nil && out.Error.Code == "cancelled" {
					v.Phase = capability.Cancelled
					v.ErrorCode = capability.RunCancelled
				}
			}
			ev := baseEvent
			ev.Kind = DelegationCapabilityInvocation
			ev.Capability = &v
			publisher.publish(ev)
		}
		if err != nil {
			terminal := failedEvent(baseEvent, out.Error)
			terminal.RemoteTaskID = out.RemoteTaskID
			publisher.replace(terminal)
		}
		publisher.flush()
		if publisher.err != nil && !errors.Is(err, publisher.err) {
			derr := &DelegationError{Code: "infrastructure_error", Message: "run event publication failed", Cause: publisher.err}
			if err != nil {
				cp := *delegationErr(err)
				cp.Cause = errors.Join(cp.Cause, publisher.err)
				derr = &cp
			}
			out = ensureDelegationError(out, derr)
			err = derr
			if d.recordFinal != nil {
				d.recordFinal(req, cloneDelegationResult(out))
			}
		}
	}()
	if d.LifecycleHook != nil {
		beforeCtx, beforeCancel := context.WithTimeout(ctx, lifecycleHookTimeout)
		payload := BeforeDelegation{DelegationID: delegationID, AgentSpec: cloneRemoteAgentSpec(spec), Request: cloneDelegationRequest(req)}
		hookErr := boundedHook(beforeCtx, func() error { return d.LifecycleHook.BeforeDelegate(beforeCtx, payload) })
		beforeCancel()
		if hookErr != nil {
			derr := lifecycleHookError("workflow_before_failed", hookErr)
			run.publish(failedEvent(baseEvent, derr))
			return ensureDelegationError(baseResult, derr), derr
		}
	}
	if derr := run.contextFailure(); derr != nil {
		return ensureDelegationError(baseResult, derr), derr
	}
	started = true
	startTime = time.Now()
	value := outerInvocation(baseEvent, capability.Started, startTime)
	outer := baseEvent
	outer.Kind = DelegationCapabilityInvocation
	outer.Capability = &value
	run.publish(outer)
	if publisher.err != nil {
		derr := &DelegationError{Code: "infrastructure_error", Message: "run event publication failed", Cause: publisher.err}
		return ensureDelegationError(baseResult, derr), derr
	}

	// 3. Resolve the remote card and enforce host policy before sending work.
	client := d.clientFor(spec)
	run.client = client
	if client == nil {
		derr := &DelegationError{Code: "configuration_error", Message: "remote agent requires AgentCardURL for default A2A execution; configure Delegator.NewClient for static AgentCard-only specs"}
		run.publish(failedEvent(baseEvent, derr))
		baseResult.Status = "failed"
		baseResult.Error = derr
		return baseResult, derr
	}
	card, err := client.AgentCard(ctx)
	if err != nil && spec.AgentCard == nil {
		derr := &DelegationError{Code: "agent_unavailable", Message: err.Error(), Retryable: true, Cause: err}
		run.publish(failedEvent(baseEvent, derr))
		baseResult.Status = "failed"
		baseResult.Error = derr
		return baseResult, derr
	}
	if derr := run.contextFailure(); derr != nil {
		return ensureDelegationError(baseResult, derr), derr
	}
	if spec.AgentCard != nil {
		card = *spec.AgentCard
	}
	if spec.Policy.RequireStreaming && !card.Capabilities.Streaming {
		derr := &DelegationError{Code: "capability_unsupported", Message: "remote agent does not advertise streaming"}
		run.publish(failedEvent(baseEvent, derr))
		baseResult.Status = "failed"
		baseResult.Error = derr
		return baseResult, derr
	}

	// 4. Build one protocol request and select streaming or polling execution.
	message, err := messageForDelegation(req)
	if err != nil {
		derr := delegationErr(err)
		run.publish(failedEvent(baseEvent, derr))
		baseResult.Status = "failed"
		baseResult.Error = derr
		return baseResult, derr
	}
	send := clienta2a.SendRequest{
		Message:             message,
		ContextID:           effectiveContextID(req),
		Tenant:              spec.Tenant,
		AcceptedOutputModes: spec.AcceptedOutputModes,
		HistoryLength:       req.HistoryLength,
		Metadata:            cloneAnyMap(req.Metadata),
	}
	if req.Tenant != "" {
		send.Tenant = req.Tenant
	}
	if req.Stream || card.Capabilities.Streaming {
		return run.delegateStreaming(ctx, client, spec, send, baseEvent, baseResult, req.MaxArtifacts, req.IncludeRemoteArtifacts)
	}
	return run.delegatePolling(ctx, client, spec, send, baseEvent, baseResult, req.MaxArtifacts, req.IncludeRemoteArtifacts)
}

func (r *delegationRun) delegateStreaming(ctx context.Context, client A2AClient, spec RemoteAgentSpec, send clienta2a.SendRequest, baseEvent DelegationEvent, baseResult DelegationResult, maxArtifacts *int, includeRemoteArtifacts bool) (DelegationResult, error) {
	// 1. Open the stream, falling back to polling only when policy allows it.
	stream, err := client.SendStream(ctx, send)
	if err != nil {
		if ctx.Err() != nil {
			derr := r.contextFailure()
			return ensureDelegationError(baseResult, derr), derr
		}
		if spec.Policy.RequireStreaming {
			derr := &DelegationError{Code: "stream_unavailable", Message: err.Error(), Retryable: true, Cause: err}
			r.publish(failedEvent(baseEvent, derr))
			baseResult.Status = "failed"
			baseResult.Error = derr
			return baseResult, derr
		}
		return r.delegatePolling(ctx, client, spec, send, baseEvent, baseResult, maxArtifacts, includeRemoteArtifacts)
	}
	defer stream.Close()
	// 2. Track remote identity and mapper state for recovery and lifecycle closure.
	mapper := r.eventMapper(baseEvent)
	mapper.includeRemoteArtifacts = includeRemoteArtifacts
	mapper.maxArtifactBytes = spec.Policy.MaxArtifactBytes
	var currentTask clienta2a.Task
	var snapshot *clienta2a.Task
	lastTaskID := send.Message.TaskID
	liveArtifacts := make(map[string]bool)
	// Historical replay restores only artifacts not updated live. Explicit
	// GetTask recovery uses a separate reconciliation rule below.
	restoreArtifacts := func(artifacts []clienta2a.Artifact) []clienta2a.Artifact {
		var restored []clienta2a.Artifact
		for _, artifact := range artifacts {
			if liveArtifacts[artifact.ID] {
				continue
			}
			mergeStreamArtifact(&currentTask, artifact, false)
			restored = append(restored, artifact)
		}
		return restored
	}
	recoverArtifacts := func(artifacts []clienta2a.Artifact) {
		for _, artifact := range artifacts {
			if reconcileRecoveredArtifact(&currentTask, artifact, liveArtifacts[artifact.ID]) {
				// The complete query is authoritative when neither content
				// view is a prefix. Report the conflict without payload data.
				event := mapper.base
				event.Kind = DelegationStreamDropped
				event.RemoteTaskID = currentTask.ID
				event.RemoteContextID = currentTask.ContextID
				event.RemoteArtifactID = artifact.ID
				event.Raw = map[string]any{"reason": "artifact_recovery_conflict", "resolution": "recovered_snapshot"}
				r.publish(event)
			}
		}
	}
	cancelResult := func() (DelegationResult, error) {
		r.publishAll(mapper.closeOpen(lastTaskID, send.ContextID))
		r.taskID = lastTaskID
		r.cancelWanted = true
		derr := r.contextFailure()
		if derr == nil {
			derr = &DelegationError{Code: "cancelled", Message: "execution cancelled", Cause: ctx.Err(), Retryable: true}
		}
		currentTask.ID = lastTaskID
		if local, ok := stream.(*localA2AStream); ok {
			r.budget.Stop()
			cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), lifecycleHookTimeout)
			task, cause := local.cancelledTask(cleanup)
			cancel()
			if task.ID != "" {
				currentTask = task
				lastTaskID = task.ID
				r.taskID = task.ID
			}
			derr.Cause = errors.Join(derr.Cause, cause)
		}
		result := partialTaskResult(baseResult, currentTask, spec.Policy, includeRemoteArtifacts)
		result.Artifacts = r.limitResultArtifacts(baseEvent, result.Artifacts, maxArtifacts)
		result = ensureDelegationError(result, derr)
		r.publish(failedEvent(baseEvent, derr))
		return result, derr
	}

	cancelKnownTask := func() {
		r.scheduleRemoteCancel(client, lastTaskID, send.Tenant)
	}
	interruptedResult := func(derr *DelegationError) (DelegationResult, error) {
		currentTask.Status = clienta2a.TaskStatus{State: clienta2a.TaskStateFailed}
		// Preserve permitted partial artifacts without letting interruption bypass
		// the content boundary used by successful completion.
		result := partialTaskResult(baseResult, currentTask, spec.Policy, includeRemoteArtifacts)
		result.Artifacts = r.limitResultArtifacts(baseEvent, result.Artifacts, maxArtifacts)
		result.Error = derr
		return result, derr
	}
	// 3. Receive and publish ordered events until a protocol terminal is observed.
	for {
		select {
		case <-ctx.Done():
			return cancelResult()
		default:
		}
		event, recvErr := receiveA2AStream(ctx, stream)
		err = recvErr
		if err == io.EOF {
			break
		}
		if err != nil {
			if ctx.Err() != nil && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) {
				return cancelResult()
			}
			if lastTaskID != "" {
				if recovered, ok := r.recoverTask(ctx, client, lastTaskID, send.Tenant, send.HistoryLength); ok && !staleRecoveredTask(recovered, snapshot, send.Message.TaskID != "") {
					recoverArtifacts(recovered.Artifacts)
					recovered.Artifacts = currentTask.Artifacts
					r.publishAll(mapper.taskEvents(recovered))
					return r.finishTask(baseEvent, baseResult, recovered, spec.Policy, maxArtifacts, includeRemoteArtifacts, mapper)
				}
				cancelKnownTask()
			}
			derr := &DelegationError{Code: "stream_interrupted", Message: err.Error(), Retryable: true, Cause: err}
			r.publishAll(mapper.closeOpen(lastTaskID, send.ContextID))
			r.publish(failedEvent(baseEvent, derr))
			return interruptedResult(derr)
		}
		if event.TaskID != "" {
			lastTaskID = event.TaskID
			r.taskID = lastTaskID
			currentTask.ID = event.TaskID
		}
		if event.ContextID != "" {
			currentTask.ContextID = event.ContextID
		}
		if event.Message != nil && event.Message.TaskID != "" {
			lastTaskID = event.Message.TaskID
			r.taskID = lastTaskID
			currentTask.ID = event.Message.TaskID
		}
		if event.Task != nil {
			lastTaskID = event.Task.ID
			r.taskID = lastTaskID
			currentTask.ID = event.Task.ID
			currentTask.ContextID = event.Task.ContextID
			if persistedTaskEvent(event) {
				if event.RecoveredState {
					if staleRecoveredTask(*event.Task, snapshot, send.Message.TaskID != "") {
						break
					}
					recovered := *event.Task
					recoverArtifacts(recovered.Artifacts)
					recovered.Artifacts = currentTask.Artifacts
					event.Task = &recovered
					r.publishAll(mapper.Map(event))
					return r.finishTask(baseEvent, baseResult, recovered, spec.Policy, maxArtifacts, includeRemoteArtifacts, mapper)
				}
				snapshot = event.Task
				projected := *event.Task
				projected.Artifacts = restoreArtifacts(event.Task.Artifacts)
				event.Task = &projected
				r.publishAll(mapper.Map(event))
				continue
			}
			recoverArtifacts(event.Task.Artifacts)
			currentTask.Messages = event.Task.Messages
		}
		if event.Artifact != nil {
			liveArtifacts[event.Artifact.ID] = true
			mergeStreamArtifact(&currentTask, *event.Artifact, event.Append)
		}
		r.publishAll(mapper.Map(event))
		if event.Message != nil && (event.Kind == clienta2a.EventTerminal || event.Kind == clienta2a.EventMessage) {
			currentTask.Raw = event.Raw
			currentTask.Status = clienta2a.TaskStatus{State: clienta2a.TaskStateCompleted}
			currentTask.Messages = []clienta2a.Message{*event.Message}
			return r.finishTask(baseEvent, baseResult, currentTask, spec.Policy, maxArtifacts, includeRemoteArtifacts, mapper)
		}
		if event.Status != nil && executionFinalState(event.Status.State) {
			// GetTask can fill in final artifacts/history, but cannot replace a
			// live status or questionnaire with a stale persisted outcome.
			if recovered, ok := r.recoverTask(ctx, client, lastTaskID, send.Tenant, send.HistoryLength); ok &&
				recovered.Status.State == event.Status.State && !staleRecoveredTask(recovered, snapshot, send.Message.TaskID != "") {
				currentTask.Messages = recovered.Messages
				recoverArtifacts(recovered.Artifacts)
				currentTask.Status = recovered.Status
			}
			currentTask.Raw = event.Raw
			currentTask.Status.State = event.Status.State
			currentTask.Status.Timestamp = event.Status.Timestamp
			if event.Status.Message != nil {
				currentTask.Status.Message = event.Status.Message
			}
			return r.finishTask(baseEvent, baseResult, currentTask, spec.Policy, maxArtifacts, includeRemoteArtifacts, mapper)
		}
	}
	// EOF without a live terminal (including old completed/failed snapshots)
	// is interrupted. Servers returning only Task results must use Send/polling.
	cancelKnownTask()
	derr := &DelegationError{Code: "stream_interrupted", Message: "remote stream ended before terminal state", Retryable: true}
	r.publishAll(mapper.closeOpen(lastTaskID, send.ContextID))
	r.publish(failedEvent(baseEvent, derr))
	return interruptedResult(derr)
}

type streamRecv struct {
	event clienta2a.Event
	err   error
}

func receiveA2AStream(ctx context.Context, stream A2AStream) (clienta2a.Event, error) {
	if contextual, ok := stream.(contextA2AStream); ok {
		return contextual.RecvContext(ctx)
	}
	item := make(chan streamRecv, 1)
	go func() {
		event, err := stream.Recv()
		item <- streamRecv{event: event, err: err}
	}()
	select {
	case <-ctx.Done():
		_ = stream.Close()
		return clienta2a.Event{}, ctx.Err()
	case received := <-item:
		return received.event, received.err
	}
}

func (r *delegationRun) delegatePolling(ctx context.Context, client A2AClient, spec RemoteAgentSpec, send clienta2a.SendRequest, baseEvent DelegationEvent, baseResult DelegationResult, maxArtifacts *int, includeRemoteArtifacts bool) (DelegationResult, error) {
	send.ReturnImmediately = true
	task, err := client.Send(ctx, send)
	if err != nil {
		derr := &DelegationError{Code: "agent_unavailable", Message: err.Error(), Retryable: true, Cause: err}
		r.publish(failedEvent(baseEvent, derr))
		baseResult.Status = "failed"
		baseResult.Error = derr
		return baseResult, derr
	}
	mapper := r.eventMapper(baseEvent)
	mapper.includeRemoteArtifacts = includeRemoteArtifacts
	mapper.maxArtifactBytes = spec.Policy.MaxArtifactBytes
	r.taskID = task.ID
	r.publish(mapper.Started(task.ID, task.ContextID))
	r.publishAll(mapper.taskEvents(task))
	if executionFinalState(task.Status.State) {
		return r.finishTask(baseEvent, baseResult, task, spec.Policy, maxArtifacts, includeRemoteArtifacts, mapper)
	}
	interval := spec.Policy.PollInterval
	if interval <= 0 {
		interval = 500 * time.Millisecond
	}
	maxPolls := spec.Policy.MaxPolls
	if maxPolls <= 0 {
		maxPolls = 600
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for i := 0; i < maxPolls; i++ {
		select {
		case <-ctx.Done():
			r.publishAll(mapper.closeOpen(task.ID, task.ContextID))
			r.taskID = task.ID
			r.cancelWanted = true
			derr := r.contextFailure()
			result := partialTaskResult(baseResult, task, spec.Policy, includeRemoteArtifacts)
			return ensureDelegationError(result, derr), derr

		case <-ticker.C:
		}
		next, getErr := client.GetTask(ctx, clienta2a.GetTaskRequest{TaskID: task.ID, Tenant: send.Tenant, HistoryLength: send.HistoryLength})
		if getErr != nil {
			continue
		}
		task = next
		r.taskID = task.ID
		r.publishAll(mapper.taskEvents(task))
		if executionFinalState(task.Status.State) {
			return r.finishTask(baseEvent, baseResult, task, spec.Policy, maxArtifacts, includeRemoteArtifacts, mapper)
		}
	}
	r.scheduleRemoteCancel(client, task.ID, send.Tenant)
	derr := &DelegationError{Code: "remote_timeout", Message: "remote task did not finish before timeout", Retryable: true, RemoteStatus: string(task.Status.State)}
	r.publishAll(mapper.closeOpen(task.ID, task.ContextID))
	r.publish(failedEvent(baseEvent, derr))
	result := partialTaskResult(baseResult, task, spec.Policy, includeRemoteArtifacts)
	result.Artifacts = r.limitResultArtifacts(baseEvent, result.Artifacts, maxArtifacts)
	return ensureDelegationError(result, derr), derr
}

func (d *Delegator) recoverTask(ctx context.Context, client A2AClient, taskID, tenant string, historyLength *int) (clienta2a.Task, bool) {
	if taskID == "" {
		return clienta2a.Task{}, false
	}
	task, err := client.GetTask(ctx, clienta2a.GetTaskRequest{TaskID: taskID, Tenant: tenant, HistoryLength: historyLength})
	return task, err == nil && executionFinalState(task.Status.State)
}

// Cancellation runs during bounded cleanup, after the active budget is settled.
func (r *delegationRun) scheduleRemoteCancel(client A2AClient, taskID, tenant string) {
	if taskID != "" {
		r.taskID = taskID
		r.client = client
		r.tenant = tenant
		r.cancelWanted = true
	}
}

func (d *Delegator) publishContext(ctx context.Context, ev DelegationEvent) error {
	if d == nil || ev.RunID == "" {
		return nil
	}
	d.publishMu.Lock()
	defer d.publishMu.Unlock()
	ev = cloneDelegationEvent(ev)
	if d.publishRun != nil {
		if err := d.publishRun(ctx, ev); err != nil {
			return err
		}
	}
	if d.beforePublish != nil {
		d.beforePublish(cloneDelegationEvent(ev))
	}
	if d.Bus != nil {
		d.Bus.Publish(ev)
	}
	return nil
}

func (r *delegationRun) publish(ev DelegationEvent) {
	r.publishEvent(ev)
}

func (r *delegationRun) publishAll(events []DelegationEvent) {
	for _, ev := range events {
		r.publish(ev)
	}
}

type terminalEventBuffer struct {
	parent   *Delegator
	terminal *DelegationEvent
	ctx      context.Context
	err      error
}

func (b *terminalEventBuffer) publish(ev DelegationEvent) {
	if isTerminal(ev.Kind) {
		if b.terminal == nil {
			copyEvent := cloneDelegationEvent(ev)
			b.terminal = &copyEvent
		}
		return
	}
	if err := b.parent.publishContext(b.ctx, ev); err != nil && b.err == nil {
		b.err = err
	}
}

func (b *terminalEventBuffer) replace(ev DelegationEvent) {
	copyEvent := cloneDelegationEvent(ev)
	b.terminal = &copyEvent
}

func (b *terminalEventBuffer) flush() {
	if b == nil || b.parent == nil || b.terminal == nil {
		return
	}
	if err := b.parent.publishContext(b.ctx, *b.terminal); err != nil && b.err == nil {
		b.err = err
	}
}

func (d *Delegator) clientFor(spec RemoteAgentSpec) A2AClient {
	if d.NewClient != nil {
		return d.NewClient(spec)
	}
	if strings.TrimSpace(spec.AgentCardURL) == "" {
		return nil
	}
	return a2aClientAdapter{Client: clienta2a.New(clienta2a.Options{
		AgentCardURL:        spec.AgentCardURL,
		Auth:                spec.Auth,
		HTTPClient:          spec.HTTPClient,
		TrustedAuthOrigins:  spec.TrustedAuthOrigins,
		AcceptedOutputModes: spec.AcceptedOutputModes,
		PreferredTransports: spec.PreferredTransports,
	})}
}

type a2aClientAdapter struct {
	*clienta2a.Client
}

func (c a2aClientAdapter) SendStream(ctx context.Context, req clienta2a.SendRequest) (A2AStream, error) {
	return c.Client.SendStream(ctx, req)
}

func (d *Delegator) newID() string {
	if d != nil && d.NewID != nil {
		return d.NewID()
	}
	var random [8]byte
	if _, err := rand.Read(random[:]); err == nil {
		return "del-" + hex.EncodeToString(random[:])
	}
	return fmt.Sprintf("del-%d", delegationIDCounter.Add(1))
}

func messageForDelegation(req DelegationRequest) (clienta2a.Message, error) {
	if req.Message != nil {
		return cloneA2AMessage(*req.Message), nil
	}
	parts := []clienta2a.Part{{Kind: clienta2a.PartText, Text: promptFor(req)}}
	for _, artifact := range req.Artifacts {
		uri := strings.TrimSpace(artifact.URI)
		if uri == "" {
			return clienta2a.Message{}, &DelegationError{Code: "invalid_artifact", Message: "input artifact uri is required"}
		}
		parts = append(parts, clienta2a.Part{Kind: clienta2a.PartURL, URL: uri, MediaType: artifact.MediaType, Filename: artifact.Name})
	}
	return clienta2a.Message{Role: "user", Parts: parts}, nil
}

func effectiveContextID(req DelegationRequest) string {
	return strings.TrimSpace(req.ContextID)
}

func promptFor(req DelegationRequest) string {
	parts := []string{strings.TrimSpace(req.Objective), strings.TrimSpace(req.Prompt), strings.TrimSpace(req.Context)}
	out := []string{}
	for _, part := range parts {
		if part != "" {
			out = append(out, part)
		}
	}
	return strings.Join(out, "\n\n")
}

func displayName(spec RemoteAgentSpec) string {
	if spec.DisplayName != "" {
		return spec.DisplayName
	}
	if spec.AgentCard != nil && spec.AgentCard.Name != "" {
		return spec.AgentCard.Name
	}
	return spec.Key
}

func clampTimeout(requested, max time.Duration) time.Duration {
	if requested <= 0 {
		return max
	}
	if max > 0 && requested > max {
		return max
	}
	return requested
}

func failedEvent(base DelegationEvent, derr *DelegationError) DelegationEvent {
	ev := base
	ev.Kind = DelegationFailed
	if derr != nil && (derr.Code == "cancelled" || derr.Code == "remote_cancelled") {
		ev.Kind = DelegationCancelled
	}
	ev.Error = derr
	if derr != nil {
		ev.Status = derr.RemoteStatus
	}
	return ev
}

func (r *delegationRun) finishTask(baseEvent DelegationEvent, baseResult DelegationResult, task clienta2a.Task, policy DelegationPolicy, maxArtifacts *int, includeRemoteArtifacts bool, mapper *eventMapper) (DelegationResult, error) {
	if mapper == nil {
		mapper = newEventMapper(baseEvent, r.statusDecoders...)
	}
	if derr := policyErrorForTask(task, policy); derr != nil {
		r.publishAll(mapper.closeOpen(task.ID, task.ContextID))
		r.publish(failedEvent(baseEvent, derr))
		baseResult.RemoteTaskID = task.ID
		baseResult.RemoteContextID = task.ContextID
		baseResult.Status = "failed"
		baseResult.Error = derr
		return baseResult, derr
	}
	result := resultFromTask(baseResult, task, includeRemoteArtifacts)
	if task.Status.State == clienta2a.TaskStateInputRequired && policy.AllowInputRequired {
		result.Error = nil
	}
	result.Artifacts = r.limitResultArtifacts(baseEvent, result.Artifacts, maxArtifacts)
	r.publishAll(mapper.terminalEventsForState(task.ID, task.ContextID, task.Status.State, task.Raw))
	derr, invalid := failureFromStatus(task.Status)
	if invalid {
		derr = &DelegationError{Code: errorCodeFromState(task.Status.State), Message: "remote task failed", RemoteStatus: string(task.Status.State), Metadata: map[string]any{"failure_payload_invalid": true}}
	}
	if local, ok := r.client.(*localClient); ok {
		if localFailure := local.taskFailure(task.ID); localFailure != nil {
			derr = localFailure
		}
	}
	if derr != nil {
		result.Error = derr
		return result, derr
	}
	return result, terminalError(task.Status.State, policy)
}

func policyErrorForTask(task clienta2a.Task, policy DelegationPolicy) *DelegationError {
	if derr := policyErrorForState(task.Status.State, policy); derr != nil {
		return derr
	}
	for _, artifact := range task.Artifacts {
		size := artifactKnownBytes(artifact)
		if size == math.MaxInt64 {
			return &DelegationError{Code: "artifact_invalid", Message: "remote artifact contains unencodable data", RemoteStatus: string(task.Status.State)}
		}
		if policy.MaxArtifactBytes > 0 && size > policy.MaxArtifactBytes {
			return &DelegationError{Code: "artifact_too_large", Message: "remote artifact exceeds max artifact byte policy", RemoteStatus: string(task.Status.State), Metadata: map[string]any{"artifact_id": artifact.ID, "max_bytes": policy.MaxArtifactBytes}}
		}
	}
	return nil
}

func policyErrorForState(state clienta2a.TaskState, policy DelegationPolicy) *DelegationError {
	if state == clienta2a.TaskStateInputRequired && !policy.AllowInputRequired {
		return &DelegationError{Code: "input_required", Message: "remote task requires input", RemoteStatus: string(state)}
	}
	return nil
}

// Size covers content and the optional raw/metadata projection; it never
// fetches URL contents. Unencodable data fails closed even without a byte limit.
func artifactKnownBytes(artifact clienta2a.Artifact) int64 {
	var total int64
	addJSON := func(value any) {
		raw, err := json.Marshal(value)
		if err != nil {
			total = math.MaxInt64
			return
		}
		total = addArtifactBytes(total, int64(len(raw)))
	}
	for _, part := range artifact.Parts {
		for _, n := range []int{len(part.Text), len(part.Raw), len(part.URL), len(part.MediaType), len(part.Filename)} {
			total = addArtifactBytes(total, int64(n))
		}
		if part.Data != nil {
			addJSON(part.Data)
		}
		if len(part.Metadata) > 0 {
			addJSON(part.Metadata)
		}
	}
	if len(artifact.Metadata) > 0 {
		addJSON(artifact.Metadata)
	}
	if len(artifact.Raw) > 0 {
		addJSON(artifact.Raw)
	}
	for _, ext := range artifact.Extensions {
		total = addArtifactBytes(total, int64(len(ext)))
	}
	return total
}

func addArtifactBytes(left, right int64) int64 {
	if right > math.MaxInt64-left {
		return math.MaxInt64
	}
	return left + right
}

func (r *delegationRun) limitResultArtifacts(base DelegationEvent, artifacts []DelegationArtifact, max *int) []DelegationArtifact {
	limited := limitArtifacts(artifacts, max)
	if len(limited) < len(artifacts) {
		ev := base
		ev.Kind = DelegationStreamDropped
		ev.Raw = map[string]any{"reason": "artifact_result_limit", "omitted_count": len(artifacts) - len(limited), "max_artifacts": *max}
		r.publish(ev)
	}
	return limited
}

func limitArtifacts(artifacts []DelegationArtifact, max *int) []DelegationArtifact {
	if max == nil || *max >= len(artifacts) {
		return artifacts
	}
	if *max <= 0 {
		return nil
	}
	return artifacts[:*max]
}

func terminalError(state clienta2a.TaskState, policy DelegationPolicy) error {
	if state == clienta2a.TaskStateCompleted || (state == clienta2a.TaskStateInputRequired && policy.AllowInputRequired) {
		return nil
	}
	return &DelegationError{Code: errorCodeFromState(state), Message: "remote task did not complete successfully", RemoteStatus: string(state)}
}

func lifecycleHookError(code string, err error) *DelegationError {
	var derr *DelegationError
	if errors.As(err, &derr) {
		return derr
	}
	if err == nil {
		return nil
	}
	return &DelegationError{Code: code, Message: err.Error(), Cause: err}
}

func cloneA2AMessage(msg clienta2a.Message) clienta2a.Message {
	out := msg
	out.Parts = make([]clienta2a.Part, 0, len(msg.Parts))
	for _, part := range msg.Parts {
		out.Parts = append(out.Parts, cloneA2APart(part))
	}
	out.ReferenceTasks = append([]string(nil), msg.ReferenceTasks...)
	out.Extensions = append([]string(nil), msg.Extensions...)
	out.Metadata = cloneAnyMap(msg.Metadata)
	out.Raw = cloneAnyMap(msg.Raw)
	return out
}

func cloneA2APart(part clienta2a.Part) clienta2a.Part {
	return clienta2a.Part{
		Kind:      part.Kind,
		Text:      part.Text,
		Raw:       append([]byte(nil), part.Raw...),
		Data:      cloneAnyValue(part.Data),
		URL:       part.URL,
		MediaType: part.MediaType,
		Filename:  part.Filename,
		Metadata:  cloneAnyMap(part.Metadata),
	}
}

func cloneDelegationRequest(req DelegationRequest) DelegationRequest {
	out := req
	out.Artifacts = append([]InputArtifact(nil), req.Artifacts...)
	if req.Message != nil {
		msg := cloneA2AMessage(*req.Message)
		out.Message = &msg
	}
	out.Metadata = cloneAnyMap(req.Metadata)
	return out
}

func cloneDelegationResult(in DelegationResult) DelegationResult {
	out := in
	if in.Artifacts != nil {
		out.Artifacts = make([]DelegationArtifact, len(in.Artifacts))
		for i, artifact := range in.Artifacts {
			out.Artifacts[i] = cloneDelegationArtifact(artifact)
		}
	}
	out.Messages = append([]DelegationMessage(nil), in.Messages...)
	if in.RemoteArtifacts != nil {
		out.RemoteArtifacts = make([]RemoteArtifact, len(in.RemoteArtifacts))
		for i, artifact := range in.RemoteArtifacts {
			out.RemoteArtifacts[i] = artifact
			out.RemoteArtifacts[i].Extensions = append([]string(nil), artifact.Extensions...)
			out.RemoteArtifacts[i].Metadata = cloneAnyMap(artifact.Metadata)
			out.RemoteArtifacts[i].Raw = cloneAnyMap(artifact.Raw)
			out.RemoteArtifacts[i].Parts = clonePartProjection(artifact.Parts)
		}
	}
	out.RawTask = cloneAnyMap(in.RawTask)
	if in.Error != nil {
		cloneErr := *in.Error
		cloneErr.Metadata = cloneAnyMap(in.Error.Metadata)
		out.Error = &cloneErr
	}
	if len(in.Metadata) > 0 {
		out.Metadata = make(map[string]interface{}, len(in.Metadata))
		for k, v := range in.Metadata {
			out.Metadata[k] = cloneAnyValue(v)
		}
	}
	return out
}

func cloneA2AArtifact(in clienta2a.Artifact) clienta2a.Artifact {
	out := in
	out.Parts = make([]clienta2a.Part, len(in.Parts))
	for i, part := range in.Parts {
		out.Parts[i] = cloneA2APart(part)
	}
	out.Extensions = append([]string(nil), in.Extensions...)
	out.Metadata = cloneAnyMap(in.Metadata)
	out.Raw = cloneAnyMap(in.Raw)
	return out
}
