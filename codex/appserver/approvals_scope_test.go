package appserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"testing/synctest"

	"github.com/agent-dance/agent-adaptor/v2/driver"
	"github.com/sourcegraph/jsonrpc2"
)

func TestApprovalRejectsStaleTurnDuringRebind(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		// RunTurn installs the new execution's request handler before TurnStart
		// supplies its turn ID. A late request from the resident's previous turn
		// must not become an approval issued by this execution.
		state := newRunState("new-execution", &recordingSink{})
		state.setThread("thread")
		sink := &decisionFixtureSink{response: driver.DecisionResponse{Result: driver.DecisionApproved}}
		raw := json.RawMessage(`{"threadId":"thread","turnId":"previous-turn","itemId":"old-command","reason":"Run old command?"}`)
		settled := make(chan struct{})
		var result any
		var rpcErr *jsonrpc2.Error
		var err error
		go func() {
			defer close(settled)
			result, rpcErr, err = approvalResponse(context.Background(), "new-execution", sink, &jsonrpc2.Request{
				ID: jsonrpc2.ID{Num: 42}, Method: "item/commandExecution/requestApproval", Params: &raw,
			}, state)
		}()
		synctest.Wait()
		if sink.request.RunID != "" {
			t.Fatal("unbound request reached host")
		}
		state.setTurn("current-turn")
		<-settled
		if rpcErr == nil || sink.request.RunID != "" {
			t.Fatalf("stale resident request reached new execution: host request=%+v reply=%+v rpcErr=%v err=%v", sink.request, result, rpcErr, err)
		}
	})
}

func TestApprovalRejectsCompletedTurn(t *testing.T) {
	state := newRunState("execution", &recordingSink{})
	state.setThread("thread")
	state.setTurn("turn")
	state.onNotification(NotifyTurnCompleted, json.RawMessage(`{"threadId":"thread","turn":{"id":"turn","status":"completed"}}`))
	sink := &decisionFixtureSink{response: driver.DecisionResponse{Result: driver.DecisionApproved}}
	raw := json.RawMessage(`{"threadId":"thread","turnId":"turn","itemId":"late-command"}`)
	result, rpcErr, err := approvalResponse(context.Background(), "execution", sink, &jsonrpc2.Request{
		ID: jsonrpc2.ID{Num: 43}, Method: "item/commandExecution/requestApproval", Params: &raw,
	}, state)
	if rpcErr == nil || sink.request.RunID != "" {
		t.Fatalf("completed-turn request reached host: request=%+v reply=%+v rpcErr=%v err=%v", sink.request, result, rpcErr, err)
	}
}

func TestApprovalWaitsForBoundLiveTurn(t *testing.T) {
	for _, outcome := range []string{"live", "completed", "cancelled"} {
		t.Run(outcome, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				state := newRunState("execution", &recordingSink{})
				sink := &decisionFixtureSink{response: driver.DecisionResponse{Result: driver.DecisionApproved}}
				raw := json.RawMessage(`{"threadId":"thread","turnId":"turn","itemId":"command"}`)
				settled := make(chan struct{})
				var result any
				var rpcErr *jsonrpc2.Error
				var err error
				go func() {
					defer close(settled)
					result, rpcErr, err = approvalResponse(ctx, "execution", sink, &jsonrpc2.Request{ID: jsonrpc2.ID{Num: 44}, Method: "item/commandExecution/requestApproval", Params: &raw}, state)
				}()
				synctest.Wait()
				if sink.request.RunID != "" {
					t.Fatal("approval reached host before thread binding")
				}
				state.setThread("thread")
				synctest.Wait()
				if sink.request.RunID != "" {
					t.Fatal("approval reached host before turn binding")
				}
				switch outcome {
				case "completed":
					state.onNotification(NotifyTurnCompleted, json.RawMessage(`{"threadId":"thread","turn":{"id":"turn","status":"completed"}}`))
				case "cancelled":
					cancel()
				}
				state.setTurn("turn")
				<-settled
				if outcome == "live" {
					if rpcErr != nil || err != nil || sink.request.RunID != "execution" || result.(map[string]any)["decision"] != "accept" {
						t.Fatalf("request=%+v result=%+v rpcErr=%v err=%v", sink.request, result, rpcErr, err)
					}
				} else if rpcErr == nil || sink.request.RunID != "" {
					t.Fatalf("inactive turn reached host: %+v, rpcErr=%v", sink.request, rpcErr)
				}
				if outcome == "cancelled" && !errors.Is(err, context.Canceled) {
					t.Fatalf("cancel error=%v", err)
				}
			})
		})
	}
}

func TestApprovalBeforeTurnStartReplyKeepsRPCReaderAvailable(t *testing.T) {
	for _, requestedTurn := range []string{"turn", "previous-turn"} {
		t.Run(requestedTurn, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				stream := &messageWireStream{requests: make(chan []byte), responses: make(chan []byte), closed: make(chan struct{})}
				client := NewClient(context.Background(), stream)
				defer client.Close()
				sink := &decisionFixtureSink{response: driver.DecisionResponse{Result: driver.DecisionApproved}}
				state := newRunState("execution", sink)
				state.setThread("thread")
				client.SetNotificationHandler(state.onNotification)
				stop := client.handleRunRequests(context.Background(), "execution", sink, state)
				defer stop()
				done := make(chan error, 1)
				go func() { done <- runTurns(context.Background(), client, state, Options{}, "thread") }()
				var start struct {
					ID json.RawMessage `json:"id"`
				}
				if err := json.Unmarshal(<-stream.requests, &start); err != nil {
					t.Fatal(err)
				}
				stream.responses <- []byte(fmt.Sprintf(`{"id":42,"method":"item/commandExecution/requestApproval","params":{"threadId":"thread","turnId":%q,"itemId":"command"}}`, requestedTurn))
				synctest.Wait()
				if sink.request.RunID != "" {
					t.Fatal("early approval surfaced before RPC identity")
				}
				stream.responses <- []byte(`{"method":"turn/started","params":{"threadId":"thread","turn":{"id":"turn","status":"inProgress"}}}`)
				stream.responses <- []byte(fmt.Sprintf(`{"id":%s,"result":{"turn":{"id":"turn","status":"inProgress"}}}`, start.ID))
				var reply struct {
					Result map[string]any  `json:"result"`
					Error  *jsonrpc2.Error `json:"error"`
				}
				if err := json.Unmarshal(<-stream.requests, &reply); err != nil {
					t.Fatal(err)
				}
				if requestedTurn == "turn" {
					if reply.Error != nil || reply.Result["decision"] != "accept" || sink.request.RunID != "execution" {
						t.Fatalf("reply=%+v host=%+v", reply, sink.request)
					}
				} else if reply.Error == nil || sink.request.RunID != "" {
					t.Fatalf("stale approval reached host: reply=%+v host=%+v", reply, sink.request)
				}
				stream.responses <- []byte(`{"method":"turn/completed","params":{"threadId":"thread","turn":{"id":"turn","status":"completed"}}}`)
				if err := <-done; err != nil {
					t.Fatal(err)
				}
			})
		})
	}
}
