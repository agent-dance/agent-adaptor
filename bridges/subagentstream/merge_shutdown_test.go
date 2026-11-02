package subagentstream_test

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"

	adaptor "github.com/agent-dance/agent-adaptor/v2"
	"github.com/agent-dance/agent-adaptor/v2/bridges/subagentstream"
)

func TestMergeCancelRetainsParentDoneForPausedSubscriber(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	parent := &fakeStream{events: make(chan adaptor.Event, 602), result: &adaptor.Result{Text: "answer"}, cancel: make(chan struct{})}
	for index := 0; index < 600; index++ {
		parent.events <- adaptor.Notice{Kind: "critical"}
	}
	updates := make(chan adaptor.SubagentUpdate) // Cancel must detach this source.
	merged := subagentstream.Merge(ctx, parent, updates)
	events := merged.Events()
	first := <-events
	merged.Cancel()
	<-parent.cancel
	// The parent's terminal events arrive after Cancel, while the consumer
	// remains paused. They must still be collected and forwarded in order.
	parent.events <- adaptor.RunFinished{RunID: "parent"}
	parent.events <- adaptor.Done{Result: parent.result}
	close(parent.events)
	result, err := merged.Result()
	pause, resume := context.WithTimeout(ctx, 50*time.Millisecond)
	defer resume()
	<-pause.Done()
	count, finished := 0, 0
	previous := first.Meta().Sequence
	var last adaptor.Event
	for event := range events {
		if event.Meta().Sequence != previous+1 {
			t.Fatalf("critical event sequence=%d after %d", event.Meta().Sequence, previous)
		}
		previous = event.Meta().Sequence
		switch event := event.(type) {
		case adaptor.Notice:
			if event.Kind == "critical" {
				count++
			}
		case adaptor.RunFinished:
			finished++
		}
		last = event
	}
	done, ok := last.(adaptor.Done)
	if count != 599 || finished != 1 || !ok || done.Result != result || done.Err != err || result != parent.result {
		t.Fatalf("critical events=%d RunFinished=%d last=%T result=%p err=%v", count, finished, last, result, err)
	}
}

func TestUnsubscribedMergeCompletionRetainsEventsWithoutPump(t *testing.T) {
	for _, cancelMerge := range []bool{false, true} {
		t.Run(fmt.Sprintf("Cancel=%v", cancelMerge), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			parent := &fakeStream{events: make(chan adaptor.Event, 601), result: &adaptor.Result{Text: "answer"}, cancel: make(chan struct{})}
			for index := 0; index < 600; index++ {
				parent.events <- adaptor.Notice{Kind: "critical"}
			}
			parent.events <- adaptor.Done{Result: parent.result}
			close(parent.events)
			updates := make(chan adaptor.SubagentUpdate)
			if !cancelMerge {
				close(updates)
			}
			merged := subagentstream.Merge(ctx, parent, updates)
			if cancelMerge {
				merged.Cancel()
				<-parent.cancel
			}
			result, err := merged.Result()
			if result != parent.result || err != nil {
				t.Fatal(result, err)
			}
			stack := make([]byte, 1<<20)
			frame := fmt.Sprintf("(*mergedStream).pump(%p,", merged)
			for {
				n := runtime.Stack(stack, true)
				if !strings.Contains(string(stack[:n]), frame) {
					break
				}
				if ctx.Err() != nil {
					t.Fatalf("unsubscribed completion left %s blocked", frame)
				}
				runtime.Gosched()
			}
			count := 0
			var last adaptor.Event
			for event := range merged.Events() {
				count++
				if event.Meta().Sequence != uint64(count) {
					t.Fatalf("sequence=%d, want %d", event.Meta().Sequence, count)
				}
				last = event
			}
			done, ok := last.(adaptor.Done)
			if count != 601 || !ok || done.Result != result || done.Err != err {
				t.Fatalf("late subscriber: count=%d last=%T result=%p err=%v", count, last, result, err)
			}
		})
	}
}

func TestMergeSubscriptionDuringCompletionRetainsDone(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	parent := &fakeStream{events: make(chan adaptor.Event, 2), result: &adaptor.Result{Text: "answer"}, cancel: make(chan struct{})}
	updates := make(chan adaptor.SubagentUpdate)
	close(updates)
	merged := subagentstream.Merge(ctx, parent, updates)
	start, finished := make(chan struct{}), make(chan struct{})
	go func() {
		<-start
		parent.events <- adaptor.Notice{Kind: "critical"}
		parent.events <- adaptor.Done{Result: parent.result}
		close(parent.events)
		close(finished)
	}()
	close(start)
	events := merged.Events()
	count := 0
	var last adaptor.Event
	for event := range events {
		count++
		if event.Meta().Sequence != uint64(count) {
			t.Fatalf("sequence=%d, want %d", event.Meta().Sequence, count)
		}
		last = event
	}
	<-finished
	result, err := merged.Result()
	done, ok := last.(adaptor.Done)
	if count != 2 || !ok || done.Result != result || done.Err != err || result != parent.result {
		t.Fatalf("subscription/completion race: count=%d last=%T result=%p err=%v", count, last, result, err)
	}
}
