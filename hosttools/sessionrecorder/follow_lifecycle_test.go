package sessionrecorder

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	adaptor "github.com/agent-dance/agent-adaptor"
)

type commitBarrierBackend struct {
	EventBackend
	entered, release chan struct{}
	fail             bool
}

func (b *commitBarrierBackend) Append(ctx context.Context, key string, rec EventRecord) error {
	close(b.entered)
	select {
	case <-b.release:
	case <-ctx.Done():
		return ctx.Err()
	}
	if b.fail {
		return errors.New("durability failed")
	}
	return b.EventBackend.Append(ctx, key, rec)
}

func TestFollowerNotifiesOnlyAfterSuccessfulCommit(t *testing.T) {
	for _, fail := range []bool{false, true} {
		backend := &commitBarrierBackend{EventBackend: NewMemoryEventBackend(), entered: make(chan struct{}), release: make(chan struct{}), fail: fail}
		rec := NewEventRecorder(backend)
		changes, stop, err := rec.(Follower).Subscribe(context.Background(), "thread")
		if err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() {
			_, err := rec.Record(context.Background(), "thread", adaptor.Notice{Kind: "test"})
			done <- err
		}()
		<-backend.entered
		select {
		case <-changes:
			t.Fatal("notified before commit")
		default:
		}
		close(backend.release)
		err = <-done
		if (err != nil) != fail {
			t.Fatal(err)
		}
		if fail {
			select {
			case <-changes:
				t.Fatal("failed append notified")
			default:
			}
		} else {
			select {
			case <-changes:
			case <-time.After(time.Second):
				t.Fatal("missing committed notification")
			}
		}
		records, err := rec.Since(context.Background(), "thread", 0)
		if err != nil || len(records) != map[bool]int{false: 1, true: 0}[fail] {
			t.Fatal(records, err)
		}
		stop()
		if err := rec.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

// This backend, rather than either recorder, owns allocation and durable state.
// Separate recorder instances must see each other's commits and external writes.
type atomicTestBackend struct {
	mu            sync.Mutex
	records       map[string][]EventRecord
	notifications notifications
	fail          bool
}

func (b *atomicTestBackend) Load(ctx context.Context, key string) ([]EventRecord, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]EventRecord(nil), b.records[key]...), nil
}
func (b *atomicTestBackend) Append(context.Context, string, EventRecord) error {
	panic("non-atomic Append used")
}
func (b *atomicTestBackend) AppendNext(ctx context.Context, key string, rec EventRecord) (EventRecord, error) {
	if err := ctx.Err(); err != nil {
		return EventRecord{}, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.fail {
		return EventRecord{}, errors.New("durable append failed")
	}
	if b.records == nil {
		b.records = make(map[string][]EventRecord)
	}
	rec.HostSeq = uint64(len(b.records[key]) + 1)
	b.records[key] = append(b.records[key], rec)
	b.notifications.notify(key)
	return rec, nil
}
func (b *atomicTestBackend) Subscribe(ctx context.Context, key string) (<-chan struct{}, func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	return b.notifications.subscribe(ctx, key)
}
func (b *atomicTestBackend) Sessions(context.Context) ([]SessionInfo, error) { return nil, nil }
func (b *atomicTestBackend) Close() error                                    { return nil }

func TestCoordinatedBackendOwnsSequenceCommitAndNotification(t *testing.T) {
	backend := &atomicTestBackend{}
	at := time.Date(2026, 10, 31, 1, 2, 3, 0, time.UTC)
	a := NewEventRecorder(backend, WithEventClock(func() time.Time { return at }))
	b := NewEventRecorder(backend)
	defer a.Close()
	defer b.Close()
	changes, stop, err := b.(Follower).Subscribe(context.Background(), "thread")
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	// Prime the reader's empty view before another worker writes.
	if records, err := b.Since(context.Background(), "thread", 0); err != nil || len(records) != 0 {
		t.Fatal(records, err)
	}
	backend.mu.Lock()
	backend.fail = true
	backend.mu.Unlock()
	if _, err := a.Record(context.Background(), "thread", adaptor.Notice{Kind: "test"}); err == nil {
		t.Fatal("swallowed storage failure")
	}
	select {
	case <-changes:
		t.Fatal("failed commit notified")
	default:
	}
	backend.mu.Lock()
	backend.fail = false
	backend.mu.Unlock()
	rec, err := a.Record(context.Background(), "thread", adaptor.Notice{Kind: "test"})
	if err != nil || rec.HostSeq != 1 || rec.RecordedAt != at {
		t.Fatal(rec, err)
	}
	select {
	case <-changes:
	case <-time.After(time.Second):
		t.Fatal("missing cross-worker notification")
	}
	records, err := b.Since(context.Background(), "thread", 0)
	if err != nil || len(records) != 1 || !reflect.DeepEqual(records[0], rec) {
		t.Fatal(records, err)
	}
	external, err := backend.AppendNext(context.Background(), "thread", EventRecord{Event: adaptor.Notice{Kind: "external"}, RecordedAt: at})
	if err != nil || external.HostSeq != 2 {
		t.Fatal(external, err)
	}
	select {
	case <-changes:
	case <-time.After(time.Second):
		t.Fatal("missing external commit notification")
	}
	records, err = b.Since(context.Background(), "thread", 1)
	if err != nil || len(records) != 1 || records[0].HostSeq != 2 {
		t.Fatal(records, err)
	}
}

func TestRecorderSubscriptionLifecycle(t *testing.T) {
	for _, coordinated := range []bool{false, true} {
		var backend EventBackend = NewMemoryEventBackend()
		if coordinated {
			backend = &atomicTestBackend{}
		}
		rec := NewEventRecorder(backend)
		follower := rec.(Follower)
		ctx, cancel := context.WithCancel(context.Background())
		changes, stop, err := follower.Subscribe(ctx, "thread")
		if err != nil {
			t.Fatal(err)
		}
		cancel()
		select {
		case _, ok := <-changes:
			if ok {
				t.Fatal("unexpected notification")
			}
		case <-time.After(time.Second):
			t.Fatal("context cancellation leaked subscription")
		}
		stop()
		stop()
		if _, _, err := follower.Subscribe(ctx, "thread"); !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
		changes, stop, err = follower.Subscribe(context.Background(), "thread")
		if err != nil {
			t.Fatal(err)
		}
		if err := rec.Close(); err != nil {
			t.Fatal(err)
		}
		select {
		case _, ok := <-changes:
			if ok {
				t.Fatal("unexpected notification")
			}
		case <-time.After(time.Second):
			t.Fatal("Close leaked subscription")
		}
		stop()
		stop()
		if _, _, err := follower.Subscribe(context.Background(), "thread"); err == nil {
			t.Fatal("subscribed after close")
		}
		if _, err := rec.Record(context.Background(), "thread", adaptor.Notice{}); err == nil {
			t.Fatal("recorded after close")
		}
		if _, err := rec.Since(context.Background(), "thread", 0); err == nil {
			t.Fatal("read after close")
		}
		if _, err := rec.Sessions(context.Background()); err == nil {
			t.Fatal("listed after close")
		}
		if err := rec.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestFollowerConcurrentStopNotifyAndClose(t *testing.T) {
	rec := NewEventRecorder(NewMemoryEventBackend())
	const count = 100
	var wg sync.WaitGroup
	for i := 0; i < count; i++ {
		ch, stop, err := rec.(Follower).Subscribe(context.Background(), "thread")
		if err != nil {
			t.Fatal(err)
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			stop()
			stop()
			for range ch {
			}
		}()
	}
	for i := 0; i < 100; i++ {
		if _, err := rec.Record(context.Background(), "thread", adaptor.Notice{Kind: "test"}); err != nil {
			t.Fatal(err)
		}
	}
	wg.Wait()
	if err := rec.Close(); err != nil {
		t.Fatal(err)
	}
	r := rec.(*eventRecorder)
	r.notifications.mu.Lock()
	defer r.notifications.mu.Unlock()
	if len(r.notifications.watchers) != 0 {
		t.Fatal("retained cancelled subscriptions")
	}
}
