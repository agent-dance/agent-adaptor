package codebuddy

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agent-dance/agent-adaptor/capability"
	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/testutil"
	"github.com/agent-dance/agent-adaptor/todo"
)

func alignmentObservationRequest() driver.Request {
	return driver.Request{RunID: "alignment/观测", Skills: driver.ResolvedSkills{Entries: []driver.ResolvedSkill{{Key: "skill/review", RuntimeName: " review "}}}, MCP: driver.MCPPayload{Servers: []driver.MCPServerSpec{{Key: "知识_库"}}}, ProfilePayload: driver.ProfilePayload{Agents: driver.AgentPayload{Agents: []driver.AgentSpec{{Key: "agent/planner", RuntimeName: "planner"}}}}}
}
func alignmentFeed(t *testing.T, p *parser, frames ...string) {
	t.Helper()
	for _, frame := range frames {
		if err := p.onChunk("stdout", []byte(frame+"\n"), time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
	}
}
func alignmentCall(name, id, input string) string {
	return fmt.Sprintf(`{"type":"assistant","parent_tool_use_id":null,"message":{"content":[{"type":"tool_use","id":%q,"name":%q,"input":%s}]}}`, id, name, input)
}
func alignmentResult(id, content string, failed bool, meta string) string {
	var tail string
	if meta != "" {
		tail = `,"_meta":{"rawResponse":` + meta + `}`
	}
	return fmt.Sprintf(`{"type":"user","parent_tool_use_id":%q,"message":{"content":[{"type":"tool_result","tool_use_id":%q,"content":%s,"is_error":%v%s}]}}`, id, id, content, failed, tail)
}
func alignmentCapabilities(rec *testutil.EventRecorder) []capability.Invocation {
	var out []capability.Invocation
	for _, p := range rec.StreamSnapshot() {
		if p.Capability != nil {
			out = append(out, *p.Capability)
		}
	}
	return out
}
func alignmentTodos(rec *testutil.EventRecorder) []todo.Snapshot {
	var out []todo.Snapshot
	for _, p := range rec.StreamSnapshot() {
		if p.Todo != nil {
			out = append(out, *p.Todo)
		}
	}
	return out
}
func alignmentNotice(rec *testutil.EventRecorder, code string) bool {
	for _, e := range rec.Snapshot() {
		if e.Data["code"] == code {
			return true
		}
	}
	return false
}

func TestAlignmentObservationOfficialLifecycle(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint(stream), func(t *testing.T) {
			rec := &testutil.EventRecorder{}
			p := newParser(rec)
			p.configureObservations(context.Background(), alignmentObservationRequest())
			if stream {
				p.enableStreaming(p.runID)
			}
			frames := []string{alignmentCall("Skill", "s", `{"command":" review "}`), alignmentCall("mcp__知识_库___search", "m", `{}`), alignmentCall("Task", "a", `{"subagent_type":"planner"}`), alignmentCall("Skill", "unknown", `{"skill":"review"}`)}
			alignmentFeed(t, p, frames...)
			alignmentFeed(t, p, frames...)
			success := alignmentResult("s", `[{"type":"text","text":"ok"}]`, false, "")
			alignmentFeed(t, p, success, success, alignmentResult("m", `"error body secret"`, true, ""), alignmentResult("a", `"ok"`, false, ""))
			p.completeStream(nil, 0, "", false)
			facts := alignmentCapabilities(rec)
			if len(facts) != 6 {
				t.Fatalf("facts=%#v", facts)
			}
			expected := []capability.Ref{{Kind: capability.Skill, Key: "skill/review", Operation: "activate"}, {Kind: capability.MCP, Key: "知识_库", Operation: "_search"}, {Kind: capability.Subagent, Key: "agent/planner", Operation: "spawn"}}
			for i, ref := range expected {
				if facts[i].Ref != ref || facts[i].Phase != capability.Started || facts[i+3].Ref != ref {
					t.Errorf("fact %d=%#v", i, facts)
				}
			}
			if facts[4].Phase != capability.Failed || facts[4].ErrorCode != capability.ToolFailed {
				t.Error("tool error not classified")
			}
			for _, f := range facts {
				if f.ScopeID != "" || f.ParentToolCallID != "" || f.Duration != nil || f.OccurredAt.IsZero() || f.OccurredAt.Location() != time.UTC || f.Source != capability.Provider || f.Evidence != capability.ProviderProtocol {
					t.Errorf("unsafe invented fact %#v", f)
				}
			}
			raw, _ := json.Marshal(facts)
			if strings.Contains(string(raw), "secret") {
				t.Error("result body leaked into fact")
			}
			if !alignmentNotice(rec, "capability_unresolved") {
				t.Error("unknown exact name not reported")
			}
		})
	}
}
func TestAlignmentObservationPartialWrapperAndControl(t *testing.T) {
	rec := &testutil.EventRecorder{}
	p := newParser(rec)
	p.configureObservations(context.Background(), alignmentObservationRequest())
	p.enableStreaming(p.runID)
	stdin := &controlTestStdin{}
	decisions := &controlTestSink{respond: func(r driver.DecisionRequest) driver.DecisionResponse {
		return driver.DecisionResponse{RequestID: r.RequestID, Result: driver.DecisionApproved}
	}}
	p.enableControl(context.Background(), decisions, stdin, p.runID, driver.HumanDecisionPolicy{}, "input")
	alignmentFeed(t, p, `{"type":"control_request","request_id":"ask","request":{"subtype":"can_use_tool","tool_name":"Skill","tool_use_id":"s","input":{"skill":" review "}}}`)
	if len(alignmentCapabilities(rec)) != 0 {
		t.Fatal("approval fabricated execution")
	}
	alignmentFeed(t, p, `{"type":"stream_event","event":{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"s","name":"Skill","input":{}}}}`, `{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"skill\":"}}}`, `{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"\" review \"}"}}}`, `{"type":"stream_event","event":{"type":"content_block_stop","index":0}}`, alignmentCall("Skill", "s", `{"skill":" review "}`), alignmentResult("s", `"ok"`, false, ""))
	if got := alignmentCapabilities(rec); len(got) != 2 || got[0].Phase != capability.Started || got[1].Phase != capability.Completed {
		t.Fatalf("partial/wrapper facts=%#v", got)
	}
	// Reusing a block index starts a fresh input buffer, not the previous JSON.
	alignmentFeed(t, p, `{"type":"stream_event","event":{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"s2","name":"Skill","input":{"command":" review "}}}}`, `{"type":"stream_event","event":{"type":"content_block_stop","index":0}}`)
	if len(alignmentCapabilities(rec)) != 3 {
		t.Fatal("reused index lost or concatenated input")
	}
}
func TestAlignmentObservationAmbiguityConflictAndClose(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		t.Run(fmt.Sprint(reverse), func(t *testing.T) {
			req := alignmentObservationRequest()
			entries := []driver.ResolvedSkill{{Key: "one", RuntimeName: "same"}, {Key: "two", RuntimeName: "same"}, {Key: "one", RuntimeName: "same"}}
			if reverse {
				entries[0], entries[1] = entries[1], entries[0]
			}
			req.Skills.Entries = entries
			req.MCP.Servers = []driver.MCPServerSpec{{Key: "a"}, {Key: "a__b"}}
			rec := &testutil.EventRecorder{}
			p := newParser(rec)
			p.configureObservations(context.Background(), req)
			alignmentFeed(t, p, alignmentCall("Skill", "s", `{"skill":"same"}`), alignmentCall("mcp__a__b__c", "m", `{}`))
			if len(alignmentCapabilities(rec)) != 0 {
				t.Fatal("ambiguous catalog picked a winner")
			}
		})
	}
	for _, cancelled := range []bool{false, true} {
		t.Run(fmt.Sprintf("cancel=%v", cancelled), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			rec := &testutil.EventRecorder{}
			p := newParser(rec)
			p.configureObservations(ctx, alignmentObservationRequest())
			alignmentFeed(t, p, alignmentCall("Skill", "s", `{"skill":" review "}`), alignmentCall("Skill", "s", `{"skill":"wrong"}`), alignmentResult("s", `"ok"`, false, ""))
			if cancelled {
				cancel()
			}
			p.completeStream(nil, 0, "", false)
			p.completeStream(nil, 0, "", false)
			facts := alignmentCapabilities(rec)
			want := capability.Interrupted
			if cancelled {
				want = capability.Cancelled
			}
			if len(facts) != 2 || facts[1].Phase != want || !alignmentNotice(rec, "observation_tool_conflict") {
				t.Fatalf("unconfirmed close=%#v", facts)
			}
		})
	}
}
func TestAlignmentTodoConfirmedRealSyntheticReplaceClear(t *testing.T) {
	rec := &testutil.EventRecorder{}
	p := newParser(rec)
	p.configureObservations(context.Background(), alignmentObservationRequest())
	alignmentFeed(t, p, alignmentCall("TaskCreate", "create", `{"subject":"实际任务","description":"test"}`))
	if len(alignmentTodos(rec)) != 0 {
		t.Fatal("input completion changed table")
	}
	result := alignmentResult("create", `[{"type":"text","text":"Task #91 created successfully: 实际任务"}]`, false, `{"task":{"id":"91","subject":"实际任务","status":"pending"}}`)
	alignmentFeed(t, p, result, result, alignmentCall("TaskUpdate", "update", `{"taskId":"91","status":"in_progress"}`), alignmentResult("update", `"Updated task #91 status"`, false, ""), alignmentCall("TaskUpdate", "unknown", `{"taskId":"1","status":"completed"}`), alignmentResult("unknown", `"Updated task #1 status"`, false, ""))
	snapshots := alignmentTodos(rec)
	if len(snapshots) != 2 || snapshots[0].Items[0].ID != "91" || snapshots[0].Items[0].SyntheticID || snapshots[1].Items[0].Status != todo.InProgress || !alignmentNotice(rec, "todo_unknown_id") {
		t.Fatalf("real IDs %#v", snapshots)
	}
	alignmentFeed(t, p, alignmentCall("TaskCreate", "failed", `{"subject":"bad"}`), alignmentResult("failed", `"failure"`, true, `{"todos":[]}`), alignmentCall("TaskCreate", "synthetic", `{"subject":"no ID"}`), alignmentResult("synthetic", `"created"`, false, `{"task":{"subject":"no ID","status":"pending"}}`))
	snapshots = alignmentTodos(rec)
	if len(snapshots) != 3 || !snapshots[2].Items[1].SyntheticID || !strings.HasPrefix(snapshots[2].Items[1].ID, "synthetic:") {
		t.Fatalf("synthetic/failed %#v", snapshots)
	}
	alignmentFeed(t, p, alignmentCall("TaskList", "list", `{}`), alignmentResult("list", `"list"`, false, `{"todos":[{"id":"900","content":"正式列表","status":"completed"}]}`), alignmentCall("TodoWrite", "clear", `{"newTodos":[]}`), alignmentResult("clear", `"Todo list updated successfully"`, false, ""), alignmentResult("clear", `"Todo list updated successfully"`, false, ""))
	snapshots = alignmentTodos(rec)
	if len(snapshots) != 5 || snapshots[3].Items[0].ID != "900" || snapshots[4].Items == nil || len(snapshots[4].Items) != 0 || snapshots[4].Revision != 5 {
		t.Fatalf("replace/clear %#v", snapshots)
	}
}
func TestAlignmentTodoMalformedReplayAndResume(t *testing.T) {
	for _, bad := range []string{`null`, `[{"content":"x","status":"unknown"}]`, `[{"id":"x","content":"x","status":"pending"},{"id":"x","content":"y","status":"pending"}]`, `[{"content":"","status":"pending"}]`, `[{"content":"` + strings.Repeat("界", 1366) + `","status":"pending"}]`} {
		t.Run(fmt.Sprint(len(bad)), func(t *testing.T) {
			rec := &testutil.EventRecorder{}
			p := newParser(rec)
			p.configureObservations(context.Background(), alignmentObservationRequest())
			alignmentFeed(t, p, alignmentCall("TodoWrite", "clear", `{"newTodos":[]}`), alignmentResult("clear", `"Todo list updated successfully"`, false, ""), alignmentCall("TodoWrite", "bad", `{"newTodos":`+bad+`}`), alignmentResult("bad", `"Todo list updated successfully"`, false, ""))
			if snapshots := alignmentTodos(rec); len(snapshots) != 1 || snapshots[0].Revision != 1 || snapshots[0].Items == nil || !alignmentNotice(rec, "todo_invalid") {
				t.Fatalf("invalid snapshot accepted %#v", snapshots)
			}
		})
	}
	rec := &testutil.EventRecorder{}
	p := newParser(rec)
	p.configureObservations(context.Background(), alignmentObservationRequest())
	for _, id := range []string{"a", "b"} {
		alignmentFeed(t, p, alignmentCall("TodoWrite", id, `{"newTodos":[{"content":"same","status":"pending"}]}`), alignmentResult(id, `"Todo list updated successfully"`, false, ""))
	}
	if len(alignmentTodos(rec)) != 1 {
		t.Fatal("equivalent ID-less full snapshot replayed")
	}
	// A fresh parser represents a new turn; no local task cache crosses resume.
	next := newParser(rec)
	req := alignmentObservationRequest()
	req.RunID = "next"
	next.configureObservations(context.Background(), req)
	alignmentFeed(t, next, alignmentCall("TaskUpdate", "a", `{"taskId":"1","status":"completed"}`), alignmentResult("a", `"Updated task #1 status"`, false, ""))
	if len(alignmentTodos(rec)) != 1 || !alignmentNotice(rec, "todo_unknown_id") {
		t.Fatal("resume guessed an existing task")
	}
}
func TestAlignmentObservationParentBoundaryAndConcurrentDispatch(t *testing.T) {
	rec := &testutil.EventRecorder{}
	p := newParser(rec)
	p.configureObservations(context.Background(), alignmentObservationRequest())
	alignmentFeed(t, p, strings.Replace(alignmentCall("Skill", "nested", `{"skill":" review "}`), `"parent_tool_use_id":null`, `"parent_tool_use_id":"unknown-parent"`, 1))
	if len(alignmentCapabilities(rec)) != 0 || !alignmentNotice(rec, "observation_parent_unavailable") {
		t.Fatal("unproved parent mapped to root")
	}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := fmt.Sprint(i)
			raw := alignmentCall("Skill", id, `{"skill":" review "}`) + "\n" + alignmentResult(id, `"ok"`, false, "") + "\n"
			_ = p.onChunk("stdout", []byte(raw), time.Now())
		}(i)
	}
	wg.Wait()
	facts := alignmentCapabilities(rec)
	if len(facts) != 40 {
		t.Fatalf("facts=%d", len(facts))
	}
	for i := 0; i < len(facts); i += 2 {
		if facts[i].Phase != capability.Started || facts[i+1].Phase != capability.Completed || !reflect.DeepEqual(facts[i].Ref, facts[i+1].Ref) {
			t.Fatal("terminal overtook start")
		}
	}
}

func TestAlignmentTodoRejectsUnknownAndMalformedResults(t *testing.T) {
	for _, result := range []string{
		alignmentResult("create", `"unrecognized result"`, false, ""),
		alignmentResult("create", `{}`, false, `{"task":{"id":"91","subject":"x"}}`),
		alignmentResult("create", `"ok"`, false, `{"task":"malformed"}`),
		alignmentResult("create", `"ok"`, false, `{"todos":null}`),
		strings.Replace(alignmentResult("create", `"ok"`, false, `{"task":{"id":"91","subject":"x"}}`), `"is_error":false`, `"is_error":"false"`, 1),
	} {
		rec := &testutil.EventRecorder{}
		p := newParser(rec)
		p.configureObservations(context.Background(), alignmentObservationRequest())
		alignmentFeed(t, p, alignmentCall("TaskCreate", "create", `{"subject":"x"}`), result)
		if len(alignmentTodos(rec)) != 0 {
			t.Fatal("malformed/unknown result confirmed todo")
		}
	}
	rec := &testutil.EventRecorder{}
	p := newParser(rec)
	p.configureObservations(context.Background(), alignmentObservationRequest())
	alignmentFeed(t, p, alignmentCall("TodoWrite", "bad", `{"newTodos":[{"content":"`+string([]byte{0xff})+`","status":"pending"}]}`), alignmentResult("bad", `"Todo list updated successfully"`, false, ""))
	if len(alignmentTodos(rec)) != 0 || !p.protocolMalformed {
		t.Fatal("invalid UTF-8 was silently repaired into a snapshot")
	}
}

func TestAlignmentObservationUnprovedNestedPartialDoesNotRebindRootID(t *testing.T) {
	rec := &testutil.EventRecorder{}
	p := newParser(rec)
	p.configureObservations(context.Background(), alignmentObservationRequest())
	alignmentFeed(t, p, alignmentCall("Skill", "same", `{"skill":" review "}`),
		`{"type":"stream_event","parent_tool_use_id":"unproved","event":{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"same","name":"Skill","input":{"skill":" review "}}}}`,
		`{"type":"stream_event","event":{"type":"content_block_stop","index":1}}`,
		alignmentResult("same", `"ok"`, false, ""))
	p.completeStream(nil, 0, "", false)
	facts := alignmentCapabilities(rec)
	if len(facts) != 2 || facts[1].Phase != capability.Interrupted {
		t.Fatalf("unproved nested result completed root call %#v", facts)
	}
}

func TestAlignmentObservationRejectsUnprovedResultParent(t *testing.T) {
	rec := &testutil.EventRecorder{}
	p := newParser(rec)
	p.configureObservations(context.Background(), alignmentObservationRequest())
	alignmentFeed(t, p, alignmentCall("Skill", "s", `{"skill":" review "}`), alignmentCall("TaskCreate", "c", `{"subject":"task"}`))
	skillResult := strings.Replace(alignmentResult("s", `"ok"`, false, ""), `"parent_tool_use_id":"s"`, `"parent_tool_use_id":"foreign"`, 1)
	taskResult := strings.Replace(alignmentResult("c", `"ok"`, false, `{"task":{"id":"91","subject":"task","status":"pending"}}`), `"parent_tool_use_id":"c"`, `"parent_tool_use_id":false`, 1)
	alignmentFeed(t, p, skillResult, taskResult)
	p.completeStream(nil, 0, "", false)
	facts := alignmentCapabilities(rec)
	if len(facts) != 2 || facts[1].Phase != capability.Interrupted || len(alignmentTodos(rec)) != 0 || !alignmentNotice(rec, "observation_parent_unavailable") {
		t.Fatalf("unproved result parent was attributed to root: facts=%#v todos=%#v", facts, alignmentTodos(rec))
	}
}
