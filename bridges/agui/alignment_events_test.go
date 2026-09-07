package agui

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	aguievents "github.com/ag-ui-protocol/ag-ui/sdks/community/go/pkg/core/events"
	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/capability"
	"github.com/agent-dance/agent-adaptor/todo"
	"strings"
	"testing"
	"time"
)

func TestAlignmentCustomObservationsAndScopedTools(t *testing.T) {
	at := time.Date(2026, 9, 7, 0, 0, 0, 0, time.UTC)
	meta := adaptor.EventMeta{RunID: "r<&", Sequence: 9, Time: at, Source: &adaptor.EventSourceMeta{InvocationID: "origin", Upstream: &adaptor.EventSourceMeta{RunID: "up"}}}
	tr := NewEventTranslator()
	tr.Translate(adaptor.RunStarted{RunID: meta.RunID})
	for _, c := range []struct {
		name, fragment string
		ev             adaptor.Event
	}{
		{"adapter.capability.invocation", `"invocation_id":"i"`, adaptor.CapabilityInvocation{Invocation: capability.Invocation{InvocationID: "i", Ref: capability.Ref{Kind: capability.Skill, Key: "review", Operation: "activate"}, Phase: capability.Started, Source: capability.Provider, Evidence: capability.ProviderProtocol, OccurredAt: at}}},
		{"adapter.todo.updated", `"items":[]`, adaptor.TodoUpdated{Snapshot: todo.Snapshot{Items: []todo.Item{}, Source: todo.PlanUpdate, Revision: 2, OccurredAt: at}}},
	} {
		t.Run(c.name, func(t *testing.T) {
			out := tr.Translate(adaptor.WithEventMeta(c.ev, meta))
			if len(out) != 1 {
				t.Fatalf("events=%#v", out)
			}
			data, err := json.Marshal(out[0])
			if err != nil {
				t.Fatal(err)
			}
			for _, frag := range []string{c.name, c.fragment, `"sequence":9`, `"upstream"`} {
				if !strings.Contains(string(data), frag) {
					t.Fatalf("missing %s: %s", frag, data)
				}
			}
		})
	}
	for _, scope := range []string{"a", "b"} {
		out := tr.Translate(adaptor.WithEventMeta(adaptor.ToolCall{ID: "x", ScopeID: scope, ParentScopeID: "p", ParentToolCallID: "parent", Name: "read", Phase: adaptor.PhaseStart}, meta))
		if len(out) != 2 {
			t.Fatalf("scope %s output=%#v", scope, out)
		}
		data, _ := json.Marshal(out)
		if !strings.Contains(string(data), "adapter.tool.parent") || !strings.Contains(string(data), "aa1:") {
			t.Fatalf("missing parent/domain: %s", data)
		}
	}
}

func TestAlignmentAGUICopiesFullObservationsAndKeepsClear(t *testing.T) {
	at := time.Date(2026, 9, 7, 0, 0, 0, 0, time.UTC)
	zero := time.Duration(0)
	tr := NewEventTranslator()
	tr.Translate(adaptor.RunStarted{RunID: "r"})
	invocation := adaptor.CapabilityInvocation{Invocation: capability.Invocation{InvocationID: "i", Ref: capability.Ref{Kind: capability.MCP, Key: "k", Operation: "search"}, Phase: capability.Completed, Evidence: capability.Relayed, Source: capability.Relay, OccurredAt: at, Duration: &zero}}
	out := tr.Translate(invocation)
	*invocation.Invocation.Duration = time.Second
	encoded, _ := json.Marshal(out)
	if !strings.Contains(string(encoded), `"duration_ns":0`) {
		t.Fatalf("duration aliases input: %s", encoded)
	}
	items := make([]todo.Item, 20)
	content := strings.Repeat("灵", 1365) + "a"
	for i := range items {
		items[i] = todo.Item{ID: fmt.Sprint(i), Content: content, Status: todo.Pending}
	}
	ev := adaptor.TodoUpdated{Snapshot: todo.Snapshot{Items: items, Source: todo.ToolResult, Revision: 1, OccurredAt: at}}
	out = tr.Translate(ev)
	ev.Snapshot.Items[0].Content = "changed"
	encoded, _ = json.Marshal(out)
	if len(encoded) <= 65536 || strings.Contains(string(encoded), "changed") {
		t.Fatal("snapshot was truncated or aliases input")
	}
	var wire []struct {
		Value struct {
			Todo todoWire `json:"todo"`
		} `json:"value"`
	}
	if err := json.Unmarshal(encoded, &wire); err != nil {
		t.Fatal(err)
	}
	if len(wire) != 1 || wire[0].Value.Todo.Items[0].Content != content {
		t.Fatal("lost UTF-8 content")
	}
	clear := tr.Translate(adaptor.TodoUpdated{Snapshot: todo.Snapshot{Source: todo.PlanUpdate, Revision: 2, OccurredAt: at}})
	encoded, _ = json.Marshal(clear)
	if !strings.Contains(string(encoded), `"items":[]`) {
		t.Fatalf("clear=%s", encoded)
	}
}

func TestAlignmentAGUIToolTupleAndPrimaryTerminal(t *testing.T) {
	tr := NewEventTranslator()
	tr.Translate(adaptor.RunStarted{RunID: "r<&"})
	out := tr.Translate(adaptor.ToolCall{ID: "x", ScopeID: "s", ParentScopeID: "p", ParentToolCallID: "parent", Phase: adaptor.PhaseStart})
	started := out[1].(*aguievents.ToolCallStartEvent)
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(started.ToolCallID, "aa1:"))
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != `["tool","r<&","s","x"]` {
		t.Fatalf("tuple not canonical=%s", raw)
	}
	result := tr.Translate(adaptor.ToolResult{ID: "x", ScopeID: "s", Result: map[string]any{"text": "ok"}})
	if result[0].(*aguievents.ToolCallResultEvent).ToolCallID != started.ToolCallID {
		t.Fatal("result mismatched scope")
	}
	tr.Translate(adaptor.RunFinished{RunID: "r<&"})
	tail := tr.CloseResult(nil, &adaptor.RunError{Reason: adaptor.ReasonApprovalDenied, Message: "denied", Cause: context.Canceled, Result: &adaptor.Result{Text: "partial"}})
	terminal := tail[len(tail)-1].(*aguievents.RunErrorEvent)
	if terminal.Code == nil || *terminal.Code != string(adaptor.ReasonApprovalDenied) {
		t.Fatalf("secondary cancel overrode primary: %#v", terminal)
	}
	if len(tr.Translate(adaptor.TodoUpdated{})) != 0 || len(tr.CloseRun(nil)) != 0 {
		t.Fatal("post-terminal traffic")
	}
}

func TestAlignmentAGUIInvalidSourceDegradesExplicitly(t *testing.T) {
	source := &adaptor.EventSourceMeta{}
	source.Upstream = source
	ev := adaptor.WithEventMeta(adaptor.TodoUpdated{}, adaptor.EventMeta{RunID: "r", Sequence: 2, Source: source})
	tr := NewEventTranslator()
	tr.Translate(adaptor.RunStarted{RunID: "r"})
	data, err := json.Marshal(tr.Translate(ev))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "relay_depth_exceeded") || strings.Contains(string(data), "adapter.todo.updated") {
		t.Fatalf("invalid source silently projected: %s", data)
	}
}
