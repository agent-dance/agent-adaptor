package subagentstream_test

import (
	"context"
	"errors"
	"fmt"
	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/bridges/subagentstream"
	"github.com/agent-dance/agent-adaptor/capability"
	"github.com/agent-dance/agent-adaptor/hosttools/a2adelegation"
	"github.com/agent-dance/agent-adaptor/todo"
	"reflect"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type boundBus struct{ bound bool }

func (b *boundBus) SubscribeRun(context.Context, string) <-chan a2adelegation.DelegationEvent {
	panic("Merge must not subscribe")
}
func (b *boundBus) RunEventsBound(runID string) bool { return b.bound && runID == "r" }

func TestAlignmentMergePreservesParent(t *testing.T) {
	f := newFakeStream("r")
	meta := adaptor.EventMeta{RunID: "r", Sequence: 99, Time: time.Now().UTC(), Source: &adaptor.EventSourceMeta{InvocationID: "inv", Upstream: &adaptor.EventSourceMeta{RunID: "up"}}}
	ev := adaptor.WithEventMeta(adaptor.ToolCall{ID: "x", ScopeID: "s", ParentToolCallID: "p"}, meta)
	f.events <- ev
	close(f.events)
	merged := subagentstream.Merge(context.Background(), f, &boundBus{bound: true})
	out, _ := collect(t, merged)
	if len(out) != 1 || !reflect.DeepEqual(out[0], ev) {
		t.Fatalf("parent changed: %#v", out)
	}
	result, err := merged.Result()
	if err != nil || result != f.result {
		t.Fatalf("result=%v %v", result, err)
	}
}

func TestAlignmentMergeUnsupportedRetainsPartialPrimaryAndCause(t *testing.T) {
	for _, mode := range []string{"success", "run-error", "ordinary-error", "no-proof"} {
		t.Run(mode, func(t *testing.T) {
			f := newFakeStream("r")
			cause := errors.New("parent transport")
			partial := &adaptor.Result{RunID: "r", Text: "partial", Summary: "brief", Metadata: map[string]string{"audit": "preserved"}}
			f.result = partial
			var original *adaptor.RunError
			switch mode {
			case "run-error":
				original = &adaptor.RunError{Reason: adaptor.ReasonApprovalDenied, Message: "primary", Details: map[string]any{"key": "value"}, Result: partial, Cause: errors.Join(cause, context.Canceled)}
				f.result = nil
				f.err = original
			case "ordinary-error":
				f.err = cause
			}
			terminal := adaptor.WithEventMeta(adaptor.RunFinished{RunID: "r"}, adaptor.EventMeta{RunID: "r", Sequence: 8})
			f.events <- terminal
			close(f.events)
			var bus subagentstream.EventBus = &boundBus{}
			if mode == "no-proof" {
				bus = &unboundBus{}
			}
			merged := subagentstream.Merge(context.Background(), f, bus)
			events, _ := collect(t, merged)
			if len(events) != 1 || !reflect.DeepEqual(events[0], terminal) {
				t.Fatalf("terminal changed=%#v", events)
			}
			result, err := merged.Result()
			var runErr *adaptor.RunError
			if result != nil || !errors.As(err, &runErr) || !errors.Is(err, subagentstream.ErrEventInjectionUnsupported) || runErr.Result != partial {
				t.Fatalf("result=%#v err=%#v", result, err)
			}
			if mode == "run-error" {
				if runErr == original || runErr.Reason != original.Reason || runErr.Message != original.Message || !reflect.DeepEqual(runErr.Details, original.Details) || !errors.Is(err, cause) || !errors.Is(err, context.Canceled) || errors.Is(original, subagentstream.ErrEventInjectionUnsupported) {
					t.Fatalf("primary or original mutated: %#v", runErr)
				}
			}
			if mode == "ordinary-error" && !errors.Is(err, cause) {
				t.Fatal("lost cause")
			}
			var wg sync.WaitGroup
			for i := 0; i < 16; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					r, e := merged.Result()
					if r != result || e != err {
						t.Error("Result not stable")
					}
				}()
			}
			wg.Wait()
		})
	}
}

type unboundBus struct{}

func (*unboundBus) SubscribeRun(context.Context, string) <-chan a2adelegation.DelegationEvent {
	panic("must not subscribe")
}

type lateBoundBus struct {
	bound   atomic.Bool
	queried chan struct{}
}

func (*lateBoundBus) SubscribeRun(context.Context, string) <-chan a2adelegation.DelegationEvent {
	panic("must not subscribe")
}
func (b *lateBoundBus) RunEventsBound(runID string) bool {
	close(b.queried)
	return b.bound.Load() && runID == "r"
}

func TestAlignmentMergeProofCheckedOnlyAfterResult(t *testing.T) {
	f := newFakeStream("r")
	bus := &lateBoundBus{queried: make(chan struct{})}
	merged := subagentstream.Merge(nil, f, bus)
	f.events <- adaptor.TextDelta{Text: "before bind"}
	<-merged.Events()
	select {
	case <-bus.queried:
		t.Fatal("proof checked before parent completed")
	default:
	}
	bus.bound.Store(true)
	close(f.events)
	for range merged.Events() {
	}
	if r, err := merged.Result(); err != nil || r != f.result {
		t.Fatalf("late bind result=%v %v", r, err)
	}
}

func TestAlignmentMergeCancelDrainsPartialResult(t *testing.T) {
	for _, byContext := range []bool{false, true} {
		t.Run(fmt.Sprint(byContext), func(t *testing.T) {
			f := newFakeStream("r")
			f.result = nil
			partial := &adaptor.Result{RunID: "r", Text: "audit after cancel"}
			f.err = &adaptor.RunError{Reason: adaptor.ReasonCancelled, Result: partial, Cause: context.Canceled}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			merged := subagentstream.Merge(ctx, f, &boundBus{bound: true})
			produced := make(chan struct{})
			go func() {
				defer close(produced)
				defer close(f.events)
				for i := 0; i < 256; i++ {
					select {
					case <-f.cancelled:
						f.events <- adaptor.WithEventMeta(adaptor.RunFinished{RunID: "r", Failed: true, Reason: adaptor.ReasonCancelled}, adaptor.EventMeta{Sequence: 300})
						return
					case f.events <- adaptor.TextDelta{Text: "x"}:
					}
				}
			}()
			deadline := time.Now().Add(time.Second)
			for len(merged.Events()) < cap(merged.Events()) && time.Now().Before(deadline) {
				runtime.Gosched()
			}
			if len(merged.Events()) != cap(merged.Events()) {
				t.Fatal("output did not saturate")
			}
			if byContext {
				cancel()
			} else {
				merged.Cancel()
				merged.Cancel()
			}
			select {
			case <-produced:
			case <-time.After(time.Second):
				t.Fatal("cancel did not unblock and drain parent")
			}
			done := make(chan struct{})
			go func() {
				defer close(done)
				for range merged.Events() {
				}
				r, err := merged.Result()
				var re *adaptor.RunError
				if r != nil || !errors.As(err, &re) || re.Result != partial {
					t.Errorf("lost cancelled partial: %v %v", r, err)
				}
			}()
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("cancel completion blocked")
			}
		})
	}
}

func TestAlignmentMergeTypedFamilyDropAndTerminalOrder(t *testing.T) {
	at := time.Date(2026, 9, 7, 0, 0, 0, 0, time.UTC)
	f := newFakeStream("r")
	events := []adaptor.Event{
		adaptor.RunStarted{RunID: "r"},
		adaptor.TextDelta{MessageID: "m", Text: "text"},
		adaptor.Thinking{MessageID: "thinking", Text: "reason"},
		adaptor.ToolCall{ID: "tool", ScopeID: "child", ParentScopeID: "root", ParentToolCallID: "parent", Args: map[string]any{"nested": map[string]any{"v": "value"}}},
		adaptor.ToolResult{ID: "tool", ScopeID: "child", ParentScopeID: "root", ParentToolCallID: "parent", Result: map[string]any{"text": "result"}},
		adaptor.CapabilityInvocation{Invocation: capability.Invocation{InvocationID: "i", Ref: capability.Ref{Kind: capability.MCP, Key: "k", Operation: "search"}, Phase: capability.Started, Source: capability.Provider, Evidence: capability.ProviderProtocol, OccurredAt: at}},
		adaptor.TodoUpdated{Snapshot: todo.Snapshot{Items: []todo.Item{}, Source: todo.PlanUpdate, Revision: 2, OccurredAt: at}},
		adaptor.ProcessInfo{Kind: "stdout", Bytes: []byte("raw")},
		adaptor.Notice{Kind: adaptor.NoticeRuntime, Data: map[string]any{"code": "observation_unavailable"}},
		adaptor.SubagentUpdate{Kind: adaptor.SubagentDelta, Agent: "child"},
		&adaptor.ApprovalRequest{ID: "q", Kind: adaptor.ApprovalQuestion, Title: "question"},
		adaptor.Dropped{Count: 2, ByKind: map[string]int{"todo.updated": 1, "text.delta": 1}, FirstSequence: 20, LastSequence: 21, Reason: "cancelled"},
		adaptor.RunFinished{RunID: "r", Failed: true, Reason: adaptor.ReasonCancelled},
	}
	for i, ev := range events {
		events[i] = adaptor.WithEventMeta(ev, adaptor.EventMeta{RunID: "r", Sequence: uint64(i*2 + 1), Time: at, ThreadKey: "opaque:/thread", Source: &adaptor.EventSourceMeta{InvocationID: "i", Upstream: &adaptor.EventSourceMeta{RunID: "up"}}})
		f.events <- events[i]
	}
	close(f.events)
	stream := subagentstream.Merge(context.Background(), f, &boundBus{bound: true})
	var got []adaptor.Event
	for ev := range stream.Events() {
		got = append(got, ev)
	}
	if !reflect.DeepEqual(got, events) {
		t.Fatal("Merge changed typed event, scope, drop, source or terminal order")
	}
}
