package sessionrecorder

import (
	"context"
	"sync"
)

// Follower provides level-triggered change notifications. Subscribe must become
// effective before returning. Notifications may coalesce: callers subscribe
// before Since, then read again on notification. Cancel releases the subscription.
// A closed channel means the subscription ended, not that a run completed.
type Follower interface {
	// Subscribe installs a cancellable change subscription before returning.
	Subscribe(context.Context, string) (<-chan struct{}, func(), error)
}

// CoordinatedEventBackend supports multi-process recording. AppendNext must
// atomically allocate HostSeq and durably append; Load must read committed data.
// Subscribe must notify after commit, including writes by other processes.
// Implementations also implement EventBackend; ordinary backends are unchanged.
type CoordinatedEventBackend interface {
	EventBackend
	Follower
	// AppendNext allocates a sequence and commits the supplied event atomically.
	AppendNext(context.Context, string, EventRecord) (EventRecord, error)
}

type notifications struct {
	mu       sync.Mutex
	next     uint64
	watchers map[string]map[uint64]chan struct{}
}

func (n *notifications) subscribe(ctx context.Context, key string) (<-chan struct{}, func(), error) {
	n.mu.Lock()
	if n.watchers == nil {
		n.watchers = make(map[string]map[uint64]chan struct{})
	}
	if n.watchers[key] == nil {
		n.watchers[key] = make(map[uint64]chan struct{})
	}
	n.next++
	id := n.next
	ch := make(chan struct{}, 1)
	n.watchers[key][id] = ch
	n.mu.Unlock()
	stop := make(chan struct{})
	var once sync.Once
	cancel := func() {
		once.Do(func() {
			n.mu.Lock()
			delete(n.watchers[key], id)
			if len(n.watchers[key]) == 0 {
				delete(n.watchers, key)
			}
			close(ch)
			n.mu.Unlock()
			close(stop)
		})
	}
	go func() {
		select {
		case <-ctx.Done():
			cancel()
		case <-stop:
		}
	}()
	return ch, cancel, nil
}

func (n *notifications) notify(key string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	for _, ch := range n.watchers[key] {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// Subscribe watches committed changes to a recorder key without blocking writers.
func (r *eventRecorder) Subscribe(ctx context.Context, key string) (<-chan struct{}, func(), error) {
	if err := r.checkKey(key); err != nil {
		return nil, nil, err
	}
	if backend, ok := r.backend.(CoordinatedEventBackend); ok {
		return backend.Subscribe(ctx, key)
	}
	return r.notifications.subscribe(ctx, key)
}
