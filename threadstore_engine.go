package adaptor

import (
	"context"
	"errors"
	"time"

	"github.com/agent-dance/agent-adaptor/v2/internal/engine"
	"github.com/agent-dance/agent-adaptor/v2/threadstore"
)

// threadNamespace is the fixed engine namespace under which the Thread path
// files every (key → session) mapping. The public Thread contract exposes one
// opaque host key, while the internal session coordinator requires a non-empty
// namespace. The adapter below supplies this constant internally and strips it
// before a request reaches threadstore.Store.
const threadNamespace = "thread"

// engineStore adapts a consumer-facing threadstore.Store onto the engine's
// SessionStore port so PrepareThreadSession/Persist can drive it. The
// mapping is purely mechanical: namespace dropped on the way out, restored
// to threadNamespace on the way in; every other field carries over 1:1.
// Keeping the adapter in the root package preserves the dependency direction:
// neither the internal engine nor threadstore imports adaptor.
type engineStore struct {
	store      threadstore.Store
	messages   *messageHub
	messageKey string
	runtime    *threadRuntime
	state      *threadRunState
	sink       *eventSink
}

type threadRunState struct {
	record   *threadstore.Record
	turns    int
	guidance map[string]bool
}

var _ engine.SessionStore = engineStore{}

func (s engineStore) Resolve(ctx context.Context, q engine.SessionQuery) (*engine.SessionRecord, error) {
	query := threadstore.Query{
		ID:              q.ID,
		Key:             q.Key,
		IncludeArchived: q.IncludeArchived,
	}
	var rec *threadstore.Record
	var err error
	if s.runtime != nil {
		s.runtime.mu.Lock()
		if pending := s.runtime.pending; pending != nil && (q.Key == pending.Key || q.ID == pending.Record.ID) {
			copyRecord := pending.Record
			rec = &copyRecord
		} else {
			rec, err = s.store.Resolve(ctx, query)
			if q.Key == s.messageKey && err == nil {
				s.runtime.durableID = ""
				s.runtime.durableVersion = engine.StableHash(rec)
				if rec != nil {
					s.runtime.durableID = rec.ID
				}
			}
		}
		s.runtime.mu.Unlock()
	} else {
		rec, err = s.store.Resolve(ctx, query)
	}
	if err != nil || rec == nil {
		return nil, err
	}
	if s.messages != nil && rec.Key == s.messageKey {
		s.messages.load(rec.DeliveredMessages)
	}
	if s.state != nil {
		s.state.record = rec
	}
	out := engineRecord(*rec)
	return &out, nil
}

func (s engineStore) Finalize(ctx context.Context, req engine.SessionFinalizeRequest) error {
	leases := make([]threadstore.Lease, 0, len(req.HeldLeases))
	for _, lease := range req.HeldLeases {
		leases = append(leases, threadstore.Lease{Target: lease.Target, Owner: lease.Owner, Token: lease.Token})
	}
	record := storeRecord(req.Record)
	if s.messages != nil {
		record.DeliveredMessages = s.messages.deliveryReceipts()
	}
	if s.state != nil {
		s.updateHistory(&record)
	}
	request := threadstore.FinalizeRequest{
		Record:           record,
		PreviousID:       req.PreviousID,
		Key:              req.Key,
		HeldLeases:       leases,
		ArchiveOld:       req.ArchiveOld,
		RebindActive:     req.RebindActive,
		RequireKeyAbsent: req.RequireKeyAbsent,
	}
	if s.runtime == nil {
		return s.store.Finalize(ctx, request)
	}
	r := s.runtime
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.ownerCtx.Err() != nil || r.ownershipErr != nil {
		return threadstore.ErrLeaseLost
	}
	if r.pending != nil {
		seen := make(map[string]bool, len(request.HeldLeases))
		for _, lease := range request.HeldLeases {
			seen[lease.Target] = true
		}
		for _, lease := range r.pending.HeldLeases {
			if !seen[lease.Target] {
				request.HeldLeases = append(request.HeldLeases, lease)
				seen[lease.Target] = true
			}
		}
		request.RequireKeyAbsent = request.RequireKeyAbsent || r.pending.RequireKeyAbsent
		request.PreviousID = r.durableID
		request.ArchiveOld = r.durableID != "" && r.durableID != record.ID
		r.pending = &request
	} else if err := s.store.Finalize(ctx, request); err != nil {
		if ctx.Err() != nil || errors.Is(err, threadstore.ErrLeaseLost) || errors.Is(err, threadstore.ErrAlreadyExists) || errors.Is(err, threadstore.ErrBusy) {
			return err
		}
		r.pending = &request
	} else {
		r.durableID = record.ID
		r.durableVersion = engine.StableHash(&record)
	}
	s.state.record = &record
	if r.pending != nil {
		s.sink.push(Notice{Kind: "thread.persistence", Text: "Checkpoint persistence failed; retaining the newest checkpoint in process and retrying storage."})
		r.signal()
	}
	return nil
}

func (s engineStore) AcquireLease(ctx context.Context, target, owner string, ttl time.Duration) (engine.SessionLease, error) {
	if lease, ok, err := s.borrowedLease(target); ok || err != nil {
		return lease, err
	}
	lease, err := s.store.AcquireLease(ctx, target, owner, ttl)
	if err != nil {
		if errors.Is(err, threadstore.ErrBusy) {
			return engine.SessionLease{}, &engine.SessionBusyError{Target: target}
		}
		return engine.SessionLease{}, err
	}
	return engine.SessionLease{Target: lease.Target, Owner: lease.Owner, Token: lease.Token}, nil
}

func (s engineStore) RenewLease(ctx context.Context, lease engine.SessionLease, ttl time.Duration) error {
	if _, ok, err := s.borrowedLease(lease.Target); ok || err != nil {
		return err
	}
	return s.store.RenewLease(ctx, threadstore.Lease{Target: lease.Target, Owner: lease.Owner, Token: lease.Token}, ttl)
}

func (s engineStore) ReleaseLease(ctx context.Context, lease engine.SessionLease) error {
	if s.runtime != nil {
		s.runtime.mu.Lock()
		borrowed := s.runtime.owner != nil && s.runtime.owner.OwnershipLease().Target == lease.Target
		s.runtime.mu.Unlock()
		if borrowed {
			return nil
		}
	}
	return s.store.ReleaseLease(ctx, threadstore.Lease{Target: lease.Target, Owner: lease.Owner, Token: lease.Token})
}

func (s engineStore) borrowedLease(target string) (engine.SessionLease, bool, error) {
	if s.runtime == nil {
		return engine.SessionLease{}, false, nil
	}
	s.runtime.mu.Lock()
	defer s.runtime.mu.Unlock()
	if s.runtime.owner == nil || s.runtime.ownerCtx.Err() != nil || s.runtime.ownershipErr != nil {
		return engine.SessionLease{}, false, threadstore.ErrLeaseLost
	}
	lease := s.runtime.owner.OwnershipLease()
	return lease, lease.Target == target, nil
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
