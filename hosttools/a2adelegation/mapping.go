package a2adelegation

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"reflect"
	"strings"

	bridgea2a "github.com/agent-dance/agent-adaptor/bridges/a2a"
	clienta2a "github.com/agent-dance/agent-adaptor/clients/a2a"
)

type eventMapper struct {
	includeRemoteArtifacts bool
	maxArtifactBytes       int64
	boundedArtifacts       map[string]clienta2a.Artifact
	artifactBlocked        map[string]string
	base                   DelegationEvent
	started                bool
	openMessage            string
	statusDecoders         []StatusPartDecoder
	streamProfile          string
	lastSequence           uint64
	seenStatusData         map[string]struct{}
	seenStatusText         map[string]struct{}
	seenRemote             map[remoteCoordinate][]DelegationEvent
	remoteSequence         map[string]uint64
}

func newEventMapper(base DelegationEvent, decoders ...StatusPartDecoder) *eventMapper {
	base.Protocol = ProtocolA2A
	statusDecoders := []StatusPartDecoder{adapterStreamStatusDecoder{}}
	statusDecoders = append(statusDecoders, decoders...)
	return &eventMapper{
		base:             cloneDelegationEvent(base),
		boundedArtifacts: make(map[string]clienta2a.Artifact),
		artifactBlocked:  make(map[string]string),
		statusDecoders:   statusDecoders,
		seenStatusData:   map[string]struct{}{},
		seenStatusText:   map[string]struct{}{},
		seenRemote:       map[remoteCoordinate][]DelegationEvent{}, remoteSequence: map[string]uint64{},
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
	if persistedTaskEvent(event) {
		if event.RecoveredState {
			return append(out, m.taskEvents(*event.Task)...)
		}
		return append(out, m.taskSnapshotEvents(*event.Task)...)
	}
	switch event.Kind {
	case clienta2a.EventStatus:
		out = append(out, m.statusEvents(event.TaskID, event.ContextID, event.Status)...)
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

		}
		if event.Message != nil {
			out = append(out, m.messageEvents(*event.Message)...)
		}
	}
	return out
}

// Stream snapshots never decode status parts or publish an old question.
func persistedTaskEvent(event clienta2a.Event) bool {
	return event.Task != nil && event.Status == nil && event.Message == nil
}

func (m *eventMapper) taskSnapshotEvents(task clienta2a.Task) []DelegationEvent {
	var out []DelegationEvent
	for _, artifact := range task.Artifacts {
		out = append(out, m.artifactEvents(clienta2a.Event{TaskID: task.ID, ContextID: task.ContextID, Artifact: &artifact})...)
	}
	return out
}

// A recovered query is authoritative only when it advances past the streamed
// history. An unchanged question cannot become a new approval request.
func staleRecoveredTask(task clienta2a.Task, snapshot *clienta2a.Task, continuation bool) bool {
	if snapshot == nil || snapshot.ID != task.ID {
		return continuation && task.Status.State == clienta2a.TaskStateInputRequired
	}
	if task.Status.State != snapshot.Status.State {
		return false
	}
	old, current := snapshot.Status.Message, task.Status.Message
	if current == nil {
		return true
	}
	if old == nil {
		return false
	}
	// Distinct formal message IDs identify distinct questions, even when
	// their wording/parts repeat. Compare content only without both IDs.
	if old.ID != "" && current.ID != "" {
		return old.ID == current.ID
	}
	return reflect.DeepEqual(old.Parts, current.Parts)
}

// Retain snapshot artifacts and later updates, following A2A append semantics.
func mergeStreamArtifact(task *clienta2a.Task, artifact clienta2a.Artifact, appendParts bool) {
	artifact = cloneA2AArtifact(artifact)
	for i := range task.Artifacts {
		if artifact.ID != "" && task.Artifacts[i].ID == artifact.ID {
			if appendParts {
				previous := task.Artifacts[i]
				if artifact.Name == "" {
					artifact.Name = previous.Name
				}
				if artifact.Description == "" {
					artifact.Description = previous.Description
				}
				if artifact.Extensions == nil {
					artifact.Extensions = previous.Extensions
				}
				if artifact.Metadata == nil {
					artifact.Metadata = previous.Metadata
				}
				artifact.Parts = append(append([]clienta2a.Part(nil), task.Artifacts[i].Parts...), artifact.Parts...)
			}
			task.Artifacts[i] = artifact
			return
		}
	}
	task.Artifacts = append(task.Artifacts, artifact)
}

// reconcileRecoveredArtifact distinguishes an explicit complete query from
// historical replay. A proven extension replaces live content; a lagging prefix
// cannot roll it back. Incomparable content uses the complete query and reports
// a conflict, leaving previously published live events intact.
func reconcileRecoveredArtifact(task *clienta2a.Task, recovered clienta2a.Artifact, observedLive bool) (conflict bool) {
	if observedLive {
		for _, observed := range task.Artifacts {
			if observed.ID != recovered.ID {
				continue
			}
			liveParts := coalescedArtifactText(observed.Parts)
			queryParts := coalescedArtifactText(recovered.Parts)
			if artifactPartsPrefix(liveParts, queryParts) {
				break
			}
			if artifactPartsPrefix(queryParts, liveParts) {
				return false
			}
			conflict = true
			break
		}
	}
	mergeStreamArtifact(task, recovered, false)
	return conflict
}

// Coalesce only adjacent text with identical non-text fields. JSON data, file
// references, inline bytes, metadata and mixed-part boundaries are not guessed.
func coalescedArtifactText(parts []clienta2a.Part) []clienta2a.Part {
	out := make([]clienta2a.Part, 0, len(parts))
	for i := 0; i < len(parts); {
		part := parts[i]
		i++
		if part.Kind == clienta2a.PartText {
			var text strings.Builder
			text.WriteString(part.Text)
			for i < len(parts) && sameArtifactTextAttributes(part, parts[i]) {
				text.WriteString(parts[i].Text)
				i++
			}
			part.Text = text.String()
		}
		out = append(out, part)
	}
	return out
}

func sameArtifactTextAttributes(left, right clienta2a.Part) bool {
	if left.Kind != clienta2a.PartText || right.Kind != clienta2a.PartText {
		return false
	}
	left.Text, right.Text = "", ""
	return reflect.DeepEqual(left, right)
}

// Inputs are text-coalesced views. Only a final text part may be an unfinished
// prefix within a part; every preceding or non-text part must match exactly.
func artifactPartsPrefix(prefix, whole []clienta2a.Part) bool {
	if len(prefix) > len(whole) {
		return false
	}
	for i, part := range prefix {
		if reflect.DeepEqual(part, whole[i]) {
			continue
		}
		if i == len(prefix)-1 && sameArtifactTextAttributes(part, whole[i]) && strings.HasPrefix(whole[i].Text, part.Text) {
			return true
		}
		return false
	}
	return true
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
	if m.streamProfile != bridgea2a.AdapterStreamSchemaV1 && !hasFailureControl(message) {
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
				out = append(out, m.droppedEvent(taskID, contextID, profile, 0, map[string]any{"reason": "invalid_payload"}))
				break
			}
			sequence := firstStatusSequence(decoded)
			formal := len(decoded) > 0 && decoded[0].Source != nil && decoded[0].Source.RunID != ""
			if formal && sequence != 0 {
				runID := decoded[0].Source.RunID
				key := remoteCoordinate{runID, sequence}
				if old, exists := m.seenRemote[key]; exists {
					if !reflect.DeepEqual(old, decoded) {
						out = append(out, m.droppedEvent(taskID, contextID, profile, 0, map[string]any{"reason": "invalid_payload", "dropped_count": 1}))
					}
					break
				}
				copies := make([]DelegationEvent, len(decoded))
				for i := range decoded {
					copies[i] = cloneDelegationEvent(decoded[i])
				}
				m.seenRemote[key] = copies
				last := m.remoteSequence[runID]
				if last != 0 && sequence > last+1 {
					out = append(out, m.droppedEvent(taskID, contextID, profile, 0, map[string]any{"dropped_count": sequence - last - 1, "first_missing": last + 1, "last_missing": sequence - 1}))
				}
				if sequence > last {
					m.remoteSequence[runID] = sequence
				}
				if sequence > m.lastSequence {
					m.lastSequence = sequence
				}
			}
			if sequence != 0 && !formal {
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
				out = append(out, m.completeStatusDelegationEvent(taskID, contextID, message.ID, profile, event))
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

// completeStatusDelegationEvent 为 decoder 返回的语义事件补齐当前 delegation 和 A2A 上下文。
func (m *eventMapper) completeStatusDelegationEvent(
	taskID, contextID, messageID, profile string,
	decoded DelegationEvent,
) DelegationEvent {
	ev := cloneDelegationEvent(decoded)
	ev.RunID = m.base.RunID
	ev = mapRelay(ev, m.base)
	ev.DelegationID = m.base.DelegationID
	ev.AgentKey = m.base.AgentKey
	ev.AgentName = m.base.AgentName
	ev.Protocol = m.base.Protocol
	ev.RemoteTaskID = taskID
	ev.RemoteContextID = contextID
	if ev.RemoteMessageID == "" {
		ev.RemoteMessageID = messageID
	}
	// mapRelay may replace payload provenance with a safe loss diagnostic.
	// Keep its cloned Raw so the reason/count survive this context completion.
	if ev.Raw == nil {
		ev.Raw = map[string]any{}
	}
	ev.Raw["stream_profile"] = profile
	if ev.Time.IsZero() {
		ev.Time = m.base.Time
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

func firstStatusSequence(events []DelegationEvent) uint64 {
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
	// Evaluate cumulative append content, but publish only the actual update.
	candidate := artifact
	reason := ""
	if event.Append && artifact.ID != "" {
		reason = m.artifactBlocked[artifact.ID]
		if previous, ok := m.boundedArtifacts[artifact.ID]; ok {
			task := clienta2a.Task{Artifacts: []clienta2a.Artifact{previous}}
			mergeStreamArtifact(&task, artifact, true)
			candidate = task.Artifacts[0]
		}
	}
	size := artifactKnownBytes(candidate)
	if size == math.MaxInt64 {
		reason = "artifact_invalid"
	} else if m.maxArtifactBytes > 0 && size > m.maxArtifactBytes {
		reason = "artifact_too_large"
	}
	if reason != "" {
		if artifact.ID != "" {
			m.artifactBlocked[artifact.ID] = reason
			delete(m.boundedArtifacts, artifact.ID)
		}
		ev := m.base
		ev.Kind = DelegationStreamDropped
		ev.RemoteTaskID, ev.RemoteContextID, ev.RemoteArtifactID = event.TaskID, event.ContextID, artifact.ID
		ev.Raw = map[string]any{"reason": reason}
		if reason == "artifact_too_large" {
			ev.Raw["max_bytes"] = m.maxArtifactBytes
		}
		return []DelegationEvent{ev}
	}
	if artifact.ID != "" {
		delete(m.artifactBlocked, artifact.ID)
		if m.maxArtifactBytes > 0 {
			m.boundedArtifacts[artifact.ID] = cloneA2AArtifact(candidate)
		}
	}

	return []DelegationEvent{m.artifactCreatedEvent(event, artifact)}
}

func (m *eventMapper) artifactCreatedEvent(event clienta2a.Event, artifact clienta2a.Artifact) DelegationEvent {
	ev := m.base
	ev.Kind = DelegationArtifactCreated
	ev.RemoteTaskID = event.TaskID
	ev.RemoteContextID = event.ContextID
	ev.RemoteArtifactID = artifact.ID
	ev.Append, ev.LastChunk = event.Append, event.LastChunk
	ev.Artifact = &DelegationArtifact{
		ID:          artifact.ID,
		Name:        artifact.Name,
		Description: artifact.Description,
		URI:         firstURI(artifact.Parts),
		MediaType:   firstMediaType(artifact.Parts),
		Metadata:    cloneAnyMap(artifact.Metadata),
	}
	if m.includeRemoteArtifacts {
		ev.Artifact.Parts = cloneRemoteParts(artifact.Parts)
		ev.Raw = cloneAnyMap(artifact.Raw)
	} else if len(artifact.Parts) > 0 || len(artifact.Raw) > 0 {
		ev.Raw = map[string]any{"parts_omitted": "remote_artifacts_not_requested"}
	}
	return ev
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

func (m *eventMapper) terminalForState(taskID, contextID string, state clienta2a.TaskState, raw map[string]any) DelegationEvent {
	ev := m.base
	ev.RemoteTaskID = taskID
	ev.RemoteContextID = contextID
	ev.Status = string(state)
	ev.Raw = cloneAnyMap(raw)
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
	if task.Status.Message != nil && !hasFailureControl(*task.Status.Message) {
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
			Data:      cloneAnyValue(part.Data),
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

type remoteCoordinate struct {
	runID    string
	sequence uint64
}

func hasFailureControl(message clienta2a.Message) bool {
	for _, part := range message.Parts {
		if part.Kind == clienta2a.PartText {
			if _, ok := part.Metadata["agentadaptor.failure"]; ok {
				return true
			}
		}
	}
	return false
}
