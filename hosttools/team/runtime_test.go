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

func TestBusyLeaderReceivesCompletionAtBoundary(t *testing.T) {
	ctx := testContext(t)
	var team *Team
	var turns atomic.Int32
	leader := &fakeDriver{cap: driver.MessageCapability{Boundaries: []driver.Boundary{driver.BoundaryTool}}, run: func(ctx context.Context, request driver.Request, _ driver.EventSink) (driver.Response, error) {
		if turns.Add(1) != 1 {
			t.Error("completion unnecessarily started another leader execution")
		}
		request.Messages.Window(true)
		if _, err := call(team, request, "delegate", map[string]any{"member": "worker", "objective": "work"}); err != nil {
			return driver.Response{}, err
		}
		select {
		case <-request.Messages.Signal():
		case <-ctx.Done():
			return driver.Response{}, ctx.Err()
		}
		messages := request.Messages.Claim(driver.BoundaryTool)
		if len(messages) != 1 || !strings.Contains(messages[0].Text, "worker complete") {
			t.Errorf("messages=%v", messages)
		}
		for _, message := range messages {
			request.Messages.Ack(message.ID, driver.MessageAck{Delivered: true, Mechanism: "fake.tool_boundary"})
		}
		return answer("integrated without waiting", 1), nil
	}}
	worker := &fakeDriver{run: func(_ context.Context, _ driver.Request, _ driver.EventSink) (driver.Response, error) {
		return answer("worker complete", 2), nil
	}}
	team = fixture(t, Config{Leader: adaptor.New(leader), Members: []Member{{Key: "worker", Runner: adaptor.New(worker)}}})
	events, result, err := collect(t, team.Send(ctx, "start"))
	if err != nil || result.Turns != 1 || result.Usage.InputTokens != 3 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	seen := false
	for _, event := range events {
		if message, ok := event.(adaptor.MessageUpdate); ok && message.State == adaptor.MessageDelivered && message.Boundary == "tool" {
			seen = true
		}
	}
	if !seen {
		t.Error("completion did not join at boundary")
	}
}

func TestLeaderSteersRunningTask(t *testing.T) {
	ctx := testContext(t)
	var team *Team
	var turns atomic.Int32
	worker := &fakeDriver{cap: driver.MessageCapability{Boundaries: []driver.Boundary{driver.BoundaryTool}}, run: func(ctx context.Context, request driver.Request, _ driver.EventSink) (driver.Response, error) {
		request.Messages.Window(true)
		select {
		case <-request.Messages.Signal():
		case <-ctx.Done():
			return driver.Response{}, ctx.Err()
		}
		messages := request.Messages.Claim(driver.BoundaryTool)
		if len(messages) != 1 || !strings.Contains(messages[0].Text, "change direction") || !strings.Contains(messages[0].Text, "leader") {
			t.Errorf("steering=%v", messages)
		}
		for _, message := range messages {
			request.Messages.Ack(message.ID, driver.MessageAck{Delivered: true, Mechanism: "fake.steer"})
		}
		return answer("steered", 1), nil
	}}
	leader := &fakeDriver{run: func(_ context.Context, request driver.Request, _ driver.EventSink) (driver.Response, error) {
		if turns.Add(1) == 1 {
			task, err := call(team, request, "delegate", map[string]any{"member": "worker", "objective": "work"})
			if err != nil {
				return driver.Response{}, err
			}
			if _, err := call(team, request, "send", map[string]any{"task": task["task"], "text": "change direction", "when": "soon"}); err != nil {
				return driver.Response{}, err
			}
		}
		return answer("leader", 1), nil
	}}
	team = fixture(t, Config{Leader: adaptor.New(leader), Members: []Member{{Key: "worker", Runner: adaptor.New(worker)}}})
	if _, _, err := collect(t, team.Send(ctx, "start")); err != nil {
		t.Fatal(err)
	}
}

func TestWakeCoalescingAndNotesInvariant(t *testing.T) {
	ctx := testContext(t)
	var team *Team
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	idle := make(chan struct{})
	var turns atomic.Int32
	worker := &fakeDriver{run: func(ctx context.Context, _ driver.Request, sink driver.EventSink) (driver.Response, error) {
		if err := sink.EmitStream(driver.StreamPayload{Kind: driver.StreamTextStart, MessageID: "text"}); err != nil {
			return driver.Response{}, err
		}
		if err := sink.EmitStream(driver.StreamPayload{Kind: driver.StreamTextContent, MessageID: "text", Delta: "progress note"}); err != nil {
			return driver.Response{}, err
		}
		if err := sink.EmitStream(driver.StreamPayload{Kind: driver.StreamTextEnd, MessageID: "text"}); err != nil {
			return driver.Response{}, err
		}
		started <- struct{}{}
		select {
		case <-release:
			return answer("worker complete", 1), nil
		case <-ctx.Done():
			return driver.Response{}, ctx.Err()
		}
	}}
	leader := &fakeDriver{run: func(_ context.Context, request driver.Request, _ driver.EventSink) (driver.Response, error) {
		if turns.Add(1) == 1 {
			for index := 0; index < 2; index++ {
				if _, err := call(team, request, "delegate", map[string]any{"member": "worker", "objective": "work"}); err != nil {
					return driver.Response{}, err
				}
			}
		} else if !strings.Contains(request.Prompt, "progress note") || strings.Count(request.Prompt, " is succeeded.") != 2 {
			t.Errorf("coalesced prompt=%s", request.Prompt)
		}
		return answer("leader", 1), nil
	}}
	team = fixture(t, Config{Leader: adaptor.New(leader), Store: &idleObservedStore{MemoryStore: NewMemoryStore(), idle: idle}, Members: []Member{{Key: "worker", Runner: adaptor.New(worker), MaxActive: 2}}})
	timers := make(chan chan time.Time, 2)
	team.newTimer = func(time.Duration) *wakeTimer {
		channel := make(chan time.Time, 1)
		timers <- channel
		return &wakeTimer{channel: channel, stop: func() bool { return true }}
	}
	stream := team.Thread("coalescing").Send(ctx, "start")
	terminal := make(chan struct{}, 2)
	notes := make(chan struct{}, 2)
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		for event := range stream.Events() {
			switch typed := event.(type) {
			case adaptor.SubagentUpdate:
				if typed.Kind == adaptor.SubagentFinished {
					terminal <- struct{}{}
				}
				if typed.Kind == adaptor.SubagentDelta && typed.Delta == "progress note" {
					notes <- struct{}{}
				}
			}
		}
	}()
	<-started
	<-started
	<-idle
	<-notes
	<-notes
	select {
	case <-timers:
		t.Error("progress woke leader")
	default:
	}
	close(release)
	<-terminal
	<-terminal
	var timer chan time.Time
	select {
	case timer = <-timers:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	timer <- time.Now()
	<-drained
	if result, err := stream.Result(); err != nil || result.Turns != 2 || turns.Load() != 2 {
		t.Fatalf("result=%+v turns=%d err=%v", result, turns.Load(), err)
	}
}

func TestWorkerApprovalForwardedAndWaiting(t *testing.T) {
	ctx := testContext(t)
	var team *Team
	allow := make(chan struct{})
	var turns atomic.Int32
	worker := &fakeDriver{run: func(ctx context.Context, _ driver.Request, sink driver.EventSink) (driver.Response, error) {
		if err := sink.Emit(driver.RunEvent{Type: driver.RunEventRuntime, Text: "approval-ready"}); err != nil {
			return driver.Response{}, err
		}
		select {
		case <-allow:
		case <-ctx.Done():
			return driver.Response{}, ctx.Err()
		}
		_, err := sink.(driver.DecisionCapableSink).RequestDecision(ctx, driver.DecisionRequest{Kind: driver.HumanDecisionPermission, Prompt: "Allow write?", RequestID: "permission"})
		if err != nil {
			return driver.Response{}, err
		}
		return answer("approved", 1), nil
	}}
	leader := &fakeDriver{run: func(_ context.Context, request driver.Request, _ driver.EventSink) (driver.Response, error) {
		if turns.Add(1) == 1 {
			if _, err := call(team, request, "delegate", map[string]any{"member": "worker", "objective": "work"}); err != nil {
				return driver.Response{}, err
			}
		}
		return answer("leader", 1), nil
	}}
	store := NewMemoryStore()
	team = fixture(t, Config{Leader: adaptor.New(leader), Store: store, Members: []Member{{Key: "worker", Runner: adaptor.New(worker, adaptor.WithPolicy(adaptor.Policy{Approvals: adaptor.ApprovalPolicy{Permission: adaptor.ApprovalAsk}}))}}})
	stream := team.Thread("approval").Send(ctx, "start")
	seen := false
	approved := false
	for event := range stream.Events() {
		switch typed := event.(type) {
		case adaptor.SubagentUpdate:
			if typed.Data["notice"] != nil && !seen {
				close(allow)
				seen = true
			}
		case *adaptor.ApprovalRequest:
			approved = true
			origin := typed.Origin()
			if origin.Member != "worker" || origin.Task == "" {
				t.Fatalf("origin=%+v", origin)
			}
			tasks, err := store.Tasks(ctx, "approval")
			if err != nil || len(tasks) != 1 || tasks[0].State != WaitingApproval {
				t.Fatalf("tasks=%v err=%v", tasks, err)
			}
			if err := typed.Approve(ctx); err != nil {
				t.Fatal(err)
			}
			if err := typed.Approve(ctx); !errors.Is(err, adaptor.ErrApprovalResolved) {
				t.Errorf("duplicate approval=%v", err)
			}
		}
	}
	if result, err := stream.Result(); err != nil || !seen || !approved || result.Subagents()[0].State != string(Succeeded) {
		t.Fatalf("result=%+v seen=%v approved=%v err=%v", result, seen, approved, err)
	}
}

func TestLeaderCancelledNotesAreUnread(t *testing.T) {
	ctx := testContext(t)
	var team *Team
	var turns atomic.Int32
	noteEmitted := make(chan struct{})
	noteObserved := make(chan struct{})
	worker := &fakeDriver{run: func(ctx context.Context, _ driver.Request, sink driver.EventSink) (driver.Response, error) {
		_ = sink.EmitStream(driver.StreamPayload{Kind: driver.StreamTextStart, MessageID: "text"})
		_ = sink.EmitStream(driver.StreamPayload{Kind: driver.StreamTextContent, MessageID: "text", Delta: "unread progress"})
		_ = sink.EmitStream(driver.StreamPayload{Kind: driver.StreamTextEnd, MessageID: "text"})
		close(noteEmitted)
		<-ctx.Done()
		return driver.Response{}, ctx.Err()
	}}
	leader := &fakeDriver{run: func(_ context.Context, request driver.Request, _ driver.EventSink) (driver.Response, error) {
		turns.Add(1)
		task, err := call(team, request, "delegate", map[string]any{"member": "worker", "objective": "work"})
		if err != nil {
			return driver.Response{}, err
		}
		<-noteEmitted
		<-noteObserved
		if _, err := call(team, request, "cancel", map[string]any{"task": task["task"], "reason": "not needed"}); err != nil {
			return driver.Response{}, err
		}
		return answer("cancelled intentionally", 1), nil
	}}
	team = fixture(t, Config{Leader: adaptor.New(leader), Members: []Member{{Key: "worker", Runner: adaptor.New(worker)}}})
	stream := team.Send(ctx, "start")
	for event := range stream.Events() {
		if update, ok := event.(adaptor.SubagentUpdate); ok && update.Kind == adaptor.SubagentDelta && update.Delta == "unread progress" {
			close(noteObserved)
		}
	}
	result, err := stream.Result()
	if err != nil {
		t.Fatal(err)
	}
	tasks := result.Subagents()
	if turns.Load() != 1 || len(tasks) != 1 || tasks[0].State != string(Cancelled) || len(tasks[0].Unread) != 1 || tasks[0].Unread[0] != "unread progress" {
		t.Fatalf("turns=%d tasks=%+v", turns.Load(), tasks)
	}
}

func TestTaskSendRejectsLiveStandby(t *testing.T) {
	ctx := testContext(t)
	leader := &fakeDriver{run: func(_ context.Context, _ driver.Request, _ driver.EventSink) (driver.Response, error) {
		return answer("standby still works", 1), nil
	}}
	team := fixture(t, Config{Leader: adaptor.New(leader)})
	lifetime, cancel := context.WithCancel(ctx)
	defer cancel()
	waiting, err := team.Thread("scope").Standby(lifetime)
	if err != nil {
		t.Fatal(err)
	}
	events := waiting.Events()
	rejected := team.Thread("scope").Send(ctx, "task", adaptor.MessageID("exact"))
	if _, err := rejected.Result(); !errors.Is(err, adaptor.ErrThreadBusy) {
		t.Fatalf("Result error=%v", err)
	}
	for event := range rejected.Events() {
		if done, ok := event.(adaptor.Done); ok && !errors.Is(done.Err, adaptor.ErrThreadBusy) {
			t.Fatalf("Done=%+v", done)
		}
	}
	if err := waiting.Send(ctx, "standby message"); err != nil {
		t.Fatal(err)
	}
	for event := range events {
		if done, ok := event.(adaptor.Done); ok {
			if done.Err != nil || done.Result.Text != "standby still works" {
				t.Fatalf("Done=%+v", done)
			}
			break
		}
	}
	cancel()
	for range events {
	}
}

func TestTaskSendRejectsBusyScope(t *testing.T) {
	ctx := testContext(t)
	entered := make(chan struct{})
	release := make(chan struct{})
	leader := &fakeDriver{run: func(ctx context.Context, _ driver.Request, _ driver.EventSink) (driver.Response, error) {
		close(entered)
		select {
		case <-release:
		case <-ctx.Done():
			return driver.Response{}, ctx.Err()
		}
		return answer("first", 1), nil
	}}
	team := fixture(t, Config{Leader: adaptor.New(leader)})
	first := team.Thread("scope").Send(ctx, "first")
	<-entered
	if _, err := team.Thread("scope").Fork("branch"); !errors.Is(err, adaptor.ErrThreadBusy) {
		t.Fatalf("active Fork=%v", err)
	}
	for _, opts := range [][]adaptor.CallOption{nil, {adaptor.MessageID("id"), adaptor.From(adaptor.Author{Kind: adaptor.AuthorHuman})}, {adaptor.WithModel("other")}} {
		second := team.Thread("scope").Send(ctx, "second", opts...)
		if second == first {
			t.Fatal("task joined active execution")
		}
		if _, err := second.Result(); !errors.Is(err, adaptor.ErrThreadBusy) {
			t.Fatalf("Result error=%v", err)
		}
		events, _, err := collect(t, second)
		if !errors.Is(err, adaptor.ErrThreadBusy) {
			t.Fatal(err)
		}
		if len(events) != 1 {
			t.Fatalf("events=%v", events)
		}
	}
	close(release)
	if _, _, err := collect(t, first); err != nil {
		t.Fatal(err)
	}
}
