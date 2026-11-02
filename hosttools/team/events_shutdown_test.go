package team

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"

	adaptor "github.com/agent-dance/agent-adaptor/v2"
	"github.com/agent-dance/agent-adaptor/v2/driver"
)

func TestCloseRetainsCriticalEventsForPausedSubscriber(t *testing.T) {
	ctx := testContext(t)
	leader := adaptor.New(&fakeDriver{run: func(ctx context.Context, _ driver.Request, _ driver.EventSink) (driver.Response, error) {
		<-ctx.Done()
		return driver.Response{}, ctx.Err()
	}})
	team := fixture(t, Config{Leader: leader})
	stream := team.Send(ctx, "start").(*execution)
	events := stream.Events()
	first := <-events
	for index := 0; index < 600; index++ {
		stream.log.push(adaptor.Notice{Kind: "critical"})
	}
	if err := team.Close(ctx); err != nil {
		t.Fatal(err)
	}
	// Deliberately hold the subscriber past the removed 25ms cutoff. This
	// deadline tests retention across a pause; it does not coordinate producers.
	pause, resume := context.WithTimeout(ctx, 50*time.Millisecond)
	defer resume()
	<-pause.Done()
	count := 0
	previous := first.Meta().Sequence
	var last adaptor.Event
	for event := range events {
		if event.Meta().Sequence != previous+1 {
			t.Fatalf("critical event sequence=%d after %d", event.Meta().Sequence, previous)
		}
		previous = event.Meta().Sequence
		if notice, ok := event.(adaptor.Notice); ok && notice.Kind == "critical" {
			count++
		}
		last = event
	}
	result, err := stream.Result()
	done, ok := last.(adaptor.Done)
	if count != 600 || !ok || done.Result != result || done.Err != err {
		t.Fatalf("critical events=%d, last=%T, Done/Result mismatch=%v", count, last, done.Result != result || done.Err != err)
	}
}

func TestUnsubscribedTeamCompletionRetainsEventsWithoutPump(t *testing.T) {
	for _, closeTeam := range []bool{false, true} {
		t.Run(fmt.Sprintf("Close=%v", closeTeam), func(t *testing.T) {
			ctx := testContext(t)
			entered := make(chan struct{})
			leader := adaptor.New(&fakeDriver{run: func(ctx context.Context, _ driver.Request, _ driver.EventSink) (driver.Response, error) {
				close(entered)
				if closeTeam {
					<-ctx.Done()
					return driver.Response{}, ctx.Err()
				}
				return answer("leader", 1), nil
			}})
			team := fixture(t, Config{Leader: leader})
			stream := team.Send(ctx, "start").(*execution)
			<-entered
			if closeTeam {
				if err := team.Close(ctx); err != nil {
					t.Fatal(err)
				}
			}
			result, err := stream.Result()
			stream.log.mu.Lock()
			finished, subscribed, queued := stream.log.finished, stream.log.subscribed, len(stream.log.queue)
			retained := len(stream.log.out)
			stream.log.mu.Unlock()
			if !finished || subscribed || queued != 0 || retained == 0 {
				t.Fatalf("unsubscribed completion: finished=%v subscribed=%v queued=%d retained=%d", finished, subscribed, queued, retained)
			}
			stack := make([]byte, 1<<20)
			frame := fmt.Sprintf("(*eventLog).pump(%p)", stream.log)
			n := runtime.Stack(stack, true)
			if strings.Contains(string(stack[:n]), frame) {
				t.Fatalf("unsubscribed completion started %s", frame)
			}
			var last adaptor.Event
			for event := range stream.Events() {
				last = event
			}
			done, ok := last.(adaptor.Done)
			if !ok || done.Result != result || done.Err != err {
				t.Fatalf("late subscriber lost Done: last=%T result=%p err=%v", last, result, err)
			}
		})
	}
}

func TestEventLogSubscriptionDuringFinishRetainsDone(t *testing.T) {
	log := newEventLog("run", "scope", false)
	result := &adaptor.Result{Text: "answer"}
	log.push(adaptor.Notice{Kind: "critical"})
	log.push(adaptor.Done{Result: result})
	start, finished := make(chan struct{}), make(chan struct{})
	go func() {
		<-start
		log.finish()
		close(finished)
	}()
	close(start)
	events := log.events()
	count := 0
	var last adaptor.Event
	for event := range events {
		count++
		last = event
	}
	<-finished
	done, ok := last.(adaptor.Done)
	if count != 2 || !ok || done.Result != result {
		t.Fatalf("subscription/finish race: count=%d last=%T result=%p", count, last, result)
	}
}
