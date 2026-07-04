package a2a

import (
	"sort"

	a2aproto "github.com/a2aproject/a2a-go/v2/a2a"

	agentadaptor "github.com/agent-dance/agent-adaptor"
)

type streamTranslator struct {
	info     a2aproto.TaskInfoProvider
	exposure ExposurePolicy
	started  map[a2aproto.ArtifactID]bool
}

func newStreamTranslator(info a2aproto.TaskInfoProvider, exposure ExposurePolicy) *streamTranslator {
	return &streamTranslator{
		info: info, exposure: exposure, started: map[a2aproto.ArtifactID]bool{},
	}
}

func (t *streamTranslator) Translate(p agentadaptor.StreamPayload) []a2aproto.Event {
	switch p.Kind {
	case agentadaptor.StreamTextContent:
		if p.Delta == "" {
			return nil
		}
		return []a2aproto.Event{t.artifact("assistant-output", "assistant-output", p.Delta, true)}
	case agentadaptor.StreamTextEnd:
		return t.closeArtifact("assistant-output", "assistant-output")
	case agentadaptor.StreamToolCallStart:
		if !t.exposure.IncludeToolCalls {
			return nil
		}
		data := map[string]any{"kind": "tool_call.start", "id": p.ToolCallID, "name": p.Name}
		if len(p.Args) > 0 {
			data["args"] = sanitizeRemoteValue(p.Args)
		}
		id := a2aproto.ArtifactID("tool-call-" + defaultString(p.ToolCallID, p.Name))
		return []a2aproto.Event{t.dataArtifact(id, string(id), data, true)}
	case agentadaptor.StreamToolCallArgs:
		if !t.exposure.IncludeToolCalls || p.Delta == "" {
			return nil
		}
		id := a2aproto.ArtifactID("tool-call-" + defaultString(p.ToolCallID, "args"))
		return []a2aproto.Event{t.artifact(id, string(id), p.Delta, true)}
	case agentadaptor.StreamToolCallEnd:
		if !t.exposure.IncludeToolCalls {
			return nil
		}
		id := a2aproto.ArtifactID("tool-call-" + defaultString(p.ToolCallID, "args"))
		return t.closeArtifact(id, string(id))
	case agentadaptor.StreamToolCallResult:
		if !t.exposure.IncludeToolCalls {
			return nil
		}
		id := a2aproto.ArtifactID("tool-call-" + defaultString(p.ToolCallID, "result"))
		return []a2aproto.Event{t.dataArtifact(id, string(id), map[string]any{
			"kind": "tool_call.result", "id": p.ToolCallID, "result": sanitizeRemoteValue(p.Result),
		}, true)}
	case agentadaptor.StreamReasoningContent:
		if !t.exposure.IncludeReasoning || p.Delta == "" {
			return nil
		}
		ev := t.artifact("reasoning", "reasoning", p.Delta, true)
		ev.Artifact.Name = "reasoning"
		return []a2aproto.Event{ev}
	case agentadaptor.StreamReasoningEnd:
		if !t.exposure.IncludeReasoning {
			return nil
		}
		return t.closeArtifact("reasoning", "reasoning")
	case agentadaptor.StreamHITLRequested:
		if !t.exposure.IncludeHITL {
			return nil
		}
		return []a2aproto.Event{t.dataArtifact("human-decision-request", "human-decision-request", hitlRequestedArtifact(p, t.exposure), true)}
	case agentadaptor.StreamHITLResolved:
		if !t.exposure.IncludeHITL {
			return nil
		}
		return []a2aproto.Event{t.dataArtifact("human-decision-result", "human-decision-result", hitlResolvedArtifact(p, t.exposure), true)}
	case agentadaptor.StreamRunError:
		msg := "stream run error"
		if p.Error != nil && p.Error.Message != "" {
			msg = p.Error.Message
		}
		return []a2aproto.Event{a2aproto.NewStatusUpdateEvent(t.info, a2aproto.TaskStateFailed, failureMessage(t.info, msg, streamFailureDetails(p.Error, t.exposure)))}
	case agentadaptor.StreamDropped:
		if !t.exposure.hasStreamingDiagnostics() {
			return nil
		}
		data := map[string]any{"kind": string(p.Kind)}
		if dropped, ok := p.Raw["dropped_count"]; ok {
			data["dropped_count"] = dropped
		}
		return []a2aproto.Event{t.dataArtifact("stream-dropped", "stream-dropped", data, true)}
	default:
		return nil
	}
}

func (t *streamTranslator) artifact(id a2aproto.ArtifactID, name, text string, appendChunk bool) *a2aproto.TaskArtifactUpdateEvent {
	var ev *a2aproto.TaskArtifactUpdateEvent
	if t.started[id] {
		ev = a2aproto.NewArtifactUpdateEvent(t.info, id, textPart(text))
	} else {
		ev = a2aproto.NewArtifactUpdateEvent(t.info, id, textPart(text))
		ev.Append = false
		t.started[id] = true
	}
	ev.Artifact.ID = id
	ev.Artifact.Name = name
	ev.Append = appendChunk && ev.Append
	return ev
}

func (t *streamTranslator) closeArtifact(id a2aproto.ArtifactID, name string) []a2aproto.Event {
	if !t.started[id] {
		return nil
	}
	ev := a2aproto.NewArtifactUpdateEvent(t.info, id, textPart(""))
	ev.Artifact.ID = id
	ev.Artifact.Name = name
	ev.LastChunk = true
	t.started[id] = false
	return []a2aproto.Event{ev}
}

func (t *streamTranslator) CloseOpen() []a2aproto.Event {
	if len(t.started) == 0 {
		return nil
	}
	ids := make([]string, 0, len(t.started))
	for id, started := range t.started {
		if started {
			ids = append(ids, string(id))
		}
	}
	sort.Strings(ids)
	out := make([]a2aproto.Event, 0, len(ids))
	for _, id := range ids {
		out = append(out, t.closeArtifact(a2aproto.ArtifactID(id), id)...)
	}
	return out
}

func (t *streamTranslator) dataArtifact(id a2aproto.ArtifactID, name string, data map[string]any, lastChunk bool) *a2aproto.TaskArtifactUpdateEvent {
	ev := a2aproto.NewArtifactUpdateEvent(t.info, id, dataPart(data))
	if !t.started[id] {
		ev.Append = false
		t.started[id] = true
	}
	ev.Artifact.ID = id
	ev.Artifact.Name = name
	ev.LastChunk = lastChunk
	if lastChunk {
		t.started[id] = false
	}
	return ev
}

func terminalArtifacts(info a2aproto.TaskInfoProvider, result agentadaptor.RunResult, exposure ExposurePolicy) []a2aproto.Event {
	var out []a2aproto.Event
	details := map[string]any{
		"summary": result.Summary,
	}
	if exposure.Diagnostics.IncludeMetadata {
		if metadata := sanitizeRemoteMap(stringMapAny(result.Metadata)); len(metadata) > 0 {
			details["metadata"] = metadata
		}
	}
	if exposure.Diagnostics.IncludeProviderResult && result.Result != nil {
		details["result"] = sanitizeRemoteValue(result.Result)
	}
	if exposure.Diagnostics.IncludeUsage && result.Usage != nil {
		details["usage"] = sanitizeRemoteValue(result.Usage)
	}
	if exposure.Diagnostics.IncludeTranscript && len(result.Transcript) > 0 {
		details["transcript"] = sanitizeRemoteValue(result.Transcript)
	}
	if exposure.Diagnostics.IncludeRawStreams && result.RawStreams != nil {
		details["raw_streams"] = map[string]any{
			"stdout": redactInlineSecrets(result.RawStreams.Stdout),
			"stderr": redactInlineSecrets(result.RawStreams.Stderr),
		}
	}
	ev := a2aproto.NewArtifactUpdateEvent(info, "agent-adaptor-result", dataPart(details))
	ev.Append = false
	ev.LastChunk = true
	ev.Artifact.ID = "agent-adaptor-result"
	ev.Artifact.Name = "agent-adaptor-result"
	out = append(out, ev)
	return out
}

func failureDetails(f *agentadaptor.RunFailure, exposure ExposurePolicy) map[string]any {
	if f == nil {
		return nil
	}
	out := map[string]any{"code": string(f.Code)}
	if exposure.Diagnostics.IncludeMetadata {
		if metadata := sanitizeRemoteMap(f.Metadata); len(metadata) > 0 {
			out["metadata"] = metadata
		}
	}
	if f.HumanDecision != nil {
		human := map[string]any{
			"kind":     string(f.HumanDecision.Kind),
			"source":   f.HumanDecision.Source,
			"decision": string(f.HumanDecision.Decision),
			"attempts": f.HumanDecision.Attempts,
		}
		if exposure.IncludeHITL && exposure.Diagnostics.IncludeHITLPayloads && f.HumanDecision.Request != nil {
			human["request"] = sanitizeRemoteValue(f.HumanDecision.Request)
		}
		out["human_decision"] = human
	}
	return out
}

func streamFailureDetails(f *agentadaptor.RunFailure, exposure ExposurePolicy) map[string]any {
	if f == nil {
		return nil
	}
	return failureDetails(f, exposure)
}

func defaultString(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}

func (p ExposurePolicy) hasStreamingDiagnostics() bool {
	return p.IncludeReasoning ||
		p.IncludeToolCalls ||
		p.IncludeHITL ||
		p.Diagnostics.IncludeMetadata ||
		p.Diagnostics.IncludeUsage ||
		p.Diagnostics.IncludeProviderResult ||
		p.Diagnostics.IncludeTranscript ||
		p.Diagnostics.IncludeRawStreams
}

func hitlRequestedArtifact(p agentadaptor.StreamPayload, exposure ExposurePolicy) map[string]any {
	out := map[string]any{"kind": string(p.Kind)}
	if p.HITLRequested == nil {
		return out
	}
	out["request_id"] = p.HITLRequested.RequestID
	out["decision_kind"] = string(p.HITLRequested.Kind)
	out["source"] = p.HITLRequested.Source
	out["retry_attempt"] = p.HITLRequested.RetryAttempt
	if !p.HITLRequested.Deadline.IsZero() {
		out["deadline"] = p.HITLRequested.Deadline.UTC().Format("2006-01-02T15:04:05Z")
	}
	if exposure.Diagnostics.IncludeHITLPayloads {
		out["request"] = sanitizeRemoteValue(p.HITLRequested)
	}
	if exposure.Diagnostics.IncludeHITLRaw && len(p.Raw) > 0 {
		out["raw"] = sanitizeRemoteValue(p.Raw)
	}
	return out
}

func hitlResolvedArtifact(p agentadaptor.StreamPayload, exposure ExposurePolicy) map[string]any {
	out := map[string]any{"kind": string(p.Kind)}
	if p.HITLResolved == nil {
		return out
	}
	out["request_id"] = p.HITLResolved.RequestID
	out["decision_kind"] = string(p.HITLResolved.Kind)
	out["source"] = p.HITLResolved.Source
	out["retry_attempt"] = p.HITLResolved.RetryAttempt
	out["result"] = string(p.HITLResolved.Result)
	if exposure.Diagnostics.IncludeHITLPayloads {
		out["response"] = sanitizeRemoteValue(p.HITLResolved)
	}
	if exposure.Diagnostics.IncludeHITLRaw && len(p.Raw) > 0 {
		out["raw"] = sanitizeRemoteValue(p.Raw)
	}
	return out
}
