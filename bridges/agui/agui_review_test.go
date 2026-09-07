package agui

import (
	"encoding/base64"
	"encoding/json"
	aguievents "github.com/ag-ui-protocol/ag-ui/sdks/community/go/pkg/core/events"
	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/capability"
	"github.com/agent-dance/agent-adaptor/todo"
	"math"
	"strings"
	"testing"
	"time"
)

func TestReviewerAGUIExactCustomAndTuple(t *testing.T) {
	at := time.Date(2026, 9, 7, 0, 0, 0, 0, time.UTC)
	zero := time.Duration(0)
	meta := adaptor.EventMeta{RunID: "汉<&\u2028", Sequence: math.MaxUint64, Time: at, Source: &adaptor.EventSourceMeta{RunID: "child", Sequence: 44, ScopeID: "cs", InvocationID: "i", Upstream: &adaptor.EventSourceMeta{RunID: "origin", Sequence: 5}}}
	tr := NewEventTranslator()
	tr.Translate(adaptor.RunStarted{RunID: meta.RunID})
	cap := adaptor.CapabilityInvocation{Invocation: capability.Invocation{InvocationID: "i", Ref: capability.Ref{Kind: capability.MCP, Key: "k", Operation: "read"}, Phase: capability.Completed, Source: capability.Provider, Evidence: capability.ProviderProtocol, OccurredAt: at, Duration: &zero}}
	out := tr.Translate(adaptor.WithEventMeta(cap, meta))
	zero = time.Second
	if len(out) != 1 {
		t.Fatal(out)
	}
	raw, err := json.Marshal(out[0])
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]json.RawMessage
	json.Unmarshal(raw, &wire)
	var name string
	json.Unmarshal(wire["name"], &name)
	if name != "adapter.capability.invocation" {
		t.Fatalf("name=%s %s", name, raw)
	}
	var value map[string]json.RawMessage
	json.Unmarshal(wire["value"], &value)
	if value["schema"] != nil || value["meta"] == nil || value["capability"] == nil || !strings.Contains(string(raw), `"duration_ns":0`) || !strings.Contains(string(raw), `"sequence":18446744073709551615`) || !strings.Contains(string(raw), `"upstream"`) {
		t.Fatal(string(raw))
	}
	clear := adaptor.WithEventMeta(adaptor.TodoUpdated{Snapshot: todo.Snapshot{Items: []todo.Item{}, Source: todo.PlanUpdate, Revision: 2, OccurredAt: at}}, meta)
	raw, _ = json.Marshal(tr.Translate(clear))
	if !strings.Contains(string(raw), `"name":"adapter.todo.updated"`) || !strings.Contains(string(raw), `"items":[]`) {
		t.Fatal(string(raw))
	}
	seen := map[string]bool{}
	for _, scope := range []string{"", "a/b", "a|b"} {
		tool := adaptor.WithEventMeta(adaptor.ToolCall{ID: "x\"\\", ScopeID: scope, ParentScopeID: "p", ParentToolCallID: "parent", Phase: adaptor.PhaseStart}, meta)
		out = tr.Translate(tool)
		if len(out) != 2 {
			t.Fatal(out)
		}
		parent, _ := json.Marshal(out[0])
		if !strings.Contains(string(parent), `"name":"adapter.tool.parent"`) || !strings.Contains(string(parent), `"parent_tool_call_id":"parent"`) || !strings.Contains(string(parent), `"sequence":18446744073709551615`) {
			t.Fatal(string(parent))
		}
		started, ok := out[1].(*aguievents.ToolCallStartEvent)
		if !ok {
			t.Fatalf("parent not followed by tool start: %#v", out)
		}
		tuple, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(started.ToolCallID, "aa1:"))
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		if err := json.Unmarshal(tuple, &got); err != nil || len(got) != 4 || got[0] != "tool" || got[1] != meta.RunID || got[2] != scope || got[3] != "x\"\\" {
			t.Fatalf("collision/corrupt tuple: %s %v", tuple, err)
		}
		if strings.Contains(string(tuple), `\u003c`) || strings.Contains(string(tuple), `\u0026`) || !strings.Contains(string(tuple), `\u2028`) || strings.HasSuffix(string(tuple), "\n") || seen[started.ToolCallID] {
			t.Fatalf("wrong canonical vector: %s", tuple)
		}
		seen[started.ToolCallID] = true
		result := tr.Translate(adaptor.WithEventMeta(adaptor.ToolResult{ID: "x\"\\", ScopeID: scope, Result: map[string]any{"text": "done"}}, meta))
		if result[0].(*aguievents.ToolCallResultEvent).ToolCallID != started.ToolCallID {
			t.Fatal("result lost scoped identity")
		}
	}
}

func TestReviewerAGUIApprovalCustomSnapshotDoesNotAlias(t *testing.T) {
	tr := NewEventTranslator(WithEventDecisionMode(DecisionAsCustom))
	tr.Translate(adaptor.RunStarted{RunID: "r"})
	req := &adaptor.ApprovalRequest{ID: "q", Kind: adaptor.ApprovalQuestion, Details: map[string]any{"nested": []any{"original"}}, Choices: []adaptor.Choice{{Key: "yes", Label: "Original"}}}
	out := tr.Translate(req)
	req.Details["nested"].([]any)[0] = "MUTATED_DETAILS"
	req.Choices[0].Label = "MUTATED_CHOICE"
	raw, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "MUTATED") {
		t.Errorf("approval custom event aliases input: %s", raw)
	}
}
