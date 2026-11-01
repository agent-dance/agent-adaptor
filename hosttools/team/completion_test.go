package team

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	adaptor "github.com/agent-dance/agent-adaptor/v2"
	"github.com/agent-dance/agent-adaptor/v2/driver"
)

func TestTeamAggregatesEveryLeaderExecution(t *testing.T) {
	ctx := testContext(t)
	var team *Team
	var calls atomic.Int32
	idle := make(chan struct{})
	store := &idleObservedStore{MemoryStore: NewMemoryStore(), idle: idle}
	worker := adaptor.New(&fakeDriver{run: func(context.Context, driver.Request, driver.EventSink) (driver.Response, error) {
		select {
		case <-idle:
		case <-ctx.Done():
			return driver.Response{}, ctx.Err()
		}
		return answer("worker", 2), nil
	}})
	leader := adaptor.New(&fakeDriver{run: func(_ context.Context, r driver.Request, _ driver.EventSink) (driver.Response, error) {
		if calls.Add(1) == 1 {
			if _, err := call(team, r, "delegate", map[string]any{"member": "worker", "objective": "work"}); err != nil {
				return driver.Response{}, err
			}
			return answer("first", 1), nil
		}
		return answer("last", 3), nil
	}})
	team = fixture(t, Config{Leader: leader, Store: store, Members: []Member{{Key: "worker", Runner: worker}}})
	events, result, err := collect(t, team.Thread("audit").Send(ctx, "start", adaptor.MessageID("caller-id")))
	if err != nil {
		t.Fatal(err)
	}
	if result.Turns != 2 || result.Text != "last" || result.Usage.InputTokens != 6 || result.Raw().Stdout != "raw firstraw last" || len(result.Services()) != 2 || len(result.Transcript()) != 2 || len(result.Messages()) != 2 || result.Messages()[0].ID != "caller-id" || result.Messages()[1].Turn != 2 || len(result.Subagents()) != 1 {
		t.Fatalf("result=%+v", result)
	}
	for _, e := range events {
		if notice, ok := e.(adaptor.Notice); ok && notice.Kind == "team.audit_scope" {
			t.Fatal("obsolete audit limitation")
		}
	}
}

func TestRecoveryInterruptsTasksAndKeepsHostID(t *testing.T) {
	ctx := testContext(t)
	store := NewMemoryStore()
	lease, err := store.AcquireScope(ctx, "scope", "dead-process", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	task, err := store.SaveTask(ctx, lease, Task{Scope: "scope", ID: "lost", Member: "worker", State: Queued, Objective: "work"})
	if err != nil {
		t.Fatal(err)
	}
	task.State = Running
	if _, err = store.SaveTask(ctx, lease, task); err != nil {
		t.Fatal(err)
	}
	host := hostRecord("scope", "unconfirmed", []adaptor.CallOption{adaptor.MessageID("host-exact"), adaptor.From(adaptor.Author{Kind: adaptor.AuthorHuman, ID: "person"})})
	if err = store.PutMessage(ctx, lease, host); err != nil {
		t.Fatal(err)
	}
	if _, err = store.ClaimMessages(ctx, lease, true, time.Hour); err != nil {
		t.Fatal(err)
	}
	if err = store.ReleaseScope(ctx, lease); err != nil {
		t.Fatal(err)
	}
	var preamble atomic.Bool
	leader := adaptor.New(&fakeDriver{run: func(_ context.Context, r driver.Request, _ driver.EventSink) (driver.Response, error) {
		if strings.Contains(r.Prompt, "interrupted") && strings.Contains(r.Prompt, "lost") {
			preamble.Store(true)
		}
		return answer("recovered", 1), nil
	}})
	team := fixture(t, Config{Leader: leader, Store: store})
	_, result, err := collect(t, team.Thread("scope").Send(ctx, "resume"))
	if err != nil {
		t.Fatal(err)
	}
	if !preamble.Load() {
		t.Fatal("leader did not receive recovery preamble")
	}
	tasks, _ := store.Tasks(ctx, "scope")
	if tasks[0].State != Interrupted || tasks[0].Reason != "execution_lost" {
		t.Fatal(tasks)
	}
	messages, _ := store.Messages(ctx, "scope")
	found := false
	for _, m := range messages {
		if m.ID == "host-exact" {
			found = true
			if m.State != Delivered || m.From.ID != "person" {
				t.Fatal(m)
			}
		}
	}
	if !found {
		t.Fatal("lost caller ID")
	}
	delivered := false
	for _, m := range result.Messages() {
		if m.ID == "host-exact" && m.State == adaptor.MessageDelivered {
			delivered = true
		}
	}
	if !delivered {
		t.Fatal("recovered message missing from Result")
	}
}

func TestTeamForkBranchesLeaderAndPerScope(t *testing.T) {
	ctx := testContext(t)
	var team *Team
	var leaderFork, memberFork atomic.Bool
	var phase atomic.Int32
	worker := adaptor.New(&fakeDriver{run: func(_ context.Context, r driver.Request, _ driver.EventSink) (driver.Response, error) {
		if r.Session != nil && r.Session.Mode == driver.SessionFork {
			memberFork.Store(true)
		}
		return answer("worker", 1), nil
	}})
	leader := adaptor.New(&fakeDriver{run: func(_ context.Context, r driver.Request, _ driver.EventSink) (driver.Response, error) {
		if r.Session != nil && r.Session.Mode == driver.SessionFork {
			leaderFork.Store(true)
		}
		if phase.Add(1)%2 == 1 {
			if _, err := call(team, r, "delegate", map[string]any{"member": "worker", "objective": "work"}); err != nil {
				return driver.Response{}, err
			}
		}
		return answer("leader", 1), nil
	}})
	store := NewMemoryStore()
	team = fixture(t, Config{Leader: leader, Store: store, Members: []Member{{Key: "worker", Runner: worker, Continuity: PerScope}}})
	if _, _, err := collect(t, team.Thread("parent").Send(ctx, "seed")); err != nil {
		t.Fatal(err)
	}
	child, err := team.Thread("parent").Fork("branch")
	if err != nil {
		t.Fatal(err)
	}
	copied, _ := store.Tasks(ctx, "branch")
	if len(copied) != 1 || copied[0].State != Succeeded {
		t.Fatal(copied)
	}
	if _, _, err := collect(t, child.Send(ctx, "branch work")); err != nil {
		t.Fatal(err)
	}
	if !leaderFork.Load() || !memberFork.Load() {
		t.Fatalf("fork leader=%v member=%v", leaderFork.Load(), memberFork.Load())
	}
	original, _ := store.Tasks(ctx, "parent")
	if len(original) != 1 {
		t.Fatal("fork modified parent")
	}
	if _, err := team.Thread("parent").Fork("branch"); !errors.Is(err, ErrConflict) {
		t.Fatalf("fork conflict=%v", err)
	}
	lifetime, cancel := context.WithCancel(ctx)
	defer cancel()
	waiting, err := team.Thread("parent").Standby(lifetime)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := team.Thread("parent").Fork("busy"); !errors.Is(err, adaptor.ErrThreadBusy) {
		t.Fatalf("fork busy=%v", err)
	}
	cancel()
	for range waiting.Events() {
	}
}

func TestLeaderTurnBudgetCapsInRunContinuation(t *testing.T) {
	ctx := testContext(t)
	entered := make(chan struct{})
	release := make(chan struct{})
	var turns atomic.Int32
	leader := adaptor.New(&fakeDriver{cap: driver.MessageCapability{Continue: driver.ContinueInRun}, run: func(ctx context.Context, r driver.Request, s driver.EventSink) (driver.Response, error) {
		r.Messages.Window(true)
		close(entered)
		select {
		case <-release:
		case <-ctx.Done():
			return driver.Response{}, ctx.Err()
		}
		for {
			index := int(turns.Add(1)) - 1
			if err := s.Emit(driver.RunEvent{Type: driver.RunEventTurnCompleted, Turn: &driver.TurnCompleted{Index: index, Text: "turn", Usage: &driver.Usage{InputTokens: 1}}}); err != nil {
				return driver.Response{}, err
			}
			items := r.Messages.Claim(driver.BoundaryTurnEnd)
			if len(items) == 0 {
				break
			}
			for _, item := range items {
				r.Messages.Ack(item.ID, driver.MessageAck{Delivered: true, Mechanism: "fake.inrun"})
			}
		}
		return answer("turn", 1), nil
	}})
	team := fixture(t, Config{Leader: leader, Limits: Limits{MaxLeaderTurns: 1}})
	stream := team.Send(ctx, "start")
	<-entered
	if err := stream.Send(ctx, "queued", adaptor.MessageID("over-budget"), adaptor.When(adaptor.AfterTurn)); err != nil {
		t.Fatal(err)
	}
	close(release)
	_, result, err := collect(t, stream)
	if err != nil || turns.Load() != 1 || result.Turns != 1 {
		t.Fatalf("turns=%d result=%+v err=%v", turns.Load(), result, err)
	}
	failed := false
	for _, m := range result.Undelivered() {
		if m.ID == "over-budget" && m.Reason == adaptor.MessageLimitExceeded {
			failed = true
		}
	}
	if !failed {
		t.Fatal("pending message not returned at turn cap")
	}
}

func TestStandbyQueuedCancellationPreservesIDAndOptions(t *testing.T) {
	ctx := testContext(t)
	entered := make(chan struct{})
	var calls atomic.Int32
	leader := adaptor.New(&fakeDriver{run: func(ctx context.Context, r driver.Request, _ driver.EventSink) (driver.Response, error) {
		if calls.Add(1) == 1 {
			close(entered)
			<-ctx.Done()
			return driver.Response{}, ctx.Err()
		}
		if r.ModelOverride != "new-model" {
			t.Errorf("model=%q", r.ModelOverride)
		}
		return answer("next", 1), nil
	}})
	store := NewMemoryStore()
	team := fixture(t, Config{Leader: leader, Store: store})
	lifetime, cancel := context.WithCancel(ctx)
	defer cancel()
	waiting, err := team.Thread("chat").Standby(lifetime)
	if err != nil {
		t.Fatal(err)
	}
	events := waiting.Events()
	if err := waiting.Send(ctx, "first", adaptor.MessageID("first")); err != nil {
		t.Fatal(err)
	}
	<-entered
	if err := waiting.Send(ctx, "cancel this", adaptor.MessageID("exact-cancel"), adaptor.WithModel("never")); err != nil {
		t.Fatal(err)
	}
	waiting.Cancel()
	failed := false
	done := false
	for !failed || !done {
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case e := <-events:
			if m, ok := e.(adaptor.MessageUpdate); ok && m.ID == "exact-cancel" && m.State == adaptor.MessageFailed && m.Text == "cancel this" {
				failed = true
			}
			if _, ok := e.(adaptor.Done); ok {
				done = true
			}
		}
	}
	messages, _ := store.Messages(ctx, "chat")
	for _, m := range messages {
		if m.ID == "exact-cancel" && m.State != DeliveryFailed {
			t.Fatalf("cancel record=%+v", m)
		}
	}
	if err := waiting.Send(ctx, "next", adaptor.MessageID("next"), adaptor.WithModel("new-model")); err != nil {
		t.Fatal(err)
	}
	for e := range events {
		if d, ok := e.(adaptor.Done); ok {
			if d.Err != nil || d.Result.Text != "next" {
				t.Fatal(d)
			}
			break
		}
	}
	cancel()
	for range events {
	}
}

// Messages is read by onEnd after it clears the leader. This is an actor
// boundary; a TurnEnd event alone does not mean the leaf execution is done.
type idleObservedStore struct {
	*MemoryStore
	idle chan struct{}
	once sync.Once
}

func (store *idleObservedStore) Messages(ctx context.Context, key string) ([]InboxMessage, error) {
	messages, err := store.MemoryStore.Messages(ctx, key)
	store.once.Do(func() { close(store.idle) })
	return messages, err
}

func TestRecoveryDoesNotReplaceQueuedExecutionOptions(t *testing.T) {
	ctx := testContext(t)
	store := NewMemoryStore()
	lease, err := store.AcquireScope(ctx, "scope", "old", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	message := hostRecord("scope", "use configured model", []adaptor.CallOption{adaptor.MessageID("exact"), adaptor.WithModel("configured")})
	message.Deferred = true
	if err := store.PutMessage(ctx, lease, message); err != nil {
		t.Fatal(err)
	}
	if err := store.ReleaseScope(ctx, lease); err != nil {
		t.Fatal(err)
	}
	next, err := store.AcquireScope(ctx, "scope", "new", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := store.ClaimMessages(ctx, next, true, time.Hour)
	if err != nil || len(claimed) != 0 {
		t.Fatalf("replayed with lost execution options: %v %v", claimed, err)
	}
	messages, _ := store.Messages(ctx, "scope")
	if len(messages) != 1 || messages[0].Text != message.Text || !messages[0].RequiresOptions || !messages[0].Deferred {
		t.Fatal(messages)
	}
	message.Deferred = false
	if err := store.PutMessage(ctx, next, message); err != nil {
		t.Fatal(err)
	}
	claimed, err = store.ClaimMessages(ctx, next, true, time.Hour)
	if err != nil || len(claimed) != 1 || claimed[0].ID != "exact" {
		t.Fatal(claimed, err)
	}
}
