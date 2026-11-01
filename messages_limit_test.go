package adaptor_test

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"

	adaptor "github.com/agent-dance/agent-adaptor/v2"
	"github.com/agent-dance/agent-adaptor/v2/driver"
	"github.com/agent-dance/agent-adaptor/v2/memory"
	"github.com/agent-dance/agent-adaptor/v2/threadstore"
)

func TestV2TurnLimitThresholds(t *testing.T) {
	for _, tc := range []struct{ max, threshold int }{{1, 0}, {2, 1}, {3, 2}, {5, 4}, {20, 16}} {
		t.Run(fmt.Sprint(tc.max), func(t *testing.T) {
			f := messageFake(driver.MessageCapability{Continue: driver.ContinueInRun})
			entered, release := make(chan int), make(chan struct{})
			calls := 0
			f.runFunc = func(_ context.Context, req driver.Request, sink driver.EventSink) (driver.Response, error) {
				calls++
				for turn := 1; turn <= tc.max; turn++ {
					entered <- turn
					<-release
					_ = sink.Emit(driver.RunEvent{Type: driver.RunEventTurnCompleted, Turn: &driver.TurnCompleted{Index: turn - 1, Text: fmt.Sprint(turn)}})
					items := req.Messages.Claim(driver.BoundaryTurnEnd)
					want := 1
					if tc.threshold > 0 && turn == tc.threshold {
						want++
						if len(items) != 2 || !strings.Contains(items[0].Text, "Host policy notice") {
							t.Errorf("turn %d wrap-up claim=%v", turn, items)
						}
					}
					if turn == tc.max {
						want = 0
						if got := req.Messages.Claim(driver.BoundaryTool); len(got) != 0 {
							t.Errorf("claim after limit=%v", got)
						}
					}
					if len(items) != want {
						t.Errorf("turn %d claim=%v; want %d items", turn, items, want)
					}
					for _, item := range items {
						req.Messages.Ack(item.ID, driver.MessageAck{Delivered: true, Mechanism: "fake.in_run"})
					}
				}
				return messageCheckpoint("done"), nil
			}
			st := adaptor.New(f, adaptor.WithPolicy(adaptor.Policy{Messages: adaptor.MessagePolicy{MaxTurns: tc.max}})).Send(context.Background(), "start")
			for turn := 1; turn <= tc.max; turn++ {
				if got := <-entered; got != turn {
					t.Fatalf("turn=%d, want %d", got, turn)
				}
				id := fmt.Sprintf("next-%d", turn)
				if err := st.Send(context.Background(), id, adaptor.MessageID(id), adaptor.When(adaptor.AfterTurn)); err != nil {
					t.Fatal(err)
				}
				release <- struct{}{}
			}
			events := assertDone(t, st)
			lastTurn, notices := 0, 0
			for _, event := range events {
				switch e := event.(type) {
				case adaptor.TurnEnd:
					lastTurn = e.Turn
				case adaptor.Notice:
					if e.Kind == "limit.approaching" {
						notices++
						if tc.threshold == 0 || lastTurn != tc.threshold {
							t.Errorf("notice after turn %d, want %d", lastTurn, tc.threshold)
						}
					}
				}
			}
			wantNotices := 1
			if tc.max == 1 {
				wantNotices = 0
			}
			res, err := st.Result()
			if err != nil || calls != 1 || res.Turns != tc.max || notices != wantNotices {
				t.Fatalf("calls=%d notices=%d result=%+v err=%v", calls, notices, res, err)
			}
			if pending := res.Undelivered(); len(pending) != 1 || pending[0].ID != fmt.Sprintf("next-%d", tc.max) || pending[0].Reason != adaptor.MessageLimitExceeded {
				t.Fatalf("pending at limit=%v", pending)
			}
		})
	}
}

func TestV2TurnLimitIdleDoesNotContinue(t *testing.T) {
	for _, mode := range []string{"resume", "in_run", "stop_hook"} {
		for _, limit := range []int{1, 2, 3, 5, 20} {
			threshold := min(limit-limit/5, limit-1)
			for _, finishAt := range []int{max(1, threshold), limit} {
				t.Run(fmt.Sprintf("%s/limit-%d/finish-%d", mode, limit, finishAt), func(t *testing.T) {
					cap := driver.MessageCapability{}
					if mode != "resume" {
						cap.Continue = driver.ContinueInRun
					}
					if mode == "stop_hook" {
						cap.Boundaries = []driver.Boundary{driver.BoundaryStop}
					}
					f := messageFake(cap)
					entered, release := make(chan int), make(chan struct{})
					calls, turns := 0, 0
					f.runFunc = func(_ context.Context, req driver.Request, sink driver.EventSink) (driver.Response, error) {
						calls++
						for index := 0; ; index++ {
							turns++
							if turns <= finishAt {
								entered <- turns
								<-release
							}
							if mode == "resume" {
								return messageCheckpoint(fmt.Sprint(turns)), nil
							}
							var items []driver.MessageItem
							if mode == "stop_hook" {
								items = req.Messages.Claim(driver.BoundaryStop)
							}
							_ = sink.Emit(driver.RunEvent{Type: driver.RunEventTurnCompleted, Turn: &driver.TurnCompleted{Index: index, Text: fmt.Sprint(turns)}})
							if mode == "in_run" {
								items = req.Messages.Claim(driver.BoundaryTurnEnd)
							}
							if len(items) == 0 {
								return messageCheckpoint(fmt.Sprint(turns)), nil
							}
							for _, item := range items {
								req.Messages.Ack(item.ID, driver.MessageAck{Delivered: true, Mechanism: "fake.next"})
							}
						}
					}
					st := adaptor.New(f, adaptor.WithPolicy(adaptor.Policy{Messages: adaptor.MessagePolicy{MaxTurns: limit}})).Send(context.Background(), "start")
					for turn := 1; turn <= finishAt; turn++ {
						<-entered
						if turn < finishAt {
							if err := st.Send(context.Background(), "next", adaptor.When(adaptor.AfterTurn)); err != nil {
								t.Fatal(err)
							}
						}
						release <- struct{}{}
					}
					events := assertDone(t, st)
					notices, lastTurn := 0, 0
					for _, event := range events {
						switch e := event.(type) {
						case adaptor.TurnEnd:
							lastTurn = e.Turn
						case adaptor.Notice:
							if e.Kind == "limit.approaching" {
								notices++
								if lastTurn != threshold {
									t.Errorf("idle notice after turn %d, want %d", lastTurn, threshold)
								}
							}
						}
					}
					wantNotices := 1
					if limit == 1 {
						wantNotices = 0
					}
					if notices != wantNotices {
						t.Errorf("idle notices=%d, want %d", notices, wantNotices)
					}
					res, err := st.Result()
					wantCalls := 1
					if mode == "resume" {
						wantCalls = finishAt
					}
					if err != nil || calls != wantCalls || turns != finishAt || res.Turns != finishAt || len(res.Undelivered()) != 0 {
						t.Fatalf("calls=%d turns=%d result=%+v undelivered=%v err=%v", calls, turns, res, res.Undelivered(), err)
					}
					if finishAt <= threshold || limit == 1 {
						for _, message := range res.Messages() {
							if message.From.Kind == adaptor.AuthorSystem {
								t.Errorf("unused wrap-up became a message: %+v", message)
							}
						}
					}
				})
			}
		}
	}
}

func TestV2TurnLimitStopHookBudget(t *testing.T) {
	for _, limit := range []int{1, 2, 3, 5, 20} {
		t.Run(fmt.Sprint(limit), func(t *testing.T) {
			f := messageFake(driver.MessageCapability{Continue: driver.ContinueInRun, Boundaries: []driver.Boundary{driver.BoundaryStop}})
			entered, release := make(chan struct{}), make(chan struct{})
			calls := 0
			f.runFunc = func(_ context.Context, req driver.Request, sink driver.EventSink) (driver.Response, error) {
				calls++
				for turn := 1; turn <= limit; turn++ {
					entered <- struct{}{}
					<-release
					// Match Claude: one Stop claim reserves the successor and
					// includes Soon and AfterTurn in admission order.
					items := req.Messages.Claim(driver.BoundaryStop)
					if turn == limit && len(items) != 0 {
						t.Errorf("Stop started a turn beyond the budget: %v", items)
					} else if turn < limit && len(items) < 2 {
						t.Errorf("Stop lost host input: %v", items)
					}
					if limit > 1 && turn == min(limit-limit/5, limit-1) {
						if len(items) != 3 || !strings.Contains(items[0].Text, "Host policy notice") || items[1].ID != fmt.Sprintf("turn-%d-timing-%d", turn, adaptor.Soon) || items[2].ID != fmt.Sprintf("turn-%d-timing-%d", turn, adaptor.AfterTurn) {
							t.Errorf("wrap-up must precede the Stop batch: %v", items)
						}
					}
					_ = sink.Emit(driver.RunEvent{Type: driver.RunEventTurnCompleted, Turn: &driver.TurnCompleted{Index: turn - 1, Text: fmt.Sprint(turn)}})
					for _, item := range items {
						req.Messages.Ack(item.ID, driver.MessageAck{Delivered: true, Mechanism: "fake.stop"})
					}
				}
				return messageCheckpoint("done"), nil
			}
			st := adaptor.New(f, adaptor.WithPolicy(adaptor.Policy{Messages: adaptor.MessagePolicy{MaxTurns: limit}})).Send(context.Background(), "start")
			for turn := 1; turn <= limit; turn++ {
				<-entered
				for _, timing := range []adaptor.Timing{adaptor.Soon, adaptor.AfterTurn} {
					id := fmt.Sprintf("turn-%d-timing-%d", turn, timing)
					if err := st.Send(context.Background(), id, adaptor.MessageID(id), adaptor.When(timing)); err != nil {
						t.Fatal(err)
					}
				}
				release <- struct{}{}
			}
			assertDone(t, st)
			res, err := st.Result()
			if err != nil || calls != 1 || res.Turns != limit || len(res.Undelivered()) != 2 {
				t.Fatalf("calls=%d result=%+v undelivered=%v err=%v", calls, res, res.Undelivered(), err)
			}
			for _, message := range res.Undelivered() {
				if message.Reason != adaptor.MessageLimitExceeded || message.From.Kind == adaptor.AuthorSystem {
					t.Errorf("pending message=%+v", message)
				}
			}
		})
	}
}

func TestV2WrapUpResumeDeliveryAndReceipts(t *testing.T) {
	store := memory.NewStore()
	f := messageFake(driver.MessageCapability{})
	entered, release := make(chan struct{}), make(chan struct{})
	var system adaptor.MessageContext
	format := func(m adaptor.MessageContext) string {
		if m.From.Kind == adaptor.AuthorSystem {
			system = m
		}
		return fmt.Sprintf("[%d:%s] %s", m.From.Kind, m.ID, m.Text)
	}
	calls := 0
	f.runFunc = func(_ context.Context, req driver.Request, _ driver.EventSink) (driver.Response, error) {
		calls++
		if calls == 1 {
			close(entered)
			<-release
		} else {
			want := fmt.Sprintf("[%d:%s] %s\n\n[%d:%s] host one\n\n[%d:host-two] host two", adaptor.AuthorSystem, system.ID, system.Text, adaptor.AuthorHuman, strings.TrimSuffix(system.ID, "-wrap"), adaptor.AuthorHuman)
			if req.Prompt != want || req.Session == nil || req.Session.Mode != driver.SessionContinueOnly {
				t.Errorf("continuation=%q session=%+v; want %q", req.Prompt, req.Session, want)
			}
		}
		return messageCheckpoint(fmt.Sprint(calls)), nil
	}
	a := adaptor.New(f, adaptor.WithThreadStore(store), adaptor.WithPolicy(adaptor.Policy{Messages: adaptor.MessagePolicy{MaxTurns: 2, Format: format}}))
	st := a.Thread("limit-receipts").Send(context.Background(), "start", adaptor.MessageID("start"))
	<-entered
	for _, host := range []struct{ id, text string }{{st.RunID() + "-limit", "host one"}, {"host-two", "host two"}} {
		if err := st.Send(context.Background(), host.text, adaptor.MessageID(host.id)); err != nil {
			t.Fatal(err)
		}
		if err := st.Send(context.Background(), "duplicate", adaptor.MessageID(host.id)); err != nil {
			t.Fatal(err)
		}
	}
	close(release)
	assertDone(t, st)
	res, err := st.Result()
	if err != nil || calls != 2 || res.Turns != 2 || len(res.Messages()) != 4 || len(res.Undelivered()) != 0 {
		t.Fatalf("calls=%d result=%+v messages=%v err=%v", calls, res, res.Messages(), err)
	}
	if system.ID != st.RunID()+"-limit-wrap" || system.From.Kind != adaptor.AuthorSystem {
		t.Fatalf("system context=%+v", system)
	}
	checkWrapUpOutcome(t, res, system.ID, "turn_end", "core.resume", 2)
	record, err := store.Resolve(context.Background(), threadstore.Query{Key: "limit-receipts"})
	if err != nil || record == nil {
		t.Fatalf("record=%+v err=%v", record, err)
	}
	var receipts []string
	for _, receipt := range record.DeliveredMessages {
		receipts = append(receipts, receipt.ID)
	}
	if !reflect.DeepEqual(receipts, []string{"start", system.ID, st.RunID() + "-limit", "host-two"}) {
		t.Fatalf("delivery receipt order=%v", receipts)
	}
	// A different Agent must dedupe the persisted policy ID just like host IDs.
	deduped := adaptor.New(f, adaptor.WithThreadStore(store)).Thread("limit-receipts").Send(context.Background(), "duplicate system input", adaptor.MessageID(system.ID))
	assertDone(t, deduped)
	if duplicate, err := deduped.Result(); err != nil || duplicate.Turns != 0 || calls != 2 {
		t.Fatalf("deduped result=%+v calls=%d err=%v", duplicate, calls, err)
	}
}

func TestV2WrapUpInRunClaims(t *testing.T) {
	for _, boundary := range []driver.Boundary{driver.BoundaryTurnEnd, driver.BoundaryStop, driver.BoundaryTool, driver.BoundaryImmediate} {
		for _, timing := range []adaptor.Timing{adaptor.Soon, adaptor.Interrupt, adaptor.AfterTurn} {
			if timing == adaptor.Interrupt && boundary != driver.BoundaryImmediate || timing == adaptor.AfterTurn && boundary != driver.BoundaryStop {
				continue
			}
			t.Run(fmt.Sprintf("%s/%d", boundary, timing), func(t *testing.T) {
				f := messageFake(driver.MessageCapability{Continue: driver.ContinueInRun, Boundaries: []driver.Boundary{driver.BoundaryStop, driver.BoundaryTool, driver.BoundaryImmediate}, Interrupt: true})
				entered, release := make(chan struct{}), make(chan struct{})
				calls := 0
				var systemID string
				f.runFunc = func(_ context.Context, req driver.Request, sink driver.EventSink) (driver.Response, error) {
					calls++
					emit := func(index int) {
						_ = sink.Emit(driver.RunEvent{Type: driver.RunEventTurnCompleted, Turn: &driver.TurnCompleted{Index: index, Text: fmt.Sprint(index)}})
					}
					successor := boundary == driver.BoundaryStop || timing == adaptor.Interrupt
					if !successor {
						emit(0)
					}
					if items := req.Messages.Claim(boundary); len(items) != 0 {
						t.Errorf("policy-only claim=%v", items)
					}
					close(entered)
					<-release
					items := req.Messages.Claim(boundary)
					if successor {
						emit(0)
					}
					if len(items) != 3 || items[1].ID != "one" || items[2].ID != "two" || !strings.Contains(items[0].Text, "Host policy notice") {
						t.Errorf("claim=%v", items)
					} else {
						systemID = items[0].ID
						if items[0].Timing != driver.Timing(timing) {
							t.Errorf("system timing=%v; want %v", items[0].Timing, timing)
						}
					}
					for _, item := range items {
						req.Messages.Ack(item.ID, driver.MessageAck{Delivered: true, Mechanism: "fake.boundary"})
					}
					emit(1)
					if items := req.Messages.Claim(driver.BoundaryTurnEnd); len(items) != 0 {
						t.Errorf("final claim=%v", items)
					}
					return messageCheckpoint("done"), nil
				}
				st := adaptor.New(f, adaptor.WithPolicy(adaptor.Policy{Messages: adaptor.MessagePolicy{MaxTurns: 2}})).Send(context.Background(), "start")
				<-entered
				for _, id := range []string{"one", "two"} {
					if err := st.Send(context.Background(), id, adaptor.MessageID(id), adaptor.When(timing)); err != nil {
						t.Fatal(err)
					}
				}
				close(release)
				assertDone(t, st)
				res, err := st.Result()
				if err != nil || calls != 1 || res.Turns != 2 || len(res.Messages()) != 4 || len(res.Undelivered()) != 0 {
					t.Fatalf("calls=%d result=%+v messages=%v err=%v", calls, res, res.Messages(), err)
				}
				wantBoundary := string(boundary)
				checkWrapUpOutcome(t, res, systemID, wantBoundary, "fake.boundary", 2)
			})
		}
	}
}

func checkWrapUpOutcome(t *testing.T, res *adaptor.Result, id, boundary, mechanism string, turn int) {
	t.Helper()
	count := 0
	for _, message := range res.Messages() {
		if message.From.Kind == adaptor.AuthorSystem {
			count++
			if message.ID != id || message.State != adaptor.MessageDelivered || message.Turn != turn || message.Boundary != boundary || message.Mechanism != mechanism || message.Reason != "" {
				t.Errorf("wrap-up outcome=%+v", message)
			}
		}
	}
	transcripts := 0
	for _, item := range res.Transcript() {
		if item.Message != nil && item.Message.ID == id {
			transcripts++
			if item.Message.From.Kind != adaptor.AuthorSystem || item.Message.Boundary != boundary || item.Message.Mechanism != mechanism {
				t.Errorf("wrap-up transcript=%+v", item.Message)
			}
		}
	}
	if count != 1 || transcripts != 1 {
		t.Errorf("wrap-up outcomes=%d transcripts=%d", count, transcripts)
	}
}
