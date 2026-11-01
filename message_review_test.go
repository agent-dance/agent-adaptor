package adaptor

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/agent-dance/agent-adaptor/v2/driver"
)

// observedWaitContext signals evaluation of a blocking select's context arm.
// Tests can prove that a producer reached its capacity wait without sleeping.
type observedWaitContext struct {
	context.Context
	entered chan struct{}
	once    sync.Once
}

func (c *observedWaitContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.entered) })
	return c.Context.Done()
}

func TestT1ReviewBlockingProducerWaits(t *testing.T) {
	for _, release := range []string{"consume", "abort", "context", "result"} {
		t.Run(release, func(t *testing.T) {
			b := newEventBroker("review", "", 1, true)
			b.spool = true
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			defer b.abort()
			wait := &observedWaitContext{Context: ctx, entered: make(chan struct{})}
			if release != "result" {
				b.subscribe()
			}
			if !b.publish(TextDelta{Text: "first"}, nil) {
				t.Fatal("first event rejected")
			}
			produced := make(chan bool, 1)
			go func() { produced <- b.publishContext(wait, TextDelta{Text: "second"}, nil) }()
			select {
			case <-wait.entered:
			case outcome := <-produced:
				t.Fatalf("producer did not wait at capacity: %v", outcome)
			}
			select {
			case outcome := <-produced:
				t.Fatalf("producer escaped full capacity: %v", outcome)
			default:
			}
			// This publication must succeed without waiting for the ordinary producer,
			// even though combined channel/queue occupancy already equals capacity.
			if !b.publish(Notice{Kind: NoticeLifecycle, Text: "critical"}, nil) {
				t.Fatal("critical publication rejected")
			}
			switch release {
			case "consume":
				if e := <-b.events; e.(TextDelta).Text != "first" {
					t.Fatal(e)
				}
				select {
				case outcome := <-produced:
					t.Fatalf("producer ignored queued critical capacity: %v", outcome)
				default:
				}
				if e := <-b.events; e.(Notice).Text != "critical" {
					t.Fatalf("critical overtaken: %#v", e)
				}
				if !<-produced {
					t.Fatal("producer failed after capacity release")
				}
				if e := <-b.events; e.(TextDelta).Text != "second" {
					t.Fatal(e)
				}
			case "abort":
				b.abort()
				if <-produced {
					t.Fatal("aborted producer reported delivery")
				}
			case "context":
				cancel()
				if <-produced {
					t.Fatal("cancelled producer reported delivery")
				}
			case "result":
				b.awaitResult()
				if <-produced {
					t.Fatal("Result-only producer retained ordinary data")
				}
			}
			b.publishTerminal(Done{}, nil)
			b.close()
			for range b.subscribe() {
			}
		})
	}
}

// reviewDriver needs no subprocess; InRun allows supplemental delivery without
// introducing a session codec or a second execution path into these tests.
type reviewDriver struct {
	run func(context.Context, driver.Request, driver.EventSink) (driver.Response, error)
	cap driver.MessageCapability
}

func (d reviewDriver) Descriptor() driver.Descriptor {
	return driver.Descriptor{Type: "review", DisplayName: "Review", Messages: d.cap}
}
func (reviewDriver) ValidateConfig(any) error { return nil }
func (d reviewDriver) Run(ctx context.Context, r driver.Request, s driver.EventSink) (driver.Response, error) {
	return d.run(ctx, r, s)
}

func TestT1ReviewSendInsideEventLoop(t *testing.T) {
	for _, blocking := range []bool{false, true} {
		t.Run(fmt.Sprint(blocking), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			begin := make(chan struct{})
			entered := make(chan struct{})
			full := make(chan struct{})
			sent := make(chan struct{})
			d := reviewDriver{cap: driver.MessageCapability{Continue: driver.ContinueInRun, Boundaries: []driver.Boundary{driver.BoundaryTool}}}
			d.run = func(ctx context.Context, r driver.Request, s driver.EventSink) (driver.Response, error) {
				select {
				case <-begin:
				case <-ctx.Done():
					return driver.Response{}, ctx.Err()
				}
				_ = s.EmitStream(driver.StreamPayload{Kind: driver.StreamTextContent, Delta: "trigger"})
				select {
				case <-entered:
				case <-ctx.Done():
					return driver.Response{}, ctx.Err()
				}
				_ = s.EmitStream(driver.StreamPayload{Kind: driver.StreamTextContent, Delta: "filler"})
				close(full)
				_ = s.EmitStream(driver.StreamPayload{Kind: driver.StreamTextContent, Delta: "pressure"})
				select {
				case <-sent:
				case <-ctx.Done():
					return driver.Response{}, ctx.Err()
				}
				items := r.Messages.Claim(driver.BoundaryTool)
				if len(items) != 1 || items[0].ID != "inside-loop" {
					return driver.Response{}, fmt.Errorf("claim=%v", items)
				}
				r.Messages.Ack(items[0].ID, driver.MessageAck{Delivered: true, Mechanism: "review.tool"})
				return driver.Response{Output: "done"}, nil
			}
			opts := []Option{WithEventBuffer(1)}
			if blocking {
				opts = append(opts, WithBlockingEvents())
			}
			st := New(d, opts...).Send(ctx, "start")
			defer st.Cancel()
			delivered := false
			var done *Result
			for ev := range st.Events() {
				switch e := ev.(type) {
				case RunStarted:
					close(begin)
				case TextDelta:
					if e.Text == "trigger" {
						close(entered)
						select {
						case <-full:
						case <-ctx.Done():
							t.Fatal("producer failed to fill capacity")
						}
						if err := st.Send(ctx, "additional", MessageID("inside-loop")); err != nil {
							t.Fatal(err)
						}
						close(sent)
					}
				case MessageUpdate:
					if e.ID == "inside-loop" && e.State == MessageDelivered {
						delivered = true
					}
				case Done:
					if e.Err != nil {
						t.Fatal(e.Err)
					}
					done = e.Result
				}
			}
			res, err := st.Result()
			if err != nil || !delivered || res != done || res.Text != "done" {
				t.Fatalf("delivered=%v res=%+v err=%v", delivered, res, err)
			}
		})
	}
}

func TestT1ReviewResultOnlyKeepsCriticalLog(t *testing.T) {
	for _, blocking := range []bool{false, true} {
		t.Run(fmt.Sprint(blocking), func(t *testing.T) {
			ordinary := []Event{
				TextDelta{Phase: PhaseStart}, TextDelta{Text: "content"}, TextDelta{Phase: PhaseEnd},
				Thinking{Text: "thinking"}, ToolCall{ID: "tool", Phase: PhaseStart}, ToolCall{ID: "tool", ArgsDelta: "{}"}, ToolCall{ID: "tool", Phase: PhaseEnd},
				ToolResult{ID: "tool"}, ProcessInfo{Kind: ProcessSpawn}, ProcessInfo{Kind: ProcessStdout, Bytes: []byte("raw")},
				SubagentUpdate{Member: "worker", Kind: SubagentStarted}, SubagentUpdate{Member: "worker", Kind: SubagentDelta}, SubagentUpdate{Member: "worker", Kind: SubagentFinished},
			}
			d := reviewDriver{run: func(_ context.Context, _ driver.Request, s driver.EventSink) (driver.Response, error) {
				sink := s.(*eventSink)
				for n := 0; n < 4; n++ {
					for _, ev := range ordinary {
						sink.push(ev)
					}
					sink.push(Notice{Kind: NoticeLifecycle, Text: "retained notice"})
				}
				sink.push(&ApprovalRequest{ID: "descriptive-approval"})
				sink.push(Dropped{Source: "provider", Reason: "provider loss", Count: 3})
				return driver.Response{Output: "answer", RawStreams: &driver.RawStreams{Stdout: "complete stdout", Stderr: "complete stderr"}, Transcript: []driver.TranscriptItem{{Kind: driver.TranscriptAssistant, Text: "answer"}}}, nil
			}}
			opts := []Option{WithEventBuffer(4)}
			if blocking {
				opts = append(opts, WithBlockingEvents())
			}
			st := New(d, opts...).Send(context.Background(), "start")
			res, err := st.Result()
			if err != nil || res.Text != "answer" || res.Raw().Stdout != "complete stdout" || len(res.Transcript()) != 1 {
				t.Fatalf("result=%+v err=%v", res, err)
			}
			var summary *Dropped
			var last Event
			providerReports := 0
			notices := 0
			approvals := 0
			var seq uint64
			for ev := range st.Events() {
				if !eventCritical(ev) {
					t.Fatalf("ordinary event retained: %T", ev)
				}
				if ev.Meta().Sequence <= seq {
					t.Fatal("closed log reordered")
				}
				seq = ev.Meta().Sequence
				switch e := ev.(type) {
				case Dropped:
					if e.Source == "sdk.event_broker" {
						if summary != nil {
							t.Fatal("multiple SDK drop summaries")
						}
						copy := e
						summary = &copy
					} else if e.Source == "provider" {
						providerReports++
					}
				case Notice:
					if e.Text == "retained notice" {
						notices++
					}
				case *ApprovalRequest:
					approvals++
				}
				last = ev
			}
			if summary == nil || summary.Count != 4*len(ordinary) || summary.Reason != "events_unobserved" {
				t.Fatalf("summary=%+v", summary)
			}
			byKind := 0
			for _, count := range summary.ByKind {
				byKind += count
			}
			if byKind != summary.Count {
				t.Fatal("incomplete drop accounting")
			}
			if providerReports != 1 || notices != 4 || approvals != 1 {
				t.Fatalf("provider=%d notices=%d approvals=%d", providerReports, notices, approvals)
			}
			if final, ok := last.(Done); !ok || final.Result != res || final.Err != err {
				t.Fatalf("last=%#v", last)
			}
		})
	}
}

func TestT1ReviewNeverObservedCompletionPrunesOrdinary(t *testing.T) {
	b := newEventBroker("unobserved", "", 2, false)
	b.spool = true
	b.publish(RunStarted{}, nil)
	for n := 0; n < 5; n++ {
		b.publish(TextDelta{Text: "ordinary"}, nil)
		b.publish(Notice{Text: "critical"}, nil)
	}
	b.publish(RunFinished{}, nil)
	b.publishTerminal(Done{}, nil)
	b.close()
	summary := 0
	for ev := range b.subscribe() {
		if !eventCritical(ev) {
			t.Fatalf("ordinary retained: %T", ev)
		}
		if d, ok := ev.(Dropped); ok {
			summary++
			if d.Count != 5 {
				t.Fatalf("count=%d", d.Count)
			}
		}
	}
	if summary != 1 {
		t.Fatalf("summaries=%d", summary)
	}
}

type reviewResolverDriver struct {
	reviewDriver
	resolve func(driver.Request) driver.MessageCapability
}

func (d reviewResolverDriver) ResolveMessageCapability(r driver.Request) driver.MessageCapability {
	return d.resolve(r)
}

type reviewAttachment struct{ entered, release chan struct{} }

func (p reviewAttachment) AttachRun(ctx context.Context, _ string) (RunAttachment, error) {
	close(p.entered)
	select {
	case <-p.release:
		return RunAttachment{Preamble: "preamble"}, nil
	case <-ctx.Done():
		return RunAttachment{}, ctx.Err()
	}
}
func (reviewAttachment) DetachRun(context.Context, string) error { return nil }

func TestT1ReviewFinalResolverRequestAndQueuedReconciliation(t *testing.T) {
	for _, unit := range []driver.MessageUnit{driver.MessageUnitBytes, driver.MessageUnitUTF16} {
		t.Run(string(unit), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			attached := reviewAttachment{entered: make(chan struct{}), release: make(chan struct{})}
			finish := make(chan struct{})
			ran := make(chan struct{})
			cap := driver.MessageCapability{Continue: driver.ContinueInRun, Interrupt: true, Boundaries: []driver.Boundary{driver.BoundaryImmediate}}
			limit := 3
			if unit == driver.MessageUnitUTF16 {
				limit = 1
			}
			d := reviewResolverDriver{reviewDriver: reviewDriver{cap: cap}}
			var resolvedRequest driver.Request
			d.resolve = func(r driver.Request) driver.MessageCapability {
				resolvedRequest = r
				if r.Messages == nil || r.Prompt != "preamble\n\nstart" || r.ModelOverride != "selected" {
					t.Errorf("resolver request=%+v", r)
				}
				return driver.MessageCapability{Continue: driver.ContinueInRun, Boundaries: []driver.Boundary{driver.BoundaryImmediate}, MaxUnits: limit, Unit: unit}
			}
			d.run = func(ctx context.Context, r driver.Request, s driver.EventSink) (driver.Response, error) {
				if !reflect.DeepEqual(r, resolvedRequest) {
					t.Error("resolver did not see final request")
				}
				_ = s.Emit(driver.RunEvent{Type: driver.RunEventLifecycle, Text: "driver running"})
				close(ran)
				select {
				case <-finish:
				case <-ctx.Done():
					return driver.Response{}, ctx.Err()
				}
				items := r.Messages.Claim(driver.BoundaryImmediate)
				if len(items) != 1 || items[0].ID != "valid" {
					return driver.Response{}, fmt.Errorf("claim=%v", items)
				}
				r.Messages.Ack(items[0].ID, driver.MessageAck{Delivered: true, Mechanism: "review.immediate"})
				return driver.Response{Output: "done"}, nil
			}
			st := New(d, WithRunServices(attached), WithPolicy(Policy{Messages: MessagePolicy{Format: func(m MessageContext) string { return m.Text }}})).Send(ctx, "start", WithModel("selected"))
			defer st.Cancel()
			<-attached.entered
			if err := st.Send(ctx, "i", MessageID("interrupt"), When(Interrupt)); err != nil {
				t.Fatal(err)
			}
			if err := st.Send(ctx, "😀", MessageID("large")); err != nil {
				t.Fatal(err)
			}
			if err := st.Send(ctx, "v", MessageID("valid")); err != nil {
				t.Fatal(err)
			}
			events := st.Events()
			close(attached.release)
			<-ran
			failed := map[string]MessageUpdate{}
			for len(failed) < 2 {
				select {
				case ev := <-events:
					if m, ok := ev.(MessageUpdate); ok && m.State == MessageFailed {
						failed[m.ID] = m
					}
					if n, ok := ev.(Notice); ok && n.Text == "driver running" {
						t.Fatal("queued failures were not reconciled before dispatch")
					}
				case <-ctx.Done():
					t.Fatal("queued messages did not fail during resolution")
				}
			}
			if failed["interrupt"].Reason != MessageProviderRejected || failed["interrupt"].Text != "i" || failed["large"].Reason != MessageLimitExceeded || failed["large"].Text != "😀" {
				t.Fatalf("failures=%+v", failed)
			}
			close(finish)
			for range events {
			}
			res, err := st.Result()
			if err != nil || len(res.Undelivered()) != 2 {
				t.Fatalf("result=%+v err=%v", res, err)
			}
		})
	}
}

func TestT1ReviewDisabledResolverGetsNilMessagePort(t *testing.T) {
	d := reviewResolverDriver{resolve: func(r driver.Request) driver.MessageCapability {
		if r.Messages != nil {
			t.Error("disabled messages exposed to resolver")
		}
		return driver.MessageCapability{}
	}}
	d.run = func(_ context.Context, r driver.Request, _ driver.EventSink) (driver.Response, error) {
		if r.Messages != nil {
			t.Error("disabled messages exposed to Driver.Run")
		}
		return driver.Response{Output: "done"}, nil
	}
	if _, err := New(d, WithPolicy(Policy{Messages: MessagePolicy{Disabled: true}})).Send(context.Background(), "start").Result(); err != nil {
		t.Fatal(err)
	}
}

func TestT1ReviewCapabilitySetterSerializesWithAdmission(t *testing.T) {
	h := testMessageHub(t, MessagePolicy{Format: func(m MessageContext) string { return m.Text }})
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		for n := 0; n < 100; n++ {
			h.setCapability(driver.MessageCapability{MaxUnits: 1 + n%2})
		}
	}()
	go func() {
		defer wg.Done()
		<-start
		for n := 0; n < 100; n++ {
			if err := h.send(context.Background(), "v", MessageID("same")); err != nil {
				t.Error(err)
			}
		}
	}()
	close(start)
	wg.Wait()
}

func TestT1ReviewCapabilityTrimPreservesClaimedOwnership(t *testing.T) {
	h := testMessageHub(t, MessagePolicy{Format: func(m MessageContext) string { return m.Text }})
	h.startMessage(messageSettings{}, "initial raw prompt")
	h.setCapability(driver.MessageCapability{Interrupt: true})
	if err := h.send(context.Background(), "interrupt", MessageID("claimed"), When(Interrupt)); err != nil {
		t.Fatal(err)
	}
	if items := h.Claim(driver.BoundaryImmediate); len(items) != 1 {
		t.Fatal(items)
	}
	h.setCapability(driver.MessageCapability{MaxUnits: 1})
	h.Ack("claimed", driver.MessageAck{Delivered: true})
	out := h.outcomes()
	if out[0].State != MessageAccepted || out[1].State != MessageDelivered {
		t.Fatalf("claimed ownership changed: %+v", out)
	}
	if err := h.send(context.Background(), "interrupt", When(Interrupt)); !errors.Is(err, ErrMessageUnsupported) {
		t.Fatal(err)
	}
}
