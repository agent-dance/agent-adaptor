package sessionrecorder_test

import (
	"context"
	"sync"
	"testing"
	"time"

	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/hosttools/sessionrecorder"
)

func TestFollowerConcurrentRecordAndCatchup(t *testing.T) {
	rec := sessionrecorder.NewEventRecorder(sessionrecorder.NewMemoryEventBackend())
	defer rec.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	changes, stop, err := rec.(sessionrecorder.Follower).Subscribe(ctx, "thread")
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	var workers sync.WaitGroup
	for i := 0; i < 8; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for n := 0; n < 20; n++ {
				if _, err := rec.Record(ctx, "thread", adaptor.Notice{Kind: "test"}); err != nil {
					t.Error(err)
				}
			}
		}()
	}
	workers.Wait()
	select {
	case <-changes:
	case <-time.After(time.Second):
		t.Fatal("missing notification")
	}
	records, err := rec.Since(ctx, "thread", 0)
	if err != nil || len(records) != 160 {
		t.Fatal(len(records), err)
	}
	for i, r := range records {
		if r.HostSeq != uint64(i+1) {
			t.Fatal("non-atomic ordering")
		}
	}
	stop()
	stop()
	for range changes {
	}
}

func TestFollowerSubscribeBeforeRead(t *testing.T) {
	rec := sessionrecorder.NewEventRecorder(sessionrecorder.NewMemoryEventBackend())
	defer rec.Close()
	for i := 0; i < 100; i++ {
		changes, stop, err := rec.(sessionrecorder.Follower).Subscribe(context.Background(), "thread")
		if err != nil {
			t.Fatal(err)
		}
		done := make(chan struct{})
		go func() {
			defer close(done)
			_, err := rec.Record(context.Background(), "thread", adaptor.Notice{Kind: "test"})
			if err != nil {
				t.Error(err)
			}
		}()
		records, err := rec.Since(context.Background(), "thread", uint64(i))
		if err != nil {
			t.Fatal(err)
		}
		if len(records) == 0 {
			select {
			case <-changes:
			case <-time.After(time.Second):
				t.Fatal("gap")
			}
		}
		<-done
		records, err = rec.Since(context.Background(), "thread", uint64(i))
		if err != nil || len(records) != 1 {
			t.Fatal(len(records), err)
		}
		stop()
	}
}

type coordinatedBackend struct{ shared sessionrecorder.EventRecorder }

func (b coordinatedBackend) Load(ctx context.Context, key string) ([]sessionrecorder.EventRecord, error) {
	return b.shared.Since(ctx, key, 0)
}

func (b coordinatedBackend) Append(context.Context, string, sessionrecorder.EventRecord) error {
	panic("coordinated append must allocate atomically")
}

func (b coordinatedBackend) AppendNext(
	ctx context.Context, key string, r sessionrecorder.EventRecord,
) (sessionrecorder.EventRecord, error) {
	return b.shared.Record(ctx, key, r.Event)
}

func (b coordinatedBackend) Subscribe(ctx context.Context, key string) (<-chan struct{}, func(), error) {
	return b.shared.(sessionrecorder.Follower).Subscribe(ctx, key)
}

func (b coordinatedBackend) Sessions(ctx context.Context) ([]sessionrecorder.SessionInfo, error) {
	return b.shared.Sessions(ctx)
}

func (b coordinatedBackend) Close() error { return nil }

func TestCoordinatedRecordersAllocateAndReadFresh(t *testing.T) {
	shared := sessionrecorder.NewEventRecorder(sessionrecorder.NewMemoryEventBackend())
	defer shared.Close()
	backend := coordinatedBackend{shared: shared}
	a := sessionrecorder.NewEventRecorder(backend)
	b := sessionrecorder.NewEventRecorder(backend)
	defer a.Close()
	defer b.Close()
	changes, stop, err := b.(sessionrecorder.Follower).Subscribe(context.Background(), "thread")
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	if _, err := b.Since(context.Background(), "thread", 0); err != nil {
		t.Fatal(err)
	}
	var workers sync.WaitGroup
	for _, rec := range []sessionrecorder.EventRecorder{a, b} {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for i := 0; i < 50; i++ {
				if _, err := rec.Record(context.Background(), "thread", adaptor.Notice{Kind: "test"}); err != nil {
					t.Error(err)
				}
			}
		}()
	}
	workers.Wait()
	select {
	case <-changes:
	case <-time.After(time.Second):
		t.Fatal("missing cross-recorder notification")
	}
	for _, rec := range []sessionrecorder.EventRecorder{a, b} {
		records, err := rec.Since(context.Background(), "thread", 0)
		if err != nil || len(records) != 100 {
			t.Fatal("stale cache", len(records), err)
		}
		for i, r := range records {
			if r.HostSeq != uint64(i+1) {
				t.Fatal("conflicting allocation")
			}
		}
	}
}
