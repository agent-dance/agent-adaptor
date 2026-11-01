package adaptor

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agent-dance/agent-adaptor/v2/driver"
	"github.com/agent-dance/agent-adaptor/v2/internal/engine"
	"github.com/agent-dance/agent-adaptor/v2/threadstore"
)

func TestStandbyInternalSubscriptionKeepsApprovalFallbackNotice(t *testing.T) {
	sink := newEventSink(eventSinkConfig{runID: "standby-approval", policy: ApprovalPolicy{Timeout: time.Hour, OnTimeout: FallbackContinue}})
	sink.broker.spool = true
	var hostSubscribed atomic.Bool
	sink.hostEventsCalled = &hostSubscribed
	// Model Standby's forwarding subscription, while the host is absent.
	events := sink.broker.subscribe()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	response, err := sink.RequestDecision(ctx, driver.DecisionRequest{Kind: driver.HumanDecisionPermission, Prompt: "allow?"})
	if err != nil || response.Result != driver.DecisionTimedOut {
		t.Fatalf("unobserved approval response=%+v err=%v", response, err)
	}
	sink.close()
	var explained bool
	for event := range events {
		if notice, ok := event.(Notice); ok && strings.Contains(notice.Text, "resolved immediately using the timeout fallback") {
			explained = true
		}
	}
	if !explained {
		t.Fatal("immediate approval fallback omitted its explanatory Notice")
	}
}

func TestStandbyForwardingCancellationPreservesReliableEventsAndReportsDrops(t *testing.T) {
	broker := newEventBroker("run", "standby", 1, true)
	broker.spool = true
	events := broker.subscribe()
	broker.publish(RunStarted{}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	observed := &observedWaitContext{Context: ctx, entered: make(chan struct{})}
	published := make(chan bool, 1)
	go func() { published <- broker.publishContext(observed, ToolResult{ID: "reliable"}, nil) }()
	select {
	case <-observed.entered:
	case <-time.After(time.Second):
		t.Fatal("forwarding did not reach backpressure")
	}
	cancel()
	select {
	case ok := <-published:
		if !ok {
			t.Fatal("cancellation discarded a reliable tool result")
		}
	case <-time.After(time.Second):
		t.Fatal("cancellation did not unblock forwarding without a reader")
	}
	if broker.publishContext(ctx, TextDelta{Phase: PhaseContent, Text: "permitted drop"}, nil) {
		t.Fatal("cancelled delta unexpectedly published")
	}
	broker.publish(Done{}, nil)
	broker.close()
	var received []Event
	for event := range events {
		received = append(received, event)
	}
	if len(received) != 4 {
		t.Fatalf("forwarded events=%+v", received)
	}
	if result, ok := received[1].(ToolResult); !ok || result.ID != "reliable" {
		t.Fatalf("reliable result lost: %+v", received)
	}
	drop, ok := received[2].(Dropped)
	if !ok || drop.Count != 1 || drop.ByKind["text.content"] != 1 || drop.FirstSequence != 3 || drop.LastSequence != 3 {
		t.Fatalf("cancelled delta accounting=%+v", received[2])
	}
	if _, ok := received[3].(Done); !ok {
		t.Fatalf("Done must follow loss reporting: %+v", received)
	}
}

func TestStandbyEventsDiscardUntilSubscription(t *testing.T) {
	broker := newEventBroker("", "standby", 1, true)
	broker.spool, broker.discardUnsubscribed, broker.multipleRuns = true, true, true
	standby := &standbyStream{broker: broker}
	for index := 0; index < 10000; index++ {
		broker.publish(MessageUpdate{ID: "unobserved", State: MessageDelivered}, nil)
		broker.publish(TextDelta{Text: "unobserved"}, nil)
		broker.publish(Done{Result: &Result{}}, nil)
	}
	if len(broker.queue) != 0 || len(broker.events) != 0 || broker.sequence != 0 {
		t.Fatalf("unobserved standby buffered events: queue=%d channel=%d seq=%d", len(broker.queue), len(broker.events), broker.sequence)
	}
	events := standby.Events()
	broker.publish(MessageUpdate{ID: "observed", State: MessageDelivered}, nil)
	broker.close()
	var count int
	for event := range events {
		if update, ok := event.(MessageUpdate); !ok || update.ID != "observed" {
			t.Fatalf("replayed unobserved event: %+v", event)
		}
		count++
	}
	if count != 1 {
		t.Fatalf("observed count=%d", count)
	}
}

func TestStandbyRetryClassificationAndBounds(t *testing.T) {
	for _, test := range []struct {
		err   error
		retry bool
		delay time.Duration
	}{
		{err: &RunError{Reason: "rate_limit", Details: map[string]any{"retry_after": "2s"}}, retry: true, delay: 2 * time.Second},
		{err: &RunError{Reason: "network_error"}, retry: true, delay: 250 * time.Millisecond},
		{err: &RunError{Reason: "server_error", Details: map[string]any{"retry_after": 3600}}, retry: true, delay: time.Hour},
		{err: &RunError{Reason: "rate_limit", Details: map[string]any{"retry_after": "invalid"}}, retry: true, delay: 250 * time.Millisecond},
		{err: &RunError{Reason: ReasonAgentError, Details: map[string]any{"status_code": 503}}, retry: true, delay: 250 * time.Millisecond},
		{err: &RunError{Reason: ReasonApprovalDenied}},
		{err: errors.New("rate limit text is not a classification")},
		{err: context.Canceled},
	} {
		delay, retry := standbyRetryDelay(test.err)
		if retry != test.retry || retry && delay != test.delay {
			t.Errorf("retry(%v)=%v/%s; want %v/%s", test.err, retry, delay, test.retry, test.delay)
		}
	}
}

func TestThreadContinuityPreambleBound(t *testing.T) {
	record := &threadstore.Record{EarlierGuidance: []string{strings.Repeat("g", 16000)}, RecentTurns: []string{strings.Repeat("t", 16000)}}
	if preamble := threadContinuityPreamble(record, true); len(preamble) > 8192 || !strings.Contains(preamble, "Earlier guidance") || !strings.Contains(preamble, "Recent assistant turns") {
		t.Fatalf("preamble size=%d", len(preamble))
	}
}

func TestStandbyQueuedAdmissionIsCancellationSafe(t *testing.T) {
	agent := New(brokerTestDriver{})
	broker := newEventBroker("", "queue", 1, true)
	broker.spool = true
	standby := &standbyStream{thread: &Thread{agent: agent}, ctx: context.Background(), broker: broker, maxPending: 1, queue: []*standbyMessage{{id: "queued", text: "original queued text", carried: []MessageOutcome{{ID: "carried", Text: "carried queued text"}}}}, changed: make(chan struct{}), wake: make(chan struct{}, 1)}
	ctx, cancel := context.WithCancel(context.Background())
	observed := &observedWaitContext{Context: ctx, entered: make(chan struct{})}
	result := make(chan error, 1)
	go func() { result <- standby.Send(observed, "not admitted", WithModel("another")) }()
	<-observed.entered
	cancel()
	err := <-result
	var message *MessageError
	if !errors.As(err, &message) || !errors.Is(err, context.Canceled) || message.Text != "not admitted" {
		t.Fatalf("rejected admission lost text or cause: %v", err)
	}
	standby.Cancel()
	broker.close()
	var returned bool
	var carriedReturned bool
	for event := range standby.Events() {
		if update, ok := event.(MessageUpdate); ok && update.ID == "queued" {
			returned = update.State == MessageFailed && update.Reason == MessageCancelled && update.Text == "original queued text"
		}
		if update, ok := event.(MessageUpdate); ok && update.ID == "carried" {
			carriedReturned = update.State == MessageFailed && update.Reason == MessageCancelled && update.Text == "carried queued text"
		}
	}
	if !returned || !carriedReturned {
		t.Fatal("Cancel did not return queued input")
	}
}

type standbyCancelDuringCheck struct {
	context.Context
	cancel context.CancelFunc
	once   sync.Once
}

func (c *standbyCancelDuringCheck) Err() error {
	first := false
	c.once.Do(func() { first = true; c.cancel() })
	if first {
		return nil
	}
	return c.Context.Err()
}

func TestStandbyContextEndBetweenLoopCheckAndWait(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	broker := newEventBroker("", "cancellation-window", 1, true)
	broker.spool, broker.discardUnsubscribed = true, true
	runtime := &threadRuntime{wake: make(chan struct{}, 1), changed: make(chan struct{})}
	standby := &standbyStream{runtime: runtime, ctx: &standbyCancelDuringCheck{Context: ctx, cancel: cancel}, broker: broker, stopOwner: func() bool { return true }, wake: make(chan struct{}, 1), changed: make(chan struct{}), done: make(chan struct{})}
	runtime.standby = standby
	go standby.run()
	select {
	case <-standby.done:
	case <-time.After(5 * time.Second):
		t.Fatal("context cancellation was lost between the loop check and select")
	}
	for range standby.Events() {
	}
}

type standbyBufferDriver struct{ brokerTestDriver }

func (standbyBufferDriver) Descriptor() driver.Descriptor {
	return driver.Descriptor{Type: "standby-buffer", Sessions: driver.SessionCapability{SupportsResume: true}}
}

func (standbyBufferDriver) SessionCodec() driver.SessionCodec {
	return engine.SessionCodecFor(brokerTestDriver{})
}

func (standbyBufferDriver) SessionConfigFingerprint() (string, error) {
	return "standby-buffer/v1", nil
}

func TestStandbyWithoutConsumerCompletesAndBuffersNothing(t *testing.T) {
	driverImpl := standbyBufferDriver{brokerTestDriver{run: func(_ context.Context, _ driver.Request, sink driver.EventSink) (driver.Response, error) {
		for index := 0; index < 8; index++ {
			if err := sink.EmitStream(driver.StreamPayload{Kind: driver.StreamTextContent, Delta: "unobserved"}); err != nil {
				return driver.Response{}, err
			}
		}
		return driver.Response{Output: "complete", Checkpoint: &driver.Checkpoint{Valid: true, State: &driver.SessionState{ResumeID: "buffer-checkpoint"}}}, nil
	}}}
	agent := New(driverImpl, WithEventBuffer(1), WithBlockingEvents())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	defer func() { _ = agent.Close(context.Background()) }()
	handle, err := agent.Thread("unobserved").Standby(ctx)
	if err != nil {
		t.Fatal(err)
	}
	standby := handle.(*standbyStream)
	for index := 0; index < 4; index++ {
		if err := standby.Send(ctx, "next execution", WithModel("fixed")); err != nil {
			t.Fatal(err)
		}
		for {
			standby.mu.Lock()
			idle := standby.current == nil && len(standby.queue) == 0
			changed := standby.changed
			standby.mu.Unlock()
			if idle {
				break
			}
			select {
			case <-changed:
			case <-ctx.Done():
				t.Fatal("execution stalled without an event consumer")
			}
		}
	}
	standby.broker.mu.Lock()
	buffered := len(standby.broker.queue) + len(standby.broker.events)
	subscribed := standby.broker.subscribed
	standby.broker.mu.Unlock()
	if buffered != 0 || subscribed {
		t.Fatalf("unobserved executions buffered=%d subscribed=%t", buffered, subscribed)
	}
	cancel()
	for range standby.Events() {
	}
}
