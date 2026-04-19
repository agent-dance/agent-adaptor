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

// RunResult is the normalized outcome returned by Runner.Run or RunHandle.Wait.
//
// It keeps the existing "output + exit code + optional session" surface while
// also exposing provider metadata, runtime service reports, structured result
// payloads, and interactive follow-up questions when an adapter supports them.
type RunResult struct {
	RunID           string
	DriverType      string
	Output          string
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

type RunEventType string

const (
	RunEventStdout     RunEventType = "stdout"
	RunEventStderr     RunEventType = "stderr"
	RunEventSystem     RunEventType = "system"
	RunEventAssistant  RunEventType = "assistant"
	RunEventLifecycle  RunEventType = "lifecycle"
	RunEventInvocation RunEventType = "invocation"
	RunEventSpawn      RunEventType = "spawn"
	RunEventRuntime    RunEventType = "runtime"
)

// RunEvent is the best-effort realtime event envelope produced by Start().
//
// Text remains the human-readable summary, Metadata is for simple string tags,
// and Data carries richer structured payloads such as invocation metadata or
// spawn details.
type RunEvent struct {
	Type      RunEventType
	Text      string
	Timestamp time.Time
	Metadata  map[string]string
	Data      map[string]any
}

type TranscriptItemType string

const (
	TranscriptOutput     TranscriptItemType = "output"
	TranscriptDiagnostic TranscriptItemType = "diagnostic"
	TranscriptStructured TranscriptItemType = "structured"
	TranscriptSummary    TranscriptItemType = "summary"
	TranscriptQuestion   TranscriptItemType = "question"
	TranscriptFailure    TranscriptItemType = "failure"
)

// TranscriptItem is the host-facing normalized transcript unit.
//
// The SDK intentionally keeps this contract conservative. It does not guess
// provider-private semantics it cannot verify. Built-in adapters currently
// emit output, diagnostic, structured, summary, question, and failure items.
type TranscriptItem struct {
	Type     TranscriptItemType
	Text     string
	Metadata map[string]string
	Data     map[string]any
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
// Adapters can keep returning the minimal output/exit-code tuple, but richer
// fields let them surface provider cost, structured payloads, runtime service
// reports, and non-fatal interactive questions without introducing another
// execution entrypoint.
type DriverRunResult struct {
	Output          string
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
