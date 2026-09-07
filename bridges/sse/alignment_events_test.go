package sse

import (
	"encoding/json"
	"fmt"
	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/capability"
	"github.com/agent-dance/agent-adaptor/todo"
	"strings"
	"testing"
	"time"
)

func TestAlignmentRawObservationAndParent(t *testing.T) {
	at := time.Date(2026, 9, 7, 0, 0, 0, 0, time.UTC)
	zero := time.Duration(0)
	meta := adaptor.EventMeta{RunID: "r", Sequence: 9, Time: at, Source: &adaptor.EventSourceMeta{ScopeID: "child", ToolCallID: "tool", InvocationID: "inv", DelegationID: "d", Upstream: &adaptor.EventSourceMeta{RunID: "origin", Sequence: 3}}}
	cases := []struct {
		name, field, fragment string
		event                 adaptor.Event
	}{
		{"capability.invocation", "capability", `"duration_ns":0`, adaptor.CapabilityInvocation{Invocation: capability.Invocation{InvocationID: "i", Ref: capability.Ref{Kind: capability.MCP, Key: "知识", Operation: "search"}, Phase: capability.Completed, Source: capability.Provider, Evidence: capability.ProviderProtocol, OccurredAt: at, Duration: &zero}}},
		{"todo.updated", "todo", `"items":[]`, adaptor.TodoUpdated{Snapshot: todo.Snapshot{Items: []todo.Item{}, Source: todo.PlanUpdate, Revision: 2, OccurredAt: at}}},
		{"tool_call", "scope_id", `"parent_tool_call_id":"parent"`, adaptor.ToolCall{ID: "x", ScopeID: "child", ParentScopeID: "root", ParentToolCallID: "parent", Phase: adaptor.PhaseStart}},
		{"tool_call.result", "scope_id", `"parent_tool_call_id":"parent"`, adaptor.ToolResult{ID: "x", ScopeID: "child", ParentScopeID: "root", ParentToolCallID: "parent"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			name, body := rawFrameFor(adaptor.WithEventMeta(c.event, meta))
			if name != c.name {
				t.Fatalf("name=%q body=%#v", name, body)
			}
			data, err := json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
			for _, fragment := range []string{c.fragment, `"scope_id":"child"`, `"upstream":{"`, `"invocation_id":"inv"`, `"sequence":9`} {
				if !strings.Contains(string(data), fragment) {
					t.Fatalf("missing %s in %s", fragment, data)
				}
			}
		})
	}
}

func TestAlignmentRawFullUTF8SnapshotAndCopies(t *testing.T) {
	at := time.Date(2026, 9, 7, 0, 0, 0, 0, time.UTC)
	content := strings.Repeat("灵", 1365) + "a"
	items := make([]todo.Item, 20)
	for i := range items {
		items[i] = todo.Item{ID: fmt.Sprint(i), Content: content, Status: todo.Pending}
	}
	snapshot := adaptor.TodoUpdated{Snapshot: todo.Snapshot{Items: items, Source: todo.ToolResult, Revision: 1, OccurredAt: at}}
	_, body := rawFrameFor(snapshot)
	snapshot.Snapshot.Items[0].Content = "changed"
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) <= 65536 || strings.Contains(string(data), "changed") {
		t.Fatal("snapshot was truncated or aliased")
	}
	var wire struct {
		Todo todoWire `json:"todo"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		t.Fatal(err)
	}
	if len(wire.Todo.Items) != 20 || wire.Todo.Items[0].Content != content {
		t.Fatal("full UTF-8 content lost")
	}
	tool := adaptor.ToolCall{ID: "tool", Args: map[string]any{"nested": map[string]any{"value": "original"}}}
	_, body = rawFrameFor(tool)
	tool.Args["nested"].(map[string]any)["value"] = "changed"
	data, _ = json.Marshal(body)
	if strings.Contains(string(data), "changed") {
		t.Fatal("tool wire aliases input")
	}
}

func TestAlignmentRawSourceChainIsIndependent(t *testing.T) {
	source := &adaptor.EventSourceMeta{RunID: "origin"}
	for i := 1; i < 8; i++ {
		source = &adaptor.EventSourceMeta{RunID: fmt.Sprint(i), ScopeID: "s", ToolCallID: "t", InvocationID: "i", DelegationID: "d", Sequence: uint64(i), Upstream: source}
	}
	meta := adaptor.EventMeta{RunID: "r", Sequence: 20, Source: source}
	ev := adaptor.WithEventMeta(adaptor.TodoUpdated{Snapshot: todo.Snapshot{Items: []todo.Item{}, Source: todo.PlanUpdate, Revision: 2}}, meta)
	_, body := rawFrameFor(ev)
	source.Upstream.RunID = "changed"
	wire := body.(map[string]any)["meta"].(map[string]any)["source"].(map[string]any)
	count := 1
	for wire["upstream"] != nil {
		wire = wire["upstream"].(map[string]any)
		count++
	}
	if count != 8 || wire["run_id"] != "origin" {
		t.Fatalf("lost source chain %d %#v", count, wire)
	}
	data, _ := json.Marshal(body)
	if strings.Contains(string(data), "changed") {
		t.Fatal("wire aliases source")
	}
}

func TestAlignmentRawInvalidSourceDegradesExplicitly(t *testing.T) {
	source := &adaptor.EventSourceMeta{}
	source.Upstream = source
	ev := adaptor.WithEventMeta(adaptor.TodoUpdated{}, adaptor.EventMeta{RunID: "r", Sequence: 2, Source: source})
	name, body := rawFrameFor(ev)
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	if name != adaptor.NoticeRuntime || !strings.Contains(string(data), "relay_depth_exceeded") {
		t.Fatalf("invalid source silently projected: %s %s", name, data)
	}
}
