package adaptertest

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/agent-dance/agent-adaptor/v2/driver"
)

func TestVerifyMessageCapability(t *testing.T) {
	full := driver.Descriptor{Sessions: driver.SessionCapability{SupportsResume: true}, Messages: referenceMessageCapability()}
	cases := []struct {
		name     string
		desc     driver.Descriptor
		resolved driver.MessageCapability
		clause   string
	}{
		{name: "zero"},
		{name: "full", desc: full, resolved: full.Messages},
		{name: "resume_downgrade", desc: full, resolved: driver.MessageCapability{MaxUnits: 128, Unit: driver.MessageUnitUTF16}},
		{name: "unknown_boundary", desc: driver.Descriptor{Messages: driver.MessageCapability{Boundaries: []driver.Boundary{"invented"}}}, clause: "MSG-01"},
		{name: "duplicate_boundary", desc: driver.Descriptor{Messages: driver.MessageCapability{Boundaries: []driver.Boundary{driver.BoundaryTool, driver.BoundaryTool}}}, clause: "MSG-01"},
		{name: "interrupt_without_immediate", desc: driver.Descriptor{Messages: driver.MessageCapability{Interrupt: true}}, clause: "MSG-01"},
		{name: "turn_end_without_continuation", desc: driver.Descriptor{Messages: driver.MessageCapability{Boundaries: []driver.Boundary{driver.BoundaryTurnEnd}}}, clause: "MSG-01"},
		{name: "invalid_continue", desc: driver.Descriptor{Messages: driver.MessageCapability{Continue: 42}}, clause: "MSG-01"},
		{name: "negative_limit", desc: driver.Descriptor{Messages: driver.MessageCapability{MaxUnits: -1}}, clause: "MSG-01"},
		{name: "invalid_unit", desc: driver.Descriptor{Messages: driver.MessageCapability{Unit: "runes"}}, clause: "MSG-01"},
		{name: "persist_without_resume", desc: driver.Descriptor{Messages: driver.MessageCapability{PersistsDelivered: true}}, clause: "MSG-01"},
		{name: "resolver_boundary", resolved: driver.MessageCapability{Boundaries: []driver.Boundary{driver.BoundaryTool}}, clause: "MSG-02"},
		{name: "resolver_interrupt", resolved: driver.MessageCapability{Boundaries: []driver.Boundary{driver.BoundaryImmediate}, Interrupt: true}, clause: "MSG-02"},
		{name: "resolver_continue", resolved: driver.MessageCapability{Continue: driver.ContinueInRun}, clause: "MSG-02"},
		{name: "resolver_confirmation", resolved: driver.MessageCapability{ConfirmedDelivery: true}, clause: "MSG-02"},
		{name: "resolver_persistence", resolved: driver.MessageCapability{PersistsDelivered: true}, clause: "MSG-02"},
		{name: "resolver_removes_limit", desc: full, clause: "MSG-02"},
		{name: "resolver_enlarges_limit", desc: full, resolved: driver.MessageCapability{MaxUnits: 129, Unit: driver.MessageUnitUTF16}, clause: "MSG-02"},
		{name: "resolver_changes_unit", desc: full, resolved: driver.MessageCapability{MaxUnits: 128, Unit: driver.MessageUnitBytes}, clause: "MSG-02"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			vs := VerifyMessageCapability(tc.desc, tc.resolved)
			if tc.clause == "" {
				if len(vs) != 0 {
					t.Fatal(vs)
				}
			} else if !clauseSet(vs)[tc.clause] {
				t.Fatalf("missing %s: %v", tc.clause, vs)
			}
		})
	}
}

func TestVerifyMessagePortViolations(t *testing.T) {
	caps := referenceMessageCapability()
	item := driver.MessageItem{ID: "a", Text: "[human a] original framed 😀 text"}
	window := MessageCall{Operation: "window", Open: true}
	claim := MessageCall{Operation: "claim", Boundary: driver.BoundaryTool, Items: []driver.MessageItem{item}}
	ack := MessageCall{Operation: "ack", ID: "a", Ack: driver.MessageAck{Delivered: true, Mechanism: "fixture.hook"}}
	closeWindow := MessageCall{Operation: "window"}
	end := MessageCall{Operation: "return"}
	evidence := MessageEvidence{Received: []string{item.Text}, Confirmed: []string{item.Text}}
	badAck := func(a driver.MessageAck) MessageCall { return MessageCall{Operation: "ack", ID: "a", Ack: a} }
	cases := []struct {
		name   string
		calls  []MessageCall
		e      MessageEvidence
		cap    driver.MessageCapability
		clause string
	}{
		{name: "clean", calls: []MessageCall{window, claim, ack, closeWindow, end}, e: evidence, cap: caps},
		{name: "initial_reset", calls: []MessageCall{closeWindow, window, claim, ack, closeWindow, end}, e: evidence, cap: caps},
		{name: "no_window", calls: []MessageCall{claim, ack, end}, e: evidence, cap: caps, clause: "MSG-03"},
		{name: "duplicate_open", calls: []MessageCall{window, window, closeWindow, end}, cap: caps, clause: "MSG-03"},
		{name: "duplicate_close", calls: []MessageCall{window, closeWindow, closeWindow, end}, cap: caps, clause: "MSG-03"},
		{name: "window_left_open", calls: []MessageCall{window, end}, cap: caps, clause: "MSG-03"},
		{name: "undeclared_claim", calls: []MessageCall{window, claim, ack, closeWindow, end}, e: evidence, clause: "MSG-02"},
		{name: "request_downgraded", calls: []MessageCall{window, claim, ack, closeWindow, end}, cap: driver.MessageCapability{Continue: driver.ContinueInRun}, e: evidence, clause: "MSG-02"},
		{name: "missing_ack", calls: []MessageCall{window, claim, closeWindow, end}, cap: caps, clause: "MSG-04"},
		{name: "duplicate_ack", calls: []MessageCall{window, claim, ack, ack, closeWindow, end}, cap: caps, e: evidence, clause: "MSG-04"},
		{name: "double_claim", calls: []MessageCall{window, claim, claim, ack, closeWindow, end}, cap: caps, e: evidence, clause: "MSG-04"},
		{name: "unknown_ack", calls: []MessageCall{ack, end}, cap: caps, e: evidence, clause: "MSG-04"},
		{name: "ack_after_return", calls: []MessageCall{window, claim, closeWindow, end, ack}, cap: caps, e: evidence, clause: "MSG-04"},
		{name: "late_empty_claim", calls: []MessageCall{end, {Operation: "claim", Boundary: driver.BoundaryTool}}, cap: caps, clause: "MSG-04"},
		{name: "missing_return_marker", calls: []MessageCall{window, claim, ack, closeWindow}, cap: caps, e: evidence, clause: "MSG-04"},
		{name: "no_mechanism", calls: []MessageCall{window, claim, badAck(driver.MessageAck{Delivered: true}), closeWindow, end}, cap: caps, e: evidence, clause: "MSG-05"},
		{name: "no_provider_receipt", calls: []MessageCall{window, claim, ack, closeWindow, end}, cap: caps, clause: "MSG-05"},
		{name: "truncated_input", calls: []MessageCall{window, claim, ack, closeWindow, end}, cap: caps, e: MessageEvidence{Received: []string{"original framed 😀 text"}}, clause: "MSG-05"},
		{name: "rejected_is_not_delivered", calls: []MessageCall{window, claim, ack, closeWindow, end}, cap: caps, e: MessageEvidence{Received: []string{item.Text}, Confirmed: []string{item.Text}, Rejected: []string{"a"}}, clause: "MSG-05"},
		{name: "unknown_is_not_delivered", calls: []MessageCall{window, claim, ack, closeWindow, end}, cap: caps, e: MessageEvidence{Received: []string{item.Text}, Confirmed: []string{item.Text}, Ambiguous: []string{"a"}}, clause: "MSG-05"},
		{name: "failed_without_error", calls: []MessageCall{window, claim, badAck(driver.MessageAck{}), closeWindow, end}, cap: caps, clause: "MSG-05"},
		{name: "ambiguous_retry", calls: []MessageCall{window, claim, badAck(driver.MessageAck{Err: errors.New("missing echo"), Requeue: true}), closeWindow, end}, cap: caps, e: evidence, clause: "MSG-05"},
		{name: "explicit_ambiguous_retry", calls: []MessageCall{window, claim, badAck(driver.MessageAck{Err: errors.New("timeout"), Requeue: true}), closeWindow, end}, cap: caps, e: MessageEvidence{Ambiguous: []string{"a"}}, clause: "MSG-05"},
		{name: "safe_rejection_retry", calls: []MessageCall{window, claim, badAck(driver.MessageAck{Err: errors.New("provider rejected"), Requeue: true}), closeWindow, end}, cap: caps, e: MessageEvidence{Received: []string{item.Text}, Rejected: []string{"a"}}},
		{name: "ambiguous_marked", calls: []MessageCall{window, claim, badAck(driver.MessageAck{Err: errors.New("timeout"), Requeue: true, MaybeDelivered: true}), closeWindow, end}, cap: caps, e: evidence},
		{name: "write_is_not_echo", calls: []MessageCall{window, claim, ack, closeWindow, end}, cap: caps, e: MessageEvidence{Received: []string{item.Text}}, clause: "MSG-06"},
		{name: "resume_claims_turn_end", calls: []MessageCall{{Operation: "claim", Boundary: driver.BoundaryTurnEnd}, end}, clause: "MSG-08"},
		{name: "inrun_claims_closed_turn_end", calls: []MessageCall{window, closeWindow, {Operation: "claim", Boundary: driver.BoundaryTurnEnd}, end}, cap: caps},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			vs := verifyMessageCalls(tc.cap, tc.calls, tc.e)
			if tc.clause == "" {
				if len(vs) != 0 {
					t.Fatal(vs)
				}
			} else if !clauseSet(vs)[tc.clause] {
				t.Fatalf("missing %s: %v", tc.clause, vs)
			}
		})
	}
}

func TestVerifyMessageTurns(t *testing.T) {
	now := time.Now()
	expected := []driver.TurnCompleted{{Text: "first", Usage: &driver.Usage{}, Interrupted: true}, {Text: "last"}}
	events := func() []driver.RunEvent {
		return []driver.RunEvent{{Type: driver.RunEventTurnCompleted, Turn: &driver.TurnCompleted{Text: "first", Usage: &driver.Usage{}, Interrupted: true, CompletedAt: now}}, {Type: driver.RunEventTurnCompleted, Turn: &driver.TurnCompleted{Index: 1, Text: "last", CompletedAt: now}}}
	}
	if vs := VerifyMessageTurns(events(), expected); len(vs) != 0 {
		t.Fatal(vs)
	}
	cases := []struct {
		name   string
		mutate func([]driver.RunEvent) []driver.RunEvent
	}{
		{"missing", func(es []driver.RunEvent) []driver.RunEvent { return es[:1] }},
		{"extra", func(es []driver.RunEvent) []driver.RunEvent { return append(es, es[1]) }},
		{"nil", func(es []driver.RunEvent) []driver.RunEvent { es[0].Turn = nil; return es }},
		{"index", func(es []driver.RunEvent) []driver.RunEvent { es[1].Turn.Index = 0; return es }},
		{"no_time", func(es []driver.RunEvent) []driver.RunEvent { es[0].Turn.CompletedAt = time.Time{}; return es }},
		{"lost_text", func(es []driver.RunEvent) []driver.RunEvent { es[0].Turn.Text = ""; return es }},
		{"unobserved_vs_zero", func(es []driver.RunEvent) []driver.RunEvent { es[0].Turn.Usage = nil; return es }},
		{"lost_usage", func(es []driver.RunEvent) []driver.RunEvent {
			es[0].Turn.Usage = &driver.Usage{InputTokens: 1}
			return es
		}},
		{"false_interruption", func(es []driver.RunEvent) []driver.RunEvent { es[0].Turn.Interrupted = false; return es }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if vs := VerifyMessageTurns(tc.mutate(events()), expected); !clauseSet(vs)["MSG-07"] {
				t.Fatal(vs)
			}
		})
	}
}

func TestScriptedMessagePortAdmissionAndCopies(t *testing.T) {
	for _, tc := range []struct {
		unit       driver.MessageUnit
		limit      int
		fits, over string
	}{{"", 4, "😀", "😀x"}, {driver.MessageUnitBytes, 4, "😀", "😀x"}, {driver.MessageUnitUTF16, 2, "😀", "😀x"}, {driver.MessageUnitUTF16, 3, "é😀", "é😀x"}} {
		t.Run(string(tc.unit)+tc.fits, func(t *testing.T) {
			p := NewScriptedMessagePort(driver.MessageCapability{MaxUnits: tc.limit, Unit: tc.unit})
			if err := p.Admit(driver.BoundaryTool, driver.MessageItem{ID: "fit", Text: tc.fits}); err != nil {
				t.Fatal(err)
			}
			for _, b := range []driver.Boundary{driver.BoundaryTool, driver.BoundaryTurnEnd} {
				if err := p.Admit(b, driver.MessageItem{ID: "over", Text: tc.over}); err == nil {
					t.Fatal("oversize admission succeeded")
				}
			}
			if len(p.Pending()) != 1 {
				t.Fatal("rejected admission changed queue")
			}
			if err := p.Admit(driver.BoundaryImmediate, driver.MessageItem{Timing: driver.TimingInterrupt}); err == nil {
				t.Fatal("unsupported interrupt admitted")
			}
			p.Finish()
			if err := p.Admit(driver.BoundaryTool, driver.MessageItem{}); err == nil {
				t.Fatal("admitted after Run")
			}
		})
	}
	cap := referenceMessageCapability()
	original := []driver.MessageItem{{ID: "one", Text: "verbatim"}}
	p := NewScriptedMessagePort(cap, MessageBatch{Boundary: driver.BoundaryImmediate, Items: original})
	original[0].ID = "mutated"
	cap.Boundaries[0] = "mutated"
	select {
	case <-p.Signal():
	default:
		t.Fatal("push notification missing")
	}
	pending := p.Pending()
	pending[0].Text = "mutated"
	p.Window(true)
	items := p.Claim(driver.BoundaryImmediate)
	if items[0].ID != "one" || items[0].Text != "verbatim" {
		t.Fatal(items)
	}
	items[0].Text = "mutated"
	calls := p.Calls()
	for i := range calls {
		if len(calls[i].Items) > 0 {
			calls[i].Items[0].Text = "mutated again"
		}
	}
	p.Ack("one", driver.MessageAck{Delivered: true, Mechanism: "fixture"})
	p.Window(false)
	p.Finish()
	if vs := p.Verify(MessageEvidence{Received: []string{"verbatim"}, Confirmed: []string{"verbatim"}}); len(vs) != 0 {
		t.Fatal(vs)
	}
	p.Window(true)
	if !clauseSet(p.Verify(MessageEvidence{}))["MSG-04"] {
		t.Fatal("post-return call not detected")
	}
}

func TestScriptedMessagePortConcurrentClaimAck(t *testing.T) {
	cap := referenceMessageCapability()
	p := NewScriptedMessagePort(cap, MessageBatch{Boundary: driver.BoundaryTool, Items: []driver.MessageItem{{ID: "one", Text: "first"}, {ID: "two", Text: "second"}}})
	start := make(chan struct{})
	var wg sync.WaitGroup
	p.Window(true)
	for range 8 {
		wg.Go(func() {
			<-start
			for _, item := range p.Claim(driver.BoundaryTool) {
				p.Ack(item.ID, driver.MessageAck{Delivered: true, Mechanism: "fixture"})
			}
			_ = p.Calls()
			_ = p.Pending()
		})
	}
	close(start)
	wg.Wait()
	p.Window(false)
	p.Finish()
	e := MessageEvidence{Received: []string{"first", "second"}, Confirmed: []string{"first", "second"}}
	if vs := p.Verify(e); len(vs) != 0 {
		t.Fatal(vs)
	}
	if len(p.Pending()) != 0 {
		t.Fatal("claim lost queued inputs")
	}
}

func TestScriptedMessagePortTimingAndCancellationOwnership(t *testing.T) {
	items := []driver.MessageItem{{ID: "soon", Text: "soon"}, {ID: "after", Text: "after", Timing: driver.TimingAfterTurn}, {ID: "interrupt", Text: "interrupt", Timing: driver.TimingInterrupt}}
	p := NewScriptedMessagePort(referenceMessageCapability(), MessageBatch{Boundary: driver.BoundaryImmediate, Items: items})
	ctx := &messageContext{Context: context.Background(), done: make(chan struct{})}
	p.afterClaim = func() { _ = p.Calls(); ctx.expire(context.Canceled) } // reentrant, outside mutex
	p.Window(true)
	got := p.Claim(driver.BoundaryImmediate)
	if !reflect.DeepEqual(got, []driver.MessageItem{items[0], items[2]}) {
		t.Fatal(got)
	}
	if ctx.Err() != context.Canceled {
		t.Fatal("claim did not trigger deterministic cancellation")
	}
	// Cancellation has not settled either owned item; only Driver Ack may do so.
	for _, c := range p.Calls() {
		if c.Operation == "ack" {
			t.Fatal("port stole cancellation ownership")
		}
	}
	for _, item := range got {
		p.Ack(item.ID, driver.MessageAck{Err: ctx.Err()})
	}
	if pending := p.Pending(); !reflect.DeepEqual(pending, []driver.MessageItem{items[1]}) {
		t.Fatal(pending)
	}
	p.Window(false)
	p.Finish()
	if vs := p.Verify(MessageEvidence{}); len(vs) != 0 {
		t.Fatal(vs)
	}
}

func TestScriptedMessagePortStopCombinedTurnEndClaim(t *testing.T) {
	items := []driver.MessageItem{
		{ID: "after", Text: "earlier turn-end guidance", Timing: driver.TimingAfterTurn},
		{ID: "soon", Text: "later immediate guidance", Timing: driver.TimingSoon},
	}
	p := NewScriptedMessagePort(referenceMessageCapability(), MessageBatch{Boundary: driver.BoundaryStop, Items: items})
	p.Window(true)
	if got := p.Claim(driver.BoundaryTool); len(got) != 0 {
		t.Fatalf("Stop batch became available before the hook: %+v", got)
	}
	got := p.Claim(driver.BoundaryTurnEnd)
	if !reflect.DeepEqual(got, items) {
		t.Fatalf("combined Stop claim lost admission order: got %+v, want %+v", got, items)
	}
	for _, item := range got {
		p.Ack(item.ID, driver.MessageAck{Delivered: true, Mechanism: "fixture.hook.stop"})
	}
	p.Window(false)
	p.Finish()
	evidence := MessageEvidence{Received: []string{items[0].Text, items[1].Text}, Confirmed: []string{items[0].Text, items[1].Text}}
	if vs := p.Verify(evidence); len(vs) != 0 {
		t.Fatal(vs)
	}
}

func TestVerifyMessageUsage(t *testing.T) {
	cases := []struct {
		name  string
		turns []driver.TurnCompleted
		usage *driver.Usage
		bad   bool
	}{
		{name: "unobserved"},
		{name: "observed_zero", turns: []driver.TurnCompleted{{Usage: &driver.Usage{}}}, usage: &driver.Usage{}},
		{name: "nil_is_not_zero", turns: []driver.TurnCompleted{{Usage: &driver.Usage{}}}, bad: true},
		{name: "zero_is_not_nil", usage: &driver.Usage{}, bad: true},
		{name: "all_metrics", turns: []driver.TurnCompleted{{Usage: &driver.Usage{InputTokens: 1, OutputTokens: 2, CachedInputTokens: 3, EstimatedCostMilli: 4}}, {}, {Usage: &driver.Usage{InputTokens: 4, OutputTokens: 3, CachedInputTokens: 2, EstimatedCostMilli: 1}}}, usage: &driver.Usage{InputTokens: 5, OutputTokens: 5, CachedInputTokens: 5, EstimatedCostMilli: 5}},
		{name: "double_counted", turns: []driver.TurnCompleted{{Usage: &driver.Usage{InputTokens: 2}}, {Usage: &driver.Usage{InputTokens: 3}}}, usage: &driver.Usage{InputTokens: 8}, bad: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			vs := VerifyMessageUsage(tc.turns, tc.usage)
			if (len(vs) > 0) != tc.bad {
				t.Fatal(vs)
			}
		})
	}
}
