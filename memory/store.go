package memory

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"

	"github.com/agent-dance/agent-adaptor/threadstore"
)

type leaseRecord struct {
	Owner string
	Until time.Time
	Token string
}

// Store is an in-memory implementation of threadstore.Store. It is useful for
// tests, local CLIs, demos, and single-process hosts that do not need thread
// state to survive process restarts. Production services that run multiple
// processes should provide a durable/centralized store instead.
//
// The memory package intentionally depends only on the public threadstore
// contract. The legacy root-package SessionStore implementation was removed
// during the v1 cutover so this package cannot pull the old SDK surface back
// into production dependency graphs.
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

func newLeaseToken() string {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return hex.EncodeToString([]byte(time.Now().UTC().Format(time.RFC3339Nano)))
	}
	return hex.EncodeToString(buf[:])
}
