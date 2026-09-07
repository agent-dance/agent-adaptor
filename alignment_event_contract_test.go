package adaptor_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/capability"
	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/memory"
	"github.com/agent-dance/agent-adaptor/todo"
)

func TestAlignmentEventFinalOutcome(t *testing.T) {
	cleanup := errors.New("fixture cleanup")
	p := &fakeProvider{log: &callLog{}, detach: func(context.Context, string) error { return cleanup }}
	d := newFakeDriver()
	d.runFunc = func(_ context.Context, req driver.Request, sink driver.EventSink) (driver.Response, error) {
		_ = sink.EmitStream(driver.StreamPayload{Kind: driver.StreamRunStarted, RunID: req.RunID})
		_ = sink.EmitStream(driver.StreamPayload{Kind: driver.StreamRunFinished, RunID: req.RunID})
		return alignmentPartialResponse(), nil
	}
	st := adaptor.New(d, adaptor.WithRunServices(p)).Stream(context.Background(), "work")
	var terminal adaptor.RunFinished
	var count int
	for ev := range st.Events() {
		if v, ok := ev.(adaptor.RunFinished); ok {
			terminal = v
			count++
		}
	}
	res, err := st.Result()
	re := alignmentRequirePartial(t, res, err, adaptor.ReasonInfrastructure, cleanup)
	if count != 1 || !terminal.Failed || terminal.Reason != re.Reason {
		t.Fatalf("terminal = %#v count=%d, error reason=%v", terminal, count, re.Reason)
	}
	if re.Result.Raw().Terminal == nil {
		t.Fatal("provider terminal lost")
	}
}

func TestAlignmentEventParentAndIndependentClone(t *testing.T) {
	args := map[string]any{"nested": []any{map[string]any{"value": "original"}}}
	d := newFakeDriver()
	d.runFunc = func(_ context.Context, req driver.Request, sink driver.EventSink) (driver.Response, error) {
		p := driver.StreamPayload{Kind: driver.StreamToolCallStart, ScopeID: "child", ParentScopeID: "root-parent", ParentToolCallID: "parent", ToolCallID: "same-id", Name: "tool", Args: args}
		if err := sink.EmitStream(p); err != nil {
			return driver.Response{}, err
		}
		args["nested"].([]any)[0].(map[string]any)["value"] = "producer mutation"
		p.Kind = driver.StreamToolCallEnd
		p.Args = nil
		_ = sink.EmitStream(p)
		p.Kind = driver.StreamToolCallResult
		p.Result = map[string]any{"ok": true}
		_ = sink.EmitStream(p)
		return driver.Response{Transcript: []driver.TranscriptItem{{Kind: driver.TranscriptToolCall, ToolUseID: "same-id", ScopeID: "child", ParentScopeID: "root-parent", ParentToolCallID: "parent", Input: map[string]any{"a": []any{"b"}}}}}, nil
	}
	events, res, err := collect(adaptor.New(d).Stream(context.Background(), "work"))
	if err != nil {
		t.Fatal(err)
	}
	call := events[1].(adaptor.ToolCall)
	if call.ScopeID != "child" || call.ParentScopeID != "root-parent" || call.ParentToolCallID != "parent" || call.Args["nested"].([]any)[0].(map[string]any)["value"] != "original" {
		t.Fatal(call)
	}
	if result := events[3].(adaptor.ToolResult); result.ScopeID != call.ScopeID || result.ParentScopeID != call.ParentScopeID || result.ParentToolCallID != call.ParentToolCallID {
		t.Fatal(result)
	}
	transcript := res.Transcript()
	transcript[0].Input.(map[string]any)["a"].([]any)[0] = "changed"
	if res.Transcript()[0].Input.(map[string]any)["a"].([]any)[0] != "b" || res.Transcript()[0].ScopeID != "child" {
		t.Fatal("transcript shallow copy")
	}
	source := &adaptor.EventSourceMeta{RunID: "immediate", ScopeID: "s", InvocationID: "i", ToolCallID: "t", DelegationID: "d", Upstream: &adaptor.EventSourceMeta{RunID: "origin"}}
	snapshot := alignmentTodo()
	ev := adaptor.WithEventMeta(adaptor.TodoUpdated{Snapshot: snapshot}, adaptor.EventMeta{Source: source})
	source.Upstream.RunID = "changed"
	snapshot.Items[0].Content = "changed"
	meta := ev.Meta()
	meta.Source.Upstream.RunID = "reader mutation"
	if ev.Meta().Source.Upstream.RunID != "origin" || ev.(adaptor.TodoUpdated).Snapshot.Items[0].Content != "work" {
		t.Fatal("relay/snapshot shallow clone")
	}
}
func TestAlignmentEventInvalidFactsAreNotAccepted(t *testing.T) {
	d := newFakeDriver()
	d.runFunc = func(_ context.Context, _ driver.Request, sink driver.EventSink) (driver.Response, error) {
		v := alignmentFact("id")
		v.Ref.Key = "bad\x00"
		if err := sink.EmitStream(driver.StreamPayload{Kind: driver.StreamCapabilityInvocation, Capability: &v}); err == nil {
			t.Fatal("invalid key accepted")
		}
		v = alignmentFact("id")
		if err := sink.EmitStream(driver.StreamPayload{Kind: driver.StreamCapabilityInvocation, Capability: &v, Raw: map[string]any{"secret": "unsafe"}}); err == nil {
			t.Fatal("raw accompanying fact accepted")
		}
		s := todo.Snapshot{Items: nil, Source: todo.PlanUpdate, Revision: 1, OccurredAt: time.Now().UTC()}
		if err := sink.EmitStream(driver.StreamPayload{Kind: driver.StreamTodoUpdated, Todo: &s}); err != nil {
			t.Fatal(err)
		}
		return driver.Response{}, nil
	}
	ev, _, err := collect(adaptor.New(d).Stream(context.Background(), "work"))
	if err != nil || len(ev) != 3 {
		t.Fatal(ev, err)
	}
	if s := ev[1].(adaptor.TodoUpdated).Snapshot; s.Items == nil || len(s.Items) != 0 || s.Revision != 1 {
		t.Fatal(s)
	}
	if ev[1].Meta().Sequence != 2 {
		t.Fatal("invalid facts consumed sequence")
	}
}
func TestAlignmentEventTerminalOutcomeMatrix(t *testing.T) {
	for _, sources := range []bool{false, true} {
		for _, kind := range []string{"success", "provider-failure", "cancel", "release"} {
			t.Run(fmt.Sprintf("%s/sources=%v", kind, sources), func(t *testing.T) {
				cause := errors.New("lease release")
				store := &alignmentFaultStore{Store: memory.NewStore(), kind: "release", cause: cause}
				if kind == "release" {
					store.active.Store(true)
				}
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				d := newFakeDriver()
				d.runFunc = func(_ context.Context, req driver.Request, sink driver.EventSink) (driver.Response, error) {
					_ = sink.EmitStream(driver.StreamPayload{Kind: driver.StreamRunStarted})
					v := alignmentFact("id")
					_ = sink.EmitStream(driver.StreamPayload{Kind: driver.StreamCapabilityInvocation, Capability: &v})
					v.Phase = capability.Interrupted
					v.ErrorCode = capability.RunInterrupted
					_ = sink.EmitStream(driver.StreamPayload{Kind: driver.StreamCapabilityInvocation, Capability: &v})
					_ = sink.EmitStream(driver.StreamPayload{Kind: driver.StreamRunFinished})
					response := alignmentPartialResponse()
					if kind == "provider-failure" {
						response.Failure = &driver.RunFailure{Code: driver.FailureAgentError, Message: "failed"}
					}
					if kind == "cancel" {
						cancel()
						return response, ctx.Err()
					}
					return response, nil
				}
				opts := []adaptor.Option{adaptor.WithThreadStore(store)}
				if sources {
					opts = append(opts, adaptor.WithRunServices(observerProvider(adaptor.RunAttachment{Events: func(ctx context.Context, _ string) <-chan adaptor.Event {
						ch := make(chan adaptor.Event, 1)
						go func() { <-ctx.Done(); ch <- adaptor.Notice{Kind: adaptor.NoticeRuntime, Text: "tail"}; close(ch) }()
						return ch
					}})))
				}
				events, res, err := collect(adaptor.New(d, opts...).Thread("thread").Stream(ctx, "work"))
				var terminal adaptor.RunFinished
				count := 0
				for _, ev := range events {
					if e, ok := ev.(adaptor.RunFinished); ok {
						terminal = e
						count++
					}
				}
				if count != 1 || !reflect.DeepEqual(events[len(events)-1], terminal) {
					t.Fatal("terminal ordering", events)
				}
				if kind == "success" {
					if err != nil || res == nil || terminal.Failed {
						t.Fatal(err, terminal)
					}
				} else {
					var re *adaptor.RunError
					if !errors.As(err, &re) || res != nil || re.Result == nil || !terminal.Failed || terminal.Reason != re.Reason {
						t.Fatal(err, terminal)
					}
					res = re.Result
				}
				if res.Raw().Terminal == nil || string(res.Raw().Terminal.JSON) != string(alignmentPartialResponse().RawStreams.Terminal.JSON) {
					t.Fatal("provider terminal lost")
				}
			})
		}
	}
}
