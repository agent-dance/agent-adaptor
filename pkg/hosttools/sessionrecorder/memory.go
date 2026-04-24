package sessionrecorder

import (
	"context"
	"sort"
	"sync"
)

// NewMemoryBackend returns a Backend that keeps records in process
// memory. It is intended for tests, ephemeral CLIs, or single-process
// hosts that do not need cross-restart recovery. For durable storage
// use NewJSONLBackend or plug your own.
func NewMemoryBackend() Backend {
	return &memoryBackend{sessions: map[string][]Record{}}
}

type memoryBackend struct {
	mu       sync.Mutex
	sessions map[string][]Record
}

func (b *memoryBackend) Load(_ context.Context, key string) ([]Record, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	src := b.sessions[key]
	if len(src) == 0 {
		return nil, nil
	}
	out := make([]Record, len(src))
	copy(out, src)
	return out, nil
}

func (b *memoryBackend) Append(_ context.Context, key string, r Record) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.sessions[key] = append(b.sessions[key], r)
	return nil
}

func (b *memoryBackend) Sessions(_ context.Context) ([]SessionInfo, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]SessionInfo, 0, len(b.sessions))
	for k, recs := range b.sessions {
		if len(recs) == 0 {
			continue
		}
		last := recs[len(recs)-1]
		out = append(out, SessionInfo{
			Key:        k,
			LastSeq:    last.HostSeq,
			RecordedAt: last.RecordedAt,
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].RecordedAt.After(out[j].RecordedAt)
	})
	return out, nil
}

func (b *memoryBackend) Close() error { return nil }
