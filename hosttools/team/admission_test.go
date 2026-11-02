package team

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	adaptor "github.com/agent-dance/agent-adaptor/v2"
	"github.com/agent-dance/agent-adaptor/v2/driver"
)

func TestRejectedInterruptIsNeverReplayed(t *testing.T) {
	ctx := testContext(t)
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	var replayed atomic.Bool
	leader := adaptor.New(&fakeDriver{run: func(ctx context.Context, req driver.Request, _ driver.EventSink) (driver.Response, error) {
		if calls.Add(1) == 1 {
			close(entered)
			select {
			case <-release:
			case <-ctx.Done():
				return driver.Response{}, ctx.Err()
			}
		} else if strings.Contains(req.Prompt, "must never execute") {
			replayed.Store(true)
		}
		return answer("leader", 1), nil
	}})
	store := NewMemoryStore()
	team := fixture(t, Config{Leader: leader, Store: store})
	stream := team.Thread("rejected").Send(ctx, "start")
	drained := make(chan struct{})
	var events []adaptor.Event
	go func() {
		defer close(drained)
		for event := range stream.Events() {
			events = append(events, event)
		}
	}()
	<-entered
	err := stream.Send(ctx, "must never execute", adaptor.MessageID("rejected-interrupt"), adaptor.When(adaptor.Interrupt))
	if !errors.Is(err, adaptor.ErrMessageUnsupported) {
		t.Fatalf("expected synchronous unsupported rejection, got %v", err)
	}
	close(release)
	<-drained
	result, err := stream.Result()
	if err != nil {
		t.Fatal(err)
	}
	if replayed.Load() {
		t.Fatalf("synchronously rejected Interrupt reached leader as a new prompt; provider calls=%d", calls.Load())
	}
	if calls.Load() != 1 {
		t.Fatalf("rejection started another provider invocation: %d", calls.Load())
	}
	for _, event := range events {
		if update, ok := event.(adaptor.MessageUpdate); ok && update.ID == "rejected-interrupt" {
			t.Fatalf("rejected input entered the delivery state machine: %+v", update)
		}
	}
	for _, outcome := range result.Messages() {
		if outcome.ID == "rejected-interrupt" {
			t.Fatalf("rejected input appeared in Result.Messages: %+v", outcome)
		}
	}
	messages, err := store.Messages(ctx, "rejected")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, message := range messages {
		if message.ID == "rejected-interrupt" {
			found = true
			if message.State != DeliveryFailed || message.Reason != string(adaptor.MessageProviderRejected) {
				t.Fatalf("rejected input remains replayable: %+v", message)
			}
		}
	}
	if !found {
		t.Fatal("missing terminal inbox record for rejected input")
	}
	if _, _, err := collect(t, team.Thread("rejected").Send(ctx, "next")); err != nil {
		t.Fatal(err)
	}
	if replayed.Load() || calls.Load() != 2 {
		t.Fatalf("rejected input replayed in a later execution; calls=%d", calls.Load())
	}
}

func TestCancelledHostAdmissionCannotRequeueDuringLeaderReroute(t *testing.T) {
	ctx := testContext(t)
	delivery, cancel := context.WithCancel(ctx)
	defer cancel()
	store := NewMemoryStore()
	lease, err := store.AcquireScope(ctx, "reroute", "owner", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	run := &execution{ctx: ctx, store: store, lease: lease, scope: lease.Scope, inflight: make(map[string]InboxMessage), deliveries: 1}
	message, err := run.admitHost(ctx, "withdraw during reroute", []adaptor.CallOption{adaptor.MessageID("withdrawn")})
	if err != nil || message == nil {
		t.Fatalf("claim=%+v err=%v", message, err)
	}
	run.inflight[message.ID] = *message
	reply := make(chan error, 1)
	admission := &messageAdmission{reply: reply}
	cancel()
	admission.mu.Lock()
	admission.completeLocked(&adaptor.MessageError{ID: message.ID, Text: message.Text, Reason: adaptor.MessageCancelled, Err: delivery.Err()})
	admission.mu.Unlock()
	host := &hostMessage{ctx: delivery, text: message.Text, opts: []adaptor.MessageOption{adaptor.MessageID(message.ID)}, admission: admission, claimed: message}
	// The previous leader ended before accepting this Send. Cancellation won
	// admission before the actor could route it to a replacement leader.
	run.handle(sendEnd{host: host, inbox: []InboxMessage{*message}, err: adaptor.ErrRunFinished})
	if err := <-reply; !errors.Is(err, context.Canceled) {
		t.Fatalf("admission error=%v", err)
	}
	messages, err := store.Messages(ctx, lease.Scope)
	if err != nil || len(messages) != 1 || messages[0].State != DeliveryFailed || messages[0].Reason != string(adaptor.MessageCancelled) {
		t.Fatalf("rejected input remained replayable: messages=%+v err=%v", messages, err)
	}
	claimed, err := store.ClaimMessages(ctx, lease, true, time.Minute)
	if err != nil || len(claimed) != 0 {
		t.Fatalf("rejected input was reclaimed: messages=%+v err=%v", claimed, err)
	}
}
