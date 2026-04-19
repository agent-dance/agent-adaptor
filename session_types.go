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

// SessionStore is an optional service-facing control-plane hook.
//
// Implementations are expected to validate lease ownership during Finalize and
// make the record save/archive/rebind sequence atomic relative to their own
// storage backend.
type SessionStore interface {
	Resolve(ctx context.Context, q SessionQuery) (*SessionRecord, error)
	Finalize(ctx context.Context, req SessionFinalizeRequest) error
	AcquireLease(ctx context.Context, sessionID, owner string, ttl time.Duration) (SessionLease, error)
	RenewLease(ctx context.Context, lease SessionLease, ttl time.Duration) error
	ReleaseLease(ctx context.Context, lease SessionLease) error
}
