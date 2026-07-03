package a2a

import (
	a2aproto "github.com/a2aproject/a2a-go/v2/a2a"

	agentadaptor "github.com/agent-dance/agent-adaptor"
)

type streamTranslator struct {
	info    a2aproto.TaskInfoProvider
	started map[a2aproto.ArtifactID]bool
}

func newStreamTranslator(info a2aproto.TaskInfoProvider) *streamTranslator {
	return &streamTranslator{info: info, started: map[a2aproto.ArtifactID]bool{}}
}

func (t *streamTranslator) Translate(p agentadaptor.StreamPayload) []a2aproto.Event {
	switch p.Kind {
	case agentadaptor.StreamTextContent:
		if p.Delta == "" {
			return nil
		}
		return []a2aproto.Event{t.artifact("assistant-output", p.Delta, true)}
	case agentadaptor.StreamToolCallStart:
		return []a2aproto.Event{t.dataArtifact(a2aproto.ArtifactID("tool-call-"+defaultString(p.ToolCallID, p.Name)), map[string]any{
			"kind": "tool_call.start", "id": p.ToolCallID, "name": p.Name, "args": p.Args,
		})}
	case agentadaptor.StreamToolCallArgs:
		return []a2aproto.Event{t.artifact(a2aproto.ArtifactID("tool-call-"+defaultString(p.ToolCallID, "args")), p.Delta, true)}
	case agentadaptor.StreamToolCallResult:
		return []a2aproto.Event{t.dataArtifact(a2aproto.ArtifactID("tool-call-"+defaultString(p.ToolCallID, "result")), map[string]any{
			"kind": "tool_call.result", "id": p.ToolCallID, "result": p.Result,
		})}
	case agentadaptor.StreamReasoningContent:
		if p.Delta == "" {
			return nil
		}
		ev := t.artifact("reasoning", p.Delta, true)
		ev.Artifact.Name = "reasoning"
		return []a2aproto.Event{ev}
	case agentadaptor.StreamHITLRequested:
		return []a2aproto.Event{t.dataArtifact("human-decision-request", map[string]any{
			"kind": string(p.Kind), "request": p.HITLRequested, "raw": p.Raw,
		})}
	case agentadaptor.StreamHITLResolved:
		return []a2aproto.Event{t.dataArtifact("human-decision-result", map[string]any{
			"kind": string(p.Kind), "result": p.HITLResolved, "raw": p.Raw,
		})}
	case agentadaptor.StreamRunError:
		msg := "stream run error"
		if p.Error != nil && p.Error.Message != "" {
			msg = p.Error.Message
		}
		return []a2aproto.Event{a2aproto.NewStatusUpdateEvent(t.info, a2aproto.TaskStateFailed, failureMessage(t.info, msg, streamFailureDetails(p.Error)))}
	case agentadaptor.StreamDropped:
		return []a2aproto.Event{t.dataArtifact("stream-dropped", map[string]any{"raw": p.Raw})}
	default:
		return nil
	}
}

func (t *streamTranslator) artifact(id a2aproto.ArtifactID, text string, appendChunk bool) *a2aproto.TaskArtifactUpdateEvent {
	var ev *a2aproto.TaskArtifactUpdateEvent
	if t.started[id] {
		ev = a2aproto.NewArtifactUpdateEvent(t.info, id, textPart(text))
	} else {
		ev = a2aproto.NewArtifactUpdateEvent(t.info, id, textPart(text))
		ev.Append = false
		t.started[id] = true
	}
	ev.Artifact.ID = id
	ev.Artifact.Name = string(id)
	ev.Append = appendChunk && ev.Append
	return ev
}

func (t *streamTranslator) dataArtifact(id a2aproto.ArtifactID, data map[string]any) *a2aproto.TaskArtifactUpdateEvent {
	ev := a2aproto.NewArtifactUpdateEvent(t.info, id, dataPart(data))
	if !t.started[id] {
		ev.Append = false
		t.started[id] = true
	}
	ev.Artifact.ID = id
	ev.Artifact.Name = string(id)
	return ev
}

func terminalArtifacts(info a2aproto.TaskInfoProvider, result agentadaptor.RunResult) []a2aproto.Event {
	var out []a2aproto.Event
	details := map[string]any{
		"run_id":      result.RunID,
		"driver_type": result.DriverType,
		"summary":     result.Summary,
		"metadata":    stringMapAny(result.Metadata),
	}
	if result.Result != nil {
		details["result"] = result.Result
	}
	if result.Usage != nil {
		details["usage"] = result.Usage
	}
	if len(result.Transcript) > 0 {
		details["transcript"] = result.Transcript
	}
	if result.RawStreams != nil {
		details["raw_streams"] = result.RawStreams
	}
	ev := a2aproto.NewArtifactUpdateEvent(info, "agent-adaptor-result", dataPart(details))
	ev.Append = false
	ev.Artifact.ID = "agent-adaptor-result"
	ev.Artifact.Name = "agent-adaptor-result"
	out = append(out, ev)
	return out
}

func failureDetails(f *agentadaptor.RunFailure) map[string]any {
	if f == nil {
		return nil
	}
	out := map[string]any{"code": string(f.Code), "metadata": f.Metadata}
	if f.HumanDecision != nil {
		out["human_decision"] = f.HumanDecision
	}
	return out
}

func streamFailureDetails(f *agentadaptor.RunFailure) map[string]any {
	if f == nil {
		return nil
	}
	return failureDetails(f)
}

func defaultString(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}
