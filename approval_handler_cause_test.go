package adaptor

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agent-dance/agent-adaptor/driver"
)

// Err's gate models preemption after the receiver has obtained the handler
// outcome, but before it observes ordinary context cancellation.
type handlerCauseGateContext struct {
	context.Context
	armed    atomic.Bool
	checking chan struct{}
	proceed  chan struct{}
}

func (c *handlerCauseGateContext) Err() error {
	if c.armed.CompareAndSwap(true, false) {
		close(c.checking)
		<-c.proceed
	}
	return c.Context.Err()
}

type handlerCauseError struct{}

func (*handlerCauseError) Error() string { return "approval backend closed" }

func requireHandlerCause(t *testing.T, err error, original *handlerCauseError, panicked bool) {
	t.Helper()
	if panicked {
		if err == nil || !strings.Contains(err.Error(), "approval handler panic: "+original.Error()) {
			t.Fatalf("lost observed handler panic: %v", err)
		}
		return
	}
	var typed *handlerCauseError
	if !errors.Is(err, original) || !errors.As(err, &typed) || typed != original {
		t.Fatalf("lost observed handler error identity: %v", err)
	}
}

func TestApprovalReceivedHandlerCauseSurvivesCancellation(t *testing.T) {
	for _, panicked := range []bool{false, true} {
		for _, decision := range []driver.DecisionResult{driver.DecisionAborted, driver.DecisionApproved, driver.DecisionAnswered, driver.DecisionRejected} {
			name := "error/" + string(decision)
			if panicked {
				name = "panic/" + string(decision)
			}
			t.Run(name, func(t *testing.T) {
				parent, cancel := context.WithCancel(context.Background())
				defer cancel()
				ctx := &handlerCauseGateContext{Context: parent, checking: make(chan struct{}), proceed: make(chan struct{})}
				original := &handlerCauseError{}
				var request *ApprovalRequest
				kind := driver.HumanDecisionPermission
				if decision == driver.DecisionAnswered {
					kind = driver.HumanDecisionQuestion
				}
				sink := newEventSink(eventSinkConfig{runID: "handler-cause", buffer: 8, handler: func(ctx context.Context, r *ApprovalRequest) error {
					request = r
					var err error
					switch decision {
					case driver.DecisionApproved:
						err = r.Approve(ctx)
					case driver.DecisionAnswered:
						err = r.Answer(ctx, "answer")
					case driver.DecisionRejected:
						err = r.Deny(ctx, "denied")
					}
					if err != nil {
						t.Errorf("initial response: %v", err)
					}
					// runHandler is the only subsequent caller of this Err gate.
					ctx.(*handlerCauseGateContext).armed.Store(true)
					if panicked {
						panic(original)
					}
					return original
				}})
				defer sink.close()
				type outcome struct {
					response driver.DecisionResponse
					decision driver.DecisionResult
					err      error
				}
				done := make(chan outcome, 1)
				go func() {
					resp, result, err := sink.runHandler(ctx, driver.DecisionRequest{RequestID: "request", Kind: kind})
					done <- outcome{resp, result, err}
				}()
				select {
				case <-ctx.checking:
				case <-time.After(5 * time.Second):
					close(ctx.proceed)
					t.Fatal("handler outcome was not received")
				}
				cancel()
				close(ctx.proceed)
				got := <-done
				if got.decision != decision || got.response.Result != decision || got.response.RequestID != "request" {
					t.Fatalf("cancellation changed the response: %#v", got)
				}
				if decision == driver.DecisionAborted {
					if !errors.Is(got.err, context.Canceled) {
						t.Fatalf("lost cancellation: %v", got.err)
					}
					requireHandlerCause(t, got.err, original, panicked)
				} else if got.err != nil {
					t.Fatalf("resolved response became an error: %v", got.err)
				}
				if err := request.Deny(context.Background(), "duplicate"); !errors.Is(err, ErrApprovalResolved) {
					t.Fatalf("late or duplicate response: %v", err)
				}
				sink.recordContext(ctx)
				sink.finishTerminal()
				res, err := finalizeRun("handler-cause", sink, driver.Response{Output: "partial"}, got.err, nil)
				var failed *RunError
				if res != nil || !errors.As(err, &failed) || failed.Reason != ReasonCancelled || failed.Result == nil || failed.Result.Text != "partial" {
					t.Fatalf("unexpected final cancellation outcome: %v, %v", res, err)
				}
				requireHandlerCause(t, failed.Cause, original, panicked)
			})
		}
	}
}

func TestApprovalReceivedHandlerCausePreservesTimeoutDecision(t *testing.T) {
	for _, panicked := range []bool{false, true} {
		name := "error"
		if panicked {
			name = "panic"
		}
		t.Run(name, func(t *testing.T) {
			parent, cancel := context.WithTimeoutCause(context.Background(), 250*time.Millisecond, errApprovalDeadline)
			defer cancel()
			ctx := &handlerCauseGateContext{Context: parent, checking: make(chan struct{}), proceed: make(chan struct{})}
			original := &handlerCauseError{}
			sink := newEventSink(eventSinkConfig{buffer: 8, handler: func(context.Context, *ApprovalRequest) error {
				ctx.armed.Store(true)
				if panicked {
					panic(original)
				}
				return original
			}})
			defer sink.close()
			done := make(chan struct{})
			go func() {
				defer close(done)
				resp, decision, err := sink.runHandler(ctx, driver.DecisionRequest{RequestID: "timeout", Kind: driver.HumanDecisionPermission})
				if resp.Result != driver.DecisionTimedOut || decision != driver.DecisionTimedOut || err != nil {
					t.Errorf("handler cause changed timeout decision: %#v, %s, %v", resp, decision, err)
				}
			}()
			select {
			case <-ctx.checking:
			case <-time.After(5 * time.Second):
				close(ctx.proceed)
				t.Fatal("handler outcome was not received before its deadline")
			}
			<-parent.Done()
			close(ctx.proceed)
			<-done
			outcome := sink.terminalSnapshot()
			if outcome.reason != "" || sink.pendingFailure() != nil {
				t.Fatalf("handler cause bypassed the timeout fallback policy: %#v", outcome)
			}
			requireHandlerCause(t, outcome.cause, original, panicked)
		})
	}
}

func TestApprovalLateHandlerDoesNotAlterCancelledResult(t *testing.T) {
	for _, panicked := range []bool{false, true} {
		name := "error"
		if panicked {
			name = "panic"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			entered := make(chan *ApprovalRequest, 1)
			release, finished := make(chan struct{}), make(chan struct{})
			original := &handlerCauseError{}
			d := brokerTestDriver{run: func(ctx context.Context, _ driver.Request, sink driver.EventSink) (driver.Response, error) {
				_, err := sink.(driver.DecisionCapableSink).RequestDecision(ctx, driver.DecisionRequest{Kind: driver.HumanDecisionPermission})
				return driver.Response{Output: "partial"}, err
			}}
			a := New(d, OnApproval(func(_ context.Context, req *ApprovalRequest) error {
				defer close(finished)
				entered <- req
				<-release
				if panicked {
					panic(original)
				}
				return original
			}))
			stream := a.Stream(ctx, "work")
			request := <-entered
			cancel()
			drained := make(chan struct{})
			go func() {
				for range stream.Events() {
				}
				close(drained)
			}()
			select {
			case <-drained:
			case <-time.After(5 * time.Second):
				close(release)
				t.Fatal("cancelled run waited for a late handler")
			}
			res, err := stream.Result()
			var failed *RunError
			if res != nil || !errors.As(err, &failed) || failed.Reason != ReasonCancelled || failed.Result == nil || failed.Result.Text != "partial" || !errors.Is(err, context.Canceled) {
				close(release)
				t.Fatalf("unexpected cancellation outcome: %v, %v", res, err)
			}
			before := failed.Cause
			if err := request.Approve(context.Background()); !errors.Is(err, ErrApprovalExpired) {
				t.Errorf("late response was accepted: %v", err)
			}
			close(release)
			<-finished
			again, againErr := stream.Result()
			if again != res || againErr != err || failed.Cause != before || errors.Is(err, original) {
				t.Fatal("late handler changed the completed result")
			}
		})
	}
}
