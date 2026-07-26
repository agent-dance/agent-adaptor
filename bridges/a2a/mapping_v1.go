package a2a

// This file is the v1-API twin of mapping.go / stream_status.go's encode
// half: it projects the new unified event family (adaptor.Event) onto the
// same adapter.stream.v1 DataPart wire and the same terminal artifacts,
// with the same ExposurePolicy gating and the same redaction helpers
// (convert.go). DecodeAdapterStreamStatus continues to decode the frames
// this file produces — the wire schema did not change, only the producer's
// input vocabulary.

import (
	"encoding/json"
	"fmt"
	"time"

	a2aproto "github.com/a2aproject/a2a-go/v2/a2a"

	adaptor "github.com/agent-dance/agent-adaptor/next"
)

// streamTranslatorV1 maps unified events onto A2A working-status DataParts.
// Exposure gating is identical to the legacy translator: text always
// crosses; reasoning / tool calls / HITL are opt-in; run failures become a
// failed status; drop markers require any streaming diagnostics; everything
// else (process output, notices, subagent updates) stays server-side, as it
// did when those flows rode the legacy RunEvent channel.
type streamTranslatorV1 struct {
	info     a2aproto.TaskInfoProvider
	exposure ExposurePolicy

	runID    string
	threadID string
	seq      uint64
}

func newStreamTranslatorV1(info a2aproto.TaskInfoProvider, exposure ExposurePolicy) *streamTranslatorV1 {
	return &streamTranslatorV1{info: info, exposure: exposure}
}

func (t *streamTranslatorV1) Translate(ev adaptor.Event) []a2aproto.Event {
	switch e := ev.(type) {
	case adaptor.RunStarted:
		// Not forwarded (legacy behavior); remembered so subsequent wire
		// frames carry run/thread identity.
		t.runID = e.RunID
		t.threadID = e.ThreadID
		return nil

	case adaptor.RunFinished:
		if !e.Failed {
			return nil // the terminal projection belongs to the executor
		}
		msg := defaultString(e.Message, "stream run error")
		details := map[string]any{"code": string(e.Reason)}
		return []a2aproto.Event{a2aproto.NewStatusUpdateEvent(t.info, a2aproto.TaskStateFailed, failureMessage(t.info, msg, details))}

	case adaptor.TextDelta:
		kind := "text.content"
		switch e.Phase {
		case adaptor.PhaseStart:
			kind = "text.start"
		case adaptor.PhaseEnd:
			kind = "text.end"
		}
		return t.emit(AdapterStreamEventV1{
			Kind:      kind,
			MessageID: e.MessageID,
			Delta:     e.Text,
			Role:      string(e.Role),
		})

	case adaptor.Thinking:
		if !t.exposure.IncludeReasoning {
			return nil
		}
		kind := "reasoning.content"
		switch e.Phase {
		case adaptor.PhaseStart:
			kind = "reasoning.start"
		case adaptor.PhaseEnd:
			kind = "reasoning.end"
		}
		return t.emit(AdapterStreamEventV1{
			Kind:      kind,
			MessageID: e.MessageID,
			Delta:     e.Text,
		})

	case adaptor.ToolCall:
		if !t.exposure.IncludeToolCalls {
			return nil
		}
		event := AdapterStreamEventV1{ToolCallID: e.ID, Name: e.Name}
		switch e.Phase {
		case adaptor.PhaseStart:
			event.Kind = "tool_call.start"
			event.Args = e.Args
		case adaptor.PhaseEnd:
			event.Kind = "tool_call.end"
			event.Result = e.Result
		default:
			event.Kind = "tool_call.args"
			event.Delta = e.ArgsDelta
		}
		return t.emit(event)

	case adaptor.ToolResult:
		if !t.exposure.IncludeToolCalls {
			return nil
		}
		return t.emit(AdapterStreamEventV1{
			Kind:       "tool_call.result",
			ToolCallID: e.ID,
			Result:     e.Result,
		})

	case *adaptor.ApprovalRequest:
		if !t.exposure.IncludeHITL || e == nil {
			return nil
		}
		return t.emit(AdapterStreamEventV1{
			Kind:       "hitl.requested",
			ToolCallID: e.ToolCallID,
			HITL:       hitlRequestedArtifactV1(e, t.exposure),
		})

	case adaptor.Notice:
		switch e.Kind {
		case adaptor.NoticeApprovalRequested:
			if !t.exposure.IncludeHITL {
				return nil
			}
			return t.emit(AdapterStreamEventV1{
				Kind: "hitl.requested",
				HITL: hitlNoticeArtifactV1("hitl.requested", e),
			})
		case adaptor.NoticeApprovalResolved:
			if !t.exposure.IncludeHITL {
				return nil
			}
			return t.emit(AdapterStreamEventV1{
				Kind: "hitl.resolved",
				HITL: hitlNoticeArtifactV1("hitl.resolved", e),
			})
		}
		return nil // step/lifecycle/runtime notices stay server-side

	case adaptor.Dropped:
		if !t.exposure.hasStreamingDiagnostics() {
			return nil
		}
		return t.emit(AdapterStreamEventV1{
			Kind: "stream.dropped",
			Raw:  map[string]any{"dropped_count": e.Count},
		})
	}
	return nil // ProcessInfo, SubagentUpdate, unknown: not exposed
}

// emit stamps identity + sequence onto the wire DTO, encodes the envelope
// (redaction + 64KiB cap + JSON normalization shared with the legacy
// encoder), and wraps it in a working-status DataPart message.
func (t *streamTranslatorV1) emit(event AdapterStreamEventV1) []a2aproto.Event {
	t.seq++
	event.Sequence = t.seq
	if event.RunID == "" {
		event.RunID = t.runID
	}
	if event.ThreadID == "" {
		event.ThreadID = t.threadID
	}
	data, err := encodeAdapterStreamEventV1(event)
	if err != nil {
		data = encodeAdapterStreamDropV1(event, err)
	}
	msg := a2aproto.NewMessageForTask(a2aproto.MessageRoleAgent, t.info, dataPart(data))
	return []a2aproto.Event{a2aproto.NewStatusUpdateEvent(t.info, a2aproto.TaskStateWorking, msg)}
}

// encodeAdapterStreamEventV1 applies the shared redaction pipeline to a
// pre-built wire DTO: inline-secret redaction on the delta, remote-map
// sanitization on args/result/hitl/raw, the 64KiB envelope cap, and the
// marshal→unmarshal JSON normalization.
func encodeAdapterStreamEventV1(event AdapterStreamEventV1) (map[string]any, error) {
	event.Delta = redactInlineSecrets(event.Delta)
	if len(event.Args) > 0 {
		event.Args = sanitizeRemoteMap(event.Args)
	}
	if len(event.Result) > 0 {
		event.Result = sanitizeRemoteMap(event.Result)
	}
	if len(event.HITL) > 0 {
		event.HITL = sanitizeRemoteMap(event.HITL)
	}
	if len(event.Raw) > 0 {
		event.Raw = sanitizeRemoteMap(event.Raw)
	}
	envelope := AdapterStreamEnvelopeV1{Schema: AdapterStreamSchemaV1, Event: event}
	raw, err := json.Marshal(envelope)
	if err != nil {
		return nil, fmt.Errorf("marshal adapter stream status: %w", err)
	}
	if len(raw) > adapterStreamMaxBytes {
		return nil, fmt.Errorf("adapter stream status exceeds %d bytes", adapterStreamMaxBytes)
	}
	var data map[string]any
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, fmt.Errorf("normalize adapter stream status: %w", err)
	}
	return data, nil
}

// encodeAdapterStreamDropV1 is the fallback frame when a payload cannot be
// encoded (marshal failure, size cap) — same shape as the legacy
// encodeAdapterStreamDrop.
func encodeAdapterStreamDropV1(event AdapterStreamEventV1, cause error) map[string]any {
	return map[string]any{
		"schema": AdapterStreamSchemaV1,
		"event": map[string]any{
			"kind":     "stream.dropped",
			"sequence": event.Sequence,
			"raw": map[string]any{
				"dropped_kind":  event.Kind,
				"dropped_count": 1,
				"reason":        cause.Error(),
			},
		},
	}
}

// hitlRequestedArtifactV1 projects a live *ApprovalRequest (full fidelity:
// the event carries deadline, payload, and choices).
func hitlRequestedArtifactV1(req *adaptor.ApprovalRequest, exposure ExposurePolicy) map[string]any {
	out := map[string]any{
		"kind":          "hitl.requested",
		"request_id":    req.ID,
		"decision_kind": string(req.Kind),
		"source":        req.Source,
		"retry_attempt": req.Attempt,
	}
	if !req.Deadline.IsZero() {
		out["deadline"] = req.Deadline.UTC().Format("2006-01-02T15:04:05Z")
	}
	if exposure.Diagnostics.IncludeHITLPayloads {
		request := map[string]any{
			"prompt":       req.Title,
			"tool_call_id": req.ToolCallID,
		}
		if len(req.Details) > 0 {
			request["payload"] = req.Details
		}
		if len(req.Choices) > 0 {
			choices := make([]map[string]any, 0, len(req.Choices))
			for _, c := range req.Choices {
				choices = append(choices, map[string]any{
					"key": c.Key, "label": c.Label, "description": c.Description,
				})
			}
			request["choices"] = choices
		}
		out["request"] = sanitizeRemoteValue(request)
	}
	return out
}

// hitlNoticeArtifactV1 projects the requested/resolved approval Notices
// (callback / auto-resolve paths). The notice Data carries the reduced
// field set: request_id, kind, source, attempt (+ result/choice/latency on
// resolve).
func hitlNoticeArtifactV1(kind string, e adaptor.Notice) map[string]any {
	out := map[string]any{"kind": kind}
	if v, ok := e.Data["request_id"].(string); ok && v != "" {
		out["request_id"] = v
	}
	if v, ok := e.Data["kind"].(string); ok && v != "" {
		out["decision_kind"] = v
	}
	if v, ok := e.Data["source"].(string); ok && v != "" {
		out["source"] = v
	}
	if v, ok := e.Data["attempt"]; ok {
		out["retry_attempt"] = v
	}
	if kind == "hitl.resolved" {
		if v, ok := e.Data["result"].(string); ok {
			out["result"] = v
		}
		if v, ok := e.Data["choice"].(string); ok && v != "" {
			out["choice"] = v
		}
		if d, ok := e.Data["latency"].(time.Duration); ok {
			out["latency_ms"] = d.Milliseconds()
		}
	}
	return out
}

// terminalArtifactsV1 mirrors terminalArtifacts for the v1 Result type.
func terminalArtifactsV1(info a2aproto.TaskInfoProvider, result *adaptor.Result, exposure ExposurePolicy, built BuiltResult) ([]a2aproto.Event, error) {
	var out []a2aproto.Event
	if !built.ReplaceDefaultArtifacts {
		out = append(out, defaultTerminalArtifactsV1(info, result, exposure)...)
	}
	custom, err := customTerminalArtifacts(info, built.Artifacts, reservedArtifactIDs(built.ReplaceDefaultArtifacts))
	if err != nil {
		return nil, err
	}
	out = append(out, custom...)
	return out, nil
}

// defaultTerminalArtifactsV1 emits the bridge-owned agent-adaptor-result
// artifact. Redaction and exposure opt-ins match the legacy function; the
// v1 Result has no provider-result field, so IncludeProviderResult has
// nothing to expose (seam note in the P4 report).
func defaultTerminalArtifactsV1(info a2aproto.TaskInfoProvider, result *adaptor.Result, exposure ExposurePolicy) []a2aproto.Event {
	if result == nil {
		result = &adaptor.Result{}
	}
	details := map[string]any{
		"summary": result.Summary,
	}
	if exposure.Diagnostics.IncludeMetadata {
		if metadata := sanitizeRemoteMap(stringMapAny(result.Metadata)); len(metadata) > 0 {
			details["metadata"] = metadata
		}
	}
	if exposure.Diagnostics.IncludeUsage && result.Usage != (adaptor.Usage{}) {
		details["usage"] = sanitizeRemoteValue(result.Usage)
	}
	if exposure.Diagnostics.IncludeTranscript {
		if transcript := result.Transcript(); len(transcript) > 0 {
			details["transcript"] = sanitizeRemoteValue(transcript)
		}
	}
	if exposure.Diagnostics.IncludeRawStreams {
		if raw := result.Raw(); raw != (adaptor.RawStreams{}) {
			details["raw_streams"] = map[string]any{
				"stdout": redactInlineSecrets(raw.Stdout),
				"stderr": redactInlineSecrets(raw.Stderr),
			}
		}
	}
	ev := a2aproto.NewArtifactUpdateEvent(info, ArtifactAgentAdaptorResult, dataPart(details))
	ev.Append = false
	ev.LastChunk = true
	ev.Artifact.ID = ArtifactAgentAdaptorResult
	ev.Artifact.Name = ArtifactAgentAdaptorResult
	return []a2aproto.Event{ev}
}

// failureDetailsV1 mirrors failureDetails for the D1 error model: the code
// is the v1 FailureReason; Details ride the metadata opt-in. The legacy
// human_decision block has no v1 source (RunError carries no
// HumanDecisionOutcome) — seam note in the P4 report.
func failureDetailsV1(re *adaptor.RunError, exposure ExposurePolicy) map[string]any {
	if re == nil {
		return nil
	}
	out := map[string]any{"code": string(re.Reason)}
	if exposure.Diagnostics.IncludeMetadata {
		if metadata := sanitizeRemoteMap(re.Details); len(metadata) > 0 {
			out["metadata"] = metadata
		}
	}
	return out
}
