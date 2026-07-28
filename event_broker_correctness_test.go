package adaptor

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/agent-dance/agent-adaptor/driver"
)

func TestEventBrokerBlockingAbortReleasesPublisher(t *testing.T) {
	b := newEventBroker("run-1", "thread-1", 1, true)
	if !b.publish(TextDelta{Text: "first"}, nil) {
		t.Fatal("first publish failed")
	}
	returned := make(chan bool, 1)
	go func() { returned <- b.publish(TextDelta{Text: "blocked"}, nil) }()

	b.abort()
	select {
	case ok := <-returned:
		if ok {
			t.Fatal("blocked publication reported delivery after abort")
		}
	case <-time.After(time.Second):
		t.Fatal("abort did not release blocking publication")
	}
	b.close()
	for range b.events {
	}
}

func TestEventBrokerSerializesConcurrentProducers(t *testing.T) {
	const producers = 8
	const perProducer = 32
	b := newEventBroker("run-order", "thread-order", 1, true)

	received := make(chan []Event, 1)
	go func() {
		var events []Event
		for ev := range b.events {
			events = append(events, ev)
		}
		received <- events
	}()

	var wg sync.WaitGroup
	for producer := 0; producer < producers; producer++ {
		wg.Add(1)
		go func(producer int) {
			defer wg.Done()
			for n := 0; n < perProducer; n++ {
				b.publish(Notice{Kind: NoticeLifecycle, Data: map[string]any{"producer": producer, "n": n}}, nil)
			}
		}(producer)
	}
	wg.Wait()
	b.close()
	events := <-received
	if len(events) != producers*perProducer {
		t.Fatalf("received %d events, want %d", len(events), producers*perProducer)
	}
	for i, ev := range events {
		meta := ev.Meta()
		if want := uint64(i + 1); meta.Sequence != want {
			t.Fatalf("events[%d] sequence=%d, want %d", i, meta.Sequence, want)
		}
		if meta.RunID != "run-order" || meta.ThreadKey != "thread-order" || meta.Time.IsZero() {
			t.Fatalf("events[%d] incomplete meta: %+v", i, meta)
		}
	}
}

func TestEventBrokerDropsOnlyDeltaAndPreservesCriticalOrder(t *testing.T) {
	b := newEventBroker("run-drop", "", 1, false)
	if !b.publish(TextDelta{Text: "kept"}, nil) {
		t.Fatal("first delta should fit")
	}
	if b.publish(TextDelta{Text: "dropped"}, nil) {
		t.Fatal("second delta should be dropped")
	}
	criticalDone := make(chan bool, 1)
	go func() {
		criticalDone <- b.publish(Notice{Kind: NoticeLifecycle, Text: "critical"}, nil)
	}()

	if got := (<-b.events).(TextDelta).Text; got != "kept" {
		t.Fatalf("first event=%q", got)
	}
	drop, ok := (<-b.events).(Dropped)
	if !ok {
		t.Fatal("drop marker must precede the next critical event")
	}
	if drop.Count != 1 || drop.ByKind["text.content"] != 1 || drop.FirstSequence != 2 || drop.LastSequence != 2 {
		t.Fatalf("incomplete dropped marker: %+v", drop)
	}
	critical, ok := (<-b.events).(Notice)
	if !ok || critical.Text != "critical" {
		t.Fatalf("critical event lost: %#v", critical)
	}
	if !<-criticalDone {
		t.Fatal("critical publication failed")
	}
	b.close()
}

func TestEventBrokerPreservesProviderDropped(t *testing.T) {
	b := newEventBroker("run-provider-drop", "", 1, false)
	b.publish(TextDelta{Text: "fill"}, nil)
	done := make(chan bool, 1)
	go func() {
		done <- b.publish(Dropped{Count: 7, Source: "provider", Reason: "provider_buffer"}, nil)
	}()
	<-b.events
	got, ok := (<-b.events).(Dropped)
	if !ok || got.Count != 7 || got.Source != "provider" {
		t.Fatalf("provider Dropped was altered or swallowed: %#v", got)
	}
	if !<-done {
		t.Fatal("provider Dropped publication failed")
	}
	b.close()
}

func TestEventBrokerAbortNeverFlushesDroppedAfterTerminal(t *testing.T) {
	b := newEventBroker("run-terminal-drop", "", 2, false)
	if !b.publish(TextDelta{Text: "first"}, nil) || !b.publish(TextDelta{Text: "second"}, nil) {
		t.Fatal("initial deltas must fill the buffer")
	}
	if b.publish(TextDelta{Text: "pending-drop"}, nil) {
		t.Fatal("third delta must become a pending drop aggregate")
	}
	b.abort()
	<-b.events
	<-b.events
	if !b.publishTerminal(RunFinished{RunID: "run-terminal-drop", Failed: true}, nil) {
		t.Fatal("terminal must fit after the consumer drains the buffer")
	}
	b.close()

	var events []Event
	for event := range b.events {
		events = append(events, event)
	}
	if len(events) != 1 {
		t.Fatalf("events after drain = %+v, want terminal only", events)
	}
	if _, ok := events[0].(RunFinished); !ok {
		t.Fatalf("event = %T, want RunFinished", events[0])
	}
}

func TestEventBrokerFullBufferAbortReservesTerminalAndSealsLateEvents(t *testing.T) {
	b := newEventBroker("run-terminal-reserve", "", 1, false)
	if !b.publish(RunStarted{RunID: "run-terminal-reserve"}, nil) {
		t.Fatal("RunStarted must fill the ordinary event slot")
	}
	if b.publish(TextDelta{Text: "pending-drop"}, nil) {
		t.Fatal("delta must be aggregated while the ordinary slot is full")
	}

	b.abort()
	terminalDone := make(chan bool, 1)
	go func() {
		terminalDone <- b.publishTerminal(RunFinished{RunID: "run-terminal-reserve", Failed: true}, nil)
	}()
	select {
	case ok := <-terminalDone:
		if !ok {
			t.Fatal("terminal publication failed despite its reserved slot")
		}
	case <-time.After(time.Second):
		t.Fatal("terminal publication waited for a consumer after abort")
	}
	if b.publish(Notice{Kind: NoticeLifecycle, Text: "late"}, nil) {
		t.Fatal("late event crossed the terminal seal")
	}
	b.close()

	var events []Event
	for event := range b.events {
		events = append(events, event)
	}
	if len(events) != 2 {
		t.Fatalf("events = %+v, want RunStarted and RunFinished", events)
	}
	if _, ok := events[0].(RunStarted); !ok {
		t.Fatalf("events[0] = %T, want RunStarted", events[0])
	}
	terminal, ok := events[1].(RunFinished)
	if !ok || !terminal.Failed {
		t.Fatalf("events[1] = %#v, want failed RunFinished", events[1])
	}
	if events[0].Meta().Sequence != 1 || events[1].Meta().Sequence != 3 {
		t.Fatalf("sequences = %d, %d, want 1, 3 including the dropped delta gap",
			events[0].Meta().Sequence, events[1].Meta().Sequence)
	}
}

func TestEventBrokerBlockingAbortReleasesPublisherWithoutUsingTerminalReserve(t *testing.T) {
	b := newEventBroker("run-blocked-terminal", "", 1, true)
	if !b.publish(RunStarted{RunID: "run-blocked-terminal"}, nil) {
		t.Fatal("RunStarted must fill the ordinary event slot")
	}

	entered := make(chan struct{})
	blocked := make(chan bool, 1)
	go func() {
		close(entered)
		blocked <- b.publish(Notice{Kind: NoticeLifecycle, Text: "blocked"}, nil)
	}()
	<-entered
	select {
	case <-blocked:
		t.Fatal("ordinary publisher unexpectedly consumed the terminal reserve")
	case <-time.After(20 * time.Millisecond):
	}

	b.abort()
	select {
	case ok := <-blocked:
		if ok {
			t.Fatal("aborted ordinary publication reported success")
		}
	case <-time.After(time.Second):
		t.Fatal("abort did not release the blocked ordinary publisher")
	}
	if !b.publishTerminal(RunFinished{RunID: "run-blocked-terminal", Failed: true}, nil) {
		t.Fatal("terminal did not use its reserved slot")
	}
	b.close()

	var events []Event
	for event := range b.events {
		events = append(events, event)
	}
	if len(events) != 2 {
		t.Fatalf("events = %+v, want RunStarted and RunFinished", events)
	}
	if _, ok := events[1].(RunFinished); !ok {
		t.Fatalf("last event = %T, want RunFinished", events[1])
	}
}

func TestEventBrokerNormalTerminalFlushesDroppedBeforeReservedTerminal(t *testing.T) {
	b := newEventBroker("run-normal-terminal", "", 1, false)
	if !b.publish(TextDelta{Text: "kept"}, nil) {
		t.Fatal("first delta must fill the ordinary slot")
	}
	if b.publish(TextDelta{Text: "dropped"}, nil) {
		t.Fatal("second delta must be aggregated")
	}

	done := make(chan bool, 1)
	go func() {
		done <- b.publishTerminal(RunFinished{RunID: "run-normal-terminal"}, nil)
	}()
	if event := <-b.events; eventKind(event) != "text.content" {
		t.Fatalf("first event = %T, want TextDelta", event)
	}
	if event := <-b.events; eventKind(event) != "dropped" {
		t.Fatalf("second event = %T, want Dropped", event)
	}
	select {
	case ok := <-done:
		if !ok {
			t.Fatal("normal terminal publication failed")
		}
	case <-time.After(time.Second):
		t.Fatal("normal terminal publication remained blocked after the ordinary slot drained")
	}
	b.close()
	if event := <-b.events; eventKind(event) != "run.finished" {
		t.Fatalf("last event = %T, want RunFinished", event)
	}
	if _, ok := <-b.events; ok {
		t.Fatal("event channel remained open after terminal drain")
	}
}

func TestWithEventMetaSupportsEveryEventKind(t *testing.T) {
	approval := newApprovalRequest(testDecisionRequest("meta", ApprovalPermission))
	events := []Event{
		TextDelta{Text: "text"}, Thinking{Text: "thinking"},
		ToolCall{ID: "call"}, ToolResult{ID: "call"},
		RunStarted{RunID: "provider-run"}, RunFinished{RunID: "provider-run"},
		ProcessInfo{Kind: ProcessStdout}, Notice{Kind: NoticeLifecycle},
		Dropped{Count: 1}, SubagentUpdate{Agent: "worker"}, approval,
	}
	want := EventMeta{RunID: "run-meta", ThreadKey: "opaque/key", Sequence: 42, Time: time.Now().UTC(), TurnID: "turn-1", Source: &EventSourceMeta{RunID: "provider-run", Sequence: 9}}
	for _, ev := range events {
		got := WithEventMeta(ev, want)
		if got == nil || got.Meta().RunID != want.RunID || got.Meta().Sequence != want.Sequence || got.Meta().Source.Sequence != 9 {
			t.Fatalf("WithEventMeta(%T)=%+v", ev, got)
		}
	}
	if WithEventMeta(nil, want) != nil {
		t.Fatal("WithEventMeta(nil) must return nil")
	}
}

func TestEventMetaOverridesDriverCoordinatesAndPreservesSource(t *testing.T) {
	sink := newEventSink(eventSinkConfig{runID: "sdk-run", threadKey: "host/thread"})
	when := time.Now().UTC().Add(-time.Minute)
	_ = sink.EmitStream(driver.StreamPayload{
		Kind: "provider.extension", RunID: "provider-run", ThreadID: "provider-thread",
		TurnID: "turn-7", Sequence: 99, Timestamp: when, Name: "extension-name",
	})
	sink.close()
	ev := <-sink.events
	notice, ok := ev.(Notice)
	if !ok || notice.Data["name"] != "extension-name" {
		t.Fatalf("unknown extension lost Name: %#v", ev)
	}
	meta := ev.Meta()
	if meta.RunID != "sdk-run" || meta.ThreadKey != "host/thread" || meta.Sequence != 1 || meta.TurnID != "turn-7" {
		t.Fatalf("SDK metadata not authoritative: %+v", meta)
	}
	if meta.Source == nil || meta.Source.RunID != "provider-run" || meta.Source.ThreadID != "provider-thread" || meta.Source.Sequence != 99 || !meta.Source.Timestamp.Equal(when) {
		t.Fatalf("driver source metadata lost: %+v", meta.Source)
	}
}

func TestApprovalNoticesPreserveAuditFields(t *testing.T) {
	sink := newEventSink(eventSinkConfig{runID: "run-notice"})
	req := testDecisionRequest("notice", ApprovalQuestion)
	req.Source = "deploy"
	req.ToolCallID = "tool-1"
	req.Prompt = "where?"
	req.Payload = map[string]any{"scope": "prod"}
	req.Choices = []driver.DecisionChoice{{Key: "hold", Label: "Hold"}}
	req.RetryAttempt = 2
	sink.pushRequestedNotice(req)
	resolvedAt := req.CreatedAt.Add(250 * time.Millisecond)
	sink.pushResolvedNotice(req, driver.DecisionResponse{
		RequestID: req.RequestID, Result: driver.DecisionAnswered,
		Choice: "hold", Text: "hold deployment", Answer: map[string]any{"environment": "prod"},
	}, resolvedAt)
	sink.close()
	requested := (<-sink.events).(Notice)
	resolved := (<-sink.events).(Notice)
	if requested.Data["tool_call_id"] != "tool-1" || requested.Data["deadline"] != req.Deadline || requested.Data["attempt"] != 2 {
		t.Fatalf("incomplete requested notice: %+v", requested)
	}
	if resolved.Text != "hold deployment" || resolved.Data["source"] != "deploy" || resolved.Data["result"] != string(driver.DecisionAnswered) || resolved.Data["text"] != "hold deployment" || resolved.Data["latency"] != 250*time.Millisecond {
		t.Fatalf("incomplete resolved notice: %+v", resolved)
	}
}

func TestStreamParentCancelUnblocksBlockingSink(t *testing.T) {
	driverDone := make(chan struct{})
	d := brokerTestDriver{run: func(ctx context.Context, _ driver.Request, sink driver.EventSink) (driver.Response, error) {
		defer close(driverDone)
		_ = sink.Emit(driver.RunEvent{Type: driver.RunEventLifecycle, Text: "fills-buffer"})
		_ = sink.Emit(driver.RunEvent{Type: driver.RunEventLifecycle, Text: "blocks"})
		<-ctx.Done()
		return driver.Response{}, ctx.Err()
	}}
	ctx, cancel := context.WithCancel(context.Background())
	stream := New(d, WithEventBuffer(1), WithBlockingEvents()).Stream(ctx, "cancel")
	cancel()
	select {
	case <-driverDone:
	case <-time.After(time.Second):
		t.Fatal("parent cancellation did not release blocking sink")
	}
	for range stream.Events() {
	}
	if _, err := stream.Result(); !errors.Is(err, context.Canceled) {
		t.Fatalf("Result=%v, want context.Canceled", err)
	}
}

func TestStreamConcurrentCancelIsIdempotent(t *testing.T) {
	d := brokerTestDriver{run: func(ctx context.Context, _ driver.Request, _ driver.EventSink) (driver.Response, error) {
		<-ctx.Done()
		return driver.Response{}, ctx.Err()
	}}
	stream := New(d).Stream(context.Background(), "cancel")
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); stream.Cancel() }()
	}
	wg.Wait()
	for range stream.Events() {
	}
	if _, err := stream.Result(); !errors.Is(err, context.Canceled) {
		t.Fatalf("Result=%v, want context.Canceled", err)
	}
}

func TestStreamImmediateCancelWithFullMergedLifecycleDeliversTerminalWithoutConsumer(t *testing.T) {
	subscribed := make(chan struct{})
	provider := brokerTestRunService{source: func(context.Context, string) <-chan Event {
		close(subscribed)
		closed := make(chan Event)
		close(closed)
		return closed
	}}
	d := brokerTestDriver{run: func(ctx context.Context, _ driver.Request, _ driver.EventSink) (driver.Response, error) {
		<-ctx.Done()
		return driver.Response{}, ctx.Err()
	}}
	stream := New(d, WithEventBuffer(1), WithRunServices(provider)).Stream(context.Background(), "cancel")
	select {
	case <-subscribed:
	case <-time.After(time.Second):
		t.Fatal("run event source was not subscribed")
	}

	// RunStarted now occupies the sole ordinary slot. Do not consume Events
	// until Result returns: Cancel must remain bounded and RunFinished must use
	// the broker's independent terminal reserve.
	stream.Cancel()
	resultDone := make(chan error, 1)
	go func() {
		_, err := stream.Result()
		resultDone <- err
	}()
	select {
	case err := <-resultDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Result error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Cancel/Result waited for an event consumer while the ordinary buffer was full")
	}

	var events []Event
	for event := range stream.Events() {
		events = append(events, event)
	}
	if len(events) != 2 {
		t.Fatalf("events = %+v, want RunStarted and RunFinished", events)
	}
	if _, ok := events[0].(RunStarted); !ok {
		t.Fatalf("first event = %T, want RunStarted", events[0])
	}
	terminal, ok := events[1].(RunFinished)
	if !ok || !terminal.Failed || terminal.Reason != ReasonCancelled {
		t.Fatalf("last event = %#v, want cancelled RunFinished", events[1])
	}
}

func TestDriverOnlyTerminalUsesReserveAndSealsAfterFullBufferCancel(t *testing.T) {
	started := make(chan struct{})
	d := brokerTestDriver{run: func(ctx context.Context, req driver.Request, sink driver.EventSink) (driver.Response, error) {
		if err := sink.EmitStream(driver.StreamPayload{Kind: driver.StreamRunStarted, RunID: req.RunID}); err != nil {
			return driver.Response{}, err
		}
		close(started)
		<-ctx.Done()
		if err := sink.EmitStream(driver.StreamPayload{
			Kind: driver.StreamRunError, RunID: req.RunID,
			Error: &driver.RunFailure{Code: driver.FailureCancelled, Message: "cancelled"},
		}); err != nil {
			return driver.Response{}, err
		}
		// A non-conforming late payload must be rejected by the terminal seal.
		_ = sink.EmitStream(driver.StreamPayload{Kind: driver.StreamTextContent, Delta: "late"})
		return driver.Response{}, ctx.Err()
	}}
	stream := New(d, WithEventBuffer(1)).Stream(context.Background(), "cancel")
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("driver did not fill the ordinary event slot")
	}

	stream.Cancel()
	resultDone := make(chan error, 1)
	go func() {
		_, err := stream.Result()
		resultDone <- err
	}()
	select {
	case err := <-resultDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Result error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Driver-only Cancel/Result waited for an event consumer")
	}

	var events []Event
	for event := range stream.Events() {
		events = append(events, event)
	}
	if len(events) != 2 {
		t.Fatalf("events = %+v, want RunStarted and RunFinished only", events)
	}
	if _, ok := events[0].(RunStarted); !ok {
		t.Fatalf("first event = %T, want RunStarted", events[0])
	}
	terminal, ok := events[1].(RunFinished)
	if !ok || !terminal.Failed || terminal.Reason != ReasonCancelled {
		t.Fatalf("last event = %#v, want cancelled RunFinished", events[1])
	}
}

func TestRunServicePumpStopsWhenSinkAborts(t *testing.T) {
	sink := newEventSink(eventSinkConfig{runID: "run-pump", buffer: 1, blocking: true})
	r := &runResources{runID: "run-pump"}
	sourceClosed := make(chan struct{})
	twoSent := make(chan struct{})
	source := func(ctx context.Context, _ string) <-chan Event {
		ch := make(chan Event)
		go func() {
			defer close(sourceClosed)
			defer close(ch)
			for n := 0; ; n++ {
				select {
				case ch <- Notice{Kind: NoticeLifecycle, Text: "provider"}:
					if n == 1 {
						close(twoSent)
					}
				case <-ctx.Done():
					return
				}
			}
		}()
		return ch
	}
	r.startPumps(context.Background(), sink, []RunEventSource{source})
	// startPumps owns the merged lifecycle and publishes RunStarted before it
	// subscribes the provider. Drain that envelope so the first provider event
	// can fill the one-slot buffer exercised below.
	if ev := <-sink.events; eventKind(ev) != "run.started" {
		t.Fatalf("first event = %T, want RunStarted", ev)
	}
	// The second source send is received only after the first publication
	// filled the one-slot output buffer; the pump then blocks publishing it.
	<-twoSent
	sink.abort()
	stopped := make(chan struct{})
	go func() { _ = r.stopPumps(context.Background()); close(stopped) }()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("event pump did not stop after sink abort")
	}
	select {
	case <-sourceClosed:
	case <-time.After(time.Second):
		t.Fatal("event source did not observe pump cancellation")
	}
}

func TestRunServicePumpShutdownIsBoundedForNonClosingSource(t *testing.T) {
	sink := newEventSink(eventSinkConfig{runID: "run-stuck-source", buffer: 2, blocking: true})
	r := &runResources{runID: "run-stuck-source"}
	providerEvents := make(chan Event)
	source := func(context.Context, string) <-chan Event {
		return providerEvents // deliberately ignores cancellation and stays open
	}
	r.startPumps(context.Background(), sink, []RunEventSource{source})
	if ev := <-sink.events; eventKind(ev) != "run.started" {
		t.Fatalf("first event = %T, want RunStarted", ev)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	err := r.stopPumps(ctx)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("stopPumps error = %v, want context deadline", err)
	}

	// Seal and publish the authoritative terminal while the deliberately
	// broken pump is still alive. Its subsequent event must be rejected by the
	// broker barrier rather than appearing after RunFinished.
	sink.completeAuthoritativeLifecycle(&Result{RunID: "run-stuck-source"}, err)
	providerEvents <- Notice{Kind: NoticeLifecycle, Text: "late-after-terminal"}
	close(providerEvents)
	select {
	case <-r.pumpDone:
	case <-time.After(time.Second):
		t.Fatal("pump did not exit after its source was closed")
	}
	sink.close()
	var events []Event
	for ev := range sink.events {
		events = append(events, ev)
	}
	if len(events) != 1 {
		t.Fatalf("events = %+v, want only the authoritative RunFinished", events)
	}
	if terminal, ok := events[0].(RunFinished); !ok || !terminal.Failed {
		t.Fatalf("event = %#v, want failed RunFinished", events[0])
	}
}

type brokerTestDriver struct {
	run func(context.Context, driver.Request, driver.EventSink) (driver.Response, error)
}

type brokerTestRunService struct {
	source RunEventSource
}

func (p brokerTestRunService) AttachRun(context.Context, string) (RunAttachment, error) {
	return RunAttachment{Events: p.source}, nil
}

func (brokerTestRunService) DetachRun(context.Context, string) error { return nil }

func (d brokerTestDriver) Descriptor() driver.Descriptor {
	return driver.Descriptor{Type: "broker-test", DisplayName: "Broker Test"}
}
func (brokerTestDriver) ValidateConfig(any) error { return nil }
func (d brokerTestDriver) Run(ctx context.Context, req driver.Request, sink driver.EventSink) (driver.Response, error) {
	return d.run(ctx, req, sink)
}

var _ driver.Driver = brokerTestDriver{}

func TestApprovalZeroNilCopyAndExpiry(t *testing.T) {
	var zero ApprovalRequest
	if err := zero.Deny(context.Background(), "no"); !errors.Is(err, ErrApprovalUnavailable) {
		t.Fatalf("zero request Deny=%v, want ErrApprovalUnavailable", err)
	}
	var nilRequest *ApprovalRequest
	if err := nilRequest.Approve(context.Background()); !errors.Is(err, ErrApprovalUnavailable) {
		t.Fatalf("nil request Approve=%v, want ErrApprovalUnavailable", err)
	}

	req := newApprovalRequest(testDecisionRequest("copy", ApprovalPermission))
	copyOfReq := *req
	if err := copyOfReq.Approve(context.Background()); err != nil {
		t.Fatalf("copied request Approve: %v", err)
	}
	if err := req.Deny(context.Background(), "late"); !errors.Is(err, ErrApprovalResolved) {
		t.Fatalf("original after copied response=%v, want ErrApprovalResolved", err)
	}
	resp, ok := req.takeResponse()
	if !ok || resp.RequestID != "copy" {
		t.Fatalf("shared response=%+v, ok=%v", resp, ok)
	}

	expired := newApprovalRequest(testDecisionRequest("expired", ApprovalQuestion))
	expired.expire()
	if err := expired.Answer(context.Background(), "x"); !errors.Is(err, ErrApprovalExpired) || !errors.Is(err, ErrApprovalResolved) {
		t.Fatalf("expired Answer=%v, want both expiry and resolved classification", err)
	}
}

func TestApprovalConcurrentCopiesExactlyOnce(t *testing.T) {
	req := newApprovalRequest(testDecisionRequest("race", ApprovalPermission))
	copyA, copyB := *req, *req
	start := make(chan struct{})
	errs := make(chan error, 2)
	go func() { <-start; errs <- copyA.Approve(context.Background()) }()
	go func() { <-start; errs <- copyB.Deny(context.Background(), "no") }()
	close(start)
	first, second := <-errs, <-errs
	if (first == nil) == (second == nil) {
		t.Fatalf("exactly one copied responder must win: %v / %v", first, second)
	}
	loser := first
	if loser == nil {
		loser = second
	}
	if !errors.Is(loser, ErrApprovalResolved) {
		t.Fatalf("loser=%v, want ErrApprovalResolved", loser)
	}
}

func TestApprovalHandlerUnansweredExpiresRequest(t *testing.T) {
	var captured *ApprovalRequest
	sink := newEventSink(eventSinkConfig{
		runID: "run-handler",
		handler: func(_ context.Context, req *ApprovalRequest) error {
			captured = req
			return nil
		},
	})
	req := testDecisionRequest("handler", ApprovalPermission)
	_, decision, err := sink.runHandler(context.Background(), req)
	if err == nil || decision != driver.DecisionAborted {
		t.Fatalf("unanswered handler decision=%q err=%v", decision, err)
	}
	if captured == nil {
		t.Fatal("handler did not receive request")
	}
	if late := captured.Approve(context.Background()); !errors.Is(late, ErrApprovalExpired) {
		t.Fatalf("late handler answer=%v, want ErrApprovalExpired", late)
	}
	sink.close()
}

func testDecisionRequest(id string, kind ApprovalKind) driver.DecisionRequest {
	return driver.DecisionRequest{
		RequestID: id,
		RunID:     "run-approval",
		Kind:      driver.HumanDecisionKind(kind),
		CreatedAt: time.Now().UTC(),
		Deadline:  time.Now().UTC().Add(time.Minute),
	}
}
