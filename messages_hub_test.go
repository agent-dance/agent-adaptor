package adaptor

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/agent-dance/agent-adaptor/v2/driver"
	"github.com/agent-dance/agent-adaptor/v2/threadstore"
)

func testMessageHub(t *testing.T, p MessagePolicy) *messageHub {
	t.Helper()
	sink := newEventSink(eventSinkConfig{runID: "hub", buffer: 1})
	sink.broker.spool = true
	ctx, cancel := context.WithCancel(context.Background())
	h := newMessageHub(ctx, sink, effectiveMessagePolicy(&Policy{Messages: p}), driver.MessageCapability{Boundaries: []driver.Boundary{driver.BoundaryTool}}, true)
	t.Cleanup(func() { cancel(); h.cancelPending(); h.finish(MessageCancelled); sink.close() })
	return h
}
func TestV2HubPendingCapacityReleasedByClaim(t *testing.T) {
	h := testMessageHub(t, MessagePolicy{MaxPending: 1})
	if err := h.send(context.Background(), "first", MessageID("first")); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	admitted := make(chan error, 1)
	go func() { close(started); admitted <- h.send(context.Background(), "second", MessageID("second")) }()
	<-started
	select {
	case err := <-admitted:
		t.Fatalf("full queue admitted: %v", err)
	default:
	}
	first := h.Claim(driver.BoundaryTool)
	if len(first) != 1 {
		t.Fatal(first)
	}
	if err := <-admitted; err != nil {
		t.Fatal(err)
	}
	second := h.Claim(driver.BoundaryTool)
	if len(second) != 1 || second[0].ID != "second" {
		t.Fatal(second)
	}
	for _, m := range append(first, second...) {
		h.Ack(m.ID, driver.MessageAck{Delivered: true})
	}
}
func TestV2HubWithdrawalAndAckCancellationRace(t *testing.T) {
	for n := 0; n < 100; n++ {
		h := testMessageHub(t, MessagePolicy{})
		ctx, cancel := context.WithCancel(context.Background())
		if err := h.send(ctx, "text", MessageID("id")); err != nil {
			t.Fatal(err)
		}
		items := h.Claim(driver.BoundaryTool)
		if len(items) != 1 {
			t.Fatal(items)
		}
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); cancel(); h.cancelPending() }()
		go func() { defer wg.Done(); h.Ack("id", driver.MessageAck{Delivered: true}) }()
		wg.Wait()
		if got := h.outcomes(); got[0].State != MessageDelivered {
			t.Fatalf("claimed cancelled: %+v", got)
		}
	}
	h := testMessageHub(t, MessagePolicy{})
	ctx, cancel := context.WithCancel(context.Background())
	if err := h.send(ctx, "withdraw", MessageID("withdraw")); err != nil {
		t.Fatal(err)
	}
	cancel()
	if got := h.Claim(driver.BoundaryTool); len(got) != 0 {
		t.Fatal(got)
	}
	if got := h.outcomes(); got[0].State != MessageFailed || got[0].Reason != MessageCancelled {
		t.Fatal(got)
	}
}
func TestV2HubMaybeDuplicateRequeueAndAckExactlyOnce(t *testing.T) {
	h := testMessageHub(t, MessagePolicy{})
	if err := h.send(context.Background(), "text", MessageID("id")); err != nil {
		t.Fatal(err)
	}
	h.Claim(driver.BoundaryTool)
	h.Ack("id", driver.MessageAck{Requeue: true, MaybeDelivered: true, Err: context.DeadlineExceeded})
	items := h.Claim(driver.BoundaryTurnEnd)
	if len(items) != 1 || !items[0].MaybeDuplicate {
		t.Fatal(items)
	}
	h.Ack("id", driver.MessageAck{Delivered: true})
	h.Ack("id", driver.MessageAck{Err: errors.New("duplicate ack")})
	if got := h.outcomes(); got[0].State != MessageDelivered {
		t.Fatal(got)
	}
}
func TestV2HubReceiptsBoundedAndExpire(t *testing.T) {
	h := testMessageHub(t, MessagePolicy{})
	now := time.Now().UTC()
	receipts := []threadstore.DeliveredMessage{{ID: "expired", At: now.Add(-8 * 24 * time.Hour)}}
	for n := 0; n < 1005; n++ {
		receipts = append(receipts, threadstore.DeliveredMessage{ID: string(rune(n + 1)), At: now})
	}
	h.load(receipts)
	got := h.deliveryReceipts()
	if len(got) != 1000 || got[0].ID != receipts[6].ID {
		t.Fatalf("receipts=%d first=%q", len(got), got[0].ID)
	}
	h.mu.Lock()
	expired := h.delivered["expired"]
	h.mu.Unlock()
	if expired {
		t.Fatal("expired ID deduped")
	}
}

func TestV2HubInterruptCapabilityImpliesImmediateClaimAndResumeFallback(t *testing.T) {
	h := testMessageHub(t, MessagePolicy{})
	h.setCapability(driver.MessageCapability{Interrupt: true, Boundaries: []driver.Boundary{driver.BoundaryTool}})
	if err := h.send(context.Background(), "interrupt", MessageID("id"), When(Interrupt)); err != nil {
		t.Fatal(err)
	}
	if got := h.Claim(driver.BoundaryTool); len(got) != 0 {
		t.Fatal(got)
	}
	got := h.Claim(driver.BoundaryImmediate)
	if len(got) != 1 {
		t.Fatalf("immediate=%v", got)
	}
	h.Ack("id", driver.MessageAck{Requeue: true, MaybeDelivered: true, Err: context.DeadlineExceeded})
	got = h.Claim(driver.BoundaryTurnEnd)
	if len(got) != 1 || got[0].Timing != driver.TimingAfterTurn {
		t.Fatalf("resume fallback=%v", got)
	}
	h.Ack("id", driver.MessageAck{Delivered: true})
}

func TestV2HubWrapUpIDDoesNotCollideWithHostID(t *testing.T) {
	h := testMessageHub(t, MessagePolicy{MaxTurns: 5})
	if err := h.send(context.Background(), "host input", MessageID("hub-limit")); err != nil {
		t.Fatal(err)
	}
	for n := 0; n < 4; n++ {
		h.completeTurn(&Result{Text: "turn"}, nil)
	}
	outcomes := h.outcomes()
	if len(outcomes) != 2 || outcomes[0].ID == outcomes[1].ID {
		t.Fatalf("outcomes=%+v", outcomes)
	}
}
