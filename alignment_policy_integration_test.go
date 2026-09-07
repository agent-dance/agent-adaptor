package adaptor

// T22 oracles below are handwritten from C02/C04 and R005/R009/R012–R016.
// The fixtures supply scheduling and protocol inputs; no production classifier,
// result mapper, or schema resolver is used to calculate expected outcomes.
import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"

	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/agent-dance/agent-adaptor/claude"
	"github.com/agent-dance/agent-adaptor/codebuddy"
	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/activebudget"
	"github.com/agent-dance/agent-adaptor/internal/engine"
	"github.com/agent-dance/agent-adaptor/internal/mcpruntime"
	"github.com/agent-dance/agent-adaptor/internal/systemprompt"
	qa "github.com/agent-dance/agent-adaptor/internal/testutil/alignmentpolicy"
	"github.com/agent-dance/agent-adaptor/profile"
	"github.com/agent-dance/agent-adaptor/threadstore"
	"github.com/agent-dance/agent-adaptor/tool"
)

func t22Schema(v map[string]any) CallOption {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return WithSchemaJSON(b)
}
func t22Clock(c *qa.Clock) SharedOption {
	return sharedOptionFunc(func(s *RunSettings) { s.budgetTiming.clock = c })
}

type t22Service struct {
	attach func(context.Context) (RunAttachment, error)
	detach func(context.Context) error
}

func (s t22Service) AttachRun(ctx context.Context, _ string) (RunAttachment, error) {
	if s.attach != nil {
		return s.attach(ctx)
	}
	return RunAttachment{}, nil
}
func (s t22Service) DetachRun(ctx context.Context, _ string) error {
	if s.detach != nil {
		return s.detach(ctx)
	}
	return nil
}
func t22Context(t *testing.T) context.Context {
	t.Helper()
	ctx, c := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(c)
	return ctx
}
func t22Await[T any](t *testing.T, c <-chan T) T {
	t.Helper()
	select {
	case x := <-c:
		return x
	case <-time.After(4 * time.Second):
		t.Fatal("fixture barrier timed out")
		var z T
		return z
	}
}
func t22Drain(s Stream) ([]Event, *Result, error) {
	var es []Event
	for e := range s.Events() {
		es = append(es, e)
	}
	r, err := s.Result()
	return es, r, err
}
func t22Terminal(t *testing.T, es []Event, id string, reason FailureReason) {
	t.Helper()
	n := 0
	for i, e := range es {
		if f, ok := e.(RunFinished); ok {
			n++
			if i != len(es)-1 || f.Meta().RunID != id || f.Failed != (reason != "") || f.Reason != reason {
				t.Fatalf("terminal=%#v position=%d/%d want=%s", f, i, len(es), reason)
			}
		}
	}
	if n != 1 {
		t.Fatalf("terminal count %d", n)
	}
}
func t22Carrier(t *testing.T, r *Result, err error, reason FailureReason) *RunError {
	t.Helper()
	var re *RunError
	if r != nil || !errors.As(err, &re) || re == nil || re.Result == nil || re.Reason != reason {
		t.Fatalf("result=%#v error=%v carrier=%#v want=%s", r, err, re, reason)
	}
	got := re.Result
	if got.Text != "audited answer" || got.Summary != "brief" || got.Raw().Stdout != "formal stdout\n" || got.Raw().Stderr != "formal stderr\n" || got.Raw().Terminal == nil || string(got.Raw().Terminal.JSON) != `{"completed":true}` || len(got.Transcript()) != 1 || got.Usage == nil || got.Usage.InputTokens != 0 || got.Usage.OutputTokens != 3 || got.Model != "known" || got.Provider != "script" || got.Metadata["audit"] != "marker" {
		t.Fatalf("partial audit lost: %#v raw=%#v", got, got.Raw())
	}
	if got.Raw().Terminal.Event != "completed" || got.Transcript()[0].Kind != driver.TranscriptAssistant || got.Transcript()[0].Text != "audited answer" {
		t.Fatal("terminal/transcript content changed")
	}
	reports := got.Services()
	if len(reports) != 1 || reports[0].ID != "observed-fixture" || reports[0].Name != "offline-service" || reports[0].Status != driver.RuntimeServiceStopped || reports[0].Lifecycle != driver.RuntimeLifecycleEphemeral || reports[0].Health != driver.RuntimeHealthUnknown || reports[0].Metadata["observation"] != "fixture-exit" {
		t.Fatalf("observed partial service report lost: %#v", reports)
	}
	return re
}

func TestAlignmentPolicyExactActiveLedger(t *testing.T) {
	for _, final := range []time.Duration{50 * time.Millisecond, 60 * time.Millisecond} {
		t.Run(final.String(), func(t *testing.T) {
			c := qa.NewClock()
			d := qa.NewDriver()
			var called atomic.Int32
			d.RunFunc = func(ctx context.Context, _ driver.Request, s driver.EventSink) (driver.Response, error) {
				c.Advance(40 * time.Millisecond)
				resp, err := s.(driver.DecisionCapableSink).RequestDecision(ctx, driver.DecisionRequest{Kind: driver.HumanDecisionPermission})
				if err != nil {
					return qa.Response(), err
				}
				if resp.Result != driver.DecisionApproved {
					return qa.Response(), fmt.Errorf("wrong decision %s", resp.Result)
				}
				c.Advance(final)
				return qa.Response(), ctx.Err()
			}
			a := New(d, t22Clock(c), WithPolicy(Policy{ActiveExecutionTimeout: 100 * time.Millisecond}), OnApproval(func(ctx context.Context, r *ApprovalRequest) error {
				called.Add(1)
				c.Advance(300 * time.Millisecond)
				if ctx.Err() != nil {
					return fmt.Errorf("Ask spent active time: %w", ctx.Err())
				}
				return r.Approve(ctx)
			}))
			s := a.Stream(t22Context(t), "ledger")
			es, r, err := t22Drain(s)
			reason := FailureReason("")
			if final == 60*time.Millisecond {
				reason = ReasonActiveExecutionTimeout
				re := t22Carrier(t, r, err, reason)
				var b *ActiveExecutionTimeoutError
				if !errors.As(re, &b) || b.Limit != 100*time.Millisecond {
					t.Fatal("wrong limit")
				}
			} else if err != nil || r == nil {
				t.Fatalf("90ms active failed: %v", err)
			}
			t22Terminal(t, es, s.RunID(), reason)
			if called.Load() != 1 || d.Calls.Load() != 1 {
				t.Fatal("not exactly one execution/Ask")
			}
		})
	}
}

func TestAlignmentPolicyOverlappingAsk(t *testing.T) {
	c := qa.NewClock()
	d := qa.NewDriver()
	firstDone := make(chan struct{}, 1)
	ctxSeen := make(chan context.Context, 1)
	d.RunFunc = func(ctx context.Context, _ driver.Request, s driver.EventSink) (driver.Response, error) {
		ctxSeen <- ctx
		errs := make(chan error, 2)
		for _, kind := range []driver.HumanDecisionKind{driver.HumanDecisionPermission, driver.HumanDecisionQuestion} {
			go func(k driver.HumanDecisionKind) {
				_, err := s.(driver.DecisionCapableSink).RequestDecision(ctx, driver.DecisionRequest{Kind: k})
				if k == driver.HumanDecisionPermission {
					firstDone <- struct{}{}
				}
				errs <- err
			}(kind)
		}
		for range 2 {
			if err := <-errs; err != nil {
				return qa.Response(), err
			}
		}
		c.Advance(100 * time.Millisecond)
		return qa.Response(), ctx.Err()
	}
	a := New(d, t22Clock(c), WithPolicy(Policy{ActiveExecutionTimeout: 100 * time.Millisecond, Approvals: ApprovalPolicy{Question: QuestionAsk}}))
	s := a.Stream(t22Context(t), "overlap")
	var es []Event
	requests := map[ApprovalKind]*ApprovalRequest{}
	for len(requests) < 2 {
		e := t22Await(t, s.Events())
		es = append(es, e)
		if r, ok := e.(*ApprovalRequest); ok {
			requests[r.Kind] = r
		}
	}
	runctx := t22Await(t, ctxSeen)
	c.Advance(time.Second)
	if err := requests[ApprovalPermission].Approve(context.Background()); err != nil {
		t.Fatal(err)
	}
	t22Await(t, firstDone)
	c.Advance(time.Second)
	if runctx.Err() != nil {
		t.Fatal("first release resumed while second Ask pending")
	}
	if err := requests[ApprovalQuestion].Answer(context.Background(), "chosen"); err != nil {
		t.Fatal(err)
	}
	tail, r, err := t22Drain(s)
	es = append(es, tail...)
	t22Carrier(t, r, err, ReasonActiveExecutionTimeout)
	t22Terminal(t, es, s.RunID(), ReasonActiveExecutionTimeout)
}

func TestAlignmentPolicySnapshotAndResponder(t *testing.T) {
	for _, kind := range []ApprovalKind{ApprovalPermission, ApprovalPlanReview, ApprovalQuestion} {
		for _, callback := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/callback=%v", kind, callback), func(t *testing.T) {
				ctx := t22Context(t)
				d := qa.NewDriver()
				source := driver.DecisionRequest{Kind: driver.HumanDecisionKind(kind), Choices: []driver.DecisionChoice{{Key: "original", Label: "label"}}, Payload: map[string]any{"nested": []any{map[string]any{"leaf": "original"}}}}
				sent := make(chan *ApprovalRequest, 1)
				response := make(chan driver.DecisionResponse, 1)
				d.RunFunc = func(ctx context.Context, _ driver.Request, s driver.EventSink) (driver.Response, error) {
					r, err := s.(driver.DecisionCapableSink).RequestDecision(ctx, source)
					response <- r
					return qa.Response(), err
				}
				opts := []Option{WithPolicy(Policy{Approvals: ApprovalPolicy{Question: QuestionAsk, OnReject: FallbackContinue}})}
				handlerDone := make(chan struct{})
				if callback {
					opts = append(opts, OnApproval(func(_ context.Context, r *ApprovalRequest) error { sent <- r; <-handlerDone; return nil }))
				}
				a := New(d, opts...)
				s := a.Stream(ctx, "snapshot")
				events := make(chan []Event, 1)
				go func() {
					var es []Event
					for e := range s.Events() {
						es = append(es, e)
						if ar, ok := e.(*ApprovalRequest); ok {
							sent <- ar
						}
					}
					events <- es
				}()
				r := t22Await(t, sent)
				source.Choices[0].Key = "source-mutated"
				source.Payload["nested"].([]any)[0].(map[string]any)["leaf"] = "source-mutated"
				copyA := WithEventMeta(r, EventMeta{RunID: "copy", Sequence: 99}).(*ApprovalRequest)
				copyB := WithEventMeta(r, EventMeta{RunID: "copy2"}).(*ApprovalRequest)
				copyA.Choices[0].Key = "consumer-mutated"
				copyA.Details["nested"].([]any)[0].(map[string]any)["leaf"] = "consumer-mutated"
				if r.Choices[0].Key != "original" || copyB.Choices[0].Key != "original" || copyB.Details["nested"].([]any)[0].(map[string]any)["leaf"] != "original" {
					t.Fatal("mutable description shared")
				}
				var wrong error
				if kind == ApprovalQuestion {
					wrong = r.Approve(ctx)
				} else {
					wrong = r.Answer(ctx, "answer")
				}
				if !errors.Is(wrong, ErrApprovalKindMismatch) {
					t.Fatalf("wrong kind: %v", wrong)
				}
				wins := atomic.Int32{}
				var wg sync.WaitGroup
				errs := make(chan error, 12)
				for i := 0; i < 12; i++ {
					wg.Add(1)
					go func(i int) {
						defer wg.Done()
						req := []*ApprovalRequest{r, copyA, copyB}[i%3]
						var err error
						if kind == ApprovalQuestion {
							err = req.Answer(ctx, "answer")
						} else {
							err = req.Approve(ctx)
						}
						if err == nil {
							wins.Add(1)
						} else {
							errs <- err
						}
					}(i)
				}
				wg.Wait()
				close(errs)
				for err := range errs {
					if !errors.Is(err, ErrApprovalResolved) {
						t.Fatal(err)
					}
				}
				if callback {
					close(handlerDone)
				}
				if wins.Load() != 1 {
					t.Fatalf("answer winners=%d", wins.Load())
				}
				got := t22Await(t, response)
				want := driver.DecisionApproved
				if kind == ApprovalQuestion {
					want = driver.DecisionAnswered
				}
				if got.Result != want {
					t.Fatalf("response=%#v", got)
				}
				es := t22Await(t, events)
				_, err := s.Result()
				if err != nil {
					t.Fatal(err)
				}
				t22Terminal(t, es, s.RunID(), "")
			})
		}
	}
	for _, r := range []*ApprovalRequest{nil, {}, {Kind: ApprovalQuestion}} {
		if !errors.Is(r.Deny(context.Background(), "no"), ErrApprovalUnavailable) {
			t.Fatal("unbound responder did not fail immediately")
		}
	}
}

func TestAlignmentPolicyRetryExpiryAndBackpressure(t *testing.T) {
	for _, mode := range []string{"retry", "timeout", "cancel", "panic", "auto"} {
		t.Run(mode, func(t *testing.T) {
			c := qa.NewClock()
			d := qa.NewDriver()
			var attempts atomic.Int32
			var last *ApprovalRequest
			policy := Policy{ActiveExecutionTimeout: 100 * time.Millisecond, Approvals: ApprovalPolicy{Timeout: 10 * time.Millisecond}}
			if mode == "retry" {
				policy.Approvals.OnReject = FallbackRetry
				policy.Approvals.MaxRetries = 1
			}
			if mode == "auto" {
				policy.Approvals.Permission = ApprovalAutoApprove
			}
			ctx, cancel := context.WithCancel(t22Context(t))
			defer cancel()
			d.RunFunc = func(ctx context.Context, _ driver.Request, s driver.EventSink) (driver.Response, error) {
				_, err := s.(driver.DecisionCapableSink).RequestDecision(ctx, driver.DecisionRequest{Kind: driver.HumanDecisionPermission})
				if mode == "auto" && len(c.Stops) != 0 {
					t.Error("automatic approval paused the active timer")
				}
				if err == nil {
					c.Advance(100 * time.Millisecond)
				}
				return qa.Response(), errors.Join(err, ctx.Err())
			}
			a := New(d, t22Clock(c), WithPolicy(policy), OnApproval(func(ctx context.Context, r *ApprovalRequest) error {
				last = r
				n := attempts.Add(1)
				c.Advance(time.Second)
				switch mode {
				case "retry":
					if n == 1 {
						return r.Deny(ctx, "first")
					}
					return r.Approve(ctx)
				case "cancel":
					cancel()
					<-ctx.Done()
					return ctx.Err()
				case "panic":
					panic("controlled fixture")
				default:
					<-ctx.Done()
					return ctx.Err()
				}
			}))
			s := a.Stream(ctx, "attempt")
			es, r, err := t22Drain(s)
			reason := ReasonActiveExecutionTimeout
			switch mode {
			case "timeout":
				reason = ReasonApprovalTimeout
			case "cancel":
				reason = ReasonCancelled
			case "panic":
				reason = ReasonAgentError
			}
			t22Carrier(t, r, err, reason)
			t22Terminal(t, es, s.RunID(), reason)
			if mode == "retry" && attempts.Load() != 2 {
				t.Fatalf("retry count=%d", attempts.Load())
			}
			if mode == "auto" && attempts.Load() != 0 {
				t.Fatal("auto used human callback")
			}
			if last != nil && !errors.Is(last.Approve(context.Background()), ErrApprovalResolved) {
				t.Fatal("late response remained live")
			}
		})
	}
	t.Run("full-buffer-wall-clock", func(t *testing.T) {
		c := qa.NewClock()
		d := qa.NewDriver()
		entered := make(chan struct{})
		d.RunFunc = func(ctx context.Context, _ driver.Request, s driver.EventSink) (driver.Response, error) {
			close(entered)
			_, err := s.(driver.DecisionCapableSink).RequestDecision(ctx, driver.DecisionRequest{Kind: driver.HumanDecisionPermission})
			return qa.Response(), err
		}
		a := New(d, t22Clock(c), WithEventBuffer(1), WithBlockingEvents(), WithPolicy(Policy{ActiveExecutionTimeout: 100 * time.Millisecond, Approvals: ApprovalPolicy{Timeout: 20 * time.Millisecond}}))
		s := a.Stream(t22Context(t), "full")
		t22Await(t, entered)
		t22Await(t, c.Stops)
		c.Advance(time.Hour)
		es, r, err := t22Drain(s)
		t22Carrier(t, r, err, ReasonApprovalTimeout)
		t22Terminal(t, es, s.RunID(), ReasonApprovalTimeout)
	})
}

func TestAlignmentPolicyBudgetSealAndAtomicFinalize(t *testing.T) {
	for _, mode := range []string{"elapsed-unscheduled", "stale-after-seal", "committed-delay", "finalize-error", "parent-before-commit", "parent-after-commit", "missing-checkpoint", "schema-failure", "cleanup-error"} {
		t.Run(mode, func(t *testing.T) {
			c := qa.NewClock()
			store := qa.NewStore()
			d := qa.NewDriver()
			ctx, cancel := context.WithCancelCause(t22Context(t))
			defer cancel(nil)
			cause := errors.New("store-origin")
			d.RunFunc = func(context.Context, driver.Request, driver.EventSink) (driver.Response, error) {
				r := qa.Response()
				switch mode {
				case "elapsed-unscheduled":
					c.Elapse(100 * time.Millisecond)
				case "missing-checkpoint":
					r.Checkpoint = nil
				case "schema-failure":
					r.StructuredOutput = &driver.StructuredOutput{RawJSON: []byte(`{"ok":"wrong-type"}`)}
				}
				return r, nil
			}
			store.BeforeFinalize = func(ctx context.Context, _ threadstore.FinalizeRequest) error {
				switch mode {
				case "stale-after-seal":
					c.Elapse(time.Hour)
					c.FireAllStale()
				case "finalize-error":
					return cause
				case "parent-before-commit":
					cancel(cause)
					if ctx.Err() == nil {
						return errors.New("Finalize lost parent context")
					}
				}
				return nil
			}
			store.AfterFinalize = func(context.Context) error {
				if mode == "committed-delay" || mode == "parent-after-commit" {
					c.Advance(time.Hour)
					if mode == "parent-after-commit" {
						cancel(cause)
					}
				}
				return nil
			}
			opts := []Option{WithThreadStore(store), t22Clock(c), WithPolicy(Policy{ActiveExecutionTimeout: 100 * time.Millisecond})}
			if mode == "cleanup-error" {
				opts = append(opts, WithRunServices(t22Service{detach: func(context.Context) error { c.Advance(time.Hour); return cause }}))
			}
			a := New(d, opts...)
			calls := []CallOption{}
			if mode == "schema-failure" {
				calls = append(calls, t22Schema(map[string]any{"type": "object", "properties": map[string]any{"ok": map[string]any{"type": "boolean"}}, "required": []any{"ok"}}))
			}
			s := a.Thread("opaque:seal\x00key").Stream(ctx, "seal", calls...)
			es, r, err := t22Drain(s)
			record, resolveErr := store.Resolve(context.Background(), threadstore.Query{Key: "opaque:seal\x00key"})
			if resolveErr != nil {
				t.Fatal(resolveErr)
			}
			wantCommit := mode == "stale-after-seal" || mode == "committed-delay" || mode == "parent-after-commit" || mode == "cleanup-error"
			if (record != nil) != wantCommit {
				t.Fatalf("atomic state=%#v wantCommit=%v", record, wantCommit)
			}
			reason := FailureReason("")
			switch mode {
			case "elapsed-unscheduled":
				reason = ReasonActiveExecutionTimeout
			case "finalize-error", "missing-checkpoint", "cleanup-error":
				reason = ReasonInfrastructure
			case "parent-before-commit", "parent-after-commit":
				reason = ReasonCancelled
			case "schema-failure":
				reason = ReasonPolicyViolation
			}
			if mode == "parent-after-commit" && err == nil {
				reason = ""
			}
			if reason != "" {
				t22Carrier(t, r, err, reason)
			} else if err != nil || r == nil {
				t.Fatalf("sealed healthy failed: %v", err)
			}
			t22Terminal(t, es, s.RunID(), reason)
			if mode == "elapsed-unscheduled" || mode == "missing-checkpoint" || mode == "schema-failure" {
				if store.Finalizes.Load() != 0 {
					t.Fatal("unhealthy candidate reached Finalize")
				}
			}
			if errors.Is(err, ErrActiveExecutionTimeout) && mode != "elapsed-unscheduled" {
				t.Fatal("late timer fabricated active timeout")
			}
			if (mode == "cleanup-error" || mode == "finalize-error" || mode == "parent-before-commit") && !errors.Is(err, cause) {
				t.Fatal("store/cleanup cause lost")
			}
		})
	}
}

func TestAlignmentPolicySelectedCauseNotificationGap(t *testing.T) {
	for _, preDriver := range []bool{false, true} {
		for _, localFirst := range []bool{false, true} {
			t.Run(fmt.Sprintf("preDriver=%v/localFirst=%v", preDriver, localFirst), func(t *testing.T) {
				clock := qa.NewClock()
				d := qa.NewDriver()
				parent, cancel := context.WithCancelCause(t22Context(t))
				defer cancel(nil)
				foreign := &ActiveExecutionTimeoutError{Limit: 777 * time.Second}
				trigger := func(ctx context.Context) {
					if localFirst {
						stopping, release := make(chan struct{}), make(chan struct{})
						clock.SetStopHook(func() { close(stopping); <-release })
						clock.Elapse(100 * time.Millisecond)
						fired := make(chan struct{})
						go func() { clock.FireDue(); close(fired) }()
						t22Await(t, stopping)
						cancel(foreign)
						close(release)
						t22Await(t, fired)
					} else {
						cancel(foreign)
						<-ctx.Done()
						clock.Advance(100 * time.Millisecond)
					}
				}
				d.RunFunc = func(ctx context.Context, _ driver.Request, _ driver.EventSink) (driver.Response, error) {
					trigger(ctx)
					return qa.Response(), errors.Join(ctx.Err(), context.Cause(ctx))
				}
				opts := []Option{t22Clock(clock), WithPolicy(Policy{ActiveExecutionTimeout: 100 * time.Millisecond})}
				if preDriver {
					opts = append(opts, WithRunServices(t22Service{attach: func(ctx context.Context) (RunAttachment, error) {
						trigger(ctx)
						return RunAttachment{}, errors.Join(ctx.Err(), context.Cause(ctx))
					}}))
				}
				a := New(d, opts...)
				s := a.Stream(parent, "selected")
				es, r, err := t22Drain(s)
				reason := ReasonCancelled
				if localFirst {
					reason = ReasonActiveExecutionTimeout
				}
				t22Terminal(t, es, s.RunID(), reason)
				if preDriver {
					var re *RunError
					if r != nil || errors.As(err, &re) || d.Calls.Load() != 0 {
						t.Fatalf("pre-driver fabricated result: %#v %v", r, err)
					}
				} else {
					t22Carrier(t, r, err, reason)
				}
				if !errors.Is(err, foreign) || !errors.Is(err, context.Canceled) {
					t.Fatalf("inherited cause lost: %v", err)
				}
				if localFirst {
					var limit *ActiveExecutionTimeoutError
					if !errors.As(err, &limit) || limit.Limit != 100*time.Millisecond {
						t.Fatalf("selected local Limit missing: %#v", limit)
					}
				}
				for range 3 {
					r2, e2 := s.Result()
					if r2 != r || e2 != err {
						t.Fatal("Result mutated after close")
					}
				}
			})
		}
	}
}

func TestAlignmentPolicyIndependentParentAndClose(t *testing.T) {
	for _, mode := range []string{"parent-deadline", "wall-timeout", "close", "member-ask"} {
		t.Run(mode, func(t *testing.T) {
			c := qa.NewClock()
			d := qa.NewDriver()
			ctx := t22Context(t)
			var cancel context.CancelFunc
			if mode == "parent-deadline" {
				ctx, cancel = context.WithTimeout(ctx, 20*time.Millisecond)
				defer cancel()
			}
			aOpts := []Option{t22Clock(c), WithPolicy(Policy{ActiveExecutionTimeout: 100 * time.Millisecond})}
			if mode == "wall-timeout" {
				aOpts = append(aOpts, WithTimeout(20*time.Millisecond))
			}
			entered := make(chan struct{})
			d.RunFunc = func(ctx context.Context, _ driver.Request, s driver.EventSink) (driver.Response, error) {
				if mode == "member-ask" {
					childClock := qa.NewClock()
					child := qa.NewDriver()
					child.RunFunc = func(ctx context.Context, _ driver.Request, s driver.EventSink) (driver.Response, error) {
						_, err := s.(driver.DecisionCapableSink).RequestDecision(ctx, driver.DecisionRequest{Kind: driver.HumanDecisionPermission})
						return qa.Response(), err
					}
					member := New(child, t22Clock(childClock), WithPolicy(Policy{ActiveExecutionTimeout: time.Second}), OnApproval(func(ctx context.Context, _ *ApprovalRequest) error {
						c.Advance(100 * time.Millisecond)
						<-ctx.Done()
						return ctx.Err()
					}))
					_, err := member.Run(ctx, "member")
					return qa.Response(), err
				}
				_, err := s.(driver.DecisionCapableSink).RequestDecision(ctx, driver.DecisionRequest{Kind: driver.HumanDecisionPermission})
				return qa.Response(), err
			}
			aOpts = append(aOpts, OnApproval(func(ctx context.Context, _ *ApprovalRequest) error {
				close(entered)
				c.Advance(time.Hour)
				<-ctx.Done()
				return ctx.Err()
			}))
			a := New(d, aOpts...)
			s := a.Stream(ctx, "outer")
			if mode == "close" {
				t22Await(t, entered)
				if err := a.Close(t22Context(t)); err != nil {
					t.Fatal(err)
				}
			}
			es, r, err := t22Drain(s)
			reason := ReasonDeadlineExceeded
			if mode == "close" {
				reason = ReasonCancelled
			}
			if mode == "member-ask" {
				reason = ReasonActiveExecutionTimeout
			}
			t22Carrier(t, r, err, reason)
			t22Terminal(t, es, s.RunID(), reason)
			if mode == "close" {
				_, err = a.Run(context.Background(), "late")
				if !errors.Is(err, ErrAgentClosed) {
					t.Fatalf("close allowed new run: %v", err)
				}
			}
		})
	}
}

func TestAlignmentPolicyStaticAndOptionResolution(t *testing.T) {
	var _ Option = WithAppendSystemPrompt("")
	var _ CallOption = WithAppendSystemPrompt("")
	var _ SharedOption = WithPolicy(Policy{})
	var _ Option = WithThreadStore(qa.NewStore())
	for _, mode := range []string{"negative", "append-unsupported", "invalid-utf8", "invalid-schema", "schema-unsupported", "clear-policy", "clear-append", "last-call"} {
		t.Run(mode, func(t *testing.T) {
			d := qa.NewDriver()
			c := qa.NewClock()
			var attaches atomic.Int32
			var captured driver.Request
			d.RunFunc = func(_ context.Context, r driver.Request, _ driver.EventSink) (driver.Response, error) {
				captured = r
				c.Advance(time.Hour)
				return qa.Response(), nil
			}
			opts := []Option{t22Clock(c), WithPolicy(Policy{ActiveExecutionTimeout: 100 * time.Millisecond, Sandbox: ReadOnly}), WithAppendSystemPrompt("default secret"), WithRunServices(t22Service{attach: func(context.Context) (RunAttachment, error) { attaches.Add(1); return RunAttachment{}, nil }})}
			calls := []CallOption{}
			var sentinel error
			switch mode {
			case "negative":
				calls = append(calls, WithPolicy(Policy{ActiveExecutionTimeout: -time.Nanosecond}))
				sentinel = ErrInvalidPolicy
			case "append-unsupported":
				d.Desc.SystemPrompt.Append = false
				sentinel = ErrSystemPromptUnsupported
			case "invalid-utf8":
				calls = append(calls, WithAppendSystemPrompt(string([]byte{255})))
				sentinel = ErrSystemPromptUnsupported
			case "invalid-schema":
				calls = append(calls, t22Schema(map[string]any{"type": 123}))
			case "schema-unsupported":
				d.Desc.StructuredOutput = driver.StructuredOutputCapability{}
				calls = append(calls, t22Schema(map[string]any{"type": "object"}))
				sentinel = driver.ErrStructuredOutputUnsupported
			case "clear-policy":
				calls = append(calls, WithPolicy(Policy{}))
			case "clear-append":
				calls = append(calls, WithAppendSystemPrompt(""), WithPolicy(Policy{}))
			case "last-call":
				calls = append(calls, WithAppendSystemPrompt("discard"), WithAppendSystemPrompt("甲\n\"乙\""), WithPolicy(Policy{}))
			}
			a := New(d, opts...)
			s := a.Stream(t22Context(t), "original-user-prompt", calls...)
			es, r, err := t22Drain(s)
			if strings.HasPrefix(mode, "clear") || mode == "last-call" {
				if err != nil || r == nil || d.Calls.Load() != 1 || captured.Prompt != "original-user-prompt" || captured.Policy.Isolation != "" {
					t.Fatalf("whole policy not replaced: %v req=%#v", err, captured)
				}
				want := "default secret"
				if mode == "clear-append" {
					want = ""
				}
				if mode == "last-call" {
					want = "甲\n\"乙\""
				}
				if captured.AppendSystemPrompt != want {
					t.Fatalf("append=%q", captured.AppendSystemPrompt)
				}
			} else {
				var re *RunError
				if err == nil || r != nil || errors.As(err, &re) || d.Calls.Load() != 0 || attaches.Load() != 0 || len(es) != 0 {
					t.Fatalf("static error got resources/lifecycle: %v calls=%d attach=%d events=%d", err, d.Calls.Load(), attaches.Load(), len(es))
				}
				if sentinel != nil && !errors.Is(err, sentinel) {
					t.Fatalf("sentinel lost: %v", err)
				}
				if strings.Contains(err.Error(), "default secret") {
					t.Fatal("append leaked into error")
				}
			}
		})
	}
}

func TestAlignmentPolicySchemaLateDemand(t *testing.T) {
	for _, mode := range []string{"native-explicit", "prompt-inherited", "legacy-nil", "exact-false", "late-batch", "no-schema-ask", "initial-batch-ask"} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%v", mode, stream), func(t *testing.T) {
				d := qa.NewDriver()
				all := &driver.StructuredOutputHITLCapability{Permission: true, PlanReview: true, Question: true}
				d.Desc.StructuredOutput.NativeHITL = &driver.StructuredOutputHITLCapability{PlanReview: true, Question: true}
				d.Desc.StructuredOutput.PromptValidateHITL = all
				d.Desc.StructuredOutput.WorksWithHITL = false
				policy := Policy{Approvals: ApprovalPolicy{Question: QuestionAsk, Permission: ApprovalAutoApprove, PlanReview: ApprovalAutoApprove}}
				want := driver.StructuredOutputSourceNative
				wantRich := true
				schema := true
				switch mode {
				case "prompt-inherited":
					policy.Approvals.Permission = ApprovalInherit
					want = driver.StructuredOutputSourcePromptValidate
				case "legacy-nil":
					d.Desc.StructuredOutput.NativeHITL = nil
					d.Desc.StructuredOutput.WorksWithHITL = true
				case "exact-false":
					d.Desc.StructuredOutput.NativeHITL = &driver.StructuredOutputHITLCapability{}
					d.Desc.StructuredOutput.WorksWithHITL = true
					want = driver.StructuredOutputSourcePromptValidate
				case "late-batch":
					policy.Approvals.Question = QuestionAutoDeny
					d.Desc.Observation.Batch.Todos = true
					wantRich = false
				case "no-schema-ask":
					schema = false
					want = ""
					d.Desc.Observation.Batch.Todos = true
				case "initial-batch-ask":
					schema = false
					want = ""
					d.Rich = false
					wantRich = false
				}
				var attaches, asks atomic.Int32
				d.RunFunc = func(ctx context.Context, r driver.Request, s driver.EventSink) (driver.Response, error) {
					if r.StructuredOutputSource != want || r.Streaming != wantRich {
						return qa.Response(), fmt.Errorf("source=%s rich=%v want=%s/%v", r.StructuredOutputSource, r.Streaming, want, wantRich)
					}
					if mode != "late-batch" {
						kind := driver.HumanDecisionQuestion
						if mode == "prompt-inherited" {
							kind = driver.HumanDecisionPermission
						}
						resp, err := s.(driver.DecisionCapableSink).RequestDecision(ctx, driver.DecisionRequest{Kind: kind})
						if err != nil {
							return qa.Response(), err
						}
						if resp.Result != driver.DecisionApproved && resp.Result != driver.DecisionAnswered {
							return qa.Response(), fmt.Errorf("Ask disappeared: %s", resp.Result)
						}
					}
					res := qa.Response()
					if schema {
						res.Output = `{"ok":true}`
						if want == driver.StructuredOutputSourceNative {
							res.StructuredOutput = &driver.StructuredOutput{RawJSON: []byte(`{"ok":true}`)}
						}
					}
					return res, nil
				}
				a := New(d, WithPolicy(policy), OnApproval(func(ctx context.Context, r *ApprovalRequest) error {
					asks.Add(1)
					if r.Kind == ApprovalQuestion {
						return r.Answer(ctx, "yes")
					}
					return r.Approve(ctx)
				}), WithRunServices(t22Service{attach: func(context.Context) (RunAttachment, error) {
					attaches.Add(1)
					return RunAttachment{Observation: ObservationDemand{Todos: true}}, nil
				}}))
				var calls []CallOption
				if schema {
					calls = append(calls, t22Schema(map[string]any{"type": "object", "required": []any{"ok"}, "properties": map[string]any{"ok": map[string]any{"type": "boolean"}}}))
				}
				var r *Result
				var err error
				if stream {
					_, r, err = t22Drain(a.Stream(t22Context(t), "schema", calls...))
				} else {
					r, err = a.Run(t22Context(t), "schema", calls...)
				}
				if err != nil || r == nil || d.Calls.Load() != 1 || attaches.Load() != 1 {
					t.Fatalf("single resolution failed: %v", err)
				}
				if mode != "late-batch" && asks.Load() != 1 {
					t.Fatal("no real Ask exchange")
				}
				if schema {
					var value struct {
						OK bool `json:"ok"`
					}
					if err := r.Decode(&value); err != nil || !value.OK {
						t.Fatalf("decode=%#v %v", value, err)
					}
				}
			})
		}
	}
}

func TestAlignmentPolicyPartialAndHealthyState(t *testing.T) {
	for _, mode := range []string{"transport", "nonzero", "protocol", "cancel", "missing", "cleanup"} {
		t.Run(mode, func(t *testing.T) {
			store := qa.NewStore()
			d := qa.NewDriver()
			a := New(d, WithThreadStore(store))
			th := a.Thread("verbatim/key:α")
			if _, err := th.Run(t22Context(t), "seed"); err != nil {
				t.Fatal(err)
			}
			before, _ := store.Resolve(context.Background(), threadstore.Query{Key: "verbatim/key:α"})
			origin := errors.New("transport-origin")
			ctx, cancel := context.WithCancel(t22Context(t))
			defer cancel()
			d.RunFunc = func(context.Context, driver.Request, driver.EventSink) (driver.Response, error) {
				r := qa.Response()
				r.Checkpoint.State.ResumeID = "must-not-replace"
				switch mode {
				case "transport":
					return r, origin
				case "protocol":
					r.Failure = &driver.RunFailure{Code: driver.FailureAgentError, Message: "formal malformed protocol"}
					return r, origin
				case "nonzero":
					r.ExitCode = 9
				case "cancel":
					cancel()
					return r, context.Canceled
				case "missing":
					r.Checkpoint = nil
				}
				return r, nil
			}
			opts := []CallOption{}
			if mode == "cleanup" {
				opts = append(opts, WithRunServices(t22Service{detach: func(context.Context) error { return origin }}))
			}
			s := th.Stream(ctx, "interrupt", opts...)
			es, r, err := t22Drain(s)
			reason := ReasonInfrastructure
			switch mode {
			case "nonzero", "protocol":
				reason = ReasonAgentError
			case "cancel":
				reason = ReasonCancelled
			}
			re := t22Carrier(t, r, err, reason)
			t22Terminal(t, es, s.RunID(), reason)
			after, _ := store.Resolve(context.Background(), threadstore.Query{Key: "verbatim/key:α"})
			if mode == "cleanup" {
				if after.State.ResumeID != "must-not-replace" {
					t.Fatal("healthy committed state rolled back")
				}
			} else if !reflect.DeepEqual(before, after) {
				t.Fatalf("healthy record contaminated before=%#v after=%#v", before, after)
			}
			if mode == "transport" || mode == "protocol" {
				if !errors.Is(re, origin) {
					t.Fatal("original cause lost")
				}
			}
			d2 := qa.NewDriver()
			d2.RunFunc = func(context.Context, driver.Request, driver.EventSink) (driver.Response, error) {
				return qa.Response(), origin
			}
			a2 := New(d2)
			rr, e := a2.Run(t22Context(t), "run")
			rRun := t22Carrier(t, rr, e, ReasonInfrastructure).Result
			_, rr, e = t22Drain(a2.Stream(t22Context(t), "stream"))
			rStream := t22Carrier(t, rr, e, ReasonInfrastructure).Result
			if !reflect.DeepEqual(rRun.Raw(), rStream.Raw()) || !reflect.DeepEqual(rRun.Transcript(), rStream.Transcript()) || !reflect.DeepEqual(rRun.Services(), rStream.Services()) || !reflect.DeepEqual(rRun.Usage, rStream.Usage) || rRun.Text != rStream.Text || rRun.Summary != rStream.Summary || rRun.Model != rStream.Model || rRun.Provider != rStream.Provider || !reflect.DeepEqual(rRun.Metadata, rStream.Metadata) {
				t.Fatal("Run and Stream result layers diverged")
			}
		})
	}
}

func TestAlignmentPolicyNativeFileIntegrity(t *testing.T) {
	text := "甲\n\"乙\"\t\\end"
	for _, mode := range []string{"roundtrip", "same-size-tamper", "symlink", "cancel-empty"} {
		t.Run(mode, func(t *testing.T) {
			ctx := t22Context(t)
			if mode == "cancel-empty" {
				c, cancel := context.WithCancelCause(ctx)
				why := errors.New("cancelled materialization")
				cancel(why)
				f, err := systemprompt.Materialize(c, "")
				if f != nil || !errors.Is(err, why) {
					t.Fatalf("cancelled no-op=%v %v", f, err)
				}
				return
			}
			f, err := systemprompt.Materialize(ctx, text)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			p := f.Path()
			got, err := os.ReadFile(p)
			if err != nil || string(got) != text {
				t.Fatal("native bytes changed")
			}
			info, _ := os.Stat(p)
			if runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
				t.Fatalf("mode=%o", info.Mode().Perm())
			}
			switch mode {
			case "same-size-tamper":
				if err := os.WriteFile(p, []byte(strings.Repeat("x", len(text))), 0600); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				target := filepath.Join(t.TempDir(), "external")
				os.WriteFile(target, []byte(text), 0600)
				os.Remove(p)
				if err := os.Symlink(target, p); err != nil {
					t.Fatal(err)
				}
			}
			err = f.Verify(ctx)
			if (mode == "roundtrip") != (err == nil) {
				t.Fatalf("Verify %s=%v", mode, err)
			}
			if mode == "roundtrip" {
				if err = f.Close(); err != nil {
					t.Fatal(err)
				}
				if _, err = os.Stat(p); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("owned carrier not removed")
				}
				if err = f.Close(); err != nil {
					t.Fatal("Close non-idempotent")
				}
			}
		})
	}
}

type t22Spoof struct{ Called *atomic.Int32 }

func (e t22Spoof) Error() string { return "private provider secret" }
func (e t22Spoof) As(target any) bool {
	e.Called.Add(1)
	return errors.As(tool.Reject("injected", "private provider secret"), target)
}
func TestAlignmentPolicySafeToolCorrection(t *testing.T) {
	schema := []byte(`{"type":"object","required":["count","mode","nested"],"properties":{"count":{"type":"integer","minimum":1},"mode":{"enum":["safe"]},"nested":{"type":"object","required":["name"],"properties":{"name":{"type":"string"}},"additionalProperties":false}},"additionalProperties":false}`)
	var calls atomic.Int32
	def := tool.Define("t22_input", "safe input", func(context.Context, map[string]any) (string, error) { calls.Add(1); return "ok", nil }, tool.InputSchemaJSON(schema))
	cases := map[string]string{"syntax": `{"count":`, "required": `{}`, "type": `{"count":"PRIVATE_VALUE","mode":"safe","nested":{"name":"n"}}`, "enum": `{"count":1,"mode":"PRIVATE_VALUE","nested":{"name":"n"}}`, "nested": `{"count":1,"mode":"safe","nested":{"name":8}}`, "extra": `{"count":1,"mode":"safe","nested":{"name":"n"},"z":"PRIVATE_VALUE","a":"PRIVATE_VALUE"}`, "malicious-key": `{"count":1,"mode":"safe","nested":{"name":"n"},"\n\"injected":"PRIVATE_VALUE"}`, "long-key": `{"count":1,"mode":"safe","nested":{"name":"n"},"` + strings.Repeat("界", 3000) + `":"PRIVATE_VALUE"}`}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			var stable string
			for range 3 {
				raw, err := def.Invoke(context.Background(), json.RawMessage(input))
				code, message, ok := tool.AsRejection(err)
				if raw != nil || !errors.Is(err, tool.ErrInvalidInput) || !ok || code != "invalid_input" || message == "" || len(message) > 4096 || !utf8.ValidString(message) || strings.Contains(message, "PRIVATE_VALUE") || strings.Contains(message, "additionalProperties") || strings.Contains(message, "\n") || strings.Contains(message, "\r") {
					t.Fatalf("unsafe correction code=%q message=%q err=%v", code, message, err)
				}
				if stable != "" && stable != message {
					t.Fatal("nondeterministic correction")
				}
				stable = message
			}
			if calls.Load() != 0 {
				t.Fatal("invalid input entered handler")
			}
		})
	}
	var called atomic.Int32
	if _, _, ok := tool.AsRejection(t22Spoof{&called}); ok || called.Load() != 0 {
		t.Fatal("external As minted rejection")
	}
	goDecode := tool.Define("t22_decode", "decode", func(context.Context, struct {
		Count int `json:"count"`
	}) (string, error) {
		calls.Add(1)
		return "ok", nil
	}, tool.InputSchemaJSON([]byte(`{"type":"object","properties":{"count":{"type":"number"}}}`)))
	_, err := goDecode.Invoke(context.Background(), []byte(`{"count":1.5}`))
	_, message, ok := tool.AsRejection(err)
	if !errors.Is(err, tool.ErrInvalidInput) || !ok || strings.Contains(strings.ToLower(message), "syntax") || calls.Load() != 0 {
		t.Fatalf("Go decoding falsely reported syntax: %q %v", message, err)
	}
}

// Build once per outer test process, then execute the external public-composition
// test binary on every TestAlignmentPolicy invocation (including all -count=20).
var t22External struct {
	sync.Once
	path, dir string
	err       error
	build     []byte
}

func TestAlignmentPolicyPublicComposition(t *testing.T) {
	if os.Getenv("T22_EXTERNAL_FIXTURE") == "1" {
		t.Fatal("recursive external fixture invocation")
	}
	t22External.Do(func() {
		var err error
		t22External.dir, err = os.MkdirTemp("", "t22-external-tests-")
		if err != nil {
			t22External.err = err
			return
		}
		t22External.path = filepath.Join(t22External.dir, "public.test")
		if runtime.GOOS == "windows" {
			t22External.path += ".exe"
		}
		args := []string{"test", "-c", "-o", t22External.path, "./internal/testutil/alignmentpolicy"}
		if qa.RaceEnabled {
			args = append(args, "-race")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, filepath.Join(runtime.GOROOT(), "bin", "go"), args...)
		cmd.Env = os.Environ()
		t22External.build, t22External.err = cmd.CombinedOutput()
	})
	if t22External.err != nil {
		t.Fatalf("external test build: %v\n%s", t22External.err, t22External.build)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, filepath.Join(runtime.GOROOT(), "bin", "go"), "tool", "test2json", "-t", "-p", "github.com/agent-dance/agent-adaptor/internal/testutil/alignmentpolicy", t22External.path, "-test.v=test2json", "-test.run=TestT22", "-test.timeout=40s", "-test.count=1")
	cmd.Env = append(os.Environ(), "T22_EXTERNAL_FIXTURE=1")
	output, err := cmd.CombinedOutput()
	t.Logf("external public test output:\n%s", output)
	if err != nil {
		t.Fatalf("external composition: %v", err)
	}
	if !strings.Contains(string(output), `"Action":"pass","Package":"github.com/agent-dance/agent-adaptor/internal/testutil/alignmentpolicy","Test":"TestT22`) || strings.Contains(string(output), `"Action":"skip"`) {
		t.Fatal("external checks zero or skipped")
	}
}

// Compile-time check that the seam remains private and reusable without a public
// context wrapper. The public behavior is tested through Agent above.
var _ activebudget.Clock = (*qa.Clock)(nil)

var t22Formal struct {
	sync.Once
	path   string
	err    error
	output []byte
}

func t22FormalCommand(t *testing.T) string {
	t.Helper()
	t22Formal.Do(func() {
		dir, err := os.MkdirTemp("", "t22-formal-binary-")
		if err != nil {
			t22Formal.err = err
			return
		}
		t22Formal.path = filepath.Join(dir, "formal")
		if runtime.GOOS == "windows" {
			t22Formal.path += ".exe"
		}
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, filepath.Join(runtime.GOROOT(), "bin", "go"), "build", "-o", t22Formal.path, "./internal/testutil/alignmentpolicy/cmd/formal")
		t22Formal.output, t22Formal.err = cmd.CombinedOutput()
	})
	if t22Formal.err != nil {
		t.Fatalf("fixture build: %v %s", t22Formal.err, t22Formal.output)
	}
	return t22Formal.path
}
func TestAlignmentPolicyClaudeFormalSchema(t *testing.T) {
	for _, mode := range []string{"question", "plan", "permission", "zero-policy", "invalid", "deny", "timeout"} {
		for _, thread := range []bool{false, true} {
			for _, streaming := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/thread=%v/stream=%v", mode, thread, streaming), func(t *testing.T) {
					dir := t.TempDir()
					receipt := filepath.Join(dir, "receipt.json")
					d := claude.Driver(claude.Config{CommonConfig: claude.CommonConfig{Command: t22FormalCommand(t), CWD: dir, Env: []driver.EnvBinding{{Name: "T22_RECEIPT", Value: receipt}}}})
					store := qa.NewStore()
					policy := Policy{Approvals: ApprovalPolicy{Permission: ApprovalAutoApprove, PlanReview: ApprovalAutoApprove, Question: QuestionAsk, Timeout: 20 * time.Millisecond}}
					scenario := mode
					switch mode {
					case "plan":
						policy.Approvals.PlanReview = ApprovalAsk
					case "permission":
						policy.Approvals.Permission = ApprovalInherit
					case "zero-policy":
						policy = Policy{}
					case "deny", "timeout":
						scenario = "question"
					}
					var asks atomic.Int32
					a := New(d, WithThreadStore(store), WithProfile(profile.Dedicated(filepath.Join(dir, "profile"))), WithPolicy(policy), WithAppendSystemPrompt("NATIVE_SECRET_甲\n\"乙\""), OnApproval(func(ctx context.Context, r *ApprovalRequest) error {
						asks.Add(1)
						switch mode {
						case "deny":
							return r.Deny(ctx, "fixture denied")
						case "timeout":
							<-ctx.Done()
							return ctx.Err()
						}
						if r.Kind == ApprovalQuestion {
							return r.Answer(ctx, "docs")
						}
						return r.Approve(ctx)
					}))
					defer a.Close(t22Context(t))
					var runner Runner = a
					if thread {
						runner = a.Thread("claude/formal")
					}
					opts := []CallOption{WithSpawn(), WithSchemaJSON([]byte(`{"type":"object","properties":{"ok":{"type":"boolean"}},"required":["ok"],"additionalProperties":false}`))}
					var s Stream
					var es []Event
					var r *Result
					var err error
					if streaming {
						s = runner.Stream(t22Context(t), "SCENARIO="+scenario, opts...)
						es, r, err = t22Drain(s)
					} else {
						r, err = runner.Run(t22Context(t), "SCENARIO="+scenario, opts...)
					}
					reason := FailureReason("")
					switch mode {
					case "invalid":
						reason = ReasonPolicyViolation
					case "deny":
						reason = ReasonApprovalDenied
					case "timeout":
						reason = ReasonApprovalTimeout
					}
					if reason != "" {
						var re *RunError
						if r != nil || !errors.As(err, &re) || re.Reason != reason || re.Result == nil {
							t.Fatalf("formal failure=%#v %v carrier=%#v", r, err, re)
						}
						r = re.Result
						if store.Finalizes.Load() != 0 {
							t.Fatal("formal failure persisted checkpoint")
						}
					} else {
						if err != nil || r == nil {
							t.Fatalf("formal success=%#v %v", r, err)
						}
						var out struct {
							OK bool `json:"ok"`
						}
						if err = r.Decode(&out); err != nil || !out.OK {
							t.Fatalf("formal structured decode=%#v %v", out, err)
						}
						if r.Raw().Terminal == nil || !strings.Contains(r.Raw().Stdout, `"type":"result"`) || r.Usage == nil || r.Usage.InputTokens != 0 || r.Usage.OutputTokens != 0 {
							t.Fatalf("formal output layers lost %#v", r)
						}
						if thread && store.Finalizes.Load() != 1 {
							t.Fatal("healthy formal checkpoint missing")
						}
						raw, err := os.ReadFile(receipt)
						if err != nil {
							t.Fatal(err)
						}
						var v map[string]any
						if err = json.Unmarshal(raw, &v); err != nil {
							t.Fatal(err)
						}
						args, _ := json.Marshal(v["args"])
						native := strings.Contains(string(args), "--json-schema")
						interactive := strings.Contains(string(args), "--permission-prompt-tool")
						wantNative := mode != "permission" && mode != "zero-policy"
						if native != wantNative || interactive != (mode != "zero-policy") || (v["response"] != nil) != (mode != "zero-policy") {
							t.Fatalf("formal argv/control mismatch %s receipt=%s", mode, raw)
						}
						if v["append"] != "NATIVE_SECRET_甲\n\"乙\"" || strings.Contains(fmt.Sprint(v["prompt"]), "NATIVE_SECRET") {
							t.Fatal("append entered user channel or lost bytes")
						}
						if strings.Contains(fmt.Sprint(r.Transcript()), "NATIVE_SECRET") || strings.Contains(fmt.Sprint(r.Metadata), "NATIVE_SECRET") {
							t.Fatal("append leaked into semantic audit")
						}
					}
					if mode == "zero-policy" {
						if asks.Load() != 0 {
							t.Fatal("zero raw policy synthesized interaction")
						}
					} else if asks.Load() != 1 {
						t.Fatalf("formal request count=%d", asks.Load())
					}
					if streaming {
						t22Terminal(t, es, s.RunID(), reason)
					}
				})
			}
		}
	}
}

func TestAlignmentPolicyLeaseContinuesDuringAsk(t *testing.T) {
	oldTTL, oldRenew := engine.LeaseTTL, engine.LeaseRenewInterval
	engine.LeaseTTL = func() time.Duration { return time.Second }
	engine.LeaseRenewInterval = func() time.Duration { return 5 * time.Millisecond }
	defer func() { engine.LeaseTTL = oldTTL; engine.LeaseRenewInterval = oldRenew }()
	c := qa.NewClock()
	store := qa.NewStore()
	entered := make(chan struct{})
	leaseCause := errors.New("lease-fenced")
	store.OnRenew = func(ctx context.Context) error {
		select {
		case <-entered:
			return leaseCause
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	d := qa.NewDriver()
	d.RunFunc = func(ctx context.Context, _ driver.Request, s driver.EventSink) (driver.Response, error) {
		_, err := s.(driver.DecisionCapableSink).RequestDecision(ctx, driver.DecisionRequest{Kind: driver.HumanDecisionPermission})
		return qa.Response(), err
	}
	a := New(d, WithThreadStore(store), t22Clock(c), WithPolicy(Policy{ActiveExecutionTimeout: 100 * time.Millisecond}), OnApproval(func(ctx context.Context, r *ApprovalRequest) error {
		close(entered)
		c.Advance(time.Hour)
		<-ctx.Done()
		return ctx.Err()
	}))
	s := a.Thread("leased").Stream(t22Context(t), "lease")
	es, r, err := t22Drain(s)
	t22Carrier(t, r, err, ReasonInfrastructure)
	t22Terminal(t, es, s.RunID(), ReasonInfrastructure)
	if !errors.Is(err, leaseCause) || store.Renews.Load() == 0 || store.Finalizes.Load() != 0 || store.Releases.Load() == 0 || errors.Is(err, ErrActiveExecutionTimeout) {
		t.Fatalf("lease/pause isolation err=%v renew=%d final=%d release=%d", err, store.Renews.Load(), store.Finalizes.Load(), store.Releases.Load())
	}
}

func TestAlignmentPolicyThreadTransportAndPromptIdentity(t *testing.T) {
	c := qa.NewClock()
	store := qa.NewStore()
	d := qa.NewDriver()
	d.Desc.Observation.Batch.Todos = true
	d.RunFunc = func(_ context.Context, r driver.Request, _ driver.EventSink) (driver.Response, error) {
		if r.Prompt == "second" && r.Streaming {
			return qa.Response(), errors.New("late demand did not select batch")
		}
		if r.Prompt != "first" && (r.Session == nil || r.Session.State == nil || r.Session.State.ResumeID != "healthy-t22") {
			return qa.Response(), errors.New("compatible transport lost resume")
		}
		return qa.Response(), nil
	}
	a := New(d, WithThreadStore(store), t22Clock(c), WithPolicy(Policy{Approvals: ApprovalPolicy{Permission: ApprovalAutoApprove, PlanReview: ApprovalAutoApprove, Question: QuestionAutoDeny}}), WithAppendSystemPrompt("same native"))
	th := a.Thread("transport-identity")
	if _, err := th.Run(t22Context(t), "first"); err != nil {
		t.Fatal(err)
	}
	before, _ := store.Resolve(context.Background(), threadstore.Query{Key: "transport-identity"})
	if _, err := a.Thread("transport-identity", ResumeOnly()).Run(t22Context(t), "second", WithRunServices(t22Service{attach: func(context.Context) (RunAttachment, error) {
		return RunAttachment{Observation: ObservationDemand{Todos: true}}, nil
	}}), WithSchemaJSON([]byte(`{"type":"object"}`))); err == nil {
		t.Fatal("fixture supplies non-JSON native value; schema must reject")
	} // Next use a valid value without changing construction config.
	d.RunFunc = func(_ context.Context, r driver.Request, _ driver.EventSink) (driver.Response, error) {
		if r.Session == nil || r.Session.State == nil || r.Session.State.ResumeID != "healthy-t22" {
			return qa.Response(), errors.New("resume lost")
		}
		out := qa.Response()
		if r.OutputSchema != nil {
			out.StructuredOutput = &driver.StructuredOutput{RawJSON: []byte(`{}`)}
		}
		return out, nil
	}
	if _, err := a.Thread("transport-identity", ResumeOnly()).Run(t22Context(t), "second", WithRunServices(t22Service{attach: func(context.Context) (RunAttachment, error) {
		return RunAttachment{Observation: ObservationDemand{Todos: true}}, nil
	}}), WithSchemaJSON([]byte(`{"type":"object"}`))); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Thread("transport-identity", ResumeOnly()).Run(t22Context(t), "third", WithPolicy(Policy{})); err != nil {
		t.Fatal(err)
	}
	after, _ := store.Resolve(context.Background(), threadstore.Query{Key: "transport-identity"})
	if before.ID != after.ID {
		t.Fatal("transport/budget re-bound Thread")
	}
	calls := d.Calls.Load()
	for _, text := range []string{"changed native", ""} {
		if _, err := a.Thread("transport-identity", ResumeOnly()).Run(t22Context(t), "change", WithAppendSystemPrompt(text)); !errors.Is(err, ErrThreadIncompatible) {
			t.Fatalf("append drift accepted: %v", err)
		}
	}
	if d.Calls.Load() != calls {
		t.Fatal("incompatible append launched Driver")
	}
}

func TestAlignmentPolicyMCPModeAndInspectSnapshot(t *testing.T) {
	for _, provider := range []string{"claude", "codex"} {
		t.Run(provider, func(t *testing.T) {
			dir := t.TempDir()
			name := ".claude.json"
			data := []byte(`{"mcpServers":{}}`)
			if provider == "codex" {
				name = "config.toml"
				data = []byte("model = \"unchanged\"\n")
			}
			path := filepath.Join(dir, name)
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			before, _ := os.Stat(path)
			payload := driver.MCPPayload{Servers: []driver.MCPServerSpec{{Key: "qa-server", Transport: driver.MCPTransportStdio, Command: "never-executed", Args: []string{"arg"}}}, Fingerprint: "test-semantics"}
			for range 2 {
				if _, err := mcpruntime.SyncResource(t22Context(t), provider, dir, mcpruntime.ProfileKindDedicated, payload); err != nil {
					t.Fatal(err)
				}
				after, _ := os.Stat(path)
				if after.Mode().Perm() != before.Mode().Perm() {
					t.Fatalf("MCP writer changed existing mode %o -> %o", before.Mode().Perm(), after.Mode().Perm())
				}
			}
			next := os.FileMode(0640)
			if runtime.GOOS == "windows" {
				next = 0444
			}
			if err := os.Chmod(path, next); err != nil {
				t.Fatal(err)
			}
			changed, _ := os.Stat(path)
			if changed.Mode().Perm() == before.Mode().Perm() {
				t.Fatal("fixture failed to create observable external mode change")
			}
			if _, err := mcpruntime.SyncResource(t22Context(t), provider, dir, mcpruntime.ProfileKindDedicated, payload); err != nil && runtime.GOOS != "windows" {
				t.Fatal(err)
			}
			observed, _ := os.Stat(path)
			if observed.Mode().Perm() != changed.Mode().Perm() {
				t.Fatal("external permissions silently normalized")
			}
		})
	}
	t.Run("configured-inspect", func(t *testing.T) {
		dir := t.TempDir()
		cfg := claude.Config{CommonConfig: claude.CommonConfig{Command: t22FormalCommand(t), CWD: dir, Env: []driver.EnvBinding{{Name: "CLAUDE_CONFIG_DIR", Value: dir}}}}
		d := claude.Driver(cfg)
		cfg.Command = filepath.Join(dir, "missing")
		a := New(d, WithAppendSystemPrompt("native private"))
		report, err := a.Inspect().Environment(t22Context(t))
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, check := range report.Checks {
			if check.Code == "command_found" {
				found = true
			}
			if check.Code == "command_missing" {
				t.Fatal("Inspect lost captured command")
			}
		}
		if !found {
			t.Fatalf("Inspect missing configured executable evidence: %#v", report)
		}
		if _, err := os.Stat(filepath.Join(dir, "receipt.json")); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("Inspect executed run")
		}
	})
}

func TestAlignmentPolicyResidentSchemaPrewarm(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "process.jsonl")
	socket, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := socket.Addr().String()
	socket.Close()
	d := codebuddy.Driver(codebuddy.Config{CommonConfig: codebuddy.CommonConfig{Command: t22FormalCommand(t), CWD: dir, GracePeriod: time.Second, Env: []driver.EnvBinding{{Name: "T22_RESIDENT", Value: "1"}, {Name: "T22_PROCESS_LOG", Value: logPath}, {Name: "T22_WRITER_ADDRESS", Value: address}}}})
	store := qa.NewStore()
	a := New(d, WithThreadStore(store), WithProfile(profile.Dedicated(filepath.Join(dir, "profile"))), WithAppendSystemPrompt("startup alpha"))
	defer a.Close(t22Context(t))
	run := func(prompt string, opts ...CallOption) {
		t.Helper()
		r, err := a.Thread("resident", ResumeOnly()).Run(t22Context(t), prompt, opts...)
		if err != nil || r == nil {
			t.Fatalf("%s: %v", prompt, err)
		}
	}
	if _, err := a.Thread("resident").Run(t22Context(t), "first"); err != nil {
		t.Fatal(err)
	}
	before, _ := store.Resolve(context.Background(), threadstore.Query{Key: "resident"})
	run("native-second", WithSchemaJSON([]byte(`{"type":"object","properties":{"ok":{"type":"boolean"}},"required":["ok"]}`)))
	read := func() []map[string]any {
		b, err := os.ReadFile(logPath)
		if err != nil {
			t.Fatal(err)
		}
		var rows []map[string]any
		for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
			var row map[string]any
			if err := json.Unmarshal([]byte(line), &row); err != nil {
				t.Fatal(err)
			}
			rows = append(rows, row)
		}
		return rows
	} // prewarm may be alive before its program is scheduled; the next turn proves use.
	run("third")
	run("fourth")
	rows := read()
	starts := 0
	prompts := map[string]float64{}
	for _, row := range rows {
		if row["kind"] == "start" {
			starts++
			args, _ := json.Marshal(row["args"])
			if !strings.Contains(string(args), "startup alpha") {
				t.Fatal("native append missing from startup")
			}
			if starts > 1 && !strings.Contains(string(args), "--resume") {
				t.Fatal("temporary/prewarm start lacks formal resume")
			}
		}
		if row["kind"] == "prompt" {
			p := fmt.Sprint(row["prompt"])
			if _, exists := prompts[p]; exists {
				t.Fatalf("prompt replay: %s", p)
			}
			prompts[p] = row["pid"].(float64)
		}
	}
	if starts != 3 || len(prompts) != 4 || prompts["third"] != prompts["fourth"] || prompts["first"] == prompts["third"] {
		t.Fatalf("rich/native/prewarm starts=%d prompts=%#v rows=%#v", starts, prompts, rows)
	}
	after, _ := store.Resolve(context.Background(), threadstore.Query{Key: "resident"})
	if after.ID != before.ID || after.State.ResumeID != before.State.ResumeID {
		t.Fatal("compatible transport rebound active Thread")
	}
	run("spawn", WithSpawn())
	run("after-spawn")
	rows = read()
	ids := map[string]any{}
	for _, row := range rows {
		if row["kind"] == "prompt" {
			ids[fmt.Sprint(row["prompt"])] = row["pid"]
		}
	}
	if ids["spawn"] == ids["after-spawn"] {
		t.Fatal("WithSpawn registered a resident writer")
	}
	if _, err := a.Thread("resident").Run(t22Context(t), "append-changed", WithAppendSystemPrompt("startup beta")); err != nil {
		t.Fatal(err)
	}
	changed, _ := store.Resolve(context.Background(), threadstore.Query{Key: "resident"})
	if changed.ID == after.ID {
		t.Fatal("append compatibility change reused old active record")
	}
	seen := 0
	var lastArgs string
	for _, row := range read() {
		if row["kind"] == "start" {
			b, _ := json.Marshal(row["args"])
			lastArgs = string(b)
		}
		if row["kind"] == "prompt" && row["prompt"] == "append-changed" {
			seen++
			if row["pid"] == ids["after-spawn"] || !strings.Contains(lastArgs, "startup beta") || strings.Contains(lastArgs, "startup alpha") {
				t.Fatalf("append startup signature did not replace writer: %#v args=%s", row, lastArgs)
			}
		}
	}
	if seen != 1 {
		t.Fatalf("append change delivered prompt %d times", seen)
	}
	if err := a.Close(t22Context(t)); err != nil {
		t.Fatal(err)
	}
	connection, err := net.DialTimeout("tcp4", address, 100*time.Millisecond)
	if err == nil {
		connection.Close()
		t.Fatal("Close left a live writer")
	}
}

func TestAlignmentPolicyToolPrivateFailures(t *testing.T) {
	for _, mode := range []string{"panic", "invalid-output", "handler-error"} {
		t.Run(mode, func(t *testing.T) {
			def := tool.Define("private_failure", "private failure", func(context.Context, map[string]any) (string, error) {
				switch mode {
				case "panic":
					panic("SECRET_PANIC")
				case "handler-error":
					return "", errors.New("SECRET_HANDLER")
				}
				return "SECRET_OUTPUT", nil
			}, tool.OutputSchemaJSON([]byte(`{"enum":["allowed"]}`)))
			out, err := def.Invoke(context.Background(), []byte(`{}`))
			if err == nil || out != nil {
				t.Fatalf("private failure became output: %s %v", out, err)
			}
			if _, _, ok := tool.AsRejection(err); ok {
				t.Fatal("private output/handler failure promoted to model correction")
			}
			if mode == "panic" && strings.Contains(err.Error(), "SECRET_PANIC") {
				t.Fatal("panic body leaked")
			}
			if mode == "invalid-output" && !errors.Is(err, tool.ErrInvalidOutput) {
				t.Fatal("output validation misclassified")
			}
		})
	}
}

func TestAlignmentPolicyTinyBudgetAndStoppedController(t *testing.T) {
	for _, limit := range []time.Duration{0, time.Nanosecond, 100 * time.Millisecond} {
		t.Run(limit.String(), func(t *testing.T) {
			c := qa.NewClock()
			expired := &ActiveExecutionTimeoutError{Limit: limit}
			ctx, b := activebudget.New(context.Background(), limit, expired, c)
			defer b.Cancel(nil)
			if limit == 0 {
				c.Advance(time.Hour)
				if ctx.Err() != nil || b.FinishExecution() != nil {
					t.Fatal("zero budget constrained run")
				}
				return
			}
			c.Elapse(limit)
			if err := b.FinishExecution(); err != expired || b.SelectedCause() != expired {
				t.Fatalf("unscheduled exact boundary err=%v selected=%v", err, b.SelectedCause())
			}
			c.FireAllStale()
			if b.FinishExecution() != expired {
				t.Fatal("finish outcome changed")
			}
		})
	}
	c := qa.NewClock()
	expired := &ActiveExecutionTimeoutError{Limit: time.Second}
	ctx, b := activebudget.New(context.Background(), time.Second, expired, c)
	b.Stop()
	c.Advance(time.Hour)
	c.FireAllStale()
	if ctx.Err() != nil || b.FinishExecution() != nil || b.SelectedCause() != nil {
		t.Fatal("cleanup Stop invented later expiry")
	}
	b.Cancel(nil)
}

func TestMain(m *testing.M) {
	code := m.Run()
	if t22External.dir != "" {
		_ = os.RemoveAll(t22External.dir)
	}
	if t22Formal.path != "" {
		_ = os.RemoveAll(filepath.Dir(t22Formal.path))
	}
	os.Exit(code)
}
