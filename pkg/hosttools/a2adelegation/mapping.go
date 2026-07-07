package a2adelegation

import (
	"encoding/json"
	"strings"

	bridgea2a "github.com/agent-dance/agent-adaptor/pkg/bridges/a2a"
	clienta2a "github.com/agent-dance/agent-adaptor/pkg/clients/a2a"
)

type eventMapper struct {
	base        DelegationEvent
	started     bool
	openMessage string
}

func newEventMapper(base DelegationEvent) *eventMapper {
	base.Protocol = ProtocolA2A
	return &eventMapper{base: base}
}

func (m *eventMapper) Started(taskID, contextID string) DelegationEvent {
	m.started = true
	ev := m.base
	ev.Kind = DelegationStarted
	ev.RemoteTaskID = taskID
	ev.RemoteContextID = contextID
	ev.Status = "started"
	return ev
}

func (m *eventMapper) Map(event clienta2a.Event) []DelegationEvent {
	out := []DelegationEvent{}
	if !m.started {
		out = append(out, m.Started(event.TaskID, event.ContextID))
	}
	switch event.Kind {
	case clienta2a.EventStatus:
		out = append(out, m.statusEvent(event.TaskID, event.ContextID, event.Status))
	case clienta2a.EventTask:
		if event.Task != nil {
			out = append(out, m.taskEvents(*event.Task)...)
		}
	case clienta2a.EventMessage:
		if event.Message != nil {
			out = append(out, m.messageEvents(*event.Message)...)
		}
	case clienta2a.EventArtifact:
		if event.Artifact != nil {
			out = append(out, m.artifactEvent(event.TaskID, event.ContextID, *event.Artifact))
		}
	case clienta2a.EventTerminal:
		out = append(out, m.terminalEvents(event)...)
	}
	return out
}

func (m *eventMapper) taskEvents(task clienta2a.Task) []DelegationEvent {
	out := []DelegationEvent{}
	if task.Status.State != "" {
		out = append(out, m.statusEvent(task.ID, task.ContextID, &task.Status))
	}
	for _, artifact := range task.Artifacts {
		out = append(out, m.artifactEvent(task.ID, task.ContextID, artifact))
	}
	if executionFinalState(task.Status.State) {
		out = append(out, m.terminalForState(task.ID, task.ContextID, task.Status.State, task.Raw))
	}
	return out
}

func (m *eventMapper) statusEvent(taskID, contextID string, status *clienta2a.TaskStatus) DelegationEvent {
	ev := m.base
	ev.Kind = DelegationStatus
	ev.RemoteTaskID = taskID
	ev.RemoteContextID = contextID
	if status != nil {
		ev.Status = string(status.State)
		ev.Raw = map[string]any{"status": string(status.State)}
		if status.Message != nil {
			ev.Text = textFromMessage(*status.Message)
		}
	}
	return ev
}

func (m *eventMapper) messageEvents(msg clienta2a.Message) []DelegationEvent {
	text := textFromMessage(msg)
	if text == "" {
		return nil
	}
	messageID := msg.ID
	if messageID == "" {
		messageID = m.base.DelegationID + ":message"
	}
	out := []DelegationEvent{}
	if m.openMessage != messageID {
		if m.openMessage != "" {
			end := m.base
			end.Kind = DelegationTextEnd
			end.RemoteMessageID = m.openMessage
			out = append(out, end)
		}
		start := m.base
		start.Kind = DelegationTextStart
		start.RemoteTaskID = msg.TaskID
		start.RemoteContextID = msg.ContextID
		start.RemoteMessageID = messageID
		out = append(out, start)
		m.openMessage = messageID
	}
	delta := m.base
	delta.Kind = DelegationTextDelta
	delta.RemoteTaskID = msg.TaskID
	delta.RemoteContextID = msg.ContextID
	delta.RemoteMessageID = messageID
	delta.Delta = text
	out = append(out, delta)
	return out
}

func (m *eventMapper) artifactEvent(taskID, contextID string, artifact clienta2a.Artifact) DelegationEvent {
	if artifact.Name == bridgea2a.ArtifactAssistantOutput {
		ev := m.base
		ev.Kind = DelegationTextDelta
		ev.RemoteTaskID = taskID
		ev.RemoteContextID = contextID
		ev.RemoteArtifactID = artifact.ID
		ev.RemoteMessageID = bridgea2a.ArtifactAssistantOutput
		ev.Delta = textFromParts(artifact.Parts)
		return ev
	}
	ev := m.base
	ev.Kind = DelegationArtifactCreated
	ev.RemoteTaskID = taskID
	ev.RemoteContextID = contextID
	ev.RemoteArtifactID = artifact.ID
	ev.Artifact = &DelegationArtifact{
		ID:          artifact.ID,
		Name:        artifact.Name,
		Description: artifact.Description,
		URI:         firstURI(artifact.Parts),
		MediaType:   firstMediaType(artifact.Parts),
		Metadata:    cloneAnyMap(artifact.Metadata),
	}
	ev.Raw = artifact.Raw
	return ev
}

func (m *eventMapper) terminalEvents(event clienta2a.Event) []DelegationEvent {
	if event.Task != nil {
		return []DelegationEvent{m.terminalForState(event.Task.ID, event.Task.ContextID, event.Task.Status.State, event.Task.Raw)}
	}
	if event.Status != nil {
		return []DelegationEvent{m.terminalForState(event.TaskID, event.ContextID, event.Status.State, event.Raw)}
	}
	if event.Message != nil {
		out := m.messageEvents(*event.Message)
		out = append(out, m.terminalForState(event.TaskID, event.ContextID, clienta2a.TaskStateCompleted, event.Raw))
		return out
	}
	return []DelegationEvent{m.terminalForState(event.TaskID, event.ContextID, clienta2a.TaskStateCompleted, event.Raw)}
}

func (m *eventMapper) terminalForState(taskID, contextID string, state clienta2a.TaskState, raw map[string]any) DelegationEvent {
	ev := m.base
	ev.RemoteTaskID = taskID
	ev.RemoteContextID = contextID
	ev.Status = string(state)
	ev.Raw = raw
	switch state {
	case clienta2a.TaskStateCompleted:
		ev.Kind = DelegationFinished
	case clienta2a.TaskStateCanceled:
		ev.Kind = DelegationCancelled
	case clienta2a.TaskStateInputRequired:
		ev.Kind = DelegationInputRequired
	default:
		ev.Kind = DelegationFailed
		ev.Error = &DelegationError{Code: "remote_failed", Message: "remote task failed", RemoteStatus: string(state)}
	}
	return ev
}

func resultFromTask(base DelegationResult, task clienta2a.Task) DelegationResult {
	base.RemoteTaskID = task.ID
	base.RemoteContextID = task.ContextID
	base.Status = statusFromState(task.Status.State)
	base.RawTask = map[string]any{"provider": ProtocolA2A, "task_id": task.ID}
	for _, msg := range task.Messages {
		text := textFromMessage(msg)
		if text != "" {
			base.Messages = append(base.Messages, DelegationMessage{Role: msg.Role, Text: text})
			if base.Summary == "" {
				base.Summary = text
			}
		}
	}
	for _, artifact := range task.Artifacts {
		if artifact.Name == bridgea2a.ArtifactAgentAdaptorResult {
			applyAgentAdaptorResult(&base, artifact)
			continue
		}
		if artifact.Name == bridgea2a.ArtifactAssistantOutput {
			if text := textFromParts(artifact.Parts); text != "" && base.Summary == "" {
				base.Summary = text
			}
			continue
		}
		base.Artifacts = append(base.Artifacts, DelegationArtifact{ID: artifact.ID, Name: artifact.Name, Description: artifact.Description, URI: firstURI(artifact.Parts), MediaType: firstMediaType(artifact.Parts), Metadata: cloneAnyMap(artifact.Metadata)})
	}
	if base.Status != "completed" && base.Error == nil {
		base.Error = &DelegationError{Code: errorCodeFromState(task.Status.State), Message: "remote task did not complete successfully", RemoteStatus: string(task.Status.State)}
	}
	return base
}

func applyAgentAdaptorResult(result *DelegationResult, artifact clienta2a.Artifact) {
	for _, part := range artifact.Parts {
		if part.Kind != clienta2a.PartData {
			continue
		}
		raw, err := json.Marshal(part.Data)
		if err != nil {
			continue
		}
		var payload struct {
			Summary string         `json:"summary"`
			Output  string         `json:"output"`
			Result  map[string]any `json:"result"`
		}
		if err := json.Unmarshal(raw, &payload); err != nil {
			continue
		}
		if payload.Summary != "" {
			result.Summary = payload.Summary
		} else if payload.Output != "" {
			result.Summary = payload.Output
		}
		if len(payload.Result) > 0 {
			result.Metadata = payload.Result
		}
	}
}

func executionFinalState(state clienta2a.TaskState) bool {
	return state.Terminal() || state == clienta2a.TaskStateInputRequired
}

func statusFromState(state clienta2a.TaskState) string {
	switch state {
	case clienta2a.TaskStateCompleted:
		return "completed"
	case clienta2a.TaskStateCanceled:
		return "cancelled"
	case clienta2a.TaskStateInputRequired:
		return "input_required"
	default:
		if state == "" {
			return "unknown"
		}
		return "failed"
	}
}

func errorCodeFromState(state clienta2a.TaskState) string {
	switch state {
	case clienta2a.TaskStateCanceled:
		return "remote_cancelled"
	case clienta2a.TaskStateInputRequired:
		return "input_required"
	case clienta2a.TaskStateRejected:
		return "remote_rejected"
	default:
		return "remote_failed"
	}
}

func textFromMessage(msg clienta2a.Message) string {
	return textFromParts(msg.Parts)
}

func textFromParts(parts []clienta2a.Part) string {
	var b strings.Builder
	for _, part := range parts {
		if part.Kind == clienta2a.PartText && part.Text != "" {
			b.WriteString(part.Text)
		}
	}
	return b.String()
}

func firstURI(parts []clienta2a.Part) string {
	for _, part := range parts {
		if part.URL != "" {
			return part.URL
		}
	}
	return ""
}

func firstMediaType(parts []clienta2a.Part) string {
	for _, part := range parts {
		if part.MediaType != "" {
			return part.MediaType
		}
	}
	return ""
}
