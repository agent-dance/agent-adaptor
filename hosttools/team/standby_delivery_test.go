package team

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	adaptor "github.com/agent-dance/agent-adaptor/v2"
	"github.com/agent-dance/agent-adaptor/v2/driver"
)

func TestStandbyQueuedExecutionOutlivesAdmissionContext(t *testing.T) {
	ctx := testContext(t)
	lifetime, stop := context.WithCancel(ctx)
	defer stop()
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	leader := adaptor.New(&fakeDriver{run: func(ctx context.Context, req driver.Request, _ driver.EventSink) (driver.Response, error) {
		if calls.Add(1) == 1 {
			close(entered)
			select {
			case <-release:
			case <-ctx.Done():
				return driver.Response{}, ctx.Err()
			}
		} else if req.ModelOverride != "next" || !strings.Contains(req.Prompt, "queued objective") || ctx.Err() != nil {
			t.Errorf("queued execution: model=%q prompt=%q ctx=%v", req.ModelOverride, req.Prompt, ctx.Err())
		}
		return answer("leader", 1), nil
	}})
	store := NewMemoryStore()
	team := fixture(t, Config{Leader: leader, Store: store})
	waiting, err := team.Thread("standby-context").Standby(lifetime)
	if err != nil {
		t.Fatal(err)
	}
	events := waiting.Events()
	drained, completed := make(chan struct{}), make(chan adaptor.Done, 2)
	var updates []adaptor.MessageUpdate
	go func() {
		defer close(drained)
		for event := range events {
			switch event := event.(type) {
			case adaptor.MessageUpdate:
				updates = append(updates, event)
			case adaptor.Done:
				completed <- event
			}
		}
	}()
	defer func() { stop(); <-drained }()
	if err := waiting.Send(ctx, "first"); err != nil {
		t.Fatal(err)
	}
	<-entered
	admission, cancel := context.WithCancel(ctx)
	defer cancel()
	if err := waiting.Send(admission, "queued objective", adaptor.MessageID("queued"), adaptor.WithModel("next")); err != nil {
		t.Fatal(err)
	}
	cancel()
	close(release)
	for index := 0; index < 2; index++ {
		select {
		case done := <-completed:
			if done.Err != nil {
				t.Fatalf("execution %d: %v", index+1, done.Err)
			}
			if index == 1 {
				outcomes := done.Result.Messages()
				if len(outcomes) != 1 || outcomes[0].ID != "queued" || outcomes[0].State != adaptor.MessageDelivered || outcomes[0].Text != "queued objective" {
					t.Fatalf("queued outcomes=%+v", outcomes)
				}
			}
		case <-ctx.Done():
			t.Fatal("admitted execution was withdrawn with its admission context")
		}
	}
	stop()
	<-drained
	<-waiting.(*standby).done
	if calls.Load() != 2 {
		t.Fatalf("provider calls=%d, want 2", calls.Load())
	}
	var delivered int
	for _, update := range updates {
		if update.ID == "queued" {
			if update.State == adaptor.MessageFailed {
				t.Fatalf("admitted input failed: %+v", update)
			}
			if update.State == adaptor.MessageDelivered {
				delivered++
			}
		}
	}
	if delivered != 1 {
		t.Fatalf("queued delivery receipts=%d, want 1", delivered)
	}
	records, err := store.Messages(ctx, "standby-context")
	if err != nil {
		t.Fatal(err)
	}
	var persisted bool
	for _, record := range records {
		if record.ID == "queued" {
			persisted = record.State == Delivered && record.Reason == ""
		}
	}
	if !persisted {
		t.Fatalf("queued delivery was not persisted: %+v", records)
	}
}

func TestStandbyBusySupplementOutlivesAdmissionContext(t *testing.T) {
	for _, when := range []string{"after_return", "at_commit"} {
		t.Run(when, func(t *testing.T) { testStandbyBusySupplement(t, when == "at_commit") })
	}
}

type commitAdmissionStore struct {
	*MemoryStore
	cancel context.CancelFunc
}

func (store *commitAdmissionStore) PutMessage(ctx context.Context, lease ScopeLease, message InboxMessage) error {
	err := store.MemoryStore.PutMessage(ctx, lease, message)
	if err == nil && message.ID == "supplement" && message.Deferred {
		store.cancel()
	}
	return err
}

func testStandbyBusySupplement(t *testing.T, cancelAtCommit bool) {
	t.Helper()
	ctx := testContext(t)
	lifetime, stop := context.WithCancel(ctx)
	defer stop()
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	leader := adaptor.New(&fakeDriver{run: func(ctx context.Context, req driver.Request, _ driver.EventSink) (driver.Response, error) {
		if calls.Add(1) == 1 {
			close(entered)
			select {
			case <-release:
			case <-ctx.Done():
				return driver.Response{}, ctx.Err()
			}
		} else if !strings.Contains(req.Prompt, "admitted supplement") {
			t.Errorf("continuation lost supplement: %q", req.Prompt)
		}
		return answer("leader", 1), nil
	}})
	admission, cancel := context.WithCancel(ctx)
	defer cancel()
	var store TaskStore = NewMemoryStore()
	if cancelAtCommit {
		store = &commitAdmissionStore{MemoryStore: NewMemoryStore(), cancel: cancel}
	}
	team := fixture(t, Config{Leader: leader, Store: store})
	waiting, err := team.Thread("busy-context").Standby(lifetime)
	if err != nil {
		t.Fatal(err)
	}
	drained, completed := make(chan struct{}), make(chan adaptor.Done, 1)
	go func() {
		defer close(drained)
		for event := range waiting.Events() {
			if done, ok := event.(adaptor.Done); ok {
				completed <- done
			}
		}
	}()
	defer func() { stop(); <-drained }()
	if err := waiting.Send(ctx, "first"); err != nil {
		t.Fatal(err)
	}
	<-entered
	if err := waiting.Send(admission, "admitted supplement", adaptor.MessageID("supplement")); err != nil {
		t.Fatal(err)
	}
	cancel()
	close(release)
	select {
	case done := <-completed:
		if done.Err != nil {
			t.Fatal(done.Err)
		}
		var delivered bool
		for _, outcome := range done.Result.Messages() {
			if outcome.ID == "supplement" {
				delivered = outcome.State == adaptor.MessageDelivered
			}
		}
		if !delivered || calls.Load() != 2 {
			t.Fatalf("busy admitted supplement was withdrawn: calls=%d outcomes=%+v", calls.Load(), done.Result.Messages())
		}
	case <-ctx.Done():
		t.Fatal("busy admitted supplement did not complete")
	}
}

func TestStandbyBusySupplementReportsProviderAdmissionFailure(t *testing.T) {
	ctx := testContext(t)
	lifetime, stop := context.WithCancel(ctx)
	defer stop()
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	leader := adaptor.New(&fakeDriver{run: func(ctx context.Context, _ driver.Request, _ driver.EventSink) (driver.Response, error) {
		calls.Add(1)
		close(entered)
		select {
		case <-release:
		case <-ctx.Done():
			return driver.Response{}, ctx.Err()
		}
		return answer("leader", 1), nil
	}})
	store := NewMemoryStore()
	team := fixture(t, Config{Leader: leader, Store: store})
	waiting, err := team.Thread("unsupported-context").Standby(lifetime)
	if err != nil {
		t.Fatal(err)
	}
	drained := make(chan struct{})
	failed := make(chan adaptor.MessageUpdate, 1)
	completed := make(chan adaptor.Done, 1)
	go func() {
		defer close(drained)
		for event := range waiting.Events() {
			switch event := event.(type) {
			case adaptor.MessageUpdate:
				if event.ID == "unsupported" && event.State == adaptor.MessageFailed {
					failed <- event
				}
			case adaptor.Done:
				completed <- event
			}
		}
	}()
	defer func() { stop(); <-drained }()
	if err := waiting.Send(ctx, "first"); err != nil {
		t.Fatal(err)
	}
	<-entered
	if err := waiting.Send(ctx, "unsupported interrupt", adaptor.MessageID("unsupported"), adaptor.When(adaptor.Interrupt)); err != nil {
		t.Fatalf("successfully persisted input was rejected synchronously: %v", err)
	}
	select {
	case update := <-failed:
		if update.Reason != adaptor.MessageProviderRejected || update.Text != "unsupported interrupt" {
			t.Fatalf("provider failure receipt=%+v", update)
		}
	case <-ctx.Done():
		t.Fatal("provider admission failure did not produce a receipt")
	}
	close(release)
	select {
	case done := <-completed:
		if done.Err != nil || calls.Load() != 1 {
			t.Fatalf("provider rejected input was executed: err=%v calls=%d", done.Err, calls.Load())
		}
	case <-ctx.Done():
		t.Fatal("leader did not complete")
	}
	records, err := store.Messages(ctx, "unsupported-context")
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range records {
		if record.ID == "unsupported" {
			if record.State != DeliveryFailed || record.Reason != string(adaptor.MessageProviderRejected) {
				t.Fatalf("persisted failure=%+v", record)
			}
			return
		}
	}
	t.Fatal("provider admission failure lost its durable input")
}

type standbyAdmissionStore struct {
	*MemoryStore
	entered chan struct{}
	release chan struct{}
}

func (store *standbyAdmissionStore) PutMessage(ctx context.Context, lease ScopeLease, message InboxMessage) error {
	if message.ID == "blocked" && message.Deferred {
		close(store.entered)
		select {
		case <-store.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return store.MemoryStore.PutMessage(ctx, lease, message)
}

type observedAdmissionContext struct {
	context.Context
	observed chan struct{}
	once     sync.Once
}

func (ctx *observedAdmissionContext) Done() <-chan struct{} {
	ctx.once.Do(func() { close(ctx.observed) })
	return ctx.Context.Done()
}

func TestStandbyAdmissionContextBoundsBlockedAdmissions(t *testing.T) {
	for _, name := range []string{"storage", "admission_capacity", "standby_lifetime"} {
		t.Run(name, func(t *testing.T) {
			queued := name == "admission_capacity"
			ctx := testContext(t)
			lifetime, stop := context.WithCancel(ctx)
			defer stop()
			var calls atomic.Int32
			leader := adaptor.New(&fakeDriver{run: func(context.Context, driver.Request, driver.EventSink) (driver.Response, error) {
				calls.Add(1)
				return answer("leader", 1), nil
			}})
			store := &standbyAdmissionStore{MemoryStore: NewMemoryStore(), entered: make(chan struct{}), release: make(chan struct{})}
			team := fixture(t, Config{Leader: leader, Store: store})
			waiting, err := team.Thread("blocked-admission").Standby(lifetime)
			if err != nil {
				t.Fatal(err)
			}
			admission, cancel := context.WithCancel(ctx)
			defer cancel()
			returned := make(chan error, 1)
			id, text := "blocked", "not admitted"
			var first chan error
			if queued {
				first = make(chan error, 1)
				go func() {
					first <- waiting.Send(ctx, "accepted", adaptor.MessageID("blocked"), adaptor.WithModel("first"))
				}()
				<-store.entered
				observed := &observedAdmissionContext{Context: admission, observed: make(chan struct{})}
				id, text = "rejected", "not admitted behind another admission"
				go func() { returned <- waiting.Send(observed, text, adaptor.MessageID(id), adaptor.WithModel("next")) }()
				select {
				case <-observed.observed:
				case <-ctx.Done():
					t.Fatal("Send did not observe admission cancellation while blocked")
				}
			} else {
				go func() { returned <- waiting.Send(admission, text, adaptor.MessageID(id), adaptor.WithModel("next")) }()
				<-store.entered
			}
			if name == "standby_lifetime" {
				stop()
			} else {
				cancel()
			}
			select {
			case err := <-returned:
				var rejected *adaptor.MessageError
				if !errors.Is(err, context.Canceled) || !errors.As(err, &rejected) || rejected.ID != id || rejected.Text != text || rejected.Reason != adaptor.MessageCancelled {
					t.Fatalf("admission error=%v, message=%+v", err, rejected)
				}
			case <-ctx.Done():
				t.Fatal("cancelled admission remained blocked")
			}
			if queued {
				close(store.release)
				if err := <-first; err != nil {
					t.Fatal(err)
				}
			}
			stop()
			<-waiting.(*standby).done
			records, err := store.Messages(ctx, "blocked-admission")
			if err != nil {
				t.Fatal(err)
			}
			for _, record := range records {
				if record.ID == id {
					t.Fatalf("rejected admission entered the inbox: %+v", record)
				}
			}
			if !queued && calls.Load() != 0 {
				t.Fatalf("rejected admission started %d provider calls", calls.Load())
			}
		})
	}
}
