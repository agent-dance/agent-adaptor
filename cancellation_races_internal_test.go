package adaptor

import (
	"context"
	"errors"
	"testing"

	"github.com/agent-dance/agent-adaptor/v2/driver"
)

func TestObservedProviderFailureSurvivesCancellationTeardown(t *testing.T) {
	for _, name := range []string{"missing_response_failure", "cancelled_response_failure", "transport_response_failure"} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			sink := newEventSink(eventSinkConfig{runID: "provider-before-cancel"})
			defer sink.abort()
			sink.invocationCtx = ctx
			sink.enableAuthoritativeLifecycle()
			failure := &driver.RunFailure{
				Code:     driver.FailureAgentError,
				Message:  "official provider failure before cancellation",
				Metadata: map[string]any{"provider_code": "upstream_failure"},
			}
			if err := sink.EmitStream(driver.StreamPayload{Kind: driver.StreamRunError, Error: failure}); err != nil {
				t.Fatal(err)
			}
			cancel()
			response := driver.Response{Output: "partial", ExitCode: -1}
			if name == "cancelled_response_failure" {
				response.Failure = &driver.RunFailure{Code: driver.FailureCancelled, Message: "teardown cancelled"}
			} else if name == "transport_response_failure" {
				response.Failure = &driver.RunFailure{Code: driver.FailureAgentError, Message: "transport closed after cancellation"}
			}
			response, err := classifyInvocationOutcome(ctx, response, context.Canceled, nil, sink.observedProviderFailure())
			_, err = finalizeRun("provider-before-cancel", sink, response, err)
			var business *RunError
			if !errors.As(err, &business) || !errors.Is(err, ErrAgentFailed) || errors.Is(err, context.Canceled) {
				t.Fatalf("observed provider verdict replaced by teardown: %v", err)
			}
			if business.Message != failure.Message || business.Details["provider_code"] != "upstream_failure" {
				t.Fatalf("observed provider failure details lost: %+v", business)
			}
			if business.Result == nil || business.Result.Text != "partial" {
				t.Fatalf("partial output lost: %+v", business.Result)
			}
		})
	}
}

func TestTranscriptFailureKeepsResponseClassificationAfterCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sink := newEventSink(eventSinkConfig{runID: "classified-failure"})
	defer sink.abort()
	sink.invocationCtx = ctx
	_ = sink.Emit(driver.RunEvent{Type: driver.RunEventItem, Item: &driver.TranscriptItem{Kind: driver.TranscriptFailure, Text: "provider limit reached"}})
	cancel()
	failure := &driver.RunFailure{Code: driver.FailurePolicyError, Message: "provider limit reached", Metadata: map[string]any{"limit": 42}}
	response, err := classifyInvocationOutcome(ctx, driver.Response{Failure: failure}, context.Canceled, nil, sink.observedProviderFailure())
	_, err = finalizeRun("classified-failure", sink, response, err)
	var business *RunError
	if !errors.As(err, &business) || business.Reason != ReasonPolicyViolation || business.Details["limit"] != 42 || errors.Is(err, context.Canceled) {
		t.Fatalf("normalized transcript signal discarded the Driver's precise failure: %v", err)
	}
}

func TestCancelContextPrecedesOutstandingApprovalTeardown(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sink := newEventSink(eventSinkConfig{runID: "cancel-before-abort"})
	defer sink.abort()
	sink.invocationCtx = ctx
	sink.enableAuthoritativeLifecycle()
	approval := newApprovalRequest(driver.DecisionRequest{RequestID: "pending", Kind: driver.HumanDecisionPermission})
	sink.outstanding[approval.ID] = approval
	enteredCancel, allowCancel := make(chan struct{}), make(chan struct{})
	stream := &runStream{sink: sink, cancel: func() {
		close(enteredCancel)
		<-allowCancel
		cancel()
	}}

	// Pause at context cancellation. If approvals were expired first, the
	// Driver can already publish a teardown failure while ctx is still live.
	cancelled := make(chan struct{})
	go func() {
		stream.Cancel()
		close(cancelled)
	}()
	<-enteredCancel
	var teardownBeforeCancel bool
	select {
	case <-approval.ready():
		teardownBeforeCancel = true
		sink.captureDriverLifecycle(driver.StreamPayload{
			Kind: driver.StreamRunError,
			Error: &driver.RunFailure{
				Code:    driver.FailureAgentError,
				Message: "approval transport shut down",
			},
		}, nil)
	default:
	}
	close(allowCancel)
	<-cancelled

	if teardownBeforeCancel {
		t.Fatal("approval teardown started before the invocation context was cancelled")
	}
	response, err := classifyInvocationOutcome(ctx, driver.Response{
		Failure: &driver.RunFailure{Code: driver.FailureAgentError, Message: "approval transport shut down"},
	}, errors.New("approval transport closed"), nil, sink.observedProviderFailure())
	_, err = finalizeRun("cancel-before-abort", sink, response, err)
	if !errors.Is(err, context.Canceled) || errors.Is(err, ErrAgentFailed) {
		t.Fatalf("approval teardown overrode cancellation: %v", err)
	}
}
