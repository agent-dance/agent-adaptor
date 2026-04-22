package agentadaptor

import (
	"context"
	"time"
)

type AgentIdentity struct {
	ID        string
	TenantID  string
	ProfileID string
	Name      string
}

// RawStreams captures the complete raw stdout and stderr emitted during one
// run. It is the stable surface hosts should rely on for auditing, replay,
// debugging, or archival.
//
// Contract:
//   - Stdout / Stderr hold the full untruncated bytes the child process wrote.
//   - No redaction, no semantic parsing, no line-wise transformation is applied.
//   - Both Run() and Start().Wait() must return the same RawStreams payload.
type RawStreams struct {
	Stdout string
	Stderr string
}

// RunResult is the normalized outcome returned by Runner.Run or RunHandle.Wait.
//
// Output layering rules (see docs/workstream-transcript-contract.md):
//   - Output: final assistant-facing text only; empty is valid when the
//     adapter never produced assistant text.
//   - RawStreams: raw stdout/stderr for auditing and debugging.
//   - Transcript: structured semantic entries produced by the adapter from its
//     protocol events. Must equal the sequence of RunEventItem items collected
//     by Seq order.
//   - Summary: short host-facing label; never equals Output and must come from
//     a terminal result event (or adapter-generated short text).
//   - Result: raw JSON payload of the terminal result event when the adapter
//     protocol defines one. May be nil.
type RunResult struct {
	RunID           string
	DriverType      string
	Output          string
	RawStreams      *RawStreams
	Transcript      []TranscriptItem
	ExitCode        int
	Signal          string
	TimedOut        bool
	Usage           *Usage
	Session         *SessionRef
	Metadata        map[string]string
	Provider        string
	Biller          string
	Model           string
	BillingType     string
	CostUSD         *float64
	Summary         string
	Result          map[string]any
	RuntimeServices []RuntimeServiceReport
	Question        *RunQuestion
	Failure         *RunFailure
}

type Usage struct {
	InputTokens        int
	OutputTokens       int
	CachedInputTokens  int
	EstimatedCostMilli int64
}

// RunEventType describes the category of a streamed RunEvent.
//
// There are two primary signals:
//   - RunEventChunk: raw stdout/stderr bytes. Chunks may not align to lines.
//   - RunEventItem: structured transcript entry emitted by the adapter after
//     parsing its own protocol.
//
// The remaining types carry operational or lifecycle metadata.
type RunEventType string

const (
	RunEventChunk      RunEventType = "chunk"
	RunEventItem       RunEventType = "item"
	RunEventInvocation RunEventType = "invocation"
	RunEventSpawn      RunEventType = "spawn"
	RunEventRuntime    RunEventType = "runtime"
	RunEventLifecycle  RunEventType = "lifecycle"
)

// RunEvent is the streamed event envelope exposed through RunHandle.Events().
//
// Field usage by Type:
//   - chunk: Stream ("stdout"|"stderr"), Bytes (raw chunk bytes, may be partial).
//   - item:  Item (*TranscriptItem).
//   - invocation/spawn/runtime/lifecycle: Text, Metadata, Data.
//
// Seq is assigned monotonically by the SDK per-run. Hosts that collect
// RunEventItem events in Seq order will observe the exact same sequence that
// RunResult.Transcript reflects.
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
	TranscriptAssistant  TranscriptKind = "assistant"
	TranscriptThinking   TranscriptKind = "thinking"
	TranscriptUser       TranscriptKind = "user"
	TranscriptToolCall   TranscriptKind = "tool_call"
	TranscriptToolResult TranscriptKind = "tool_result"
	TranscriptInit       TranscriptKind = "init"
	TranscriptResult     TranscriptKind = "result"
	TranscriptStdout     TranscriptKind = "stdout"
	TranscriptStderr     TranscriptKind = "stderr"
	TranscriptSystem     TranscriptKind = "system"
	TranscriptSummary    TranscriptKind = "summary"
	TranscriptQuestion   TranscriptKind = "question"
	TranscriptFailure    TranscriptKind = "failure"
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

type DriverRunRequest struct {
	RunID        string
	Prompt       string
	Config       any
	Agent        AgentIdentity
	Workspace    WorkspaceLease
	Runtime      RuntimePayload
	Skills       SkillPayload
	Policy       RunPolicy
	Instructions *InstructionsBundleRef
	Session      *DriverSessionContext
	Metadata     map[string]string

	// Streaming is a hint from the host that the caller wants structured
	// stream events (StreamPayload) emitted via EventSink.EmitStream in
	// addition to the regular RunEvent channel. Adapters that implement
	// StreamAwareDriver should switch their underlying transport to the
	// richest token-level channel available (for codex this means
	// `codex app-server`; for claude this means `--include-partial-messages`;
	// for cursor this means `--stream-partial-output`).
	//
	// Adapters that do not implement StreamAwareDriver are free to ignore
	// this field. Hosts consuming StreamEvents() on such adapters will see
	// an immediately-closed channel.
	Streaming bool
}

// DriverRunResult is the adapter-facing execution result.
//
// Built-in adapters must fill Output / RawStreams / Transcript / Summary /
// Result / Checkpoint from the same pass that parses the CLI protocol; none of
// these fields may be recomputed by downstream helpers.
type DriverRunResult struct {
	Output          string
	RawStreams      *RawStreams
	Transcript      []TranscriptItem
	ExitCode        int
	Signal          string
	TimedOut        bool
	Usage           *Usage
	Checkpoint      *DriverCheckpoint
	Metadata        map[string]string
	Provider        string
	Biller          string
	Model           string
	BillingType     string
	CostUSD         *float64
	Summary         string
	Result          map[string]any
	RuntimeServices []RuntimeServiceReport
	Question        *RunQuestion
	Failure         *RunFailure
}

type DriverSessionContext struct {
	EngineSessionID string
	Mode            SessionMode
	State           *DriverSessionState
	PreviousID      string
}

type DriverSessionState struct {
	ResumeID  string
	DisplayID string
	// Data stores adapter-specific session parameters such as cwd,
	// prompt-bundle fingerprints, or repo identifiers needed to validate a
	// resume attempt.
	Data map[string]string
}

type DriverCheckpoint struct {
	State *DriverSessionState
	Valid bool
}

// RunQuestion represents an adapter-requested follow-up question that the host
// can present to the operator before launching another run.
type RunQuestion struct {
	Prompt  string
	Choices []RunChoice
}

// RunChoice is one selectable answer for a RunQuestion.
type RunChoice struct {
	Key         string
	Label       string
	Description string
}

// RunFailure carries structured error information when the SDK or an
// adapter classifies a failure more precisely than a plain stderr string.
//
// HumanDecision is non-nil exactly when Code is FailureReject or
// FailureTimeout; hosts can rely on that invariant when rendering attribution
// (see docs/workstream-hitl-v2.md §3.2).
type RunFailure struct {
	Message       string
	Code          FailureCode
	Metadata      map[string]any
	HumanDecision *HumanDecisionFailure
}

// IsHumanDecision reports whether the failure originated from a HITL
// decision (rejected or timed out). nil-safe.
func (f *RunFailure) IsHumanDecision() bool {
	return f != nil && f.HumanDecision != nil
}

// IsRejected reports whether the failure is a user-visible rejection
// (includes AutoReject synthesis). nil-safe.
func (f *RunFailure) IsRejected() bool {
	return f != nil && f.Code == FailureReject
}

// IsTimedOut reports whether the failure is a HITL decision timeout
// (OnTimeout=FailureAbort path). Distinct from context.DeadlineExceeded on
// the outer ctx, which is surfaced as the Wait() error instead. nil-safe.
func (f *RunFailure) IsTimedOut() bool {
	return f != nil && f.Code == FailureTimeout
}

type WorkspaceManager interface {
	Resolve(ctx context.Context, req WorkspaceRequest) (WorkspaceLease, error)
	Release(ctx context.Context, lease WorkspaceLease, mode WorkspaceReleaseMode) error
}

type SkillCatalog interface {
	Resolve(ctx context.Context, tenantID string, refs []string) ([]Skill, error)
}

type SkillCatalogInventory interface {
	List(ctx context.Context, tenantID string) ([]Skill, error)
}

type SkillAssembler interface {
	Prepare(ctx context.Context, req SkillAssemblyRequest) (SkillPayload, error)
}

type RuntimeServiceManager interface {
	Ensure(ctx context.Context, req RuntimeServiceRequest) ([]RuntimeServiceRef, error)
	ReleaseByRun(ctx context.Context, runID string) error
}

type resolvedInvocation struct {
	runID        string
	prompt       string
	adapter      DriverAdapter
	config       any
	agent        AgentIdentity
	workspace    WorkspaceLease
	runtime      RuntimePayload
	skills       SkillPayload
	policy       RunPolicy
	handlers     decisionHandlers
	instructions *InstructionsBundleRef
	session      SessionRequest
	metadata     map[string]string
	fingerprint  string
	streaming    bool
}

// StreamKind enumerates the protocol-agnostic streaming events adapters emit
// via EventSink.EmitStream when streaming is enabled. Bridges (pkg/bridges/*)
// translate these into host-facing protocols such as AG-UI without having to
// know the concrete adapter.
//
// The full list is the union of what codex / claude / cursor can expose.
// Adapters may emit a subset; bridges that require a specific kind should
// degrade gracefully when it is absent.
type StreamKind string

const (
	// Lifecycle.
	StreamRunStarted   StreamKind = "run.started"
	StreamRunFinished  StreamKind = "run.finished"
	StreamRunError     StreamKind = "run.error"
	StreamStepStarted  StreamKind = "step.started"
	StreamStepFinished StreamKind = "step.finished"

	// Text message.
	StreamTextStart   StreamKind = "text.start"
	StreamTextContent StreamKind = "text.content"
	StreamTextEnd     StreamKind = "text.end"

	// Tool call.
	StreamToolCallStart  StreamKind = "tool_call.start"
	StreamToolCallArgs   StreamKind = "tool_call.args"
	StreamToolCallEnd    StreamKind = "tool_call.end"
	StreamToolCallResult StreamKind = "tool_call.result"

	// Reasoning / thinking.
	StreamReasoningStart   StreamKind = "reasoning.start"
	StreamReasoningContent StreamKind = "reasoning.content"
	StreamReasoningEnd     StreamKind = "reasoning.end"

	// HITL (audit-only in v1; no response channel is wired yet).
	StreamHITLRequested StreamKind = "hitl.requested"
	StreamHITLResolved  StreamKind = "hitl.resolved"

	// Backpressure marker emitted by the SDK when StreamPayloads were dropped
	// because the host was slow. Raw["dropped_count"] reports how many.
	StreamDropped StreamKind = "stream.dropped"
)

// StreamPayload is a single structured streaming event emitted by a
// stream-aware adapter. It is intentionally a superset capable of carrying
// text deltas, tool-call lifecycles, reasoning, lifecycle markers, and opaque
// provider payloads.
//
// Field usage by Kind:
//   - run.started / run.finished / run.error: RunID, ThreadID, Usage (on
//     finished), Error (on error). MessageID / ToolCallID empty.
//   - step.started / step.finished: Name required.
//   - text.start / text.end: MessageID required.
//   - text.content: MessageID required; Delta non-empty.
//   - tool_call.start: ToolCallID and Name required; Args optional
//     (complete initial snapshot when the adapter does not stream args).
//   - tool_call.args: ToolCallID required; Delta non-empty (incremental
//     argument chunk, usually JSON fragment).
//   - tool_call.end / tool_call.result: ToolCallID required; Result optional.
//   - reasoning.*: MessageID required; Delta for reasoning.content.
//   - hitl.requested / hitl.resolved: Name identifies the approval type;
//     Raw carries adapter-specific payload (audit-only in v1).
//   - stream.dropped: Raw["dropped_count"] reports the count.
//
// Sequence is assigned monotonically by the SDK in EmitStream; adapters must
// not set it themselves. Timestamp is similarly backfilled when zero.
//
// Seq mirrors Sequence as the canonical per-run monotonic cursor exposed by
// the HITL v2 contract (see docs/workstream-hitl-v2.md §3.4.2). It is always
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

	// Raw carries provider-specific structured data that does not fit the
	// normalized fields. Bridges may pass it through opaquely.
	Raw map[string]any
}
