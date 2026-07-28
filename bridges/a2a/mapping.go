package a2a

// This file projects adaptor.Event onto the adapter.stream.v1 DataPart wire
// and terminal A2A artifacts. DecodeAdapterEventV1 restores the public Event
// vocabulary from that stable wire schema.

import (
	"encoding/json"
	"fmt"
	"time"

	a2aproto "github.com/a2aproject/a2a-go/v2/a2a"

	adaptor "github.com/agent-dance/agent-adaptor"
)

// streamTranslator maps unified events onto A2A working-status DataParts.
// Exposure gating is conservative: text always
// crosses; reasoning / tool calls / HITL are opt-in; run failures become a
// failed status; drop markers require any streaming diagnostics; everything
// else (process output, notices, subagent updates) stays server-side.
type streamTranslator struct {
	info     a2aproto.TaskInfoProvider
	exposure ExposurePolicy

	runID    string
	threadID string
	seq      uint64
}

func newStreamTranslator(info a2aproto.TaskInfoProvider, exposure ExposurePolicy) *streamTranslator {
	return &streamTranslator{info: info, exposure: exposure}
}

func (t *streamTranslator) Translate(ev adaptor.Event) []a2aproto.Event {
	switch e := ev.(type) {
	case adaptor.RunStarted:
		// Not forwarded; remembered so subsequent wire
		// frames carry run/thread identity.
		t.runID = e.RunID
		t.threadID = e.ThreadID
		if meta := e.Meta(); meta.RunID != "" {
			t.runID = meta.RunID
		}
		return nil

	case adaptor.RunFinished:
		if e.RunID != "" {
			t.runID = e.RunID
		}
		if e.ThreadID != "" {
			t.threadID = e.ThreadID
		}
		// Informational only. executor drains the stream and projects
		// exactly one terminal from Stream.Result().
		return nil

	case adaptor.TextDelta:
		kind := "text.content"
		switch e.Phase {
		case adaptor.PhaseStart:
			kind = "text.start"
		case adaptor.PhaseEnd:
			kind = "text.end"
		}
		return t.emit(ev, AdapterStreamEventV1{
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
		return t.emit(ev, AdapterStreamEventV1{
			Kind:      kind,
			MessageID: e.MessageID,
			Delta:     e.Text,
		})

	case adaptor.ToolCall:
		if !t.exposure.IncludeToolCalls {
			return nil
		}
		event := AdapterStreamEventV1{
			ToolCallID: e.ID, Name: e.Name, Delta: e.ArgsDelta,
			Args: e.Args, Result: e.Result,
		}
		switch e.Phase {
		case adaptor.PhaseStart:
			event.Kind = "tool_call.start"
		case adaptor.PhaseEnd:
			event.Kind = "tool_call.end"
		default:
			event.Kind = "tool_call.args"
		}
		return t.emit(ev, event)

	case adaptor.ToolResult:
		if !t.exposure.IncludeToolCalls {
			return nil
		}
		return t.emit(ev, AdapterStreamEventV1{
			Kind:       "tool_call.result",
			ToolCallID: e.ID,
			Result:     e.Result,
		})

	case *adaptor.ApprovalRequest:
		if !t.exposure.IncludeHITL || e == nil {
			return nil
		}
		return t.emit(ev, AdapterStreamEventV1{
			Kind:       "hitl.requested",
			ToolCallID: e.ToolCallID,
			HITL:       hitlRequestedArtifact(e, t.exposure),
		})

	case adaptor.Notice:
		switch e.Kind {
		case adaptor.NoticeApprovalRequested:
			if !t.exposure.IncludeHITL {
				return nil
			}
			return t.emit(ev, AdapterStreamEventV1{
				Kind: "hitl.requested",
				HITL: hitlNoticeArtifact("hitl.requested", e, t.exposure),
			})
		case adaptor.NoticeApprovalResolved:
			if !t.exposure.IncludeHITL {
				return nil
			}
			return t.emit(ev, AdapterStreamEventV1{
				Kind: "hitl.resolved",
				HITL: hitlNoticeArtifact("hitl.resolved", e, t.exposure),
			})
		}
		return nil // step/lifecycle/runtime notices stay server-side

	case adaptor.Dropped:
		if !t.exposure.hasStreamingDiagnostics() {
			return nil
		}
		return t.emit(ev, AdapterStreamEventV1{
			Kind: "stream.dropped",
			Raw: map[string]any{
				"dropped_count": e.Count, "by_kind": e.ByKind,
				"first_sequence": e.FirstSequence, "last_sequence": e.LastSequence,
				"reason": e.Reason, "source": e.Source, "details": e.Details,
			},
		})
	}
	return nil // ProcessInfo, SubagentUpdate, unknown: not exposed
}

// emit stamps identity + sequence onto the wire DTO, encodes the envelope
// (redaction + 64KiB cap + JSON normalization), and wraps it in a
// working-status DataPart message.
func (t *streamTranslator) emit(source adaptor.Event, event AdapterStreamEventV1) []a2aproto.Event {
	meta := source.Meta()
	if meta.Sequence != 0 {
		event.Sequence = meta.Sequence
		if meta.Sequence > t.seq {
			t.seq = meta.Sequence
		}
	} else {
		t.seq++
		event.Sequence = t.seq
	}
	if meta.RunID != "" {
		event.RunID = meta.RunID
	} else if event.RunID == "" {
		event.RunID = t.runID
	}
	if meta.ThreadKey != "" {
		event.ThreadID = meta.ThreadKey
	} else if event.ThreadID == "" {
		event.ThreadID = t.threadID
	}
	if meta.TurnID != "" {
		event.TurnID = meta.TurnID
	}
	if !meta.Time.IsZero() {
		event.Timestamp = meta.Time.UTC().Format(time.RFC3339Nano)
	}
	event.Meta = adapterEventMeta(meta, t.exposure.Diagnostics.IncludeMetadata)
	data, err := encodeAdapterStreamEvent(event)
	if err != nil {
		data = encodeAdapterStreamDrop(event, err)
	}
	msg := a2aproto.NewMessageForTask(a2aproto.MessageRoleAgent, t.info, dataPart(data))
	return []a2aproto.Event{a2aproto.NewStatusUpdateEvent(t.info, a2aproto.TaskStateWorking, msg)}
}

func adapterEventMeta(meta adaptor.EventMeta, includeSource bool) *AdapterEventMetaV1 {
	if meta == (adaptor.EventMeta{}) {
		return nil
	}
	out := &AdapterEventMetaV1{
		RunID: meta.RunID, ThreadKey: meta.ThreadKey, Sequence: meta.Sequence,
		TurnID: meta.TurnID,
	}
	if !meta.Time.IsZero() {
		out.Time = meta.Time.UTC().Format(time.RFC3339Nano)
	}
	if includeSource && meta.Source != nil {
		out.Source = &AdapterEventSourceMetaV1{
			RunID: meta.Source.RunID, ThreadID: meta.Source.ThreadID,
			TurnID: meta.Source.TurnID, Sequence: meta.Source.Sequence,
		}
		if !meta.Source.Timestamp.IsZero() {
			out.Source.Timestamp = meta.Source.Timestamp.UTC().Format(time.RFC3339Nano)
		}
	}
	return out
}

// encodeAdapterStreamEvent applies the shared redaction pipeline to a
// pre-built wire DTO: inline-secret redaction on the delta, remote-map
// sanitization on args/result/hitl/raw, the 64KiB envelope cap, and the
// marshal→unmarshal JSON normalization.
func encodeAdapterStreamEvent(event AdapterStreamEventV1) (map[string]any, error) {
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

// encodeAdapterStreamDrop is the fallback frame when an event cannot be
// encoded (marshal failure or size cap).
func encodeAdapterStreamDrop(event AdapterStreamEventV1, cause error) map[string]any {
	dropped := AdapterStreamEnvelopeV1{Schema: AdapterStreamSchemaV1, Event: AdapterStreamEventV1{
		Kind: "stream.dropped", Sequence: event.Sequence,
		RunID: event.RunID, ThreadID: event.ThreadID, TurnID: event.TurnID,
		Timestamp: event.Timestamp, Meta: event.Meta,
		Raw: map[string]any{
			"dropped_kind": event.Kind, "dropped_count": 1, "reason": cause.Error(),
		},
	}}
	raw, err := json.Marshal(dropped)
	if err == nil {
		var normalized map[string]any
		if json.Unmarshal(raw, &normalized) == nil {
			return normalized
		}
	}
	return map[string]any{"schema": AdapterStreamSchemaV1, "event": map[string]any{
		"kind": "stream.dropped", "sequence": event.Sequence,
		"raw": map[string]any{"dropped_kind": event.Kind, "dropped_count": 1, "reason": cause.Error()},
	}}
}

// hitlRequestedArtifact projects a live *ApprovalRequest (full fidelity:
// the event carries deadline, payload, and choices).
func hitlRequestedArtifact(req *adaptor.ApprovalRequest, exposure ExposurePolicy) map[string]any {
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

// hitlNoticeArtifact projects the requested/resolved approval Notices
// (callback / auto-resolve paths). The notice Data carries the reduced
// field set: request_id, kind, source, attempt (+ result/choice/latency on
// resolve).
func hitlNoticeArtifact(kind string, e adaptor.Notice, exposure ExposurePolicy) map[string]any {
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
	if exposure.Diagnostics.IncludeHITLRaw && len(e.Data) > 0 {
		out["raw"] = sanitizeRemoteMap(e.Data)
	}
	return out
}

// terminalArtifacts builds the terminal artifact set for a Result.
func terminalArtifacts(info a2aproto.TaskInfoProvider, result *adaptor.Result, exposure ExposurePolicy, built BuiltResult) ([]a2aproto.Event, error) {
	var out []a2aproto.Event
	if !built.ReplaceDefaultArtifacts {
		out = append(out, defaultTerminalArtifacts(info, result, exposure)...)
	}
	custom, err := customTerminalArtifacts(info, built.Artifacts, reservedArtifactIDs(built.ReplaceDefaultArtifacts))
	if err != nil {
		return nil, err
	}
	out = append(out, custom...)
	return out, nil
}

// defaultTerminalArtifacts emits the bridge-owned agent-adaptor-result
// artifact with explicit exposure opt-ins and redaction.
func defaultTerminalArtifacts(info a2aproto.TaskInfoProvider, result *adaptor.Result, exposure ExposurePolicy) []a2aproto.Event {
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
	if exposure.Diagnostics.IncludeUsage && result.Usage != nil {
		details["usage"] = sanitizeRemoteValue(*result.Usage)
	}
	if exposure.Diagnostics.IncludeTranscript {
		if transcript := result.Transcript(); len(transcript) > 0 {
			details["transcript"] = sanitizeRemoteValue(transcript)
		}
	}
	if exposure.Diagnostics.IncludeRawStreams {
		if raw := result.Raw(); raw.Stdout != "" || raw.Stderr != "" {
			details["raw_streams"] = map[string]any{
				"stdout": redactInlineSecrets(raw.Stdout),
				"stderr": redactInlineSecrets(raw.Stderr),
			}
		}
	}
	if exposure.Diagnostics.IncludeProviderResult {
		if terminal := result.Raw().Terminal; terminal != nil && len(terminal.JSON) > 0 && json.Valid(terminal.JSON) {
			var payload any
			if err := json.Unmarshal(terminal.JSON, &payload); err == nil {
				details["provider_result"] = map[string]any{
					"event":   redactInlineSecrets(terminal.Event),
					"payload": sanitizeRemoteValue(payload),
				}
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

// failureDetails exposes the typed FailureReason; Details ride the metadata
// opt-in.
func failureDetails(re *adaptor.RunError, exposure ExposurePolicy) map[string]any {
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
