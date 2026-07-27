// Package threadstore defines the storage contract behind stateful Threads
// (v1 consumer API, docs/api-v1-redesign.md §2.4).
//
// A Store persists the driver resume checkpoints that let a Thread continue
// a conversation across runs and processes. It carries the exact capability
// set of the legacy root-package SessionStore — resolve, finalize, and the
// lease trio that guards concurrent use — with the v1 identity model: the
// legacy (Namespace, Key) pair collapses into the host's single thread key
// (multi-tenant hosts compose their own, e.g. "tenant-1/issue-123").
//
// Ontology guardrail (unchanged from the legacy SessionStore contract): a
// Store holds resume tokens, compatibility fingerprints, and lease
// coordination. It is NOT a chat-history store, not an HITL pending queue,
// and not "the conversation a user sees in a chat UI" — hosts that need
// those compose their own recording on top (see pkg/hosttools).
//
// Dependency rule: this package may import the driver SPI package only —
// never the root package or the internal engine.
package threadstore

import (
	"context"
	"errors"
	"time"

	"github.com/agent-dance/agent-adaptor/driver"
)

// Status is the storage lifecycle state of a Record.
type Status string

const (
	// StatusActive marks the current record for a thread key.
	StatusActive Status = "active"
	// StatusArchived marks a previous record retained for audit/forking.
	StatusArchived Status = "archived"
)

// Record is the durable per-conversation record a Store persists. State is
// driver-owned checkpoint data; the fingerprint fields are used to reject
// unsafe resumes when important context (identity, model, workspace,
// instructions, ...) changed between runs.
type Record struct {
	// ID is the SDK-assigned internal session identifier. Consumers never
	// see it (v1 exposes only the thread key and run IDs); stores index by
	// it and keep it stable for the record's lifetime.
	ID string
	// Key is the host's thread key — the stable business handle. Multiple
	// records may share a Key over time when StartNew archives the old one;
	// at most one of them is StatusActive. Fork requires a previously unused
	// target key and never archives its parent.
	Key string
	// Status is the lifecycle state (active / archived).
	Status Status
	// DriverType records which driver produced the checkpoint.
	DriverType string
	// Agent is the caller identity captured at persist time.
	Agent driver.AgentIdentity
	// Fingerprint is the invocation fingerprint captured at persist time.
	Fingerprint string
	// CompatibilityFingerprint is the guard compared on resume; a mismatch
	// rejects the resume instead of contaminating the conversation.
	CompatibilityFingerprint string
	// SessionCodec is the stable name of the driver codec that normalized
	// State. Fork and resume coordination use it to reject checkpoint formats
	// that the current driver cannot safely interpret.
	SessionCodec string
	// State is the driver-owned resume checkpoint.
	State *driver.SessionState
	// CreatedAt/UpdatedAt are storage timestamps (UTC).
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Query is the lookup shape passed to Store.Resolve. Exactly one of ID or
// Key is set. Archived records are only returned when IncludeArchived is
// true.
type Query struct {
	ID              string
	Key             string
	IncludeArchived bool
}

// Lease is the concurrent-use guard returned by Store.AcquireLease. Stores
// must validate Owner+Token during Finalize, RenewLease, and ReleaseLease so
// an expired-and-reacquired lease can never finalize stale state.
type Lease struct {
	Target string
	Owner  string
	Token  string
}

// FinalizeRequest tells a Store how to persist the post-run thread state:
// save the new record, optionally archive the previous one, and rebind the
// key's active mapping — atomically relative to the store's backend, after
// validating every held lease. When RequireKeyAbsent is set, checking the
// key precondition and applying all mutations are one atomic operation.
type FinalizeRequest struct {
	Record     Record
	PreviousID string
	// Key is the thread key whose active mapping is rebound when
	// RebindActive is set.
	Key        string
	HeldLeases []Lease
	// ArchiveOld archives the PreviousID record (StartNew/resume-fallback
	// paths keep the old conversation addressable for audit).
	ArchiveOld bool
	// RebindActive points the Key's active mapping at Record.ID.
	RebindActive bool
	// RequireKeyAbsent makes Finalize fail atomically with ErrAlreadyExists
	// when Key already has an active mapping. Fork uses this compare-and-set
	// guard in addition to its key lease so a stale or non-cooperating writer
	// cannot create two active children for the same host key.
	RequireKeyAbsent bool
}

// Store persists Thread state for resume-capable drivers. Semantics are
// capability-equivalent to the legacy SessionStore five-method contract:
//
//   - Resolve: look up by internal ID or by thread key. A missing record is
//     (nil, nil), not an error. Archived records require IncludeArchived.
//   - Finalize: validate every held lease (owner+token, unexpired), then
//     atomically save/archive/rebind.
//   - AcquireLease / RenewLease / ReleaseLease: exclusive-use coordination.
//     Acquire fails with a BusyError while another owner holds an unexpired
//     lease on target; acquiring an expired or self-owned lease succeeds.
//     Renew and Finalize fail with a LeaseLostError when the caller no
//     longer owns the matching token. Release is idempotent and ignores
//     lost/stale leases.
type Store interface {
	Resolve(ctx context.Context, q Query) (*Record, error)
	Finalize(ctx context.Context, req FinalizeRequest) error
	AcquireLease(ctx context.Context, target, owner string, ttl time.Duration) (Lease, error)
	RenewLease(ctx context.Context, lease Lease, ttl time.Duration) error
	ReleaseLease(ctx context.Context, lease Lease) error
}

// Base sentinels for errors.Is matching. The struct errors below unwrap to
// them and carry the concrete target.
var (
	// ErrBusy matches AcquireLease failures while another owner holds an
	// unexpired lease on the target.
	ErrBusy = errors.New("threadstore: target busy")
	// ErrLeaseLost matches RenewLease/Finalize failures when the caller's
	// lease token is no longer current.
	ErrLeaseLost = errors.New("threadstore: lease lost")
	// ErrAlreadyExists matches a conditional Finalize that found an active
	// mapping where the caller required the thread key to be unused.
	ErrAlreadyExists = errors.New("threadstore: thread already exists")
)

// BusyError is returned by AcquireLease when the target is exclusively held
// by another owner. Unwrap returns ErrBusy.
type BusyError struct {
	Target string
}

// Error reports the busy target when present.
func (e *BusyError) Error() string {
	if e == nil || e.Target == "" {
		return ErrBusy.Error()
	}
	return ErrBusy.Error() + ": " + e.Target
}

// Unwrap returns ErrBusy so errors.Is(err, ErrBusy) holds.
func (e *BusyError) Unwrap() error { return ErrBusy }

// LeaseLostError is returned by RenewLease/Finalize when the caller no
// longer owns the lease (expired, released, or reacquired by someone else).
// Unwrap returns ErrLeaseLost.
type LeaseLostError struct {
	Target string
}

// Error reports the lost lease target when present.
func (e *LeaseLostError) Error() string {
	if e == nil || e.Target == "" {
		return ErrLeaseLost.Error()
	}
	return ErrLeaseLost.Error() + ": " + e.Target
}

// Unwrap returns ErrLeaseLost so errors.Is(err, ErrLeaseLost) holds.
func (e *LeaseLostError) Unwrap() error { return ErrLeaseLost }

// AlreadyExistsError is returned by a conditional Finalize when Key already
// has an active mapping. Unwrap returns ErrAlreadyExists.
type AlreadyExistsError struct {
	Key string
}

// Error reports the conflicting host thread key when present.
func (e *AlreadyExistsError) Error() string {
	if e == nil || e.Key == "" {
		return ErrAlreadyExists.Error()
	}
	return ErrAlreadyExists.Error() + ": " + e.Key
}

// Unwrap returns ErrAlreadyExists so errors.Is(err, ErrAlreadyExists) holds.
func (e *AlreadyExistsError) Unwrap() error { return ErrAlreadyExists }

// ThreadAlreadyExists marks this as the store-neutral conditional-finalize
// conflict understood by the internal coordinator. The method deliberately
// carries no data and keeps threadstore independent of internal/engine.
func (e *AlreadyExistsError) ThreadAlreadyExists() bool { return true }
