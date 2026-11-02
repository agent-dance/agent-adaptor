package appserver

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
	"testing/synctest"
	"time"

	adaptor "github.com/agent-dance/agent-adaptor/v2"
	"github.com/agent-dance/agent-adaptor/v2/driver"
)

type steerReceiptDriver struct{ ready, release chan struct{} }

func (*steerReceiptDriver) Descriptor() driver.Descriptor {
	return driver.Descriptor{Type: "appserver-steer-core-fixture", Messages: driver.MessageCapability{
		Boundaries: []driver.Boundary{driver.BoundaryImmediate}, Continue: driver.ContinueInRun,
	}}
}
func (*steerReceiptDriver) ValidateConfig(any) error { return nil }

func (d *steerReceiptDriver) Run(ctx context.Context, req driver.Request, sink driver.EventSink) (driver.Response, error) {
	state := newRunState(req.RunID, sink)
	state.ctx, state.messages = ctx, req.Messages
	state.setThread("thread")
	rpc := &messageTestRPC{state: state, disconnected: make(chan struct{})}
	rpc.start = func(int) {
		close(d.ready)
		<-d.release
	}
	rpc.steer = func(_ context.Context, _ TurnSteerParams) (*TurnSteerResponse, error) {
		// A terminal can precede the steer response on the wire. Even if the
		// response comes first, the dispatcher can process completion before
		// the RPC caller wakes up and Acks the successful steer.
		rpc.complete(1, "completed")
		return &TurnSteerResponse{TurnID: "turn-1"}, nil
	}
	err := runTurns(ctx, rpc, state, Options{Prompt: req.Prompt, Messages: req.Messages}, "thread")
	return state.snapshot(Options{}, "thread", "", "", 0, "", false), err
}

func TestSteerReceiptPrecedesTurnEnd(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	d := &steerReceiptDriver{ready: make(chan struct{}), release: make(chan struct{})}
	st := adaptor.New(d).Send(ctx, "initial", adaptor.MessageID("initial"))
	<-d.ready
	if err := st.Send(ctx, "steer", adaptor.MessageID("extra")); err != nil {
		t.Fatal(err)
	}
	close(d.release)
	var ids []string
	for event := range st.Events() {
		if turn, ok := event.(adaptor.TurnEnd); ok {
			ids = turn.MessageIDs
		}
	}
	result, err := st.Result()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(ids, []string{"initial", "extra"}) {
		t.Fatalf("provider accepted extra in turn 1, but TurnEnd.MessageIDs=%v; final messages=%+v", ids, result.Messages())
	}
}

func TestSteerCompletionWaitsForWireReceiptWithoutBlockingReader(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		stream := &messageWireStream{requests: make(chan []byte), responses: make(chan []byte), closed: make(chan struct{})}
		client := NewClient(context.Background(), stream)
		defer client.Close()
		port := newMessageTestPort(driver.MessageItem{ID: "extra", Text: "steer guidance"})
		state, _, sink := setupMessageLoop(port)
		client.SetNotificationHandler(state.onNotification)
		done := make(chan error, 1)
		go func() { done <- runTurns(context.Background(), client, state, Options{Messages: port}, "thread") }()
		var request struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if err := json.Unmarshal(<-stream.requests, &request); err != nil {
			t.Fatal(err)
		}
		if request.Method != "turn/start" {
			t.Fatalf("method=%s", request.Method)
		}
		stream.responses <- []byte(`{"method":"turn/started","params":{"threadId":"thread","turn":{"id":"turn","status":"inProgress"}}}`)
		stream.responses <- []byte(fmt.Sprintf(`{"id":%s,"result":{"turn":{"id":"turn","status":"inProgress"}}}`, request.ID))
		if err := json.Unmarshal(<-stream.requests, &request); err != nil {
			t.Fatal(err)
		}
		if request.Method != "turn/steer" {
			t.Fatalf("method=%s", request.Method)
		}
		stream.responses <- []byte(`{"method":"turn/completed","params":{"threadId":"thread","turn":{"id":"turn","status":"completed"}}}`)
		synctest.Wait()
		if len(completedTurns(sink)) != 0 || len(port.acks) != 0 {
			t.Fatal("completion published before steer receipt")
		}
		if !state.publicClosed {
			t.Fatal("terminal did not close approval/notification ingestion")
		}
		stream.responses <- []byte(fmt.Sprintf(`{"id":%s,"result":{"turnId":"turn"}}`, request.ID))
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		if ack := singleAck(t, port, "extra"); !ack.Delivered {
			t.Fatalf("ack=%+v", ack)
		}
		if len(completedTurns(sink)) != 1 {
			t.Fatal("staged completion lost")
		}
	})
}
