package adaptor_test

import (
	"context"
	"errors"
	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/memory"
	"reflect"
	"testing"
	"time"
)

func TestAlignmentActiveBudgetInitialContract(t *testing.T) {
	if _, ok := reflect.TypeFor[adaptor.Policy]().FieldByName("ActiveExecutionTimeout"); !ok {
		t.Fatal("Policy has no independent active execution budget")
	}
}
func TestAlignmentActiveBudgetParentDeadline(t *testing.T) {
	d := newFakeDriver()
	d.runFunc = func(ctx context.Context, req driver.Request, sink driver.EventSink) (driver.Response, error) {
		_, err := sink.(driver.DecisionCapableSink).RequestDecision(ctx, driver.DecisionRequest{Kind: driver.HumanDecisionPermission})
		return alignmentPartialResponse(), err
	}
	a := adaptor.New(d, adaptor.OnApproval(func(ctx context.Context, req *adaptor.ApprovalRequest) error { <-ctx.Done(); return ctx.Err() }))
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	res, err := a.Run(ctx, "work")
	alignmentRequirePartial(t, res, err, adaptor.ReasonDeadlineExceeded, context.DeadlineExceeded)
}

func TestAlignmentActiveBudgetPolicyAndParents(t *testing.T) {
	t.Run("negative before resources", func(t *testing.T) {
		d := newFakeDriver()
		a := adaptor.New(d)
		_, err := a.Run(context.Background(), "work", adaptor.WithPolicy(adaptor.Policy{ActiveExecutionTimeout: -time.Nanosecond}))
		var invalid *adaptor.InvalidPolicyError
		if !errors.Is(err, adaptor.ErrInvalidPolicy) || !errors.As(err, &invalid) || invalid.Field != "Policy.ActiveExecutionTimeout" || d.runCount() != 0 {
			t.Fatal(err)
		}
	})
	for _, kind := range []string{"close", "cancel", "deadline", "timeout"} {
		t.Run(kind, func(t *testing.T) {
			d := newFakeDriver()
			entered := make(chan struct{})
			d.runFunc = func(ctx context.Context, _ driver.Request, sink driver.EventSink) (driver.Response, error) {
				_, err := sink.(driver.DecisionCapableSink).RequestDecision(ctx, driver.DecisionRequest{Kind: driver.HumanDecisionPermission})
				return alignmentPartialResponse(), err
			}
			a := adaptor.New(d, adaptor.WithPolicy(adaptor.Policy{ActiveExecutionTimeout: time.Second}), adaptor.OnApproval(func(ctx context.Context, r *adaptor.ApprovalRequest) error {
				close(entered)
				<-ctx.Done()
				return ctx.Err()
			}))
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if kind == "deadline" {
				var dc context.CancelFunc
				ctx, dc = context.WithTimeout(ctx, 30*time.Millisecond)
				defer dc()
			}
			var opts []adaptor.CallOption
			if kind == "timeout" {
				opts = append(opts, adaptor.WithTimeout(30*time.Millisecond))
			}
			st := a.Stream(ctx, "wait", opts...)
			<-entered
			switch kind {
			case "close":
				if err := a.Close(context.Background()); err != nil {
					t.Fatal(err)
				}
			case "cancel":
				cancel()
			}
			for range st.Events() {
			}
			res, err := st.Result()
			reason := adaptor.ReasonCancelled
			cause := context.Canceled
			if kind == "deadline" || kind == "timeout" {
				reason = adaptor.ReasonDeadlineExceeded
				cause = context.DeadlineExceeded
			}
			alignmentRequirePartial(t, res, err, reason, cause)
			if kind == "close" {
				if _, err := a.Run(context.Background(), "again"); !errors.Is(err, adaptor.ErrAgentClosed) {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestAlignmentActiveBudgetLeaseRenewalDuringAsk(t *testing.T) {
	shortLeases(t, time.Second, 10*time.Millisecond)
	store := &alignmentFaultStore{Store: memory.NewStore(), kind: "renew", cause: errors.New("lease backend failure"), entered: make(chan struct{})}
	fake := newSessionFake("lease")
	ctx := context.Background()
	a := adaptor.New(fake, adaptor.WithThreadStore(store))
	if _, err := a.Thread("key").Run(ctx, "seed"); err != nil {
		t.Fatal(err)
	}
	before := *activeRecord(t, store, "key")
	store.active.Store(true)
	fake.runFunc = func(ctx context.Context, _ driver.Request, sink driver.EventSink) (driver.Response, error) {
		_, err := sink.(driver.DecisionCapableSink).RequestDecision(ctx, driver.DecisionRequest{Kind: driver.HumanDecisionPermission})
		return alignmentPartialResponse(), err
	}
	res, err := a.Thread("key").Run(ctx, "wait", adaptor.WithPolicy(adaptor.Policy{ActiveExecutionTimeout: time.Hour}), adaptor.OnApproval(func(ctx context.Context, r *adaptor.ApprovalRequest) error {
		close(store.entered)
		<-ctx.Done()
		return ctx.Err()
	}))
	alignmentRequirePartial(t, res, err, adaptor.ReasonInfrastructure, store.cause)
	if !reflect.DeepEqual(before, *activeRecord(t, store, "key")) {
		t.Fatal("lease failure changed healthy checkpoint")
	}
}
