package memory

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"

	agentadaptor "github.com/agent-dance/agent-adaptor"
)

type leaseRecord struct {
	Owner string
	Until time.Time
	Token string
}

type SessionStore struct {
	mu       sync.Mutex
	records  map[string]agentadaptor.SessionRecord
	keyIndex map[string]string
	leases   map[string]leaseRecord
}

func NewSessionStore() *SessionStore {
	return &SessionStore{
		records:  map[string]agentadaptor.SessionRecord{},
		keyIndex: map[string]string{},
		leases:   map[string]leaseRecord{},
	}
}

func (s *SessionStore) Resolve(_ context.Context, q agentadaptor.SessionQuery) (*agentadaptor.SessionRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if q.ID != "" {
		record, ok := s.records[q.ID]
		if !ok {
			return nil, nil
		}
		if record.Status == agentadaptor.SessionStatusArchived && !q.IncludeArchived {
			return nil, nil
		}
		copyRecord := record
		return &copyRecord, nil
	}
	if q.Namespace == "" || q.Key == "" {
		return nil, nil
	}
	indexKey := q.Namespace + ":" + q.Key
	id := s.keyIndex[indexKey]
	if id == "" {
		return nil, nil
	}
	record, ok := s.records[id]
	if !ok {
		return nil, nil
	}
	if record.Status == agentadaptor.SessionStatusArchived && !q.IncludeArchived {
		return nil, nil
	}
	copyRecord := record
	return &copyRecord, nil
}

func (s *SessionStore) Finalize(_ context.Context, req agentadaptor.SessionFinalizeRequest) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().UTC()
	for _, lease := range req.HeldLeases {
		current, ok := s.leases[lease.Target]
		if !ok || current.Owner != lease.Owner || current.Token != lease.Token || !current.Until.After(now) {
			return &agentadaptor.SessionLeaseLostError{Target: lease.Target}
		}
	}

	copyRecord := req.Record
	s.records[copyRecord.ID] = copyRecord

	if req.ArchiveOld && req.PreviousID != "" {
		record, ok := s.records[req.PreviousID]
		if ok {
			record.Status = agentadaptor.SessionStatusArchived
			record.UpdatedAt = now
			s.records[req.PreviousID] = record
		}
	}

	if req.RebindActive && req.Namespace != "" && req.Key != "" {
		s.keyIndex[req.Namespace+":"+req.Key] = req.Record.ID
	}
	return nil
}

func (s *SessionStore) AcquireLease(_ context.Context, sessionID, owner string, ttl time.Duration) (agentadaptor.SessionLease, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	current, ok := s.leases[sessionID]
	if ok && current.Until.After(now) && current.Owner != owner {
		return agentadaptor.SessionLease{}, &agentadaptor.SessionBusyError{Target: sessionID}
	}
	token := current.Token
	if token == "" || !ok || !current.Until.After(now) {
		token = newLeaseToken()
	}
	s.leases[sessionID] = leaseRecord{
		Owner: owner,
		Until: now.Add(ttl),
		Token: token,
	}
	return agentadaptor.SessionLease{
		Target: sessionID,
		Owner:  owner,
		Token:  token,
	}, nil
}

func (s *SessionStore) RenewLease(_ context.Context, lease agentadaptor.SessionLease, ttl time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.leases[lease.Target]
	if !ok || current.Owner != lease.Owner || current.Token != lease.Token || !current.Until.After(time.Now().UTC()) {
		return &agentadaptor.SessionLeaseLostError{Target: lease.Target}
	}
	current.Until = time.Now().UTC().Add(ttl)
	s.leases[lease.Target] = current
	return nil
}

func (s *SessionStore) ReleaseLease(_ context.Context, lease agentadaptor.SessionLease) error {
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
