package agentadaptor

import (
	"context"
	"encoding/json"
	"time"
)

// AgentIdentity is host-supplied caller identity propagated into SDK hooks.
//
// The SDK does not use these fields for routing. They exist so host-provided
// components such as SkillProvider, WorkspaceManager, and RuntimeServiceManager
// can scope lookups by tenant, user/profile, or logical agent name without
// inventing their own context keys.
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
	RunID      string
	DriverType string
	// Output is final assistant-facing text only. It must not contain raw
	// stdout/stderr dumps, Summary text, or provider terminal JSON.
	Output string
	// RawStreams carries complete raw stdout/stderr for audit and debugging.
	RawStreams *RawStreams
	// Transcript is the normalized semantic item stream parsed by the adapter.
	Transcript []TranscriptItem
	ExitCode   int
	Signal     string
	TimedOut   bool
	Usage      *Usage
	// Session is populated when a valid checkpoint was produced and persisted
	// (or when a stateless/no-store run can still report the provider handle).
	Session  *SessionRef
	Metadata map[string]string
	Provider string
	Biller   string
	Model    string
	// BillingType and CostUSD are best-effort adapter metadata for hosts that
	// want to render cost/audit summaries; absence does not imply free usage.
	BillingType string
	CostUSD     *float64
	// Summary is a short host-facing label suitable for lists, logs, or issue
	// comments. It is deliberately separate from Output.
	Summary string
	// Result is the adapter-recognized terminal result event payload. Hosts
	// should treat it as provider-specific audit data, not portable text.
	Result           map[string]any
	StructuredOutput *StructuredOutput
	RuntimeServices  []RuntimeServiceReport
	Question         *RunQuestion
	// Failure is a structured business-level failure from a completed run.
	// Check Wait's returned error first, then Failure, then success.
	Failure *RunFailure
}

// Usage is normalized token/cost accounting reported by adapters when the
// provider protocol exposes it. Values may be zero when an adapter cannot
// observe the metric.
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
	// TranscriptSystem is adapter/system text.
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

// DriverRunRequest is the fully resolved invocation the SDK passes to an
// adapter. By the time an adapter sees this value, binding defaults and
// per-call RunOptions have been merged, sessions have been coordinated,
// workspace/runtime/skill/MCP payloads have been resolved, and policy has been
// validated against the descriptor.
type DriverRunRequest struct {
	RunID          string
	Prompt         string
	Config         any
	Agent          AgentIdentity
	Workspace      WorkspaceLease
	Runtime        RuntimePayload
	Skills         ResolvedSkills
	MCP            MCPPayload
	ProfilePayload ProfilePayload
	Profile        *ProfileSelection
	Policy         RunPolicy
	Instructions   *InstructionsBundleRef
	Session        *DriverSessionContext
	Metadata       map[string]string
	OutputSchema   *OutputSchema

	// ModelOverride is the per-run model selected via WithModel. When
	// non-empty it supersedes the binding model carried by Config for this
	// invocation; adapters must prefer it over their Config model when
	// resolving the provider-native model selection. Empty means "no
	// override" and the adapter falls back to the binding model.
	ModelOverride string

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
	// Output follows the same contract as RunResult.Output.
	Output string
	// RawStreams follows the same contract as RunResult.RawStreams.
	RawStreams *RawStreams
	// Transcript follows the same contract as RunResult.Transcript.
	Transcript       []TranscriptItem
	ExitCode         int
	Signal           string
	TimedOut         bool
	Usage            *Usage
	Checkpoint       *DriverCheckpoint
	Metadata         map[string]string
	Provider         string
	Biller           string
	Model            string
	BillingType      string
	CostUSD          *float64
	Summary          string
	Result           map[string]any
	StructuredOutput *StructuredOutput
	RuntimeServices  []RuntimeServiceReport
	Question         *RunQuestion
	Failure          *RunFailure
}

// DriverSessionContext is the session state a resume-capable adapter receives
// for one run. EngineSessionID is the provider handle to continue; State holds
// the full adapter checkpoint; Mode tells the adapter whether this is a fresh,
// continued, forked, or stateless invocation.
type DriverSessionContext struct {
	EngineSessionID string
	Mode            SessionMode
	State           *DriverSessionState
	PreviousID      string
}

// DriverSessionState is the adapter-owned checkpoint payload persisted by the
// SessionStore after a successful run. ResumeID is the provider session handle;
// Data contains adapter-specific guards such as cwd or prompt-bundle hashes.
type DriverSessionState struct {
	ResumeID  string
	DisplayID string
	// Data stores adapter-specific session parameters such as cwd,
	// prompt-bundle fingerprints, or repo identifiers needed to validate a
	// resume attempt.
	Data map[string]string
}

// DriverCheckpoint is returned by adapters when the run produced a session
// state that is safe to persist. Valid must be false for non-resumable
// runs so the SDK does not contaminate a healthy session mapping.
//
// "Non-resumable" refers to the session itself — e.g. the upstream provider
// never issued a session id, or the adapter knows the session was rejected
// or revoked server-side. A non-zero exit code from the local CLI subprocess
// (max_turns reached, upstream model provider API error, network blip, OOM,
// signal, ...) does not by itself imply the session is non-resumable: the
// session id is already minted on the provider when the first event arrives,
// and a subsequent resume will either succeed or surface a clean upstream
// error. Adapters SHOULD therefore preserve the captured session in the
// checkpoint whenever one is available, and only set Valid=false when they
// have positive evidence that resuming will not work.
type DriverCheckpoint struct {
	State *DriverSessionState
	Valid bool
}

// OutputFormat labels the final business-output contract requested by a host.
// It is distinct from adapter protocol envelopes such as `stream-json`, which
// only make CLI events machine-readable.
type OutputFormat string

const (
	OutputFormatJSONSchema OutputFormat = "json_schema"
)

// StructuredOutputMode states the enforcement level the host is requesting.
type StructuredOutputMode string

const (
	// StructuredOutputNativeStrict requires provider/CLI-native schema
	// enforcement. The SDK rejects unsupported adapters before launch.
	StructuredOutputNativeStrict StructuredOutputMode = "native_strict"
	// StructuredOutputPreferNative uses native enforcement when available,
	// otherwise the explicit prompt+validation fallback when supported.
	StructuredOutputPreferNative StructuredOutputMode = "prefer_native"
	// StructuredOutputPromptValidate injects exact-JSON instructions and
	// validates the adapter's final Output locally.
	StructuredOutputPromptValidate StructuredOutputMode = "prompt_validate"
)

// StructuredOutputInvalidPolicy selects how prompt-validation failures are
// surfaced after the adapter returns.
type StructuredOutputInvalidPolicy string

const (
	// StructuredOutputFailRun marks the run with FailurePolicyError when the
	// final JSON is absent or fails local validation.
	StructuredOutputFailRun StructuredOutputInvalidPolicy = "fail_run"
	// StructuredOutputReturnInvalid returns StructuredOutput.Valid=false but
	// does not turn the run into a failure.
	StructuredOutputReturnInvalid StructuredOutputInvalidPolicy = "return_invalid"
)

// OutputSchema is a per-run request for final structured JSON output.
// SchemaJSON is the raw JSON Schema document supplied by the host or generated
// by JSONSchemaFor. Public API intentionally exposes no third-party schema
// library types.
type OutputSchema struct {
	Format      OutputFormat
	Mode        StructuredOutputMode
	SchemaJSON  json.RawMessage
	Name        string
	Description string
	OnInvalid   StructuredOutputInvalidPolicy
}

// StructuredOutputSource reports which mechanism produced the final JSON.
type StructuredOutputSource string

const (
	StructuredOutputSourceNative         StructuredOutputSource = "native"
	StructuredOutputSourcePromptValidate StructuredOutputSource = "prompt_validate"
)

// StructuredOutput is the portable final business value for structured-output
// runs. RawJSON is never raw stdout and never a provider terminal wrapper; it
// is the final assistant JSON value validated against the requested schema.
type StructuredOutput struct {
	Format OutputFormat
	Mode   StructuredOutputMode
	Source StructuredOutputSource

	RawJSON json.RawMessage
	Value   any

	Valid            bool
	ValidationErrors []string
	SchemaHash       string
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
// (see docs/run-policy.md §5).
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

// WorkspaceManager is the host hook that turns a WorkspaceSpec into a concrete
// working directory lease. Implementations may return the project directory
// unchanged, create git worktrees, provision sandboxes, or delegate to an
// external workspace service. The SDK releases the lease after the run using
// the requested WorkspaceReleaseMode.
type WorkspaceManager interface {
	Resolve(ctx context.Context, req WorkspaceRequest) (WorkspaceLease, error)
	Release(ctx context.Context, lease WorkspaceLease, mode WorkspaceReleaseMode) error
}

// RuntimeServiceManager is the host hook for preparing services a run depends
// on, such as local dev servers, databases, or tool sidecars. The SDK only
// coordinates lifecycle; the host owns process/container orchestration.
type RuntimeServiceManager interface {
	// Ensure starts or locates the runtime services needed for one run and
	// returns concrete refs the adapter can inject into the prompt/profile.
	Ensure(ctx context.Context, req RuntimeServiceRequest) ([]RuntimeServiceRef, error)
	// ReleaseByRun releases services scoped to one SDK RunID. The SDK calls it
	// during normal cleanup for run-scoped services.
	ReleaseByRun(ctx context.Context, runID string) error

	// ReleaseByLabels releases every service whose Metadata contains
	// every key-value pair in labels. The semantics match a logical
	// AND across the labels map, NOT individual matches.
	//
	// An empty labels map releases nothing — callers must explicitly
	// opt-in to broad releases by passing at least one key-value
	// pair. This is a deliberate guard against accidental "release
	// everything you have" calls (compare ReleaseByRun, which always
	// scopes to one runID).
	//
	// Implementations should not error when no service matches; the
	// invariant after a successful call is "no service whose
	// Metadata covers labels remains running", which is trivially
	// satisfied by an empty match. Backend errors (Docker daemon
	// down, permission denied, etc.) propagate as-is.
	//
	// This method exists primarily to support host-side cleanup by
	// task / tenant / workspace label after a process restart, when
	// the runID-scoped index from a previous incarnation has been
	// lost. See docs/v0.5.0-host-integration-plan.md §A3 for the
	// design rationale and why a host-driven Reconcile(aliveRunIDs)
	// was deliberately NOT added.
	ReleaseByLabels(ctx context.Context, labels map[string]string) error
}

type resolvedInvocation struct {
	runID          string
	prompt         string
	adapter        DriverAdapter
	config         any
	agent          AgentIdentity
	workspace      WorkspaceLease
	runtime        RuntimePayload
	skills         ResolvedSkills
	mcp            MCPPayload
	profilePayload ProfilePayload
	profile        *ProfileSelection
	policy         RunPolicy
	handlers       decisionHandlers
	instructions   *InstructionsBundleRef
	session        SessionRequest
	metadata       map[string]string
	outputSchema   *OutputSchema
	outputSource   StructuredOutputSource
	fingerprint    string
	streaming      bool
	model          string
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
// The zero value is RoleAssistant: every adapter today emits text.start /
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
// (see pkg/bridges/agui.RunAgentInput.UserTurnPayloads). Adapters MUST
// NOT emit RoleUser themselves.
type Role string

const (
	// RoleAssistant is the default; emitted by every adapter today.
	RoleAssistant Role = ""
	// RoleUser marks a text lifecycle synthesized above the driver
	// layer to represent the human turn that triggered the run.
	RoleUser Role = "user"
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
//   - text.start / text.end: MessageID required. Role optional; zero value
//     is treated as RoleAssistant. RoleUser MUST be paired with a
//     non-empty MessageID that stays stable across the start/content/end
//     triple of a single user turn.
//   - text.content: MessageID required; Delta non-empty. Role optional
//     (see text.start).
//   - tool_call.start: ToolCallID and Name required; Args optional
//     (complete initial snapshot when the adapter does not stream args).
//   - tool_call.args: ToolCallID required; Delta non-empty (incremental
//     argument chunk, usually JSON fragment).
//   - tool_call.end / tool_call.result: ToolCallID required; Result optional.
//   - reasoning.*: MessageID required; Delta for reasoning.content.
//   - hitl.requested / hitl.resolved: HITLRequested / HITLResolved carry the
//     normalized decision envelope; Raw may carry adapter-specific payload.
//   - stream.dropped: Raw["dropped_count"] reports the count.
//
// Sequence is assigned monotonically by the SDK in EmitStream; adapters must
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
	// RoleAssistant for backward compatibility; see Role docs. Adapters
	// MUST leave Role at zero on every Kind they emit.
	Role Role

	// Raw carries provider-specific structured data that does not fit the
	// normalized fields. Bridges may pass it through opaquely.
	Raw map[string]any
}
