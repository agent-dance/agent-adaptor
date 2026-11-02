package adaptor_test

import (
	"context"
	"errors"
	adaptor "github.com/agent-dance/agent-adaptor/v2"
	"github.com/agent-dance/agent-adaptor/v2/driver"
	"github.com/agent-dance/agent-adaptor/v2/memory"
	"github.com/agent-dance/agent-adaptor/v2/threadstore"
	"sync/atomic"
	"testing"
	"time"
)

type failureBlockedFinalize struct {
	threadstore.Store
	count   atomic.Int32
	entered chan struct{}
}

func (s *failureBlockedFinalize) Finalize(ctx context.Context, req threadstore.FinalizeRequest) error {
	if s.count.Add(1) == 2 {
		close(s.entered)
		<-ctx.Done()
		return ctx.Err()
	}
	return s.Store.Finalize(ctx, req)
}

func TestProviderFailureSurvivesCancelDuringGuidancePersistence(t *testing.T) {
	store := &failureBlockedFinalize{Store: memory.NewStore(), entered: make(chan struct{})}
	fake := messageFake(driver.MessageCapability{Boundaries: []driver.Boundary{driver.BoundaryTool}})
	var calls atomic.Int32
	entered := make(chan struct{})
	fake.runFunc = func(ctx context.Context, req driver.Request, sink driver.EventSink) (driver.Response, error) {
		if calls.Add(1) == 1 {
			return messageCheckpoint("seed"), nil
		}
		req.Messages.Window(true)
		close(entered)
		select {
		case <-req.Messages.Signal():
		case <-ctx.Done():
			return driver.Response{}, ctx.Err()
		}
		for _, item := range req.Messages.Claim(driver.BoundaryTool) {
			req.Messages.Ack(item.ID, driver.MessageAck{Delivered: true, Mechanism: "test.tool"})
		}
		failure := &driver.RunFailure{Code: driver.FailureAgentError, Message: "official failure before persistence"}
		_ = sink.EmitStream(driver.StreamPayload{Kind: driver.StreamRunError, Error: failure})
		return driver.Response{Failure: failure, Output: "partial"}, nil
	}
	agent := adaptor.New(fake, adaptor.WithThreadStore(store))
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = agent.Close(ctx)
	})
	th := agent.Thread("failure-order")
	if _, err := th.Send(context.Background(), "seed").Result(); err != nil {
		t.Fatal(err)
	}
	stream := th.Send(context.Background(), "second")
	<-entered
	if err := stream.Send(context.Background(), "preserve this guidance", adaptor.MessageID("guidance")); err != nil {
		t.Fatal(err)
	}
	<-store.entered
	stream.Cancel()
	_, err := stream.Result()
	if !errors.Is(err, adaptor.ErrAgentFailed) {
		t.Fatalf("provider failure observed before cancellation replaced during persistence: %v", err)
	}
}
