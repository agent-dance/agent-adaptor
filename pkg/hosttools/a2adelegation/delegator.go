package a2adelegation

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	clienta2a "github.com/agent-dance/agent-adaptor/pkg/clients/a2a"
)

type A2AClient interface {
	AgentCard(ctx context.Context) (clienta2a.AgentCard, error)
	Send(ctx context.Context, req clienta2a.SendRequest) (clienta2a.Task, error)
	SendStream(ctx context.Context, req clienta2a.SendRequest) (*clienta2a.Stream, error)
	GetTask(ctx context.Context, req clienta2a.GetTaskRequest) (clienta2a.Task, error)
	CancelTask(ctx context.Context, req clienta2a.CancelTaskRequest) (clienta2a.Task, error)
}

type ClientFactory func(RemoteAgentSpec) A2AClient

type Delegator struct {
	Registry  *Registry
	Bus       *EventBus
	NewClient ClientFactory
	NewID     func() string
}

func NewDelegator(registry *Registry, bus *EventBus) *Delegator {
	return &Delegator{Registry: registry, Bus: bus}
}

func (d *Delegator) Delegate(ctx context.Context, req DelegationRequest) (DelegationResult, error) {
	if d == nil || d.Registry == nil {
		return DelegationResult{}, &DelegationError{Code: "configuration_error", Message: "delegation registry is required"}
	}
	spec, ok := d.Registry.Lookup(req.Agent)
	if !ok {
		err := &DelegationError{Code: "agent_not_found", Message: fmt.Sprintf("remote agent %q is not registered", req.Agent)}
		return DelegationResult{DelegationID: d.newID(), Agent: req.Agent, RemoteProtocol: ProtocolA2A, Status: "failed", Error: err}, err
	}
	delegationID := d.newID()
	if delegationID == "" {
		delegationID = "del-unknown"
	}
	baseEvent := DelegationEvent{
		RunID:            req.RunID,
		ParentToolCallID: req.ParentToolCallID,
		DelegationID:     delegationID,
		AgentKey:         spec.Key,
		AgentName:        displayName(spec),
		Protocol:         ProtocolA2A,
	}
	baseResult := DelegationResult{DelegationID: delegationID, Agent: spec.Key, RemoteProtocol: ProtocolA2A, Status: "running"}

	client := d.clientFor(spec)
	card, err := client.AgentCard(ctx)
	if err != nil && spec.AgentCard == nil {
		derr := &DelegationError{Code: "agent_unavailable", Message: err.Error(), Retryable: true}
		d.publish(failedEvent(baseEvent, derr))
		baseResult.Status = "failed"
		baseResult.Error = derr
		return baseResult, derr
	}
	if spec.AgentCard != nil {
		card = *spec.AgentCard
	}
	if spec.Policy.RequireStreaming && !card.Capabilities.Streaming {
		derr := &DelegationError{Code: "capability_unsupported", Message: "remote agent does not advertise streaming"}
		d.publish(failedEvent(baseEvent, derr))
		baseResult.Status = "failed"
		baseResult.Error = derr
		return baseResult, derr
	}

	timeout := clampTimeout(req.Timeout, spec.Policy.MaxTimeout)
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	message, err := messageForDelegation(req)
	if err != nil {
		derr := delegationErr(err)
		d.publish(failedEvent(baseEvent, derr))
		baseResult.Status = "failed"
		baseResult.Error = derr
		return baseResult, derr
	}
	send := clienta2a.SendRequest{
		Message:             message,
		Tenant:              spec.Tenant,
		AcceptedOutputModes: spec.AcceptedOutputModes,
		Metadata:            cloneAnyMap(req.Metadata),
	}
	if req.Tenant != "" {
		send.Tenant = req.Tenant
	}
	if req.Stream || card.Capabilities.Streaming {
		return d.delegateStreaming(ctx, client, spec, send, baseEvent, baseResult, req.MaxArtifacts)
	}
	return d.delegatePolling(ctx, client, spec, send, baseEvent, baseResult, req.MaxArtifacts)
}

func (d *Delegator) delegateStreaming(ctx context.Context, client A2AClient, spec RemoteAgentSpec, send clienta2a.SendRequest, baseEvent DelegationEvent, baseResult DelegationResult, maxArtifacts *int) (DelegationResult, error) {
	stream, err := client.SendStream(ctx, send)
	if err != nil {
		if spec.Policy.RequireStreaming {
			derr := &DelegationError{Code: "stream_unavailable", Message: err.Error(), Retryable: true}
			d.publish(failedEvent(baseEvent, derr))
			baseResult.Status = "failed"
			baseResult.Error = derr
			return baseResult, derr
		}
		return d.delegatePolling(ctx, client, spec, send, baseEvent, baseResult, maxArtifacts)
	}
	defer stream.Close()
	mapper := newEventMapper(baseEvent)
	var lastTask clienta2a.Task
	for {
		event, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			if lastTask.ID != "" {
				if recovered, ok := d.recoverTask(ctx, client, spec, lastTask.ID); ok {
					lastTask = recovered
					for _, ev := range mapper.taskEvents(recovered) {
						d.publish(ev)
					}
					return d.finishTask(baseEvent, baseResult, recovered, spec.Policy, maxArtifacts)
				}
			}
			derr := &DelegationError{Code: "stream_interrupted", Message: err.Error(), Retryable: true}
			d.publish(failedEvent(baseEvent, derr))
			baseResult.Status = "failed"
			baseResult.Error = derr
			return baseResult, derr
		}
		if event.Task != nil {
			lastTask = *event.Task
		}
		for _, ev := range mapper.Map(event) {
			d.publish(ev)
		}
		if event.Task != nil && executionFinalState(event.Task.Status.State) {
			return d.finishTask(baseEvent, baseResult, *event.Task, spec.Policy, maxArtifacts)
		}
		if event.Status != nil && executionFinalState(event.Status.State) {
			task, ok := d.recoverTask(ctx, client, spec, event.TaskID)
			if ok {
				return d.finishTask(baseEvent, baseResult, task, spec.Policy, maxArtifacts)
			}
			baseResult.RemoteTaskID = event.TaskID
			baseResult.RemoteContextID = event.ContextID
			baseResult.Status = statusFromState(event.Status.State)
			if derr := policyErrorForState(event.Status.State, spec.Policy); derr != nil {
				d.publish(failedEvent(baseEvent, derr))
				baseResult.Status = "failed"
				baseResult.Error = derr
				return baseResult, derr
			}
			if baseResult.Status != "completed" {
				baseResult.Error = &DelegationError{Code: errorCodeFromState(event.Status.State), Message: "remote task did not complete successfully", RemoteStatus: string(event.Status.State)}
			}
			return baseResult, terminalError(event.Status.State, spec.Policy)
		}
	}
	if lastTask.ID != "" && executionFinalState(lastTask.Status.State) {
		return d.finishTask(baseEvent, baseResult, lastTask, spec.Policy, maxArtifacts)
	}
	derr := &DelegationError{Code: "stream_interrupted", Message: "remote stream ended before terminal state", Retryable: true}
	d.publish(failedEvent(baseEvent, derr))
	baseResult.Status = "failed"
	baseResult.Error = derr
	return baseResult, derr
}

func (d *Delegator) delegatePolling(ctx context.Context, client A2AClient, spec RemoteAgentSpec, send clienta2a.SendRequest, baseEvent DelegationEvent, baseResult DelegationResult, maxArtifacts *int) (DelegationResult, error) {
	send.ReturnImmediately = true
	task, err := client.Send(ctx, send)
	if err != nil {
		derr := &DelegationError{Code: "agent_unavailable", Message: err.Error(), Retryable: true}
		d.publish(failedEvent(baseEvent, derr))
		baseResult.Status = "failed"
		baseResult.Error = derr
		return baseResult, derr
	}
	mapper := newEventMapper(baseEvent)
	d.publish(mapper.Started(task.ID, task.ContextID))
	for _, ev := range mapper.taskEvents(task) {
		d.publish(ev)
	}
	if executionFinalState(task.Status.State) {
		return d.finishTask(baseEvent, baseResult, task, spec.Policy, maxArtifacts)
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
			d.cancelRemote(context.Background(), client, task.ID, send.Tenant, baseEvent)
			derr := &DelegationError{Code: "cancelled", Message: ctx.Err().Error(), Retryable: true}
			baseResult.Status = "cancelled"
			baseResult.Error = derr
			return baseResult, derr
		case <-ticker.C:
		}
		task, err = client.GetTask(ctx, clienta2a.GetTaskRequest{TaskID: task.ID, Tenant: send.Tenant})
		if err != nil {
			continue
		}
		for _, ev := range mapper.taskEvents(task) {
			d.publish(ev)
		}
		if executionFinalState(task.Status.State) {
			return d.finishTask(baseEvent, baseResult, task, spec.Policy, maxArtifacts)
		}
	}
	d.cancelRemote(context.Background(), client, task.ID, send.Tenant, baseEvent)
	derr := &DelegationError{Code: "remote_timeout", Message: "remote task did not finish before timeout", Retryable: true, RemoteStatus: string(task.Status.State)}
	baseResult.Status = "failed"
	baseResult.Error = derr
	return baseResult, derr
}

func (d *Delegator) recoverTask(ctx context.Context, client A2AClient, spec RemoteAgentSpec, taskID string) (clienta2a.Task, bool) {
	if taskID == "" {
		return clienta2a.Task{}, false
	}
	task, err := client.GetTask(ctx, clienta2a.GetTaskRequest{TaskID: taskID, Tenant: spec.Tenant})
	return task, err == nil && executionFinalState(task.Status.State)
}

func (d *Delegator) cancelRemote(ctx context.Context, client A2AClient, taskID, tenant string, base DelegationEvent) {
	if taskID != "" {
		_, _ = client.CancelTask(ctx, clienta2a.CancelTaskRequest{TaskID: taskID, Tenant: tenant, Metadata: map[string]any{"reason": "parent_cancelled", "delegation_id": base.DelegationID}})
	}
	ev := base
	ev.Kind = DelegationCancelled
	ev.RemoteTaskID = taskID
	ev.Status = "cancelled"
	d.publish(ev)
}

func (d *Delegator) publish(ev DelegationEvent) {
	if d.Bus != nil {
		d.Bus.Publish(ev)
	}
}

func (d *Delegator) clientFor(spec RemoteAgentSpec) A2AClient {
	if d.NewClient != nil {
		return d.NewClient(spec)
	}
	return clienta2a.New(clienta2a.Options{
		AgentCardURL:        spec.AgentCardURL,
		Auth:                spec.Auth,
		HTTPClient:          spec.HTTPClient,
		TrustedAuthOrigins:  spec.TrustedAuthOrigins,
		AcceptedOutputModes: spec.AcceptedOutputModes,
		PreferredTransports: spec.PreferredTransports,
	})
}

func (d *Delegator) newID() string {
	if d != nil && d.NewID != nil {
		return d.NewID()
	}
	return "del-" + time.Now().UTC().Format("20060102150405.000000000")
}

func messageForDelegation(req DelegationRequest) (clienta2a.Message, error) {
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

func (d *Delegator) finishTask(baseEvent DelegationEvent, baseResult DelegationResult, task clienta2a.Task, policy DelegationPolicy, maxArtifacts *int) (DelegationResult, error) {
	if derr := policyErrorForTask(task, policy); derr != nil {
		d.publish(failedEvent(baseEvent, derr))
		baseResult.RemoteTaskID = task.ID
		baseResult.RemoteContextID = task.ContextID
		baseResult.Status = "failed"
		baseResult.Error = derr
		return baseResult, derr
	}
	result := resultFromTask(baseResult, task)
	if task.Status.State == clienta2a.TaskStateInputRequired && policy.AllowInputRequired {
		result.Error = nil
	}
	result.Artifacts = limitArtifacts(result.Artifacts, maxArtifacts)
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
