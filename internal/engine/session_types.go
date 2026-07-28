package engine

import (
	"context"
	"time"
)

// SessionRequest is the engine's thread-state instruction for one run.
// ForkFromKey lets the coordinator lock and resolve both parent and target
// thread keys without a caller-side prelookup.
type SessionRequest struct {
	ID           string
	Namespace    string
	Key          string
	Mode         SessionMode
	ForkFrom     string
	ForkFromKey  string
	SessionCodec string
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

// SessionRef reports the engine record reused, created, or rebound by a Thread
// operation.
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
	SessionCodec             string
	DriverState              *SessionState
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
	Record           SessionRecord
	PreviousID       string
	Namespace        string
	Key              string
	HeldLeases       []SessionLease
	ArchiveOld       bool
	RebindActive     bool
	RequireKeyAbsent bool
}

// SessionStore is the private coordination port behind the public threadstore
// contract. It persists resume checkpoints, compatibility fingerprints, and
// leases; chat history and pending approvals remain host concerns. Finalize
// must validate lease ownership and atomically save, archive, and rebind the
// active record. AcquireLease implementations must return an error matching
// ErrSessionBusy only for a genuine live-owner conflict and preserve context
// cancellation, deadlines, and backend error identity for all other failures.
type SessionStore interface {
	Resolve(ctx context.Context, q SessionQuery) (*SessionRecord, error)
	Finalize(ctx context.Context, req SessionFinalizeRequest) error
	AcquireLease(ctx context.Context, sessionID, owner string, ttl time.Duration) (SessionLease, error)
	RenewLease(ctx context.Context, lease SessionLease, ttl time.Duration) error
	ReleaseLease(ctx context.Context, lease SessionLease) error
}
