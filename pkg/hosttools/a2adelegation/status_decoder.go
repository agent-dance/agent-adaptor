package a2adelegation

import (
	agentadaptor "github.com/agent-dance/agent-adaptor"
	bridgea2a "github.com/agent-dance/agent-adaptor/pkg/bridges/a2a"
)

type adapterStreamStatusDecoder struct{}

func (adapterStreamStatusDecoder) Profile() string {
	return bridgea2a.AdapterStreamSchemaV1
}

func (adapterStreamStatusDecoder) DecodeStatusPart(data any) ([]StatusPartEvent, bool, error) {
	payload, matched, err := bridgea2a.DecodeAdapterStreamStatus(data)
	if err != nil || !matched {
		return nil, matched, err
	}
	event := StatusPartEvent{
		Sequence:   payload.Sequence,
		MessageID:  payload.MessageID,
		ToolCallID: payload.ToolCallID,
		Name:       payload.Name,
		Role:       string(payload.Role),
		Delta:      payload.Delta,
		Args:       payload.Args,
		Result:     payload.Result,
		Raw:        cloneAnyMap(payload.Raw),
		Time:       payload.Timestamp,
	}
	if event.Raw == nil {
		event.Raw = map[string]any{}
	}
	if payload.RunID != "" {
		event.Raw["member_run_id"] = payload.RunID
	}
	if payload.ThreadID != "" {
		event.Raw["member_thread_id"] = payload.ThreadID
	}
	if payload.TurnID != "" {
		event.Raw["member_turn_id"] = payload.TurnID
	}
	switch payload.Kind {
	case agentadaptor.StreamTextStart:
		event.Kind = DelegationTextStart
	case agentadaptor.StreamTextContent:
		event.Kind = DelegationTextDelta
	case agentadaptor.StreamTextEnd:
		event.Kind = DelegationTextEnd
	case agentadaptor.StreamReasoningStart:
		event.Kind = DelegationReasoningStart
	case agentadaptor.StreamReasoningContent:
		event.Kind = DelegationReasoningDelta
	case agentadaptor.StreamReasoningEnd:
		event.Kind = DelegationReasoningEnd
	case agentadaptor.StreamToolCallStart:
		event.Kind = DelegationToolCallStart
	case agentadaptor.StreamToolCallArgs:
		event.Kind = DelegationToolCallArgs
	case agentadaptor.StreamToolCallResult:
		event.Kind = DelegationToolCallResult
	case agentadaptor.StreamToolCallEnd:
		event.Kind = DelegationToolCallEnd
	case agentadaptor.StreamDropped:
		event.Kind = DelegationStreamDropped
	case agentadaptor.StreamHITLRequested, agentadaptor.StreamHITLResolved:
		event.Kind = DelegationCustom
		event.Name = string(payload.Kind)
	default:
		return nil, true, nil
	}
	return []StatusPartEvent{event}, true, nil
}
