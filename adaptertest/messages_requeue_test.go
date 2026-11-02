package adaptertest

import (
	"errors"
	"testing"

	"github.com/agent-dance/agent-adaptor/v2/driver"
)

func TestVerifyMessagePortAcceptsLegalRequeueAndRedelivery(t *testing.T) {
	caps := referenceMessageCapability()
	item := driver.MessageItem{ID: "same-input", Text: "[human same-input] keep the original goal"}
	p := NewScriptedMessagePort(caps,
		MessageBatch{Boundary: driver.BoundaryImmediate, Items: []driver.MessageItem{item}},
		MessageBatch{Boundary: driver.BoundaryTurnEnd, Items: []driver.MessageItem{item}},
	)
	p.Window(true)
	claimed := p.Claim(driver.BoundaryImmediate)
	if len(claimed) != 1 {
		t.Fatal(claimed)
	}
	p.Ack(item.ID, driver.MessageAck{Err: errors.New("provider explicitly rejected steer"), Requeue: true})
	p.Window(false)
	claimed = p.Claim(driver.BoundaryTurnEnd)
	if len(claimed) != 1 {
		t.Fatal(claimed)
	}
	p.Ack(item.ID, driver.MessageAck{Delivered: true, Mechanism: "provider.turn_start"})
	p.Finish()
	// The first attempt never reached the provider; the continuation did.
	// Each Claim has exactly one Ack, all before return.
	violations := p.Verify(MessageEvidence{Received: []string{item.Text}, Confirmed: []string{item.Text}})
	if len(violations) != 0 {
		t.Fatalf("valid rejection -> Requeue -> turn_end redelivery was rejected: %v", violations)
	}
}

func TestVerifyMessagePortRequeueAttempts(t *testing.T) {
	item := driver.MessageItem{ID: "same-input", Text: "[human same-input] original guidance"}
	caps := referenceMessageCapability()
	rejected := driver.MessageAck{Err: errors.New("provider rejected"), Requeue: true}
	ambiguous := driver.MessageAck{Err: errors.New("missing echo"), Requeue: true, MaybeDelivered: true}
	delivered := driver.MessageAck{Delivered: true, Mechanism: "provider.turn_start"}
	cases := []struct {
		name       string
		first      driver.MessageAck
		second     driver.MessageAck
		duplicate  bool
		boundary   driver.Boundary
		receipts   []string
		firstProof MessageAttemptEvidence
		lastProof  MessageAttemptEvidence
		clause     string
	}{
		{name: "explicit_rejection_then_delivery", first: rejected, second: delivered, boundary: driver.BoundaryTurnEnd,
			receipts: []string{item.Text, item.Text}, firstProof: MessageAttemptEvidence{Received: []string{item.Text}, Rejected: true}, lastProof: MessageAttemptEvidence{Received: []string{item.Text}, Confirmed: []string{item.Text}}},
		{name: "ambiguous_then_delivery", first: ambiguous, second: delivered, duplicate: true, boundary: driver.BoundaryTurnEnd,
			receipts: []string{item.Text, item.Text}, firstProof: MessageAttemptEvidence{Received: []string{item.Text}, Ambiguous: true}, lastProof: MessageAttemptEvidence{Received: []string{item.Text}, Confirmed: []string{item.Text}}},
		{name: "missing_duplicate_marker", first: ambiguous, second: delivered, boundary: driver.BoundaryTurnEnd,
			receipts: []string{item.Text, item.Text}, firstProof: MessageAttemptEvidence{Received: []string{item.Text}, Ambiguous: true}, lastProof: MessageAttemptEvidence{Received: []string{item.Text}, Confirmed: []string{item.Text}}, clause: "MSG-05"},
		{name: "missing_ambiguity_marker", first: rejected, second: delivered, boundary: driver.BoundaryTurnEnd,
			receipts: []string{item.Text, item.Text}, firstProof: MessageAttemptEvidence{Received: []string{item.Text}, Ambiguous: true}, lastProof: MessageAttemptEvidence{Received: []string{item.Text}, Confirmed: []string{item.Text}}, clause: "MSG-05"},
		{name: "later_success_cannot_supply_earlier_echo", first: delivered, second: delivered, boundary: driver.BoundaryTurnEnd,
			receipts: []string{item.Text, item.Text}, firstProof: MessageAttemptEvidence{Received: []string{item.Text}}, lastProof: MessageAttemptEvidence{Received: []string{item.Text}, Confirmed: []string{item.Text}}, clause: "MSG-06"},
		{name: "earlier_receipt_cannot_prove_redelivery", first: ambiguous, second: delivered, duplicate: true, boundary: driver.BoundaryTurnEnd,
			receipts: []string{item.Text}, firstProof: MessageAttemptEvidence{Received: []string{item.Text}, Ambiguous: true}, lastProof: MessageAttemptEvidence{Confirmed: []string{item.Text}}, clause: "MSG-05"},
		{name: "one_receipt_cannot_prove_two_attempts", first: ambiguous, second: delivered, duplicate: true, boundary: driver.BoundaryTurnEnd,
			receipts: []string{item.Text}, firstProof: MessageAttemptEvidence{Received: []string{item.Text}, Ambiguous: true}, lastProof: MessageAttemptEvidence{Received: []string{item.Text}, Confirmed: []string{item.Text}}, clause: "MSG-04"},
		{name: "failed_retry_still_requires_error", first: rejected, boundary: driver.BoundaryTurnEnd, clause: "MSG-05"},
		{name: "no_retry_after_delivery", first: delivered, second: delivered, boundary: driver.BoundaryTurnEnd,
			receipts: []string{item.Text, item.Text}, firstProof: MessageAttemptEvidence{Received: []string{item.Text}, Confirmed: []string{item.Text}}, lastProof: MessageAttemptEvidence{Received: []string{item.Text}, Confirmed: []string{item.Text}}, clause: "MSG-04"},
		{name: "no_retry_after_terminal_failure", first: driver.MessageAck{Err: errors.New("terminal failure")}, second: delivered, boundary: driver.BoundaryTurnEnd,
			receipts: []string{item.Text}, lastProof: MessageAttemptEvidence{Received: []string{item.Text}, Confirmed: []string{item.Text}}, clause: "MSG-04"},
		{name: "no_retry_at_failed_boundary", first: rejected, second: delivered, boundary: driver.BoundaryImmediate,
			receipts: []string{item.Text}, lastProof: MessageAttemptEvidence{Received: []string{item.Text}, Confirmed: []string{item.Text}}, clause: "MSG-05"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			retry := item
			retry.MaybeDuplicate = tc.duplicate
			p := NewScriptedMessagePort(caps,
				MessageBatch{Boundary: driver.BoundaryImmediate, Items: []driver.MessageItem{item}},
				MessageBatch{Boundary: tc.boundary, Items: []driver.MessageItem{retry}},
			)
			p.Window(true)
			if len(p.Claim(driver.BoundaryImmediate)) != 1 {
				t.Fatal("missing first claim")
			}
			p.Ack(item.ID, tc.first)
			if len(p.Claim(tc.boundary)) != 1 {
				t.Fatal("missing retry claim")
			}
			p.Ack(item.ID, tc.second)
			p.Window(false)
			p.Finish()
			first, last := tc.firstProof, tc.lastProof
			first.ID, first.Attempt = item.ID, 1
			last.ID, last.Attempt = item.ID, 2
			e := MessageEvidence{Received: tc.receipts, Confirmed: append(append([]string(nil), first.Confirmed...), last.Confirmed...), Attempts: []MessageAttemptEvidence{first, last}}
			if first.Rejected || last.Rejected {
				e.Rejected = []string{item.ID}
			}
			if first.Ambiguous || last.Ambiguous {
				e.Ambiguous = []string{item.ID}
			}
			violations := p.Verify(e)
			if tc.clause == "" && len(violations) != 0 || tc.clause != "" && !clauseSet(violations)[tc.clause] {
				t.Fatalf("want clause %q, got %v", tc.clause, violations)
			}
		})
	}
}

func TestVerifyMessagePortRequiresAckForEveryAttempt(t *testing.T) {
	item := driver.MessageItem{ID: "retry", Text: "retry guidance"}
	for _, missing := range []int{1, 2} {
		t.Run(string(rune('0'+missing)), func(t *testing.T) {
			p := NewScriptedMessagePort(referenceMessageCapability(),
				MessageBatch{Boundary: driver.BoundaryImmediate, Items: []driver.MessageItem{item}},
				MessageBatch{Boundary: driver.BoundaryTurnEnd, Items: []driver.MessageItem{item}},
			)
			p.Window(true)
			p.Claim(driver.BoundaryImmediate)
			if missing != 1 {
				p.Ack(item.ID, driver.MessageAck{Err: errors.New("rejected"), Requeue: true})
			}
			p.Claim(driver.BoundaryTurnEnd)
			if missing != 2 {
				p.Ack(item.ID, driver.MessageAck{Err: errors.New("failed")})
			}
			p.Window(false)
			p.Finish()
			if vs := p.Verify(MessageEvidence{}); !clauseSet(vs)["MSG-04"] {
				t.Fatalf("missing Ack for attempt %d was accepted: %v", missing, vs)
			}
		})
	}
}
