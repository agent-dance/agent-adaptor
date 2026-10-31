package appserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"testing/synctest"

	"github.com/agent-dance/agent-adaptor/v2/driver"
	"github.com/sourcegraph/jsonrpc2"
)

type messageTestPort struct {
	mu        sync.Mutex
	signal    chan struct{}
	immediate []driver.MessageItem
	ends      [][]driver.MessageItem
	acks      map[string][]driver.MessageAck
	windows   []bool
	claims    []driver.Boundary
}

func newMessageTestPort(items ...driver.MessageItem) *messageTestPort {
	p := &messageTestPort{signal: make(chan struct{}, 1), immediate: items, acks: make(map[string][]driver.MessageAck)}
	if len(items) > 0 {
		p.signal <- struct{}{}
	}
	return p
}
func (p *messageTestPort) Window(open bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.windows = append(p.windows, open)
}
func (p *messageTestPort) Signal() <-chan struct{} { return p.signal }
func (p *messageTestPort) Claim(b driver.Boundary) []driver.MessageItem {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.claims = append(p.claims, b)
	if b == driver.BoundaryImmediate {
		items := p.immediate
		p.immediate = nil
		return items
	}
	if len(p.ends) == 0 {
		return nil
	}
	items := p.ends[0]
	p.ends = p.ends[1:]
	return items
}
func (p *messageTestPort) Ack(id string, a driver.MessageAck) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.acks[id] = append(p.acks[id], a)
	if a.Requeue {
		p.ends = append(p.ends, []driver.MessageItem{{ID: id, Text: "requeued " + id}})
	}
}

type messageTestRPC struct {
	state        *runState
	starts       []TurnStartParams
	steer        func(context.Context, TurnSteerParams) (*TurnSteerResponse, error)
	interrupt    func(context.Context, TurnInterruptParams) error
	start        func(int)
	disconnected chan struct{}
}

func (r *messageTestRPC) TurnStart(_ context.Context, p TurnStartParams) (*TurnStartResponse, error) {
	r.starts = append(r.starts, p)
	n := len(r.starts)
	r.notify(NotifyTurnStarted, map[string]any{"threadId": "thread", "turn": map[string]any{"id": fmt.Sprintf("turn-%d", n), "status": "inProgress"}})
	if r.start != nil {
		r.start(n)
	}
	var out TurnStartResponse
	_ = json.Unmarshal([]byte(fmt.Sprintf(`{"turn":{"id":"turn-%d","status":"inProgress"}}`, n)), &out)
	return &out, nil
}
func (r *messageTestRPC) TurnSteer(ctx context.Context, p TurnSteerParams) (*TurnSteerResponse, error) {
	return r.steer(ctx, p)
}
func (r *messageTestRPC) TurnInterrupt(ctx context.Context, p TurnInterruptParams) error {
	if r.interrupt != nil {
		return r.interrupt(ctx, p)
	}
	return nil
}
func (r *messageTestRPC) DisconnectNotify() <-chan struct{} { return r.disconnected }
func (r *messageTestRPC) notify(method string, v any) {
	raw, _ := json.Marshal(v)
	r.state.onNotification(method, raw)
}
func (r *messageTestRPC) complete(n int, status string) {
	id := fmt.Sprintf("turn-%d", n)
	r.notify(NotifyItemStarted, map[string]any{"threadId": "thread", "turnId": id, "item": map[string]any{"id": "msg-" + id, "type": "agentMessage", "text": ""}})
	r.notify(NotifyItemCompleted, map[string]any{"threadId": "thread", "turnId": id, "item": map[string]any{"id": "msg-" + id, "type": "agentMessage", "text": fmt.Sprintf("reply-%d", n)}})
	r.notify(NotifyTurnCompleted, map[string]any{"threadId": "thread", "turn": map[string]any{"id": id, "status": status, "usage": map[string]any{"inputTokens": n, "outputTokens": 2 * n, "cachedInputTokens": n}}})
}
func setupMessageLoop(port *messageTestPort) (*runState, *messageTestRPC, *recordingSink) {
	sink := &recordingSink{}
	state := newRunState("run", sink)
	state.messages = port
	state.setThread("thread")
	return state, &messageTestRPC{state: state, disconnected: make(chan struct{})}, sink
}
func singleAck(t *testing.T, p *messageTestPort, id string) driver.MessageAck {
	t.Helper()
	p.mu.Lock()
	defer p.mu.Unlock()
	a := p.acks[id]
	if len(a) != 1 {
		t.Fatalf("Ack(%s) = %+v", id, a)
	}
	return a[0]
}
func completedTurns(s *recordingSink) []*driver.TurnCompleted {
	var out []*driver.TurnCompleted
	for _, e := range s.events {
		if e.Type == driver.RunEventTurnCompleted {
			out = append(out, e.Turn)
		}
	}
	return out
}

func TestMessageSteerOutcomes(t *testing.T) {
	for _, kind := range []string{"success", "mismatch", "timeout", "provider-error", "transport-error"} {
		t.Run(kind, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				p := newMessageTestPort(driver.MessageItem{ID: "one", Text: "verbatim\nmessage"})
				state, rpc, sink := setupMessageLoop(p)
				rpc.start = func(n int) {
					if n > 1 {
						rpc.complete(n, "completed")
					}
				}
				rpc.steer = func(ctx context.Context, p TurnSteerParams) (*TurnSteerResponse, error) {
					if p.ThreadID != "thread" || p.ExpectedTurnID != "turn-1" || !reflect.DeepEqual(p.Input, []UserInput{TextInput("verbatim\nmessage")}) {
						t.Fatalf("steer params: %+v", p)
					}
					var err error
					switch kind {
					case "mismatch":
						err = &jsonrpc2.Error{Code: -32600, Message: "expected turn id does not match active turn"}
					case "provider-error":
						err = &jsonrpc2.Error{Code: -32602, Message: "invalid input"}
					case "timeout":
						<-ctx.Done()
						err = ctx.Err()
					case "transport-error":
						err = errors.New("connection lost after write")
					}
					rpc.complete(1, "completed")
					return &TurnSteerResponse{TurnID: "turn-1"}, err
				}
				if err := runTurns(context.Background(), rpc, state, Options{Messages: p, Prompt: "initial"}, "thread"); err != nil {
					t.Fatal(err)
				}
				if kind == "success" {
					a := singleAck(t, p, "one")
					if !a.Delivered || a.Mechanism != "codex.turn_steer" {
						t.Fatalf("Ack: %+v", a)
					}
				} else {
					a := p.acks["one"]
					if len(a) != 2 || !a[0].Requeue || a[0].Err == nil || !a[1].Delivered || a[1].Mechanism != "codex.turn_start" {
						t.Fatalf("requeued Ack: %+v", a)
					}
					if a[0].MaybeDelivered != (kind == "timeout" || kind == "transport-error") {
						t.Fatalf("ambiguity: %+v", a[0])
					}
				}
				result := state.snapshot(Options{}, "thread", "raw", "stderr", 0, "", false)
				if result.Checkpoint == nil || !result.Checkpoint.Valid {
					t.Fatalf("checkpoint: %+v", result)
				}
				if len(completedTurns(sink)) != len(rpc.starts) {
					t.Fatal("missing turn completion")
				}
			})
		})
	}
}

func TestMessageInterruptRestart(t *testing.T) {
	p := newMessageTestPort(driver.MessageItem{ID: "i1", Text: "first", Timing: driver.TimingInterrupt}, driver.MessageItem{ID: "i2", Text: "second", Timing: driver.TimingInterrupt})
	state, rpc, sink := setupMessageLoop(p)
	rpc.start = func(n int) {
		if n == 2 {
			rpc.complete(n, "completed")
		}
	}
	rpc.interrupt = func(_ context.Context, p TurnInterruptParams) error {
		if p.ThreadID != "thread" || p.TurnID != "turn-1" {
			t.Fatalf("interrupt params: %+v", p)
		}
		if len(rpc.starts) != 1 {
			t.Fatal("restart before completion")
		}
		rpc.complete(1, "interrupted")
		return nil
	}
	schema := &driver.OutputSchema{SchemaJSON: []byte(`{"type":"string"}`)}
	if err := runTurns(context.Background(), rpc, state, Options{Messages: p, OutputSchema: schema}, "thread"); err != nil {
		t.Fatal(err)
	}
	if len(rpc.starts) != 2 || !reflect.DeepEqual(rpc.starts[1].Input, []UserInput{TextInput("first"), TextInput("second")}) {
		t.Fatalf("starts: %+v", rpc.starts)
	}
	for _, start := range rpc.starts {
		if string(start.OutputSchema) != string(schema.SchemaJSON) {
			t.Fatal("schema missing on restart")
		}
	}
	for _, id := range []string{"i1", "i2"} {
		a := singleAck(t, p, id)
		if !a.Delivered || a.Mechanism != "codex.turn_interrupt" {
			t.Fatalf("Ack: %+v", a)
		}
	}
	turns := completedTurns(sink)
	if len(turns) != 2 || !turns[0].Interrupted || turns[1].Interrupted {
		t.Fatalf("turns: %+v", turns)
	}
	result := state.snapshot(Options{}, "thread", "all turns", "", 0, "", false)
	if result.Output != "reply-2" || result.Failure != nil || result.Usage.InputTokens != 3 || result.Checkpoint == nil {
		t.Fatalf("result: %+v", result)
	}
}

func TestMessageThreeTurnContinuation(t *testing.T) {
	p := newMessageTestPort()
	p.ends = [][]driver.MessageItem{{{ID: "a", Text: "A", Timing: driver.TimingAfterTurn}, {ID: "b", Text: "B", Timing: driver.TimingAfterTurn}}, {{ID: "c", Text: "C", Timing: driver.TimingAfterTurn}}}
	state, rpc, sink := setupMessageLoop(p)
	rpc.start = func(n int) { rpc.complete(n, "completed") }
	if err := runTurns(context.Background(), rpc, state, Options{Messages: p}, "thread"); err != nil {
		t.Fatal(err)
	}
	result := state.snapshot(Options{}, "thread", "all turns", "all stderr", 0, "", false)
	state.finishPublicResult(result, nil)
	if result.Output != "reply-3" || result.Usage.InputTokens != 6 || result.Usage.OutputTokens != 12 || result.Usage.CachedInputTokens != 6 {
		t.Fatalf("aggregate: %+v", result)
	}
	turns := completedTurns(sink)
	if len(turns) != 3 {
		t.Fatalf("turns=%d", len(turns))
	}
	for i, turn := range turns {
		if turn.Index != i || turn.Text != fmt.Sprintf("reply-%d", i+1) || turn.CompletedAt.IsZero() || turn.Usage.InputTokens != i+1 {
			t.Fatalf("turn[%d]: %+v", i, turn)
		}
	}
	if !reflect.DeepEqual(p.windows, []bool{false, true, false, true, false, true, false}) {
		t.Fatalf("windows: %v", p.windows)
	}
	if !reflect.DeepEqual(rpc.starts[1].Input, []UserInput{TextInput("A"), TextInput("B")}) {
		t.Fatalf("combined inputs: %+v", rpc.starts[1])
	}
	for _, id := range []string{"a", "b", "c"} {
		a := singleAck(t, p, id)
		if !a.Delivered || a.Mechanism != "codex.turn_start" {
			t.Fatalf("Ack: %+v", a)
		}
	}
	if len(result.Transcript) != 7 {
		t.Fatalf("transcript: %+v", result.Transcript)
	}
	verifyAppserverContracts(t, sink, result)
}

func TestMessageCancellationAcknowledgesEveryClaim(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p := newMessageTestPort(driver.MessageItem{ID: "a", Text: "A"}, driver.MessageItem{ID: "b", Text: "B"})
	state, rpc, _ := setupMessageLoop(p)
	interrupts := 0
	rpc.steer = func(ctx context.Context, _ TurnSteerParams) (*TurnSteerResponse, error) {
		cancel()
		<-ctx.Done()
		return nil, ctx.Err()
	}
	rpc.interrupt = func(context.Context, TurnInterruptParams) error { interrupts++; return nil }
	if err := runTurns(ctx, rpc, state, Options{Messages: p}, "thread"); !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v", err)
	}
	for _, id := range []string{"a", "b"} {
		a := singleAck(t, p, id)
		if !errors.Is(a.Err, context.Canceled) || a.Requeue || a.Delivered {
			t.Fatalf("Ack: %+v", a)
		}
	}
	if interrupts != 1 {
		t.Fatalf("cancellation interrupts=%d", interrupts)
	}
	if !reflect.DeepEqual(p.windows, []bool{false, true, false}) {
		t.Fatalf("windows: %v", p.windows)
	}
}

func TestMessageSteerFailureStillAcceptsInterrupt(t *testing.T) {
	p := newMessageTestPort(driver.MessageItem{ID: "soon", Text: "soon"})
	state, rpc, sink := setupMessageLoop(p)
	rpc.steer = func(context.Context, TurnSteerParams) (*TurnSteerResponse, error) {
		p.mu.Lock()
		p.immediate = append(p.immediate, driver.MessageItem{ID: "interrupt", Text: "interrupt", Timing: driver.TimingInterrupt})
		p.mu.Unlock()
		p.signal <- struct{}{}
		return nil, &jsonrpc2.Error{Code: -32600, Message: "boundary rejected"}
	}
	rpc.interrupt = func(context.Context, TurnInterruptParams) error { rpc.complete(1, "interrupted"); return nil }
	rpc.start = func(n int) {
		if n > 1 {
			rpc.complete(n, "completed")
		}
	}
	if err := runTurns(context.Background(), rpc, state, Options{Messages: p}, "thread"); err != nil {
		t.Fatal(err)
	}
	if len(rpc.starts) != 3 {
		t.Fatalf("starts=%d", len(rpc.starts))
	}
	if a := singleAck(t, p, "interrupt"); !a.Delivered || a.Mechanism != "codex.turn_interrupt" {
		t.Fatalf("Ack=%+v", a)
	}
	if a := p.acks["soon"]; len(a) != 2 || !a[0].Requeue || !a[1].Delivered {
		t.Fatalf("Ack=%+v", a)
	}
	if turns := completedTurns(sink); !turns[0].Interrupted {
		t.Fatal("interruption not recorded")
	}
}

type messageWireStream struct {
	requests  chan []byte
	responses chan []byte
	closed    chan struct{}
	once      sync.Once
}

func (s *messageWireStream) ReadObject(v any) error {
	select {
	case raw := <-s.responses:
		return json.Unmarshal(raw, v)
	case <-s.closed:
		return errors.New("stream closed")
	}
}
func (s *messageWireStream) WriteObject(v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	select {
	case s.requests <- raw:
		return nil
	case <-s.closed:
		return errors.New("stream closed")
	}
}
func (s *messageWireStream) Close() error { s.once.Do(func() { close(s.closed) }); return nil }

func TestTurnSteerWireShapes(t *testing.T) {
	for _, kind := range []string{"success", "no-active-turn", "mismatch", "invalid-response"} {
		t.Run(kind, func(t *testing.T) {
			stream := &messageWireStream{requests: make(chan []byte), responses: make(chan []byte), closed: make(chan struct{})}
			client := NewClient(context.Background(), stream)
			defer client.Close()
			done := make(chan error, 1)
			go func() {
				_, err := client.TurnSteer(context.Background(), TurnSteerParams{ThreadID: "thread", ExpectedTurnID: "turn", Input: []UserInput{TextInput("verbatim")}})
				done <- err
			}()
			var req struct {
				ID     json.RawMessage            `json:"id"`
				Method string                     `json:"method"`
				Params map[string]json.RawMessage `json:"params"`
			}
			if err := json.Unmarshal(<-stream.requests, &req); err != nil {
				t.Fatal(err)
			}
			if req.Method != "turn/steer" || len(req.Params) != 3 || string(req.Params["threadId"]) != `"thread"` || string(req.Params["expectedTurnId"]) != `"turn"` {
				t.Fatalf("request=%+v", req)
			}
			var input []map[string]any
			if err := json.Unmarshal(req.Params["input"], &input); err != nil {
				t.Fatal(err)
			}
			if len(input) != 1 || input[0]["type"] != "text" || input[0]["text"] != "verbatim" {
				t.Fatalf("input=%+v", input)
			}
			reply := `"result":{"turnId":"turn"}`
			switch kind {
			case "no-active-turn":
				reply = `"error":{"code":-32600,"message":"no active turn to steer"}`
			case "mismatch":
				reply = `"error":{"code":-32600,"message":"expected turn id does not match active turn"}`
			case "invalid-response":
				reply = `"result":{"turnId":"wrong-turn"}`
			}
			stream.responses <- []byte(fmt.Sprintf(`{"id":%s,%s}`, req.ID, reply))
			err := <-done
			if kind == "success" {
				if err != nil {
					t.Fatal(err)
				}
			} else if kind == "invalid-response" {
				if err == nil {
					t.Fatal("invalid response accepted")
				}
			} else {
				var providerErr *jsonrpc2.Error
				if !errors.As(err, &providerErr) || providerErr.Code != -32600 {
					t.Fatalf("provider error=%v", err)
				}
			}
		})
	}
}

func TestMessageUsageExcludesHistoryAndSumsInferenceDeltas(t *testing.T) {
	p := newMessageTestPort()
	p.ends = [][]driver.MessageItem{{{ID: "next", Text: "next"}}}
	state, rpc, sink := setupMessageLoop(p)
	rpc.start = func(n int) {
		update := func(total, last int) {
			rpc.notify(NotifyThreadTokenUsageUpdated, map[string]any{"threadId": "thread", "turnId": fmt.Sprintf("turn-%d", n), "tokenUsage": map[string]any{"total": map[string]any{"inputTokens": total, "outputTokens": total}, "last": map[string]any{"inputTokens": last, "outputTokens": last}}})
		}
		if n == 1 {
			update(102, 2)
			update(105, 3)
			update(105, 3)
		} else {
			update(109, 4)
		}
		rpc.notify(NotifyTurnCompleted, map[string]any{"threadId": "thread", "turn": map[string]any{"id": fmt.Sprintf("turn-%d", n), "status": "completed"}})
	}
	if err := runTurns(context.Background(), rpc, state, Options{Messages: p}, "thread"); err != nil {
		t.Fatal(err)
	}
	turns := completedTurns(sink)
	if len(turns) != 2 || turns[0].Usage.InputTokens != 5 || turns[1].Usage.InputTokens != 4 {
		t.Fatalf("turn usage: %+v", turns)
	}
	if usage := state.snapshot(Options{}, "thread", "", "", 0, "", false).Usage; usage.InputTokens != 9 || usage.OutputTokens != 9 {
		t.Fatalf("usage=%+v", usage)
	}
}

func TestMessageFailedFinalTurnDoesNotCheckpoint(t *testing.T) {
	p := newMessageTestPort()
	p.ends = [][]driver.MessageItem{{{ID: "next", Text: "next"}}, {{ID: "unclaimed", Text: "unclaimed"}}}
	state, rpc, sink := setupMessageLoop(p)
	rpc.start = func(n int) {
		status := "completed"
		if n == 2 {
			status = "failed"
		}
		rpc.complete(n, status)
	}
	if err := runTurns(context.Background(), rpc, state, Options{Messages: p}, "thread"); err != nil {
		t.Fatal(err)
	}
	result := state.snapshot(Options{}, "thread", "all raw", "", 0, "", false)
	if result.Checkpoint != nil || result.Failure == nil || result.Output != "reply-2" || result.Usage.InputTokens != 3 {
		t.Fatalf("result=%+v", result)
	}
	if len(completedTurns(sink)) != 2 || len(p.ends) != 1 {
		t.Fatal("continued after provider failure")
	}
}

func TestMessageInterruptRacesNaturalCompletion(t *testing.T) {
	p := newMessageTestPort(driver.MessageItem{ID: "i", Text: "restart", Timing: driver.TimingInterrupt})
	state, rpc, sink := setupMessageLoop(p)
	rpc.start = func(n int) {
		if n > 1 {
			rpc.complete(n, "completed")
		}
	}
	rpc.interrupt = func(context.Context, TurnInterruptParams) error { rpc.complete(1, "completed"); return nil }
	if err := runTurns(context.Background(), rpc, state, Options{Messages: p}, "thread"); err != nil {
		t.Fatal(err)
	}
	turns := completedTurns(sink)
	if len(turns) != 2 || turns[0].Interrupted {
		t.Fatalf("natural completion marked interrupted: %+v", turns)
	}
	if ack := singleAck(t, p, "i"); !ack.Delivered || ack.Mechanism != "codex.turn_interrupt" {
		t.Fatalf("Ack=%+v", ack)
	}
}

func TestMessageInterruptFailureAcknowledgesClaims(t *testing.T) {
	p := newMessageTestPort(driver.MessageItem{ID: "i", Text: "restart", Timing: driver.TimingInterrupt})
	state, rpc, _ := setupMessageLoop(p)
	providerErr := &jsonrpc2.Error{Code: -32600, Message: "interrupt rejected"}
	rpc.interrupt = func(context.Context, TurnInterruptParams) error { return providerErr }
	if err := runTurns(context.Background(), rpc, state, Options{Messages: p}, "thread"); !errors.Is(err, providerErr) {
		t.Fatalf("err=%v", err)
	}
	if ack := singleAck(t, p, "i"); !errors.Is(ack.Err, providerErr) || ack.Requeue || ack.Delivered {
		t.Fatalf("Ack=%+v", ack)
	}
	if result := state.snapshot(Options{}, "thread", "", "", 0, "", false); result.Checkpoint != nil {
		t.Fatal("checkpoint after failed interrupt")
	}
}
