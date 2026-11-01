package subagentstream_test

import (
	"context"
	adaptor "github.com/agent-dance/agent-adaptor/v2"
	"github.com/agent-dance/agent-adaptor/v2/bridges/subagentstream"
	"testing"
)

type fakeStream struct {
	events chan adaptor.Event
	result *adaptor.Result
	cancel chan struct{}
}

func (f *fakeStream) Events() <-chan adaptor.Event     { return f.events }
func (f *fakeStream) RunID() string                    { return "parent" }
func (f *fakeStream) Result() (*adaptor.Result, error) { return f.result, nil }
func (f *fakeStream) Cancel() {
	select {
	case <-f.cancel:
	default:
		close(f.cancel)
	}
}
func (f *fakeStream) Send(context.Context, string, ...adaptor.MessageOption) error {
	return adaptor.ErrRunFinished
}
func TestMergeTypedUpdates(t *testing.T) {
	p := &fakeStream{events: make(chan adaptor.Event, 2), result: &adaptor.Result{Text: "answer"}, cancel: make(chan struct{})}
	p.events <- adaptor.TextDelta{Text: "parent"}
	p.events <- adaptor.Done{Result: p.result}
	close(p.events)
	updates := make(chan adaptor.SubagentUpdate, 2)
	updates <- adaptor.WithEventMeta(adaptor.SubagentUpdate{Task: "t", Member: "m", Kind: adaptor.SubagentStarted}, adaptor.EventMeta{RunID: "worker", Sequence: 7}).(adaptor.SubagentUpdate)
	updates <- adaptor.SubagentUpdate{Task: "t", Member: "m", Kind: adaptor.SubagentFinished, State: "succeeded"}
	close(updates)
	merged := subagentstream.Merge(context.Background(), p, updates)
	result, err := merged.Result()
	if result != p.result || err != nil {
		t.Fatal(result, err)
	}
	count := 0
	var last adaptor.Event
	for e := range merged.Events() {
		count++
		if e.Meta().Sequence != uint64(count) {
			t.Fatal("event order")
		}
		if u, ok := e.(adaptor.SubagentUpdate); ok && u.Kind == adaptor.SubagentStarted && (u.Meta().Source == nil || u.Meta().Source.Sequence != 7) {
			t.Fatal("lost source")
		}
		last = e
	}
	if _, ok := last.(adaptor.Done); !ok || count != 4 {
		t.Fatalf("last=%T count=%d", last, count)
	}
	merged.Cancel()
	<-p.cancel
}
