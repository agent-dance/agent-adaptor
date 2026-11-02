package appserver

import (
	"context"
	"reflect"
	"testing"
	"testing/synctest"
	"time"

	adaptor "github.com/agent-dance/agent-adaptor/v2"
	"github.com/agent-dance/agent-adaptor/v2/driver"
)

type interruptContinuationDriver struct {
	ready, release chan struct{}
	providerTurns  int
	rpc            *messageTestRPC
}

func (*interruptContinuationDriver) Descriptor() driver.Descriptor {
	return driver.Descriptor{Type: "appserver-interrupt-core-fixture", Messages: driver.MessageCapability{
		Boundaries: []driver.Boundary{driver.BoundaryImmediate}, Interrupt: true, Continue: driver.ContinueInRun,
	}}
}
func (*interruptContinuationDriver) ValidateConfig(any) error { return nil }

func (d *interruptContinuationDriver) Run(ctx context.Context, req driver.Request, sink driver.EventSink) (driver.Response, error) {
	state := newRunState(req.RunID, sink)
	state.ctx, state.messages = ctx, req.Messages
	state.setThread("thread")
	rpc := &messageTestRPC{state: state, disconnected: make(chan struct{})}
	d.rpc = rpc
	rpc.start = func(n int) {
		d.providerTurns++
		if n == 1 {
			close(d.ready)
			<-d.release
		} else {
			rpc.complete(n, "completed")
		}
	}
	rpc.interrupt = func(context.Context, TurnInterruptParams) error {
		rpc.complete(1, "interrupted")
		return nil
	}
	err := runTurns(ctx, rpc, state, Options{Prompt: req.Prompt, Messages: req.Messages}, "thread")
	return state.snapshot(Options{}, "thread", "", "", 0, "", false), err
}

func TestInterruptRespectsCoreTurnLimit(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		d := &interruptContinuationDriver{ready: make(chan struct{}), release: make(chan struct{})}
		st := adaptor.New(d, adaptor.WithPolicy(adaptor.Policy{Messages: adaptor.MessagePolicy{MaxTurns: 1}})).Send(ctx, "initial")
		<-d.ready
		if err := st.Send(ctx, "interrupt", adaptor.MessageID("restart"), adaptor.When(adaptor.Interrupt)); err != nil {
			t.Fatal(err)
		}
		close(d.release)
		synctest.Wait()
		if d.providerTurns == 1 {
			d.rpc.complete(1, "completed")
		}
		result, err := st.Result()
		if err != nil {
			t.Fatal(err)
		}
		if d.providerTurns > 1 {
			t.Fatalf("MaxTurns=1, but interrupt started %d provider turns; Result.Turns=%d, messages=%+v, undelivered=%+v", d.providerTurns, result.Turns, result.Messages(), result.Undelivered())
		}
		if result.Turns != 1 {
			t.Fatalf("Turns=%d", result.Turns)
		}
		pending := result.Undelivered()
		if len(pending) != 1 || pending[0].ID != "restart" || pending[0].Text != "interrupt" || pending[0].Reason != adaptor.MessageLimitExceeded {
			t.Fatalf("undelivered=%+v", pending)
		}

	})
}

func TestInterruptAttributesBatchToSuccessor(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d := &interruptContinuationDriver{ready: make(chan struct{}), release: make(chan struct{})}
	st := adaptor.New(d, adaptor.WithPolicy(adaptor.Policy{Messages: adaptor.MessagePolicy{MaxTurns: 2}})).Send(ctx, "initial", adaptor.MessageID("initial"))
	<-d.ready
	for _, input := range []struct {
		id     string
		timing adaptor.Timing
	}{{"soon", adaptor.Soon}, {"restart", adaptor.Interrupt}} {
		if err := st.Send(ctx, input.id, adaptor.MessageID(input.id), adaptor.When(input.timing)); err != nil {
			t.Fatal(err)
		}
	}
	close(d.release)
	turns := map[int][]string{}
	for event := range st.Events() {
		if turn, ok := event.(adaptor.TurnEnd); ok {
			turns[turn.Turn] = turn.MessageIDs
		}
	}
	result, err := st.Result()
	if err != nil {
		t.Fatal(err)
	}
	if d.providerTurns != 2 || result.Turns != 2 || len(result.Undelivered()) != 0 {
		t.Fatalf("turns=%d result=%+v", d.providerTurns, result)
	}
	// The policy wrap-up is also delivered in the successor.
	if !reflect.DeepEqual(turns[1], []string{"initial"}) || len(turns[2]) != 3 || turns[2][0] != "soon" || turns[2][1] != "restart" {
		t.Fatalf("turn message IDs=%v", turns)
	}
	for _, outcome := range result.Messages() {
		if outcome.ID != "initial" && outcome.Turn != 2 {
			t.Fatalf("receipt=%+v", outcome)
		}
	}
}
