package adaptor

import (
	"context"
	"time"

	"github.com/agent-dance/agent-adaptor/internal/engine"
	"github.com/agent-dance/agent-adaptor/threadstore"
)

// threadNamespace is the fixed engine namespace under which the Thread path
// files every (key → session) mapping. The legacy engine addresses sessions
// by a (Namespace, Key) pair; v1 collapses it to the host's single thread
// key (multi-tenant hosts compose "tenant/key" themselves), so one constant
// namespace satisfies the engine's non-empty requirement while the adapter
// below strips it before it reaches a threadstore.Store.
const threadNamespace = "thread"

// engineStore adapts a consumer-facing threadstore.Store onto the engine's
// SessionStore port so PrepareThreadSession/Persist can drive it. The
// mapping is purely mechanical: namespace dropped on the way out, restored
// to threadNamespace on the way in; every other field carries over 1:1.
// Living in next/ keeps the dependency arrow pointing outward — neither
// engine nor threadstore imports the other.
type engineStore struct {
	store threadstore.Store
}

var _ engine.SessionStore = engineStore{}

func (s engineStore) Resolve(ctx context.Context, q engine.SessionQuery) (*engine.SessionRecord, error) {
	rec, err := s.store.Resolve(ctx, threadstore.Query{
		ID:              q.ID,
		Key:             q.Key,
		IncludeArchived: q.IncludeArchived,
	})
	if err != nil || rec == nil {
		return nil, err
	}
	out := engineRecord(*rec)
	return &out, nil
}

func (s engineStore) Finalize(ctx context.Context, req engine.SessionFinalizeRequest) error {
	leases := make([]threadstore.Lease, 0, len(req.HeldLeases))
	for _, lease := range req.HeldLeases {
		leases = append(leases, threadstore.Lease{Target: lease.Target, Owner: lease.Owner, Token: lease.Token})
	}
	return s.store.Finalize(ctx, threadstore.FinalizeRequest{
		Record:           storeRecord(req.Record),
		PreviousID:       req.PreviousID,
		Key:              req.Key,
		HeldLeases:       leases,
		ArchiveOld:       req.ArchiveOld,
		RebindActive:     req.RebindActive,
		RequireKeyAbsent: req.RequireKeyAbsent,
	})
}

func (s engineStore) AcquireLease(ctx context.Context, target, owner string, ttl time.Duration) (engine.SessionLease, error) {
	lease, err := s.store.AcquireLease(ctx, target, owner, ttl)
	if err != nil {
		return engine.SessionLease{}, err
	}
	return engine.SessionLease{Target: lease.Target, Owner: lease.Owner, Token: lease.Token}, nil
}

func (s engineStore) RenewLease(ctx context.Context, lease engine.SessionLease, ttl time.Duration) error {
	return s.store.RenewLease(ctx, threadstore.Lease{Target: lease.Target, Owner: lease.Owner, Token: lease.Token}, ttl)
}

func (s engineStore) ReleaseLease(ctx context.Context, lease engine.SessionLease) error {
	return s.store.ReleaseLease(ctx, threadstore.Lease{Target: lease.Target, Owner: lease.Owner, Token: lease.Token})
}

// engineRecord maps a store record into the engine shape, restoring the
// fixed thread namespace.
func engineRecord(rec threadstore.Record) engine.SessionRecord {
	return engine.SessionRecord{
		ID:                       rec.ID,
		Namespace:                threadNamespace,
		Key:                      rec.Key,
		Status:                   engine.SessionStatus(rec.Status),
		DriverType:               rec.DriverType,
		Agent:                    rec.Agent,
		Fingerprint:              rec.Fingerprint,
		CompatibilityFingerprint: rec.CompatibilityFingerprint,
		SessionCodec:             rec.SessionCodec,
		DriverState:              rec.State,
		CreatedAt:                rec.CreatedAt,
		UpdatedAt:                rec.UpdatedAt,
	}
}

// storeRecord maps an engine record into the store shape, dropping the
// fixed thread namespace.
func storeRecord(rec engine.SessionRecord) threadstore.Record {
	return threadstore.Record{
		ID:                       rec.ID,
		Key:                      rec.Key,
		Status:                   threadstore.Status(rec.Status),
		DriverType:               rec.DriverType,
		Agent:                    rec.Agent,
		Fingerprint:              rec.Fingerprint,
		CompatibilityFingerprint: rec.CompatibilityFingerprint,
		SessionCodec:             rec.SessionCodec,
		State:                    rec.DriverState,
		CreatedAt:                rec.CreatedAt,
		UpdatedAt:                rec.UpdatedAt,
	}
}
