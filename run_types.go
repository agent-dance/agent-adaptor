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
	Permissions  PermissionProfile
	Instructions *InstructionsBundleRef
	Session      *DriverSessionContext
	Metadata     map[string]string
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

// RunFailure carries structured error information when an adapter can classify
// a failure more precisely than a plain stderr string.
type RunFailure struct {
	Message  string
	Code     string
	Metadata map[string]string
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
	permissions  PermissionProfile
	instructions *InstructionsBundleRef
	session      SessionRequest
	metadata     map[string]string
	fingerprint  string
}
