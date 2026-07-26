package engine

import (
	"context"
	"time"
)

// SessionRequest is the host-facing session instruction for one run. Namespace
// and Key form the stable business key; ID/ForkFrom refer to concrete SDK
// session handles.
type SessionRequest struct {
	ID        string
	Namespace string
	Key       string
	Mode      SessionMode
	ForkFrom  string
}

// SessionCompatibilityStatus classifies whether a stored checkpoint can be
// reused with the current resolved invocation.
type SessionCompatibilityStatus string

const (
	// SessionCompatibilityNew means no previous checkpoint was reused.
	SessionCompatibilityNew SessionCompatibilityStatus = "new"
	// SessionCompatibilityCompatible means the stored checkpoint matched the
	// current invocation fingerprint and was safe to resume.
	SessionCompatibilityCompatible SessionCompatibilityStatus = "compatible"
	// SessionCompatibilityIncompatible means a stored checkpoint existed but
	// resume was rejected because important context changed.
	SessionCompatibilityIncompatible SessionCompatibilityStatus = "incompatible"
)

// SessionCompatibility explains why a stored session did or did not match the
// current invocation fingerprint.
type SessionCompatibility struct {
	Status              SessionCompatibilityStatus
	Reason              string
	ExpectedFingerprint string
	ActualFingerprint   string
}

// SessionRef is returned in RunResult when a session was reused, created, or
// rebound. ID is the concrete SDK session handle; Namespace/Key are the stable
// host-facing lookup tuple.
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

// SessionQuery is the lookup shape passed to SessionStore.Resolve.
type SessionQuery struct {
	ID              string
	Namespace       string
	Key             string
	IncludeArchived bool
}

// SessionStatus is the storage lifecycle state of a SessionRecord.
type SessionStatus string

const (
	// SessionStatusActive marks the current record for a SessionKey.
	SessionStatusActive SessionStatus = "active"
	// SessionStatusArchived marks a previous record retained for audit/forking.
	SessionStatusArchived SessionStatus = "archived"
)

// SessionRecord is the durable SDK session record stored by SessionStore.
// DriverState is adapter-owned checkpoint data; Fingerprint fields are used to
// reject unsafe resumes when workspace/skills/MCP/policy context changes.
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

// SessionLease is the optimistic/concurrent-use guard returned by
// SessionStore.AcquireLease. Stores should validate Token ownership during
// Finalize and ReleaseLease.
type SessionLease struct {
	Target string
	Owner  string
	Token  string
}

// SessionFinalizeRequest tells a SessionStore how to persist the post-run
// session state. It includes the new record, any old active mapping, held
// leases, and whether the store should archive/rebind atomically.
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
