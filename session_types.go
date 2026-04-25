package agentadaptor

import (
	"context"
	"time"
)

type SessionMode string

const (
	SessionContinueOrStart SessionMode = "continue_or_start"
	SessionContinueOnly    SessionMode = "continue_only"
	SessionStartNew        SessionMode = "start_new"
	SessionFork            SessionMode = "fork"
	SessionStateless       SessionMode = "stateless"
)

type SessionRequest struct {
	ID        string
	Namespace string
	Key       string
	Mode      SessionMode
	ForkFrom  string
}

type SessionCompatibilityStatus string

const (
	SessionCompatibilityNew          SessionCompatibilityStatus = "new"
	SessionCompatibilityCompatible   SessionCompatibilityStatus = "compatible"
	SessionCompatibilityIncompatible SessionCompatibilityStatus = "incompatible"
)

type SessionCompatibility struct {
	Status              SessionCompatibilityStatus
	Reason              string
	ExpectedFingerprint string
	ActualFingerprint   string
}

type SessionRef struct {
	ID            string
	Namespace     string
	Key           string
	DisplayID     string
	Reused        bool
	Created       bool
	PreviousID    string
	Compatibility SessionCompatibility
}

type SessionQuery struct {
	ID              string
	Namespace       string
	Key             string
	IncludeArchived bool
}

type SessionStatus string

const (
	SessionStatusActive   SessionStatus = "active"
	SessionStatusArchived SessionStatus = "archived"
)

type SessionRecord struct {
	ID                       string
	Namespace                string
	Key                      string
	Status                   SessionStatus
	DriverType               string
	Agent                    AgentIdentity
	Fingerprint              string
	CompatibilityFingerprint string
	DriverState              *DriverSessionState
	CreatedAt                time.Time
	UpdatedAt                time.Time
}

type SessionLease struct {
	Target string
	Owner  string
	Token  string
}

type SessionFinalizeRequest struct {
	Record       SessionRecord
	PreviousID   string
	Namespace    string
	Key          string
	HeldLeases   []SessionLease
	ArchiveOld   bool
	RebindActive bool
}

// SessionStore persists SDK-level session state for resume-capable adapters.
//
// "Session" here is the SDK's session ontology (see AGENTS.md §6):
//
//   - resume tokens, compatibility fingerprints, lease coordination
//   - mode-driven lifecycle (continue_or_start / start_new / fork / ...)
//   - indexed by SessionID; (Namespace, Key) is a secondary index
//
// SessionStore is NOT the right place for:
//
//   - host-facing chat / thread / conversation history payloads
//     → use pkg/hosttools/sessionrecorder with sessionKey = ThreadID
//     (or sessionKey = RunID for audit-style recording)
//   - HITL pending requests
//     → derive on demand via sessionrecorder.PendingDecisions(records);
//     do NOT persist a separate pending dimension (double-write risk
//     between history and pending; see docs/v0.5.0-host-integration-plan.md §B1)
//   - "the conversation a user sees in a chat UI"
//     → host concern; the SDK exposes no equivalent abstraction
//
// Hosts that need any of the above should compose
// pkg/hosttools/sessionrecorder (and their own task store), NOT extend
// SessionStore. Adding a SaveHistory/LoadHistory method on a SessionStore
// implementation is a no-op as far as the SDK is concerned — those
// methods will never be invoked.
//
// See docs/usage-guide.md "宿主集成 — 命名陷阱" for the four
// canonical mistakes hosts make at this boundary.
//
// Implementations are expected to validate lease ownership during
// Finalize and make the record save/archive/rebind sequence atomic
// relative to their own storage backend.
type SessionStore interface {
	Resolve(ctx context.Context, q SessionQuery) (*SessionRecord, error)
	Finalize(ctx context.Context, req SessionFinalizeRequest) error
	AcquireLease(ctx context.Context, sessionID, owner string, ttl time.Duration) (SessionLease, error)
	RenewLease(ctx context.Context, lease SessionLease, ttl time.Duration) error
	ReleaseLease(ctx context.Context, lease SessionLease) error
}
