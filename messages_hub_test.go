package adaptor

import (
	"context"
	"errors"
	"fmt"
	"strings"
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
	h.load([]threadstore.DeliveredMessage{{ID: "hub-limit-wrap", At: time.Now().UTC()}})
	if err := h.send(context.Background(), "host input", MessageID("hub-limit")); err != nil {
		t.Fatal(err)
	}
	for n := 0; n < 4; n++ {
		h.completeTurn(&Result{Text: "turn"}, nil)
	}
	if outcomes := h.outcomes(); len(outcomes) != 1 {
		t.Fatalf("unclaimed policy input became a message: %+v", outcomes)
	}
	items := h.Claim(driver.BoundaryTurnEnd)
	if len(items) != 2 || items[0].ID != "hub-limit-wrap-wrap" || items[1].ID != "hub-limit" {
		t.Fatalf("claim=%+v", items)
	}
	for _, item := range items {
		h.Ack(item.ID, driver.MessageAck{Delivered: true, Mechanism: "fake.next"})
	}
	outcomes := h.outcomes()
	if len(outcomes) != 2 || outcomes[0].ID == outcomes[1].ID {
		t.Fatalf("outcomes=%+v", outcomes)
	}
}

func TestV2HubWrapUpRetryRequiresHostInput(t *testing.T) {
	for _, retryHost := range []bool{false, true} {
		t.Run(fmt.Sprint(retryHost), func(t *testing.T) {
			h := testMessageHub(t, MessagePolicy{MaxTurns: 3, MaxPending: 1})
			for n := 0; n < 2; n++ {
				h.completeTurn(&Result{}, nil)
			}
			if err := h.send(context.Background(), "host", MessageID("host")); err != nil {
				t.Fatal(err)
			}
			items := h.Claim(driver.BoundaryTool)
			if len(items) != 2 || items[1].ID != "host" {
				t.Fatalf("claim=%v", items)
			}
			h.Ack(items[0].ID, driver.MessageAck{Requeue: true, MaybeDelivered: true, Err: context.DeadlineExceeded})
			h.Ack("host", driver.MessageAck{Delivered: !retryHost, Requeue: retryHost, Err: context.DeadlineExceeded})
			if retryHost {
				if h.closeIfIdle() {
					t.Fatal("host retry was ignored")
				}
				retry := h.Claim(driver.BoundaryTurnEnd)
				if len(retry) != 2 || retry[0].ID != items[0].ID || !retry[0].MaybeDuplicate || !strings.Contains(retry[0].Text, "ignore the duplicate") || retry[1].ID != "host" {
					t.Fatalf("retry=%v", retry)
				}
				for _, item := range retry {
					h.Ack(item.ID, driver.MessageAck{Delivered: true, Mechanism: "fake.resume"})
					h.Ack(item.ID, driver.MessageAck{Delivered: true, Mechanism: "duplicate"})
				}
			} else if items := h.Claim(driver.BoundaryTurnEnd); len(items) != 0 {
				t.Fatalf("policy retry claimed without host input: %v", items)
			}
			if !h.closeIfIdle() {
				t.Fatal("policy retry prevented execution from finishing")
			}
			h.finish(MessageRunFailed)
			want := 1
			if retryHost {
				want = 2
			}
			if outcomes, receipts := h.outcomes(), h.deliveryReceipts(); len(outcomes) != want || len(receipts) != want {
				t.Fatalf("outcomes=%v receipts=%v", outcomes, receipts)
			}
		})
	}
}
