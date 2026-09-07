package a2adelegation_test

import (
	"context"
	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/bridges/subagentstream"
	"github.com/agent-dance/agent-adaptor/hosttools/capabilityrecorder"
	"testing"
)

func TestAlignmentRealServiceBindingSupportsMergeAndRecorder(t *testing.T) {
	team := newTeamOfThree(t, nil)
	recorder, err := capabilityrecorder.New(capabilityrecorder.Config{Store: capabilityrecorder.NewMemoryStore()})
	if err != nil {
		t.Fatal(err)
	}
	leader := adaptor.New(&requestDrivenLeader{calls: []leaderCall{{agent: "plan", objective: "draft"}}, output: "done"}, team.Option(), recorder.Option())
	defer leader.Close(context.Background())
	original := leader.Stream(context.Background(), "go")
	merged := subagentstream.Merge(context.Background(), original, team.Bus())
	var events []adaptor.Event
	for ev := range merged.Events() {
		events = append(events, ev)
	}
	result, err := merged.Result()
	if err != nil || result.Text != "done" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if !team.Bus().RunEventsBound(original.RunID()) {
		t.Fatal("real historical binding missing")
	}
	page, err := recorder.Query(context.Background(), capabilityrecorder.Query{Scope: capabilityrecorder.Scope{RunID: original.RunID()}})
	if err != nil || len(page.Records) != 2 {
		t.Fatalf("records=%+v err=%v", page, err)
	}
	for _, record := range page.Records {
		found := false
		for _, ev := range events {
			if ev.Meta().Sequence == record.Sequence {
				if _, ok := ev.(adaptor.CapabilityInvocation); ok {
					found = true
				}
			}
		}
		if !found {
			t.Fatal("recorder sequence absent from original Stream")
		}
	}
}
