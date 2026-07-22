package claude

import (
	"testing"
	"time"

	agentadaptor "github.com/agent-dance/agent-adaptor"
)

// kindCounts returns a map of StreamKind → count for all payloads.
func kindCounts(payloads []agentadaptor.StreamPayload) map[agentadaptor.StreamKind]int {
	m := make(map[agentadaptor.StreamKind]int)
	for _, p := range payloads {
		m[p.Kind]++
	}
	return m
}

// subagentKinds filters payloads to those that have a non-nil Subagent.
func subagentKinds(payloads []agentadaptor.StreamPayload) []agentadaptor.StreamPayload {
	var out []agentadaptor.StreamPayload
	for _, p := range payloads {
		if p.Subagent != nil {
			out = append(out, p)
		}
	}
	return out
}

func assertSubagentEndBeforeRunTerminal(t *testing.T, payloads []agentadaptor.StreamPayload, terminal agentadaptor.StreamKind) {
	t.Helper()

	endIndex, terminalIndex := -1, -1
	endCount := 0
	for i, payload := range payloads {
		switch payload.Kind {
		case agentadaptor.StreamSubagentEnd:
			endCount++
			endIndex = i
		case terminal:
			if terminalIndex == -1 {
				terminalIndex = i
			}
		}
	}
	if endCount != 1 {
		t.Fatalf("want exactly 1 subagent.end, got %d", endCount)
	}
	if terminalIndex == -1 {
		t.Fatalf("missing parent terminal event %q", terminal)
	}
	if endIndex >= terminalIndex {
		t.Fatalf("subagent.end index %d must precede %s index %d", endIndex, terminal, terminalIndex)
	}
	for i := terminalIndex + 1; i < len(payloads); i++ {
		if payloads[i].Subagent != nil {
			t.Fatalf("subagent event %s at index %d follows parent terminal %s", payloads[i].Kind, i, terminal)
		}
	}
}

// TestSubagentTaskStarted_EmitsStartAndEnd verifies the happy-path:
// task_started → StreamSubagentStart; task_notification completed →
// StreamSubagentEnd; exactly one start and one end per subagent ID.
func TestSubagentTaskStarted_EmitsStartAndEnd(t *testing.T) {
	data := loadFixture(t, "subagent-task-started.jsonl")

	sink := &streamSink{}
	p := newClaudeParser(sink)
	p.enableStreaming("run-subagent-start")
	if err := p.onChunk("stdout", data, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	p.finalize()

	payloads := sink.snapshot()
	counts := kindCounts(payloads)

	if counts[agentadaptor.StreamSubagentStart] != 1 {
		t.Errorf("want 1 StreamSubagentStart, got %d", counts[agentadaptor.StreamSubagentStart])
	}
	if counts[agentadaptor.StreamSubagentEnd] != 1 {
		t.Errorf("want 1 StreamSubagentEnd, got %d", counts[agentadaptor.StreamSubagentEnd])
	}
	assertSubagentEndBeforeRunTerminal(t, payloads, agentadaptor.StreamRunFinished)

	var startPL, endPL *agentadaptor.StreamPayload
	for i := range payloads {
		switch payloads[i].Kind {
		case agentadaptor.StreamSubagentStart:
			startPL = &payloads[i]
		case agentadaptor.StreamSubagentEnd:
			endPL = &payloads[i]
		}
	}
	if startPL == nil || startPL.Subagent == nil {
		t.Fatal("StreamSubagentStart missing or has nil Subagent")
	}
	if endPL == nil || endPL.Subagent == nil {
		t.Fatal("StreamSubagentEnd missing or has nil Subagent")
	}
	if startPL.Subagent.ID != endPL.Subagent.ID {
		t.Errorf("start/end Subagent.ID mismatch: %q vs %q", startPL.Subagent.ID, endPL.Subagent.ID)
	}
	if startPL.Subagent.ToolCallID == "" {
		t.Error("Subagent.ToolCallID must be set to parent_tool_use_id")
	}
	if startPL.Subagent.Kind != "native" {
		t.Errorf("Subagent.Kind want %q got %q", "native", startPL.Subagent.Kind)
	}

	// SubagentEnd result must contain terminal status.
	if endPL.Result == nil {
		t.Error("StreamSubagentEnd.Result must not be nil")
	} else if status, _ := endPL.Result["status"].(string); status != "completed" {
		t.Errorf("SubagentEnd.Result.status want %q got %q", "completed", status)
	}

	// Parent scope events (text) must have nil Subagent.
	for _, pl := range payloads {
		if pl.Kind == agentadaptor.StreamTextContent && pl.Subagent != nil {
			t.Error("parent-scope text.content must have nil Subagent")
		}
	}

	// No fake text delta inside subagent scope (SubagentTextDelta=false).
	childPLs := subagentKinds(payloads)
	for _, pl := range childPLs {
		if pl.Kind == agentadaptor.StreamTextContent || pl.Kind == agentadaptor.StreamTextStart {
			t.Errorf("unexpected child text delta: kind=%s", pl.Kind)
		}
	}
}

// TestSubagentTaskProgress_EmitsStatusEvents verifies that task_progress
// events become StreamSubagentStatus payloads carrying description/delta.
func TestSubagentTaskProgress_EmitsStatusEvents(t *testing.T) {
	data := loadFixture(t, "subagent-task-progress.jsonl")

	sink := &streamSink{}
	p := newClaudeParser(sink)
	p.enableStreaming("run-subagent-progress")
	if err := p.onChunk("stdout", data, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	p.finalize()

	payloads := sink.snapshot()
	counts := kindCounts(payloads)

	if counts[agentadaptor.StreamSubagentStatus] != 2 {
		t.Errorf("want 2 StreamSubagentStatus (two task_progress), got %d",
			counts[agentadaptor.StreamSubagentStatus])
	}
	if counts[agentadaptor.StreamSubagentStart] != 1 {
		t.Errorf("want 1 start, got %d", counts[agentadaptor.StreamSubagentStart])
	}
	if counts[agentadaptor.StreamSubagentEnd] != 1 {
		t.Errorf("want 1 end, got %d", counts[agentadaptor.StreamSubagentEnd])
	}

	// All status payloads must have matching Subagent.ID.
	var startID string
	for _, pl := range payloads {
		if pl.Kind == agentadaptor.StreamSubagentStart {
			startID = pl.Subagent.ID
		}
	}
	for _, pl := range payloads {
		if pl.Kind == agentadaptor.StreamSubagentStatus {
			if pl.Subagent == nil || pl.Subagent.ID != startID {
				t.Errorf("status Subagent.ID mismatch: want %q got %v", startID, pl.Subagent)
			}
			if pl.Delta == "" {
				t.Error("StreamSubagentStatus.Delta must not be empty")
			}
		}
	}
}

// TestSubagentChildTool_EmitsToolCallWithSubagent verifies that
// assistant{parent_tool_use_id} tool_use frames become StreamToolCallStart/End
// with a non-nil Subagent, and user{parent_tool_use_id} tool_result becomes
// StreamToolCallResult with a non-nil Subagent.
func TestSubagentChildTool_EmitsToolCallWithSubagent(t *testing.T) {
	data := loadFixture(t, "subagent-child-tool.jsonl")

	sink := &streamSink{}
	p := newClaudeParser(sink)
	p.enableStreaming("run-subagent-child")
	if err := p.onChunk("stdout", data, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	p.finalize()

	payloads := sink.snapshot()

	// Child tool_call.start must have Subagent.
	var sawChildStart, sawChildEnd, sawChildResult bool
	const childToolCallID = "toolu_read_child_1"
	for _, pl := range payloads {
		switch pl.Kind {
		case agentadaptor.StreamToolCallStart:
			if pl.ToolCallID == childToolCallID {
				if pl.Subagent == nil {
					t.Error("child tool_call.start: Subagent must not be nil")
				}
				if got, _ := pl.Args["file_path"].(string); got != "/repo/README.md" {
					t.Errorf("child tool_call.start Args.file_path want %q got %q", "/repo/README.md", got)
				}
				if got, _ := pl.Args["limit"].(float64); got != 20 {
					t.Errorf("child tool_call.start Args.limit want 20 got %v", pl.Args["limit"])
				}
				sawChildStart = true
			}
		case agentadaptor.StreamToolCallEnd:
			if pl.ToolCallID == childToolCallID {
				if pl.Subagent == nil {
					t.Error("child tool_call.end: Subagent must not be nil")
				}
				sawChildEnd = true
			}
		case agentadaptor.StreamToolCallResult:
			if pl.ToolCallID == childToolCallID {
				if pl.Subagent == nil {
					t.Error("child tool_call.result: Subagent must not be nil")
				}
				sawChildResult = true
			}
		}
	}
	if !sawChildStart {
		t.Error("missing child StreamToolCallStart")
	}
	if !sawChildEnd {
		t.Error("missing child StreamToolCallEnd")
	}
	if !sawChildResult {
		t.Error("missing child StreamToolCallResult")
	}

	// Parent Agent tool_use result must NOT have Subagent (it's the parent scope).
	const parentAgentToolCallID = "toolu_agent_003"
	for _, pl := range payloads {
		if pl.Kind == agentadaptor.StreamToolCallResult && pl.ToolCallID == parentAgentToolCallID {
			if pl.Subagent != nil {
				t.Error("parent Agent tool_result must have nil Subagent")
			}
		}
	}

	// No child text token delta.
	childPLs := subagentKinds(payloads)
	for _, pl := range childPLs {
		if pl.Kind == agentadaptor.StreamTextContent || pl.Kind == agentadaptor.StreamTextStart {
			t.Errorf("no child text delta expected, got kind=%s", pl.Kind)
		}
	}
}

func TestSubagentChildTool_EmptyAndNonMapInputAreSafe(t *testing.T) {
	tests := []struct {
		name  string
		input any
	}{
		{name: "empty map", input: map[string]any{}},
		{name: "non-map", input: "unexpected-input"},
		{name: "nil", input: nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sink := &streamSink{}
			p := newClaudeParser(sink)
			p.enableStreaming("run-child-input-" + tt.name)
			p.handleSubagentTaskStarted(map[string]any{
				"task_id":            "task-input",
				"subagent_type":      "worker",
				"parent_tool_use_id": "toolu_agent_input",
			})
			p.handleAssistantMessage(map[string]any{
				"content": []any{map[string]any{
					"type":  "tool_use",
					"id":    "toolu_child_input",
					"name":  "Read",
					"input": tt.input,
				}},
			}, "toolu_agent_input")

			var start *agentadaptor.StreamPayload
			for _, payload := range sink.snapshot() {
				if payload.Kind == agentadaptor.StreamToolCallStart && payload.ToolCallID == "toolu_child_input" {
					copy := payload
					start = &copy
					break
				}
			}
			if start == nil {
				t.Fatal("missing child tool_call.start")
			}
			if start.Args != nil {
				t.Fatalf("empty/non-map child input must leave Args nil, got %#v", start.Args)
			}
			if len(p.transcript) != 1 {
				t.Fatalf("want one transcript tool call, got %d", len(p.transcript))
			}
			if _, isMap := tt.input.(map[string]any); isMap {
				inputMap, ok := p.transcript[0].Input.(map[string]any)
				if !ok || len(inputMap) != 0 {
					t.Fatalf("transcript input changed: got %#v want empty map", p.transcript[0].Input)
				}
			} else if p.transcript[0].Input != tt.input {
				t.Fatalf("transcript input changed: got %#v want %#v", p.transcript[0].Input, tt.input)
			}
		})
	}
}

// TestSubagentFailed_EmitsFailedEnd verifies that task_notification status=failed
// produces StreamSubagentEnd with status="failed".
func TestSubagentFailed_EmitsFailedEnd(t *testing.T) {
	data := loadFixture(t, "subagent-failed.jsonl")

	sink := &streamSink{}
	p := newClaudeParser(sink)
	p.enableStreaming("run-subagent-failed")
	if err := p.onChunk("stdout", data, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	p.finalize()

	payloads := sink.snapshot()
	counts := kindCounts(payloads)

	if counts[agentadaptor.StreamSubagentEnd] != 1 {
		t.Errorf("want exactly 1 StreamSubagentEnd, got %d", counts[agentadaptor.StreamSubagentEnd])
	}
	assertSubagentEndBeforeRunTerminal(t, payloads, agentadaptor.StreamRunFinished)
	for _, pl := range payloads {
		if pl.Kind == agentadaptor.StreamSubagentEnd {
			if pl.Result == nil {
				t.Fatal("SubagentEnd.Result nil on failed subagent")
			}
			if status, _ := pl.Result["status"].(string); status != "failed" {
				t.Errorf("SubagentEnd status want %q got %q", "failed", status)
			}
		}
	}
}

// TestSubagentTruncated_SyntheticEnd verifies that a truncated stream (no
// task_notification) causes finalize() to synthesize a StreamSubagentEnd.
func TestSubagentTruncated_SyntheticEnd(t *testing.T) {
	data := loadFixture(t, "subagent-truncated.jsonl")

	sink := &streamSink{}
	p := newClaudeParser(sink)
	p.enableStreaming("run-subagent-trunc")
	if err := p.onChunk("stdout", data, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	p.finalize()

	payloads := sink.snapshot()
	counts := kindCounts(payloads)

	if counts[agentadaptor.StreamSubagentStart] != 1 {
		t.Errorf("want 1 start, got %d", counts[agentadaptor.StreamSubagentStart])
	}
	if counts[agentadaptor.StreamSubagentEnd] != 1 {
		t.Errorf("want 1 synthetic end, got %d", counts[agentadaptor.StreamSubagentEnd])
	}
	assertSubagentEndBeforeRunTerminal(t, payloads, agentadaptor.StreamRunFinished)

	// The synthetic end must carry status=failed and synthetic=true.
	for _, pl := range payloads {
		if pl.Kind == agentadaptor.StreamSubagentEnd {
			if pl.Result == nil {
				t.Fatal("synthetic SubagentEnd.Result nil")
			}
			if status, _ := pl.Result["status"].(string); status != "failed" {
				t.Errorf("synthetic end status want %q got %q", "failed", status)
			}
			if synthetic, _ := pl.Result["synthetic"].(bool); !synthetic {
				t.Error("synthetic end must carry synthetic=true in Result")
			}
		}
	}
}

// TestSubagentOpenScopeClosesBeforeParentTerminal covers provider terminals
// that arrive before task_notification. The synthetic child end must precede
// both successful and error parent terminals, and remain exactly-once after
// parser.finalize runs.
func TestSubagentOpenScopeClosesBeforeParentTerminal(t *testing.T) {
	tests := []struct {
		name     string
		fixture  string
		terminal agentadaptor.StreamKind
	}{
		{name: "result", fixture: "subagent-result-open.jsonl", terminal: agentadaptor.StreamRunFinished},
		{name: "error", fixture: "subagent-error-open.jsonl", terminal: agentadaptor.StreamRunError},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sink := &streamSink{}
			p := newClaudeParser(sink)
			p.enableStreaming("run-subagent-" + tt.name + "-open")
			if err := p.onChunk("stdout", loadFixture(t, tt.fixture), time.Now().UTC()); err != nil {
				t.Fatal(err)
			}
			p.finalize()

			payloads := sink.snapshot()
			assertSubagentEndBeforeRunTerminal(t, payloads, tt.terminal)
			for _, payload := range payloads {
				if payload.Kind != agentadaptor.StreamSubagentEnd {
					continue
				}
				if synthetic, _ := payload.Result["synthetic"].(bool); !synthetic {
					t.Fatal("open scope at parent terminal must close with synthetic=true")
				}
			}
		})
	}
}

// TestSubagentNoTaskStarted_OpensScopeOnFirstChildEvent verifies that when no
// task_started arrives, the first child event (assistant with parent_tool_use_id)
// auto-opens a subagent scope and emits StreamSubagentStart.
func TestSubagentNoTaskStarted_OpensScopeOnFirstChildEvent(t *testing.T) {
	data := loadFixture(t, "subagent-no-task-started.jsonl")

	sink := &streamSink{}
	p := newClaudeParser(sink)
	p.enableStreaming("run-subagent-no-task")
	if err := p.onChunk("stdout", data, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	p.finalize()

	payloads := sink.snapshot()
	counts := kindCounts(payloads)

	if counts[agentadaptor.StreamSubagentStart] != 1 {
		t.Errorf("want 1 auto-opened StreamSubagentStart, got %d",
			counts[agentadaptor.StreamSubagentStart])
	}
	// End must come either from parent tool_result or synthetic flush.
	if counts[agentadaptor.StreamSubagentEnd] != 1 {
		t.Errorf("want 1 StreamSubagentEnd, got %d", counts[agentadaptor.StreamSubagentEnd])
	}
}

// TestSubagentCheckpoint_ChildIDNotInParent verifies that subagent IDs do not
// contaminate the parent session checkpoint.
func TestSubagentCheckpoint_ChildIDNotInParent(t *testing.T) {
	data := loadFixture(t, "subagent-child-tool.jsonl")
	p := snapshotClaudeStdout(string(data))

	cp := p.checkpoint(0)
	if cp == nil || cp.State == nil {
		t.Fatal("expected a valid parent checkpoint")
	}
	// The parent session ID must be from the top-level session_id field only.
	if cp.State.ResumeID == "" {
		t.Fatal("parent ResumeID must not be empty")
	}
	// Subagent task IDs (task-abc-003) must not appear in the checkpoint state.
	const subagentTaskID = "task-abc-003"
	if cp.State.ResumeID == subagentTaskID {
		t.Errorf("parent checkpoint.ResumeID must not equal subagent task ID %q", subagentTaskID)
	}
}

// TestSubagentRole_StaysZero verifies that all subagent-scoped payloads emitted
// by the adapter keep Role at its zero value (per §4 hard constraint).
func TestSubagentRole_StaysZero(t *testing.T) {
	data := loadFixture(t, "subagent-child-tool.jsonl")

	sink := &streamSink{}
	p := newClaudeParser(sink)
	p.enableStreaming("run-subagent-role")
	if err := p.onChunk("stdout", data, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	p.finalize()

	for _, pl := range sink.snapshot() {
		if pl.Role != agentadaptor.RoleAssistant {
			t.Errorf("payload kind=%s has non-zero Role=%q", pl.Kind, pl.Role)
		}
	}
}

// TestSubagentIDStability verifies the (RunID, Subagent.ID) pairing is stable:
// all events within the same subagent scope share a consistent Subagent.ID and
// ToolCallID that matches the parent Agent tool_use id.
func TestSubagentIDStability(t *testing.T) {
	data := loadFixture(t, "subagent-child-tool.jsonl")

	sink := &streamSink{}
	p := newClaudeParser(sink)
	p.enableStreaming("run-subagent-id")
	if err := p.onChunk("stdout", data, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	p.finalize()

	payloads := sink.snapshot()
	scopeID := ""
	toolCallID := ""
	for _, pl := range payloads {
		if pl.Subagent == nil {
			continue
		}
		if scopeID == "" {
			scopeID = pl.Subagent.ID
			toolCallID = pl.Subagent.ToolCallID
			continue
		}
		if pl.Subagent.ID != scopeID {
			t.Errorf("inconsistent Subagent.ID within run: %q vs %q", scopeID, pl.Subagent.ID)
		}
		if pl.Subagent.ToolCallID != toolCallID {
			t.Errorf("inconsistent Subagent.ToolCallID: %q vs %q", toolCallID, pl.Subagent.ToolCallID)
		}
	}
	if scopeID == "" {
		t.Fatal("no subagent-scoped payloads found")
	}
	// ToolCallID must match the parent Agent tool_use id in the fixture.
	if toolCallID != "toolu_agent_003" {
		t.Errorf("Subagent.ToolCallID want %q got %q", "toolu_agent_003", toolCallID)
	}
}

// TestSubagentExistingStreamRegression verifies that happy-path (non-subagent)
// streaming output is unaffected by the subagent changes.
func TestSubagentExistingStreamRegression(t *testing.T) {
	for _, name := range []string{
		"streaming-happy.jsonl",
		"streaming-tool-use.jsonl",
		"streaming-thinking.jsonl",
	} {
		t.Run(name, func(t *testing.T) {
			data := loadFixture(t, name)
			sink := &streamSink{}
			p := newClaudeParser(sink)
			p.enableStreaming("run-regression")
			if err := p.onChunk("stdout", data, time.Now().UTC()); err != nil {
				t.Fatal(err)
			}
			p.finalize()

			for _, pl := range sink.snapshot() {
				if pl.Subagent != nil {
					t.Errorf("%s: non-subagent fixture must not emit Subagent-tagged payloads: kind=%s", name, pl.Kind)
				}
			}
		})
	}
}
