package appserver

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/agent-dance/agent-adaptor/v2/driver"
	"github.com/sourcegraph/jsonrpc2"
)

type decisionFixtureSink struct {
	recordingSink
	request  driver.DecisionRequest
	response driver.DecisionResponse
	err      error
}

func (s *decisionFixtureSink) RequestDecision(_ context.Context, request driver.DecisionRequest) (driver.DecisionResponse, error) {
	s.request = request
	return s.response, s.err
}

func TestF2ApprovalRequestProtocolMapping(t *testing.T) {
	for _, method := range []string{"mcpServer/elicitation/request", "item/commandExecution/requestApproval", "item/fileChange/requestApproval"} {
		for _, decision := range []driver.DecisionResult{driver.DecisionApproved, driver.DecisionRejected, driver.DecisionAborted} {
			t.Run(method+"/"+string(decision), func(t *testing.T) {
				state := newRunState("run", &recordingSink{})
				state.setThread("thread")
				state.setTurn("turn")
				raw := json.RawMessage(`{"threadId":"thread","turnId":"turn","itemId":"call","serverName":"tools","mode":"form","message":"Allow?","requestedSchema":{"type":"object","properties":{}}}`)
				sink := &decisionFixtureSink{response: driver.DecisionResponse{Result: decision}}
				if decision == driver.DecisionAborted {
					sink.err = context.Canceled
				}
				result, rpcErr, err := approvalResponse(context.Background(), "run", sink, &jsonrpc2.Request{ID: jsonrpc2.ID{Num: 42}, Method: method, Params: &raw}, state)
				if rpcErr != nil || !errors.Is(err, sink.err) {
					t.Fatalf("rpcErr=%v err=%v", rpcErr, err)
				}
				expected := "decline"
				if decision == driver.DecisionApproved {
					expected = "accept"
				}
				if decision == driver.DecisionAborted {
					expected = "cancel"
				}
				key := "decision"
				if method == "mcpServer/elicitation/request" {
					key = "action"
				}
				if result.(map[string]any)[key] != expected || sink.request.ToolCallID != "call" || sink.request.ThreadID != "thread" || sink.request.RunID != "run" || sink.request.RequestID != "42" {
					t.Fatalf("result=%+v request=%+v", result, sink.request)
				}
			})
		}
	}
}

func TestF2ApprovalRejectsForeignScopeAndInputForms(t *testing.T) {
	state := newRunState("run", &recordingSink{})
	state.setThread("thread")
	state.setTurn("turn")
	for _, raw := range []string{
		`{"threadId":"foreign","turnId":"turn","serverName":"tools","mode":"form","requestedSchema":{"type":"object","properties":{}}}`,
		`{"threadId":"thread","turnId":"old-turn","serverName":"tools","mode":"form","requestedSchema":{"type":"object","properties":{}}}`,
		`{"threadId":"thread","turnId":"turn","serverName":"tools","mode":"form","requestedSchema":{"type":"object","properties":{"name":{"type":"string"}}}}`,
	} {
		params := json.RawMessage(raw)
		sink := &decisionFixtureSink{response: driver.DecisionResponse{Result: driver.DecisionApproved}}
		_, rpcErr, _ := approvalResponse(context.Background(), "run", sink, &jsonrpc2.Request{ID: jsonrpc2.ID{Num: 42}, Method: "mcpServer/elicitation/request", Params: &params}, state)
		if rpcErr == nil || sink.request.RunID != "" {
			t.Fatalf("invalid request reached host: %+v, %v", sink.request, rpcErr)
		}
	}
}

type blockedApprovalReplyStream struct {
	messageWireStream
	writeStarted chan struct{}
	writeOnce    sync.Once
}

func (s *blockedApprovalReplyStream) WriteObject(value any) error {
	s.writeOnce.Do(func() { close(s.writeStarted) })
	return s.messageWireStream.WriteObject(value)
}

func TestApprovalShutdownRetiresBlockedReplyTransport(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		t.Run(map[bool]string{false: "completion", true: "cancel"}[cancelled], func(t *testing.T) {
			stream := &blockedApprovalReplyStream{
				messageWireStream: messageWireStream{
					requests: make(chan []byte), responses: make(chan []byte), closed: make(chan struct{}),
				},
				writeStarted: make(chan struct{}),
			}
			client := NewClient(context.Background(), stream)
			defer client.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			sink := &decisionFixtureSink{response: driver.DecisionResponse{Result: driver.DecisionApproved}}
			state := newRunState("run", sink)
			state.setThread("thread")
			state.setTurn("turn")
			stop := client.handleRunRequests(ctx, "run", sink, state)
			raw := json.RawMessage(`{"threadId":"thread","turnId":"turn","itemId":"call","serverName":"tools","mode":"form","requestedSchema":{"type":"object","properties":{}}}`)
			(&connHandler{client: client}).Handle(context.Background(), client.conn, &jsonrpc2.Request{
				ID: jsonrpc2.ID{Num: 42}, Method: "mcpServer/elicitation/request", Params: &raw,
			})
			select {
			case <-stream.writeStarted:
			case <-time.After(time.Second):
				t.Fatal("approval reply did not reach the blocked peer")
			}
			if cancelled {
				cancel()
			}
			settled := make(chan struct{})
			go func() {
				stop()
				close(settled)
			}()
			select {
			case <-settled:
			case <-time.After(time.Second):
				t.Fatal("blocked approval reply kept execution shutdown waiting")
			}
			if !client.closed.Load() || state.executionError() == nil {
				t.Fatal("unsettled approval transport was retained or its reply failure was hidden")
			}
			stop()
		})
	}
}

type awaitingApprovalSink struct {
	recordingSink
	entered chan struct{}
}

func (s *awaitingApprovalSink) RequestDecision(ctx context.Context, _ driver.DecisionRequest) (driver.DecisionResponse, error) {
	close(s.entered)
	<-ctx.Done()
	return driver.DecisionResponse{Result: driver.DecisionAborted}, ctx.Err()
}

func TestApprovalCompletionDoesNotFabricateHostCancellation(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		t.Run(map[bool]string{false: "completion", true: "cancel"}[cancelled], func(t *testing.T) {
			stream := &messageWireStream{
				requests: make(chan []byte, 1), responses: make(chan []byte), closed: make(chan struct{}),
			}
			client := NewClient(context.Background(), stream)
			defer client.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			sink := &awaitingApprovalSink{entered: make(chan struct{})}
			state := newRunState("run", sink)
			state.setThread("thread")
			state.setTurn("turn")
			stop := client.handleRunRequests(ctx, "run", sink, state)
			raw := json.RawMessage(`{"threadId":"thread","turnId":"turn","itemId":"call","serverName":"tools","mode":"form","requestedSchema":{"type":"object","properties":{}}}`)
			(&connHandler{client: client}).Handle(context.Background(), client.conn, &jsonrpc2.Request{
				ID: jsonrpc2.ID{Num: 42}, Method: "mcpServer/elicitation/request", Params: &raw,
			})
			select {
			case <-sink.entered:
			case <-time.After(time.Second):
				t.Fatal("approval did not reach decision sink")
			}
			if cancelled {
				cancel()
			}
			stop()
			err := state.executionError()
			if err == nil || errors.Is(err, context.Canceled) != cancelled {
				t.Fatalf("host cancelled=%v, execution error=%v", cancelled, err)
			}
		})
	}
}
