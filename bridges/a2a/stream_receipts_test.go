package a2a

import (
	"context"
	"errors"
	"reflect"
	"runtime"
	"sync"
	"testing"
	"time"

	a2aproto "github.com/a2aproject/a2a-go/v2/a2a"
	adaptor "github.com/agent-dance/agent-adaptor/v2"
)

// receiptStream makes delivery immediate, then lets the test complete the
// execution while the first response snapshot is still being consumed.
type receiptStream struct {
	events chan adaptor.Event
	done   chan struct{}
	once   sync.Once
	result *adaptor.Result
	onSend func(context.Context)
}

func (s *receiptStream) Send(ctx context.Context, _ string, _ ...adaptor.MessageOption) error {
	s.events <- adaptor.MessageUpdate{ID: "supplement", State: adaptor.MessageAccepted}
	s.events <- adaptor.MessageUpdate{ID: "supplement", State: adaptor.MessageDelivered, Turn: 1}
	if s.onSend != nil {
		s.onSend(ctx)
	}
	return nil
}

func TestSameTaskDispatchUsesTaskLifetimeAcrossRequestCancellation(t *testing.T) {
	for _, timing := range []string{"before_dispatch", "before_return", "after_return"} {
		t.Run(timing, func(t *testing.T) {
			stream := &receiptStream{
				events: make(chan adaptor.Event, 8), done: make(chan struct{}),
				result: &adaptor.Result{RunID: "run", Turns: 1, Text: "done"},
			}
			t.Cleanup(stream.finish)
			server := NewServer(receiptRunner{stream}, ServerOptions{
				AgentCard: AgentCard{Name: "lifetime", Version: "1", URL: "https://example.test/a2a"},
			})
			initial, err := server.handler.SendMessage(t.Context(), &a2aproto.SendMessageRequest{
				Message: &a2aproto.Message{ID: "initial", Role: a2aproto.MessageRoleUser,
					Parts: a2aproto.ContentParts{a2aproto.NewTextPart("start")}},
				Config: &a2aproto.SendMessageConfig{ReturnImmediately: true},
			})
			if err != nil {
				t.Fatal(err)
			}
			requestCtx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var deliveryCtx context.Context
			stream.onSend = func(ctx context.Context) {
				deliveryCtx = ctx
				if timing == "before_return" {
					cancel()
				}
			}
			if timing == "before_dispatch" {
				cancel()
			}
			_, err = server.handler.SendMessage(requestCtx, &a2aproto.SendMessageRequest{Message: &a2aproto.Message{
				ID: "supplement", TaskID: initial.(*a2aproto.Task).ID, Role: a2aproto.MessageRoleUser,
				Parts: a2aproto.ContentParts{a2aproto.NewTextPart("extra guidance")},
			}})
			if timing == "before_dispatch" {
				if !errors.Is(err, context.Canceled) || deliveryCtx != nil {
					t.Fatalf("canceled request started dispatch: ctx=%v err=%v", deliveryCtx, err)
				}
				return
			}
			if err != nil || deliveryCtx == nil {
				t.Fatalf("dispatch failed: ctx=%v err=%v", deliveryCtx, err)
			}
			cancel()
			if deliveryCtx.Err() != nil || deliveryCtx.Done() != nil {
				t.Fatalf("request cancellation retracted task-owned input: %v", deliveryCtx.Err())
			}
		})
	}
}
func (s *receiptStream) Events() <-chan adaptor.Event { return s.events }
func (s *receiptStream) Result() (*adaptor.Result, error) {
	<-s.done
	return s.result, nil
}
func (s *receiptStream) RunID() string { return s.result.RunID }
func (s *receiptStream) Cancel()       { s.finish() }
func (s *receiptStream) finish() {
	s.once.Do(func() {
		s.events <- adaptor.TurnEnd{Turn: 1, Result: s.result, MessageIDs: []string{"supplement"}}
		s.events <- adaptor.Done{Result: s.result}
		close(s.events)
		close(s.done)
	})
}

type receiptRunner struct{ stream *receiptStream }

func (r receiptRunner) Send(context.Context, string, ...adaptor.CallOption) adaptor.Stream {
	return r.stream
}

func TestSameTaskStreamingKeepsReceiptsWhenTaskFinishesAtFirstSnapshot(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	stream := &receiptStream{
		events: make(chan adaptor.Event, 8), done: make(chan struct{}),
		result: &adaptor.Result{RunID: "run", Turns: 1, Text: "done"},
	}
	t.Cleanup(stream.finish)
	server := NewServer(receiptRunner{stream}, ServerOptions{
		AgentCard: AgentCard{Name: "receipts", Version: "1", URL: "https://example.test/a2a"},
	})
	result, err := server.handler.SendMessage(ctx, &a2aproto.SendMessageRequest{
		Message: &a2aproto.Message{
			ID: "initial", Role: a2aproto.MessageRoleUser, ContextID: "conversation",
			Parts: a2aproto.ContentParts{a2aproto.NewTextPart("start")},
		},
		Config: &a2aproto.SendMessageConfig{ReturnImmediately: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	task := result.(*a2aproto.Task)
	request := &a2aproto.SendMessageRequest{Message: &a2aproto.Message{
		ID: "supplement", TaskID: task.ID, Role: a2aproto.MessageRoleUser,
		Parts: a2aproto.ContentParts{a2aproto.NewTextPart("extra guidance")},
	}}
	var received []string
	first := true
	server.handler.SendStreamingMessage(ctx, request)(func(event a2aproto.Event, err error) bool {
		if err != nil {
			t.Fatal(err)
		}
		if first {
			first = false
			stream.finish()
			// Keep the initial yield suspended until all source events have
			// reached the task store. Subscribing only after that yield loses
			// the receipts as streamed events, even if a terminal snapshot's
			// History happens to retain copies of their messages.
			for {
				latest, err := server.handler.GetTask(ctx, &a2aproto.GetTaskRequest{ID: task.ID})
				if err != nil {
					t.Fatal(err)
				}
				if latest.Status.State.Terminal() {
					break
				}
				if ctx.Err() != nil {
					t.Fatal(ctx.Err())
				}
				runtime.Gosched()
			}
		}
		status, ok := event.(*a2aproto.TaskStatusUpdateEvent)
		if !ok || status.Status.Message == nil {
			return true
		}
		for _, part := range status.Status.Message.Parts {
			data, ok := part.Content.(a2aproto.Data)
			if !ok {
				continue
			}
			decoded, matched, err := DecodeAdapterEventV1(data.Value)
			if err != nil {
				t.Fatal(err)
			}
			if !matched {
				continue
			}
			switch e := decoded.(type) {
			case adaptor.MessageUpdate:
				if e.ID != "supplement" {
					t.Fatalf("unexpected message receipt: %+v", e)
				}
				switch e.State {
				case adaptor.MessageAccepted:
					received = append(received, "accepted")
				case adaptor.MessageDelivered:
					received = append(received, "delivered")
				}
			case adaptor.TurnEnd:
				received = append(received, "turn.end")
			case adaptor.Done:
				received = append(received, "done")
			}
		}
		return true
	})
	if want := []string{"accepted", "delivered", "turn.end", "done"}; !reflect.DeepEqual(received, want) {
		t.Fatalf("same-task streaming lifecycle = %v, want %v", received, want)
	}
}
