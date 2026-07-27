package adaptor

import (
	"maps"
	"time"

	"github.com/agent-dance/agent-adaptor/driver"
)

// ============ The unified event stream (P1.1, design doc §2.5) ============
//
// One run produces one ordered stream of typed events. Semantic streaming
// (text / thinking / tool calls), operational signals (process spawn, raw
// chunks, lifecycle notices), approvals, and backpressure markers all travel
// the same channel; a single type switch dispatches them and unhandled types
// are ignored for free. The legacy dual-channel model (RunEvent operational +
// StreamPayload semantic, each with its own drain obligation) is gone.

// Event is the sealed interface implemented by every stream event type.
//
// The interface is sealed (unexported method): the set of event types is an
// SDK contract, exhaustively listed in this file. Consumers dispatch with a
// type switch:
//
//	for ev := range stream.Events() {
//	    switch e := ev.(type) {
//	    case adaptor.TextDelta:        io.WriteString(w, e.Text)
//	    case adaptor.ToolCall:         renderToolCard(e.Name, e.Args)
//	    case adaptor.Thinking:         renderReasoning(e.Text)
//	    case *adaptor.ApprovalRequest: _ = e.Approve(ctx)
//	    }
//	}
type Event interface {
	// isEvent seals the interface to this package.
	isEvent()
	// Meta returns the SDK-owned event envelope. Sequence and Time describe
	// the order in which the unified sink accepted events, not a provider's
	// protocol cursor. Provider values, when present, remain in Source.
	Meta() EventMeta
}

// EventMeta is the common, SDK-owned envelope carried by every Event.
// Sequence is strictly increasing for one run and is assigned while the
// event sink serializes producers. ThreadKey is the host's opaque key; a
// provider thread/session identifier, if any, is kept in Source.ThreadID.
type EventMeta struct {
	RunID     string
	ThreadKey string
	Sequence  uint64
	Time      time.Time
	TurnID    string
	Source    *EventSourceMeta
}

// EventSourceMeta preserves provider/driver envelope coordinates without
// allowing them to compete with the SDK's authoritative EventMeta fields.
type EventSourceMeta struct {
	RunID     string
	ThreadID  string
	TurnID    string
	Sequence  uint64
	Timestamp time.Time
}

type eventMetaCarrier struct{ meta EventMeta }

func (c eventMetaCarrier) Meta() EventMeta {
	out := c.meta
	if out.Source != nil {
		source := *out.Source
		out.Source = &source
	}
	return out
}

// Role identifies the speaker of a text event. It aliases the driver SPI
// type; the zero value is RoleAssistant.
type Role = driver.Role

const (
	// RoleAssistant is the default speaker for text events.
	RoleAssistant Role = driver.RoleAssistant
	// RoleUser marks a text lifecycle synthesized above the driver layer
	// (bridges replaying the human turn). Drivers never emit it.
	RoleUser Role = driver.RoleUser
)

// Phase marks lifecycle boundaries on the streaming event types (TextDelta,
// Thinking, ToolCall). The zero value is the content-bearing middle of the
// lifecycle, so consumers that only care about content can ignore the field:
// start/end boundary events simply carry empty content.
type Phase string

const (
	// PhaseContent is the content-bearing default phase.
	PhaseContent Phase = ""
	// PhaseStart opens a message / reasoning / tool-call lifecycle.
	PhaseStart Phase = "start"
	// PhaseEnd closes a message / reasoning / tool-call lifecycle.
	PhaseEnd Phase = "end"
)

// TextDelta is one assistant (or bridge-synthesized user) text event.
// Translated from StreamKinds text.start / text.content / text.end,
// discriminated by Phase; only PhaseContent events carry Text.
type TextDelta struct {
	eventMetaCarrier
	// MessageID groups the deltas of one message lifecycle.
	MessageID string
	// Text is the incremental content chunk (empty on start/end phases).
	Text string
	// Role is the speaker; zero value RoleAssistant.
	Role Role
	// Phase discriminates lifecycle boundary events from content.
	Phase Phase
}

// Thinking is one reasoning/thinking text event. Translated from
// reasoning.start / reasoning.content / reasoning.end, discriminated by
// Phase; only PhaseContent events carry Text.
type Thinking struct {
	eventMetaCarrier
	// MessageID groups the deltas of one reasoning lifecycle.
	MessageID string
	// Text is the incremental reasoning chunk (empty on start/end phases).
	Text string
	// Phase discriminates lifecycle boundary events from content.
	Phase Phase
}

// ToolCall is one tool-call lifecycle event. Translated from
// tool_call.start / tool_call.args / tool_call.end, discriminated by Phase:
//
//   - PhaseStart: Name (and Args, when the driver sends a complete initial
//     snapshot) identify the invocation — the natural "render the tool card"
//     event.
//   - PhaseContent: ArgsDelta carries one streamed argument fragment
//     (usually a JSON fragment) for drivers with argument streaming.
//   - PhaseEnd: closes the lifecycle; Result is populated when the driver
//     attaches it to the end marker.
type ToolCall struct {
	eventMetaCarrier
	// ID is the tool-call correlation identifier.
	ID string
	// Name is the tool name (PhaseStart).
	Name string
	// Args is the complete initial argument snapshot (PhaseStart, optional).
	Args map[string]any
	// ArgsDelta is one streamed argument fragment (PhaseContent).
	ArgsDelta string
	// Result is the optional result attached to the end marker (PhaseEnd).
	Result map[string]any
	// Phase discriminates start / argument-streaming / end.
	Phase Phase
}

// ToolResult carries a completed tool result (StreamKind tool_call.result).
type ToolResult struct {
	eventMetaCarrier
	// ID correlates with the originating ToolCall.ID.
	ID string
	// Result is the structured tool result.
	Result map[string]any
}

// RunStarted marks the beginning of a streamed run (StreamKind run.started).
type RunStarted struct {
	eventMetaCarrier
	RunID    string
	ThreadID string
}

// RunFinished marks the end of a streamed run. Translated from run.finished
// (Failed == false, Usage populated when the driver reports it) and
// run.error (Failed == true with the classified Reason / Message).
//
// RunFinished is informational: the authoritative outcome — including the
// full Result and the typed *RunError — always comes from Stream.Result().
type RunFinished struct {
	eventMetaCarrier
	RunID    string
	ThreadID string
	// Usage is the token accounting reported on normal completion.
	Usage *Usage
	// Failed reports that this is a run.error terminal marker.
	Failed bool
	// Reason classifies the failure (Failed == true).
	Reason FailureReason
	// Message is the driver's failure message (Failed == true).
	Message string
}

// ProcessInfo kinds.
const (
	// ProcessSpawn reports child-process launch details.
	ProcessSpawn = "spawn"
	// ProcessStdout carries a raw stdout chunk.
	ProcessStdout = "stdout"
	// ProcessStderr carries a raw stderr chunk.
	ProcessStderr = "stderr"
)

// ProcessInfo carries process-level operational signals: child-process
// spawn details and raw stdout/stderr chunks (legacy RunEvent types spawn
// and chunk). Most consumers ignore it; debugging and audit tooling reads
// it from the same stream instead of a second channel.
type ProcessInfo struct {
	eventMetaCarrier
	// Kind is ProcessSpawn, ProcessStdout, or ProcessStderr.
	Kind string
	// Text is the human-readable description (spawn).
	Text string
	// Bytes is the raw chunk (stdout/stderr); it may not align to lines.
	Bytes []byte
	// Metadata carries short string tags from the driver.
	Metadata map[string]string
	// Data carries structured driver-specific extensions.
	Data map[string]any
}

// Notice kinds.
const (
	// NoticeInvocation describes the resolved invocation metadata.
	NoticeInvocation = "invocation"
	// NoticeLifecycle reports high-level run lifecycle markers and
	// SDK warnings (for example the approval-retry degradation warning,
	// Data["warning"] == "human_decision_retry_unsupported").
	NoticeLifecycle = "lifecycle"
	// NoticeRuntime reports runtime-service preparation or cleanup.
	NoticeRuntime = "runtime"
	// NoticeStep marks a provider-defined work step boundary
	// (StreamKinds step.started / step.finished); Data["phase"] is
	// "started" or "finished" and Text carries the step name.
	NoticeStep = "step"
	// NoticeTranscriptItem carries one progressively parsed transcript
	// item (legacy RunEventItem). Item holds the normalized entry; the
	// complete ordered transcript remains available via Result.Transcript().
	NoticeTranscriptItem = "transcript.item"
	// NoticeApprovalRequested broadcasts that an approval request was
	// routed to a callback handler or auto-resolved by policy (in event
	// form the *ApprovalRequest event itself is the request signal).
	NoticeApprovalRequested = "approval.requested"
	// NoticeApprovalResolved broadcasts the final outcome of an approval
	// request (Data: request_id / kind / result / choice / attempt).
	NoticeApprovalResolved = "approval.resolved"
)

// Notice is a low-frequency operational event: invocation metadata,
// lifecycle markers, runtime-service reports, progressive transcript items,
// and approval lifecycle broadcasts. Consumers that do not care simply omit
// the case.
type Notice struct {
	eventMetaCarrier
	// Kind is one of the Notice* constants (unknown driver-specific kinds
	// pass through verbatim so no information is lost).
	Kind string
	// Text is the human-readable message.
	Text string
	// Item is the transcript entry (NoticeTranscriptItem only).
	Item *TranscriptItem
	// Metadata carries short string tags from the driver.
	Metadata map[string]string
	// Data carries structured details.
	Data map[string]any
}

// Dropped is the aggregated backpressure marker: under the default drop
// strategy, events discarded because the consumer was slow are counted and
// surfaced as one Dropped event as soon as the channel has room again.
// See WithEventBuffer / WithBlockingEvents.
type Dropped struct {
	eventMetaCarrier
	// Count is how many events were dropped since the previous marker.
	Count int
	// ByKind breaks Count down by the public event kind.
	ByKind map[string]int
	// FirstSequence and LastSequence bound the SDK sequence numbers which
	// were reserved for, but not delivered with, the discarded events.
	FirstSequence uint64
	LastSequence  uint64
	// Reason and Source distinguish local backpressure from provider-side
	// loss reports. Details preserves additional audit fields.
	Reason  string
	Source  string
	Details map[string]any
}

// SubagentEventKind classifies a SubagentUpdate.
type SubagentEventKind string

const (
	// SubagentStarted marks a delegated subagent beginning work.
	SubagentStarted SubagentEventKind = "started"
	// SubagentDelta carries incremental subagent output.
	SubagentDelta SubagentEventKind = "delta"
	// SubagentFinished marks a delegated subagent completing.
	SubagentFinished SubagentEventKind = "finished"
)

// SubagentUpdate reports remote progress of a delegated subagent on the
// leader's own event stream (design doc §9). The type is part of the P1
// event vocabulary; the delegation service starts injecting it in P4.
type SubagentUpdate struct {
	eventMetaCarrier
	// Agent is the delegation key of the subagent.
	Agent string
	// Kind classifies the update.
	Kind SubagentEventKind
	// Delta is the incremental output chunk (SubagentDelta).
	Delta string
	// Data carries structured details.
	Data map[string]any
}

// isEvent implementations seal the Event interface.
func (TextDelta) isEvent()        {}
func (Thinking) isEvent()         {}
func (ToolCall) isEvent()         {}
func (ToolResult) isEvent()       {}
func (RunStarted) isEvent()       {}
func (RunFinished) isEvent()      {}
func (ProcessInfo) isEvent()      {}
func (Notice) isEvent()           {}
func (Dropped) isEvent()          {}
func (SubagentUpdate) isEvent()   {}
func (*ApprovalRequest) isEvent() {}

// WithEventMeta returns ev with meta restored on a value copy. It is the
// narrow replay hook for bridges and persistent event recorders, whose wire
// envelope stores EventMeta separately from the typed event payload. A live
// run's sink always overwrites restored coordinates with its own authoritative
// run order before publication.
func WithEventMeta(ev Event, meta EventMeta) Event {
	if ev == nil {
		return nil
	}
	meta.Source = cloneEventSourceMeta(meta.Source)
	return stampEvent(ev, meta)
}

// stampEvent returns an event copy carrying the authoritative SDK envelope.
// ApprovalRequest copies retain the shared exactly-once responder pointer.
func stampEvent(ev Event, meta EventMeta) Event {
	c := eventMetaCarrier{meta: meta}
	switch e := ev.(type) {
	case TextDelta:
		e.eventMetaCarrier = c
		return e
	case Thinking:
		e.eventMetaCarrier = c
		return e
	case ToolCall:
		e.eventMetaCarrier = c
		return e
	case ToolResult:
		e.eventMetaCarrier = c
		return e
	case RunStarted:
		e.eventMetaCarrier = c
		return e
	case RunFinished:
		e.eventMetaCarrier = c
		return e
	case ProcessInfo:
		e.eventMetaCarrier = c
		return e
	case Notice:
		e.eventMetaCarrier = c
		return e
	case Dropped:
		e.eventMetaCarrier = c
		e.ByKind = maps.Clone(e.ByKind)
		e.Details = maps.Clone(e.Details)
		return e
	case SubagentUpdate:
		e.eventMetaCarrier = c
		return e
	case *ApprovalRequest:
		if e != nil {
			out := *e
			out.eventMetaCarrier = c
			return &out
		}
		return e
	default:
		return ev
	}
}

func eventKind(ev Event) string {
	switch e := ev.(type) {
	case TextDelta:
		return "text." + phaseKind(e.Phase)
	case Thinking:
		return "thinking." + phaseKind(e.Phase)
	case ToolCall:
		return "tool_call." + phaseKind(e.Phase)
	case ToolResult:
		return "tool_result"
	case RunStarted:
		return "run.started"
	case RunFinished:
		return "run.finished"
	case ProcessInfo:
		return "process." + e.Kind
	case Notice:
		return "notice." + e.Kind
	case Dropped:
		return "dropped"
	case SubagentUpdate:
		return "subagent." + string(e.Kind)
	case *ApprovalRequest:
		return "approval.requested"
	default:
		return "unknown"
	}
}

func phaseKind(p Phase) string {
	if p == PhaseContent {
		return "content"
	}
	return string(p)
}

// eventMayDrop is deliberately narrow. Only replayable, high-frequency
// deltas may be discarded. Lifecycle, approvals, terminal events, provider
// Dropped reports, transcript items, and tool results are always reliable
// until the run is explicitly cancelled.
func eventMayDrop(ev Event) bool {
	switch e := ev.(type) {
	case TextDelta:
		return e.Phase == PhaseContent
	case Thinking:
		return e.Phase == PhaseContent
	case ToolCall:
		return e.Phase == PhaseContent
	case ProcessInfo:
		return e.Kind == ProcessStdout || e.Kind == ProcessStderr
	case SubagentUpdate:
		return e.Kind == SubagentDelta
	default:
		return false
	}
}

// ============ SPI → Event translation (P1.2) ============

// eventFromRunEvent translates one operational RunEvent. Every RunEventType
// has a destination:
//
//	chunk              → ProcessInfo (Kind stdout/stderr)
//	spawn              → ProcessInfo (Kind spawn)
//	item               → Notice (NoticeTranscriptItem, Item attached)
//	invocation         → Notice (NoticeInvocation)
//	runtime            → Notice (NoticeRuntime)
//	lifecycle          → Notice (NoticeLifecycle)
//	unknown extensions → Notice (Kind passes through verbatim)
func eventFromRunEvent(ev driver.RunEvent) Event {
	switch ev.Type {
	case driver.RunEventChunk:
		kind := ProcessStdout
		if ev.Stream == "stderr" {
			kind = ProcessStderr
		}
		return ProcessInfo{Kind: kind, Bytes: ev.Bytes, Metadata: ev.Metadata, Data: ev.Data}
	case driver.RunEventSpawn:
		return ProcessInfo{Kind: ProcessSpawn, Text: ev.Text, Metadata: ev.Metadata, Data: ev.Data}
	case driver.RunEventItem:
		var text string
		if ev.Item != nil {
			text = ev.Item.Text
		}
		return Notice{Kind: NoticeTranscriptItem, Text: text, Item: ev.Item, Metadata: ev.Metadata, Data: ev.Data}
	case driver.RunEventInvocation:
		return Notice{Kind: NoticeInvocation, Text: ev.Text, Metadata: ev.Metadata, Data: ev.Data}
	case driver.RunEventRuntime:
		return Notice{Kind: NoticeRuntime, Text: ev.Text, Metadata: ev.Metadata, Data: ev.Data}
	case driver.RunEventLifecycle:
		return Notice{Kind: NoticeLifecycle, Text: ev.Text, Metadata: ev.Metadata, Data: ev.Data}
	default:
		return Notice{Kind: string(ev.Type), Text: ev.Text, Metadata: ev.Metadata, Data: ev.Data}
	}
}

// eventFromStreamPayload translates one semantic StreamPayload. All 18
// StreamKinds have a destination (see the mapping table in the P1 report /
// contract test); several kinds share one event type discriminated by Phase
// or by field population.
func eventFromStreamPayload(p driver.StreamPayload) Event {
	switch p.Kind {
	case driver.StreamRunStarted:
		return RunStarted{RunID: p.RunID, ThreadID: p.ThreadID}
	case driver.StreamRunFinished:
		return RunFinished{RunID: p.RunID, ThreadID: p.ThreadID, Usage: p.Usage}
	case driver.StreamRunError:
		ev := RunFinished{RunID: p.RunID, ThreadID: p.ThreadID, Failed: true, Reason: ReasonAgentError}
		if p.Error != nil {
			ev.Reason = failureReason(p.Error.Code)
			ev.Message = p.Error.Message
		}
		return ev

	case driver.StreamStepStarted:
		return Notice{Kind: NoticeStep, Text: p.Name, Data: map[string]any{"phase": "started"}}
	case driver.StreamStepFinished:
		return Notice{Kind: NoticeStep, Text: p.Name, Data: map[string]any{"phase": "finished"}}

	case driver.StreamTextStart:
		return TextDelta{MessageID: p.MessageID, Role: p.Role, Phase: PhaseStart}
	case driver.StreamTextContent:
		return TextDelta{MessageID: p.MessageID, Role: p.Role, Text: p.Delta}
	case driver.StreamTextEnd:
		return TextDelta{MessageID: p.MessageID, Role: p.Role, Phase: PhaseEnd}

	case driver.StreamToolCallStart:
		return ToolCall{ID: p.ToolCallID, Name: p.Name, Args: p.Args, Phase: PhaseStart}
	case driver.StreamToolCallArgs:
		return ToolCall{ID: p.ToolCallID, ArgsDelta: p.Delta}
	case driver.StreamToolCallEnd:
		return ToolCall{ID: p.ToolCallID, Result: p.Result, Phase: PhaseEnd}
	case driver.StreamToolCallResult:
		return ToolResult{ID: p.ToolCallID, Result: p.Result}

	case driver.StreamReasoningStart:
		return Thinking{MessageID: p.MessageID, Phase: PhaseStart}
	case driver.StreamReasoningContent:
		return Thinking{MessageID: p.MessageID, Text: p.Delta}
	case driver.StreamReasoningEnd:
		return Thinking{MessageID: p.MessageID, Phase: PhaseEnd}

	case driver.StreamHITLRequested:
		// A driver-emitted broadcast has no responder attached — the
		// answerable *ApprovalRequest is produced exclusively by the
		// sink's RequestDecision path — so it translates to a Notice.
		n := Notice{Kind: NoticeApprovalRequested, Data: map[string]any{}}
		if r := p.HITLRequested; r != nil {
			n.Text = r.Prompt
			n.Data["request_id"] = r.RequestID
			n.Data["kind"] = string(r.Kind)
			n.Data["source"] = r.Source
			n.Data["tool_call_id"] = r.ToolCallID
			n.Data["payload"] = maps.Clone(r.Payload)
			n.Data["choices"] = append([]driver.DecisionChoice(nil), r.Choices...)
			n.Data["created_at"] = r.CreatedAt
			n.Data["deadline"] = r.Deadline
			n.Data["attempt"] = r.RetryAttempt
		}
		return n
	case driver.StreamHITLResolved:
		n := Notice{Kind: NoticeApprovalResolved, Data: map[string]any{}}
		if r := p.HITLResolved; r != nil {
			n.Data["request_id"] = r.RequestID
			n.Data["kind"] = string(r.Kind)
			n.Data["source"] = r.Source
			n.Data["result"] = string(r.Result)
			n.Data["choice"] = r.Choice
			n.Data["answer"] = maps.Clone(r.Answer)
			n.Data["attempt"] = r.RetryAttempt
			n.Data["resolved_at"] = r.ResolvedAt
			n.Data["latency"] = r.Latency
		}
		return n

	case driver.StreamDropped:
		return droppedFromProvider(p.Raw)

	default:
		// Unknown driver extension kinds pass through as notices so no
		// information is silently lost.
		data := maps.Clone(p.Raw)
		if data == nil {
			data = make(map[string]any)
		}
		if p.Name != "" {
			data["name"] = p.Name
		}
		return Notice{Kind: string(p.Kind), Text: p.Delta, Data: data}
	}
}

func droppedFromProvider(raw map[string]any) Dropped {
	d := Dropped{
		Count:   droppedCount(raw),
		Reason:  stringValue(raw["reason"]),
		Source:  stringValue(raw["source"]),
		Details: maps.Clone(raw),
		ByKind:  map[string]int{},
	}
	if d.Source == "" {
		d.Source = "provider"
	}
	if byKind, ok := raw["by_kind"].(map[string]int); ok {
		d.ByKind = maps.Clone(byKind)
	} else if byKind, ok := raw["by_kind"].(map[string]any); ok {
		for kind, value := range byKind {
			d.ByKind[kind] = numberValue(value)
		}
	}
	d.FirstSequence = uint64(numberValue(raw["first_sequence"]))
	d.LastSequence = uint64(numberValue(raw["last_sequence"]))
	return d
}

func stringValue(v any) string {
	s, _ := v.(string)
	return s
}

func numberValue(v any) int {
	switch n := v.(type) {
	case int:
		return n
	case int32:
		return int(n)
	case int64:
		return int(n)
	case uint:
		return int(n)
	case uint32:
		return int(n)
	case uint64:
		return int(n)
	case float64:
		return int(n)
	default:
		return 0
	}
}

// droppedCount extracts Raw["dropped_count"] from a stream.dropped payload.
func droppedCount(raw map[string]any) int {
	return numberValue(raw["dropped_count"])
}
