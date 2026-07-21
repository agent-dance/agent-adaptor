package a2a

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	agentadaptor "github.com/agent-dance/agent-adaptor"
)

const (
	// AdapterStreamSchemaV1 标识 Adapter StreamPayload 的 A2A DataPart wire schema。
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

// AdapterStreamEventV1 是 StreamPayload 的 A2A wire DTO。
type AdapterStreamEventV1 struct {
	Kind       string         `json:"kind"`
	Sequence   uint64         `json:"sequence,omitempty"`
	RunID      string         `json:"run_id,omitempty"`
	ThreadID   string         `json:"thread_id,omitempty"`
	TurnID     string         `json:"turn_id,omitempty"`
	MessageID  string         `json:"message_id,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
	Name       string         `json:"name,omitempty"`
	Delta      string         `json:"delta,omitempty"`
	Args       map[string]any `json:"args,omitempty"`
	Result     map[string]any `json:"result,omitempty"`
	Role       string         `json:"role,omitempty"`
	Timestamp  string         `json:"timestamp,omitempty"`
	HITL       map[string]any `json:"hitl,omitempty"`
	Raw        map[string]any `json:"raw,omitempty"`
}

func encodeAdapterStreamStatus(p agentadaptor.StreamPayload, exposure ExposurePolicy) (map[string]any, error) {
	event := AdapterStreamEventV1{
		Kind:       string(p.Kind),
		Sequence:   p.Sequence,
		RunID:      p.RunID,
		ThreadID:   p.ThreadID,
		TurnID:     p.TurnID,
		MessageID:  p.MessageID,
		ToolCallID: p.ToolCallID,
		Name:       p.Name,
		Delta:      redactInlineSecrets(p.Delta),
		Role:       string(p.Role),
	}
	if event.Sequence == 0 {
		event.Sequence = p.Seq
	}
	if !p.Timestamp.IsZero() {
		event.Timestamp = p.Timestamp.UTC().Format(time.RFC3339Nano)
	}
	if len(p.Args) > 0 {
		event.Args = sanitizeRemoteMap(p.Args)
	}
	if len(p.Result) > 0 {
		event.Result = sanitizeRemoteMap(p.Result)
	}
	switch p.Kind {
	case agentadaptor.StreamHITLRequested:
		event.HITL = hitlRequestedArtifact(p, exposure)
	case agentadaptor.StreamHITLResolved:
		event.HITL = hitlResolvedArtifact(p, exposure)
	case agentadaptor.StreamDropped:
		event.Raw = sanitizeRemoteMap(p.Raw)
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

// DecodeAdapterStreamStatus 将 adapter.stream.v1 DataPart 还原为 StreamPayload。
// matched=false 表示 data 不属于该 schema；属于该 schema 但非法时返回错误。
func DecodeAdapterStreamStatus(data any) (payload agentadaptor.StreamPayload, matched bool, err error) {
	raw, err := json.Marshal(data)
	if err != nil {
		return payload, false, nil
	}
	var header struct {
		Schema string `json:"schema"`
	}
	if err := json.Unmarshal(raw, &header); err != nil || header.Schema != AdapterStreamSchemaV1 {
		return payload, false, nil
	}
	if len(raw) > adapterStreamMaxBytes {
		return payload, true, fmt.Errorf("adapter stream status exceeds %d bytes", adapterStreamMaxBytes)
	}
	var envelope AdapterStreamEnvelopeV1
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return payload, true, fmt.Errorf("decode adapter stream status: %w", err)
	}
	event := envelope.Event
	if strings.TrimSpace(event.Kind) == "" {
		return payload, true, errors.New("adapter stream status kind is empty")
	}
	if !supportedAdapterStreamKind(agentadaptor.StreamKind(event.Kind)) {
		return payload, true, fmt.Errorf("unsupported adapter stream status kind %q", event.Kind)
	}
	payload = agentadaptor.StreamPayload{
		Kind:       agentadaptor.StreamKind(event.Kind),
		Sequence:   event.Sequence,
		Seq:        event.Sequence,
		RunID:      event.RunID,
		ThreadID:   event.ThreadID,
		TurnID:     event.TurnID,
		MessageID:  event.MessageID,
		ToolCallID: event.ToolCallID,
		Name:       event.Name,
		Delta:      event.Delta,
		Args:       event.Args,
		Result:     event.Result,
		Role:       agentadaptor.Role(event.Role),
		Raw:        event.Raw,
	}
	if event.Timestamp != "" {
		payload.Timestamp, err = time.Parse(time.RFC3339Nano, event.Timestamp)
		if err != nil {
			return agentadaptor.StreamPayload{}, true, fmt.Errorf("decode adapter stream timestamp: %w", err)
		}
	}
	if len(event.HITL) > 0 {
		payload.Raw = cloneMap(event.HITL)
	}
	return payload, true, nil
}

func supportedAdapterStreamKind(kind agentadaptor.StreamKind) bool {
	switch kind {
	case agentadaptor.StreamTextStart, agentadaptor.StreamTextContent, agentadaptor.StreamTextEnd,
		agentadaptor.StreamToolCallStart, agentadaptor.StreamToolCallArgs, agentadaptor.StreamToolCallEnd,
		agentadaptor.StreamToolCallResult, agentadaptor.StreamReasoningStart,
		agentadaptor.StreamReasoningContent, agentadaptor.StreamReasoningEnd,
		agentadaptor.StreamHITLRequested, agentadaptor.StreamHITLResolved, agentadaptor.StreamDropped:
		return true
	default:
		return false
	}
}
