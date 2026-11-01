package adaptor_test

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	adaptor "github.com/agent-dance/agent-adaptor/v2"
	"github.com/agent-dance/agent-adaptor/v2/driver"
)

func TestF2CoreCancelPriority(t *testing.T) {
	for _, mode := range []string{"stream", "caller", "deadline", "after-interrupt"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				diagnostic := errors.New("transport closed by teardown")
				started := make(chan struct{})
				fake := newFakeDriver()
				fake.runFunc = func(ctx context.Context, _ driver.Request, sink driver.EventSink) (driver.Response, error) {
					if mode == "after-interrupt" {
						_ = sink.Emit(driver.RunEvent{Type: driver.RunEventItem, Item: &driver.TranscriptItem{Kind: driver.TranscriptResult, IsError: true, Subtype: "interrupted"}})
					}
					close(started)
					<-ctx.Done()
					return driver.Response{ExitCode: -1, Output: "partial", RawStreams: &driver.RawStreams{Stdout: "raw before cancel"}, Failure: &driver.RunFailure{Code: driver.FailureAgentError, Message: "process exited with status -1"}}, diagnostic
				}
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				var opts []adaptor.CallOption
				if mode == "deadline" {
					opts = append(opts, adaptor.WithTimeout(time.Second))
				}
				stream := adaptor.New(fake).Send(ctx, "original", opts...)
				<-started
				switch mode {
				case "stream", "after-interrupt":
					stream.Cancel()
				case "caller":
					cancel()
				}
				_, err := stream.Result()
				want := context.Canceled
				if mode == "deadline" {
					want = context.DeadlineExceeded
				}
				var business *adaptor.RunError
				if !errors.Is(err, want) || !errors.Is(err, diagnostic) || !errors.As(err, &business) || business.Reason != adaptor.ReasonCancelled {
					t.Fatalf("cancellation lost its class or diagnostics: %T %v", err, err)
				}
				if business.Result.Raw().Stdout != "raw before cancel" {
					t.Fatal("partial audit lost")
				}
				var done *adaptor.Done
				for event := range stream.Events() {
					if value, ok := event.(adaptor.Done); ok {
						done = &value
					}
					if value, ok := event.(adaptor.TurnEnd); ok && !errors.Is(value.Err, want) {
						t.Fatalf("TurnEnd error: %v", value.Err)
					}
					if value, ok := event.(adaptor.RunFinished); ok && value.Reason != adaptor.ReasonCancelled {
						t.Fatalf("RunFinished reason: %s", value.Reason)
					}
				}
				if done == nil || done.Err != err || done.Result != nil {
					t.Fatalf("Done differs from Result: %+v / %v", done, err)
				}
				messages := business.Result.Undelivered()
				if len(messages) != 1 || messages[0].Reason != adaptor.MessageCancelled || messages[0].Text != "original" {
					t.Fatalf("cancelled message: %+v", messages)
				}
			})
		})
	}
}

func TestF2ResultIncludesUsageObservedAfterTurnEnd(t *testing.T) {
	fake := newFakeDriver()
	fake.runFunc = func(_ context.Context, _ driver.Request, sink driver.EventSink) (driver.Response, error) {
		_ = sink.Emit(driver.RunEvent{Type: driver.RunEventTurnCompleted, Turn: &driver.TurnCompleted{Usage: &driver.Usage{InputTokens: 2}}})
		return driver.Response{Usage: &driver.Usage{InputTokens: 5}}, nil
	}
	result, err := adaptor.New(fake).Send(context.Background(), "go").Result()
	if err != nil || result.Usage.InputTokens != 5 {
		t.Fatalf("late usage lost: %+v / %v", result, err)
	}
}
