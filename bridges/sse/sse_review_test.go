package sse

import (
	"encoding/json"
	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/capability"
	"github.com/agent-dance/agent-adaptor/todo"
	"math"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestReviewerRawSSEExactValueAndFrame(t *testing.T) {
	at := time.Date(2026, 9, 7, 1, 2, 3, 4, time.UTC)
	zero := time.Duration(0)
	meta := adaptor.EventMeta{RunID: "r\n汉", Sequence: math.MaxUint64, Time: at, ThreadKey: "host/key", TurnID: "t", Source: &adaptor.EventSourceMeta{RunID: "child", ThreadID: "ct", TurnID: "c-turn", Sequence: 99, Timestamp: at, ScopeID: "cs", ToolCallID: "tool", InvocationID: "inv", DelegationID: "del", Upstream: &adaptor.EventSourceMeta{RunID: "origin", Sequence: 2}}}
	input := adaptor.WithEventMeta(adaptor.CapabilityInvocation{Invocation: capability.Invocation{InvocationID: "i", Ref: capability.Ref{Kind: capability.MCP, Key: "k", Operation: "search"}, Phase: capability.Completed, Source: capability.Provider, Evidence: capability.ProviderProtocol, ScopeID: "s", ParentScopeID: "p", ParentToolCallID: "parent", OccurredAt: at, Duration: &zero}}, meta)
	name, body := rawFrameFor(input)
	zero = time.Second
	rec := httptest.NewRecorder()
	if err := writeRawFrame(rec, rec, name, meta.Sequence, body); err != nil {
		t.Fatal(err)
	}
	raw := rec.Body.String()
	if !strings.Contains(raw, "event: capability.invocation\n") || !strings.Contains(raw, "id: 18446744073709551615\n") || !strings.HasSuffix(raw, "\n\n") || strings.Count(raw, "data:") != 1 {
		t.Fatalf("bad SSE frame: %s", raw)
	}
	var payload map[string]json.RawMessage
	start := strings.Index(raw, "data: ")
	if start < 0 {
		t.Fatal(raw)
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw[start+6:])), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload) != 2 || payload["meta"] == nil || payload["capability"] == nil {
		t.Fatalf("wrong envelope: %s", raw)
	}
	var cap map[string]any
	json.Unmarshal(payload["capability"], &cap)
	for k, v := range map[string]any{"invocation_id": "i", "kind": "mcp", "key": "k", "operation": "search", "phase": "completed", "source": "provider", "evidence": "provider_protocol", "scope_id": "s", "parent_scope_id": "p", "parent_tool_call_id": "parent", "duration_ns": float64(0)} {
		if cap[k] != v {
			t.Errorf("%s got=%v want=%v", k, cap[k], v)
		}
	}
	if strings.Contains(raw, `"Invocation"`) || !strings.Contains(raw, `"upstream":{"`) || !strings.Contains(raw, `"invocation_id":"inv"`) {
		t.Fatal("not complete private snake_case wire")
	}
	for _, items := range [][]todo.Item{nil, {}, {{ID: "a", Content: "line1\n汉\tline2", Status: todo.Pending}}} {
		ev := adaptor.WithEventMeta(adaptor.TodoUpdated{Snapshot: todo.Snapshot{Items: items, Source: todo.PlanUpdate, Revision: math.MaxUint64, OccurredAt: at}}, meta)
		name, body = rawFrameFor(ev)
		data, err := json.Marshal(body)
		if err != nil || name != "todo.updated" {
			t.Fatal(err)
		}
		if len(items) == 0 && !strings.Contains(string(data), `"items":[]`) {
			t.Fatal("clear omitted")
		}
		if len(items) > 0 {
			items[0].Content = "MUTATED"
			data, _ = json.Marshal(body)
			if strings.Contains(string(data), "MUTATED") {
				t.Fatal("snapshot aliases input")
			}
		}
	}
	tool := adaptor.ToolCall{ID: "x", ScopeID: "s", ParentScopeID: "p", ParentToolCallID: "parent", Args: map[string]any{"nested": []any{"keep"}}}
	_, body = rawFrameFor(tool)
	tool.Args["nested"].([]any)[0] = "MUTATED"
	data, _ := json.Marshal(body)
	for _, fragment := range []string{`"scope_id":"s"`, `"parent_scope_id":"p"`, `"parent_tool_call_id":"parent"`, `"nested":["keep"]`} {
		if !strings.Contains(string(data), fragment) {
			t.Fatal(string(data))
		}
	}
}

func TestReviewerSSEApprovalSnapshotDoesNotAlias(t *testing.T) {
	req := &adaptor.ApprovalRequest{ID: "q", Kind: adaptor.ApprovalQuestion, Details: map[string]any{"nested": []any{"original"}}, Choices: []adaptor.Choice{{Key: "yes", Label: "Original"}}}
	_, body := rawFrameFor(req)
	req.Details["nested"].([]any)[0] = "MUTATED_DETAILS"
	req.Choices[0].Label = "MUTATED_CHOICE"
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "MUTATED") {
		t.Errorf("approval frame aliases input: %s", data)
	}
}
