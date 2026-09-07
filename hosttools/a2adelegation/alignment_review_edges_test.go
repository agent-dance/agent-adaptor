package a2adelegation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	adaptor "github.com/agent-dance/agent-adaptor"
	clienta2a "github.com/agent-dance/agent-adaptor/clients/a2a"
)

func TestAlignmentReviewT18RelayLossPreservesDiagnostics(t *testing.T) {
	for _, which := range []string{"domain_overflow", "depth_overflow"} {
		t.Run(which, func(t *testing.T) {
			const at = "2026-09-07T00:00:00Z"
			invocation := "call"
			meta := map[string]any{"run_id": "child-run", "sequence": uint64(7), "time": at}
			wantReason := "invalid_payload"
			if which == "domain_overflow" {
				invocation = strings.Repeat("x", 2048)
			} else {
				var upstream map[string]any
				for i := 0; i < 8; i++ {
					node := map[string]any{"run_id": fmt.Sprintf("up-%d", i), "sequence": uint64(99), "timestamp": at}
					if upstream != nil {
						node["upstream"] = upstream
					}
					upstream = node
				}
				meta["source"] = upstream
				wantReason = "relay_depth_exceeded"
			}
			payload := map[string]any{"schema": "adapter.stream.v1", "event": map[string]any{
				"kind": "capability.invocation", "meta": meta,
				"capability": map[string]any{"invocation_id": invocation, "kind": "mcp", "key": "catalog", "operation": "search", "phase": "started", "source": "provider", "evidence": "provider_protocol", "occurred_at": at},
			}}
			decoded, matched, err := (adapterStreamStatusDecoder{}).DecodeStatusPart(payload)
			if err != nil || !matched || len(decoded) != 1 {
				t.Fatalf("legal inbound fixture rejected: matched=%v decoded=%d err=%v", matched, len(decoded), err)
			}
			m := newEventMapper(DelegationEvent{RunID: "leader", DelegationID: "D", AgentKey: "agent", Protocol: ProtocolA2A})
			got := m.statusPartEvents("task", "ctx", clienta2a.Message{ID: "message", Parts: []clienta2a.Part{{Kind: clienta2a.PartData, Data: payload}}})
			if len(got) != 1 || got[0].Kind != DelegationStreamDropped || got[0].Capability != nil || got[0].Source != nil {
				t.Fatalf("expected explicit loss, got %#v", got)
			}
			if got[0].Raw["reason"] != wantReason || got[0].Raw["dropped_count"] != 1 {
				t.Fatalf("safe loss diagnostics overwritten: want reason=%q count=1; got %#v", wantReason, got[0].Raw)
			}
		})
	}
}

type reviewT18Runner struct {
	err     error
	result  *adaptor.Result
	streams atomic.Int32
	runs    atomic.Int32
}

func (r *reviewT18Runner) Run(context.Context, string, ...adaptor.CallOption) (*adaptor.Result, error) {
	r.runs.Add(1)
	return r.result, r.err
}
func (r *reviewT18Runner) Stream(context.Context, string, ...adaptor.CallOption) adaptor.Stream {
	r.streams.Add(1)
	ch := make(chan adaptor.Event, 1)
	ch <- adaptor.WithEventMeta(adaptor.RunFinished{Failed: true, Reason: adaptor.ReasonActiveExecutionTimeout}, adaptor.EventMeta{RunID: "review-child", Sequence: 1, Time: time.Date(2026, 9, 7, 0, 0, 0, 0, time.UTC)})
	close(ch)
	return &reviewT18Stream{events: ch, result: r.result, err: r.err}
}

type reviewT18Stream struct {
	events <-chan adaptor.Event
	result *adaptor.Result
	err    error
}

func (s *reviewT18Stream) Events() <-chan adaptor.Event     { return s.events }
func (s *reviewT18Stream) Result() (*adaptor.Result, error) { return s.result, s.err }
func (s *reviewT18Stream) RunID() string                    { return "review-child" }
func (s *reviewT18Stream) Cancel()                          {}

func TestAlignmentReviewT18LocalCarrierOwnLimit(t *testing.T) {
	for _, order := range []string{"parent_first", "carrier_first"} {
		for _, mode := range []string{"Send", "SendStream"} {
			t.Run(order+"/"+mode, func(t *testing.T) {
				parent := &adaptor.ActiveExecutionTimeoutError{Limit: 777 * time.Second}
				own := &adaptor.ActiveExecutionTimeoutError{Limit: 100 * time.Millisecond}
				partial := &adaptor.Result{Text: "partial assistant", Summary: "partial summary"}
				carrier := &adaptor.RunError{Reason: adaptor.ReasonActiveExecutionTimeout, Cause: own, Result: partial}
				joined := errors.Join(parent, carrier)
				if order == "carrier_first" {
					joined = errors.Join(carrier, parent)
				}
				runner := &reviewT18Runner{err: joined}
				c := newLocalClient("member", runner)
				req := clienta2a.SendRequest{Message: clienta2a.Message{Parts: []clienta2a.Part{{Kind: clienta2a.PartText, Text: "prompt"}}}}
				var task clienta2a.Task
				if mode == "Send" {
					var err error
					task, err = c.Send(context.Background(), req)
					if err != nil {
						t.Fatal(err)
					}
				} else {
					s, err := c.SendStream(context.Background(), req)
					if err != nil {
						t.Fatal(err)
					}
					defer s.Close()
					for {
						ev, err := s.Recv()
						if err == io.EOF {
							break
						}
						if err != nil {
							t.Fatal(err)
						}
						if ev.Task != nil {
							task = *ev.Task
						}
					}
				}
				if runner.streams.Load() != 1 || runner.runs.Load() != 0 {
					t.Fatalf("execution counts: Stream=%d Run=%d", runner.streams.Load(), runner.runs.Load())
				}
				failure := c.taskFailure(task.ID)
				if failure == nil || failure.Code != "active_execution_timeout" {
					t.Fatalf("failure=%#v", failure)
				}
				if !errors.Is(failure, parent) || !errors.Is(failure, own) {
					t.Fatal("original cause graph lost")
				}
				if len(task.Messages) != 1 || task.Messages[0].Parts[0].Text != partial.Text || len(task.Artifacts) != 1 {
					t.Fatalf("partial output lost: %#v", task)
				}
				if failure.Metadata["limit_ms"] != int64(100) {
					t.Fatalf("carrier own budget must win regardless of outer join order: limit_ms=%v want 100", failure.Metadata["limit_ms"])
				}
			})
		}
	}
}

func TestAlignmentReviewT18CarrierInvalidLimitDoesNotBorrowParent(t *testing.T) {
	parent := &adaptor.ActiveExecutionTimeoutError{Limit: 777 * time.Second}
	own := &adaptor.ActiveExecutionTimeoutError{Limit: 100 * time.Millisecond}
	for _, tc := range []struct {
		name  string
		cause error
	}{
		{"missing", nil},
		{"sentinel_only", adaptor.ErrActiveExecutionTimeout},
		{"zero_first", errors.Join(&adaptor.ActiveExecutionTimeoutError{}, own)},
		{"negative_first", errors.Join(&adaptor.ActiveExecutionTimeoutError{Limit: -1}, own)},
		{"nil_typed_first", errors.Join((*adaptor.ActiveExecutionTimeoutError)(nil), own)},
	} {
		for _, streaming := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%v", tc.name, streaming), func(t *testing.T) {
				partial := &adaptor.Result{Text: "partial", Summary: "summary"}
				carrier := &adaptor.RunError{Reason: adaptor.ReasonActiveExecutionTimeout, Result: partial, Cause: tc.cause}
				original := errors.Join(parent, carrier)
				runner := &reviewT18Runner{err: original}
				client := newLocalClient("member", runner)
				task := terminalTask(t, client, context.Background(), streaming)
				failure := client.taskFailure(task.ID)
				if failure == nil || failure.Code != "active_execution_timeout" || failure.Cause != original || !errors.Is(failure, parent) {
					t.Fatalf("carrier/graph changed: %#v", failure)
				}
				if _, present := failure.Metadata["limit_ms"]; present {
					t.Fatalf("invalid/missing own limit borrowed from graph: %+v", failure.Metadata)
				}
				if len(task.Messages) != 1 || task.Messages[0].Parts[0].Text != partial.Text || len(task.Artifacts) != 1 || carrier.Result != partial {
					t.Fatalf("partial lost: %#v", task)
				}
				if runner.runs.Load() != 0 || runner.streams.Load() != 1 {
					t.Fatal("second execution")
				}
			})
		}
	}
}

func TestAlignmentReviewT18NumericMathematicalDomain(t *testing.T) {
	for _, tc := range []struct {
		literal string
		ms      int64
	}{
		{"100.0", 100}, {"1e2", 100}, {"1.00e+2", 100}, {"0.001e3", 1}, {"100000e-3", 100},
		{"9223372036855.0", maxBudgetMilliseconds}, {"9.223372036855e12", maxBudgetMilliseconds},
		{"1" + strings.Repeat("0", 100) + "e-100", 1},
		{"100.0000000000000000000001", 0}, {"9.223372036855000000001e12", 0}, {"9223372036856.0", 0},
		{"1e100000000", 0}, {"1e-100000000", 0}, {"1e9223372036854775807", 0}, {"1e-9223372036854775808", 0},
		{"1e9999999999999999999999999999999999999", 0}, {"01", 0}, {"+100", 0}, {"0x64", 0}, {"1/1", 0}, {"1.", 0}, {"NaN", 0}, {"Infinity", 0}, {`"100"`, 0}, {"0.0", 0}, {"-100", 0}, {"1e-1", 0},
	} {
		t.Run(tc.literal, func(t *testing.T) {
			c := &alignmentClient{send: func(context.Context, clienta2a.SendRequest) (clienta2a.Task, error) {
				return clienta2a.Task{ID: "task", Status: incomingFailure(map[string]any{"code": "active_execution_timeout", "limit_ms": json.Number(tc.literal)}, clienta2a.TaskStateFailed)}, nil
			}}
			d := alignmentDelegator(t, DelegationPolicy{}, c)
			out, err := d.Delegate(context.Background(), DelegationRequest{RunID: "leader", Agent: "member"})
			if tc.ms == 0 {
				if err == nil || out.Error.Code != "remote_failed" || out.Error.Metadata["failure_payload_invalid"] != true {
					t.Fatalf("invalid mathematical/JSON domain promoted: %q %+v %v", tc.literal, out, err)
				}
				return
			}
			var typed *adaptor.ActiveExecutionTimeoutError
			want := time.Duration(tc.ms) * time.Millisecond
			if tc.ms == maxBudgetMilliseconds {
				want = time.Duration(math.MaxInt64)
			}
			if err == nil || out.Error.Code != "active_execution_timeout" || out.Error.Metadata["limit_ms"] != tc.ms || !errors.As(err, &typed) || typed.Limit != want {
				t.Fatalf("exact integer rejected or changed: %q %+v %v", tc.literal, out, err)
			}
		})
	}
}
