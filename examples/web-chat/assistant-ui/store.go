package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/agent-dance/agent-adaptor/memory"
	"github.com/agent-dance/agent-adaptor/threadstore"
)

// diskStore is a single-process example Store. Leases use the SDK memory
// implementation; complete record/index snapshots use fsync + atomic rename.
// One process must exclusively own the data directory. It is not a database.
type diskStore struct {
	mu       sync.Mutex
	path     string
	records  map[string]threadstore.Record
	leases   *memory.Store
	poisoned error
}

func openDiskStore(dir string) (*diskStore, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	s := &diskStore{path: filepath.Join(dir, "threads.json"), records: map[string]threadstore.Record{}, leases: memory.NewStore()}
	f, err := os.Open(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	d := json.NewDecoder(f)
	d.DisallowUnknownFields()
	if err = d.Decode(&s.records); err != nil {
		return nil, fmt.Errorf("checkpoint store: %w", err)
	}
	if err = d.Decode(new(any)); !errors.Is(err, io.EOF) {
		return nil, errors.New("checkpoint store: trailing JSON")
	}
	if s.records == nil {
		return nil, errors.New("checkpoint store: null records")
	}
	active := map[string]bool{}
	for id, r := range s.records {
		if id == "" || r.ID != id || r.Key == "" || (r.Status != threadstore.StatusActive && r.Status != threadstore.StatusArchived) {
			return nil, errors.New("checkpoint store: invalid record")
		}
		if r.Status == threadstore.StatusActive {
			if active[r.Key] {
				return nil, errors.New("checkpoint store: duplicate active key")
			}
			active[r.Key] = true
		}
	}
	return s, nil
}

func cloneRecord(r threadstore.Record) threadstore.Record {
	if r.State != nil {
		state := *r.State
		state.Data = make(map[string]string, len(r.State.Data))
		for k, v := range r.State.Data {
			state.Data[k] = v
		}
		r.State = &state
	}
	return r
}
func (s *diskStore) Resolve(ctx context.Context, q threadstore.Query) (*threadstore.Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.check(ctx); err != nil {
		return nil, err
	}
	for _, r := range s.records {
		if (q.ID != "" && r.ID == q.ID || q.ID == "" && q.Key != "" && r.Key == q.Key && r.Status == threadstore.StatusActive) && (q.IncludeArchived || r.Status != threadstore.StatusArchived) {
			c := cloneRecord(r)
			return &c, nil
		}
	}
	return nil, nil
}
func (s *diskStore) check(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.poisoned
}
func (s *diskStore) Finalize(ctx context.Context, req threadstore.FinalizeRequest) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.check(ctx); err != nil {
		return err
	}
	// Finalize on the lease-only memory store validates owner, token and expiry.
	// Its records are never read. The durable mutation below remains under mu.
	if err := s.leases.Finalize(ctx, threadstore.FinalizeRequest{HeldLeases: req.HeldLeases}); err != nil {
		return err
	}
	if req.RequireKeyAbsent {
		for _, r := range s.records {
			if r.Key == req.Key && r.Status == threadstore.StatusActive {
				return &threadstore.AlreadyExistsError{Key: req.Key}
			}
		}
	}
	next := make(map[string]threadstore.Record, len(s.records)+1)
	for k, v := range s.records {
		next[k] = v
	}
	next[req.Record.ID] = cloneRecord(req.Record)
	if req.ArchiveOld && req.PreviousID != "" {
		if old, ok := next[req.PreviousID]; ok {
			old.Status = threadstore.StatusArchived
			old.UpdatedAt = time.Now().UTC()
			next[old.ID] = old
		}
	}
	// SDK finalization rebinds an active key only with archive or a new record.
	for id, r := range next {
		if id != req.Record.ID && r.Status == threadstore.StatusActive && r.Key == req.Record.Key && req.Record.Status == threadstore.StatusActive {
			return errors.New("checkpoint store: multiple active records")
		}
	}
	b, err := json.Marshal(next)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(s.path), ".threads-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(b); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = os.Rename(f.Name(), s.path); err != nil {
		return err
	}
	// A post-rename directory sync failure leaves uncertain durability. Fail
	// closed for the rest of this process instead of serving stale memory.
	dir, err := os.Open(filepath.Dir(s.path))
	if err == nil {
		err = dir.Sync()
		e := dir.Close()
		if err == nil {
			err = e
		}
	}
	if err != nil {
		s.poisoned = fmt.Errorf("checkpoint store directory sync: %w", err)
		return s.poisoned
	}
	s.records = next
	return nil
}
func (s *diskStore) AcquireLease(ctx context.Context, target, owner string, ttl time.Duration) (threadstore.Lease, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.check(ctx); err != nil {
		return threadstore.Lease{}, err
	}
	return s.leases.AcquireLease(ctx, target, owner, ttl)
}
func (s *diskStore) RenewLease(ctx context.Context, l threadstore.Lease, ttl time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.check(ctx); err != nil {
		return err
	}
	return s.leases.RenewLease(ctx, l, ttl)
}
func (s *diskStore) ReleaseLease(ctx context.Context, l threadstore.Lease) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.leases.ReleaseLease(ctx, l)
}

var _ threadstore.Store = (*diskStore)(nil)
