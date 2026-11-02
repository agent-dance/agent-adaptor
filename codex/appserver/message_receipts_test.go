package appserver

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/agent-dance/agent-adaptor/v2/driver"
	"github.com/sourcegraph/jsonrpc2"
)

// A completed successful RPC is authoritative even when cancellation becomes
// visible before the caller inspects its return value.
func TestF2MessageSuccessfulRPCAckSurvivesCancellation(t *testing.T) {
	for _, mechanism := range []string{"steer", "continuation"} {
		t.Run(mechanism, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			p := newMessageTestPort()
			state, rpc, _ := setupMessageLoop(p)
			if mechanism == "steer" {
				p.immediate = []driver.MessageItem{{ID: "accepted", Text: "provider accepted this"}}
				p.signal <- struct{}{}
				rpc.steer = func(context.Context, TurnSteerParams) (*TurnSteerResponse, error) {
					cancel()
					return &TurnSteerResponse{TurnID: "turn-1"}, nil
				}
			} else {
				p.ends = [][]driver.MessageItem{{{ID: "accepted", Text: "provider accepted this", Timing: driver.TimingAfterTurn}}}
				rpc.start = func(n int) {
					if n == 1 {
						rpc.complete(n, "completed")
					} else {
						cancel()
					}
				}
			}
			if err := runTurns(ctx, rpc, state, Options{Messages: p}, "thread"); !errors.Is(err, context.Canceled) {
				t.Fatalf("run error = %v, want cancellation", err)
			}
			ack := singleAck(t, p, "accepted")
			if !ack.Delivered {
				t.Fatalf("successful %s RPC was reported undelivered: %+v", mechanism, ack)
			}
		})
	}
}

func TestF2MessageImmediateClaimIsOneInjection(t *testing.T) {
	p := newMessageTestPort(driver.MessageItem{ID: "a", Text: "first"}, driver.MessageItem{ID: "b", Text: "second"})
	state, rpc, _ := setupMessageLoop(p)
	var requests []TurnSteerParams
	// The second-call fallback allows a regression to fail with a clear
	// assertion instead of leaving the fixture waiting forever.
	rpc.steer = func(_ context.Context, params TurnSteerParams) (*TurnSteerResponse, error) {
		requests = append(requests, params)
		if len(params.Input) == 2 || len(requests) == 2 {
			rpc.complete(1, "completed")
		}
		return &TurnSteerResponse{TurnID: "turn-1"}, nil
	}
	if err := runTurns(context.Background(), rpc, state, Options{Messages: p}, "thread"); err != nil {
		t.Fatal(err)
	}
	if len(requests) != 1 || !reflect.DeepEqual(requests[0].Input, []UserInput{TextInput("first"), TextInput("second")}) {
		t.Fatalf("one boundary claim must be one ordered batch: %+v", requests)
	}
	for _, id := range []string{"a", "b"} {
		if a := singleAck(t, p, id); !a.Delivered || a.Mechanism != "codex.turn_steer" {
			t.Fatalf("Ack(%s)=%+v", id, a)
		}
	}
}

func TestF2MessageBatchSteerOutcomes(t *testing.T) {
	for _, kind := range []string{"confirmed-then-cancel", "rejected", "ambiguous", "failed-then-cancel"} {
		t.Run(kind, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			port := newMessageTestPort(driver.MessageItem{ID: "a", Text: "first"}, driver.MessageItem{ID: "b", Text: "second"})
			state, rpc, _ := setupMessageLoop(port)
			calls := 0
			rpc.start = func(n int) {
				if n > 1 {
					rpc.complete(n, "completed")
				}
			}
			rejected := &jsonrpc2.Error{Code: -32602, Message: "rejected batch"}
			ambiguous := errors.New("connection lost after write")
			rpc.steer = func(_ context.Context, params TurnSteerParams) (*TurnSteerResponse, error) {
				calls++
				if !reflect.DeepEqual(params.Input, []UserInput{TextInput("first"), TextInput("second")}) {
					t.Fatalf("split/reordered batch: %+v", params)
				}
				switch kind {
				case "confirmed-then-cancel":
					cancel()
					return &TurnSteerResponse{TurnID: "turn-1"}, nil
				case "rejected":
					rpc.complete(1, "completed")
					return nil, rejected
				case "ambiguous":
					rpc.complete(1, "completed")
					return nil, ambiguous
				default:
					cancel()
					return nil, ambiguous
				}
			}
			err := runTurns(ctx, rpc, state, Options{Messages: port}, "thread")
			cancelled := kind == "confirmed-then-cancel" || kind == "failed-then-cancel"
			if cancelled && !errors.Is(err, context.Canceled) || !cancelled && err != nil {
				t.Fatalf("run error=%v", err)
			}
			if calls != 1 {
				t.Fatalf("batch used %d RPCs", calls)
			}
			for _, id := range []string{"a", "b"} {
				acks := port.acks[id]
				if cancelled {
					a := singleAck(t, port, id)
					if kind == "confirmed-then-cancel" {
						if !a.Delivered || a.Requeue || a.Err != nil {
							t.Fatalf("confirmed Ack(%s)=%+v", id, a)
						}
					} else if a.Delivered || a.Requeue || !errors.Is(a.Err, context.Canceled) {
						t.Fatalf("cancelled Ack(%s)=%+v", id, a)
					}
				} else {
					if len(acks) != 2 || acks[0].Delivered || !acks[0].Requeue || acks[0].MaybeDelivered != (kind == "ambiguous") || !acks[1].Delivered {
						t.Fatalf("retry Ack(%s)=%+v", id, acks)
					}
					want := error(rejected)
					if kind == "ambiguous" {
						want = ambiguous
					}
					if !errors.Is(acks[0].Err, want) {
						t.Fatalf("batch cause lost: %+v", acks[0])
					}
				}
			}
		})
	}
}

func TestF2MessageMixedInterruptClaimKeepsOrder(t *testing.T) {
	for _, cancelOnStart := range []bool{false, true} {
		t.Run(fmt.Sprint(cancelOnStart), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			port := newMessageTestPort(driver.MessageItem{ID: "a", Text: "first"}, driver.MessageItem{ID: "b", Text: "second", Timing: driver.TimingInterrupt}, driver.MessageItem{ID: "c", Text: "third"})
			state, rpc, _ := setupMessageLoop(port)
			interrupts := 0
			rpc.interrupt = func(_ context.Context, params TurnInterruptParams) error {
				interrupts++
				if params.TurnID == "turn-1" {
					rpc.complete(1, "interrupted")
				}
				return nil
			}
			rpc.steer = func(context.Context, TurnSteerParams) (*TurnSteerResponse, error) {
				t.Fatal("mixed claim was split into steer and restart")
				return nil, nil
			}
			rpc.start = func(n int) {
				if n == 2 {
					if cancelOnStart {
						cancel()
					} else {
						rpc.complete(n, "completed")
					}
				}
			}
			err := runTurns(ctx, rpc, state, Options{Messages: port}, "thread")
			if cancelOnStart && !errors.Is(err, context.Canceled) || !cancelOnStart && err != nil {
				t.Fatal(err)
			}
			wantInterrupts := 1
			if cancelOnStart {
				wantInterrupts++
			} // bounded cancellation cleanup
			if interrupts != wantInterrupts || len(rpc.starts) != 2 || !reflect.DeepEqual(rpc.starts[1].Input, []UserInput{TextInput("first"), TextInput("second"), TextInput("third")}) {
				t.Fatalf("split/reordered restart: interrupts=%d starts=%+v", interrupts, rpc.starts)
			}
			for _, id := range []string{"a", "b", "c"} {
				if a := singleAck(t, port, id); !a.Delivered || a.Mechanism != "codex.turn_interrupt" {
					t.Fatalf("Ack(%s)=%+v", id, a)
				}
			}
		})
	}
}
