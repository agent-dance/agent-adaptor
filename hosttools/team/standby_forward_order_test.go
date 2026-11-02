package team

import (
	"context"
	"runtime"
	"strings"
	"sync"
	"testing"

	adaptor "github.com/agent-dance/agent-adaptor/v2"
	"github.com/agent-dance/agent-adaptor/v2/driver"
)

type finishedForwardStream struct {
	events  chan adaptor.Event
	done    chan struct{}
	entered chan struct{}
	release chan struct{}
	result  *adaptor.Result
	once    sync.Once
}

func (stream *finishedForwardStream) Events() <-chan adaptor.Event { return stream.events }
func (stream *finishedForwardStream) Result() (*adaptor.Result, error) {
	<-stream.done
	return stream.result, nil
}
func (stream *finishedForwardStream) RunID() string { return "old" }
func (stream *finishedForwardStream) Cancel() {
	stream.once.Do(func() {
		stream.events <- adaptor.Done{Result: stream.result}
		close(stream.events)
		close(stream.done)
	})
}
func (stream *finishedForwardStream) Send(ctx context.Context, _ string, _ ...adaptor.MessageOption) error {
	close(stream.entered)
	select {
	case <-stream.release:
	case <-ctx.Done():
		return ctx.Err()
	}
	return adaptor.ErrRunFinished
}

func TestStandbyFinishedForwardKeepsAdmissionOrder(t *testing.T) {
	ctx := testContext(t)
	lifetime, stop := context.WithCancel(ctx)
	defer stop()
	calls := make(chan driver.Request, 2)
	leader := adaptor.New(&fakeDriver{run: func(_ context.Context, request driver.Request, _ driver.EventSink) (driver.Response, error) {
		calls <- request
		return answer("leader", 1), nil
	}})
	store := NewMemoryStore()
	team := fixture(t, Config{Leader: leader, Store: store})
	lease, err := store.AcquireScope(ctx, "forward-order", "test", scopeTTL)
	if err != nil {
		t.Fatal(err)
	}
	old := &finishedForwardStream{events: make(chan adaptor.Event, 1), done: make(chan struct{}), entered: make(chan struct{}), release: make(chan struct{}), result: &adaptor.Result{}}
	waiting := &standby{
		thread: team.Thread("forward-order"), ctx: lifetime, cancel: stop, current: old, lease: lease,
		observed: map[string]bool{}, wake: make(chan struct{}, 1), done: make(chan struct{}),
		log: newEventLog("", "forward-order", true), subscribed: make(chan struct{}),
		ready: make(chan struct{}), admission: make(chan struct{}, 1),
	}
	close(waiting.ready)
	team.mu.Lock()
	team.standbys["forward-order"] = waiting
	team.mu.Unlock()
	events := waiting.Events()
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		for range events {
		}
	}()
	go waiting.loop()
	defer func() { stop(); <-drained; <-waiting.done }()
	if err := waiting.Send(ctx, "first admitted supplement", adaptor.MessageID("first")); err != nil {
		t.Fatal(err)
	}
	<-old.entered
	if err := waiting.Send(ctx, "later configured execution", adaptor.MessageID("later"), adaptor.WithModel("later")); err != nil {
		t.Fatal(err)
	}
	old.Cancel()
	// Observe the actual handoff state, without a scheduling sleep. The old
	// execution has finished, but its admitted Send has not returned yet.
	for {
		waiting.mu.Lock()
		cleared := waiting.current == nil
		waiting.mu.Unlock()
		if cleared {
			break
		}
		if ctx.Err() != nil {
			t.Fatal("completed stream did not relinquish the current slot")
		}
		runtime.Gosched()
	}
	select {
	case request := <-calls:
		t.Fatalf("later input overtook an unfinished earlier handoff: %q", request.Prompt)
	default:
	}
	close(old.release)
	for index := 0; index < 2; index++ {
		select {
		case request := <-calls:
			if index == 0 && (!strings.Contains(request.Prompt, "first admitted supplement") || request.ModelOverride != "") {
				t.Fatalf("first execution=%+v", request)
			}
			if index == 1 && (!strings.Contains(request.Prompt, "later configured execution") || request.ModelOverride != "later") {
				t.Fatalf("later execution=%+v", request)
			}
		case <-ctx.Done():
			t.Fatal("admitted inputs did not execute after the handoff completed")
		}
	}
}
