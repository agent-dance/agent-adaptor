package team

import (
	"context"
	"reflect"
	"testing"

	adaptor "github.com/agent-dance/agent-adaptor/v2"
	"github.com/agent-dance/agent-adaptor/v2/driver"
)

type collectedChildProvider struct{}

func (collectedChildProvider) AttachRun(context.Context, string) (adaptor.RunAttachment, error) {
	return adaptor.RunAttachment{Results: func() []adaptor.SubagentResult {
		return []adaptor.SubagentResult{{Member: "external", Task: "observed-task", State: "succeeded", Summary: "observed child", Usage: &adaptor.Usage{InputTokens: 7}}}
	}}, nil
}

func (collectedChildProvider) DetachRun(context.Context, string) error { return nil }

func TestFinalSnapshotReplacesOnlyTeamTaskSummaries(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
	}{{name: "success"}, {name: "cancelled", err: context.Canceled}} {
		t.Run(test.name, func(t *testing.T) {
			external := adaptor.SubagentResult{Member: "external", Task: "task", Summary: "other provider", Usage: &adaptor.Usage{InputTokens: 7}}
			old := adaptor.SubagentResult{Member: "worker", Task: "task", Summary: "outdated", Usage: &adaptor.Usage{InputTokens: 3}}
			part := (&adaptor.Result{Text: "leader", Usage: &adaptor.Usage{InputTokens: 11}}).WithSubagents([]adaptor.SubagentResult{external, old, old})
			run := &execution{
				id: "run", parts: []*adaptor.Result{part}, total: &adaptor.Usage{InputTokens: 11},
				tasks: map[string]*taskRun{"task": {record: Task{ID: "task", Member: "worker", State: Succeeded, Summary: "final", Usage: &adaptor.Usage{InputTokens: 3}, Unread: []string{"late note"}}}},
				order: []string{"task"}, log: newEventLog("run", "", false), done: make(chan struct{}),
			}
			run.finish(test.err)
			want := []adaptor.SubagentResult{external, {Member: "worker", Task: "task", State: string(Succeeded), Summary: "final", Usage: &adaptor.Usage{InputTokens: 3}, Unread: []string{"late note"}}}
			if got := run.result.Subagents(); !reflect.DeepEqual(got, want) {
				t.Fatalf("final summaries=%+v, want %+v", got, want)
			}
			if run.result.Usage.InputTokens != 11 || !reflect.DeepEqual(part.Subagents(), []adaptor.SubagentResult{external, old, old}) {
				t.Fatal("snapshot refresh recounted usage or mutated the leader audit")
			}
		})
	}
}

func TestFinalSnapshotPreservesOtherCollectedSubagents(t *testing.T) {
	leader := adaptor.New(&fakeDriver{run: func(context.Context, driver.Request, driver.EventSink) (driver.Response, error) {
		return answer("leader", 1), nil
	}}, adaptor.WithRunServices(collectedChildProvider{}))
	team := fixture(t, Config{Leader: leader})
	_, result, err := collect(t, team.Send(testContext(t), "work"))
	if err != nil {
		t.Fatal(err)
	}
	if result.Usage == nil || result.Usage.InputTokens != 8 {
		t.Fatalf("observed usage missing: %+v", result.Usage)
	}
	if children := result.Subagents(); len(children) != 1 || children[0].Task != "observed-task" {
		t.Fatalf("leader's observed child summary lost by final team snapshot: subagents=%+v usage=%+v", children, result.Usage)
	}
}
