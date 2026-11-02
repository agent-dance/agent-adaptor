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

func TestCancelWhileLeaderIdleKeepsTaskInPartialResult(t *testing.T) {
	ctx := testContext(t)
	idle := make(chan struct{})
	workerReady := make(chan struct{})
	store := &idleObservedStore{MemoryStore: NewMemoryStore(), idle: idle}
	var team *Team
	worker := adaptor.New(&fakeDriver{run: func(ctx context.Context, _ driver.Request, sink driver.EventSink) (driver.Response, error) {
		if err := sink.Emit(driver.RunEvent{Type: driver.RunEventTurnCompleted, Turn: &driver.TurnCompleted{Index: 0, Text: "partial worker", Usage: &driver.Usage{InputTokens: 7}}}); err != nil {
			return driver.Response{}, err
		}
		close(workerReady)
		<-ctx.Done()
		return driver.Response{}, ctx.Err()
	}})
	leader := adaptor.New(&fakeDriver{run: func(_ context.Context, req driver.Request, _ driver.EventSink) (driver.Response, error) {
		_, err := call(team, req, "delegate", map[string]any{"member": "worker", "objective": "wait"})
		return answer("delegated", 1), err
	}})
	team = fixture(t, Config{Leader: leader, Store: store, Members: []Member{{Key: "worker", Runner: worker}}})
	stream := team.Thread("cancel-result").Send(ctx, "start")
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		for range stream.Events() {
		}
	}()
	select {
	case <-idle:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	select {
	case <-workerReady:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	stream.Cancel()
	<-drained
	result, err := stream.Result()
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Cancel error=%v", err)
	}
	tasks, err := store.Tasks(ctx, "cancel-result")
	if err != nil || len(tasks) != 1 || tasks[0].State != Cancelled {
		t.Fatalf("tasks=%+v err=%v", tasks, err)
	}
	if got := result.Subagents(); len(got) != 1 || got[0].State != string(Cancelled) {
		t.Fatalf("partial Result lost cancelled task: Subagents=%+v; persisted task=%s", got, tasks[0].State)
	}
	if got := result.Subagents()[0].Usage; got == nil || got.InputTokens != 7 {
		t.Fatalf("cancelled task lost observed usage: %+v", got)
	}
	if result.Usage == nil || result.Usage.InputTokens != 8 {
		t.Fatalf("partial result must count leader and worker usage once: %+v", result.Usage)
	}
}

func TestLeaderRetryRetainsOriginalObjective(t *testing.T) {
	ctx := testContext(t)
	const objective = "Implement the payment reconciliation endpoint for account 48217"
	var calls atomic.Int32
	var retryPrompt string
	leader := adaptor.New(&fakeDriver{run: func(_ context.Context, req driver.Request, _ driver.EventSink) (driver.Response, error) {
		if calls.Add(1) == 1 {
			return driver.Response{}, temporaryFailure{}
		}
		retryPrompt = req.Prompt
		return answer("recovered", 1), nil
	}})
	team := fixture(t, Config{Leader: leader})
	events, result, err := collect(t, team.Thread("retry-objective").Send(ctx, objective, adaptor.MessageID("original-objective")))
	if err != nil {
		t.Fatal(err)
	}
	originalReceipt := false
	for _, message := range result.Messages() {
		if message.ID == "original-objective" {
			originalReceipt = message.State == adaptor.MessageDelivered && message.Text == objective && message.Turn == 2
		}
	}
	if !strings.Contains(retryPrompt, objective) || !originalReceipt {
		t.Fatalf("leader retry has no checkpoint and lost original objective; provider calls=%d, original ID in Result.Messages=%v", calls.Load(), originalReceipt)
	}
	var states []adaptor.MessageState
	for _, event := range events {
		if update, ok := event.(adaptor.MessageUpdate); ok && update.ID == "original-objective" && update.State != adaptor.MessageAccepted {
			states = append(states, update.State)
			if update.State == adaptor.MessageFailed && (update.Reason != adaptor.MessageRunFailed || update.Text != objective) {
				t.Fatalf("untruthful failure receipt: %+v", update)
			}
		}
	}
	if len(states) != 2 || states[0] != adaptor.MessageFailed || states[1] != adaptor.MessageDelivered {
		t.Fatalf("retry receipts=%v", states)
	}
	if calls.Load() != 2 || result.Turns != 2 || result.Usage.InputTokens != 1 {
		t.Fatalf("unexpected retry accounting: calls=%d result=%+v", calls.Load(), result)
	}
}

func TestLeaderRetryRetainsUndeliveredHostInputs(t *testing.T) {
	ctx := testContext(t)
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
			return driver.Response{}, temporaryFailure{}
		}
		return answer("recovered", 1), nil
	}})
	store := NewMemoryStore()
	team := fixture(t, Config{Leader: leader, Store: store})
	stream := team.Thread("retry-inputs").Send(ctx, "original objective", adaptor.MessageID("objective"))
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if err := stream.Send(ctx, "additional constraint", adaptor.MessageID("constraint")); err != nil {
		t.Fatal(err)
	}
	close(release)
	events, result, err := collect(t, stream)
	if err != nil {
		t.Fatal(err)
	}
	for id, text := range map[string]string{"objective": "original objective", "constraint": "additional constraint"} {
		failed, delivered := false, false
		for _, event := range events {
			if update, ok := event.(adaptor.MessageUpdate); ok && update.ID == id {
				if update.State == adaptor.MessageFailed {
					failed = update.Reason == adaptor.MessageRunFailed && update.Text == text
				}
				if update.State == adaptor.MessageDelivered {
					delivered = failed
				}
			}
		}
		if !failed || !delivered {
			t.Fatalf("input %s lost its truthful retry receipts: failed=%v delivered=%v", id, failed, delivered)
		}
		found := false
		for _, outcome := range result.Messages() {
			if outcome.ID == id {
				found = outcome.State == adaptor.MessageDelivered && outcome.Text == text
			}
		}
		if !found {
			t.Fatalf("input %s missing from final outcomes: %+v", id, result.Messages())
		}
	}
	if len(result.Undelivered()) != 0 {
		t.Fatalf("recovered inputs still reported undelivered: %+v", result.Undelivered())
	}
	messages, err := store.Messages(ctx, "retry-inputs")
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range messages {
		if message.Host && message.State != Delivered {
			t.Fatalf("recovered input not persisted as delivered: %+v", message)
		}
	}
}

func TestFailureAndLimitKeepFinalTaskSnapshot(t *testing.T) {
	leaderFailure := errors.New("leader unavailable")
	for _, limit := range []bool{false, true} {
		name := "leader_failure"
		if limit {
			name = "leader_turn_limit"
		}
		t.Run(name, func(t *testing.T) {
			ctx := testContext(t)
			workerReady := make(chan struct{})
			worker := adaptor.New(&fakeDriver{run: func(ctx context.Context, _ driver.Request, sink driver.EventSink) (driver.Response, error) {
				if err := sink.Emit(driver.RunEvent{Type: driver.RunEventTurnCompleted, Turn: &driver.TurnCompleted{Index: 0, Text: "partial worker", Usage: &driver.Usage{InputTokens: 7}}}); err != nil {
					return driver.Response{}, err
				}
				close(workerReady)
				<-ctx.Done()
				return driver.Response{}, ctx.Err()
			}})
			var team *Team
			leader := adaptor.New(&fakeDriver{run: func(ctx context.Context, request driver.Request, sink driver.EventSink) (driver.Response, error) {
				if _, err := call(team, request, "delegate", map[string]any{"member": "worker", "objective": "work until cancelled"}); err != nil {
					return driver.Response{}, err
				}
				select {
				case <-workerReady:
				case <-ctx.Done():
					return driver.Response{}, ctx.Err()
				}
				if limit {
					return answer("leader reached limit", 1), nil
				}
				if err := sink.Emit(driver.RunEvent{Type: driver.RunEventTurnCompleted, Turn: &driver.TurnCompleted{Index: 0, Text: "partial leader", Usage: &driver.Usage{InputTokens: 1}}}); err != nil {
					return driver.Response{}, err
				}
				return driver.Response{}, leaderFailure
			}})
			limits := Limits{}
			wantErr := leaderFailure
			if limit {
				limits.MaxLeaderTurns = 1
				wantErr = ErrLimitExceeded
			}
			team = fixture(t, Config{Leader: leader, Members: []Member{{Key: "worker", Runner: worker}}, Limits: limits})
			stream := team.Thread("snapshot").Send(ctx, "start")
			events, result, err := collect(t, stream)
			if !errors.Is(err, wantErr) || result == nil {
				t.Fatalf("result=%+v error=%v, want %v", result, err, wantErr)
			}
			tasks := result.Subagents()
			if len(tasks) != 1 || tasks[0].State != string(Cancelled) || tasks[0].Usage == nil || tasks[0].Usage.InputTokens != 7 {
				t.Fatalf("final task snapshot=%+v", tasks)
			}
			if result.Usage == nil || result.Usage.InputTokens != 8 {
				t.Fatalf("observed leader/worker usage must be counted once: %+v", result.Usage)
			}
			final, ok := events[len(events)-1].(adaptor.Done)
			if !ok || final.Result != result || final.Err != err {
				t.Fatalf("Done did not preserve final result/error: %+v", final)
			}
		})
	}
}
