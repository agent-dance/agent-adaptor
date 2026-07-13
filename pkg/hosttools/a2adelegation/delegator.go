package a2adelegation

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync/atomic"
	"time"

	clienta2a "github.com/agent-dance/agent-adaptor/pkg/clients/a2a"
)

const remoteCancelTimeout = 5 * time.Second
const lifecycleHookTimeout = 5 * time.Second

var delegationIDCounter atomic.Uint64

type A2AStream interface {
	// Recv blocks until the next event. Close must unblock any in-flight Recv.
	Recv() (clienta2a.Event, error)
	Close() error
}

type contextA2AStream interface {
	RecvContext(context.Context) (clienta2a.Event, error)
}

type A2AClient interface {
	AgentCard(ctx context.Context) (clienta2a.AgentCard, error)
	Send(ctx context.Context, req clienta2a.SendRequest) (clienta2a.Task, error)
	SendStream(ctx context.Context, req clienta2a.SendRequest) (A2AStream, error)
	GetTask(ctx context.Context, req clienta2a.GetTaskRequest) (clienta2a.Task, error)
	CancelTask(ctx context.Context, req clienta2a.CancelTaskRequest) (clienta2a.Task, error)
}

type ClientFactory func(RemoteAgentSpec) A2AClient

type Delegator struct {
	Registry      *Registry
	Bus           *EventBus
	NewClient     ClientFactory
	NewID         func() string
	LifecycleHook DelegationLifecycleHook
}

func NewDelegator(registry *Registry, bus *EventBus) *Delegator {
	return &Delegator{Registry: registry, Bus: bus}
}

type delegationRun struct {
	*Delegator
	publishEvent func(DelegationEvent)
}

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
		DelegationID:     delegationID,
		AgentKey:         req.Agent,
		AgentName:        req.Agent,
		Protocol:         ProtocolA2A,
	}
	if d.Registry == nil {
		derr := &DelegationError{Code: "configuration_error", Message: "delegation registry is required"}
		d.publish(failedEvent(baseEvent, derr))
		return DelegationResult{DelegationID: delegationID, Agent: req.Agent, RemoteProtocol: ProtocolA2A, Status: "failed", Error: derr}, derr
	}
	spec, ok := d.Registry.Lookup(req.Agent)
	if !ok {
		derr := &DelegationError{Code: "agent_not_found", Message: fmt.Sprintf("remote agent %q is not registered", req.Agent)}
		d.publish(failedEvent(baseEvent, derr))
		return DelegationResult{DelegationID: delegationID, Agent: req.Agent, RemoteProtocol: ProtocolA2A, Status: "failed", Error: derr}, derr
	}
	baseEvent.AgentKey = spec.Key
	baseEvent.AgentName = displayName(spec)
	baseResult := DelegationResult{DelegationID: delegationID, Agent: spec.Key, RemoteProtocol: ProtocolA2A, Status: "running"}
	// 2. Apply the timeout and lifecycle hooks before any remote I/O.
	timeout := clampTimeout(req.Timeout, spec.Policy.MaxTimeout)
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	publisher := &terminalEventBuffer{parent: d}
	run := &delegationRun{Delegator: d, publishEvent: publisher.publish}
	defer publisher.flush()
	if d.LifecycleHook != nil {
		if hookErr := d.LifecycleHook.BeforeDelegate(ctx, BeforeDelegation{
			DelegationID: delegationID,
			AgentSpec:    spec,
			Request:      cloneDelegationRequest(req),
		}); hookErr != nil {
			derr := lifecycleHookError("workflow_before_failed", hookErr)
			run.publish(failedEvent(baseEvent, derr))
			baseResult.Status = "failed"
			baseResult.Error = derr
			return baseResult, derr
		}
		defer func() {
			afterCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), lifecycleHookTimeout)
			defer cancel()
			if hookErr := d.LifecycleHook.AfterDelegate(afterCtx, AfterDelegation{
				DelegationID: delegationID,
				AgentSpec:    spec,
				Request:      cloneDelegationRequest(req),
				Result:       cloneDelegationResult(out),
				Err:          err,
			}); hookErr != nil {
				derr := lifecycleHookError("workflow_after_failed", hookErr)
				if err == nil {
					out = ensureDelegationError(out, derr)
					terminal := failedEvent(baseEvent, derr)
					if terminal.Kind == DelegationCancelled {
						out.Status = "cancelled"
					} else {
						out.Status = "failed"
					}
					err = derr
					publisher.replace(terminal)
				} else if out.Error == nil {
					out.Error = derr
				}
			}
		}()
	}

	// 3. Resolve the remote card and enforce host policy before sending work.
	client := d.clientFor(spec)
	if client == nil {
		derr := &DelegationError{Code: "configuration_error", Message: "remote agent requires AgentCardURL for default A2A execution; configure Delegator.NewClient for static AgentCard-only specs"}
		run.publish(failedEvent(baseEvent, derr))
		baseResult.Status = "failed"
		baseResult.Error = derr
		return baseResult, derr
	}
	card, err := client.AgentCard(ctx)
	if err != nil && spec.AgentCard == nil {
		derr := &DelegationError{Code: "agent_unavailable", Message: err.Error(), Retryable: true}
		run.publish(failedEvent(baseEvent, derr))
		baseResult.Status = "failed"
		baseResult.Error = derr
		return baseResult, derr
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
		if spec.Policy.RequireStreaming {
			derr := &DelegationError{Code: "stream_unavailable", Message: err.Error(), Retryable: true}
			r.publish(failedEvent(baseEvent, derr))
			baseResult.Status = "failed"
			baseResult.Error = derr
			return baseResult, derr
		}
		return r.delegatePolling(ctx, client, spec, send, baseEvent, baseResult, maxArtifacts, includeRemoteArtifacts)
	}
	defer stream.Close()
	// 2. Track remote identity and mapper state for recovery and lifecycle closure.
	mapper := newEventMapper(baseEvent)
	var lastTask clienta2a.Task
	lastTaskID := ""
	cancelResult := func() (DelegationResult, error) {
		r.publishAll(mapper.closeOpen(lastTaskID, send.ContextID))
		if lastTaskID != "" {
			r.cancelRemote(ctx, client, lastTaskID, send.Tenant, baseEvent)
		} else {
			r.publish(cancelledEvent(baseEvent, ""))
		}
		derr := &DelegationError{Code: "cancelled", Message: ctx.Err().Error(), Retryable: true}
		baseResult.Status = "cancelled"
		baseResult.Error = derr
		return baseResult, derr
	}
	cancelKnownTask := func() {
		if lastTaskID != "" {
			r.cancelRemoteTask(ctx, client, lastTaskID, send.Tenant, baseEvent)
		}
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
				if recovered, ok := r.recoverTask(ctx, client, lastTaskID, send.Tenant, send.HistoryLength); ok {
					lastTask = recovered
					for _, ev := range mapper.taskEvents(recovered) {
						r.publish(ev)
					}
					return r.finishTask(baseEvent, baseResult, recovered, spec.Policy, maxArtifacts, includeRemoteArtifacts, mapper)
				}
				cancelKnownTask()
			}
			derr := &DelegationError{Code: "stream_interrupted", Message: err.Error(), Retryable: true}
			r.publishAll(mapper.closeOpen(lastTaskID, send.ContextID))
			r.publish(failedEvent(baseEvent, derr))
			baseResult.Status = "failed"
			baseResult.Error = derr
			return baseResult, derr
		}
		if event.TaskID != "" {
			lastTaskID = event.TaskID
		}
		if event.Message != nil && event.Message.TaskID != "" {
			lastTaskID = event.Message.TaskID
		}
		if event.Task != nil {
			lastTask = *event.Task
			lastTaskID = event.Task.ID
		}
		for _, ev := range mapper.Map(event) {
			r.publish(ev)
		}
		if event.Message != nil && event.Kind == clienta2a.EventTerminal {
			baseResult.RemoteTaskID = event.TaskID
			baseResult.RemoteContextID = event.ContextID
			baseResult.Status = "completed"
			baseResult.Summary = textFromMessage(*event.Message)
			if baseResult.Summary != "" {
				baseResult.Messages = append(baseResult.Messages, DelegationMessage{Role: event.Message.Role, Text: baseResult.Summary})
			}
			r.publishAll(mapper.terminalEventsForState(event.TaskID, event.ContextID, clienta2a.TaskStateCompleted, event.Raw))
			return baseResult, nil
		}
		if event.Task != nil && executionFinalState(event.Task.Status.State) {
			return r.finishTask(baseEvent, baseResult, *event.Task, spec.Policy, maxArtifacts, includeRemoteArtifacts, mapper)
		}
		if event.Status != nil && executionFinalState(event.Status.State) {
			task, ok := r.recoverTask(ctx, client, event.TaskID, send.Tenant, send.HistoryLength)
			if ok {
				return r.finishTask(baseEvent, baseResult, task, spec.Policy, maxArtifacts, includeRemoteArtifacts, mapper)
			}
			baseResult.RemoteTaskID = event.TaskID
			baseResult.RemoteContextID = event.ContextID
			baseResult.Status = statusFromState(event.Status.State)
			if derr := policyErrorForState(event.Status.State, spec.Policy); derr != nil {
				r.publishAll(mapper.closeOpen(event.TaskID, event.ContextID))
				r.publish(failedEvent(baseEvent, derr))
				baseResult.Status = "failed"
				baseResult.Error = derr
				return baseResult, derr
			}
			r.publishAll(mapper.terminalEventsForState(event.TaskID, event.ContextID, event.Status.State, event.Raw))
			if baseResult.Status != "completed" {
				baseResult.Error = &DelegationError{Code: errorCodeFromState(event.Status.State), Message: "remote task did not complete successfully", RemoteStatus: string(event.Status.State)}
			}
			return baseResult, terminalError(event.Status.State, spec.Policy)
		}
	}
	// 4. Recover a final task snapshot or fail an incomplete stream explicitly.
	if lastTask.ID != "" && executionFinalState(lastTask.Status.State) {
		return r.finishTask(baseEvent, baseResult, lastTask, spec.Policy, maxArtifacts, includeRemoteArtifacts, mapper)
	}
	cancelKnownTask()
	derr := &DelegationError{Code: "stream_interrupted", Message: "remote stream ended before terminal state", Retryable: true}
	r.publishAll(mapper.closeOpen(lastTaskID, send.ContextID))
	r.publish(failedEvent(baseEvent, derr))
	baseResult.Status = "failed"
	baseResult.Error = derr
	return baseResult, derr
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
		derr := &DelegationError{Code: "agent_unavailable", Message: err.Error(), Retryable: true}
		r.publish(failedEvent(baseEvent, derr))
		baseResult.Status = "failed"
		baseResult.Error = derr
		return baseResult, derr
	}
	mapper := newEventMapper(baseEvent)
	r.publish(mapper.Started(task.ID, task.ContextID))
	for _, ev := range mapper.taskEvents(task) {
		r.publish(ev)
	}
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
			r.cancelRemote(ctx, client, task.ID, send.Tenant, baseEvent)
			derr := &DelegationError{Code: "cancelled", Message: ctx.Err().Error(), Retryable: true}
			baseResult.Status = "cancelled"
			baseResult.Error = derr
			return baseResult, derr
		case <-ticker.C:
		}
		task, err = client.GetTask(ctx, clienta2a.GetTaskRequest{TaskID: task.ID, Tenant: send.Tenant, HistoryLength: send.HistoryLength})
		if err != nil {
			continue
		}
		for _, ev := range mapper.taskEvents(task) {
			r.publish(ev)
		}
		if executionFinalState(task.Status.State) {
			return r.finishTask(baseEvent, baseResult, task, spec.Policy, maxArtifacts, includeRemoteArtifacts, mapper)
		}
	}
	r.cancelRemoteTask(ctx, client, task.ID, send.Tenant, baseEvent)
	derr := &DelegationError{Code: "remote_timeout", Message: "remote task did not finish before timeout", Retryable: true, RemoteStatus: string(task.Status.State)}
	r.publishAll(mapper.closeOpen(task.ID, task.ContextID))
	r.publish(failedEvent(baseEvent, derr))
	baseResult.Status = "failed"
	baseResult.Error = derr
	return baseResult, derr
}

func (d *Delegator) recoverTask(ctx context.Context, client A2AClient, taskID, tenant string, historyLength *int) (clienta2a.Task, bool) {
	if taskID == "" {
		return clienta2a.Task{}, false
	}
	task, err := client.GetTask(ctx, clienta2a.GetTaskRequest{TaskID: taskID, Tenant: tenant, HistoryLength: historyLength})
	return task, err == nil && executionFinalState(task.Status.State)
}

func (r *delegationRun) cancelRemote(ctx context.Context, client A2AClient, taskID, tenant string, base DelegationEvent) {
	r.cancelRemoteTask(ctx, client, taskID, tenant, base)
	r.publish(cancelledEvent(base, taskID))
}

func cancelledEvent(base DelegationEvent, taskID string) DelegationEvent {
	ev := base
	ev.Kind = DelegationCancelled
	ev.RemoteTaskID = taskID
	ev.Status = "cancelled"
	return ev
}

func (d *Delegator) cancelRemoteTask(ctx context.Context, client A2AClient, taskID, tenant string, base DelegationEvent) {
	if taskID == "" {
		return
	}
	cancelCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), remoteCancelTimeout)
	defer cancel()
	_, _ = client.CancelTask(cancelCtx, clienta2a.CancelTaskRequest{TaskID: taskID, Tenant: tenant, Metadata: map[string]any{"reason": "parent_cancelled", "delegation_id": base.DelegationID}})
}

func (d *Delegator) publish(ev DelegationEvent) {
	if d != nil && d.Bus != nil {
		d.Bus.Publish(ev)
	}
}

func (r *delegationRun) publish(ev DelegationEvent) {
	if r != nil && r.publishEvent != nil {
		r.publishEvent(ev)
		return
	}
	if r != nil {
		r.Delegator.publish(ev)
	}
}

func (r *delegationRun) publishAll(events []DelegationEvent) {
	for _, ev := range events {
		r.publish(ev)
	}
}

type terminalEventBuffer struct {
	parent   *Delegator
	terminal *DelegationEvent
}

func (b *terminalEventBuffer) publish(ev DelegationEvent) {
	if isTerminal(ev.Kind) {
		if b.terminal == nil {
			copyEvent := ev
			b.terminal = &copyEvent
		}
		return
	}
	b.parent.publish(ev)
}

func (b *terminalEventBuffer) replace(ev DelegationEvent) {
	copyEvent := ev
	b.terminal = &copyEvent
}

func (b *terminalEventBuffer) flush() {
	if b == nil || b.parent == nil || b.terminal == nil {
		return
	}
	b.parent.publish(*b.terminal)
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
		mapper = newEventMapper(baseEvent)
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
	result.Artifacts = limitArtifacts(result.Artifacts, maxArtifacts)
	r.publishAll(mapper.terminalEventsForState(task.ID, task.ContextID, task.Status.State, task.Raw))
	return result, terminalError(task.Status.State, policy)
}

func policyErrorForTask(task clienta2a.Task, policy DelegationPolicy) *DelegationError {
	if derr := policyErrorForState(task.Status.State, policy); derr != nil {
		return derr
	}
	if policy.MaxArtifactBytes <= 0 {
		return nil
	}
	for _, artifact := range task.Artifacts {
		if artifactKnownBytes(artifact) > policy.MaxArtifactBytes {
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

func artifactKnownBytes(artifact clienta2a.Artifact) int64 {
	var total int64
	for _, part := range artifact.Parts {
		total += int64(len(part.Text) + len(part.Raw) + len(part.URL) + len(part.MediaType) + len(part.Filename))
		if part.Data != nil {
			if raw, err := json.Marshal(part.Data); err == nil {
				total += int64(len(raw))
			}
		}
	}
	return total
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
	return &DelegationError{Code: code, Message: err.Error()}
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
		Data:      part.Data,
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
	out.Artifacts = append([]DelegationArtifact(nil), in.Artifacts...)
	out.Messages = append([]DelegationMessage(nil), in.Messages...)
	out.RemoteArtifacts = append([]RemoteArtifact(nil), in.RemoteArtifacts...)
	out.RawTask = cloneAnyMap(in.RawTask)
	if in.Error != nil {
		cloneErr := *in.Error
		cloneErr.Metadata = cloneAnyMap(in.Error.Metadata)
		out.Error = &cloneErr
	}
	if len(in.Metadata) > 0 {
		out.Metadata = make(map[string]interface{}, len(in.Metadata))
		for k, v := range in.Metadata {
			out.Metadata[k] = v
		}
	}
	return out
}
