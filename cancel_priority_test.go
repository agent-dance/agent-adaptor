package adaptor_test

import (
	"context"
	"errors"
	"strings"
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
				// Cancellation is observed at several layers; the host sees each
				// distinct cause once, not a stack of repeated context errors.
				text := err.Error()
				if strings.Count(text, want.Error()) > 2 || strings.Count(text, diagnostic.Error()) != 1 || strings.Count(text, "process exited with status -1") != 1 {
					t.Fatalf("unreadable cancellation error: %q", text)
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

func TestCancelledDriverFailureIsNotRepeatedAsDiagnostic(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		started := make(chan struct{})
		fake := newFakeDriver()
		fake.runFunc = func(ctx context.Context, _ driver.Request, _ driver.EventSink) (driver.Response, error) {
			close(started)
			<-ctx.Done()
			// A Driver that already classifies its own teardown as cancellation.
			return driver.Response{Failure: &driver.RunFailure{Code: driver.FailureCancelled, Message: ctx.Err().Error()}}, ctx.Err()
		}
		stream := adaptor.New(fake).Send(context.Background(), "original")
		<-started
		stream.Cancel()
		_, err := stream.Result()
		var business *adaptor.RunError
		if !errors.Is(err, context.Canceled) || !errors.As(err, &business) || business.Reason != adaptor.ReasonCancelled {
			t.Fatalf("cancellation class lost: %v", err)
		}
		if text := err.Error(); strings.Contains(text, "driver failure") || strings.Count(text, context.Canceled.Error()) > 2 {
			t.Fatalf("redundant cancellation diagnostics: %q", text)
		}
	})
}
