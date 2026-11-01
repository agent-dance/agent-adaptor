package subagentstream_test

import (
	adaptor "github.com/agent-dance/agent-adaptor/v2"
	"github.com/agent-dance/agent-adaptor/v2/bridges/subagentstream"
	"reflect"
	"testing"
)

func TestProject(t *testing.T) {
	e := adaptor.WithEventMeta(adaptor.SubagentUpdate{Task: "t", Member: "m", Kind: adaptor.SubagentMessage, State: "running", Delta: "hello", Summary: "summary", Data: map[string]any{"remote": "id"}}, adaptor.EventMeta{RunID: "r", Sequence: 2})
	u, ok := subagentstream.Project(e)
	if !ok || u.Task != "t" || u.Member != "m" || u.Kind != adaptor.SubagentMessage || u.Delta != "hello" || u.Summary != "summary" || u.State != "running" || !reflect.DeepEqual(u.Meta, e.Meta()) || u.Data["remote"] != "id" {
		t.Fatalf("projection=%+v", u)
	}
	if _, ok := subagentstream.Project(adaptor.Done{}); ok {
		t.Fatal("projected terminal as a worker event")
	}
}
