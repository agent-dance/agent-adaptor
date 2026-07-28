package a2adelegation

import (
	adaptor "github.com/agent-dance/agent-adaptor"
	bridgea2a "github.com/agent-dance/agent-adaptor/bridges/a2a"
)

type adapterStreamStatusDecoder struct{}

func (adapterStreamStatusDecoder) Profile() string {
	return bridgea2a.AdapterStreamSchemaV1
}

func (adapterStreamStatusDecoder) DecodeStatusPart(data any) ([]DelegationEvent, bool, error) {
	decoded, matched, err := bridgea2a.DecodeAdapterEventV1(data)
	if err != nil || !matched {
		return nil, matched, err
	}
	event, supported := delegationEventFromAdapterEvent(decoded)
	if !supported {
		return nil, true, nil
	}
	return []DelegationEvent{event}, true, nil
}

func delegationEventFromAdapterEvent(decoded adaptor.Event) (DelegationEvent, bool) {
	if decoded == nil {
		return DelegationEvent{}, false
	}
	meta := decoded.Meta()
	event := DelegationEvent{
		Sequence: meta.Sequence,
		Time:     meta.Time,
		Raw:      map[string]any{},
	}
	if meta.RunID != "" {
		event.Raw["member_run_id"] = meta.RunID
	}
	if meta.ThreadKey != "" {
		event.Raw["member_thread_key"] = meta.ThreadKey
	}
	if meta.TurnID != "" {
		event.Raw["member_turn_id"] = meta.TurnID
	}
	if meta.Source != nil {
		if meta.Source.RunID != "" {
			event.Raw["member_provider_run_id"] = meta.Source.RunID
		}
		if meta.Source.ThreadID != "" {
			event.Raw["member_thread_id"] = meta.Source.ThreadID
			event.Raw["member_provider_thread_id"] = meta.Source.ThreadID
		}
		if meta.Source.TurnID != "" {
			event.Raw["member_provider_turn_id"] = meta.Source.TurnID
		}
		event.Raw["member_provider_sequence"] = meta.Source.Sequence
		if !meta.Source.Timestamp.IsZero() {
			event.Raw["member_provider_timestamp"] = meta.Source.Timestamp
		}
	} else {
		// The adapter.stream.v1 wire contract permits a flat ThreadID when
		// provider Source coordinates are absent. DecodeAdapterEventV1 maps
		// it to ThreadKey; retain that value as the member thread coordinate.
		if meta.ThreadKey != "" {
			event.Raw["member_thread_id"] = meta.ThreadKey
		}
	}

	switch typed := decoded.(type) {
	case adaptor.TextDelta:
		event.RemoteMessageID = typed.MessageID
		event.Role = string(typed.Role)
		event.Delta = typed.Text
		event.Kind = delegationTextKind(typed.Phase)
	case adaptor.Thinking:
		event.RemoteMessageID = typed.MessageID
		event.Delta = typed.Text
		event.Kind = delegationReasoningKind(typed.Phase)
	case adaptor.ToolCall:
		event.RemoteToolCallID = typed.ID
		event.ToolName = typed.Name
		event.Args = cloneAnyMap(typed.Args)
		event.Delta = typed.ArgsDelta
		event.Result = cloneAnyMap(typed.Result)
		event.Kind = delegationToolCallKind(typed.Phase)
	case adaptor.ToolResult:
		event.RemoteToolCallID = typed.ID
		event.Result = cloneAnyMap(typed.Result)
		event.Kind = DelegationToolCallResult
	case adaptor.Dropped:
		event.Kind = DelegationStreamDropped
		event.Raw["dropped_count"] = typed.Count
		event.Raw["by_kind"] = cloneStringIntMap(typed.ByKind)
		event.Raw["first_sequence"] = typed.FirstSequence
		event.Raw["last_sequence"] = typed.LastSequence
		event.Raw["reason"] = typed.Reason
		event.Raw["source"] = typed.Source
		event.Raw["details"] = cloneAnyMap(typed.Details)
	case *adaptor.ApprovalRequest:
		if typed == nil {
			return DelegationEvent{}, false
		}
		event.RemoteToolCallID = typed.ToolCallID
		event.Kind = DelegationCustom
		event.Name = "hitl.requested"
		event.Raw["request_id"] = typed.ID
		event.Raw["decision_kind"] = string(typed.Kind)
		event.Raw["title"] = typed.Title
		event.Raw["source"] = typed.Source
		event.Raw["tool_call_id"] = typed.ToolCallID
		event.Raw["choices"] = append([]adaptor.Choice(nil), typed.Choices...)
		event.Raw["details"] = cloneAnyMap(typed.Details)
		event.Raw["created_at"] = typed.CreatedAt
		event.Raw["deadline"] = typed.Deadline
		event.Raw["attempt"] = typed.Attempt
	case adaptor.Notice:
		if typed.Kind != adaptor.NoticeApprovalResolved {
			return DelegationEvent{}, false
		}
		event.Kind = DelegationCustom
		event.Name = "hitl.resolved"
		event.Text = typed.Text
		for key, value := range typed.Data {
			event.Raw[key] = value
		}
	default:
		return DelegationEvent{}, false
	}
	return event, true
}

func delegationTextKind(phase adaptor.Phase) DelegationEventKind {
	switch phase {
	case adaptor.PhaseStart:
		return DelegationTextStart
	case adaptor.PhaseEnd:
		return DelegationTextEnd
	default:
		return DelegationTextDelta
	}
}

func delegationReasoningKind(phase adaptor.Phase) DelegationEventKind {
	switch phase {
	case adaptor.PhaseStart:
		return DelegationReasoningStart
	case adaptor.PhaseEnd:
		return DelegationReasoningEnd
	default:
		return DelegationReasoningDelta
	}
}

func delegationToolCallKind(phase adaptor.Phase) DelegationEventKind {
	switch phase {
	case adaptor.PhaseStart:
		return DelegationToolCallStart
	case adaptor.PhaseEnd:
		return DelegationToolCallEnd
	default:
		return DelegationToolCallArgs
	}
}

func cloneStringIntMap(source map[string]int) map[string]int {
	if len(source) == 0 {
		return nil
	}
	result := make(map[string]int, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}
