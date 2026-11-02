package adaptor_test

import (
	"context"
	"errors"
	"testing"

	adaptor "github.com/agent-dance/agent-adaptor/v2"
	"github.com/agent-dance/agent-adaptor/v2/driver"
)

// A provider fails while a host approval callback is in flight. The host then
// cancels and answers. A decision settled during cancellation must not replace
// the provider verdict already observed while the invocation was live.
func TestLateApprovalDoesNotReplaceObservedProviderFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	handlerEntered := make(chan struct{})
	providerFailed := make(chan struct{})
	failure := &driver.RunFailure{Code: driver.FailureAgentError, Message: "provider failed before cancellation"}
	fake := newFakeDriver()
	fake.runFunc = func(ctx context.Context, _ driver.Request, sink driver.EventSink) (driver.Response, error) {
		decisionErr := make(chan error, 1)
		go func() {
			_, err := sink.(driver.DecisionCapableSink).RequestDecision(ctx, driver.DecisionRequest{Kind: driver.HumanDecisionPermission, Prompt: "allow?"})
			decisionErr <- err
		}()
		<-handlerEntered
		_ = sink.EmitStream(driver.StreamPayload{Kind: driver.StreamRunError, Error: failure})
		close(providerFailed)
		return driver.Response{Output: "partial", Failure: failure}, <-decisionErr
	}
	agent := adaptor.New(fake, adaptor.OnApproval(func(_ context.Context, req *adaptor.ApprovalRequest) error {
		close(handlerEntered)
		<-providerFailed
		cancel()
		if err := req.Deny(context.Background(), "no"); err != nil {
			return err
		}
		return nil
	}))
	stream := agent.Send(ctx, "original")
	_, err := stream.Result()
	var business *adaptor.RunError
	if !errors.As(err, &business) || business.Reason != adaptor.ReasonAgentError || business.Message != failure.Message {
		t.Fatalf("early provider failure replaced by late approval: %v", err)
	}
}
