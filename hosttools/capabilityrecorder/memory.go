package capabilityrecorder

import (
	"context"
	"reflect"
	"sort"
	"sync"
)

type memoryStore struct {
	mu         sync.RWMutex
	partitions map[Scope][]Record
}

// NewMemoryStore returns an empty concurrent Store with process-local retention.
// It has no persistence, retention limit or Close; the host owns its lifetime.
func NewMemoryStore() Store {
	return &memoryStore{partitions: make(map[Scope][]Record)}
}

func (s *memoryStore) Append(ctx context.Context, record Record) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !validRecord(record) {
		return errInvalidRecord
	}
	record = cloneRecord(record)
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	rows := s.partitions[record.Scope]
	i := sort.Search(len(rows), func(i int) bool { return rows[i].Sequence >= record.Sequence })
	if i < len(rows) && rows[i].Sequence == record.Sequence {
		if reflect.DeepEqual(rows[i], record) {
			return nil
		}
		return errConflict
	}
	// Check at the commit boundary after any allocation or search work.
	if len(rows) == cap(rows) {
		grown := make([]Record, len(rows), max(1, len(rows)*2))
		copy(grown, rows)
		rows = grown
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	rows = append(rows, Record{})
	copy(rows[i+1:], rows[i:])
	rows[i] = record
	s.partitions[record.Scope] = rows
	return nil
}

func (s *memoryStore) Query(ctx context.Context, q Query) (Page, error) {
	q, err := normalizeQuery(q)
	if err != nil {
		return Page{}, err
	}
	if err := ctx.Err(); err != nil {
		return Page{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := ctx.Err(); err != nil {
		return Page{}, err
	}
	rows := s.partitions[q.Scope]
	start := sort.Search(len(rows), func(i int) bool { return rows[i].Sequence > q.AfterSequence })
	end := min(len(rows), start+q.Limit)
	page := Page{Records: rows[start:end]}
	if end < len(rows) {
		page.NextSequence = rows[end-1].Sequence
	}
	page = clonePage(page)
	if err := ctx.Err(); err != nil {
		return Page{}, err
	}
	return page, nil
}
