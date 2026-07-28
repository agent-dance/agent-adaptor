package driver

import "encoding/json"

// AgentIdentity is host-supplied caller identity propagated into SDK hooks.
//
// The SDK does not use these fields for routing. They exist so host-provided
// components such as SkillProvider, WorkspaceManager, and ServiceManager
// can scope lookups by tenant, user/profile, or logical agent name without
// inventing their own context keys.
type AgentIdentity struct {
	ID        string
	TenantID  string
	ProfileID string
	Name      string
}

// TerminalPayload preserves the provider's official terminal protocol event.
// JSON contains the exact JSON value recognized by the driver parser, before
// downstream normalization. Event is the provider-native event or method name
// (for example "result" or "turn/completed").
//
// Drivers MUST populate this value only from an official terminal event. They
// must not synthesize it from Output, Summary, Transcript, or arbitrary JSON
// found elsewhere in stdout.
type TerminalPayload struct {
	Event string
	JSON  json.RawMessage
}

// RawStreams captures the complete raw stdout and stderr emitted during one
// run together with the provider terminal payload recognized from that same
// byte stream. It is the stable surface hosts should rely on for auditing,
// replay, debugging, or archival.
//
// Contract:
//   - Stdout / Stderr hold the full untruncated bytes the child process wrote.
//   - No redaction, no semantic parsing, no line-wise transformation is applied.
//   - Terminal is nil when no official terminal event was observed.
//   - Both consumer Run and Stream.Result must return equivalent values.
type RawStreams struct {
	Stdout   string
	Stderr   string
	Terminal *TerminalPayload
}

// Usage is normalized token/cost accounting reported by drivers when the
// provider protocol exposes it. Individual values may legitimately be zero.
// A nil *Usage on Response, TranscriptItem, or a terminal event means usage
// was not observed; a non-nil zero Usage means the provider explicitly
// reported zero for every normalized metric.
type Usage struct {
	InputTokens        int
	OutputTokens       int
	CachedInputTokens  int
	EstimatedCostMilli int64
}

// Request is the fully resolved invocation the SDK passes to a driver. By
// the time a driver sees this value, Agent defaults and CallOption values
// have been merged, Thread state has been coordinated, workspace/runtime/skill/
// MCP payloads have been resolved, and policy has been validated against the
// descriptor.
type Request struct {
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
	Session        *SessionContext
	Metadata       map[string]string
	OutputSchema   *OutputSchema

	// ModelOverride is the per-run model selected via WithModel. When
	// non-empty it supersedes the construction model carried by Config for this
	// invocation; drivers must prefer it over their Config model when
	// resolving the provider-native model selection. Empty means "no
	// override" and the driver falls back to its construction config.
	ModelOverride string

	// Streaming selects a provider-native streaming transport after the core
	// has resolved the invocation. It is not derived from whether the consumer
	// called Agent.Run or Agent.Stream: both consumer methods share one Event
	// pipeline, and either may use a batch or streaming provider transport.
	// Drivers that implement StreamSupport should use their declared native
	// transport when this field is true.
	//
	// Drivers that do not implement StreamSupport are free to ignore
	// this field. The public Event stream remains available, but it will not
	// contain provider-native normalized deltas from that capability.
	Streaming bool
}

// Response is the driver-facing execution result.
//
// Built-in drivers must fill Output / RawStreams / Transcript / Summary /
// Checkpoint from the same pass that parses the CLI protocol; none of
// these fields may be recomputed by downstream helpers.
type Response struct {
	// Output is final assistant-facing text only. It must not contain raw
	// stdout/stderr dumps, Summary text, or provider terminal JSON.
	Output string
	// RawStreams carries complete raw stdout/stderr and the official provider
	// terminal JSON (when the protocol defines one) for audit and debugging.
	RawStreams *RawStreams
	// Transcript is the normalized semantic item stream parsed by the driver.
	Transcript []TranscriptItem
	// ExitCode, Signal, and TimedOut preserve the observed subprocess
	// outcome. Drivers should attach the more specific Failure parsed from an
	// official provider error event when one exists. If they do not, core
	// classifies any non-zero exit, signal, or timeout as FailureAgentError,
	// except that cancellation of the outer invocation context retains its
	// context.Canceled/context.DeadlineExceeded error identity. An abnormal
	// process outcome can therefore never become a successful Result merely
	// because the provider emitted no terminal error event.
	ExitCode         int
	Signal           string
	TimedOut         bool
	Usage            *Usage
	Checkpoint       *Checkpoint
	Metadata         map[string]string
	Provider         string
	Model            string
	Summary          string
	StructuredOutput *StructuredOutput
	RuntimeServices  []RuntimeServiceReport
	Failure          *RunFailure
}

// SessionMode controls how the SDK coordinates an invocation with a Thread
// store. Without a Thread store, runs are stateless and stateful modes return
// an error rather than silently pretending to resume.
type SessionMode string

const (
	// SessionContinueOrStart resolves the Thread's single opaque host key and
	// resumes its active checkpoint when one exists; otherwise it starts fresh.
	SessionContinueOrStart SessionMode = "continue_or_start"
	// SessionContinueOnly requires a compatible active checkpoint for the
	// Thread key and fails when none exists.
	SessionContinueOnly SessionMode = "continue_only"
	// SessionStartNew starts fresh and rebinds the Thread key only after a valid
	// checkpoint is produced.
	SessionStartNew SessionMode = "start_new"
	// SessionFork starts from a parent checkpoint but persists the result under
	// the fork's distinct Thread key without modifying the parent.
	SessionFork SessionMode = "fork"
	// SessionStateless forces no session resolution or persistence for this run.
	SessionStateless SessionMode = "stateless"
)

// SessionContext is the checkpoint state a resume-capable driver receives for
// one invocation. EngineSessionID is the provider handle to continue; State holds
// the full driver checkpoint; Mode tells the driver whether this is a fresh,
// continued, forked, or stateless invocation.
type SessionContext struct {
	EngineSessionID string
	Mode            SessionMode
	State           *SessionState
	PreviousID      string
}

// SessionState is the driver-owned checkpoint payload persisted by the Thread
// store after a successful run. ResumeID is the provider session
// handle; Data contains driver-specific guards such as cwd or effective
// profile fingerprints.
type SessionState struct {
	ResumeID  string
	DisplayID string
	// Data stores driver-specific session parameters such as cwd, effective
	// profile fingerprints, or repo identifiers needed to validate a resume
	// attempt.
	Data map[string]string
}

// Checkpoint is returned by drivers only when the run produced session state
// that is proven safe to persist. A driver MUST set Valid=true only when all
// of the following hold: the provider process exited successfully, no signal
// or timeout occurred, Response.Failure is nil, the driver's official parser
// observed its successful terminal event, and that protocol supplied an
// explicit top-level resume/session identifier accepted by SessionCodec.
// Init/session announcements, partial output, guessed/nested identifiers and
// terminal error events are not sufficient. There is no failed-run exception:
// non-zero exit, cancellation, malformed protocol, missing
// terminal, or business failure MUST return nil or Valid=false. This prevents
// a failed run from replacing a previously healthy Thread checkpoint.
// Structurally, Valid=true also requires State != nil and a non-empty
// State.ResumeID that round-trips through the codec exposed by the same
// resume-capable Driver.
type Checkpoint struct {
	State *SessionState
	Valid bool
}

// RunFailure carries structured error information when the SDK or a driver
// classifies a failure more precisely than a plain stderr string.
//
// HumanDecision is non-nil exactly when Code is FailureReject or
// FailureTimeout. Drivers MUST uphold this invariant on Response.Failure;
// adaptertest verifies it as RSP-02.
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
// (OnTimeout=FailureAbort path). It is distinct from context.DeadlineExceeded
// on the outer context, which is returned by Run or Stream.Result. nil-safe.
func (f *RunFailure) IsTimedOut() bool {
	return f != nil && f.Code == FailureTimeout
}
