package driver

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

// Usage is normalized token/cost accounting reported by drivers when the
// provider protocol exposes it. Values may be zero when a driver cannot
// observe the metric.
type Usage struct {
	InputTokens        int
	OutputTokens       int
	CachedInputTokens  int
	EstimatedCostMilli int64
}

// Request is the fully resolved invocation the SDK passes to a driver. By
// the time a driver sees this value, binding defaults and per-call options
// have been merged, sessions have been coordinated, workspace/runtime/skill/
// MCP payloads have been resolved, and policy has been validated against the
// descriptor.
//
// The root package exposes this type as agentadaptor.DriverRunRequest.
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
	// non-empty it supersedes the binding model carried by Config for this
	// invocation; drivers must prefer it over their Config model when
	// resolving the provider-native model selection. Empty means "no
	// override" and the driver falls back to the binding model.
	ModelOverride string

	// Streaming selects a provider-native streaming transport after the core
	// has resolved the invocation. It is not derived from whether the consumer
	// called Agent.Run or Agent.Stream: both consumer methods share one Event
	// pipeline, and either may use a batch or streaming provider transport.
	// Drivers that implement StreamSupport should use their declared native
	// transport when this field is true.
	//
	// Drivers that do not implement StreamSupport are free to ignore
	// this field. Hosts consuming stream events on such drivers will see
	// an immediately-closed channel.
	Streaming bool
}

// Response is the driver-facing execution result.
//
// Built-in drivers must fill Output / RawStreams / Transcript / Summary /
// Result / Checkpoint from the same pass that parses the CLI protocol; none of
// these fields may be recomputed by downstream helpers.
//
// The root package exposes this type as agentadaptor.DriverRunResult.
type Response struct {
	// Output is final assistant-facing text only. It must not contain raw
	// stdout/stderr dumps, Summary text, or provider terminal JSON.
	Output string
	// RawStreams carries complete raw stdout/stderr for audit and debugging.
	RawStreams *RawStreams
	// Transcript is the normalized semantic item stream parsed by the driver.
	Transcript       []TranscriptItem
	ExitCode         int
	Signal           string
	TimedOut         bool
	Usage            *Usage
	Checkpoint       *Checkpoint
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

// SessionMode controls how the SDK coordinates a run with the session store.
// Without a session store, runs are stateless and session-aware modes return
// an error rather than silently pretending to resume.
type SessionMode string

const (
	// SessionContinueOrStart resolves (Namespace, Key) and resumes when an
	// active mapping exists; otherwise it starts a new session.
	SessionContinueOrStart SessionMode = "continue_or_start"
	// SessionContinueOnly requires an existing concrete SessionID or key
	// mapping and fails when no compatible session exists.
	SessionContinueOnly SessionMode = "continue_only"
	// SessionStartNew starts fresh and rebinds (Namespace, Key) only after a
	// valid checkpoint is produced.
	SessionStartNew SessionMode = "start_new"
	// SessionFork starts from ForkFrom but persists the result as a distinct
	// session mapping.
	SessionFork SessionMode = "fork"
	// SessionStateless forces no session resolution or persistence for this run.
	SessionStateless SessionMode = "stateless"
)

// SessionContext is the session state a resume-capable driver receives for
// one run. EngineSessionID is the provider handle to continue; State holds
// the full driver checkpoint; Mode tells the driver whether this is a fresh,
// continued, forked, or stateless invocation.
//
// The root package exposes this type as agentadaptor.DriverSessionContext.
type SessionContext struct {
	EngineSessionID string
	Mode            SessionMode
	State           *SessionState
	PreviousID      string
}

// SessionState is the driver-owned checkpoint payload persisted by the
// session store after a successful run. ResumeID is the provider session
// handle; Data contains driver-specific guards such as cwd or prompt-bundle
// hashes.
//
// The root package exposes this type as agentadaptor.DriverSessionState.
type SessionState struct {
	ResumeID  string
	DisplayID string
	// Data stores driver-specific session parameters such as cwd,
	// prompt-bundle fingerprints, or repo identifiers needed to validate a
	// resume attempt.
	Data map[string]string
}

// Checkpoint is returned by drivers only when the run produced session state
// that is proven safe to persist. A driver MUST set Valid=true only when all
// of the following hold: the provider process exited successfully, no signal
// or timeout occurred, Response.Failure is nil, the driver's official parser
// observed its successful terminal event, and that protocol supplied an
// explicit top-level resume/session identifier accepted by SessionCodec.
// Init/session announcements, partial output, guessed/nested identifiers and
// terminal error events are not sufficient. In v1 there is no failed-run
// exception: non-zero exit, cancellation, malformed protocol, missing
// terminal, or business failure MUST return nil or Valid=false. This prevents
// a failed run from replacing a previously healthy Thread checkpoint.
//
// The root package exposes this type as agentadaptor.DriverCheckpoint.
type Checkpoint struct {
	State *SessionState
	Valid bool
}

// RunQuestion represents a driver-requested follow-up question that the host
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

// RunFailure carries structured error information when the SDK or a driver
// classifies a failure more precisely than a plain stderr string.
//
// HumanDecision is non-nil exactly when Code is FailureReject or
// FailureTimeout. Drivers MUST uphold this invariant on Response.Failure;
// core validates it before exposing the final Result.
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
