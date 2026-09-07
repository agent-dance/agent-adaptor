package claude

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/agent-dance/agent-adaptor/capability"
	"github.com/agent-dance/agent-adaptor/todo"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agent-dance/agent-adaptor/driver"
)

func TestAlignmentClaudeWrapperLifecycle(t *testing.T) {
	sink := &streamSink{}
	p := newClaudeParser(sink)
	p.enableStreaming("adoption")
	data := `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"parent","name":"Agent","input":{}}]}}
{"type":"assistant","parent_tool_use_id":"parent","message":{"content":[{"type":"tool_use","id":"child","name":"Read","input":{"path":"hello"}}]}}
{"type":"assistant","parent_tool_use_id":"parent","message":{"content":[{"type":"tool_use","id":"child","name":"Read","input":{"path":"hello"}}]}}
{"type":"user","parent_tool_use_id":"parent","message":{"content":[{"type":"tool_result","tool_use_id":"child","content":"hello"}]}}
`
	_ = p.onChunk("stdout", []byte(data), time.Now().UTC())
	p.finalize()
	p.completeStream(nil, 0, "", false)
	counts := map[driver.StreamKind]int{}
	for _, e := range sink.snapshot() {
		if e.ToolCallID != "child" {
			continue
		}
		counts[e.Kind]++
		if e.ScopeID == "" || e.ParentScopeID != "" || e.ParentToolCallID != "parent" {
			t.Fatalf("lost parent: %+v", e)
		}
	}
	if counts[driver.StreamToolCallStart] != 1 || counts[driver.StreamToolCallEnd] != 1 || counts[driver.StreamToolCallResult] != 1 || counts[driver.StreamToolCallArgs] != 0 {
		t.Fatalf("wrapper lifecycle: %+v", counts)
	}
}

func TestAlignmentClaudeAppendRejectsHiddenOverride(t *testing.T) {
	_, err := buildClaudeExecArgs(Config{CommonConfig: CommonConfig{ExtraArgs: []string{"--system-prompt=hidden"}}}, driver.Request{}, false)
	if err == nil {
		t.Fatal("hidden system override accepted")
	}
}

// All frames below use formal stream-json wrapper fields. These helpers only
// encode fixtures and make no decisions about the events expected from them.
func alignmentTool(id, name, parent string, input any) string {
	return alignmentJSON(map[string]any{"type": "assistant", "parent_tool_use_id": parent, "message": map[string]any{"content": []any{map[string]any{"type": "tool_use", "id": id, "name": name, "input": input}}}})
}
func alignmentToolResult(id, parent string, failed bool, output any) string {
	return alignmentJSON(map[string]any{"type": "user", "parent_tool_use_id": parent, "tool_use_result": output, "message": map[string]any{"content": []any{map[string]any{"type": "tool_result", "tool_use_id": id, "content": "tool output", "is_error": failed}}}})
}
func alignmentJSON(v any) string { b, _ := json.Marshal(v); return string(b) + "\n" }
func alignmentFeed(p *claudeParser, frames string) {
	_ = p.onChunk("stdout", []byte(frames), time.Now().UTC())
}
func alignmentAdoptionParser(req driver.Request) (*claudeParser, *streamSink) {
	sink := &streamSink{}
	p := newClaudeParser(sink)
	p.enableStreaming(req.RunID)
	p.configureObservations(req)
	return p, sink
}
func alignmentKinds(sink *streamSink, kind driver.StreamKind) []driver.StreamPayload {
	var out []driver.StreamPayload
	for _, e := range sink.snapshot() {
		if e.Kind == kind {
			out = append(out, e)
		}
	}
	return out
}

func TestAlignmentClaudeScopedReplayAndErrorTail(t *testing.T) {
	p, sink := alignmentAdoptionParser(driver.Request{RunID: "scope-run"})
	alignmentFeed(p, alignmentTool("p", "Agent", "", map[string]any{})+alignmentTool("q", "Agent", "", map[string]any{}))
	for _, parent := range []string{"p", "q"} {
		alignmentFeed(p, alignmentTool("x", "Read", parent, map[string]any{"path": "file"}))
	}
	// Bare result is ambiguous while both scopes have a pending x.
	alignmentFeed(p, `{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"x","content":"ambiguous"}]}}`+"\n")
	if len(alignmentKinds(sink, driver.StreamToolCallResult)) != 0 || !p.tools.notices["tool_result_ambiguous"] {
		t.Fatal("ambiguous result was paired")
	}
	for _, parent := range []string{"p", "q"} {
		alignmentFeed(p, alignmentToolResult("x", parent, false, nil))
	}
	// Unresolved/malformed parent and missing IDs never fabricate a root call.
	alignmentFeed(p, alignmentTool("lost", "Read", "missing", map[string]any{})+alignmentTool("", "Read", "", map[string]any{}))
	alignmentFeed(p, `{"type":"assistant","parent_tool_use_id":5,"message":{"content":[{"type":"tool_use","id":"invalid","name":"Read","input":{}}]}}`+"\n")
	alignmentFeed(p, alignmentTool("x", "Other", "p", map[string]any{}))
	if !p.tools.notices["tool_conflict"] || !p.tools.notices["parent_unresolved"] {
		t.Fatal("missing safe degradation")
	}
	// Interleaved partial blocks with identical index are independently scoped.
	for _, parent := range []string{"p", "q"} {
		alignmentFeed(p, alignmentJSON(map[string]any{"type": "stream_event", "parent_tool_use_id": parent, "event": map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "tool_use", "id": "open", "name": "Read", "input": map[string]any{}}}}))
	}
	alignmentFeed(p, "{broken")
	p.finalize()
	p.completeStream(p.failureForOutcome(-1, "", false), -1, "", false)
	starts, ends := alignmentKinds(sink, driver.StreamToolCallStart), alignmentKinds(sink, driver.StreamToolCallEnd)
	if len(starts) != 6 || len(ends) != 6 {
		t.Fatalf("lifecycle counts %d/%d", len(starts), len(ends))
	}
	for i := 4; i < 6; i++ {
		if starts[i].ScopeID != ends[i].ScopeID || starts[i].ToolCallID != ends[i].ToolCallID {
			t.Fatal("error-tail order differs from starts")
		}
	}
	results := alignmentKinds(sink, driver.StreamToolCallResult)
	if len(results) != 2 || results[0].ScopeID == results[1].ScopeID {
		t.Fatalf("scope lost: %+v", results)
	}
	if !p.protocolMalformed || p.checkpointForOutcome(-1, "", false, nil) != nil {
		t.Fatal("malformed tail checkpoint")
	}
}

func TestAlignmentClaudeIncrementalWrapperDedupe(t *testing.T) {
	p, sink := alignmentAdoptionParser(driver.Request{RunID: "incremental"})
	frames := `{"type":"stream_event","event":{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"t","name":"Read","input":{}}}}
{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"path\":"}}}
{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"\"file\"}"}}}
{"type":"stream_event","event":{"type":"content_block_stop","index":0}}
`
	alignmentFeed(p, frames+alignmentTool("t", "Read", "", map[string]any{"path": "file"})+frames+alignmentTool("t", "Read", "", map[string]any{"path": "file"}))
	starts := alignmentKinds(sink, driver.StreamToolCallStart)
	if len(starts) != 1 || starts[0].Args != nil || len(alignmentKinds(sink, driver.StreamToolCallArgs)) != 2 || len(alignmentKinds(sink, driver.StreamToolCallEnd)) != 1 {
		t.Fatal("duplicated args/lifecycle")
	}
	count := 0
	for _, item := range p.transcript {
		if item.Kind == driver.TranscriptToolCall {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("transcript replays=%d", count)
	}
}

func TestAlignmentClaudeBareResultAfterScopedCompletion(t *testing.T) {
	p, sink := alignmentAdoptionParser(driver.Request{RunID: "late-replay"})
	alignmentFeed(p, alignmentTool("p", "Agent", "", map[string]any{})+alignmentTool("q", "Agent", "", map[string]any{}))
	for _, parent := range []string{"p", "q"} {
		alignmentFeed(p, alignmentTool("same", "Read", parent, map[string]any{}))
	}
	alignmentFeed(p, alignmentToolResult("same", "p", false, nil))
	// This can be a replay from p. The only unfinished candidate q is not
	// enough evidence to assign a result without its parent coordinates.
	alignmentFeed(p, `{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"same","content":"tool output","is_error":false}]}}`+"\n")
	if len(alignmentKinds(sink, driver.StreamToolCallResult)) != 1 || !p.tools.notices["tool_result_ambiguous"] {
		t.Fatal("bare replay was attributed to the remaining unfinished scope")
	}
	alignmentFeed(p, alignmentToolResult("same", "q", false, nil))
	if len(alignmentKinds(sink, driver.StreamToolCallResult)) != 2 {
		t.Fatal("explicitly scoped result lost after ambiguous replay")
	}
}

func TestAlignmentClaudeCapabilityCatalog(t *testing.T) {
	req := driver.Request{RunID: "catalog", Skills: driver.ResolvedSkills{Entries: []driver.ResolvedSkill{{Key: "skill/key", RuntimeName: "review"}}}, MCP: driver.MCPPayload{Servers: []driver.MCPServerSpec{{Key: "灵山 知识"}, {Key: "_srv_"}, {Key: "a.b"}, {Key: "a_b"}, {Key: "a"}, {Key: "a__b"}}}, ProfilePayload: driver.ProfilePayload{Agents: driver.AgentPayload{Agents: []driver.AgentSpec{{Key: "agent/key", RuntimeName: "planner"}}}}}
	p, sink := alignmentAdoptionParser(req)
	calls := []struct {
		name    string
		input   map[string]any
		key, op string
	}{
		{"Skill", map[string]any{"skill": "review", "args": "SECRET"}, "skill/key", "activate"},
		{"Agent", map[string]any{"subagent_type": "planner", "prompt": "SECRET"}, "agent/key", "spawn"},
		{"mcp__" + claudeMCPAlias("灵山 知识") + "__search", map[string]any{"query": "SECRET"}, "灵山 知识", "search"},
		{"mcp___srv____search", map[string]any{}, "_srv_", "_search"},
	}
	for i, c := range calls {
		id := fmt.Sprint(i)
		alignmentFeed(p, alignmentTool(id, c.name, "", c.input)+alignmentToolResult(id, "", i == 1, nil))
	}
	for _, c := range []struct {
		name  string
		input map[string]any
	}{{"Explore", map[string]any{}}, {"Skill", map[string]any{"skill": " review"}}, {"mcp__a_b__search", map[string]any{}}, {"mcp__a__b__search", map[string]any{}}} {
		alignmentFeed(p, alignmentTool(c.name, c.name, "", c.input))
	}
	facts := alignmentKinds(sink, driver.StreamCapabilityInvocation)
	if len(facts) != 8 {
		t.Fatalf("facts=%+v", facts)
	}
	for i, c := range calls {
		start, end := facts[2*i].Capability, facts[2*i+1].Capability
		if start.Ref.Key != c.key || start.Ref.Operation != c.op || start.Phase != capability.Started || start.OccurredAt.IsZero() || start.Duration != nil || end.Duration != nil {
			t.Fatalf("fact=%+v %+v", start, end)
		}
		if i == 1 && end.Phase != capability.Failed || i != 1 && end.Phase != capability.Completed {
			t.Fatalf("terminal=%+v", end)
		}
		encoded, _ := json.Marshal([]any{start, end})
		if strings.Contains(string(encoded), "SECRET") {
			t.Fatal("capability leaked payload")
		}
	}
	if !p.tools.notices["capability_ambiguous"] {
		t.Fatal("ambiguity hidden")
	}
	// Duplicate catalog names are tombstoned independently of insertion order.
	for _, reverse := range []bool{false, true} {
		entries := []driver.ResolvedSkill{{Key: "a", RuntimeName: "same"}, {Key: "a", RuntimeName: "same"}, {Key: "b", RuntimeName: "same"}}
		if reverse {
			entries[0], entries[2] = entries[2], entries[0]
		}
		p, s := alignmentAdoptionParser(driver.Request{RunID: "duplicate", Skills: driver.ResolvedSkills{Entries: entries}})
		alignmentFeed(p, alignmentTool("x", "Skill", "", map[string]any{"skill": "same"}))
		if len(alignmentKinds(s, driver.StreamCapabilityInvocation)) != 0 {
			t.Fatal("catalog collision guessed")
		}
	}
}

func TestAlignmentClaudeCapabilityCloseAndConcurrentDispatch(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		p, sink := alignmentAdoptionParser(driver.Request{RunID: "parallel", MCP: driver.MCPPayload{Servers: []driver.MCPServerSpec{{Key: "mcp"}}}})
		var wg sync.WaitGroup
		for i := 0; i < 20; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				alignmentFeed(p, alignmentTool(fmt.Sprint(i), "mcp__mcp__op", "", map[string]any{}))
			}(i)
		}
		wg.Wait()
		var cause error
		if cancelled {
			cause = context.Canceled
		}
		p.closeObservations(cause)
		p.completeStream(nil, 0, "", false)
		facts := alignmentKinds(sink, driver.StreamCapabilityInvocation)
		if len(facts) != 40 {
			t.Fatalf("facts=%d", len(facts))
		}
		for i := 0; i < 20; i++ {
			a, b := facts[i].Capability, facts[i+20].Capability
			if a.InvocationID != b.InvocationID || a.Phase != capability.Started || b.Phase != map[bool]capability.Phase{true: capability.Cancelled, false: capability.Interrupted}[cancelled] {
				t.Fatalf("order=%+v %+v", a, b)
			}
		}
	}
}

func TestAlignmentClaudeConfirmedTodos(t *testing.T) {
	p, sink := alignmentAdoptionParser(driver.Request{RunID: "todos"})
	create := map[string]any{"subject": "实现 中文"}
	alignmentFeed(p, alignmentTool("unconfirmed", "TaskCreate", "", create)+alignmentTool("failed", "TaskCreate", "", create)+alignmentToolResult("failed", "", true, map[string]any{"task": map[string]any{"id": "1"}}))
	if len(alignmentKinds(sink, driver.StreamTodoUpdated)) != 0 {
		t.Fatal("request/failed create became success")
	}
	alignmentFeed(p, alignmentTool("real", "TaskCreate", "", create)+alignmentToolResult("real", "", false, map[string]any{"task": map[string]any{"id": "91", "subject": "实现 中文"}}))
	alignmentFeed(p, alignmentTool("synthetic", "TaskCreate", "", map[string]any{"subject": "synthetic"})+alignmentToolResult("synthetic", "", false, nil))
	alignmentFeed(p, alignmentTool("unknown", "TaskUpdate", "", map[string]any{"taskId": "1", "status": "completed"})+alignmentToolResult("unknown", "", false, nil))
	alignmentFeed(p, alignmentTool("update", "TaskUpdate", "", map[string]any{"taskId": "91", "status": "in_progress"})+alignmentToolResult("update", "", false, nil)+alignmentToolResult("update", "", false, nil))
	snapshots := alignmentKinds(sink, driver.StreamTodoUpdated)
	if len(snapshots) != 3 {
		t.Fatalf("snapshots=%+v", snapshots)
	}
	last := snapshots[2].Todo
	if len(last.Items) != 2 || last.Items[0].ID != "91" || last.Items[0].Status != todo.InProgress || last.Items[0].SyntheticID || !last.Items[1].SyntheticID || !strings.HasPrefix(last.Items[1].ID, "synthetic:") || last.Revision != 3 || !p.tools.notices["todo_unknown_id"] {
		t.Fatalf("snapshot=%+v", last)
	}
	// Failure outputs, unsupported status, oversize UTF-8 and conflicting IDs
	// must preserve the previous complete snapshot atomically.
	invalidInputs := []any{
		[]any{map[string]any{"content": "bad", "status": "unknown"}},
		[]any{map[string]any{"id": 42, "content": "bad ID", "status": "pending"}},
		[]any{map[string]any{"id": "same", "content": "a", "status": "pending"}, map[string]any{"id": "same", "content": "b", "status": "pending"}},
		[]any{map[string]any{"content": strings.Repeat("界", 1366), "status": "pending"}},
		nil,
	}
	for i, input := range invalidInputs {
		id := fmt.Sprintf("invalid-%d", i)
		alignmentFeed(p, alignmentTool(id, "TodoWrite", "", map[string]any{"todos": input})+alignmentToolResult(id, "", false, nil))
	}
	if len(alignmentKinds(sink, driver.StreamTodoUpdated)) != 3 {
		t.Fatal("invalid whole table partially accepted")
	}
	alignmentFeed(p, alignmentTool("clear", "TodoWrite", "", map[string]any{"todos": []any{}})+alignmentToolResult("clear", "", false, nil))
	alignmentFeed(p, alignmentTool("clear-replay", "TodoWrite", "", map[string]any{"todos": []any{}})+alignmentToolResult("clear-replay", "", false, nil))
	snapshots = alignmentKinds(sink, driver.StreamTodoUpdated)
	if len(snapshots) != 4 || snapshots[3].Todo.Items == nil || len(snapshots[3].Todo.Items) != 0 || snapshots[3].Todo.Revision != 4 {
		t.Fatalf("clear=%+v", snapshots)
	}
}

func TestAlignmentClaudeMalformedTodoFieldsPreserveTable(t *testing.T) {
	p, sink := alignmentAdoptionParser(driver.Request{RunID: "malformed-task"})
	alignmentFeed(p, alignmentTool("create", "TaskCreate", "", map[string]any{"subject": "keep"})+alignmentToolResult("create", "", false, map[string]any{"task": map[string]any{"id": "91"}}))
	alignmentFeed(p, alignmentTool("delete", "TaskUpdate", "", map[string]any{"taskId": "91", "subject": 42, "status": "deleted"})+alignmentToolResult("delete", "", false, nil))
	alignmentFeed(p, alignmentTool("invalid", "TaskCreate", "", map[string]any{"subject": "request"})+alignmentToolResult("invalid", "", false, map[string]any{"task": map[string]any{"id": "92", "subject": false}}))
	snapshots := alignmentKinds(sink, driver.StreamTodoUpdated)
	if len(snapshots) != 1 || snapshots[0].Todo.Items[0].ID != "91" || !p.tools.notices["todo_invalid"] {
		t.Fatal("malformed task fields changed the last confirmed table")
	}
}

func TestAlignmentClaudeTodoResumeScopeAndFullList(t *testing.T) {
	for _, runID := range []string{"turn-1", "turn-2"} {
		p, sink := alignmentAdoptionParser(driver.Request{RunID: runID})
		alignmentFeed(p, alignmentTool("update", "TaskUpdate", "", map[string]any{"taskId": "91", "status": "completed"})+alignmentToolResult("update", "", false, nil))
		if len(alignmentKinds(sink, driver.StreamTodoUpdated)) != 0 {
			t.Fatal("resumed table inferred from previous run")
		}
		alignmentFeed(p, alignmentTool("list", "TaskList", "", map[string]any{})+alignmentToolResult("list", "", false, map[string]any{"tasks": []any{map[string]any{"id": "91", "subject": "restored", "status": "pending"}}}))
		alignmentFeed(p, alignmentTool("parent", "Agent", "", map[string]any{})+alignmentTool("clear-child", "TodoWrite", "parent", map[string]any{"todos": []any{}})+alignmentToolResult("clear-child", "parent", false, nil))
		alignmentFeed(p, alignmentTool("delete", "TaskUpdate", "", map[string]any{"taskId": "91", "status": "deleted"})+alignmentToolResult("delete", "", false, nil))
		snapshots := alignmentKinds(sink, driver.StreamTodoUpdated)
		if len(snapshots) != 3 {
			t.Fatalf("snapshots=%d", len(snapshots))
		}
		if len(snapshots[0].Todo.Items) != 1 || snapshots[0].Todo.ScopeID != "" || snapshots[1].Todo.Revision != 1 || snapshots[1].Todo.ScopeID == "" || snapshots[1].Todo.ParentToolCallID != "parent" || snapshots[1].Todo.Items == nil || snapshots[2].Todo.Revision != 2 || snapshots[2].Todo.ScopeID != "" || len(snapshots[2].Todo.Items) != 0 {
			t.Fatalf("scope/restore=%+v", snapshots)
		}
	}
}

func TestAlignmentClaudeUsageSameMessageIDAcrossParents(t *testing.T) {
	p, _ := alignmentAdoptionParser(driver.Request{RunID: "usage"})
	alignmentFeed(p, alignmentTool("p", "Agent", "", map[string]any{}))
	for _, parent := range []string{"", "p"} {
		alignmentFeed(p, alignmentJSON(map[string]any{"type": "stream_event", "parent_tool_use_id": parent, "event": map[string]any{"type": "message_start", "message": map[string]any{"id": "same", "usage": map[string]any{"input_tokens": 10}}}}))
		for i := 0; i < 2; i++ {
			alignmentFeed(p, alignmentJSON(map[string]any{"type": "assistant", "parent_tool_use_id": parent, "message": map[string]any{"id": "same", "usage": map[string]any{"input_tokens": 10, "output_tokens": 3}, "content": []any{}}}))
		}
	}
	usage := p.observedUsage()
	if usage == nil || usage.InputTokens != 20 || usage.OutputTokens != 6 {
		t.Fatalf("parent partition=%+v", usage)
	}
}

func TestAlignmentClaudeTodoPartialReplayAndMalformedResult(t *testing.T) {
	p, sink := alignmentAdoptionParser(driver.Request{RunID: "partial-todo"})
	frames := `{"type":"stream_event","event":{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"t","name":"TodoWrite","input":{}}}}
{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"todos\":[]}"}}}
{"type":"stream_event","event":{"type":"content_block_stop","index":0}}
`
	alignmentFeed(p, frames+alignmentTool("t", "TodoWrite", "", map[string]any{"todos": []any{}}))
	if len(alignmentKinds(sink, driver.StreamTodoUpdated)) != 0 {
		t.Fatal("input End committed todo")
	}
	alignmentFeed(p, alignmentToolResult("t", "", false, nil)+alignmentToolResult("t", "", false, nil))
	if snapshots := alignmentKinds(sink, driver.StreamTodoUpdated); len(snapshots) != 1 || snapshots[0].Todo.Items == nil || snapshots[0].Todo.Revision != 1 {
		t.Fatal("empty confirmation/replay lost")
	}
	alignmentFeed(p, alignmentTool("bad", "TodoWrite", "", map[string]any{"todos": []any{}})+`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"bad","is_error":"false","content":"invalid"}]}}`+"\n")
	if len(alignmentKinds(sink, driver.StreamTodoUpdated)) != 1 || len(alignmentKinds(sink, driver.StreamToolCallResult)) != 1 || !p.tools.notices["tool_result_invalid"] {
		t.Fatal("malformed result emitted success")
	}
}

func TestAlignmentClaudeTodoUTF8Wire(t *testing.T) {
	p, sink := alignmentAdoptionParser(driver.Request{RunID: "utf8"})
	content := strings.Repeat("界", 1365) + "a"
	alignmentFeed(p, alignmentTool("valid", "TodoWrite", "", map[string]any{"todos": []any{map[string]any{"content": content, "status": "pending"}}})+alignmentToolResult("valid", "", false, nil))
	if snapshots := alignmentKinds(sink, driver.StreamTodoUpdated); len(snapshots) != 1 || len(snapshots[0].Todo.Items[0].Content) != 4096 {
		t.Fatal("valid UTF-8 byte boundary lost")
	}
	malformed := `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"bad","name":"TodoWrite","input":{"todos":[{"content":"` + string([]byte{0xff}) + `","status":"pending"}]}}]}}` + "\n"
	alignmentFeed(p, malformed+alignmentToolResult("bad", "", false, nil))
	if !p.protocolMalformed || len(alignmentKinds(sink, driver.StreamTodoUpdated)) != 1 {
		t.Fatal("invalid UTF-8 repaired into confirmed state")
	}
}
