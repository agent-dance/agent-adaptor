package main

import (
	"context"
	"fmt"
	"os"
	"reflect"
	"testing"
	"time"

	adaptor "github.com/agent-dance/agent-adaptor"
)

func Example() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := demonstrate(ctx, os.Stdout); err != nil {
		fmt.Println("error:", err)
	}
	// Output:
	// default: prompt="show channels" append="Use repo terminology.\n"
	// override: prompt="show channels" append="Answer in Chinese.\n"
	// clear: prompt="show channels" append=""
	// default again: prompt="show channels" append="Use repo terminology.\n"
	// capability: demo-review started
	// todo: revision=1 items=1
	// text: Reviewed docs.
	// todo: revision=2 items=0
	// capability: demo-review completed
	// result: Reviewed docs.
	// recorded capability facts: 2
	// budget: active_execution_timeout partial="work started" raw=true
}

// The callback consumer exercises the same real SDK pipeline as the event
// responder in Example. It also checks that Run does not lose stream results.
func TestOfflineRunAndStreamAgree(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	agent := adaptor.New(demoDriver{},
		adaptor.WithPolicy(adaptor.Policy{Approvals: adaptor.ApprovalPolicy{Question: adaptor.QuestionAsk}}),
		adaptor.OnApproval(func(ctx context.Context, req *adaptor.ApprovalRequest) error {
			return req.Answer(ctx, "docs")
		}),
	)
	t.Cleanup(func() {
		shutdown, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := agent.Close(shutdown); err != nil {
			t.Error(err)
		}
	})
	fromRun, err := agent.Run(ctx, "review demo")
	if err != nil {
		t.Fatal(err)
	}
	stream := agent.Stream(ctx, "review demo")
	var revisions []uint64
	var sizes []int
	var last adaptor.Event
	var sequence uint64
	var starts, finishes int
	for event := range stream.Events() {
		if meta := event.Meta(); meta.RunID != stream.RunID() || meta.Sequence <= sequence {
			t.Fatalf("invalid event coordinates: %#v after %d", meta, sequence)
		} else {
			sequence = meta.Sequence
		}
		if event, ok := event.(adaptor.TodoUpdated); ok {
			revisions = append(revisions, event.Snapshot.Revision)
			sizes = append(sizes, len(event.Snapshot.Items))
		}
		switch event.(type) {
		case adaptor.RunStarted:
			starts++
		case adaptor.RunFinished:
			finishes++
		}
		last = event
	}
	fromStream, err := stream.Result()
	if err != nil {
		t.Fatal(err)
	}
	if fromRun.Text != "Reviewed docs." || fromStream.Text != fromRun.Text || fromStream.Summary != fromRun.Summary {
		t.Fatalf("Run=%#v Stream=%#v", fromRun, fromStream)
	}
	if !reflect.DeepEqual(revisions, []uint64{1, 2}) || !reflect.DeepEqual(sizes, []int{1, 0}) {
		t.Fatalf("full/clear snapshots = %v %v", revisions, sizes)
	}
	if terminal, ok := last.(adaptor.RunFinished); !ok || terminal.Failed {
		t.Fatalf("last event = %#v", last)
	}
	if starts != 1 || finishes != 1 {
		t.Fatalf("consumer lifecycle counts = %d started, %d finished", starts, finishes)
	}
	again, err := stream.Result()
	if err != nil || again != fromStream {
		t.Fatal("completed Result was not stable", err)
	}
}
