package adaptor

import (
	"context"
	"reflect"
	"testing"

	"github.com/agent-dance/agent-adaptor/v2/driver"
)

func TestV2HubStopReservesOrderedSuccessorIncludingRequeue(t *testing.T) {
	h := testMessageHub(t, MessagePolicy{MaxTurns: 5})
	h.setCapability(driver.MessageCapability{Boundaries: []driver.Boundary{driver.BoundaryTool, driver.BoundaryStop}, Continue: driver.ContinueInRun})
	for _, input := range []struct {
		id     string
		timing Timing
	}{{"after", AfterTurn}, {"soon", Soon}} {
		if err := h.send(context.Background(), input.id, MessageID(input.id), When(input.timing)); err != nil {
			t.Fatal(err)
		}
	}
	tool := h.Claim(driver.BoundaryTool)
	if len(tool) != 1 || tool[0].ID != "soon" {
		t.Fatalf("tool=%+v", tool)
	}
	h.Ack("soon", driver.MessageAck{Err: context.DeadlineExceeded, Requeue: true})
	if err := h.send(context.Background(), "last", MessageID("last")); err != nil {
		t.Fatal(err)
	}
	batch := h.Claim(driver.BoundaryStop)
	var ids []string
	for _, item := range batch {
		ids = append(ids, item.ID)
		h.Ack(item.ID, driver.MessageAck{Delivered: true, Mechanism: "test.stop"})
	}
	if !reflect.DeepEqual(ids, []string{"after", "soon", "last"}) {
		t.Fatalf("stop order=%v", ids)
	}
	for _, outcome := range h.outcomes() {
		if outcome.Turn != 2 || outcome.State != MessageDelivered || outcome.Boundary != "stop" {
			t.Fatalf("outcome=%+v", outcome)
		}
	}
	h.completeTurn(&Result{Text: "stopped"}, nil)
	h.completeTurn(&Result{Text: "successor"}, nil)
}

func TestV2HubImmediateReservesOnlySelectedInterruptBatch(t *testing.T) {
	h := testMessageHub(t, MessagePolicy{MaxTurns: 1, Format: func(m MessageContext) string { return m.Text }})
	h.setCapability(driver.MessageCapability{Boundaries: []driver.Boundary{driver.BoundaryImmediate}, Interrupt: true, Continue: driver.ContinueInRun, MaxUnits: 3})
	if err := h.send(context.Background(), "one", MessageID("soon")); err != nil {
		t.Fatal(err)
	}
	if err := h.send(context.Background(), "two", MessageID("interrupt"), When(Interrupt)); err != nil {
		t.Fatal(err)
	}
	batch := h.Claim(driver.BoundaryImmediate)
	if len(batch) != 1 || batch[0].ID != "soon" {
		t.Fatalf("steer=%+v", batch)
	}
	h.Ack("soon", driver.MessageAck{Delivered: true, Mechanism: "test.steer"})
	if batch := h.Claim(driver.BoundaryImmediate); len(batch) != 0 {
		t.Fatalf("over-budget restart=%+v", batch)
	}
	pending := h.byID["interrupt"]
	if pending.claimed || pending.out.Turn != 0 || pending.out.State != MessageAccepted {
		t.Fatalf("reservation mutated refused input: %+v", pending)
	}
	h.completeTurn(&Result{Text: "done"}, nil)
	h.finish(MessageLimitExceeded)
	outcomes := h.outcomes()
	if outcomes[0].Turn != 1 || outcomes[1].Reason != MessageLimitExceeded || outcomes[1].Text != "two" {
		t.Fatalf("outcomes=%+v", outcomes)
	}
}

func TestV2HubRefusedSuccessorDoesNotResignalFullBatch(t *testing.T) {
	h := testMessageHub(t, MessagePolicy{MaxTurns: 1, Format: func(m MessageContext) string { return m.Text }})
	h.setCapability(driver.MessageCapability{Boundaries: []driver.Boundary{driver.BoundaryImmediate}, Interrupt: true, Continue: driver.ContinueInRun, MaxUnits: 3})
	for _, input := range []struct {
		id     string
		timing Timing
	}{{"one", Interrupt}, {"two", Soon}} {
		if err := h.send(context.Background(), input.id, MessageID(input.id), When(input.timing)); err != nil {
			t.Fatal(err)
		}
	}
	<-h.Signal()
	if batch := h.Claim(driver.BoundaryImmediate); len(batch) != 0 {
		t.Fatalf("over-budget restart=%+v", batch)
	}
	select {
	case <-h.Signal():
		t.Fatal("refused size-bounded successor caused an immediate retry loop")
	default:
	}
	for _, item := range h.items {
		if item.claimed || item.out.Turn != 0 || item.out.State != MessageAccepted {
			t.Fatalf("refused input=%+v", item)
		}
	}
}
