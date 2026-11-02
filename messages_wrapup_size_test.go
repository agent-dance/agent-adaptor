package adaptor

import (
	"context"
	"strings"
	"testing"
	"unicode/utf16"

	"github.com/agent-dance/agent-adaptor/v2/driver"
)

func TestV2WrapUpClaimHonorsFramedSizeLimit(t *testing.T) {
	h := testMessageHub(t, MessagePolicy{MaxTurns: 2})
	h.setCapability(driver.MessageCapability{Boundaries: []driver.Boundary{driver.BoundaryTool}, MaxUnits: 256, Unit: driver.MessageUnitBytes})
	h.completeTurn(&Result{Text: "first turn"}, nil)
	if err := h.send(context.Background(), "hi", MessageID("host")); err != nil {
		t.Fatalf("short host message should fit: %v", err)
	}
	items := h.Claim(driver.BoundaryTool)
	if len(items) == 0 {
		t.Fatal("host input was not claimed")
	}
	for _, item := range items {
		// A conforming Driver accepts each MessageItem only up to MaxUnits.
		if len(item.Text) > 256 {
			t.Errorf("core handed Driver oversized message %q: %d bytes exceeds MaxUnits=256", item.ID, len(item.Text))
		}
		h.Ack(item.ID, driver.MessageAck{Delivered: true, Mechanism: "review.tool"})
	}
	if outcomes := h.outcomes(); len(outcomes) != 1 || outcomes[0].ID != "host" || outcomes[0].State != MessageDelivered {
		t.Fatalf("skipped wrap-up fabricated an outcome: %+v", outcomes)
	}
	h.finish(MessageRunFailed)
	h.sink.close()
	var warned bool
	for event := range h.sink.broker.subscribe() {
		if notice, ok := event.(Notice); ok && notice.Kind == "limit.approaching" {
			warned = true
		}
	}
	if !warned {
		t.Fatal("oversized wrap-up suppressed limit.approaching Notice")
	}
}

func TestV2WrapUpClaimsRespectBatchBudget(t *testing.T) {
	for _, unit := range []driver.MessageUnit{"", driver.MessageUnitBytes, driver.MessageUnitUTF16} {
		t.Run(string(unit), func(t *testing.T) {
			h := testMessageHub(t, MessagePolicy{MaxTurns: 2, Format: func(m MessageContext) string { return m.Text }})
			h.setCapability(driver.MessageCapability{Boundaries: []driver.Boundary{driver.BoundaryTool}, MaxUnits: 6, Unit: unit})
			h.completeTurn(&Result{}, nil)
			text := "abc"
			if unit == driver.MessageUnitUTF16 {
				text = "😀a" // Three UTF-16 units, five UTF-8 bytes.
			}
			for _, id := range []string{"one", "two", "three"} {
				if err := h.send(context.Background(), text, MessageID(id)); err != nil {
					t.Fatal(err)
				}
			}
			for _, id := range []string{"one", "two", "three"} {
				// Push transports consume Signal before claiming. A bounded
				// batch must keep the remaining input discoverable.
				select {
				case <-h.Signal():
				default:
					t.Fatalf("pending batch %s was not signalled", id)
				}
				items := h.Claim(driver.BoundaryTool)
				if len(items) != 1 || items[0].ID != id {
					t.Fatalf("batch must retain remaining input in order: %+v", items)
				}
				var texts []string
				for _, item := range items {
					texts = append(texts, item.Text)
					h.Ack(item.ID, driver.MessageAck{Delivered: true, Mechanism: "test.tool"})
				}
				batch := strings.Join(texts, "\n\n")
				units := len(batch)
				if unit == driver.MessageUnitUTF16 {
					units = len(utf16.Encode([]rune(batch)))
				}
				if units > 6 {
					t.Fatalf("batch has %d %s, limit 6", units, unit)
				}
			}
			if items := h.Claim(driver.BoundaryTool); len(items) != 0 {
				t.Fatalf("policy-only batch=%+v", items)
			}
			if outcomes, receipts := h.outcomes(), h.deliveryReceipts(); len(outcomes) != 3 || len(receipts) != 3 {
				t.Fatalf("batch accounting: outcomes=%+v receipts=%+v", outcomes, receipts)
			}
		})
	}
}

func TestV2WrapUpDeferredUntilCombinedClaimFits(t *testing.T) {
	for _, unit := range []driver.MessageUnit{driver.MessageUnitBytes, driver.MessageUnitUTF16} {
		t.Run(string(unit), func(t *testing.T) {
			policyText := "policy"
			if unit == driver.MessageUnitUTF16 {
				policyText = "😀😀😀" // Six UTF-16 units, twelve UTF-8 bytes.
			}
			h := testMessageHub(t, MessagePolicy{MaxTurns: 2, Format: func(m MessageContext) string {
				if m.From.Kind == AuthorSystem {
					return policyText
				}
				return m.Text
			}})
			h.setCapability(driver.MessageCapability{Boundaries: []driver.Boundary{driver.BoundaryTool}, MaxUnits: 10, Unit: unit})
			h.completeTurn(&Result{}, nil)
			if err := h.send(context.Background(), "large", MessageID("large")); err != nil {
				t.Fatal(err)
			}
			items := h.Claim(driver.BoundaryTool)
			if len(items) != 1 || items[0].ID != "large" {
				t.Fatalf("wrap-up exceeds combined budget: %+v", items)
			}
			h.Ack("large", driver.MessageAck{Delivered: true, Mechanism: "test.tool"})
			if outcomes := h.outcomes(); len(outcomes) != 1 {
				t.Fatalf("unattempted wrap-up outcome=%+v", outcomes)
			}
			if err := h.send(context.Background(), "x", MessageID("small")); err != nil {
				t.Fatal(err)
			}
			items = h.Claim(driver.BoundaryTool)
			if len(items) != 2 || items[0].Text != policyText || items[1].ID != "small" {
				t.Fatalf("later claim should carry deferred wrap-up: %+v", items)
			}
			for _, item := range items {
				h.Ack(item.ID, driver.MessageAck{Delivered: true, Mechanism: "test.tool"})
			}
			if outcomes, receipts := h.outcomes(), h.deliveryReceipts(); len(outcomes) != 3 || len(receipts) != 3 {
				t.Fatalf("deferred delivery: outcomes=%+v receipts=%+v", outcomes, receipts)
			}
		})
	}
}

func TestV2WrapUpRequeueCannotExceedFramedLimit(t *testing.T) {
	h := testMessageHub(t, MessagePolicy{MaxTurns: 2, Format: func(m MessageContext) string {
		if m.From.Kind == AuthorSystem {
			return "policy"
		}
		return m.Text
	}})
	h.setCapability(driver.MessageCapability{Boundaries: []driver.Boundary{driver.BoundaryTool}, MaxUnits: 10})
	h.completeTurn(&Result{}, nil)
	if err := h.send(context.Background(), "x", MessageID("host")); err != nil {
		t.Fatal(err)
	}
	items := h.Claim(driver.BoundaryTool)
	if len(items) != 2 {
		t.Fatalf("initial claim=%+v", items)
	}
	h.Ack(items[0].ID, driver.MessageAck{Requeue: true, MaybeDelivered: true, Err: context.DeadlineExceeded})
	h.Ack("host", driver.MessageAck{Delivered: true, Mechanism: "test.tool"})
	if err := h.send(context.Background(), "y", MessageID("next")); err != nil {
		t.Fatal(err)
	}
	retry := h.Claim(driver.BoundaryTurnEnd)
	if len(retry) != 1 || retry[0].ID != "next" {
		t.Fatalf("duplicate warning made wrap-up exceed its limit: %+v", retry)
	}
	h.Ack("next", driver.MessageAck{Delivered: true, Mechanism: "test.resume"})
	if outcomes, receipts := h.outcomes(), h.deliveryReceipts(); len(outcomes) != 2 || len(receipts) != 2 {
		t.Fatalf("unsuccessful wrap-up became host work: outcomes=%+v receipts=%+v", outcomes, receipts)
	}
}
