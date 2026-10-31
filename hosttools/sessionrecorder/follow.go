package sessionrecorder

import (
	"context"
	"sync"
)

// Follower provides level-triggered change notifications. Subscribe must become
// effective before returning. Notifications may coalesce: callers subscribe
// before Since, then read again on notification. Cancel releases the subscription.
// Cancellation must close the channel and release resources. A closed channel
// means the subscription ended, not that a run completed. An already-cancelled
// context returns an error. Implementations must be safe for concurrent use.
type Follower interface {
	// Subscribe installs a cancellable change subscription before returning.
	Subscribe(context.Context, string) (<-chan struct{}, func(), error)
}

// CoordinatedEventBackend supports multi-process recording. AppendNext must
// atomically allocate a nonzero, strictly increasing per-key HostSeq and durably
// append the supplied event/RecordedAt unchanged; failures must not publish a
// record or notification. Load must return fresh committed data in HostSeq order.
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
		})
	}
	stopContext := context.AfterFunc(ctx, cancel)
	return ch, func() { stopContext(); cancel() }, nil
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
	if err := r.checkOpen(ctx); err != nil {
		return nil, nil, err
	}
	if err := r.checkKey(key); err != nil {
		return nil, nil, err
	}
	// Tie backend subscriptions to both caller and recorder lifetime. AfterFunc
	// avoids a permanently waiting goroutine for each idle subscription.
	followCtx, cancel := context.WithCancel(ctx)
	stopClose := context.AfterFunc(r.followCtx, cancel)
	var ch <-chan struct{}
	var stop func()
	var err error
	if backend, ok := r.backend.(CoordinatedEventBackend); ok {
		ch, stop, err = backend.Subscribe(followCtx, key)
	} else {
		ch, stop, err = r.notifications.subscribe(followCtx, key)
	}
	if err != nil {
		stopClose()
		cancel()
		return nil, nil, err
	}
	var once sync.Once
	release := func() {
		once.Do(func() {
			stopClose()
			cancel()
			stop()
		})
	}
	stopContext := context.AfterFunc(followCtx, release)
	return ch, func() { stopContext(); release() }, nil
}
