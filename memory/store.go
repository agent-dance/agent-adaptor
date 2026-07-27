package memory

import (
	"context"
	"sync"
	"time"

	"github.com/agent-dance/agent-adaptor/threadstore"
)

// Store is an in-memory implementation of threadstore.Store — the v1 Thread
// counterpart of SessionStore. It is useful for tests, local CLIs, demos,
// and single-process hosts that do not need thread state to survive process
// restarts. Production services that run multiple processes should provide
// a durable/centralized store instead.
//
// The package intentionally ships both implementations side by side: the
// legacy SessionStore keeps serving the root-package session API unchanged,
// while Store backs adaptor.WithThreadStore. (One type cannot satisfy both
// interfaces — the method names collide on different parameter types.)
type Store struct {
	mu       sync.Mutex
	records  map[string]threadstore.Record
	keyIndex map[string]string
	leases   map[string]leaseRecord
}

var _ threadstore.Store = (*Store)(nil)

// NewStore constructs an empty in-memory threadstore.Store.
func NewStore() *Store {
	return &Store{
		records:  map[string]threadstore.Record{},
		keyIndex: map[string]string{},
		leases:   map[string]leaseRecord{},
	}
}

// Resolve looks up a record by internal ID or by thread key. Missing records
// resolve to (nil, nil); archived records require q.IncludeArchived.
func (s *Store) Resolve(_ context.Context, q threadstore.Query) (*threadstore.Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if q.ID != "" {
		return s.recordByIDLocked(q.ID, q.IncludeArchived), nil
	}
	if q.Key == "" {
		return nil, nil
	}
	id := s.keyIndex[q.Key]
	if id == "" {
		return nil, nil
	}
	return s.recordByIDLocked(id, q.IncludeArchived), nil
}

func (s *Store) recordByIDLocked(id string, includeArchived bool) *threadstore.Record {
	record, ok := s.records[id]
	if !ok {
		return nil
	}
	if record.Status == threadstore.StatusArchived && !includeArchived {
		return nil
	}
	copyRecord := cloneThreadRecord(record)
	return &copyRecord
}

// Finalize persists the post-run record and performs archive/rebind
// operations after validating all held leases.
func (s *Store) Finalize(_ context.Context, req threadstore.FinalizeRequest) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().UTC()
	for _, lease := range req.HeldLeases {
		current, ok := s.leases[lease.Target]
		if !ok || current.Owner != lease.Owner || current.Token != lease.Token || !current.Until.After(now) {
			return &threadstore.LeaseLostError{Target: lease.Target}
		}
	}
	if req.RequireKeyAbsent && req.Key != "" {
		if existingID := s.keyIndex[req.Key]; existingID != "" {
			return &threadstore.AlreadyExistsError{Key: req.Key}
		}
	}

	copyRecord := cloneThreadRecord(req.Record)
	s.records[copyRecord.ID] = copyRecord

	if req.ArchiveOld && req.PreviousID != "" {
		record, ok := s.records[req.PreviousID]
		if ok {
			record.Status = threadstore.StatusArchived
			record.UpdatedAt = now
			s.records[req.PreviousID] = record
		}
	}

	if req.RebindActive && req.Key != "" {
		s.keyIndex[req.Key] = req.Record.ID
	}
	return nil
}

func cloneThreadRecord(record threadstore.Record) threadstore.Record {
	copyRecord := record
	if record.State != nil {
		state := *record.State
		if record.State.Data != nil {
			state.Data = make(map[string]string, len(record.State.Data))
			for key, value := range record.State.Data {
				state.Data[key] = value
			}
		}
		copyRecord.State = &state
	}
	return copyRecord
}

// AcquireLease obtains or renews exclusive use of target for owner until ttl
// elapses. A valid lease held by a different owner yields a BusyError.
func (s *Store) AcquireLease(_ context.Context, target, owner string, ttl time.Duration) (threadstore.Lease, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	current, ok := s.leases[target]
	if ok && current.Until.After(now) && current.Owner != owner {
		return threadstore.Lease{}, &threadstore.BusyError{Target: target}
	}
	token := current.Token
	if token == "" || !ok || !current.Until.After(now) {
		token = newLeaseToken()
	}
	s.leases[target] = leaseRecord{
		Owner: owner,
		Until: now.Add(ttl),
		Token: token,
	}
	return threadstore.Lease{
		Target: target,
		Owner:  owner,
		Token:  token,
	}, nil
}

// RenewLease extends a lease if the caller still owns the matching token.
func (s *Store) RenewLease(_ context.Context, lease threadstore.Lease, ttl time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.leases[lease.Target]
	if !ok || current.Owner != lease.Owner || current.Token != lease.Token || !current.Until.After(time.Now().UTC()) {
		return &threadstore.LeaseLostError{Target: lease.Target}
	}
	current.Until = time.Now().UTC().Add(ttl)
	s.leases[lease.Target] = current
	return nil
}

// ReleaseLease releases a lease when the caller owns the matching token.
// Lost or already-expired leases are ignored to keep cleanup idempotent.
func (s *Store) ReleaseLease(_ context.Context, lease threadstore.Lease) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.leases[lease.Target]
	if !ok {
		return nil
	}
	if current.Owner != lease.Owner || current.Token != lease.Token {
		return nil
	}
	delete(s.leases, lease.Target)
	return nil
}
