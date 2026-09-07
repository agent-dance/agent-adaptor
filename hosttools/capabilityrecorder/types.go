package capabilityrecorder

import (
	"context"
	"errors"
	"time"

	"github.com/agent-dance/agent-adaptor/capability"
)

// Scope is an exact partition, including all three identity dimensions. Empty
// identity fields mean unconfigured; RunID must be nonempty. Values are opaque
// and are never trimmed, joined with delimiters or replaced by display names.
type Scope struct {
	IdentityID string
	Tenant     string
	Profile    string
	RunID      string
}

// Record is the closed projection of one accepted CapabilityInvocation. Sequence
// and Time come from the authoritative Event meta; gaps in Sequence are normal.
// It contains no Thread key, identity name, source envelope or arbitrary metadata.
type Record struct {
	Scope      Scope
	Sequence   uint64
	Time       time.Time
	Invocation capability.Invocation
}

// Query selects exactly one Scope. AfterSequence is an exclusive lower bound.
// Limit=0 means 100; 1..1000 are valid. There is no cross-scope scan.
type Query struct {
	Scope         Scope
	AfterSequence uint64
	Limit         int
}

// Page holds records in ascending Sequence order. NextSequence is the last
// returned Sequence when more records currently exist, otherwise zero. Zero
// does not mean the run ended or that its observation history is complete.
// To poll a live run, retain the last received Sequence even when NextSequence=0.
type Page struct {
	Records      []Record
	NextSequence uint64
}

// Store is host-owned and must support concurrent runs and queries. Append is
// atomic and idempotent by (Scope, Sequence): identical values are a no-op;
// conflicting values fail without replacing the original. Successful Append
// must be immediately visible to Query. Both methods return independent copies
// and Append must copy its input before retaining it.
//
// Implementations must check context before committing and honor cancellation.
// Core abandons an observer after 100ms (or remaining cleanup time); it cannot
// undo a custom Store's side effects after cancellation. Query must enforce the
// exact scope, cursor and page bounds, including on a run still in progress.
// Store has no SDK-owned Close. Hosts close their own optional io.Closer.
type Store interface {
	Append(context.Context, Record) error
	Query(context.Context, Query) (Page, error)
}

// Config requires an explicit Store. No implicit persistence or memory fallback
// is installed when Store is absent.
type Config struct{ Store Store }

var (
	// ErrInvalidQuery identifies a missing RunID or a limit outside 0..1000.
	ErrInvalidQuery = errors.New("capabilityrecorder: invalid query")
	// ErrStoreRequired identifies a missing (including typed-nil) Store.
	ErrStoreRequired = errors.New("capabilityrecorder: store required")
)
