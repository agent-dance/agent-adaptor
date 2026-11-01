package team

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	adaptor "github.com/agent-dance/agent-adaptor/v2"
	clienta2a "github.com/agent-dance/agent-adaptor/v2/clients/a2a"
)

// Continuity selects the lifetime of a member conversation.
type Continuity uint8

const (
	// PerTask isolates conversations by task, retaining them for explicit continue.
	PerTask Continuity = iota
	// PerScope shares a member conversation within one named team scope.
	PerScope
)

// ConversationProvider lets a composite Runner supply conversations without
// covariant Thread return types. Team implements it. The runtime also adapts
// *adaptor.Agent directly, since Agent.Thread returns *adaptor.Thread.
type ConversationProvider interface {
	// Conversation returns the conversation identified by the exact key.
	Conversation(key string) adaptor.ThreadRunner
}

// Member describes one host-selected local or remote team participant.
type Member struct {
	// Key is the exact host-selected roster identifier.
	Key string
	// Name is the display name included in the roster.
	Name string
	// Description describes work this member can accept.
	Description string
	// Skills is descriptive roster data, not a skill installation request.
	Skills []string
	// Runner executes local tasks; Team and Agent are supported directly.
	Runner adaptor.Runner
	// Remote configures an A2A member instead of Runner; exactly one is required.
	Remote *RemoteSpec
	// Continuity defaults to PerTask.
	Continuity Continuity
	// Input is the optional JSON Schema validated before task registration.
	Input map[string]any
	// Output is the optional structured-output schema supplied to the Runner.
	Output map[string]any
	// MaxActive defaults to one; a shared conversation always has one writer.
	MaxActive int
	// Timeout defaults to thirty minutes; negative durations are rejected.
	Timeout time.Duration
}

// Config supplies the leader, roster, optional persistence and resource limits.
type Config struct {
	// Leader is the host-owned coordinating Agent.
	Leader *adaptor.Agent
	// Members is the host-selected local roster, copied during construction.
	Members []Member
	// Store defaults to MemoryStore; anonymous scopes always use private memory.
	Store TaskStore
	// Limits bounds turns, delegation, concurrency and peer messaging.
	Limits Limits
	// Surface configures tool naming without introducing business roles.
	Surface Surface
}

// Surface configures MCP tool names. Prefix defaults to "team_"; a nested
// team should use a distinct prefix when it also receives its parent's tools.
type Surface struct {
	// Prefix is prepended verbatim to delegate, send, status and cancel.
	Prefix string
}

// Limits bounds a single team execution. Zero fields select the documented
// defaults. All configured limits and durations must be positive.
type Limits struct {
	// MaxLeaderTurns defaults to fifty provider turns per team execution.
	MaxLeaderTurns int
	// MaxTasks defaults to twenty registrations per team execution.
	MaxTasks int
	// MaxActiveTasks defaults to four concurrent workers; excess tasks queue.
	MaxActiveTasks int
	// MaxAgentMessages defaults to ten explicit peer messages per leader turn.
	MaxAgentMessages int
	// MaxSenderMessages defaults to three messages per sender per leader turn.
	MaxSenderMessages int
	// WakeWindow defaults to two seconds; it coalesces idle-leader notifications.
	WakeWindow time.Duration
	// RetryBackoff is the minimum retry delay, defaulting to 100 milliseconds.
	RetryBackoff time.Duration
}

func (limits Limits) resolve() (Limits, error) {
	if limits.MaxLeaderTurns < 0 || limits.MaxTasks < 0 || limits.MaxActiveTasks < 0 || limits.MaxAgentMessages < 0 || limits.MaxSenderMessages < 0 || limits.WakeWindow < 0 || limits.RetryBackoff < 0 {
		return limits, errors.New("team: limits must not be negative")
	}
	if limits.MaxLeaderTurns == 0 {
		limits.MaxLeaderTurns = 50
	}
	if limits.MaxTasks == 0 {
		limits.MaxTasks = 20
	}
	if limits.MaxActiveTasks == 0 {
		limits.MaxActiveTasks = 4
	}
	if limits.MaxAgentMessages == 0 {
		limits.MaxAgentMessages = 10
	}
	if limits.MaxSenderMessages == 0 {
		limits.MaxSenderMessages = 3
	}
	if limits.WakeWindow == 0 {
		limits.WakeWindow = 2 * time.Second
	}
	if limits.RetryBackoff == 0 {
		limits.RetryBackoff = 100 * time.Millisecond
	}
	return limits, nil
}

// EncodeKey encodes a tuple with decimal byte-length prefixes. It is stable,
// injective (including empty strings and Unicode), and safe for opaque keys.
func EncodeKey(parts ...string) string {
	var encoded strings.Builder
	for _, part := range parts {
		encoded.WriteString(strconv.Itoa(len(part)))
		encoded.WriteByte(':')
		encoded.WriteString(part)
	}
	return encoded.String()
}

// TaskState describes the lifecycle of a delegation.
type TaskState string

const (
	// Queued means registered and waiting for an execution slot.
	Queued TaskState = "queued"
	// Running means the member is executing.
	Running TaskState = "running"
	// WaitingApproval means the member is waiting for its own approval policy.
	WaitingApproval TaskState = "waiting_approval"
	// Succeeded means the task completed successfully.
	Succeeded TaskState = "succeeded"
	// Failed means the task failed.
	Failed TaskState = "failed"
	// Cancelled means the task was explicitly cancelled.
	Cancelled TaskState = "cancelled"
	// Interrupted means the task lost its execution, including on recovery.
	Interrupted TaskState = "interrupted"
)

func terminal(state TaskState) bool {
	return state == Succeeded || state == Failed || state == Cancelled || state == Interrupted
}

// Task is a durable task snapshot. Revision is a compare-and-swap version.
// Conversation identifies the member context used by explicit continue.
// Result is the locally observed result and may be absent after recovery.
type Task struct {
	// RemoteContext is the last observed A2A context used for continuity.
	RemoteContext string
	// RemoteTask is the provider task ID retained for auditing.
	RemoteTask string
	// RemoteSnapshot preserves the last observed A2A task, including non-text
	// artifacts and protocol payloads. It is absent for local members.
	RemoteSnapshot *clienta2a.Task
	// Scope is the opaque team conversation key.
	Scope string
	// ID is the team-issued task handle.
	ID string
	// Member is the exact roster key.
	Member string
	// Objective is the original delegated objective.
	Objective string
	// Input is detached JSON data validated against the member schema.
	Input map[string]any
	// Conversation is the encoded member Thread key, or empty if unsupported.
	Conversation string
	// Continues identifies the previous task whose context is reused.
	Continues string
	// State is the persisted task lifecycle state.
	State TaskState
	// Revision is the expected CAS version on save and the new version on return.
	Revision uint64
	// CreatedAt records task registration in UTC.
	CreatedAt time.Time
	// UpdatedAt records the last successful store mutation in UTC.
	UpdatedAt time.Time
	// Summary is bounded progress or the member's final summary.
	Summary string
	// Reason is a stable failure or cancellation code, not a provider error.
	Reason string
	// Usage is observed member accounting, or nil when unobserved.
	Usage *adaptor.Usage
	// Unread contains notes left unread after a leader-initiated cancellation.
	Unread []string
	// Result preserves the locally observed full member result, when available.
	Result *adaptor.Result
}

// InboxState is the durable delivery state of a leader notification.
type InboxState string

const (
	// Pending means the message is waiting to be claimed.
	Pending InboxState = "pending"
	// Delivering means a particular lease owner is attempting delivery.
	Delivering InboxState = "delivering"
	// Delivered means provider delivery was acknowledged.
	Delivered InboxState = "delivered"
	// DeliveryFailed means delivery ended with a stable reason code.
	DeliveryFailed InboxState = "failed"
)

// InboxMessage records a leader notification, including task-bound progress
// notes. Wake distinguishes wake-worthy notifications from progress notes.
// Claims are exclusive until ClaimUntil; an expired claim can be reclaimed.
type InboxMessage struct {
	// Host marks an original host message that must retain its ID and author.
	Host bool
	// Deferred holds a standby message with execution options until its own
	// execution starts. Re-admission of the same ID with Deferred=false activates
	// it; acquiring a new scope lease activates option-free records left by a crashed owner.
	Deferred bool
	// RequiresOptions records process-local execution options. After owner loss
	// these messages remain deferred until an explicit host resubmission restores
	// the options; recovery must never execute them with different defaults.
	RequiresOptions bool
	// Timing preserves the host requested delivery timing.
	Timing adaptor.Timing
	// Scope is the exact team conversation key.
	Scope string
	// ID is the exact caller ID for host messages, otherwise team-issued.
	ID string
	// Task associates progress and completion messages with a task handle.
	Task string
	// Text is the original notification body.
	Text string
	// From is assigned by the runtime, never supplied through tool arguments.
	From adaptor.Author
	// Wake distinguishes notifications from progress notes.
	Wake bool
	// State is the durable inbox delivery state.
	State InboxState
	// ClaimToken fences acknowledgments against expired delivery attempts.
	ClaimToken string
	// ClaimUntil is extended with the owning scope lease.
	ClaimUntil time.Time
	// MaybeDuplicate marks an interrupted attempt with ambiguous delivery.
	MaybeDuplicate bool
	// Reason is a stable delivery-failure code.
	Reason string
	// CreatedAt is the UTC admission time.
	CreatedAt time.Time
}

// ScopeLease fences writes by execution owner and token, not just by scope.
// Stores must reject expired leases, including during release and finalization.
type ScopeLease struct {
	// Scope is the exact conversation key fenced by this lease.
	Scope string
	// Owner is the team execution identifier.
	Owner string
	// Token is the store-issued fencing token.
	Token string
	// ExpiresAt is the store-authoritative lease deadline.
	ExpiresAt time.Time
}

// Artifact preserves notification text that exceeds a delivery byte budget.
// URI is a scope-isolated MCP resource; hosts can also read it from TaskStore.
type Artifact struct {
	// Scope is the owning team conversation key.
	Scope string
	// ID is the team-issued artifact identifier.
	ID string
	// Task attributes the content to a delegation, when applicable.
	Task string
	// URI is the stable MCP resource reference used in notifications.
	URI string
	// Text is the complete, immutable original content.
	Text string
}

// TaskStore atomically persists tasks and a claimed leader inbox. All returned
// records must be detached copies. Mutations require an unexpired scope lease;
// SaveTask is CAS (revision zero creates), and FinishMessage requires the exact
// claim token. AcquireScope may replace only an expired lease. ClaimMessages
// claims pending or expired-delivering messages in insertion order, including
// notes only when includeNotes is true. Durable implementations must preserve
// these contracts transactionally across processes.
type TaskStore interface {
	// ForkScope atomically copies terminal tasks and immutable artifacts to an
	// empty target, recording parent lineage. Both live leases must be validated.
	// Target history or existing lineage yields ErrConflict. Inbox is not copied.
	ForkScope(context.Context, ScopeLease, ScopeLease) error
	// ForkParent returns durable fork lineage, or empty for an ordinary scope.
	ForkParent(context.Context, string) (string, error)
	// AcquireScope obtains a lease and reclaims the expired owner's inbox claims.
	AcquireScope(context.Context, string, string, time.Duration) (ScopeLease, error)
	// RenewScope extends the scope lease and its live inbox claims together.
	RenewScope(context.Context, ScopeLease, time.Duration) (ScopeLease, error)
	// ReleaseScope releases only the exact live owner and token.
	ReleaseScope(context.Context, ScopeLease) error
	// Tasks lists all task records in creation order.
	Tasks(context.Context, string) ([]Task, error)
	// SaveTask atomically creates or replaces a task by expected revision and
	// admits any supplied notifications in the same transaction. A terminal
	// task and its leader wake must never be separated by a crash window.
	SaveTask(context.Context, ScopeLease, Task, ...InboxMessage) (Task, error)
	// PutMessage registers a pending message, deduplicates a delivered ID and
	// re-admits an identical failed ID. A duplicate Deferred=false activates a
	// held record; process-local execution options must be restored by the host.
	PutMessage(context.Context, ScopeLease, InboxMessage) error
	// Messages lists detached inbox records in admission order.
	Messages(context.Context, string) ([]InboxMessage, error)
	// ClaimMessages atomically claims pending or expired-delivering records.
	ClaimMessages(context.Context, ScopeLease, bool, time.Duration) ([]InboxMessage, error)
	// RequeueMessage returns an exact live claim after a rejected admission.
	RequeueMessage(context.Context, ScopeLease, string, string) error
	// FinishMessage terminalizes an exact live claim, never a stale one.
	FinishMessage(context.Context, ScopeLease, string, string, InboxState, string) error
	// PutArtifact creates immutable, scope-fenced text content.
	PutArtifact(context.Context, ScopeLease, Artifact) error
	// Artifacts lists detached artifacts in creation order within a scope.
	Artifacts(context.Context, string) ([]Artifact, error)
}

var (
	// ErrConflict means a record revision, lease, or message claim is stale.
	ErrConflict = errors.New("team: stale lease, revision, or claim")
	// ErrThreadBusy means the scope is owned by another execution.
	ErrThreadBusy = adaptor.ErrThreadBusy
	// ErrLimitExceeded means a team execution exhausted a configured limit.
	ErrLimitExceeded = errors.New("team: limit exceeded")
)

// RetryableError is an optional error contract for leader infrastructure
// failures. RetryAfter supplies a minimum backoff; a negative duration means
// the error is not retryable. Context cancellation is never retried.
type RetryableError interface {
	error
	// RetryAfter returns a minimum delay, or a negative duration to disable retry.
	RetryAfter() time.Duration
}

func conversation(runner adaptor.Runner, key string) (adaptor.Runner, error) {
	switch typed := runner.(type) {
	case *adaptor.Agent:
		return typed.Thread(key), nil
	case ConversationProvider:
		thread := typed.Conversation(key)
		if !nilRunner(thread) {
			return thread, nil
		}
	}
	return nil, fmt.Errorf("team: %T does not provide conversations", runner)
}
