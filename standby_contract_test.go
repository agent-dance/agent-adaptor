package adaptor_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	adaptor "github.com/agent-dance/agent-adaptor/v2"
	"github.com/agent-dance/agent-adaptor/v2/driver"
	"github.com/agent-dance/agent-adaptor/v2/internal/engine"
	"github.com/agent-dance/agent-adaptor/v2/memory"
	"github.com/agent-dance/agent-adaptor/v2/threadstore"
)

type standbyReply struct {
	response driver.Response
	err      error
}

type standbyCall struct {
	request driver.Request
	reply   chan standbyReply
}

func controlledStandbyDriver() (*fakeDriver, <-chan standbyCall) {
	fake := messageFake(driver.MessageCapability{Boundaries: []driver.Boundary{driver.BoundaryTool}, Interrupt: true})
	calls := make(chan standbyCall, 64)
	fake.runFunc = func(ctx context.Context, request driver.Request, _ driver.EventSink) (driver.Response, error) {
		call := standbyCall{request: request, reply: make(chan standbyReply, 1)}
		select {
		case calls <- call:
		case <-ctx.Done():
			return driver.Response{}, ctx.Err()
		}
		select {
		case reply := <-call.reply:
			return reply.response, reply.err
		case <-ctx.Done():
			return driver.Response{}, ctx.Err()
		}
	}
	return fake, calls
}

func standbyReceive[Value any](t *testing.T, input <-chan Value) Value {
	t.Helper()
	select {
	case value, open := <-input:
		if !open {
			t.Fatal("channel closed unexpectedly")
		}
		return value
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for progress")
		var zero Value
		return zero
	}
}

func standbyDone(t *testing.T, events <-chan adaptor.Event) ([]adaptor.Event, adaptor.Done) {
	t.Helper()
	var observed []adaptor.Event
	for {
		event := standbyReceive(t, events)
		observed = append(observed, event)
		if done, ok := event.(adaptor.Done); ok {
			return observed, done
		}
	}
}

func openTestStandby(t *testing.T, agent *adaptor.Agent, key string) (adaptor.Standby, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	standby, err := agent.Thread(key).Standby(ctx)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cancel()
		ctx, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		if err := agent.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	return standby, cancel
}

func TestStandbyGetOrCreateConcurrent(t *testing.T) {
	fake, _ := controlledStandbyDriver()
	agent := adaptor.New(fake)
	first, cancel := openTestStandby(t, agent, "opaque:/same-key")
	var group sync.WaitGroup
	for index := 0; index < 40; index++ {
		group.Add(1)
		go func() {
			defer group.Done()
			ctx, stop := context.WithCancel(context.Background())
			got, err := agent.Thread("opaque:/same-key").Standby(ctx)
			stop()
			if err != nil || got != first {
				t.Errorf("Standby=%p, %v; want %p", got, err, first)
			}
		}()
	}
	group.Wait()
	if _, err := agent.Thread("opaque:/same-key").Standby(context.Background()); err != nil {
		t.Fatal(err)
	}
	events := first.Events()
	select {
	case <-events:
		t.Fatal("later admission contexts ended the standby")
	default:
	}
	cancel()
	for range events {
	}
}

func TestStandbyConcurrentCreationHasOneOwner(t *testing.T) {
	fake, _ := controlledStandbyDriver()
	agent := adaptor.New(fake)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	start := make(chan struct{})
	results := make(chan adaptor.Standby, 32)
	var group sync.WaitGroup
	for index := 0; index < 32; index++ {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			standby, err := agent.Thread("create").Standby(ctx)
			if err != nil {
				t.Error(err)
			}
			results <- standby
		}()
	}
	close(start)
	group.Wait()
	close(results)
	var first adaptor.Standby
	for result := range results {
		if first == nil {
			first = result
		}
		if result != first {
			t.Fatalf("more than one live standby: %p and %p", first, result)
		}
	}
	cancel()
	if err := agent.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestStandbyBusyMatrix(t *testing.T) {
	store := memory.NewStore()
	fake, calls := controlledStandbyDriver()
	first := adaptor.New(fake, adaptor.WithThreadStore(store))
	second := adaptor.New(fake, adaptor.WithThreadStore(store))
	t.Cleanup(func() { _ = second.Close(context.Background()) })
	task := first.Thread("busy").Send(context.Background(), "task")
	call := standbyReceive(t, calls)
	for _, agent := range []*adaptor.Agent{first, second} {
		if _, err := agent.Thread("busy").Standby(context.Background()); !errors.Is(err, adaptor.ErrThreadBusy) {
			t.Fatalf("task vs standby=%v", err)
		}
		if _, err := agent.Thread("busy").Send(context.Background(), "competing").Result(); !errors.Is(err, adaptor.ErrThreadBusy) {
			t.Fatalf("task vs task=%v", err)
		}
	}
	call.reply <- standbyReply{response: messageCheckpoint("finished")}
	if _, err := task.Result(); err != nil {
		t.Fatal(err)
	}
	standby, cancel := openTestStandby(t, first, "busy")
	for _, agent := range []*adaptor.Agent{first, second} {
		if _, err := agent.Thread("busy").Send(context.Background(), "competing").Result(); !errors.Is(err, adaptor.ErrThreadBusy) {
			t.Fatalf("standby vs task=%v", err)
		}
	}
	if _, err := second.Thread("busy").Standby(context.Background()); !errors.Is(err, adaptor.ErrThreadBusy) {
		t.Fatalf("standby vs other owner=%v", err)
	}
	cancel()
	for range standby.Events() {
	}
	other, err := second.Thread("busy").Standby(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	other.Cancel()
}

func TestStandbyRoutingAndEventOrdering(t *testing.T) {
	fake, calls := controlledStandbyDriver()
	standby, _ := openTestStandby(t, adaptor.New(fake, adaptor.WithEventBuffer(1)), "routing")
	events := standby.Events()
	if err := standby.Send(context.Background(), "initial", adaptor.MessageID("initial")); err != nil {
		t.Fatal(err)
	}
	first := standbyReceive(t, calls)
	admission, cancelAdmission := context.WithCancel(context.Background())
	if err := standby.Send(admission, "join", adaptor.MessageID("join")); err != nil {
		t.Fatal(err)
	}
	cancelAdmission()
	if err := standby.Send(context.Background(), "after", adaptor.When(adaptor.AfterTurn), adaptor.MessageID("after")); err != nil {
		t.Fatal(err)
	}
	if err := standby.Send(context.Background(), "new model", adaptor.WithModel("model-b"), adaptor.MessageID("new")); err != nil {
		t.Fatal(err)
	}
	claimed := first.request.Messages.Claim(driver.BoundaryTool)
	if len(claimed) != 1 || claimed[0].ID != "join" {
		t.Fatalf("tool claims=%+v", claimed)
	}
	first.request.Messages.Ack("join", driver.MessageAck{Delivered: true, Mechanism: "test.tool"})
	first.reply <- standbyReply{response: messageCheckpoint("first turn")}
	continuation := standbyReceive(t, calls)
	if continuation.request.RunID != first.request.RunID || !strings.Contains(continuation.request.Prompt, "after") {
		t.Fatalf("continuation=%+v", continuation.request)
	}
	select {
	case next := <-calls:
		t.Fatalf("concurrent execution=%+v", next.request)
	default:
	}
	continuation.reply <- standbyReply{response: messageCheckpoint("second turn")}
	observed, done := standbyDone(t, events)
	if done.Err != nil || done.Result.Turns != 2 {
		t.Fatalf("Done=%+v", done)
	}
	next := standbyReceive(t, calls)
	if next.request.RunID == first.request.RunID || next.request.ModelOverride != "model-b" {
		t.Fatalf("option routing=%+v", next.request)
	}
	next.reply <- standbyReply{response: messageCheckpoint("third turn")}
	more, done := standbyDone(t, events)
	if done.Err != nil {
		t.Fatal(done.Err)
	}
	observed = append(observed, more...)
	started, finished, turns := 0, 0, 0
	var sequence uint64
	for _, event := range observed {
		if event.Meta().Sequence <= sequence || event.Meta().ThreadKey != "routing" {
			t.Fatalf("invalid lifetime event order: %+v", event.Meta())
		}
		sequence = event.Meta().Sequence
		switch event.(type) {
		case adaptor.RunStarted:
			started++
		case adaptor.RunFinished:
			finished++
		case adaptor.TurnEnd:
			turns++
		}
	}
	if started != 2 || finished != 2 || turns != 3 {
		t.Fatalf("started=%d finished=%d turns=%d", started, finished, turns)
	}
}

func TestStandbyCancelSurvivesAndReturnsQueuedText(t *testing.T) {
	fake, calls := controlledStandbyDriver()
	standby, cancel := openTestStandby(t, adaptor.New(fake, adaptor.WithEventBuffer(1), adaptor.WithBlockingEvents()), "cancel")
	events := standby.Events()
	if err := standby.Send(context.Background(), "initial", adaptor.MessageID("initial")); err != nil {
		t.Fatal(err)
	}
	standbyReceive(t, calls)
	if err := standby.Send(context.Background(), "undelivered", adaptor.MessageID("pending"), adaptor.When(adaptor.AfterTurn)); err != nil {
		t.Fatal(err)
	}
	if err := standby.Send(context.Background(), "queued configuration", adaptor.MessageID("queued"), adaptor.WithModel("other")); err != nil {
		t.Fatal(err)
	}
	standby.Cancel()
	observed, done := standbyDone(t, events)
	if !errors.Is(done.Err, context.Canceled) {
		t.Fatalf("cancel Done=%v", done.Err)
	}
	failed := map[string]string{}
	for _, event := range observed {
		if message, ok := event.(adaptor.MessageUpdate); ok && message.State == adaptor.MessageFailed {
			if message.Reason != adaptor.MessageCancelled {
				t.Fatalf("failure=%+v", message)
			}
			failed[message.ID] = message.Text
		}
	}
	if failed["pending"] != "undelivered" || failed["queued"] != "queued configuration" {
		t.Fatalf("cancelled messages=%v", failed)
	}
	if err := standby.Send(context.Background(), "survivor"); err != nil {
		t.Fatal(err)
	}
	next := standbyReceive(t, calls)
	next.reply <- standbyReply{response: messageCheckpoint("alive")}
	if _, done := standbyDone(t, events); done.Err != nil {
		t.Fatal(done.Err)
	}
	if err := standby.Send(context.Background(), "ctx cancellation"); err != nil {
		t.Fatal(err)
	}
	standbyReceive(t, calls)
	cancel()
	if _, done := standbyDone(t, events); !errors.Is(done.Err, context.Canceled) {
		t.Fatalf("ctx Done=%v", done.Err)
	}
	for range events {
	}
	if err := standby.Send(context.Background(), "too late"); !errors.Is(err, context.Canceled) {
		t.Fatalf("ended Send=%v", err)
	}
}

func TestStandbyFailureCarriesMessagesAndRetriesOnlyOnce(t *testing.T) {
	for _, retryable := range []bool{false, true} {
		t.Run(fmt.Sprint(retryable), func(t *testing.T) {
			fake, calls := controlledStandbyDriver()
			standby, _ := openTestStandby(t, adaptor.New(fake), "failure")
			events := standby.Events()
			if err := standby.Send(context.Background(), "original", adaptor.MessageID("original")); err != nil {
				t.Fatal(err)
			}
			first := standbyReceive(t, calls)
			if err := standby.Send(context.Background(), "pending guidance", adaptor.MessageID("guidance")); err != nil {
				t.Fatal(err)
			}
			failure := standbyReply{err: errors.New("provider crashed")}
			if retryable {
				failure = standbyReply{response: driver.Response{Failure: &driver.RunFailure{Code: "rate_limit", Message: "slow down", Metadata: map[string]any{"retry_after": time.Duration(0)}}}}
			}
			first.reply <- failure
			if _, done := standbyDone(t, events); done.Err == nil {
				t.Fatal("failure hidden")
			}
			if retryable {
				retry := standbyReceive(t, calls)
				if !strings.Contains(retry.request.Prompt, "original") || !strings.Contains(retry.request.Prompt, "pending guidance") {
					t.Fatalf("retry lost input: %q", retry.request.Prompt)
				}
				retry.reply <- failure
				if _, done := standbyDone(t, events); done.Err == nil {
					t.Fatal("second failure hidden")
				}
			}
			if err := standby.Send(context.Background(), "new input", adaptor.MessageID("new")); err != nil {
				t.Fatal(err)
			}
			next := standbyReceive(t, calls)
			if !strings.Contains(next.request.Prompt, "new input") || !strings.Contains(next.request.Prompt, "pending guidance") || !strings.Contains(next.request.Prompt, "original") {
				t.Fatalf("carry-over lost input or unbounded retry: %q", next.request.Prompt)
			}
			next.reply <- standbyReply{response: messageCheckpoint("recovered")}
			if _, done := standbyDone(t, events); done.Err != nil || len(done.Result.Messages()) != 3 {
				t.Fatalf("recovery=%+v", done)
			}
		})
	}
}

func TestStandbyFailedMessageIDCanWakeAgain(t *testing.T) {
	fake, calls := controlledStandbyDriver()
	standby, _ := openTestStandby(t, adaptor.New(fake), "resend")
	events := standby.Events()
	if err := standby.Send(context.Background(), "original", adaptor.MessageID("same-id")); err != nil {
		t.Fatal(err)
	}
	first := standbyReceive(t, calls)
	first.reply <- standbyReply{err: errors.New("provider process failed")}
	if _, done := standbyDone(t, events); done.Err == nil {
		t.Fatal("failure hidden")
	}
	if err := standby.Send(context.Background(), "original", adaptor.MessageID("same-id")); err != nil {
		t.Fatal(err)
	}
	next := standbyReceive(t, calls)
	next.reply <- standbyReply{response: messageCheckpoint("recovered")}
	if _, done := standbyDone(t, events); done.Err != nil || len(done.Result.Messages()) != 1 {
		t.Fatalf("failed ID was not resumed exactly once: %+v", done)
	}
}

func TestStandbyPrelaunchFailureReturnsAdmittedMessage(t *testing.T) {
	fake, calls := controlledStandbyDriver()
	// Resource preparation may fail after admission. Return the original
	// input, rather than replaying it with the next execution's options.
	cause := errors.New("workspace temporarily unavailable")
	manager := &fakeWorkspaceManager{err: cause, log: &callLog{}}
	standby, _ := openTestStandby(t, adaptor.New(fake, adaptor.WithWorkspaceManager(manager), adaptor.WithWorkspaceSpec(adaptor.SharedWorkspace{})), "prelaunch")
	events := standby.Events()
	if err := standby.Send(context.Background(), "admitted before validation", adaptor.MessageID("resource-failure")); err != nil {
		t.Fatal(err)
	}
	observed, done := standbyDone(t, events)
	if !errors.Is(done.Err, cause) {
		t.Fatalf("prelaunch error=%v", done.Err)
	}
	returned := false
	for _, event := range observed {
		if update, ok := event.(adaptor.MessageUpdate); ok && update.ID == "resource-failure" && update.State == adaptor.MessageFailed {
			if update.Reason != adaptor.MessageProviderRejected || update.Text != "admitted before validation" {
				t.Fatalf("prelaunch receipt=%+v", update)
			}
			returned = true
		}
	}
	if !returned {
		t.Fatal("resource preparation failure omitted the admitted input")
	}
	manager.mu.Lock()
	manager.err = nil
	manager.mu.Unlock()
	if err := standby.Send(context.Background(), "valid next execution", adaptor.MessageID("valid")); err != nil {
		t.Fatal(err)
	}
	call := standbyReceive(t, calls)
	if call.request.Prompt != "valid next execution" {
		t.Fatalf("prelaunch input silently replayed: %q", call.request.Prompt)
	}
	call.reply <- standbyReply{response: messageCheckpoint("recovered")}
	if _, done := standbyDone(t, events); done.Err != nil || len(done.Result.Messages()) != 1 {
		t.Fatalf("prelaunch recovery=%+v", done)
	}
}

func TestStandbyOwnershipRenewsWhileIdle(t *testing.T) {
	shortLeases(t, time.Second, time.Millisecond)
	store := &renewCountingStore{Store: memory.NewStore(), threshold: 2, reached: make(chan struct{})}
	fake, _ := controlledStandbyDriver()
	standby, cancel := openTestStandby(t, adaptor.New(fake, adaptor.WithThreadStore(store)), "renew")
	<-store.reached
	other := adaptor.New(fake, adaptor.WithThreadStore(store))
	if _, err := other.Thread("renew").Standby(context.Background()); !errors.Is(err, adaptor.ErrThreadBusy) {
		t.Fatal(err)
	}
	cancel()
	for range standby.Events() {
	}
}

type standbyClockStore struct {
	threadstore.Store
	mu     sync.Mutex
	clock  time.Time
	leases map[string]threadstore.Lease
	until  map[string]time.Time
}

func (s *standbyClockStore) AcquireLease(ctx context.Context, target, owner string, ttl time.Duration) (threadstore.Lease, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if old, ok := s.leases[target]; ok && !s.until[target].After(s.clock) {
		if err := s.Store.ReleaseLease(ctx, old); err != nil {
			return threadstore.Lease{}, err
		}
	}
	lease, err := s.Store.AcquireLease(ctx, target, owner, ttl)
	if err == nil {
		s.leases[target] = lease
		s.until[target] = s.clock.Add(ttl)
	}
	return lease, err
}

func TestStandbyLeaseExpiryAfterSimulatedCrash(t *testing.T) {
	store := &standbyClockStore{Store: memory.NewStore(), clock: time.Now(), leases: make(map[string]threadstore.Lease), until: make(map[string]time.Time)}
	fake, _ := controlledStandbyDriver()
	first := adaptor.New(fake, adaptor.WithThreadStore(store))
	standby, cancel := openTestStandby(t, first, "crash")
	store.mu.Lock()
	store.clock = store.clock.Add(engine.LeaseTTL() + time.Second)
	store.mu.Unlock()
	second := adaptor.New(fake, adaptor.WithThreadStore(store))
	_, secondCancel := openTestStandby(t, second, "crash")
	cancel()
	for range standby.Events() {
	}
	if _, err := first.Thread("crash").Send(context.Background(), "stale writer").Result(); !errors.Is(err, adaptor.ErrThreadBusy) {
		t.Fatalf("old release displaced new owner: %v", err)
	}
	secondCancel()
}

func TestStandbyExpiredOwnerCannotDispatch(t *testing.T) {
	store := &standbyClockStore{Store: memory.NewStore(), clock: time.Now(), leases: make(map[string]threadstore.Lease), until: make(map[string]time.Time)}
	fake, calls := controlledStandbyDriver()
	standby, _ := openTestStandby(t, adaptor.New(fake, adaptor.WithThreadStore(store)), "fenced")
	events := standby.Events()
	store.mu.Lock()
	store.clock = store.clock.Add(engine.LeaseTTL() + time.Second)
	store.mu.Unlock()
	_, cancelOther := openTestStandby(t, adaptor.New(fake, adaptor.WithThreadStore(store)), "fenced")
	defer cancelOther()
	if err := standby.Send(context.Background(), "stale owner"); err != nil {
		t.Fatal(err)
	}
	if _, done := standbyDone(t, events); !errors.Is(done.Err, adaptor.ErrThreadLeaseLost) {
		t.Fatalf("unfenced expired owner: %v", done.Err)
	}
	for range events {
	}
	select {
	case call := <-calls:
		t.Fatalf("expired owner reached Driver.Run: %+v", call.request)
	default:
	}
}
