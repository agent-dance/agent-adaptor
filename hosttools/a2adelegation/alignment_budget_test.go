package a2adelegation

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync"
	"testing"
	"time"

	adaptor "github.com/agent-dance/agent-adaptor"
	clienta2a "github.com/agent-dance/agent-adaptor/clients/a2a"
	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/activebudget"
)

type alignmentClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*alignmentTimer
}
type alignmentTimer struct {
	fire func()
	stop func()
}

func (t *alignmentTimer) Stop() bool {
	if t.stop != nil {
		t.stop()
	}
	return true
}
func (c *alignmentClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *alignmentClock) AfterFunc(_ time.Duration, f func()) activebudget.Timer {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := &alignmentTimer{fire: f}
	c.timers = append(c.timers, t)
	return t
}
func (c *alignmentClock) advance(d time.Duration) { c.mu.Lock(); c.now = c.now.Add(d); c.mu.Unlock() }
func (c *alignmentClock) timer(i int) *alignmentTimer {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.timers[i]
}

type alignmentClient struct {
	card   func(context.Context) (clienta2a.AgentCard, error)
	send   func(context.Context, clienta2a.SendRequest) (clienta2a.Task, error)
	stream func(context.Context, clienta2a.SendRequest) (A2AStream, error)
	get    func(context.Context, clienta2a.GetTaskRequest) (clienta2a.Task, error)
	cancel func(context.Context, clienta2a.CancelTaskRequest) (clienta2a.Task, error)
}

func (c *alignmentClient) AgentCard(ctx context.Context) (clienta2a.AgentCard, error) {
	if c.card != nil {
		return c.card(ctx)
	}
	return clienta2a.AgentCard{}, nil
}
func (c *alignmentClient) Send(ctx context.Context, r clienta2a.SendRequest) (clienta2a.Task, error) {
	if c.send != nil {
		return c.send(ctx, r)
	}
	return clienta2a.Task{ID: "task", Status: clienta2a.TaskStatus{State: clienta2a.TaskStateCompleted}}, nil
}
func (c *alignmentClient) SendStream(ctx context.Context, r clienta2a.SendRequest) (A2AStream, error) {
	if c.stream != nil {
		return c.stream(ctx, r)
	}
	return nil, errors.New("stream unavailable")
}
func (c *alignmentClient) GetTask(ctx context.Context, r clienta2a.GetTaskRequest) (clienta2a.Task, error) {
	if c.get != nil {
		return c.get(ctx, r)
	}
	return clienta2a.Task{}, errors.New("unavailable")
}
func (c *alignmentClient) CancelTask(ctx context.Context, r clienta2a.CancelTaskRequest) (clienta2a.Task, error) {
	if c.cancel != nil {
		return c.cancel(ctx, r)
	}
	return clienta2a.Task{}, nil
}
func alignmentDelegator(t *testing.T, p DelegationPolicy, c *alignmentClient) *Delegator {
	t.Helper()
	registry, err := NewRegistry(RemoteAgentSpec{Key: "member", AgentCard: &clienta2a.AgentCard{}, Policy: p})
	if err != nil {
		t.Fatal(err)
	}
	d := NewDelegator(registry, NewEventBus(128))
	d.NewClient = func(RemoteAgentSpec) A2AClient { return c }
	return d
}
func TestAlignmentBudgetClampAndBeforeBoundary(t *testing.T) {
	for _, tc := range []struct{ req, max, want time.Duration }{{0, 0, 0}, {10, 0, 10}, {0, 20, 20}, {10, 20, 10}, {30, 20, 20}, {-1, 0, -1}} {
		t.Run(fmt.Sprint(tc), func(t *testing.T) {
			clock := &alignmentClock{now: time.Now()}
			calls := 0
			c := &alignmentClient{card: func(context.Context) (clienta2a.AgentCard, error) { calls++; return clienta2a.AgentCard{}, nil }}
			d := alignmentDelegator(t, DelegationPolicy{MaxActiveExecutionTimeout: tc.max}, c)
			d.budgetClock = clock
			before := 0
			d.LifecycleHook = DelegationLifecycleHookFuncs{BeforeFunc: func(ctx context.Context, _ BeforeDelegation) error {
				before++
				if tc.want > 0 {
					clock.advance(tc.want)
					clock.timer(0).fire()
					<-ctx.Done()
				}
				return nil
			}}
			_, err := d.Delegate(context.Background(), DelegationRequest{RunID: "leader", Agent: "member", ActiveExecutionTimeout: tc.req})
			if tc.want < 0 {
				if delegationErr(err).Code != "invalid_policy" || before != 0 {
					t.Fatalf("invalid=%v before=%d", err, before)
				}
				return
			}
			if tc.want == 0 {
				if err != nil || len(clock.timers) != 0 || calls != 1 {
					t.Fatalf("zero: err=%v timers=%d calls=%d", err, len(clock.timers), calls)
				}
				return
			}
			var limit *adaptor.ActiveExecutionTimeoutError
			if !errors.As(err, &limit) || limit.Limit != tc.want || calls != 0 {
				t.Fatalf("err=%v limit=%v calls=%d", err, limit, calls)
			}
			for _, ev := range drainAvailableBus(t, d.Bus, "leader") {
				if ev.Capability != nil {
					t.Fatal("Before timeout invented invocation")
				}
			}
		})
	}
	if _, err := NewRegistry(RemoteAgentSpec{Key: "member", AgentCard: &clienta2a.AgentCard{}, Policy: DelegationPolicy{MaxActiveExecutionTimeout: -1}}); err == nil {
		t.Fatal("negative registry policy accepted")
	}
}
func TestAlignmentBudgetSealRetryContinuationAndAfter(t *testing.T) {
	clock := &alignmentClock{now: time.Now()}
	sends := 0
	after := 0
	c := &alignmentClient{stream: func(context.Context, clienta2a.SendRequest) (A2AStream, error) {
		clock.advance(40 * time.Millisecond)
		return nil, errors.New("retry polling")
	}, send: func(context.Context, clienta2a.SendRequest) (clienta2a.Task, error) {
		sends++
		clock.advance(50 * time.Millisecond)
		return clienta2a.Task{ID: "same-task", Status: clienta2a.TaskStatus{State: clienta2a.TaskStateCompleted}}, nil
	}}
	d := alignmentDelegator(t, DelegationPolicy{}, c)
	d.budgetClock = clock
	d.LifecycleHook = DelegationLifecycleHookFuncs{AfterFunc: func(context.Context, AfterDelegation) error {
		after++
		clock.advance(time.Hour)
		for _, timer := range clock.timers {
			timer.fire()
		}
		return nil
	}}
	for i := 0; i < 2; i++ {
		out, err := d.Delegate(context.Background(), DelegationRequest{RunID: "leader", Agent: "member", Stream: true, Message: &clienta2a.Message{TaskID: "same-task"}, ActiveExecutionTimeout: 100 * time.Millisecond})
		if err != nil || out.Status != "completed" {
			t.Fatalf("continuation=%d out=%+v err=%v", i, out, err)
		}
	}
	if sends != 2 || after != 2 || len(clock.timers) != 2 {
		t.Fatalf("sends=%d after=%d timers=%d", sends, after, len(clock.timers))
	}
	// An unscheduled timer cannot let the success candidate steal exhausted time.
	d.LifecycleHook = nil
	c.send = func(context.Context, clienta2a.SendRequest) (clienta2a.Task, error) {
		clock.advance(60 * time.Millisecond)
		return clienta2a.Task{ID: "same-task", Status: clienta2a.TaskStatus{State: clienta2a.TaskStateCompleted}}, nil
	}
	_, err := d.Delegate(context.Background(), DelegationRequest{RunID: "leader", Agent: "member", Stream: true, ActiveExecutionTimeout: 100 * time.Millisecond})
	if !errors.Is(err, adaptor.ErrActiveExecutionTimeout) || delegationErr(err).Retryable {
		t.Fatalf("seal=%v", err)
	}
}
func TestAlignmentBudgetSelectedOwnCauseAndParentEvidence(t *testing.T) {
	for _, ownFirst := range []bool{false, true} {
		t.Run(fmt.Sprint(ownFirst), func(t *testing.T) {
			clock := &alignmentClock{now: time.Now()}
			parent, parentCancel := context.WithCancelCause(context.Background())
			defer parentCancel(nil)
			own := &adaptor.ActiveExecutionTimeoutError{Limit: 100 * time.Millisecond}
			inherited := &adaptor.ActiveExecutionTimeoutError{Limit: 777 * time.Second}
			ctx, budget := activebudget.New(parent, own.Limit, own, clock)
			defer budget.Stop()
			r := &delegationRun{ctx: ctx, parent: parent, budget: budget, expired: own}
			if ownFirst {
				clock.timer(0).stop = func() { parentCancel(inherited) }
				clock.advance(own.Limit)
				clock.timer(0).fire()
			} else {
				parentCancel(inherited)
				clock.advance(own.Limit)
				clock.timer(0).fire()
			}
			err := r.complete(nil)
			var observed *adaptor.ActiveExecutionTimeoutError
			if err == nil || !errors.Is(err, inherited) || !errors.As(err, &observed) {
				t.Fatalf("missing graph: %v", err)
			}
			if ownFirst {
				if err.Code != "active_execution_timeout" || observed != own || !errors.Is(err, own) {
					t.Fatalf("own selection: %+v first=%p want=%p", err, observed, own)
				}
			} else if err.Code != "cancelled" || observed != inherited {
				t.Fatalf("parent classified as own: %+v", err)
			}
		})
	}
}
func TestAlignmentBudgetKnownTaskCancelAndPartial(t *testing.T) {
	clock := &alignmentClock{now: time.Now()}
	cancelCount := 0
	c := &alignmentClient{cancel: func(ctx context.Context, req clienta2a.CancelTaskRequest) (clienta2a.Task, error) {
		cancelCount++
		deadline, ok := ctx.Deadline()
		if ctx.Err() != nil || !ok || time.Until(deadline) > 5*time.Second || req.TaskID != "known" {
			t.Errorf("bad cleanup context/request: %v %+v", ctx.Err(), req)
		}
		return clienta2a.Task{}, errors.New("do not expose secret")
	}}
	d := alignmentDelegator(t, DelegationPolicy{}, c)
	d.budgetClock = clock
	d.LifecycleHook = DelegationLifecycleHookFuncs{BeforeFunc: func(ctx context.Context, _ BeforeDelegation) error {
		clock.advance(time.Second)
		clock.timer(0).fire()
		return ctx.Err()
	}}
	out, err := d.Delegate(context.Background(), DelegationRequest{RunID: "leader", Agent: "member", ActiveExecutionTimeout: time.Second, Message: &clienta2a.Message{TaskID: "known"}})
	if !errors.Is(err, adaptor.ErrActiveExecutionTimeout) || cancelCount != 1 || out.Metadata["remote_cancel"] != "failed" || out.RemoteTaskID != "known" {
		t.Fatalf("out=%+v err=%v cancel=%d", out, err, cancelCount)
	}
}
func TestAlignmentBudgetWireNumericBoundary(t *testing.T) {
	for _, value := range []any{int64(1), int64(100), int64(maxBudgetMilliseconds), float64(100)} {
		status := failureStatus(&DelegationError{Code: "active_execution_timeout", Metadata: map[string]any{"limit_ms": value}})
		got, invalid := failureFromStatus(status)
		if got == nil || invalid {
			t.Fatalf("value=%v invalid", value)
		}
		var typed *adaptor.ActiveExecutionTimeoutError
		if !errors.As(got, &typed) || typed.Limit <= 0 {
			t.Fatal("missing typed")
		}
		if value == int64(maxBudgetMilliseconds) && typed.Limit != time.Duration(math.MaxInt64) {
			t.Fatal("overflow")
		}
	}
}

func TestAlignmentBudgetLocalAskKeepsMemberPolicyAndPartialResult(t *testing.T) {
	clock := &alignmentClock{now: time.Now()}
	entered := make(chan struct{})
	policy := adaptor.Policy{ActiveExecutionTimeout: time.Hour, Approvals: adaptor.ApprovalPolicy{Permission: driver.HumanDecisionAsk, Timeout: 10 * time.Minute, OnReject: driver.FailureContinue, MaxRetries: 7}}
	member := adaptor.New(alignmentDriver{run: func(ctx context.Context, req driver.Request, sink driver.EventSink) (driver.Response, error) {
		if req.Policy.HumanDecision.Timeout != policy.Approvals.Timeout || req.Policy.HumanDecision.OnReject != policy.Approvals.OnReject || req.Policy.HumanDecision.MaxRetries != 7 {
			t.Error("delegator replaced Member Policy")
		}
		_, err := sink.(driver.DecisionCapableSink).RequestDecision(ctx, driver.DecisionRequest{Kind: driver.HumanDecisionPermission, Prompt: "wait"})
		return driver.Response{Output: "partial answer", Summary: "partial summary", RawStreams: &driver.RawStreams{Stdout: "raw stdout", Stderr: "raw stderr"}}, err
	}}, adaptor.WithPolicy(policy), adaptor.OnApproval(func(ctx context.Context, _ *adaptor.ApprovalRequest) error {
		close(entered)
		<-ctx.Done()
		return ctx.Err()
	}))
	defer member.Close(context.Background())
	team, err := NewService(Config{Agents: []AgentRef{Local("member", member, Policy{})}})
	if err != nil {
		t.Fatal(err)
	}
	defer team.Close()
	team.delegator.budgetClock = clock
	type answer struct {
		out DelegationResult
		err error
	}
	done := make(chan answer, 1)
	go func() {
		out, err := team.Delegate(context.Background(), DelegationRequest{RunID: "leader", Agent: "member", ActiveExecutionTimeout: 100 * time.Millisecond, IncludeRemoteArtifacts: true})
		done <- answer{out, err}
	}()
	select {
	case <-entered:
	case got := <-done:
		t.Fatalf("did not reach Ask: %+v", got)
	case <-time.After(3 * time.Second):
		t.Fatal("Ask never entered")
	}
	// Member is paused in its own Ask; the independent Delegate keeps charging.
	clock.advance(100 * time.Millisecond)
	clock.timer(0).fire()
	select {
	case got := <-done:
		var partial *adaptor.RunError
		var limit *adaptor.ActiveExecutionTimeoutError
		if !errors.As(got.err, &limit) || limit.Limit != 100*time.Millisecond || got.out.Error.Code != "active_execution_timeout" || !errors.As(got.err, &partial) || partial.Result == nil {
			t.Fatalf("lost budget or partial root error: %+v %v", got.out, got.err)
		}
		if partial.Result.Text != "partial answer" || partial.Result.Summary != "partial summary" || partial.Result.Raw().Stdout != "raw stdout" || partial.Result.Raw().Stderr != "raw stderr" || len(got.out.RemoteArtifacts) != 1 {
			t.Fatalf("partial layers lost: %+v %+v", partial.Result, got.out)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("delegation waited on Member Ask")
	}
}

func TestAlignmentBudgetRemotePartialAndRecoveryUseSameBudget(t *testing.T) {
	for _, recovery := range []bool{false, true} {
		t.Run(fmt.Sprint(recovery), func(t *testing.T) {
			clock := &alignmentClock{now: time.Now()}
			stream := &fakeA2AStream{events: make(chan streamRecv, 2), closed: make(chan struct{})}
			artifact := clienta2a.Artifact{ID: "partial", Parts: []clienta2a.Part{{Kind: clienta2a.PartText, Text: "available before cancellation"}}}
			stream.events <- streamRecv{event: clienta2a.Event{Kind: clienta2a.EventArtifact, TaskID: "known", Artifact: &artifact}}
			if recovery {
				stream.events <- streamRecv{err: errors.New("recover connection")}
			}
			cancels, gets := 0, 0
			client := &alignmentClient{stream: func(context.Context, clienta2a.SendRequest) (A2AStream, error) {
				clock.advance(40 * time.Millisecond)
				return stream, nil
			}, get: func(context.Context, clienta2a.GetTaskRequest) (clienta2a.Task, error) {
				gets++
				clock.advance(60 * time.Millisecond)
				return clienta2a.Task{ID: "known", Status: clienta2a.TaskStatus{State: clienta2a.TaskStateCompleted}}, nil
			}, cancel: func(ctx context.Context, req clienta2a.CancelTaskRequest) (clienta2a.Task, error) {
				cancels++
				if ctx.Err() != nil || req.TaskID != "known" {
					t.Error("cleanup inherited cancellation")
				}
				return clienta2a.Task{}, nil
			}}
			d := alignmentDelegator(t, DelegationPolicy{}, client)
			d.budgetClock = clock
			if !recovery {
				d.beforePublish = func(ev DelegationEvent) {
					if ev.Kind == DelegationArtifactCreated {
						clock.advance(60 * time.Millisecond)
						clock.timer(0).fire()
					}
				}
			}
			out, err := d.Delegate(context.Background(), DelegationRequest{RunID: "leader", Agent: "member", Stream: true, ActiveExecutionTimeout: 100 * time.Millisecond, IncludeRemoteArtifacts: true})
			if !errors.Is(err, adaptor.ErrActiveExecutionTimeout) || out.Error.Code != "active_execution_timeout" || len(clock.timers) != 1 || cancels != 1 || len(out.RemoteArtifacts) != 1 || out.RemoteArtifacts[0].Parts[0].Text != artifact.Parts[0].Text {
				t.Fatalf("partial/budget lost: %+v %v timers=%d cancels=%d", out, err, len(clock.timers), cancels)
			}
			if recovery && gets != 1 {
				t.Fatalf("recovery calls=%d", gets)
			}
		})
	}
}
