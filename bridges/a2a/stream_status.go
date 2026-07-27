package a2a

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	adaptor "github.com/agent-dance/agent-adaptor"
)

const (
	// AdapterStreamSchemaV1 identifies the adaptor Event A2A DataPart wire schema.
	AdapterStreamSchemaV1 = "adapter.stream.v1"
	// AdapterStreamExtensionURI 是 Agent Card 声明 adapter.stream.v1 能力的扩展 URI。
	AdapterStreamExtensionURI = "urn:agent-adaptor:stream:v1"
	adapterStreamMaxBytes     = 64 * 1024
)

// AdapterStreamEnvelopeV1 是内置 Agent 在 StatusUpdate DataPart 中使用的稳定信封。
type AdapterStreamEnvelopeV1 struct {
	Schema string               `json:"schema"`
	Event  AdapterStreamEventV1 `json:"event"`
}

// AdapterStreamEventV1 is the stable wire DTO decoded into adaptor.Event.
type AdapterStreamEventV1 struct {
	Kind       string              `json:"kind"`
	Sequence   uint64              `json:"sequence,omitempty"`
	RunID      string              `json:"run_id,omitempty"`
	ThreadID   string              `json:"thread_id,omitempty"`
	TurnID     string              `json:"turn_id,omitempty"`
	MessageID  string              `json:"message_id,omitempty"`
	ToolCallID string              `json:"tool_call_id,omitempty"`
	Name       string              `json:"name,omitempty"`
	Delta      string              `json:"delta,omitempty"`
	Args       map[string]any      `json:"args,omitempty"`
	Result     map[string]any      `json:"result,omitempty"`
	Role       string              `json:"role,omitempty"`
	Timestamp  string              `json:"timestamp,omitempty"`
	HITL       map[string]any      `json:"hitl,omitempty"`
	Raw        map[string]any      `json:"raw,omitempty"`
	Meta       *AdapterEventMetaV1 `json:"meta,omitempty"`
}

// AdapterEventMetaV1 is the lossless wire form of adaptor.EventMeta. Flat
// identity fields remain part of the v1 wire contract; Meta adds the host
// Thread key and provider-source coordinates without making either compete
// with the adaptor envelope.
type AdapterEventMetaV1 struct {
	RunID     string                    `json:"run_id,omitempty"`
	ThreadKey string                    `json:"thread_key,omitempty"`
	Sequence  uint64                    `json:"sequence,omitempty"`
	Time      string                    `json:"time,omitempty"`
	TurnID    string                    `json:"turn_id,omitempty"`
	Source    *AdapterEventSourceMetaV1 `json:"source,omitempty"`
}

// AdapterEventSourceMetaV1 is the opt-in provider envelope nested under the
// adaptor-owned AdapterEventMetaV1 coordinates.
type AdapterEventSourceMetaV1 struct {
	RunID     string `json:"run_id,omitempty"`
	ThreadID  string `json:"thread_id,omitempty"`
	TurnID    string `json:"turn_id,omitempty"`
	Sequence  uint64 `json:"sequence,omitempty"`
	Timestamp string `json:"timestamp,omitempty"`
}

func decodeAdapterStreamEventV1Wire(data any) (event AdapterStreamEventV1, matched bool, err error) {
	raw, err := json.Marshal(data)
	if err != nil {
		return event, false, nil
	}
	var header struct {
		Schema string `json:"schema"`
	}
	if err := json.Unmarshal(raw, &header); err != nil || header.Schema != AdapterStreamSchemaV1 {
		return event, false, nil
	}
	if len(raw) > adapterStreamMaxBytes {
		return event, true, fmt.Errorf("adapter stream status exceeds %d bytes", adapterStreamMaxBytes)
	}
	var envelope AdapterStreamEnvelopeV1
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return event, true, fmt.Errorf("decode adapter stream status: %w", err)
	}
	event = envelope.Event
	if strings.TrimSpace(event.Kind) == "" {
		return event, true, errors.New("adapter stream status kind is empty")
	}
	if !supportedAdapterStreamKind(event.Kind) {
		return event, true, fmt.Errorf("unsupported adapter stream status kind %q", event.Kind)
	}
	if event.Timestamp != "" {
		if _, err := time.Parse(time.RFC3339Nano, event.Timestamp); err != nil {
			return AdapterStreamEventV1{}, true, fmt.Errorf("decode adapter stream timestamp: %w", err)
		}
	}
	if event.Meta != nil {
		if event.Meta.Time != "" {
			if _, err := time.Parse(time.RFC3339Nano, event.Meta.Time); err != nil {
				return AdapterStreamEventV1{}, true, fmt.Errorf("decode adapter event meta time: %w", err)
			}
		}
		if event.Meta.Source != nil && event.Meta.Source.Timestamp != "" {
			if _, err := time.Parse(time.RFC3339Nano, event.Meta.Source.Timestamp); err != nil {
				return AdapterStreamEventV1{}, true, fmt.Errorf("decode adapter event source timestamp: %w", err)
			}
		}
	}
	return event, true, nil
}

// DecodeAdapterEventV1 restores one adapter.stream.v1 DataPart as the public
// v1 Event vocabulary, including EventMeta, complete tool snapshots, approval
// request fields, and detailed Dropped markers. matched=false means the value
// belongs to another DataPart schema.
func DecodeAdapterEventV1(data any) (decoded adaptor.Event, matched bool, err error) {
	event, matched, err := decodeAdapterStreamEventV1Wire(data)
	if err != nil || !matched {
		return nil, matched, err
	}

	switch event.Kind {
	case "text.start", "text.content", "text.end":
		decoded = adaptor.TextDelta{
			MessageID: event.MessageID, Text: event.Delta,
			Role: adaptor.Role(event.Role), Phase: adapterPhaseV1(event.Kind),
		}
	case "reasoning.start", "reasoning.content", "reasoning.end":
		decoded = adaptor.Thinking{MessageID: event.MessageID, Text: event.Delta, Phase: adapterPhaseV1(event.Kind)}
	case "tool_call.start", "tool_call.args", "tool_call.end":
		decoded = adaptor.ToolCall{
			ID: event.ToolCallID, Name: event.Name, Args: cloneMap(event.Args),
			ArgsDelta: event.Delta, Result: cloneMap(event.Result), Phase: adapterPhaseV1(event.Kind),
		}
	case "tool_call.result":
		decoded = adaptor.ToolResult{ID: event.ToolCallID, Result: cloneMap(event.Result)}
	case "hitl.requested":
		decoded, err = decodeApprovalRequestV1(event)
	case "hitl.resolved":
		decoded = decodeApprovalResolvedV1(event)
	case "stream.dropped":
		decoded = decodeDroppedV1(event.Raw)
	default:
		return nil, true, fmt.Errorf("unsupported adapter event kind %q", event.Kind)
	}
	if err != nil {
		return nil, true, err
	}
	return adaptor.WithEventMeta(decoded, decodeEventMetaV1(event)), true, nil
}

func adapterPhaseV1(kind string) adaptor.Phase {
	switch {
	case strings.HasSuffix(kind, ".start"):
		return adaptor.PhaseStart
	case strings.HasSuffix(kind, ".end"):
		return adaptor.PhaseEnd
	default:
		return adaptor.PhaseContent
	}
}

func decodeEventMetaV1(event AdapterStreamEventV1) adaptor.EventMeta {
	meta := adaptor.EventMeta{
		RunID: event.RunID, ThreadKey: event.ThreadID, TurnID: event.TurnID,
		Sequence: event.Sequence,
	}
	if event.Timestamp != "" {
		meta.Time, _ = time.Parse(time.RFC3339Nano, event.Timestamp)
	}
	if event.Meta == nil {
		return meta
	}
	if event.Meta.RunID != "" {
		meta.RunID = event.Meta.RunID
	}
	if event.Meta.ThreadKey != "" {
		meta.ThreadKey = event.Meta.ThreadKey
	}
	if event.Meta.TurnID != "" {
		meta.TurnID = event.Meta.TurnID
	}
	if event.Meta.Sequence != 0 {
		meta.Sequence = event.Meta.Sequence
	}
	if event.Meta.Time != "" {
		meta.Time, _ = time.Parse(time.RFC3339Nano, event.Meta.Time)
	}
	if event.Meta.Source != nil {
		meta.Source = &adaptor.EventSourceMeta{
			RunID: event.Meta.Source.RunID, ThreadID: event.Meta.Source.ThreadID,
			TurnID: event.Meta.Source.TurnID, Sequence: event.Meta.Source.Sequence,
		}
		if event.Meta.Source.Timestamp != "" {
			meta.Source.Timestamp, _ = time.Parse(time.RFC3339Nano, event.Meta.Source.Timestamp)
		}
	}
	return meta
}

func decodeApprovalRequestV1(event AdapterStreamEventV1) (adaptor.Event, error) {
	hitl := event.HITL
	request := &adaptor.ApprovalRequest{
		ID:     stringValueV1(hitl["request_id"]),
		Kind:   adaptor.ApprovalKind(stringValueV1(hitl["decision_kind"])),
		Source: stringValueV1(hitl["source"]), Attempt: intValueV1(hitl["retry_attempt"]),
		ToolCallID: event.ToolCallID,
	}
	if rawDeadline := stringValueV1(hitl["deadline"]); rawDeadline != "" {
		deadline, err := time.Parse(time.RFC3339, rawDeadline)
		if err != nil {
			return nil, fmt.Errorf("decode approval deadline: %w", err)
		}
		request.Deadline = deadline
	}
	if fields, ok := hitl["request"].(map[string]any); ok {
		request.Title = stringValueV1(fields["prompt"])
		if toolCallID := stringValueV1(fields["tool_call_id"]); toolCallID != "" {
			request.ToolCallID = toolCallID
		}
		if details, ok := fields["payload"].(map[string]any); ok {
			request.Details = cloneMap(details)
		}
		if choices, ok := fields["choices"].([]any); ok {
			for _, rawChoice := range choices {
				choice, ok := rawChoice.(map[string]any)
				if !ok {
					continue
				}
				request.Choices = append(request.Choices, adaptor.Choice{
					Key: stringValueV1(choice["key"]), Label: stringValueV1(choice["label"]),
					Description: stringValueV1(choice["description"]),
				})
			}
		}
	}
	return request, nil
}

func decodeApprovalResolvedV1(event AdapterStreamEventV1) adaptor.Event {
	data := map[string]any{
		"request_id": event.HITL["request_id"], "kind": event.HITL["decision_kind"],
		"source": event.HITL["source"], "attempt": event.HITL["retry_attempt"],
		"result": event.HITL["result"], "choice": event.HITL["choice"],
	}
	if latency, ok := event.HITL["latency_ms"].(float64); ok {
		data["latency"] = time.Duration(latency) * time.Millisecond
	}
	return adaptor.Notice{Kind: adaptor.NoticeApprovalResolved, Data: data}
}

func decodeDroppedV1(raw map[string]any) adaptor.Event {
	dropped := adaptor.Dropped{
		Count: intValueV1(raw["dropped_count"]), FirstSequence: uint64ValueV1(raw["first_sequence"]),
		LastSequence: uint64ValueV1(raw["last_sequence"]), Reason: stringValueV1(raw["reason"]),
		Source: stringValueV1(raw["source"]),
	}
	if byKind, ok := raw["by_kind"].(map[string]any); ok {
		dropped.ByKind = make(map[string]int, len(byKind))
		for kind, count := range byKind {
			dropped.ByKind[kind] = intValueV1(count)
		}
	}
	if details, ok := raw["details"].(map[string]any); ok {
		dropped.Details = cloneMap(details)
	}
	return dropped
}

func stringValueV1(value any) string {
	text, _ := value.(string)
	return text
}

func intValueV1(value any) int { return int(uint64ValueV1(value)) }

func uint64ValueV1(value any) uint64 {
	switch number := value.(type) {
	case uint64:
		return number
	case int:
		if number > 0 {
			return uint64(number)
		}
	case float64:
		if number > 0 {
			return uint64(number)
		}
	}
	return 0
}

func supportedAdapterStreamKind(kind string) bool {
	switch kind {
	case "text.start", "text.content", "text.end",
		"tool_call.start", "tool_call.args", "tool_call.end", "tool_call.result",
		"reasoning.start", "reasoning.content", "reasoning.end",
		"hitl.requested", "hitl.resolved", "stream.dropped":
		return true
	default:
		return false
	}
}
