package a2adelegation

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"

	bridgea2a "github.com/agent-dance/agent-adaptor/pkg/bridges/a2a"
	clienta2a "github.com/agent-dance/agent-adaptor/pkg/clients/a2a"
)

type eventMapper struct {
	base           DelegationEvent
	started        bool
	openMessage    string
	statusDecoders []StatusPartDecoder
	streamProfile  string
	lastSequence   uint64
	seenStatusData map[string]struct{}
	seenStatusText map[string]struct{}
}

func newEventMapper(base DelegationEvent, decoders ...StatusPartDecoder) *eventMapper {
	base.Protocol = ProtocolA2A
	statusDecoders := []StatusPartDecoder{adapterStreamStatusDecoder{}}
	statusDecoders = append(statusDecoders, decoders...)
	return &eventMapper{
		base:           base,
		statusDecoders: statusDecoders,
		seenStatusData: map[string]struct{}{},
		seenStatusText: map[string]struct{}{},
	}
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
		out = append(out, m.statusEvents(event.TaskID, event.ContextID, event.Status)...)
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
			out = append(out, m.artifactEvents(event)...)
		}
	case clienta2a.EventTerminal:
		if event.Status != nil {
			out = append(out, m.statusEvents(event.TaskID, event.ContextID, event.Status)...)
		} else if event.Task != nil && event.Task.Status.State != "" {
			out = append(out, m.statusEvents(event.Task.ID, event.Task.ContextID, &event.Task.Status)...)
		}
		if event.Message != nil {
			out = append(out, m.messageEvents(*event.Message)...)
		}
	}
	return out
}

func (m *eventMapper) taskEvents(task clienta2a.Task) []DelegationEvent {
	out := []DelegationEvent{}
	if task.Status.State != "" {
		out = append(out, m.statusEvents(task.ID, task.ContextID, &task.Status)...)
	}
	for _, artifact := range task.Artifacts {
		out = append(out, m.artifactEvents(clienta2a.Event{
			TaskID: task.ID, ContextID: task.ContextID, Artifact: &artifact,
			LastChunk: executionFinalState(task.Status.State),
		})...)
	}
	return out
}

func (m *eventMapper) statusEvents(taskID, contextID string, status *clienta2a.TaskStatus) []DelegationEvent {
	base := m.statusEvent(taskID, contextID, status)
	out := []DelegationEvent{base}
	if status == nil || status.Message == nil {
		return out
	}
	message := *status.Message
	out = append(out, m.statusPartEvents(taskID, contextID, message)...)
	if m.streamProfile == "" || (m.streamProfile != bridgea2a.AdapterStreamSchemaV1 && m.streamProfile != "legacy_artifact_stream") {
		out = append(out, m.statusTextEvents(taskID, contextID, message)...)
	}
	return out
}

func (m *eventMapper) statusPartEvents(taskID, contextID string, message clienta2a.Message) []DelegationEvent {
	var out []DelegationEvent
	for _, part := range message.Parts {
		if part.Kind != clienta2a.PartData {
			continue
		}
		for _, decoder := range m.statusDecoders {
			if decoder == nil || strings.TrimSpace(decoder.Profile()) == "" {
				continue
			}
			decoded, matched, err := decoder.DecodeStatusPart(part.Data)
			if !matched {
				continue
			}
			profile := decoder.Profile()
			if !m.claimStreamProfile(profile) {
				break
			}
			fingerprint := statusDataFingerprint(profile, message.ID, part.Data)
			if _, seen := m.seenStatusData[fingerprint]; seen {
				break
			}
			m.seenStatusData[fingerprint] = struct{}{}
			if err != nil {
				out = append(out, m.droppedEvent(taskID, contextID, profile, 0, map[string]any{"reason": err.Error()}))
				break
			}
			sequence := firstStatusSequence(decoded)
			if sequence != 0 {
				if sequence <= m.lastSequence {
					break
				}
				if m.lastSequence != 0 && sequence > m.lastSequence+1 {
					out = append(out, m.droppedEvent(taskID, contextID, profile, 0, map[string]any{
						"dropped_count": sequence - m.lastSequence - 1,
						"first_missing": m.lastSequence + 1,
						"last_missing":  sequence - 1,
					}))
				}
				m.lastSequence = sequence
			}
			for _, event := range decoded {
				out = append(out, m.delegationEventFromStatus(taskID, contextID, message.ID, profile, event))
			}
			break
		}
	}
	return out
}

func (m *eventMapper) statusTextEvents(taskID, contextID string, message clienta2a.Message) []DelegationEvent {
	text := textFromMessage(message)
	if strings.TrimSpace(text) == "" {
		return nil
	}
	messageID := strings.TrimSpace(message.ID)
	if messageID == "" {
		messageID = m.base.DelegationID + ":status"
	}
	fingerprint := messageID + ":" + text
	if _, seen := m.seenStatusText[fingerprint]; seen {
		return nil
	}
	m.seenStatusText[fingerprint] = struct{}{}
	start := m.base
	start.Kind = DelegationTextStart
	start.RemoteTaskID = taskID
	start.RemoteContextID = contextID
	start.RemoteMessageID = messageID
	delta := start
	delta.Kind = DelegationTextDelta
	delta.Delta = text
	end := start
	end.Kind = DelegationTextEnd
	return []DelegationEvent{start, delta, end}
}

func (m *eventMapper) delegationEventFromStatus(
	taskID, contextID, messageID, profile string,
	decoded StatusPartEvent,
) DelegationEvent {
	ev := m.base
	ev.Kind = decoded.Kind
	ev.RemoteTaskID = taskID
	ev.RemoteContextID = contextID
	ev.RemoteMessageID = decoded.MessageID
	if ev.RemoteMessageID == "" {
		ev.RemoteMessageID = messageID
	}
	ev.RemoteToolCallID = decoded.ToolCallID
	ev.Sequence = decoded.Sequence
	ev.Name = decoded.Name
	ev.ToolName = decoded.Name
	ev.Role = decoded.Role
	ev.Delta = decoded.Delta
	ev.Args = decoded.Args
	ev.Result = decoded.Result
	ev.Raw = cloneAnyMap(decoded.Raw)
	if ev.Raw == nil {
		ev.Raw = map[string]any{}
	}
	ev.Raw["stream_profile"] = profile
	if !decoded.Time.IsZero() {
		ev.Time = decoded.Time
	}
	return ev
}

func (m *eventMapper) droppedEvent(
	taskID, contextID, profile string,
	sequence uint64,
	raw map[string]any,
) DelegationEvent {
	ev := m.base
	ev.Kind = DelegationStreamDropped
	ev.RemoteTaskID = taskID
	ev.RemoteContextID = contextID
	ev.Sequence = sequence
	ev.Raw = cloneAnyMap(raw)
	if ev.Raw == nil {
		ev.Raw = map[string]any{}
	}
	ev.Raw["stream_profile"] = profile
	return ev
}

func (m *eventMapper) claimStreamProfile(profile string) bool {
	if m.streamProfile == "" {
		m.streamProfile = profile
		return true
	}
	return m.streamProfile == profile
}

func firstStatusSequence(events []StatusPartEvent) uint64 {
	for _, event := range events {
		if event.Sequence != 0 {
			return event.Sequence
		}
	}
	return 0
}

func statusDataFingerprint(profile, messageID string, data any) string {
	raw, _ := json.Marshal(data)
	digest := sha256.Sum256(raw)
	return profile + ":" + messageID + ":" + hex.EncodeToString(digest[:])
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
			ev.RemoteMessageID = status.Message.ID
			ev.Text = textFromMessage(*status.Message)
			ev.StatusParts = cloneRemoteParts(status.Message.Parts)
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

func (m *eventMapper) artifactEvents(event clienta2a.Event) []DelegationEvent {
	artifact := *event.Artifact
	if legacyStreamArtifact(artifact.Name) && !m.claimStreamProfile("legacy_artifact_stream") {
		return nil
	}
	if artifact.Name == bridgea2a.ArtifactAssistantOutput {
		return m.assistantOutputEvents(event, artifact)
	}
	if events := m.toolCallEvents(event, artifact); len(events) > 0 {
		return append([]DelegationEvent{m.artifactCreatedEvent(event, artifact)}, events...)
	}
	return []DelegationEvent{m.artifactCreatedEvent(event, artifact)}
}

func legacyStreamArtifact(name string) bool {
	return name == bridgea2a.ArtifactAssistantOutput || name == "reasoning" || name == "stream-dropped" ||
		name == "human-decision-request" || name == "human-decision-result" || strings.HasPrefix(name, "tool-call-")
}

func (m *eventMapper) artifactCreatedEvent(event clienta2a.Event, artifact clienta2a.Artifact) DelegationEvent {
	ev := m.base
	ev.Kind = DelegationArtifactCreated
	ev.RemoteTaskID = event.TaskID
	ev.RemoteContextID = event.ContextID
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

// toolCallEvents restores the typed tool lifecycle encoded by the A2A bridge
// as tool-call artifacts. This keeps A2A wire artifacts protocol-native while
// giving hosts UI-facing events instead of opaque artifacts.
func (m *eventMapper) toolCallEvents(event clienta2a.Event, artifact clienta2a.Artifact) []DelegationEvent {
	if data := artifactData(artifact); data != nil {
		toolID := dataString(data, "id")
		switch dataString(data, "kind") {
		case "tool_call.start":
			ev := m.base
			ev.Kind = DelegationToolCallStart
			ev.RemoteTaskID = event.TaskID
			ev.RemoteContextID = event.ContextID
			ev.RemoteArtifactID = artifact.ID
			ev.RemoteToolCallID = toolID
			ev.ToolName = dataString(data, "name")
			ev.Args = data["args"]
			return []DelegationEvent{ev}
		case "tool_call.result":
			ev := m.base
			ev.Kind = DelegationToolCallResult
			ev.RemoteTaskID = event.TaskID
			ev.RemoteContextID = event.ContextID
			ev.RemoteArtifactID = artifact.ID
			ev.RemoteToolCallID = toolID
			ev.Result = data["result"]
			return []DelegationEvent{ev}
		case "tool_call.end":
			ev := m.base
			ev.Kind = DelegationToolCallEnd
			ev.RemoteTaskID = event.TaskID
			ev.RemoteContextID = event.ContextID
			ev.RemoteArtifactID = artifact.ID
			ev.RemoteToolCallID = toolID
			return []DelegationEvent{ev}
		}
	}
	if !strings.HasPrefix(artifact.Name, "tool-call-") {
		return nil
	}
	toolID := strings.TrimPrefix(artifact.Name, "tool-call-")
	ev := m.base
	ev.RemoteTaskID = event.TaskID
	ev.RemoteContextID = event.ContextID
	ev.RemoteArtifactID = artifact.ID
	ev.RemoteToolCallID = toolID
	var out []DelegationEvent
	if text := textFromParts(artifact.Parts); text != "" {
		args := ev
		args.Kind = DelegationToolCallArgs
		args.Delta = text
		out = append(out, args)
	}
	if event.LastChunk {
		end := ev
		end.Kind = DelegationToolCallEnd
		out = append(out, end)
	}
	return out
}

func artifactData(artifact clienta2a.Artifact) map[string]any {
	for _, part := range artifact.Parts {
		if data, ok := part.Data.(map[string]any); ok {
			return data
		}
	}
	return nil
}

func dataString(data map[string]any, key string) string {
	value, _ := data[key].(string)
	return strings.TrimSpace(value)
}

// assistantOutputEvents 将 A2A 文本 artifact chunk 规范化为完整的文本消息生命周期。
// A2A 用 append / lastChunk 标识 chunk 边界；下游 AG-UI 客户端则要求 start/content/end
// 共享同一个 message ID，因此不能只透传 delta。
func (m *eventMapper) assistantOutputEvents(event clienta2a.Event, artifact clienta2a.Artifact) []DelegationEvent {
	messageID := artifact.ID
	if messageID == "" {
		messageID = bridgea2a.ArtifactAssistantOutput
	}
	out := make([]DelegationEvent, 0, 3)
	if m.openMessage != messageID {
		if m.openMessage != "" {
			end := m.base
			end.Kind = DelegationTextEnd
			end.RemoteMessageID = m.openMessage
			out = append(out, end)
		}
		start := m.base
		start.Kind = DelegationTextStart
		start.RemoteTaskID = event.TaskID
		start.RemoteContextID = event.ContextID
		start.RemoteArtifactID = artifact.ID
		start.RemoteMessageID = messageID
		out = append(out, start)
		m.openMessage = messageID
	}
	if text := textFromParts(artifact.Parts); text != "" {
		delta := m.base
		delta.Kind = DelegationTextDelta
		delta.RemoteTaskID = event.TaskID
		delta.RemoteContextID = event.ContextID
		delta.RemoteArtifactID = artifact.ID
		delta.RemoteMessageID = messageID
		delta.Delta = text
		out = append(out, delta)
	}
	if event.LastChunk {
		end := m.base
		end.Kind = DelegationTextEnd
		end.RemoteTaskID = event.TaskID
		end.RemoteContextID = event.ContextID
		end.RemoteArtifactID = artifact.ID
		end.RemoteMessageID = messageID
		out = append(out, end)
		m.openMessage = ""
	}
	return out
}

func (m *eventMapper) closeOpen(taskID, contextID string) []DelegationEvent {
	if m == nil || m.openMessage == "" {
		return nil
	}
	end := m.base
	end.Kind = DelegationTextEnd
	end.RemoteTaskID = taskID
	end.RemoteContextID = contextID
	end.RemoteMessageID = m.openMessage
	m.openMessage = ""
	return []DelegationEvent{end}
}

func (m *eventMapper) terminalEventsForState(taskID, contextID string, state clienta2a.TaskState, raw map[string]any) []DelegationEvent {
	out := m.closeOpen(taskID, contextID)
	return append(out, m.terminalForState(taskID, contextID, state, raw))
}

func (m *eventMapper) terminalEvents(event clienta2a.Event) []DelegationEvent {
	if event.Task != nil {
		return m.terminalEventsForState(event.Task.ID, event.Task.ContextID, event.Task.Status.State, event.Task.Raw)
	}
	if event.Status != nil {
		return m.terminalEventsForState(event.TaskID, event.ContextID, event.Status.State, event.Raw)
	}
	if event.Message != nil {
		out := m.messageEvents(*event.Message)
		out = append(out, m.terminalEventsForState(event.TaskID, event.ContextID, clienta2a.TaskStateCompleted, event.Raw)...)
		return out
	}
	return m.terminalEventsForState(event.TaskID, event.ContextID, clienta2a.TaskStateCompleted, event.Raw)
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

func resultFromTask(base DelegationResult, task clienta2a.Task, includeRemoteArtifacts bool) DelegationResult {
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
	if task.Status.Message != nil {
		text := textFromMessage(*task.Status.Message)
		if text != "" {
			if !hasDelegationMessage(base.Messages, task.Status.Message.Role, text) {
				base.Messages = append(base.Messages, DelegationMessage{Role: task.Status.Message.Role, Text: text})
			}
			base.Summary = text
		}
	}
	for _, artifact := range task.Artifacts {
		if includeRemoteArtifacts {
			base.RemoteArtifacts = append(base.RemoteArtifacts, cloneRemoteArtifact(artifact))
		}
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

func hasDelegationMessage(messages []DelegationMessage, role, text string) bool {
	for _, msg := range messages {
		if msg.Role == role && msg.Text == text {
			return true
		}
	}
	return false
}

func cloneRemoteArtifact(artifact clienta2a.Artifact) RemoteArtifact {
	out := RemoteArtifact{
		ID:          artifact.ID,
		Name:        artifact.Name,
		Description: artifact.Description,
		Parts:       cloneRemoteParts(artifact.Parts),
		Extensions:  append([]string(nil), artifact.Extensions...),
		Metadata:    cloneAnyMap(artifact.Metadata),
		Raw:         cloneAnyMap(artifact.Raw),
	}
	return out
}

func cloneRemoteParts(parts []clienta2a.Part) []RemotePart {
	if len(parts) == 0 {
		return nil
	}
	out := make([]RemotePart, 0, len(parts))
	for _, part := range parts {
		out = append(out, RemotePart{
			Kind:      part.Kind,
			Text:      part.Text,
			Raw:       append([]byte(nil), part.Raw...),
			Data:      part.Data,
			URL:       part.URL,
			MediaType: part.MediaType,
			Filename:  part.Filename,
			Metadata:  cloneAnyMap(part.Metadata),
		})
	}
	return out
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
