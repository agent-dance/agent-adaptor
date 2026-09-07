package a2a

import (
	"fmt"
	"strings"
	"time"

	adaptor "github.com/agent-dance/agent-adaptor"
)

const (
	// AdapterStreamSchemaV1 identifies the versioned adaptor Event A2A DataPart
	// wire schema. Its V1 suffix denotes the wire version.
	AdapterStreamSchemaV1 = "adapter.stream.v1"
	// AdapterStreamExtensionURI identifies the Agent Card extension that
	// advertises adapter.stream.v1 status DataParts.
	AdapterStreamExtensionURI = "urn:agent-adaptor:stream:v1"
	adapterStreamMaxBytes     = 64 * 1024
)

// AdapterStreamEnvelopeV1 is the stable, versioned wire envelope used in
// StatusUpdate DataParts. Its V1 suffix is intentional.
type AdapterStreamEnvelopeV1 struct {
	// Schema is always [AdapterStreamSchemaV1].
	Schema string `json:"schema"`
	// Event contains one projected adaptor Event.
	Event AdapterStreamEventV1 `json:"event"`
}

// AdapterStreamEventV1 is the stable versioned wire DTO decoded into
// adaptor.Event. Its V1 suffix is intentional.
type AdapterStreamEventV1 struct {
	// Kind identifies the event shape, such as text.content, tool_call.result,
	// hitl.requested, or stream.dropped.
	Kind string `json:"kind"`
	// Sequence is the flattened event sequence retained for v1 wire
	// compatibility.
	Sequence uint64 `json:"sequence,omitempty"`
	// RunID is the flattened run identifier retained for v1 wire compatibility.
	RunID string `json:"run_id,omitempty"`
	// ThreadID is the flattened thread coordinate retained for v1 wire
	// compatibility. It prefers the host Thread key and otherwise carries the
	// provider lifecycle thread identifier.
	ThreadID string `json:"thread_id,omitempty"`
	// TurnID identifies the provider turn, when reported.
	TurnID string `json:"turn_id,omitempty"`
	// MessageID identifies a text or reasoning message.
	MessageID string `json:"message_id,omitempty"`
	// ToolCallID identifies a tool call or approval-associated tool call.
	ToolCallID string `json:"tool_call_id,omitempty"`
	// Name is the tool name for tool-call events.
	Name string `json:"name,omitempty"`
	// Delta contains incremental text or tool arguments.
	Delta string `json:"delta,omitempty"`
	// Args contains a complete structured tool argument snapshot when available.
	Args map[string]any `json:"args,omitempty"`
	// Result contains a structured tool result snapshot when available.
	Result map[string]any `json:"result,omitempty"`
	// Role identifies the text message role.
	Role string `json:"role,omitempty"`
	// Timestamp is the flattened RFC 3339 event time retained for v1 wire
	// compatibility.
	Timestamp string `json:"timestamp,omitempty"`
	// HITL contains the approval request or resolution payload.
	HITL map[string]any `json:"hitl,omitempty"`
	// Raw contains event-specific diagnostic fields, including Dropped details.
	Raw map[string]any `json:"raw,omitempty"`
	// ScopeID and parent coordinates identify tools without conflating nested scopes.
	ScopeID          string `json:"scope_id,omitempty"`
	ParentScopeID    string `json:"parent_scope_id,omitempty"`
	ParentToolCallID string `json:"parent_tool_call_id,omitempty"`
	// Capability is the closed capability fact for capability.invocation only.
	Capability *AdapterCapabilityInvocationV1 `json:"capability,omitempty"`
	// Todo is the full ordered snapshot for todo.updated only; an empty array clears it.
	Todo *AdapterTodoSnapshotV1 `json:"todo,omitempty"`
	// Meta is the adaptor-owned event metadata envelope.
	Meta *AdapterEventMetaV1 `json:"meta,omitempty"`
}

// AdapterEventMetaV1 is the versioned wire form of adaptor.EventMeta. Its V1
// suffix is intentional. Flat identity fields remain part of the v1 wire
// contract; Meta adds the host Thread key and, when exposure allows it,
// upstream-source coordinates without making either compete with the adaptor
// envelope.
type AdapterEventMetaV1 struct {
	// RunID is the adaptor-assigned run identifier.
	RunID string `json:"run_id,omitempty"`
	// ThreadKey is the host-visible adaptor Thread key.
	ThreadKey string `json:"thread_key,omitempty"`
	// Sequence is the adaptor-assigned receive-order sequence.
	Sequence uint64 `json:"sequence,omitempty"`
	// Time is the adaptor event time formatted as RFC 3339 with nanoseconds.
	Time string `json:"time,omitempty"`
	// TurnID identifies the provider turn when available.
	TurnID string `json:"turn_id,omitempty"`
	// Source contains provider-reported coordinates when metadata exposure is
	// enabled.
	Source *AdapterEventSourceMetaV1 `json:"source,omitempty"`
}

// AdapterEventSourceMetaV1 is the versioned opt-in upstream envelope nested
// under the adaptor-owned AdapterEventMetaV1 coordinates. Its V1 suffix is
// intentional.
type AdapterEventSourceMetaV1 struct {
	// RunID is the provider-reported run identifier.
	RunID string `json:"run_id,omitempty"`
	// ThreadID is the provider-reported thread identifier.
	ThreadID string `json:"thread_id,omitempty"`
	// TurnID is the provider-reported turn identifier.
	TurnID string `json:"turn_id,omitempty"`
	// Sequence is the provider-reported sequence.
	Sequence uint64 `json:"sequence,omitempty"`
	// ScopeID, ToolCallID, InvocationID and DelegationID retain upstream coordinates.
	ScopeID      string `json:"scope_id,omitempty"`
	ToolCallID   string `json:"tool_call_id,omitempty"`
	InvocationID string `json:"invocation_id,omitempty"`
	DelegationID string `json:"delegation_id,omitempty"`
	// Upstream retains the preceding source envelope, to a maximum of eight nodes.
	Upstream *AdapterEventSourceMetaV1 `json:"upstream,omitempty"`
	// Timestamp is the provider-reported time formatted as RFC 3339 with
	// nanoseconds.
	Timestamp string `json:"timestamp,omitempty"`
}

// AdapterCapabilityInvocationV1 contains only safe, validated observation fields.
// DurationNS preserves an observed zero; absence means unobserved duration.
type AdapterCapabilityInvocationV1 struct {
	InvocationID     string `json:"invocation_id"`
	Kind             string `json:"kind"`
	Key              string `json:"key"`
	Operation        string `json:"operation"`
	Phase            string `json:"phase"`
	Evidence         string `json:"evidence"`
	Source           string `json:"source"`
	ScopeID          string `json:"scope_id,omitempty"`
	ParentScopeID    string `json:"parent_scope_id,omitempty"`
	ParentToolCallID string `json:"parent_tool_call_id,omitempty"`
	OccurredAt       string `json:"occurred_at"`
	DurationNS       *int64 `json:"duration_ns,omitempty"`
	ErrorCode        string `json:"error_code,omitempty"`
}

// AdapterTodoItemV1 is one ordered, confirmed todo entry. Content is subject
// to the sender's explicit todo exposure and existing inline-secret filtering.
type AdapterTodoItemV1 struct {
	ID          string `json:"id"`
	Content     string `json:"content"`
	Status      string `json:"status"`
	SyntheticID bool   `json:"synthetic_id"`
}

// AdapterTodoSnapshotV1 is a full scope snapshot, including an empty Items array
// for a confirmed clear. Revision is independent of the event sequence.
type AdapterTodoSnapshotV1 struct {
	Items            []AdapterTodoItemV1 `json:"items"`
	Source           string              `json:"source"`
	ScopeID          string              `json:"scope_id,omitempty"`
	ParentScopeID    string              `json:"parent_scope_id,omitempty"`
	ParentToolCallID string              `json:"parent_tool_call_id,omitempty"`
	Revision         uint64              `json:"revision"`
	OccurredAt       string              `json:"occurred_at"`
}

// DecodeAdapterEventV1 restores one adapter.stream.v1 DataPart as the public
// Event vocabulary, including EventMeta, complete tool snapshots, approval
// request fields, and detailed Dropped markers. A false matched result means
// the value belongs to another DataPart schema. Its V1 suffix is intentional
// because it selects the versioned wire schema.
//
// Capability and todo events use closed fields and require authoritative Meta.
// Invalid or unknown events from this schema return matched=true and an error,
// with no partial Event. RawMessage input is checked without re-marshaling, so
// duplicate keys, invalid UTF-8, unpaired surrogates and trailing values cannot
// be repaired in the new closed shapes. Added parent/source fields on legacy
// kinds are checked independently of their existing tool/diagnostic payloads.
// Legacy optional zero times keep their published semantics. Already decoded
// maps have lost their original byte evidence. OccurredAt values decode to UTC.
// The complete envelope is limited to 64 KiB and JSON-safe integer coordinates;
// source chains have at most eight nodes. Opaque Thread keys retain all valid
// UTF-8 content and have no separate byte cap. Returned values own their copies.
func DecodeAdapterEventV1(data any) (decoded adaptor.Event, matched bool, err error) {
	event, matched, err := decodeAdapterStreamEventWire(data)
	if err != nil || !matched {
		return nil, matched, err
	}

	switch event.Kind {
	case "text.start", "text.content", "text.end":
		decoded = adaptor.TextDelta{
			MessageID: event.MessageID, Text: event.Delta,
			Role: adaptor.Role(event.Role), Phase: adapterPhase(event.Kind),
		}
	case "reasoning.start", "reasoning.content", "reasoning.end":
		decoded = adaptor.Thinking{MessageID: event.MessageID, Text: event.Delta, Phase: adapterPhase(event.Kind)}
	case "tool_call.start", "tool_call.args", "tool_call.end":
		decoded = adaptor.ToolCall{
			ID: event.ToolCallID, Name: event.Name, Args: cloneMap(event.Args),
			ScopeID: event.ScopeID, ParentScopeID: event.ParentScopeID, ParentToolCallID: event.ParentToolCallID,
			ArgsDelta: event.Delta, Result: cloneMap(event.Result), Phase: adapterPhase(event.Kind),
		}
	case "tool_call.result":
		decoded = adaptor.ToolResult{ID: event.ToolCallID, Result: cloneMap(event.Result), ScopeID: event.ScopeID, ParentScopeID: event.ParentScopeID, ParentToolCallID: event.ParentToolCallID}
	case "capability.invocation":
		decoded = adaptor.CapabilityInvocation{Invocation: decodeCapability(event.Capability)}
	case "todo.updated":
		decoded = adaptor.TodoUpdated{Snapshot: decodeTodo(event.Todo)}
	case "hitl.requested":
		decoded, err = decodeApprovalRequest(event)
	case "hitl.resolved":
		decoded = decodeApprovalResolved(event)
	case "stream.dropped":
		decoded = decodeDropped(event.Raw)
	default:
		return nil, true, fmt.Errorf("unsupported adapter event kind %q", event.Kind)
	}
	if err != nil {
		return nil, true, err
	}
	return adaptor.WithEventMeta(decoded, decodeEventMeta(event)), true, nil
}

func adapterPhase(kind string) adaptor.Phase {
	switch {
	case strings.HasSuffix(kind, ".start"):
		return adaptor.PhaseStart
	case strings.HasSuffix(kind, ".end"):
		return adaptor.PhaseEnd
	default:
		return adaptor.PhaseContent
	}
}

func decodeEventMeta(event AdapterStreamEventV1) adaptor.EventMeta {
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
	meta.Source = decodeSource(event.Meta.Source)

	return meta
}

func decodeApprovalRequest(event AdapterStreamEventV1) (adaptor.Event, error) {
	hitl := event.HITL
	request := &adaptor.ApprovalRequest{
		ID:     stringValue(hitl["request_id"]),
		Kind:   adaptor.ApprovalKind(stringValue(hitl["decision_kind"])),
		Source: stringValue(hitl["source"]), Attempt: intValue(hitl["retry_attempt"]),
		ToolCallID: event.ToolCallID,
	}
	if rawDeadline := stringValue(hitl["deadline"]); rawDeadline != "" {
		deadline, err := time.Parse(time.RFC3339, rawDeadline)
		if err != nil {
			return nil, fmt.Errorf("decode approval deadline: %w", err)
		}
		request.Deadline = deadline
	}
	if fields, ok := hitl["request"].(map[string]any); ok {
		request.Title = stringValue(fields["prompt"])
		if toolCallID := stringValue(fields["tool_call_id"]); toolCallID != "" {
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
					Key: stringValue(choice["key"]), Label: stringValue(choice["label"]),
					Description: stringValue(choice["description"]),
				})
			}
		}
	}
	return request, nil
}

func decodeApprovalResolved(event AdapterStreamEventV1) adaptor.Event {
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

func decodeDropped(raw map[string]any) adaptor.Event {
	dropped := adaptor.Dropped{
		Count: intValue(raw["dropped_count"]), FirstSequence: uint64Value(raw["first_sequence"]),
		LastSequence: uint64Value(raw["last_sequence"]), Reason: stringValue(raw["reason"]),
		Source: stringValue(raw["source"]),
	}
	if byKind, ok := raw["by_kind"].(map[string]any); ok {
		dropped.ByKind = make(map[string]int, len(byKind))
		for kind, count := range byKind {
			dropped.ByKind[kind] = intValue(count)
		}
	}
	if details, ok := raw["details"].(map[string]any); ok {
		dropped.Details = cloneMap(details)
	}
	if kind, ok := raw["event_kind"].(string); ok && (kind == "unknown" || supportedAdapterStreamKind(kind)) {
		if dropped.Details == nil {
			dropped.Details = map[string]any{}
		}
		dropped.Details["event_kind"] = kind
	}
	return dropped
}

func stringValue(value any) string {
	text, _ := value.(string)
	return text
}

func intValue(value any) int { return int(uint64Value(value)) }

func uint64Value(value any) uint64 {
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
		"hitl.requested", "hitl.resolved", "stream.dropped", "capability.invocation", "todo.updated":
		return true
	default:
		return false
	}
}
