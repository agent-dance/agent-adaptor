package driver

import "time"

// RunEventType describes the category of a streamed RunEvent.
//
// There are two primary signals:
//   - RunEventChunk: raw stdout/stderr bytes. Chunks may not align to lines.
//   - RunEventItem: structured transcript entry emitted by the driver after
//     parsing its own protocol.
//
// The remaining types carry operational or lifecycle metadata.
type RunEventType string

const (
	// RunEventChunk carries raw stdout/stderr bytes.
	RunEventChunk RunEventType = "chunk"
	// RunEventItem carries a parsed TranscriptItem.
	RunEventItem RunEventType = "item"
	// RunEventInvocation describes the resolved invocation metadata.
	RunEventInvocation RunEventType = "invocation"
	// RunEventSpawn reports child-process launch details.
	RunEventSpawn RunEventType = "spawn"
	// RunEventRuntime reports runtime-service preparation or cleanup.
	RunEventRuntime RunEventType = "runtime"
	// RunEventLifecycle reports high-level run lifecycle markers.
	RunEventLifecycle RunEventType = "lifecycle"
)

// RunEvent is the streamed event envelope exposed through the host-facing
// event channel.
//
// Field usage by Type:
//   - chunk: Stream ("stdout"|"stderr"), Bytes (raw chunk bytes, may be partial).
//   - item:  Item (*TranscriptItem).
//   - invocation/spawn/runtime/lifecycle: Text, Metadata, Data.
//
// Seq is assigned monotonically by the SDK per-run. Hosts that collect
// RunEventItem events in Seq order will observe the exact same sequence that
// the final result's Transcript reflects.
type RunEvent struct {
	Type      RunEventType
	Seq       uint64
	Timestamp time.Time

	Stream string
	Bytes  []byte

	Item *TranscriptItem

	Text     string
	Metadata map[string]string
	Data     map[string]any
}

// TranscriptKind identifies the semantic category of a transcript item.
type TranscriptKind string

const (
	// TranscriptAssistant is assistant-facing text.
	TranscriptAssistant TranscriptKind = "assistant"
	// TranscriptThinking is reasoning/thinking text.
	TranscriptThinking TranscriptKind = "thinking"
	// TranscriptUser is user text captured from the provider transcript.
	TranscriptUser TranscriptKind = "user"
	// TranscriptToolCall is a normalized tool invocation.
	TranscriptToolCall TranscriptKind = "tool_call"
	// TranscriptToolResult is a normalized tool result.
	TranscriptToolResult TranscriptKind = "tool_result"
	// TranscriptInit records provider/model/session initialization metadata.
	TranscriptInit TranscriptKind = "init"
	// TranscriptResult records a terminal provider result event.
	TranscriptResult TranscriptKind = "result"
	// TranscriptStdout is parser fallback text from stdout.
	TranscriptStdout TranscriptKind = "stdout"
	// TranscriptStderr is parser fallback text from stderr.
	TranscriptStderr TranscriptKind = "stderr"
	// TranscriptSystem is driver/system text.
	TranscriptSystem TranscriptKind = "system"
	// TranscriptSummary is a short terminal summary item.
	TranscriptSummary TranscriptKind = "summary"
	// TranscriptQuestion is a provider follow-up question.
	TranscriptQuestion TranscriptKind = "question"
	// TranscriptFailure is a structured failure item.
	TranscriptFailure TranscriptKind = "failure"
)

// TranscriptItem is the host-facing normalized transcript unit.
//
// Kind field rules (see docs/workstream-output-transcript-impl-spec.md §1.5):
//   - assistant / thinking / user: Text required. Delta allowed for assistant
//     and thinking only.
//   - tool_call: ToolName required, ToolUseID recommended, Input optional.
//   - tool_result: ToolUseID required, Text recommended, IsError optional.
//   - init: Model and SessionID recommended.
//   - result: Text/Usage/CostUSD/Subtype/IsError/Errors optional.
//   - stdout/stderr/system: Text required (parser fallback).
//   - summary/question/failure: Text required; Data["choices"] for question,
//     Metadata["code"] for failure.
//
// Metadata carries short string tags; Data carries provider-specific
// structured extensions. A field already captured by a struct member must not
// be duplicated into Metadata or Data.
type TranscriptItem struct {
	Kind      TranscriptKind
	Text      string
	Delta     bool
	ToolUseID string
	ToolName  string
	Input     any
	IsError   bool
	Model     string
	SessionID string
	Usage     *Usage
	CostUSD   *float64
	Subtype   string
	Errors    []string
	Metadata  map[string]string
	Data      map[string]any
}

// StreamKind enumerates the protocol-agnostic streaming events drivers emit
// via EventSink.EmitStream when streaming is enabled. Bridges (pkg/bridges/*)
// translate these into host-facing protocols such as AG-UI without having to
// know the concrete driver.
//
// The full list is the union of what codex / claude / cursor can expose.
// Drivers may emit a subset; bridges that require a specific kind should
// degrade gracefully when it is absent.
type StreamKind string

const (
	// StreamRunStarted marks the beginning of a streamed run.
	StreamRunStarted StreamKind = "run.started"
	// StreamRunFinished marks normal completion of a streamed run.
	StreamRunFinished StreamKind = "run.finished"
	// StreamRunError marks terminal failure of a streamed run.
	StreamRunError StreamKind = "run.error"
	// StreamStepStarted marks a provider-defined work step beginning.
	StreamStepStarted StreamKind = "step.started"
	// StreamStepFinished marks a provider-defined work step ending.
	StreamStepFinished StreamKind = "step.finished"

	// StreamTextStart opens an assistant text message lifecycle.
	StreamTextStart StreamKind = "text.start"
	// StreamTextContent carries one assistant text delta.
	StreamTextContent StreamKind = "text.content"
	// StreamTextEnd closes an assistant text message lifecycle.
	StreamTextEnd StreamKind = "text.end"

	// StreamToolCallStart opens a tool-call lifecycle.
	StreamToolCallStart StreamKind = "tool_call.start"
	// StreamToolCallArgs carries streamed tool arguments or command output.
	StreamToolCallArgs StreamKind = "tool_call.args"
	// StreamToolCallEnd closes a tool-call lifecycle.
	StreamToolCallEnd StreamKind = "tool_call.end"
	// StreamToolCallResult carries a completed tool result.
	StreamToolCallResult StreamKind = "tool_call.result"

	// StreamReasoningStart opens a reasoning/thinking lifecycle.
	StreamReasoningStart StreamKind = "reasoning.start"
	// StreamReasoningContent carries one reasoning/thinking delta.
	StreamReasoningContent StreamKind = "reasoning.content"
	// StreamReasoningEnd closes a reasoning/thinking lifecycle.
	StreamReasoningEnd StreamKind = "reasoning.end"

	// StreamHITLRequested broadcasts a human-decision request.
	StreamHITLRequested StreamKind = "hitl.requested"
	// StreamHITLResolved broadcasts the final human-decision result.
	StreamHITLResolved StreamKind = "hitl.resolved"

	// StreamDropped reports StreamPayloads dropped because the host was slow.
	// Raw["dropped_count"] reports how many.
	StreamDropped StreamKind = "stream.dropped"
)

// Role identifies the speaker for text-bearing StreamPayloads.
//
// The zero value is RoleAssistant: every driver today emits text.start /
// text.content / text.end as assistant output, so leaving Role unset
// preserves the historical wire shape exactly.
//
// Role only carries semantics on text-lifecycle kinds (text.start /
// text.content / text.end). On every other Kind it MUST be left at the
// zero value; bridges treat non-zero Role on non-text kinds as a
// programming error and may ignore it.
//
// RoleUser is exclusively produced by bridges or hosts that want the
// human turn to appear in the recorded / replayed StreamPayload stream
// (see pkg/bridges/agui.RunAgentInput.UserTurnPayloads). Drivers MUST
// NOT emit RoleUser themselves.
type Role string

const (
	// RoleAssistant is the default; emitted by every driver today.
	RoleAssistant Role = ""
	// RoleUser marks a text lifecycle synthesized above the driver
	// layer to represent the human turn that triggered the run.
	RoleUser Role = "user"
)

// StreamPayload is a single structured streaming event emitted by a
// stream-aware driver. It is intentionally a superset capable of carrying
// text deltas, tool-call lifecycles, reasoning, lifecycle markers, and opaque
// provider payloads.
//
// Field usage by Kind:
//   - run.started / run.finished / run.error: RunID, ThreadID, Usage (on
//     finished), Error (on error). MessageID / ToolCallID empty.
//   - step.started / step.finished: Name required.
//   - text.start / text.end: MessageID required. Role optional; zero value
//     is treated as RoleAssistant. RoleUser MUST be paired with a
//     non-empty MessageID that stays stable across the start/content/end
//     triple of a single user turn.
//   - text.content: MessageID required; Delta non-empty. Role optional
//     (see text.start).
//   - tool_call.start: ToolCallID and Name required; Args optional
//     (complete initial snapshot when the driver does not stream args).
//   - tool_call.args: ToolCallID required; Delta non-empty (incremental
//     argument chunk, usually JSON fragment).
//   - tool_call.end / tool_call.result: ToolCallID required; Result optional.
//   - reasoning.*: MessageID required; Delta for reasoning.content.
//   - hitl.requested / hitl.resolved: HITLRequested / HITLResolved carry the
//     normalized decision envelope; Raw may carry driver-specific payload.
//   - stream.dropped: Raw["dropped_count"] reports the count.
//
// Sequence is assigned monotonically by the SDK in EmitStream; drivers must
// not set it themselves. Timestamp is similarly backfilled when zero.
//
// Seq mirrors Sequence as the canonical per-run monotonic cursor exposed by
// the streaming/HITL contract (see docs/run-policy.md §6). It is always
// equal to Sequence; Sequence is retained as the legacy field so downstream
// bridges and tests that already referenced it keep compiling.
type StreamPayload struct {
	Kind       StreamKind
	Sequence   uint64
	Seq        uint64
	RunID      string
	ThreadID   string
	TurnID     string
	MessageID  string
	ToolCallID string
	Name       string
	Delta      string
	Args       map[string]any
	Result     map[string]any
	Usage      *Usage
	Error      *RunFailure
	Timestamp  time.Time

	// HITLRequested is populated when Kind == StreamHITLRequested.
	HITLRequested *HITLRequestedPayload
	// HITLResolved is populated when Kind == StreamHITLResolved.
	HITLResolved *HITLResolvedPayload

	// Role identifies the speaker for text.* kinds. Zero value =
	// RoleAssistant for backward compatibility; see Role docs. Drivers
	// MUST leave Role at zero on every Kind they emit.
	Role Role

	// Raw carries provider-specific structured data that does not fit the
	// normalized fields. Bridges may pass it through opaquely.
	Raw map[string]any
}
