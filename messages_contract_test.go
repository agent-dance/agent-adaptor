package adaptor_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"

	adaptor "github.com/agent-dance/agent-adaptor/v2"
	"github.com/agent-dance/agent-adaptor/v2/driver"
	"github.com/agent-dance/agent-adaptor/v2/memory"
)

func messageCheckpoint(text string) driver.Response {
	return driver.Response{Output: text, Checkpoint: &driver.Checkpoint{Valid: true, State: &driver.SessionState{ResumeID: "v2-session"}}}
}
func messageFake(cap driver.MessageCapability) *fakeDriver {
	f := newFakeDriver()
	d := f.Descriptor()
	d.Messages = cap
	f.descriptor = &d
	return f
}
func fullEvents(s adaptor.Stream) []adaptor.Event {
	var out []adaptor.Event
	for ev := range s.Events() {
		out = append(out, ev)
	}
	return out
}
func assertDone(t *testing.T, s adaptor.Stream) []adaptor.Event {
	t.Helper()
	res, err := s.Result()
	events := fullEvents(s)
	if len(events) == 0 {
		t.Fatal("missing Done")
	}
	done, ok := events[len(events)-1].(adaptor.Done)
	if !ok || done.Result != res || done.Err != err {
		t.Fatalf("last=%#v Result=%p err=%v", events[len(events)-1], res, err)
	}
	for n, e := range events {
		if e.Meta().RunID != s.RunID() || (n > 0 && e.Meta().Sequence <= events[n-1].Meta().Sequence) {
			t.Fatalf("event order: %+v", events)
		}
	}
	again, againErr := s.Result()
	if again != res || againErr != err {
		t.Fatal("Result changed")
	}
	return events
}
func TestV2DoneAndResultEquivalence(t *testing.T) {
	for _, kind := range []string{"success", "business", "infrastructure"} {
		t.Run(kind, func(t *testing.T) {
			f := newFakeDriver()
			f.response = driver.Response{Output: "answer", Summary: "label", Usage: &driver.Usage{InputTokens: 5}, RawStreams: &driver.RawStreams{Stdout: "out", Stderr: "err"}, Transcript: []driver.TranscriptItem{{Kind: driver.TranscriptAssistant, Text: "answer"}}}
			if kind == "business" {
				f.response.Failure = &driver.RunFailure{Code: driver.FailureAgentError, Message: "failed"}
			}
			if kind == "infrastructure" {
				f.err = errors.New("broken pipe")
			}
			s := adaptor.New(f, adaptor.WithEventBuffer(1), adaptor.WithBlockingEvents()).Send(context.Background(), "raw", adaptor.MessageID("initial"))
			assertDone(t, s)
		})
	}
}
func TestV2ContinueResume(t *testing.T) {
	for _, thread := range []bool{false, true} {
		t.Run(fmt.Sprint(thread), func(t *testing.T) {
			f := messageFake(driver.MessageCapability{})
			entered := make(chan struct{})
			release := make(chan struct{})
			calls := 0
			f.runFunc = func(_ context.Context, req driver.Request, _ driver.EventSink) (driver.Response, error) {
				calls++
				if calls == 1 {
					if req.Prompt != "original" {
						t.Errorf("start prompt=%q", req.Prompt)
					}
					close(entered)
					<-release
				} else {
					if req.Session == nil || req.Session.Mode != driver.SessionContinueOnly || req.Session.State.ResumeID != "v2-session" {
						t.Errorf("session=%+v", req.Session)
					}
					if !strings.Contains(req.Prompt, "additional") || !strings.Contains(req.Prompt, "original objective") {
						t.Errorf("unframed continuation=%q", req.Prompt)
					}
					if !thread && !req.Spawn {
						t.Error("anonymous continuation must spawn")
					}
				}
				r := messageCheckpoint(fmt.Sprintf("turn-%d", calls))
				r.Usage = &driver.Usage{InputTokens: calls, OutputTokens: 2}
				r.RawStreams = &driver.RawStreams{Stdout: fmt.Sprint(calls)}
				r.Transcript = []driver.TranscriptItem{{Kind: driver.TranscriptAssistant, Text: r.Output}}
				return r, nil
			}
			a := adaptor.New(f)
			var s adaptor.Stream
			if thread {
				s = a.Thread("thread").Send(context.Background(), "original", adaptor.MessageID("start"))
			} else {
				s = a.Send(context.Background(), "original", adaptor.MessageID("start"))
			}
			<-entered
			if err := s.Send(context.Background(), "additional", adaptor.MessageID("extra")); err != nil {
				t.Fatal(err)
			}
			close(release)
			events := assertDone(t, s)
			res, err := s.Result()
			if err != nil || res.Text != "turn-2" || res.Turns != 2 || res.Usage.InputTokens != 3 || res.Usage.OutputTokens != 4 || res.Raw().Stdout != "12" {
				t.Fatalf("result=%+v err=%v", res, err)
			}
			ends := 0
			delivered := false
			for _, ev := range events {
				switch e := ev.(type) {
				case adaptor.TurnEnd:
					ends++
					if e.Turn == 2 && !reflect.DeepEqual(e.MessageIDs, []string{"extra"}) {
						t.Errorf("turn ids=%v", e.MessageIDs)
					}
				case adaptor.MessageUpdate:
					if e.ID == "extra" && e.State == adaptor.MessageDelivered {
						delivered = e.Mechanism == "core.resume" && e.Boundary == "turn_end" && e.Turn == 2
					}
				}
			}
			if ends != 2 || !delivered {
				t.Fatalf("turns=%d delivered=%v events=%#v", ends, delivered, events)
			}
			items := res.Transcript()
			if len(items) != 3 || items[2].Message == nil || items[2].Message.Text != "additional" {
				t.Fatalf("transcript=%+v", items)
			}
		})
	}
}
func TestV2ContinueInRunAndFinalClaimRace(t *testing.T) {
	for _, late := range []bool{false, true} {
		t.Run(fmt.Sprint(late), func(t *testing.T) {
			f := messageFake(driver.MessageCapability{Continue: driver.ContinueInRun})
			entered := make(chan struct{})
			release := make(chan struct{})
			calls := 0
			f.runFunc = func(_ context.Context, r driver.Request, s driver.EventSink) (driver.Response, error) {
				calls++
				if calls == 1 {
					_ = s.Emit(driver.RunEvent{Type: driver.RunEventTurnCompleted, Turn: &driver.TurnCompleted{Index: 0, Text: "one", Usage: &driver.Usage{InputTokens: 1}}})
					if late {
						if got := r.Messages.Claim(driver.BoundaryTurnEnd); len(got) != 0 {
							t.Error("expected final empty claim")
						}
					}
					close(entered)
					<-release
					if !late {
						items := r.Messages.Claim(driver.BoundaryTurnEnd)
						if len(items) != 1 {
							t.Fatalf("claim=%v", items)
						}
						for _, m := range items {
							r.Messages.Ack(m.ID, driver.MessageAck{Delivered: true, Mechanism: "fake.in_run"})
						}
						_ = s.Emit(driver.RunEvent{Type: driver.RunEventTurnCompleted, Turn: &driver.TurnCompleted{Index: 1, Text: "two", Usage: &driver.Usage{InputTokens: 2}}})
					}
				}
				text := "two"
				if late && calls == 1 {
					text = "one"
				}
				return messageCheckpoint(text), nil
			}
			st := adaptor.New(f).Send(context.Background(), "start")
			<-entered
			if err := st.Send(context.Background(), "next"); err != nil {
				t.Fatal(err)
			}
			close(release)
			assertDone(t, st)
			res, err := st.Result()
			if err != nil || res.Turns != 2 || res.Text != "two" {
				t.Fatalf("res=%+v err=%v", res, err)
			}
			if (!late && calls != 1) || (late && calls != 2) {
				t.Fatalf("calls=%d late=%v", calls, late)
			}
			if !late && res.Usage.InputTokens != 3 {
				t.Fatalf("usage=%+v", res.Usage)
			}
		})
	}
}
func TestV2TimingAndBoundaryDelivery(t *testing.T) {
	f := messageFake(driver.MessageCapability{Boundaries: []driver.Boundary{driver.BoundaryTool, driver.BoundaryImmediate}, Interrupt: true, Continue: driver.ContinueInRun})
	entered := make(chan struct{})
	release := make(chan struct{})
	f.runFunc = func(_ context.Context, r driver.Request, s driver.EventSink) (driver.Response, error) {
		close(entered)
		<-release
		tool := r.Messages.Claim(driver.BoundaryTool)
		if len(tool) != 1 || tool[0].ID != "soon" {
			t.Fatalf("tool=%v", tool)
		}
		immediate := r.Messages.Claim(driver.BoundaryImmediate)
		if len(immediate) != 1 || immediate[0].ID != "interrupt" {
			t.Fatalf("immediate=%v", immediate)
		}
		for _, m := range append(tool, immediate...) {
			r.Messages.Ack(m.ID, driver.MessageAck{Delivered: true, Mechanism: "fake.boundary"})
		}
		_ = s.Emit(driver.RunEvent{Type: driver.RunEventTurnCompleted, Turn: &driver.TurnCompleted{Text: "first"}})
		end := r.Messages.Claim(driver.BoundaryTurnEnd)
		if len(end) != 1 || end[0].ID != "after" {
			t.Fatalf("end=%v", end)
		}
		r.Messages.Ack(end[0].ID, driver.MessageAck{Delivered: true, Mechanism: "fake.next"})
		_ = s.Emit(driver.RunEvent{Type: driver.RunEventTurnCompleted, Turn: &driver.TurnCompleted{Index: 1, Text: "last"}})
		return messageCheckpoint("last"), nil
	}
	st := adaptor.New(f).Send(context.Background(), "start")
	<-entered
	for n, timing := range []adaptor.Timing{adaptor.Soon, adaptor.Interrupt, adaptor.AfterTurn} {
		id := []string{"soon", "interrupt", "after"}[n]
		if err := st.Send(context.Background(), id, adaptor.MessageID(id), adaptor.When(timing)); err != nil {
			t.Fatal(err)
		}
	}
	close(release)
	events := assertDone(t, st)
	boundaries := map[string]string{}
	for _, ev := range events {
		if m, ok := ev.(adaptor.MessageUpdate); ok && m.State == adaptor.MessageDelivered && m.Mechanism != "core.prompt" {
			boundaries[m.ID] = m.Boundary
		}
	}
	if !reflect.DeepEqual(boundaries, map[string]string{"soon": "tool", "interrupt": "immediate", "after": "turn_end"}) {
		t.Fatalf("boundaries=%v", boundaries)
	}
}
func TestV2AdmissionErrorsAndSize(t *testing.T) {
	for _, tc := range []struct {
		name   string
		cap    driver.MessageCapability
		resume bool
		policy adaptor.MessagePolicy
		text   string
		timing adaptor.Timing
	}{
		{name: "interrupt", resume: true, timing: adaptor.Interrupt},
		{name: "no resume", resume: false},
		{name: "disabled", resume: true, policy: adaptor.MessagePolicy{Disabled: true}},
		{name: "bytes", resume: true, cap: driver.MessageCapability{MaxUnits: 3}, text: "😀"},
		{name: "utf16", resume: true, cap: driver.MessageCapability{MaxUnits: 1, Unit: driver.MessageUnitUTF16}, text: "😀"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := messageFake(tc.cap)
			f.descriptor.Sessions.SupportsResume = tc.resume
			entered := make(chan struct{})
			release := make(chan struct{})
			f.runFunc = func(context.Context, driver.Request, driver.EventSink) (driver.Response, error) {
				close(entered)
				<-release
				return messageCheckpoint("ok"), nil
			}
			p := tc.policy
			p.Format = func(m adaptor.MessageContext) string { return m.Text }
			st := adaptor.New(f, adaptor.WithPolicy(adaptor.Policy{Messages: p})).Send(context.Background(), "start")
			<-entered
			err := st.Send(context.Background(), tc.text, adaptor.MessageID("rejected"), adaptor.When(tc.timing))
			var me *adaptor.MessageError
			if !errors.As(err, &me) || me.Text != tc.text {
				t.Fatalf("err=%v", err)
			}
			if tc.name == "bytes" || tc.name == "utf16" {
				if me.Reason != adaptor.MessageLimitExceeded {
					t.Fatal(me)
				}
			} else if !errors.Is(err, adaptor.ErrMessageUnsupported) {
				t.Fatal(err)
			}
			close(release)
			assertDone(t, st)
			err = st.Send(context.Background(), "late", adaptor.MessageID("late"))
			if !errors.Is(err, adaptor.ErrRunFinished) || !errors.As(err, &me) || me.Text != "late" {
				t.Fatalf("late error=%v", err)
			}
		})
	}
	if _, ok := adaptor.WithModel("model").(adaptor.MessageOption); ok {
		t.Fatal("execution option implements MessageOption")
	}
}
func TestV2FramingAndCustomFormat(t *testing.T) {
	for _, kind := range []adaptor.AuthorKind{adaptor.AuthorHuman, adaptor.AuthorAgent, adaptor.AuthorSystem} {
		t.Run(fmt.Sprint(kind), func(t *testing.T) {
			f := messageFake(driver.MessageCapability{Boundaries: []driver.Boundary{driver.BoundaryTool}})
			entered := make(chan struct{})
			release := make(chan struct{})
			f.runFunc = func(_ context.Context, r driver.Request, _ driver.EventSink) (driver.Response, error) {
				if r.Prompt != "RAW" {
					t.Errorf("prompt=%q", r.Prompt)
				}
				close(entered)
				<-release
				items := r.Messages.Claim(driver.BoundaryTool)
				for _, m := range items {
					want := map[adaptor.AuthorKind]string{adaptor.AuthorHuman: "Additional human guidance", adaptor.AuthorAgent: "Peer agent input", adaptor.AuthorSystem: "Host policy notice"}[kind]
					if !strings.Contains(m.Text, want) || !strings.Contains(m.Text, "no separate") && !strings.Contains(m.Text, "do not send a separate") {
						t.Errorf("frame=%q", m.Text)
					}
					r.Messages.Ack(m.ID, driver.MessageAck{Delivered: true, Mechanism: "fake.tool"})
				}
				return messageCheckpoint("ok"), nil
			}
			st := adaptor.New(f).Send(context.Background(), "RAW", adaptor.From(adaptor.Author{Kind: kind}))
			<-entered
			if err := st.Send(context.Background(), "extra", adaptor.From(adaptor.Author{Kind: kind, Name: "author"})); err != nil {
				t.Fatal(err)
			}
			close(release)
			assertDone(t, st)
		})
	}
}
func TestV2MaxTurnsAndPendingLimit(t *testing.T) {
	f := messageFake(driver.MessageCapability{})
	entered := make(chan struct{})
	release := make(chan struct{})
	calls := 0
	f.runFunc = func(context.Context, driver.Request, driver.EventSink) (driver.Response, error) {
		calls++
		if calls == 1 {
			close(entered)
			<-release
		}
		return messageCheckpoint(fmt.Sprint(calls)), nil
	}
	st := adaptor.New(f, adaptor.WithPolicy(adaptor.Policy{Messages: adaptor.MessagePolicy{MaxTurns: 1, MaxPending: 1}})).Send(context.Background(), "start")
	<-entered
	if err := st.Send(context.Background(), "one", adaptor.MessageID("one")); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() { close(started); done <- st.Send(ctx, "two") }()
	<-started
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	close(release)
	assertDone(t, st)
	res, err := st.Result()
	if err != nil || res.Turns != 1 || len(res.Undelivered()) != 1 || res.Undelivered()[0].Reason != adaptor.MessageLimitExceeded {
		t.Fatalf("res=%+v undelivered=%v err=%v", res, res.Undelivered(), err)
	}
}
func TestV2DedupeAcrossAgentsAndExecutions(t *testing.T) {
	store := memory.NewStore()
	f := newSessionFake("dedupe")
	a := adaptor.New(f.fakeDriver, adaptor.WithThreadStore(store))
	if _, err := a.Thread("key").Send(context.Background(), "one", adaptor.MessageID("id")).Result(); err != nil {
		t.Fatal(err)
	}
	b := adaptor.New(f.fakeDriver, adaptor.WithThreadStore(store))
	for _, agent := range []*adaptor.Agent{a, b} {
		s := agent.Thread("key").Send(context.Background(), "duplicate", adaptor.MessageID("id"))
		assertDone(t, s)
		r, err := s.Result()
		if err != nil || r.Turns != 0 {
			t.Fatalf("res=%+v err=%v", r, err)
		}
	}
	f.mu.Lock()
	calls := len(f.requests)
	f.mu.Unlock()
	if calls != 1 {
		t.Fatalf("duplicate called Driver.Run %d times", calls)
	}
}
func TestV2ApprovalFallbackAndForwarding(t *testing.T) {
	f := newFakeDriver()
	f.runFunc = func(ctx context.Context, _ driver.Request, s driver.EventSink) (driver.Response, error) {
		_, err := s.(driver.DecisionCapableSink).RequestDecision(ctx, driver.DecisionRequest{Kind: driver.HumanDecisionPermission, Prompt: "allow?"})
		return driver.Response{Output: "partial"}, err
	}
	st := adaptor.New(f).Send(context.Background(), "start")
	events := assertDone(t, st)
	var re *adaptor.RunError
	_, err := st.Result()
	if !errors.As(err, &re) || re.Reason != adaptor.ReasonApprovalTimeout {
		t.Fatalf("error=%v", err)
	}
	found := false
	for _, ev := range events {
		if n, ok := ev.(adaptor.Notice); ok && strings.Contains(n.Text, "never called") {
			found = true
		}
	}
	if !found {
		t.Fatal("missing fallback notice")
	}
	f = newFakeDriver()
	f.runFunc = func(ctx context.Context, _ driver.Request, s driver.EventSink) (driver.Response, error) {
		_, err := s.(driver.DecisionCapableSink).RequestDecision(ctx, driver.DecisionRequest{Kind: driver.HumanDecisionPermission})
		return driver.Response{Output: "ok"}, err
	}
	var original, forwarded *adaptor.ApprovalRequest
	st = adaptor.New(f, adaptor.OnApproval(func(_ context.Context, r *adaptor.ApprovalRequest) error {
		original = r
		forwarded = r.Forward(adaptor.Origin{Member: "worker", Task: "task"})
		return forwarded.Approve(context.Background())
	})).Send(context.Background(), "start")
	assertDone(t, st)
	if original.Origin() != (adaptor.Origin{}) || forwarded.Origin().Member != "worker" {
		t.Fatal("origin")
	}
	if err := original.Approve(context.Background()); err == nil {
		t.Fatal("forwarded responder resolved twice")
	}
}
func TestV2AttachmentPreambleSubagentsAndUsage(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	provider := &fakeProvider{log: &callLog{}, attachment: adaptor.RunAttachment{Preamble: "roster", Results: func() []adaptor.SubagentResult {
		mu.Lock()
		defer mu.Unlock()
		calls++
		return []adaptor.SubagentResult{{Member: "worker", Task: "task", State: "succeeded", Usage: &adaptor.Usage{InputTokens: 5}, Unread: []string{"peer"}}}
	}}}
	f := newSessionFake("preamble")
	f.runFunc = func(_ context.Context, r driver.Request, _ driver.EventSink) (driver.Response, error) {
		if !strings.HasPrefix(r.Prompt, "roster\n\n") {
			t.Errorf("preamble=%q", r.Prompt)
		}
		res := messageCheckpoint("ok")
		res.Usage = &driver.Usage{InputTokens: 2}
		return res, nil
	}
	a := adaptor.New(f.fakeDriver, adaptor.WithRunServices(provider))
	s := a.Thread("key").Send(context.Background(), "raw")
	assertDone(t, s)
	res, err := s.Result()
	if err != nil || res.Usage.InputTokens != 7 || len(res.Subagents()) != 1 {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	provider.attachment.Preamble = "roster changed"
	f.runFunc = func(_ context.Context, r driver.Request, _ driver.EventSink) (driver.Response, error) {
		if r.Session.State == nil {
			t.Error("preamble changed fingerprint")
		}
		return messageCheckpoint("ok"), nil
	}
	s = a.Thread("key", adaptor.ResumeOnly()).Send(context.Background(), "again")
	assertDone(t, s)
	if _, err := s.Result(); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 2 {
		t.Fatalf("Results callbacks=%d", calls)
	}
	subs := res.Subagents()
	subs[0].Usage.InputTokens = 99
	subs[0].Unread[0] = "changed"
	if res.Subagents()[0].Usage.InputTokens != 5 || res.Subagents()[0].Unread[0] != "peer" {
		t.Fatal("Subagents aliases result")
	}
}

func TestV2SenderCancellationAndClaimOwnership(t *testing.T) {
	for _, claimed := range []bool{false, true} {
		t.Run(fmt.Sprint(claimed), func(t *testing.T) {
			f := messageFake(driver.MessageCapability{Boundaries: []driver.Boundary{driver.BoundaryTool}})
			entered := make(chan struct{})
			claimNow := make(chan struct{})
			claimDone := make(chan struct{})
			release := make(chan struct{})
			f.runFunc = func(_ context.Context, r driver.Request, _ driver.EventSink) (driver.Response, error) {
				close(entered)
				<-claimNow
				var items []driver.MessageItem
				if claimed {
					items = r.Messages.Claim(driver.BoundaryTool)
				}
				close(claimDone)
				<-release
				if claimed {
					if len(items) != 1 {
						t.Fatalf("claim=%v", items)
					}
					r.Messages.Ack(items[0].ID, driver.MessageAck{Delivered: true, Mechanism: "fake.tool"})
				} else if items = r.Messages.Claim(driver.BoundaryTool); len(items) != 0 {
					t.Errorf("withdrawn claimed=%v", items)
				}
				return messageCheckpoint("ok"), nil
			}
			st := adaptor.New(f).Send(context.Background(), "start")
			<-entered
			ctx, cancel := context.WithCancel(context.Background())
			if err := st.Send(ctx, "extra", adaptor.MessageID("extra")); err != nil {
				t.Fatal(err)
			}
			close(claimNow)
			<-claimDone
			cancel()
			if claimed {
				st.Cancel()
			}
			close(release)
			assertDone(t, st)
			res, err := st.Result()
			if claimed {
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("err=%v", err)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				m := res.Undelivered()
				if len(m) != 1 || m[0].ID != "extra" || m[0].Reason != adaptor.MessageCancelled {
					t.Fatalf("undelivered=%+v", m)
				}
			}

		})
	}
}
func TestV2CancelPreservesClaimedAckAndFailsPending(t *testing.T) {
	f := messageFake(driver.MessageCapability{Boundaries: []driver.Boundary{driver.BoundaryTool}})
	entered := make(chan struct{})
	take := make(chan struct{})
	taken := make(chan struct{})
	ack := make(chan struct{})
	f.runFunc = func(ctx context.Context, r driver.Request, _ driver.EventSink) (driver.Response, error) {
		close(entered)
		<-take
		items := r.Messages.Claim(driver.BoundaryTool)
		close(taken)
		<-ack
		for _, m := range items {
			r.Messages.Ack(m.ID, driver.MessageAck{Delivered: true, Mechanism: "fake.write"})
		}
		return driver.Response{}, ctx.Err()
	}
	st := adaptor.New(f).Send(context.Background(), "start")
	<-entered
	if err := st.Send(context.Background(), "claimed", adaptor.MessageID("claimed")); err != nil {
		t.Fatal(err)
	}
	close(take)
	<-taken
	if err := st.Send(context.Background(), "pending", adaptor.MessageID("pending")); err != nil {
		t.Fatal(err)
	}
	st.Cancel()
	close(ack)
	events := assertDone(t, st)
	states := map[string]adaptor.MessageUpdate{}
	for _, ev := range events {
		if m, ok := ev.(adaptor.MessageUpdate); ok {
			states[m.ID] = m
		}
	}
	if states["claimed"].State != adaptor.MessageDelivered || states["pending"].State != adaptor.MessageFailed || states["pending"].Text != "pending" || states["pending"].Reason != adaptor.MessageCancelled {
		t.Fatalf("states=%+v", states)
	}
}
func TestV2RequeueDuplicateAndExecutionDedupe(t *testing.T) {
	f := messageFake(driver.MessageCapability{Boundaries: []driver.Boundary{driver.BoundaryTool}})
	entered := make(chan struct{})
	release := make(chan struct{})
	calls := 0
	f.runFunc = func(_ context.Context, r driver.Request, _ driver.EventSink) (driver.Response, error) {
		calls++
		if calls == 1 {
			close(entered)
			<-release
			items := r.Messages.Claim(driver.BoundaryTool)
			if len(items) != 1 {
				t.Fatalf("items=%v", items)
			}
			r.Messages.Ack(items[0].ID, driver.MessageAck{Requeue: true, MaybeDelivered: true, Err: context.DeadlineExceeded, Mechanism: "fake.timeout"})
			if got := r.Messages.Claim(driver.BoundaryTool); len(got) != 0 {
				t.Errorf("requeued back to same boundary: %v", got)
			}
		} else if !strings.Contains(r.Prompt, "ignore the duplicate") {
			t.Errorf("retry prompt=%q", r.Prompt)
		}
		return messageCheckpoint("ok"), nil
	}
	st := adaptor.New(f).Send(context.Background(), "start")
	<-entered
	for n := 0; n < 2; n++ {
		if err := st.Send(context.Background(), "extra", adaptor.MessageID("id")); err != nil {
			t.Fatal(err)
		}
	}
	close(release)
	assertDone(t, st)
	res, err := st.Result()
	if err != nil || calls != 2 || len(res.Messages()) != 2 {
		t.Fatalf("calls=%d messages=%v err=%v", calls, res.Messages(), err)
	}
}
func TestV2TurnLimitWarningInRun(t *testing.T) {
	f := messageFake(driver.MessageCapability{Continue: driver.ContinueInRun})
	entered := make(chan struct{})
	release := make(chan struct{})
	f.runFunc = func(_ context.Context, r driver.Request, s driver.EventSink) (driver.Response, error) {
		close(entered)
		<-release
		for n := 0; n < 5; n++ {
			_ = s.Emit(driver.RunEvent{Type: driver.RunEventTurnCompleted, Turn: &driver.TurnCompleted{Index: n, Text: fmt.Sprint(n)}})
			items := r.Messages.Claim(driver.BoundaryTurnEnd)
			if n == 3 {
				if len(items) != 1 || !strings.Contains(items[0].Text, "Host policy notice") {
					t.Fatalf("wrap-up=%v", items)
				}
			}
			if n == 4 && len(items) != 0 {
				t.Error("new turn allowed after limit")
			}
			for _, m := range items {
				r.Messages.Ack(m.ID, driver.MessageAck{Delivered: true, Mechanism: "fake.next"})
			}
		}
		return messageCheckpoint("last"), nil
	}
	st := adaptor.New(f, adaptor.WithPolicy(adaptor.Policy{Messages: adaptor.MessagePolicy{MaxTurns: 5}})).Send(context.Background(), "start")
	<-entered
	close(release)
	events := assertDone(t, st)
	warning := 0
	for _, e := range events {
		if n, ok := e.(adaptor.Notice); ok && n.Kind == "limit.approaching" {
			warning++
		}
	}
	res, err := st.Result()
	if err != nil || warning != 1 || res.Turns != 5 {
		t.Fatalf("warning=%d res=%+v err=%v", warning, res, err)
	}
}
func TestV2CustomFormatAndUTF16Acceptance(t *testing.T) {
	f := messageFake(driver.MessageCapability{MaxUnits: 2, Unit: driver.MessageUnitUTF16, Boundaries: []driver.Boundary{driver.BoundaryTool}})
	entered := make(chan struct{})
	release := make(chan struct{})
	f.runFunc = func(_ context.Context, r driver.Request, _ driver.EventSink) (driver.Response, error) {
		close(entered)
		<-release
		items := r.Messages.Claim(driver.BoundaryTool)
		if len(items) != 1 || items[0].Text != "😀" {
			t.Errorf("items=%v", items)
		}
		for _, m := range items {
			r.Messages.Ack(m.ID, driver.MessageAck{Delivered: true})
		}
		return messageCheckpoint("ok"), nil
	}
	format := func(m adaptor.MessageContext) string {
		if m.ID != "id" || m.Text != "original" || m.From.ID != "author" || m.Timing != adaptor.Soon {
			t.Errorf("context=%+v", m)
		}
		return "😀"
	}
	st := adaptor.New(f, adaptor.WithPolicy(adaptor.Policy{Messages: adaptor.MessagePolicy{Format: format}})).Send(context.Background(), "raw")
	<-entered
	if err := st.Send(context.Background(), "original", adaptor.MessageID("id"), adaptor.From(adaptor.Author{Kind: adaptor.AuthorAgent, ID: "author"})); err != nil {
		t.Fatal(err)
	}
	close(release)
	assertDone(t, st)
}

type resolvingMessageDriver struct {
	*fakeDriver
	resolved driver.MessageCapability
	calls    int
}

func (d *resolvingMessageDriver) ResolveMessageCapability(req driver.Request) driver.MessageCapability {
	d.calls++
	if req.ModelOverride != "resolved-model" {
		panic("capability resolved before invocation options")
	}
	return d.resolved
}
func TestV2ResolvedMessageCapability(t *testing.T) {
	f := messageFake(driver.MessageCapability{Interrupt: true, Boundaries: []driver.Boundary{driver.BoundaryImmediate}})
	d := &resolvingMessageDriver{fakeDriver: f}
	entered := make(chan struct{})
	release := make(chan struct{})
	f.runFunc = func(context.Context, driver.Request, driver.EventSink) (driver.Response, error) {
		close(entered)
		<-release
		return messageCheckpoint("ok"), nil
	}
	st := adaptor.New(d).Send(context.Background(), "start", adaptor.WithModel("resolved-model"))
	<-entered
	if err := st.Send(context.Background(), "interrupt", adaptor.When(adaptor.Interrupt)); !errors.Is(err, adaptor.ErrMessageUnsupported) {
		t.Fatalf("err=%v", err)
	}
	close(release)
	assertDone(t, st)
	if d.calls != 1 {
		t.Fatalf("resolver calls=%d", d.calls)
	}
}
func TestV2ThreadSupplementDedupeAfterRestart(t *testing.T) {
	store := memory.NewStore()
	entered := make(chan struct{})
	release := make(chan struct{})
	f := messageFake(driver.MessageCapability{Boundaries: []driver.Boundary{driver.BoundaryTool}})
	f.runFunc = func(_ context.Context, r driver.Request, _ driver.EventSink) (driver.Response, error) {
		close(entered)
		<-release
		for _, m := range r.Messages.Claim(driver.BoundaryTool) {
			r.Messages.Ack(m.ID, driver.MessageAck{Delivered: true, Mechanism: "fake.tool"})
		}
		return messageCheckpoint("ok"), nil
	}
	a := adaptor.New(f, adaptor.WithThreadStore(store))
	st := a.Thread("key").Send(context.Background(), "start")
	<-entered
	if err := st.Send(context.Background(), "once", adaptor.MessageID("supplement")); err != nil {
		t.Fatal(err)
	}
	close(release)
	assertDone(t, st)
	entered = make(chan struct{})
	release = make(chan struct{})
	f.runFunc = func(_ context.Context, r driver.Request, _ driver.EventSink) (driver.Response, error) {
		close(entered)
		<-release
		if got := r.Messages.Claim(driver.BoundaryTool); len(got) != 0 {
			t.Errorf("duplicate delivery=%v", got)
		}
		return messageCheckpoint("ok"), nil
	}
	b := adaptor.New(f, adaptor.WithThreadStore(store))
	st = b.Thread("key").Send(context.Background(), "follow up")
	<-entered
	if err := st.Send(context.Background(), "once", adaptor.MessageID("supplement")); err != nil {
		t.Fatal(err)
	}
	close(release)
	assertDone(t, st)
}

type blockedMessageAttachment struct{ entered, release chan struct{} }

func (p blockedMessageAttachment) AttachRun(context.Context, string) (adaptor.RunAttachment, error) {
	close(p.entered)
	<-p.release
	return adaptor.RunAttachment{}, nil
}
func (blockedMessageAttachment) DetachRun(context.Context, string) error { return nil }
func TestV2MessagesBufferBeforeResourceAcquisition(t *testing.T) {
	p := blockedMessageAttachment{entered: make(chan struct{}), release: make(chan struct{})}
	f := messageFake(driver.MessageCapability{Boundaries: []driver.Boundary{driver.BoundaryTool}})
	f.runFunc = func(_ context.Context, r driver.Request, _ driver.EventSink) (driver.Response, error) {
		items := r.Messages.Claim(driver.BoundaryTool)
		if len(items) != 1 || items[0].ID != "early" {
			t.Errorf("early messages=%v", items)
		}
		for _, m := range items {
			r.Messages.Ack(m.ID, driver.MessageAck{Delivered: true, Mechanism: "fake.tool"})
		}
		return messageCheckpoint("ok"), nil
	}
	st := adaptor.New(f, adaptor.WithRunServices(p)).Send(context.Background(), "start")
	<-p.entered
	if err := st.Send(context.Background(), "early", adaptor.MessageID("early")); err != nil {
		t.Fatal(err)
	}
	close(p.release)
	assertDone(t, st)
}

func TestV2ForkKeepsIndependentMessageIDScope(t *testing.T) {
	f := newSessionFake("fork")
	a := adaptor.New(f.fakeDriver)
	parent := a.Thread("parent")
	if _, err := parent.Send(context.Background(), "parent task", adaptor.MessageID("same-id")).Result(); err != nil {
		t.Fatal(err)
	}
	s := parent.Fork("child").Send(context.Background(), "child task", adaptor.MessageID("same-id"))
	assertDone(t, s)
	res, err := s.Result()
	if err != nil || res.Turns != 1 {
		t.Fatalf("child result=%+v err=%v", res, err)
	}
}

func TestV2CancelReleasesPrelaunchMessageWaiter(t *testing.T) {
	p := blockedMessageAttachment{entered: make(chan struct{}), release: make(chan struct{})}
	f := newFakeDriver()
	d := &resolvingMessageDriver{fakeDriver: f}
	f.runFunc = func(ctx context.Context, _ driver.Request, _ driver.EventSink) (driver.Response, error) {
		return driver.Response{}, ctx.Err()
	}
	st := adaptor.New(d, adaptor.WithRunServices(p), adaptor.WithPolicy(adaptor.Policy{Messages: adaptor.MessagePolicy{MaxPending: 1}})).Send(context.Background(), "start", adaptor.WithModel("resolved-model"))
	<-p.entered
	if err := st.Send(context.Background(), "first"); err != nil {
		t.Fatal(err)
	}
	rejected := make(chan error, 1)
	go func() { rejected <- st.Send(context.Background(), "waiting") }()
	st.Cancel()
	if err := <-rejected; !errors.Is(err, context.Canceled) {
		t.Fatalf("waiting Send error=%v", err)
	}
	close(p.release)
	assertDone(t, st)
}
