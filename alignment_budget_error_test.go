package adaptor

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agent-dance/agent-adaptor/capability"
	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/activebudget"
	"github.com/agent-dance/agent-adaptor/internal/engine"
	"github.com/agent-dance/agent-adaptor/memory"
	"github.com/agent-dance/agent-adaptor/threadstore"
)

type alignmentClock struct {
	mu     sync.Mutex
	now    time.Time
	next   int
	timers map[int]*alignmentTimer
	paused chan struct{}
}
type alignmentTimer struct {
	c      *alignmentClock
	id     int
	at     time.Time
	fn     func()
	active bool
}

func (c *alignmentClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *alignmentClock) AfterFunc(d time.Duration, f func()) activebudget.Timer {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.next++
	tm := &alignmentTimer{c, c.next, c.now.Add(d), f, true}
	c.timers[tm.id] = tm
	return tm
}
func (tm *alignmentTimer) Stop() bool {
	c := tm.c
	c.mu.Lock()
	defer c.mu.Unlock()
	old := tm.active
	tm.active = false
	if old && c.paused != nil {
		select {
		case c.paused <- struct{}{}:
		default:
		}
	}
	return old
}
func newAlignmentClock() *alignmentClock {
	return &alignmentClock{now: time.Now(), timers: map[int]*alignmentTimer{}, paused: make(chan struct{}, 32)}
}
func (c *alignmentClock) advance(d time.Duration) {
	if d < 0 {
		panic("negative advance")
	}
	c.mu.Lock()
	end := c.now.Add(d)
	c.mu.Unlock()
	for {
		c.mu.Lock()
		var due []*alignmentTimer
		for _, tm := range c.timers {
			if tm.active && !tm.at.After(end) {
				due = append(due, tm)
			}
		}
		sort.Slice(due, func(i, j int) bool {
			if due[i].at.Equal(due[j].at) {
				return due[i].id < due[j].id
			}
			return due[i].at.Before(due[j].at)
		})
		if len(due) == 0 {
			c.now = end
			c.mu.Unlock()
			return
		}
		tm := due[0]
		c.now = tm.at
		tm.active = false
		c.mu.Unlock()
		tm.fn()
	}
}
func alignmentClockOption(c *alignmentClock) SharedOption {
	return sharedOptionFunc(func(s *RunSettings) { s.budgetTiming.clock = c })
}
func alignmentBudgetPartial() driver.Response {
	return driver.Response{Output: "partial", Summary: "short", RawStreams: &driver.RawStreams{Stdout: "raw", Stderr: "stderr", Terminal: &driver.TerminalPayload{Event: "official", JSON: []byte(`{"seen":true}`)}}, Transcript: []driver.TranscriptItem{{Kind: driver.TranscriptAssistant, Text: "partial"}}, Usage: &driver.Usage{InputTokens: 2}}
}
func alignmentBudgetCarrier(t *testing.T, res *Result, err error, reason FailureReason) {
	t.Helper()
	var re *RunError
	if res != nil || !errors.As(err, &re) || re.Reason != reason || re.Result == nil || re.Result.Raw().Stdout != "raw" || re.Result.Text != "partial" || len(re.Result.Transcript()) != 1 {
		t.Fatalf("outcome=%#v, %#v (carrier=%#v)", res, err, re)
	}
	if reason == ReasonActiveExecutionTimeout {
		var limit *ActiveExecutionTimeoutError
		if !errors.Is(err, ErrActiveExecutionTimeout) || !errors.As(err, &limit) || limit.Limit != 100*time.Millisecond {
			t.Fatalf("missing typed limit: %v", err)
		}
	}
}

func TestAlignmentActiveBudgetExactAsk(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		for _, spawn := range []bool{false, true} {
			t.Run(fmt.Sprintf("stream=%v/spawn=%v", streaming, spawn), func(t *testing.T) {
				c := newAlignmentClock()
				d := brokerTestDriver{run: func(ctx context.Context, req driver.Request, sink driver.EventSink) (driver.Response, error) {
					c.advance(40 * time.Millisecond)
					_, err := sink.(driver.DecisionCapableSink).RequestDecision(ctx, driver.DecisionRequest{Kind: driver.HumanDecisionPermission})
					if err != nil {
						return alignmentBudgetPartial(), err
					}
					c.advance(50 * time.Millisecond)
					if ctx.Err() != nil {
						return alignmentBudgetPartial(), fmt.Errorf("premature budget: %w", context.Cause(ctx))
					}
					c.advance(10 * time.Millisecond)
					<-ctx.Done()
					return alignmentBudgetPartial(), ctx.Err()
				}}
				a := New(d, alignmentClockOption(c), WithPolicy(Policy{ActiveExecutionTimeout: 100 * time.Millisecond}), OnApproval(func(ctx context.Context, r *ApprovalRequest) error {
					c.advance(300 * time.Millisecond)
					return r.Approve(ctx)
				}))
				var opts []CallOption
				if spawn {
					opts = append(opts, WithSpawn())
				}
				var res *Result
				var err error
				if streaming {
					st := a.Stream(context.Background(), "prompt", opts...)
					var terminal RunFinished
					for ev := range st.Events() {
						if end, ok := ev.(RunFinished); ok {
							terminal = end
						}
					}
					res, err = st.Result()
					if terminal.Reason != ReasonActiveExecutionTimeout {
						t.Fatal(terminal)
					}
				} else {
					res, err = a.Run(context.Background(), "prompt", opts...)
				}
				alignmentBudgetCarrier(t, res, err, ReasonActiveExecutionTimeout)
			})
		}
	}
}
func TestAlignmentActiveBudgetConcurrentAsk(t *testing.T) {
	c := newAlignmentClock()
	resolved := make(chan string, 2)
	d := brokerTestDriver{run: func(ctx context.Context, _ driver.Request, sink driver.EventSink) (driver.Response, error) {
		c.advance(40 * time.Millisecond)
		var wg sync.WaitGroup
		var errs [2]error
		for i, id := range []string{"A", "B"} {
			wg.Go(func() {
				_, errs[i] = sink.(driver.DecisionCapableSink).RequestDecision(ctx, driver.DecisionRequest{RequestID: id, Kind: driver.HumanDecisionPermission})
				resolved <- id
			})
		}
		wg.Wait()
		if err := errors.Join(errs[:]...); err != nil {
			return alignmentBudgetPartial(), err
		}
		c.advance(60 * time.Millisecond)
		<-ctx.Done()
		return alignmentBudgetPartial(), ctx.Err()
	}}
	a := New(d, alignmentClockOption(c), WithPolicy(Policy{ActiveExecutionTimeout: 100 * time.Millisecond}))
	st := a.Stream(context.Background(), "work")
	requests := map[string]*ApprovalRequest{}
	for len(requests) < 2 {
		select {
		case ev := <-st.Events():
			if r, ok := ev.(*ApprovalRequest); ok {
				requests[r.ID] = r
			}
		case <-time.After(time.Second):
			st.Cancel()
			t.Fatal("Ask requests were serialized across human wait")
		}
	}
	c.advance(300 * time.Millisecond)
	if err := requests["A"].Approve(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-resolved:
	case <-time.After(time.Second):
		t.Fatal("first Ask did not resolve")
	}
	c.advance(time.Second)
	if err := requests["B"].Approve(context.Background()); err != nil {
		t.Fatal("one Ask resumed another token", err)
	}
	for range st.Events() {
	}
	res, err := st.Result()
	alignmentBudgetCarrier(t, res, err, ReasonActiveExecutionTimeout)
}
func TestAlignmentActiveBudgetQueueWait(t *testing.T) {
	for _, callback := range []bool{false, true} {
		t.Run(fmt.Sprint(callback), func(t *testing.T) {
			c := newAlignmentClock()
			entered := make(chan context.Context, 1)
			d := brokerTestDriver{run: func(ctx context.Context, _ driver.Request, sink driver.EventSink) (driver.Response, error) {
				entered <- ctx
				_, err := sink.(driver.DecisionCapableSink).RequestDecision(ctx, driver.DecisionRequest{Kind: driver.HumanDecisionPermission})
				return alignmentBudgetPartial(), err
			}}
			opts := []Option{alignmentClockOption(c), WithEventBuffer(1), WithBlockingEvents(), WithPolicy(Policy{ActiveExecutionTimeout: 100 * time.Millisecond, Approvals: ApprovalPolicy{Timeout: 30 * time.Millisecond}})}
			if callback {
				opts = append(opts, OnApproval(func(context.Context, *ApprovalRequest) error {
					t.Error("handler ran while its notice was blocked")
					return nil
				}))
			}
			st := New(d, opts...).Stream(context.Background(), "queue")
			ctx := <-entered
			select {
			case <-c.paused:
			case <-time.After(time.Second):
				st.Cancel()
				t.Fatal("Ask did not pause before queue")
			}
			c.advance(time.Second)
			if ctx.Err() != nil {
				st.Cancel()
				t.Fatal("queue wait consumed active budget")
			}
			done := make(chan struct{})
			go func() { st.Result(); close(done) }()
			select {
			case <-done:
			case <-time.After(time.Second):
				st.Cancel()
				t.Fatal("approval wall deadline failed to release queue")
			}
			for range st.Events() {
			}
			res, err := st.Result()
			alignmentBudgetCarrier(t, res, err, ReasonApprovalTimeout)
		})
	}
}

type alignmentBudgetService struct {
	attach func(context.Context) (RunAttachment, error)
	detach func(context.Context) error
}

func (s alignmentBudgetService) AttachRun(ctx context.Context, _ string) (RunAttachment, error) {
	return s.attach(ctx)
}
func (s alignmentBudgetService) DetachRun(ctx context.Context, _ string) error {
	if s.detach != nil {
		return s.detach(ctx)
	}
	return nil
}
func TestAlignmentActiveBudgetPreparationAndCleanup(t *testing.T) {
	t.Run("preparation", func(t *testing.T) {
		c := newAlignmentClock()
		called := false
		d := brokerTestDriver{run: func(context.Context, driver.Request, driver.EventSink) (driver.Response, error) {
			called = true
			return driver.Response{}, nil
		}}
		a := New(d, alignmentClockOption(c), WithPolicy(Policy{ActiveExecutionTimeout: 100 * time.Millisecond}), WithRunServices(alignmentBudgetService{attach: func(context.Context) (RunAttachment, error) {
			c.advance(100 * time.Millisecond)
			return RunAttachment{}, nil
		}}))
		res, err := a.Run(context.Background(), "prepare")
		var re *RunError
		if res != nil || called || errors.As(err, &re) || !errors.Is(err, ErrActiveExecutionTimeout) {
			t.Fatalf("pre-dispatch=%v, %v, called=%v", res, err, called)
		}
	})
	t.Run("cleanup", func(t *testing.T) {
		c := newAlignmentClock()
		cleanup := errors.New("cleanup failure")
		a := New(brokerTestDriver{run: func(context.Context, driver.Request, driver.EventSink) (driver.Response, error) {
			c.advance(90 * time.Millisecond)
			return alignmentBudgetPartial(), nil
		}}, alignmentClockOption(c), WithPolicy(Policy{ActiveExecutionTimeout: 100 * time.Millisecond}), WithRunServices(alignmentBudgetService{attach: func(context.Context) (RunAttachment, error) { return RunAttachment{}, nil }, detach: func(context.Context) error { c.advance(time.Hour); return cleanup }}))
		res, err := a.Run(context.Background(), "cleanup")
		alignmentBudgetCarrier(t, res, err, ReasonInfrastructure)
		if !errors.Is(err, cleanup) || errors.Is(err, ErrActiveExecutionTimeout) {
			t.Fatal(err)
		}
	})
}
func TestAlignmentActiveBudgetObserverCountsActive(t *testing.T) {
	c := newAlignmentClock()
	observer := func(context.Context, RunEventInfo, Event) error { c.advance(100 * time.Millisecond); return nil }
	d := brokerTestDriver{run: func(ctx context.Context, _ driver.Request, sink driver.EventSink) (driver.Response, error) {
		sink.EmitStream(driver.StreamPayload{Kind: driver.StreamCapabilityInvocation, Capability: &capability.Invocation{InvocationID: "one", Ref: capability.Ref{Kind: capability.MCP, Key: "known", Operation: "read"}, Phase: capability.Started, Source: capability.Provider, Evidence: capability.ProviderProtocol, OccurredAt: time.Now().UTC()}})
		<-ctx.Done()
		return alignmentBudgetPartial(), ctx.Err()
	}}
	a := New(d, alignmentClockOption(c), WithPolicy(Policy{ActiveExecutionTimeout: 100 * time.Millisecond}), WithRunServices(alignmentBudgetService{attach: func(context.Context) (RunAttachment, error) { return RunAttachment{Observer: observer}, nil }}))
	res, err := a.Run(context.Background(), "observer")
	alignmentBudgetCarrier(t, res, err, ReasonActiveExecutionTimeout)
}
func TestAlignmentActiveBudgetTerminalOrder(t *testing.T) {
	cases := []struct {
		name   string
		first  func(*eventSink, context.Context)
		second func(*eventSink, context.CancelFunc)
		reason FailureReason
	}{
		{"provider before cancel", func(s *eventSink, ctx context.Context) {
			s.recordOutcome(ctx, &driver.RunFailure{Code: driver.FailureAgentError, Message: "formal failure"}, nil, nil)
		}, func(s *eventSink, c context.CancelFunc) { c(); s.recordContext(s.terminal.ctx) }, ReasonAgentError},
		{"cancel before provider", func(s *eventSink, ctx context.Context) { s.recordCancellation(ctx) }, func(s *eventSink, c context.CancelFunc) {
			c()
			s.recordOutcome(s.terminal.ctx, &driver.RunFailure{Code: driver.FailureAgentError}, nil, nil)
		}, ReasonCancelled},
		{"lease before cancel", func(s *eventSink, ctx context.Context) { s.recordOutcome(ctx, nil, nil, errors.New("lost lease")) }, func(s *eventSink, c context.CancelFunc) { c(); s.recordContext(s.terminal.ctx) }, ReasonInfrastructure},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			s := newEventSink(eventSinkConfig{})
			defer s.close()
			s.terminal.ctx = ctx
			registered := make(chan struct{})
			go func() {
				tc.first(s, ctx)
				close(registered)
			}()
			// The second producer cannot cancel until the concrete first
			// candidate has completed registration under the outcome mutex.
			<-registered
			tc.second(s, cancel)
			s.finishTerminal()
			res, err := finalizeRun("test", s, alignmentBudgetPartial(), ctx.Err(), nil)
			alignmentBudgetCarrier(t, res, err, tc.reason)
			if !errors.Is(err, context.Canceled) {
				t.Fatal("secondary context evidence lost")
			}
		})
	}
}

type alignmentBudgetSessionDriver struct{ brokerTestDriver }

func (d alignmentBudgetSessionDriver) Descriptor() driver.Descriptor {
	return driver.Descriptor{Type: "budget-session", Sessions: driver.SessionCapability{SupportsResume: true}, RunPolicyCaps: driver.RunPolicyCapabilities{Permission: driver.HumanDecisionSupport{Ask: true, AutoApprove: true, AutoReject: true, Retry: true}}, StructuredOutput: driver.StructuredOutputCapability{JSONSchemaNative: true, WorksWithRun: true}}
}
func (alignmentBudgetSessionDriver) SessionConfigFingerprint() (string, error) {
	return "budget/session/config/v1", nil
}
func (alignmentBudgetSessionDriver) SessionCodec() driver.SessionCodec { return alignmentBudgetCodec{} }

type alignmentBudgetCodec struct{}

func (alignmentBudgetCodec) Name() string { return "budget/session/v1" }
func (alignmentBudgetCodec) ToParams(s *driver.SessionState) driver.SessionParams {
	if s == nil {
		return driver.SessionParams{}
	}
	return driver.SessionParams{ResumeID: s.ResumeID}
}
func (alignmentBudgetCodec) FromParams(p driver.SessionParams) *driver.SessionState {
	if p.ResumeID == "" {
		return nil
	}
	return &driver.SessionState{ResumeID: p.ResumeID}
}
func (alignmentBudgetCodec) GuardFingerprint(p driver.SessionParams) string { return p.ResumeID }

type alignmentCommitStore struct {
	threadstore.Store
	finalize func(context.Context, threadstore.FinalizeRequest) error
	calls    int
}

func (s *alignmentCommitStore) Finalize(ctx context.Context, req threadstore.FinalizeRequest) error {
	s.calls++
	return s.finalize(ctx, req)
}
func (c *alignmentClock) elapseWithoutCallbacks(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}
func (c *alignmentClock) fireOld() {
	c.mu.Lock()
	var callbacks []func()
	for _, tm := range c.timers {
		callbacks = append(callbacks, tm.fn)
	}
	c.mu.Unlock()
	for _, fn := range callbacks {
		fn()
	}
}
func TestAlignmentActiveBudgetSealBeforeCommit(t *testing.T) {
	for _, mode := range []string{"expired before seal", "delayed commit return", "commit error", "parent cancel before commit", "parent cancel after commit", "missing checkpoint", "schema failure"} {
		t.Run(mode, func(t *testing.T) {
			c := newAlignmentClock()
			inner := memory.NewStore()
			parent, cancel := context.WithCancel(context.Background())
			defer cancel()
			commitErr := errors.New("commit backend failure")
			d := alignmentBudgetSessionDriver{brokerTestDriver{run: func(context.Context, driver.Request, driver.EventSink) (driver.Response, error) {
				if mode == "expired before seal" {
					c.elapseWithoutCallbacks(100 * time.Millisecond)
				} else {
					c.advance(90 * time.Millisecond)
				}
				resp := alignmentBudgetPartial()
				if mode != "missing checkpoint" {
					resp.Checkpoint = &driver.Checkpoint{Valid: true, State: &driver.SessionState{ResumeID: "healthy"}}
				}
				return resp, nil
			}}}
			store := &alignmentCommitStore{Store: inner}
			store.finalize = func(ctx context.Context, req threadstore.FinalizeRequest) error {
				if mode == "commit error" {
					return commitErr
				}
				if mode == "parent cancel before commit" {
					cancel()
					return inner.Finalize(ctx, req)
				}
				if err := inner.Finalize(ctx, req); err != nil {
					return err
				}
				c.advance(time.Hour)
				c.fireOld()
				if mode == "parent cancel after commit" {
					cancel()
				}
				return nil
			}
			a := New(d, WithThreadStore(store), alignmentClockOption(c), WithPolicy(Policy{ActiveExecutionTimeout: 100 * time.Millisecond}))
			var opts []CallOption
			if mode == "schema failure" {
				opts = append(opts, WithSchemaJSON([]byte(`{"type":"object","required":["ok"]}`)))
			}
			res, err := a.Thread("key").Run(parent, "work", opts...)
			var want FailureReason
			switch mode {
			case "expired before seal":
				want = ReasonActiveExecutionTimeout
			case "commit error", "missing checkpoint":
				want = ReasonInfrastructure
			case "schema failure":
				want = ReasonPolicyViolation
			case "parent cancel before commit", "parent cancel after commit":
				want = ReasonCancelled
			}
			if want != "" {
				alignmentBudgetCarrier(t, res, err, want)
			} else if err != nil || res == nil {
				t.Fatal(res, err)
			}
			if mode != "expired before seal" && errors.Is(err, ErrActiveExecutionTimeout) {
				t.Fatal("post-seal/failed execution recharged active time", err)
			}
			if (mode == "expired before seal" || mode == "missing checkpoint" || mode == "schema failure") && store.calls != 0 {
				t.Fatal("unhealthy candidate reached atomic persistence")
			}
			if mode == "commit error" && !errors.Is(err, commitErr) {
				t.Fatal("backend cause lost")
			}
			record, _ := inner.Resolve(context.Background(), threadstore.Query{Key: "key"})
			committed := mode == "delayed commit return" || mode == "parent cancel after commit"
			if (record != nil) != committed {
				t.Fatalf("record=%#v, committed=%v", record, committed)
			}
		})
	}
	t.Run("stateless overdue callback", func(t *testing.T) {
		c := newAlignmentClock()
		a := New(brokerTestDriver{run: func(context.Context, driver.Request, driver.EventSink) (driver.Response, error) {
			c.elapseWithoutCallbacks(100 * time.Millisecond)
			return alignmentBudgetPartial(), nil
		}}, alignmentClockOption(c), WithPolicy(Policy{ActiveExecutionTimeout: 100 * time.Millisecond}))
		res, err := a.Run(context.Background(), "work")
		alignmentBudgetCarrier(t, res, err, ReasonActiveExecutionTimeout)
	})
}
func TestAlignmentActiveBudgetPolicyWholeReplacementAndSubmillisecond(t *testing.T) {
	c := newAlignmentClock()
	a := New(brokerTestDriver{run: func(context.Context, driver.Request, driver.EventSink) (driver.Response, error) {
		c.advance(time.Hour)
		return alignmentBudgetPartial(), nil
	}}, alignmentClockOption(c), WithPolicy(Policy{ActiveExecutionTimeout: 100 * time.Millisecond}))
	if _, err := a.Run(context.Background(), "no budget", WithPolicy(Policy{})); err != nil {
		t.Fatal("zero replacement inherited active budget", err)
	}
	c2 := newAlignmentClock()
	a = New(brokerTestDriver{run: func(ctx context.Context, _ driver.Request, _ driver.EventSink) (driver.Response, error) {
		c2.advance(time.Nanosecond)
		<-ctx.Done()
		return alignmentBudgetPartial(), ctx.Err()
	}}, alignmentClockOption(c2), WithPolicy(Policy{ActiveExecutionTimeout: time.Nanosecond}))
	_, err := a.Run(context.Background(), "submillisecond")
	var limit *ActiveExecutionTimeoutError
	if !errors.As(err, &limit) || limit.Limit != time.Nanosecond {
		t.Fatal(err)
	}
}
func TestAlignmentActiveBudgetAskExitsAndRetry(t *testing.T) {
	for _, mode := range []string{"retry", "continue", "auto approve", "auto deny", "panic", "handler error", "unresolved"} {
		t.Run(mode, func(t *testing.T) {
			c := newAlignmentClock()
			attempts := 0
			handlerErr := errors.New("handler failed")
			policy := Policy{ActiveExecutionTimeout: 100 * time.Millisecond}
			want := ReasonActiveExecutionTimeout
			switch mode {
			case "retry":
				policy.Approvals.OnReject = driver.FailureRetry
			case "continue":
				policy.Approvals.OnReject = driver.FailureContinue
			case "auto approve":
				policy.Approvals.Permission = driver.HumanDecisionAutoApprove
			case "auto deny":
				policy.Approvals.Permission = driver.HumanDecisionAutoReject
				want = ReasonApprovalDenied
			case "panic", "unresolved":
				want = ReasonAgentError
			case "handler error":
				want = ReasonInfrastructure
			}
			d := alignmentBudgetSessionDriver{brokerTestDriver{run: func(ctx context.Context, _ driver.Request, sink driver.EventSink) (driver.Response, error) {
				c.advance(40 * time.Millisecond)
				_, err := sink.(driver.DecisionCapableSink).RequestDecision(ctx, driver.DecisionRequest{Kind: driver.HumanDecisionPermission})
				if err != nil {
					return alignmentBudgetPartial(), err
				}
				c.advance(60 * time.Millisecond)
				<-ctx.Done()
				return alignmentBudgetPartial(), ctx.Err()
			}}}
			a := New(d, alignmentClockOption(c), WithPolicy(policy), OnApproval(func(ctx context.Context, r *ApprovalRequest) error {
				attempts++
				c.advance(time.Hour)
				switch mode {
				case "panic":
					panic("handler panic")
				case "handler error":
					return handlerErr
				case "unresolved":
					return nil
				case "continue":
					return r.Deny(ctx, "no")
				case "retry":
					if attempts == 1 {
						return r.Deny(ctx, "retry")
					}
					return r.Approve(ctx)
				}
				t.Error("automatic mode invoked Ask handler")
				return r.Approve(ctx)
			}))
			res, err := a.Run(context.Background(), "work")
			alignmentBudgetCarrier(t, res, err, want)
			if mode == "retry" && attempts != 2 {
				t.Fatal("retry count", attempts)
			}
			if mode == "handler error" && !errors.Is(err, handlerErr) {
				t.Fatal("handler error lost")
			}
			c.advance(time.Hour)
			c.fireOld()
			if want != ReasonActiveExecutionTimeout && errors.Is(err, ErrActiveExecutionTimeout) {
				t.Fatal("late timer overwrote error")
			}
		})
	}
}
func TestAlignmentActiveBudgetMemberDoesNotPauseLeader(t *testing.T) {
	leaderClock, memberClock := newAlignmentClock(), newAlignmentClock()
	member := New(brokerTestDriver{run: func(ctx context.Context, _ driver.Request, sink driver.EventSink) (driver.Response, error) {
		_, err := sink.(driver.DecisionCapableSink).RequestDecision(ctx, driver.DecisionRequest{Kind: driver.HumanDecisionPermission})
		return alignmentBudgetPartial(), err
	}}, alignmentClockOption(memberClock), WithPolicy(Policy{ActiveExecutionTimeout: 100 * time.Millisecond}), OnApproval(func(ctx context.Context, r *ApprovalRequest) error {
		leaderClock.advance(100 * time.Millisecond)
		<-ctx.Done()
		return ctx.Err()
	}))
	leader := New(brokerTestDriver{run: func(ctx context.Context, _ driver.Request, _ driver.EventSink) (driver.Response, error) {
		res, err := member.Run(ctx, "nested")
		alignmentBudgetCarrier(t, res, err, ReasonCancelled)
		return alignmentBudgetPartial(), err
	}}, alignmentClockOption(leaderClock), WithPolicy(Policy{ActiveExecutionTimeout: 100 * time.Millisecond}))
	res, err := leader.Run(context.Background(), "lead")
	alignmentBudgetCarrier(t, res, err, ReasonActiveExecutionTimeout)
}

func TestAlignmentActiveBudgetFailedThreadPreservesCheckpoint(t *testing.T) {
	c := newAlignmentClock()
	store := memory.NewStore()
	fail := false
	d := alignmentBudgetSessionDriver{brokerTestDriver{run: func(ctx context.Context, _ driver.Request, _ driver.EventSink) (driver.Response, error) {
		resp := alignmentBudgetPartial()
		resp.Checkpoint = &driver.Checkpoint{Valid: true, State: &driver.SessionState{ResumeID: "healthy"}}
		if fail {
			c.advance(100 * time.Millisecond)
			return resp, ctx.Err()
		}
		return resp, nil
	}}}
	a := New(d, WithThreadStore(store), alignmentClockOption(c), WithPolicy(Policy{ActiveExecutionTimeout: 100 * time.Millisecond}))
	ctx := context.Background()
	if _, err := a.Thread("key").Run(ctx, "seed"); err != nil {
		t.Fatal(err)
	}
	before, _ := store.Resolve(ctx, threadstore.Query{Key: "key"})
	fail = true
	for _, streaming := range []bool{false, true} {
		var res *Result
		var err error
		if streaming {
			st := a.Thread("key").Stream(ctx, "fail")
			for range st.Events() {
			}
			res, err = st.Result()
		} else {
			res, err = a.Thread("key").Run(ctx, "fail")
		}
		alignmentBudgetCarrier(t, res, err, ReasonActiveExecutionTimeout)
		after, _ := store.Resolve(ctx, threadstore.Query{Key: "key"})
		if !reflect.DeepEqual(before, after) {
			t.Fatal("failed checkpoint replaced old record")
		}
	}
}
func TestAlignmentActiveBudgetErrors(t *testing.T) {
	var nilLimit *ActiveExecutionTimeoutError
	if nilLimit.Error() == "" || !errors.Is(nilLimit, ErrActiveExecutionTimeout) {
		t.Fatal("nil typed limit error")
	}
	err := &RunError{Reason: ReasonActiveExecutionTimeout, Cause: &ActiveExecutionTimeoutError{Limit: 100 * time.Millisecond}}
	if !errors.Is(err, ErrActiveExecutionTimeout) {
		t.Fatal(err)
	}
	if failureReason(driver.FailureActiveExecutionTimeout) != ReasonActiveExecutionTimeout {
		t.Fatal("SPI failure mapping")
	}
}

func TestAlignmentActiveBudgetResumeFallbackSharesRemaining(t *testing.T) {
	c := newAlignmentClock()
	store := memory.NewStore()
	phase, attempts := "seed", 0
	rejected := errors.New("resume rejected before prompt delivery")
	d := alignmentBudgetSessionDriver{brokerTestDriver{run: func(ctx context.Context, req driver.Request, _ driver.EventSink) (driver.Response, error) {
		resp := alignmentBudgetPartial()
		resp.Checkpoint = &driver.Checkpoint{Valid: true, State: &driver.SessionState{ResumeID: "healthy"}}
		if phase == "seed" {
			return resp, nil
		}
		attempts++
		if attempts == 1 {
			if req.Session == nil || req.Session.State == nil || req.Session.State.ResumeID != "healthy" {
				t.Error("first attempt did not resume the healthy checkpoint")
			}
			c.advance(40 * time.Millisecond)
			return driver.Response{}, &engine.ResumeRejectedError{Cause: rejected}
		}
		if req.Session != nil && req.Session.State != nil && req.Session.State.ResumeID != "" {
			t.Error("fallback retained rejected session")
		}
		c.advance(59 * time.Millisecond)
		if ctx.Err() != nil {
			t.Error("fallback expired before the remaining budget elapsed")
		}
		c.advance(time.Millisecond)
		if !errors.Is(context.Cause(ctx), ErrActiveExecutionTimeout) {
			t.Error("fallback reset this invocation's budget")
		}
		return resp, ctx.Err()
	}}}
	a := New(d, WithThreadStore(store), alignmentClockOption(c), WithPolicy(Policy{ActiveExecutionTimeout: 100 * time.Millisecond}))
	ctx := context.Background()
	if _, err := a.Thread("key").Run(ctx, "seed"); err != nil {
		t.Fatal(err)
	}
	before, _ := store.Resolve(ctx, threadstore.Query{Key: "key"})
	phase = "fallback"
	res, err := a.Thread("key").Run(ctx, "fallback")
	alignmentBudgetCarrier(t, res, err, ReasonActiveExecutionTimeout)
	if attempts != 2 || !errors.Is(err, rejected) {
		t.Fatal("fallback count or original rejection lost", attempts, err)
	}
	after, _ := store.Resolve(ctx, threadstore.Query{Key: "key"})
	if !reflect.DeepEqual(before, after) {
		t.Fatal("timed-out fallback replaced healthy checkpoint")
	}
}

func TestAlignmentActiveBudgetHandlerReturnsOwnDeadline(t *testing.T) {
	for i := 0; i < 10; i++ {
		c := newAlignmentClock()
		a := New(brokerTestDriver{run: func(ctx context.Context, _ driver.Request, sink driver.EventSink) (driver.Response, error) {
			_, err := sink.(driver.DecisionCapableSink).RequestDecision(ctx, driver.DecisionRequest{Kind: driver.HumanDecisionPermission})
			return alignmentBudgetPartial(), err
		}}, alignmentClockOption(c), WithPolicy(Policy{ActiveExecutionTimeout: 100 * time.Millisecond, Approvals: ApprovalPolicy{Timeout: time.Millisecond}}), OnApproval(func(ctx context.Context, _ *ApprovalRequest) error {
			c.advance(time.Hour)
			<-ctx.Done()
			return ctx.Err()
		}))
		res, err := a.Run(context.Background(), "own approval deadline")
		alignmentBudgetCarrier(t, res, err, ReasonApprovalTimeout)
		if errors.Is(err, ErrActiveExecutionTimeout) {
			t.Fatal("Ask deadline consumed paused budget")
		}
	}
}

func TestAlignmentActiveBudgetParentCauseIdentity(t *testing.T) {
	for _, kind := range []string{"cancel", "deadline"} {
		for _, stage := range []string{"preparation", "driver", "Ask"} {
			t.Run(kind+"/"+stage, func(t *testing.T) {
				foreign := &ActiveExecutionTimeoutError{Limit: 777 * time.Second}
				var parent context.Context
				var stop context.CancelFunc
				trigger := func() {}
				want := ReasonCancelled
				if kind == "cancel" {
					var cancel context.CancelCauseFunc
					parent, cancel = context.WithCancelCause(context.Background())
					trigger = func() { cancel(foreign) }
					stop = func() { cancel(nil) }
				} else {
					parent, stop = context.WithDeadlineCause(context.Background(), time.Now().Add(100*time.Millisecond), foreign)
					want = ReasonDeadlineExceeded
				}
				defer stop()
				c := newAlignmentClock() // Never advanced: this run cannot exhaust its budget.
				d := brokerTestDriver{run: func(ctx context.Context, _ driver.Request, sink driver.EventSink) (driver.Response, error) {
					if stage == "Ask" {
						_, err := sink.(driver.DecisionCapableSink).RequestDecision(ctx, driver.DecisionRequest{Kind: driver.HumanDecisionPermission})
						return alignmentBudgetPartial(), err
					}
					trigger()
					<-ctx.Done()
					return alignmentBudgetPartial(), ctx.Err()
				}}
				opts := []Option{alignmentClockOption(c), WithPolicy(Policy{ActiveExecutionTimeout: 100 * time.Millisecond})}
				if stage == "preparation" {
					opts = append(opts, WithRunServices(alignmentBudgetService{attach: func(ctx context.Context) (RunAttachment, error) {
						trigger()
						<-ctx.Done()
						return RunAttachment{}, ctx.Err()
					}}))
				}
				if stage == "Ask" {
					opts = append(opts, OnApproval(func(ctx context.Context, _ *ApprovalRequest) error {
						trigger()
						<-ctx.Done()
						return ctx.Err()
					}))
				}
				st := New(d, opts...).Stream(parent, "inherited cause")
				var terminal RunFinished
				for event := range st.Events() {
					if end, ok := event.(RunFinished); ok {
						terminal = end
					}
				}
				res, err := st.Result()
				if stage != "preparation" {
					alignmentBudgetCarrier(t, res, err, want)
				}
				if terminal.Reason != want || !errors.Is(err, foreign) {
					t.Fatalf("parent cause became own expiry: terminal=%s want=%s error=%v", terminal.Reason, want, err)
				}
			})
		}
	}
}

func TestAlignmentActiveBudgetOwnExpiryBeforeParentNotification(t *testing.T) {
	parent, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	lateParent := errors.New("later parent cancellation")
	c := newAlignmentClock()
	d := brokerTestDriver{run: func(ctx context.Context, _ driver.Request, supplied driver.EventSink) (driver.Response, error) {
		sink := supplied.(*eventSink)
		// Fence only core notification, not controller time/cancellation. The
		// own budget wins before the parent's watcher can register anything.
		sink.terminal.mu.Lock()
		c.advance(100 * time.Millisecond)
		if ctx.Err() == nil || context.Cause(ctx) != sink.terminal.expired {
			t.Error("own expiry did not select the authoritative child cause")
		}
		cancel(lateParent)
		sink.terminal.mu.Unlock()
		return alignmentBudgetPartial(), errors.Join(ctx.Err(), lateParent)
	}}
	a := New(d, alignmentClockOption(c), WithPolicy(Policy{ActiveExecutionTimeout: 100 * time.Millisecond}))
	st := a.Stream(parent, "expiry before parent notification")
	for event := range st.Events() {
		if end, ok := event.(RunFinished); ok && end.Reason != ReasonActiveExecutionTimeout {
			t.Errorf("late parent notification rewrote terminal: %s", end.Reason)
		}
	}
	res, err := st.Result()
	alignmentBudgetCarrier(t, res, err, ReasonActiveExecutionTimeout)
	if !errors.Is(err, lateParent) {
		t.Fatal("late parent evidence lost")
	}
}

type alignmentSelectionClock struct {
	*alignmentClock
	enabled           atomic.Bool
	once              sync.Once
	stopping, release chan struct{}
}
type alignmentSelectionTimer struct {
	activebudget.Timer
	clock *alignmentSelectionClock
}

func (c *alignmentSelectionClock) AfterFunc(d time.Duration, fn func()) activebudget.Timer {
	return &alignmentSelectionTimer{Timer: c.alignmentClock.AfterFunc(d, fn), clock: c}
}
func (t *alignmentSelectionTimer) Stop() bool {
	stopped := t.Timer.Stop()
	if t.clock.enabled.Load() {
		t.clock.once.Do(func() { close(t.clock.stopping); <-t.clock.release })
	}
	return stopped
}

func TestAlignmentActiveBudgetSelectedExpiryBeforeChildCancellation(t *testing.T) {
	for _, tc := range []struct {
		name        string
		parentCause error
		parentFirst bool
	}{
		{name: "plain_parent"},
		{name: "same_type_parent", parentCause: &ActiveExecutionTimeoutError{Limit: 777 * time.Second}},
		{name: "same_type_parent_first", parentCause: &ActiveExecutionTimeoutError{Limit: 777 * time.Second}, parentFirst: true},
	} {
		for _, preparation := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/preparation_%t", tc.name, preparation), func(t *testing.T) {
				c := &alignmentSelectionClock{alignmentClock: newAlignmentClock(), stopping: make(chan struct{}), release: make(chan struct{})}
				parent, cancel := context.WithCancelCause(context.Background())
				defer cancel(nil)
				entered := make(chan context.Context, 1)
				finishWork, advanced := make(chan struct{}), make(chan struct{})
				d := brokerTestDriver{run: func(ctx context.Context, _ driver.Request, _ driver.EventSink) (driver.Response, error) {
					if preparation {
						t.Error("preparation cancellation reached Driver")
					}
					entered <- ctx
					<-finishWork
					return alignmentBudgetPartial(), ctx.Err()
				}}
				opts := []Option{sharedOptionFunc(func(s *RunSettings) { s.budgetTiming.clock = c }), WithPolicy(Policy{ActiveExecutionTimeout: 100 * time.Millisecond})}
				if preparation {
					opts = append(opts, WithRunServices(alignmentBudgetService{attach: func(ctx context.Context) (RunAttachment, error) {
						entered <- ctx
						<-finishWork
						return RunAttachment{}, ctx.Err()
					}}))
				}
				st := New(d, opts...).Stream(parent, "selected before propagation")
				runctx := <-entered
				wantReason := ReasonActiveExecutionTimeout
				wantLimit := 100 * time.Millisecond
				if tc.parentFirst {
					cancel(tc.parentCause)
					<-runctx.Done()
					c.advance(100 * time.Millisecond)
					wantReason, wantLimit = ReasonCancelled, 777*time.Second
				} else {
					c.enabled.Store(true)
					go func() { c.advance(100 * time.Millisecond); close(advanced) }()
					// The timer has confirmed expiry and saved its cause while parent
					// was healthy; its Stop call holds propagation at a precise barrier.
					<-c.stopping
					cancel(tc.parentCause)
					<-runctx.Done()
					close(c.release)
					<-advanced
				}
				close(finishWork)
				var terminal RunFinished
				terminals := 0
				for event := range st.Events() {
					if end, ok := event.(RunFinished); ok {
						terminal = end
						terminals++
					}
				}
				res, err := st.Result()
				if !preparation {
					alignmentBudgetCarrier(t, res, err, wantReason)
				} else {
					var re *RunError
					if res != nil || errors.As(err, &re) {
						t.Fatal("pre-dispatch failure fabricated a Result", res, err)
					}
				}
				var limit *ActiveExecutionTimeoutError
				if terminals != 1 || terminal.Reason != wantReason || !errors.As(err, &limit) || limit.Limit != wantLimit {
					t.Fatal("selected budget cause lost during propagation", terminal.Reason, err)
				}
				if tc.parentCause != nil && !errors.Is(err, tc.parentCause) {
					t.Fatal("parent original cause identity lost", err)
				}
				if tc.parentFirst && limit != tc.parentCause {
					t.Fatal("parent-first typed cause identity changed", limit)
				}
				for range 3 {
					again, againErr := st.Result()
					if again != res || againErr != err {
						t.Fatal("Result changed after terminal")
					}
				}
			})
		}
	}
}

func TestAlignmentActiveBudgetBindingCancellationRace(t *testing.T) {
	for range 100 {
		parent, cancel := context.WithCancel(context.Background())
		expired := &ActiveExecutionTimeoutError{Limit: time.Second}
		ctx, budget := activebudget.New(parent, time.Second, expired, newAlignmentClock())
		s := newEventSink(eventSinkConfig{})
		s.terminal.ctx = parent
		start := make(chan struct{})
		var wg sync.WaitGroup
		wg.Go(func() { <-start; s.bindBudget(ctx, budget, expired) })
		wg.Go(func() {
			<-start
			// Cancel/Agent.Close register through this same path while the
			// execution goroutine may still be binding its new controller.
			s.recordCancellation(parent)
			cancel()
			s.recordContext(parent)
		})
		close(start)
		wg.Wait()
		s.recordContext(ctx)
		if got := s.terminalSnapshot(); got.reason != ReasonCancelled {
			t.Error("binding race changed cancellation reason", got.reason)
		}
		budget.Cancel(nil)
		s.close()
	}
}
