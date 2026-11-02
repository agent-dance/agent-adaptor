package team

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	adaptor "github.com/agent-dance/agent-adaptor/v2"
	"github.com/agent-dance/agent-adaptor/v2/driver"
)

func TestResultOnlyUsesMemberApprovalFallback(t *testing.T) {
	ctx := testContext(t)
	var team *Team
	var turns atomic.Int32
	var asked atomic.Bool
	worker := &fakeDriver{run: func(ctx context.Context, _ driver.Request, sink driver.EventSink) (driver.Response, error) {
		asked.Store(true)
		_, err := sink.(driver.DecisionCapableSink).RequestDecision(ctx, driver.DecisionRequest{RequestID: "permission", Kind: driver.HumanDecisionPermission, Prompt: "permission"})
		if err != nil {
			return driver.Response{}, err
		}
		return answer("member policy continued", 1), nil
	}}
	leader := &fakeDriver{run: func(_ context.Context, request driver.Request, _ driver.EventSink) (driver.Response, error) {
		if turns.Add(1) == 1 {
			if _, err := call(team, request, "delegate", map[string]any{"member": "worker", "objective": "work"}); err != nil {
				return driver.Response{}, err
			}
		}
		return answer("leader", 1), nil
	}}
	policy := adaptor.Policy{Approvals: adaptor.ApprovalPolicy{Permission: adaptor.ApprovalAsk, Timeout: -1, OnTimeout: adaptor.FallbackContinue}}
	team = fixture(t, Config{Leader: adaptor.New(leader), Members: []Member{{Key: "worker", Runner: adaptor.New(worker, adaptor.WithPolicy(policy))}}})
	stream := team.Send(ctx, "start")
	result, err := stream.Result()
	if err != nil || !asked.Load() || len(result.Subagents()) != 1 || result.Subagents()[0].State != string(Succeeded) {
		t.Fatalf("result=%+v asked=%v err=%v", result, asked.Load(), err)
	}
	collect(t, stream)
}

func TestPerScopeSerializesConversationWriters(t *testing.T) {
	ctx := testContext(t)
	var team *Team
	var leaderCalls atomic.Int32
	var workerCalls atomic.Int32
	worker := &fakeDriver{run: func(_ context.Context, request driver.Request, _ driver.EventSink) (driver.Response, error) {
		if workerCalls.Add(1) == 2 && (request.Session == nil || request.Session.State.ResumeID != "healthy") {
			t.Error("PerScope did not resume")
		}
		return answer("worker", 1), nil
	}}
	leader := &fakeDriver{run: func(_ context.Context, request driver.Request, _ driver.EventSink) (driver.Response, error) {
		if leaderCalls.Add(1) == 1 {
			for index := 0; index < 2; index++ {
				if _, err := call(team, request, "delegate", map[string]any{"member": "worker", "objective": "work"}); err != nil {
					return driver.Response{}, err
				}
			}
		}
		return answer("leader", 1), nil
	}}
	store := NewMemoryStore()
	team = fixture(t, Config{Leader: adaptor.New(leader), Store: store, Members: []Member{{Key: "worker", Runner: adaptor.New(worker), Continuity: PerScope, MaxActive: 2}}})
	if _, _, err := collect(t, team.Thread("scope").Send(ctx, "start")); err != nil {
		t.Fatal(err)
	}
	tasks, err := store.Tasks(ctx, "scope")
	if err != nil || len(tasks) != 2 || tasks[0].Conversation != EncodeKey("scope", "member", "worker") || tasks[0].Conversation != tasks[1].Conversation {
		t.Fatalf("tasks=%+v err=%v", tasks, err)
	}
}

func TestStandbyOwnsIdleScopeAndNoConsumerBuffer(t *testing.T) {
	ctx := testContext(t)
	leader := &fakeDriver{run: func(_ context.Context, _ driver.Request, _ driver.EventSink) (driver.Response, error) {
		return answer("leader", 1), nil
	}}
	store := NewMemoryStore()
	team := fixture(t, Config{Leader: adaptor.New(leader), Store: store})
	other := fixture(t, Config{Leader: adaptor.New(leader), Store: store})
	lifetime, cancel := context.WithCancel(ctx)
	waiting, err := team.Thread("scope").Standby(lifetime)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := other.Thread("scope").Send(ctx, "conflict").Result(); !errors.Is(err, ErrThreadBusy) {
		t.Fatalf("idle standby did not fence scope: %v", err)
	}
	internal := waiting.(*standby)
	internal.log.push(adaptor.Notice{Kind: "not-buffered"})
	internal.log.mu.Lock()
	queued := len(internal.log.queue)
	internal.log.mu.Unlock()
	if queued != 0 {
		t.Fatal("standby buffered without consumer")
	}
	cancel()
	<-internal.done
	for range waiting.Events() {
		t.Error("unexpected buffered event")
	}
	if _, err := other.Thread("scope").Send(ctx, "after release").Result(); err != nil {
		t.Fatal(err)
	}
}

func TestInitialMessageIDDoesNotDedupeLaterHostMessage(t *testing.T) {
	ctx := testContext(t)
	var team *Team
	var turns atomic.Int32
	workerStarted := make(chan struct{})
	release := make(chan struct{})
	idle := make(chan struct{})
	hostSeen := make(chan struct{})
	worker := &fakeDriver{run: func(ctx context.Context, _ driver.Request, _ driver.EventSink) (driver.Response, error) {
		close(workerStarted)
		select {
		case <-release:
			return answer("worker", 1), nil
		case <-ctx.Done():
			return driver.Response{}, ctx.Err()
		}
	}}
	leader := &fakeDriver{run: func(_ context.Context, request driver.Request, _ driver.EventSink) (driver.Response, error) {
		switch turns.Add(1) {
		case 1:
			if _, err := call(team, request, "delegate", map[string]any{"member": "worker", "objective": "work"}); err != nil {
				return driver.Response{}, err
			}
		case 2:
			if !strings.Contains(request.Prompt, "new host message") {
				t.Error("host message lost")
			}
			close(hostSeen)
		}
		return answer("leader", 1), nil
	}}
	team = fixture(t, Config{Leader: adaptor.New(leader), Members: []Member{{Key: "worker", Runner: adaptor.New(worker)}}})
	stream := team.Send(ctx, "initial", adaptor.MessageID("initial-id"))
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		for event := range stream.Events() {
			if turn, ok := event.(adaptor.TurnEnd); ok && turn.Turn == 1 {
				close(idle)
			}
		}
	}()
	<-workerStarted
	<-idle
	if err := stream.Send(ctx, "new host message"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-hostSeen:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	close(release)
	<-drained
	if _, err := stream.Result(); err != nil {
		t.Fatal(err)
	}
}

func TestOversizedNotificationsAreReadableScopedArtifacts(t *testing.T) {
	ctx := testContext(t)
	var team *Team
	var turns atomic.Int32
	large := strings.Repeat("content", 12_000)
	worker := &fakeDriver{run: func(_ context.Context, _ driver.Request, _ driver.EventSink) (driver.Response, error) {
		return answer(large, 1), nil
	}}
	leader := &fakeDriver{run: func(_ context.Context, request driver.Request, _ driver.EventSink) (driver.Response, error) {
		if turns.Add(1) == 1 {
			if _, err := call(team, request, "delegate", map[string]any{"member": "worker", "objective": "work"}); err != nil {
				return driver.Response{}, err
			}
		} else {
			if len(request.Prompt) >= 64<<10 || !strings.Contains(request.Prompt, "team://") {
				t.Error("oversized content was inlined or lost")
			}
			listed, status, err := rpc(team, tokenFor(request, team), "resources/list", map[string]any{})
			if err != nil || status != http.StatusOK {
				return driver.Response{}, errors.New("cannot list artifacts")
			}
			resources := listed["result"].(map[string]any)["resources"].([]any)
			if len(resources) == 0 {
				t.Error("no resource")
			}
			found := false
			for _, resource := range resources {
				read, _, err := rpc(team, tokenFor(request, team), "resources/read", map[string]any{"uri": resource.(map[string]any)["uri"]})
				if err != nil {
					return driver.Response{}, err
				}
				text := read["result"].(map[string]any)["contents"].([]any)[0].(map[string]any)["text"].(string)
				if strings.Contains(text, large) {
					found = true
				}
			}
			if !found {
				t.Error("artifact content incomplete")
			}
		}
		return answer("leader", 1), nil
	}}
	team = fixture(t, Config{Leader: adaptor.New(leader), Members: []Member{{Key: "worker", Runner: adaptor.New(worker)}}})
	if _, _, err := collect(t, team.Send(ctx, "start")); err != nil {
		t.Fatal(err)
	}
}

func TestEventLogDroppedAccountingAndTerminalRetention(t *testing.T) {
	log := newEventLog("run", "scope", false)
	for index := 0; index < 600; index++ {
		log.push(adaptor.TextDelta{Text: "chunk"})
	}
	result := &adaptor.Result{}
	log.push(adaptor.Done{Result: result})
	log.finish()
	dropped := 0
	previous := uint64(0)
	var last adaptor.Event
	for event := range log.events() {
		if event.Meta().Sequence <= previous {
			t.Fatal("unordered event")
		}
		previous = event.Meta().Sequence
		if marker, ok := event.(adaptor.Dropped); ok {
			dropped += marker.Count
			if marker.Count != marker.ByKind["text"] || marker.LastSequence-marker.FirstSequence+1 != uint64(marker.Count) {
				t.Fatalf("marker=%+v", marker)
			}
		}
		last = event
	}
	if dropped != 344 || last.(adaptor.Done).Result != result {
		t.Fatalf("dropped=%d last=%v", dropped, last)
	}
}

func TestNewListenerAndClose(t *testing.T) {
	leader := adaptor.New(&fakeDriver{run: func(_ context.Context, _ driver.Request, _ driver.EventSink) (driver.Response, error) {
		return answer("leader", 1), nil
	}})
	team, err := New(Config{Leader: leader})
	if errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EACCES) {
		t.Skipf("loopback listener denied by sandbox: %v", err)
	}
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := team.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if err := team.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := team.Send(ctx, "closed").Result(); !errors.Is(err, adaptor.ErrAgentClosed) {
		t.Fatal(err)
	}
}

func TestStoreTaskAndWakeCommitAtomically(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	lease, err := store.AcquireScope(ctx, "scope", "owner", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	task, err := store.SaveTask(ctx, lease, Task{Scope: "scope", ID: "task", State: Queued})
	if err != nil {
		t.Fatal(err)
	}
	stale := task
	task.State = Running
	task, err = store.SaveTask(ctx, lease, task)
	if err != nil {
		t.Fatal(err)
	}
	message := InboxMessage{Scope: "scope", ID: "terminal", Task: "task", Text: "succeeded", Wake: true}
	stale.State = Succeeded
	if _, err := store.SaveTask(ctx, lease, stale, message); !errors.Is(err, ErrConflict) {
		t.Fatal("stale task admitted notification")
	}
	messages, err := store.Messages(ctx, "scope")
	if err != nil || len(messages) != 0 {
		t.Fatalf("messages=%v err=%v", messages, err)
	}
	task.State = Succeeded
	if _, err := store.SaveTask(ctx, lease, task, message); err != nil {
		t.Fatal(err)
	}
	messages, err = store.Messages(ctx, "scope")
	if err != nil || len(messages) != 1 || messages[0].State != Pending {
		t.Fatalf("messages=%v err=%v", messages, err)
	}
}

func TestMessageBudgetNoticeIncludesSystemMessage(t *testing.T) {
	ctx := testContext(t)
	var team *Team
	worker := &fakeDriver{cap: driver.MessageCapability{Boundaries: []driver.Boundary{driver.BoundaryTool}}, run: func(ctx context.Context, request driver.Request, _ driver.EventSink) (driver.Response, error) {
		request.Messages.Window(true)
		<-ctx.Done()
		return driver.Response{}, ctx.Err()
	}}
	leader := &fakeDriver{cap: driver.MessageCapability{Boundaries: []driver.Boundary{driver.BoundaryTool}}, run: func(ctx context.Context, request driver.Request, _ driver.EventSink) (driver.Response, error) {
		request.Messages.Window(true)
		task, err := call(team, request, "delegate", map[string]any{"member": "worker", "objective": "work"})
		if err != nil {
			return driver.Response{}, err
		}
		for index := 0; index < 8; index++ {
			if _, err := call(team, request, "send", map[string]any{"task": task["task"], "text": "steering"}); err != nil {
				return driver.Response{}, err
			}
		}
		select {
		case <-request.Messages.Signal():
		case <-ctx.Done():
			return driver.Response{}, ctx.Err()
		}
		messages := request.Messages.Claim(driver.BoundaryTool)
		if len(messages) == 0 {
			t.Error("no system budget message")
		}
		for _, message := range messages {
			request.Messages.Ack(message.ID, driver.MessageAck{Delivered: true, Mechanism: "fake.budget"})
		}
		if _, err := call(team, request, "cancel", map[string]any{"task": task["task"], "reason": "complete"}); err != nil {
			return driver.Response{}, err
		}
		return answer("wrapped up", 1), nil
	}}
	team = fixture(t, Config{Leader: adaptor.New(leader), Members: []Member{{Key: "worker", Runner: adaptor.New(worker)}}, Limits: Limits{MaxSenderMessages: 20}})
	events, result, err := collect(t, team.Send(ctx, "start"))
	if err != nil {
		t.Fatal(err)
	}
	notice := false
	for _, event := range events {
		if update, ok := event.(adaptor.Notice); ok && update.Kind == "limit.approaching" {
			notice = true
		}
	}
	if !notice {
		t.Error("missing 80 percent notice")
	}
	system := false
	for _, message := range result.Messages() {
		if message.From.Kind == adaptor.AuthorSystem && message.State == adaptor.MessageDelivered {
			system = true
		}
	}
	if !system {
		t.Error("notice was not accompanied by system delivery")
	}
}

func TestWorkerQuestionWakesLeaderAndReplySteersWorker(t *testing.T) {
	ctx := testContext(t)
	var team *Team
	var turns atomic.Int32
	ask := make(chan struct{})
	idle := make(chan struct{})
	taskID := make(chan string, 1)
	worker := &fakeDriver{cap: driver.MessageCapability{Boundaries: []driver.Boundary{driver.BoundaryTool}}, run: func(ctx context.Context, request driver.Request, _ driver.EventSink) (driver.Response, error) {
		request.Messages.Window(true)
		select {
		case <-ask:
		case <-ctx.Done():
			return driver.Response{}, ctx.Err()
		}
		if _, err := call(team, request, "send", map[string]any{"text": "which design?", "expect_reply": true}); err != nil {
			return driver.Response{}, err
		}
		select {
		case <-request.Messages.Signal():
		case <-ctx.Done():
			return driver.Response{}, ctx.Err()
		}
		messages := request.Messages.Claim(driver.BoundaryTool)
		if len(messages) != 1 || !strings.Contains(messages[0].Text, "use design A") {
			t.Errorf("reply=%v", messages)
		}
		for _, message := range messages {
			request.Messages.Ack(message.ID, driver.MessageAck{Delivered: true, Mechanism: "fake.reply"})
		}
		return answer("worker finished", 1), nil
	}}
	leader := &fakeDriver{run: func(_ context.Context, request driver.Request, _ driver.EventSink) (driver.Response, error) {
		switch turns.Add(1) {
		case 1:
			task, err := call(team, request, "delegate", map[string]any{"member": "worker", "objective": "work"})
			if err != nil {
				return driver.Response{}, err
			}
			taskID <- task["task"].(string)
		case 2:
			if !strings.Contains(request.Prompt, "which design?") {
				t.Error("question did not wake leader")
			}
			if _, err := call(team, request, "send", map[string]any{"task": <-taskID, "text": "use design A"}); err != nil {
				return driver.Response{}, err
			}
		}
		return answer("leader", 1), nil
	}}
	team = fixture(t, Config{Leader: adaptor.New(leader), Members: []Member{{Key: "worker", Runner: adaptor.New(worker)}}})
	stream := team.Send(ctx, "start")
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		for event := range stream.Events() {
			if turn, ok := event.(adaptor.TurnEnd); ok && turn.Turn == 1 {
				close(idle)
			}
		}
	}()
	<-idle
	close(ask)
	<-drained
	if result, err := stream.Result(); err != nil || result.Subagents()[0].State != string(Succeeded) || turns.Load() < 2 {
		t.Fatalf("result=%+v turns=%d err=%v", result, turns.Load(), err)
	}
}

type acquireGate struct {
	*MemoryStore
	entered chan struct{}
	release chan struct{}
}

func (store *acquireGate) AcquireScope(ctx context.Context, scope, owner string, ttl time.Duration) (ScopeLease, error) {
	close(store.entered)
	select {
	case <-store.release:
		return store.MemoryStore.AcquireScope(ctx, scope, owner, ttl)
	case <-ctx.Done():
		return ScopeLease{}, ctx.Err()
	}
}

func TestMessageAdmissionCancellationWhileScopeAcquisitionBlocked(t *testing.T) {
	ctx := testContext(t)
	var calls atomic.Int32
	leader := &fakeDriver{run: func(_ context.Context, request driver.Request, _ driver.EventSink) (driver.Response, error) {
		calls.Add(1)
		if strings.Contains(request.Prompt, "withdrawn") {
			t.Error("cancelled admission reached leader")
		}
		return answer("leader", 1), nil
	}}
	store := &acquireGate{MemoryStore: NewMemoryStore(), entered: make(chan struct{}), release: make(chan struct{})}
	team := fixture(t, Config{Leader: adaptor.New(leader), Store: store})
	stream := team.Thread("scope").Send(ctx, "start")
	<-store.entered
	messageCtx, cancel := context.WithCancel(ctx)
	sent := make(chan error, 1)
	go func() { sent <- stream.Send(messageCtx, "withdrawn") }()
	run := stream.(*execution)
	var command any
	select {
	case command = <-run.commands:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	run.commands <- command
	cancel()
	select {
	case err := <-sent:
		var message *adaptor.MessageError
		if !errors.Is(err, context.Canceled) || !errors.As(err, &message) || message.Text != "withdrawn" {
			t.Fatalf("admission=%v", err)
		}
	case <-ctx.Done():
		t.Fatal("Send did not respect admission context")
	}
	close(store.release)
	if _, _, err := collect(t, stream); err != nil || calls.Load() != 1 {
		t.Fatalf("calls=%d err=%v", calls.Load(), err)
	}
}

func TestCloseCancelsStandbyScopeAcquisition(t *testing.T) {
	ctx := testContext(t)
	leader := &fakeDriver{run: func(_ context.Context, _ driver.Request, _ driver.EventSink) (driver.Response, error) {
		return answer("leader", 1), nil
	}}
	store := &acquireGate{MemoryStore: NewMemoryStore(), entered: make(chan struct{}), release: make(chan struct{})}
	team := fixture(t, Config{Leader: adaptor.New(leader), Store: store})
	returned := make(chan error, 1)
	go func() { _, err := team.Thread("scope").Standby(ctx); returned <- err }()
	<-store.entered
	if err := team.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-returned; !errors.Is(err, context.Canceled) {
		t.Fatalf("Standby=%v", err)
	}
}

type detachGate struct {
	entered chan struct{}
	release chan struct{}
	calls   atomic.Int32
}

func (*detachGate) AttachRun(context.Context, string) (adaptor.RunAttachment, error) {
	return adaptor.RunAttachment{}, nil
}
func (gate *detachGate) DetachRun(ctx context.Context, _ string) error {
	if gate.calls.Add(1) != 1 {
		return nil
	}
	close(gate.entered)
	select {
	case <-gate.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func TestNotificationDuringLeaderFinalizationIsNotLost(t *testing.T) {
	ctx := testContext(t)
	var team *Team
	var turns atomic.Int32
	ask := make(chan struct{})
	taskID := make(chan string, 1)
	gate := &detachGate{entered: make(chan struct{}), release: make(chan struct{})}
	worker := &fakeDriver{cap: driver.MessageCapability{Boundaries: []driver.Boundary{driver.BoundaryTool}}, run: func(ctx context.Context, request driver.Request, _ driver.EventSink) (driver.Response, error) {
		request.Messages.Window(true)
		select {
		case <-ask:
		case <-ctx.Done():
			return driver.Response{}, ctx.Err()
		}
		if _, err := call(team, request, "send", map[string]any{"text": "question during finalization"}); err != nil {
			return driver.Response{}, err
		}
		select {
		case <-request.Messages.Signal():
		case <-ctx.Done():
			return driver.Response{}, ctx.Err()
		}
		for _, message := range request.Messages.Claim(driver.BoundaryTool) {
			request.Messages.Ack(message.ID, driver.MessageAck{Delivered: true, Mechanism: "fake.reply"})
		}
		return answer("worker", 1), nil
	}}
	leader := &fakeDriver{run: func(_ context.Context, request driver.Request, _ driver.EventSink) (driver.Response, error) {
		switch turns.Add(1) {
		case 1:
			task, err := call(team, request, "delegate", map[string]any{"member": "worker", "objective": "work"})
			if err != nil {
				return driver.Response{}, err
			}
			taskID <- task["task"].(string)
		case 2:
			if !strings.Contains(request.Prompt, "question during finalization") {
				t.Error("notification lost across finalization")
			}
			if _, err := call(team, request, "send", map[string]any{"task": <-taskID, "text": "answer"}); err != nil {
				return driver.Response{}, err
			}
		}
		return answer("leader", 1), nil
	}}
	team = fixture(t, Config{Leader: adaptor.New(leader, adaptor.WithRunServices(gate)), Members: []Member{{Key: "worker", Runner: adaptor.New(worker)}}})
	stream := team.Send(ctx, "start")
	released := false
	retried := false
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		for event := range stream.Events() {
			release := false
			switch typed := event.(type) {
			case adaptor.MessageUpdate:
				release = typed.State == adaptor.MessageAccepted && strings.HasPrefix(typed.ID, "team-")
			case adaptor.Notice:
				if typed.Kind == "team.notification_retry" {
					retried = true
					release = true
				}
			}
			if release && !released {
				released = true
				close(gate.release)
			}
		}
	}()
	<-gate.entered
	close(ask)
	<-drained
	result, err := stream.Result()
	if err != nil || !retried || result.Subagents()[0].State != string(Succeeded) {
		t.Fatalf("retried=%v result=%+v err=%v", retried, result, err)
	}
}

func TestAdvertisedToolSchemasCompile(t *testing.T) {
	for _, size := range []int{0, 2} {
		leader := adaptor.New(&fakeDriver{run: func(_ context.Context, _ driver.Request, _ driver.EventSink) (driver.Response, error) {
			return answer("leader", 1), nil
		}})
		var members []Member
		for index := 0; index < size; index++ {
			members = append(members, Member{Key: string(rune('a' + index)), Runner: leader})
		}
		team := fixture(t, Config{Leader: leader, Members: members})
		grant := capability{Actor: "leader", Tools: []string{"team_delegate", "team_send", "team_status", "team_cancel"}}
		for _, tool := range team.tools(grant) {
			if _, _, err := compileSchema(tool["inputSchema"].(map[string]any)); err != nil {
				t.Fatalf("%s: %v", tool["name"], err)
			}
		}
	}
}
