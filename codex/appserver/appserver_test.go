package appserver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"testing"

	agentadaptor "github.com/agent-dance/agent-adaptor"
)

// ---------------------------------------------------------------------------
// stdioStream smoke test
// ---------------------------------------------------------------------------
//
// The stream no longer mutates frames (sourcegraph/jsonrpc2 does not
// require the "jsonrpc":"2.0" marker), so we only need a smoke test to
// confirm it reads/writes newline-delimited JSON objects in FIFO order
// and closes the stdin half when asked.
func TestStdioStreamRoundtrip(t *testing.T) {
	t.Parallel()

	stdin := &bytesWriteCloser{Buffer: &bytes.Buffer{}}
	stdout := bytes.NewBufferString(
		`{"id":1,"result":{"ok":true}}` + "\n" +
			`{"jsonrpc":"2.0","method":"thread/started","params":{"threadId":"t1"}}` + "\n",
	)

	stream := newStdioStream(stdin, stdout)

	var first map[string]any
	if err := stream.ReadObject(&first); err != nil {
		t.Fatalf("read 1: %v", err)
	}
	if id, _ := first["id"].(float64); id != 1 {
		t.Fatalf("read 1 wrong object: %#v", first)
	}
	var second map[string]any
	if err := stream.ReadObject(&second); err != nil {
		t.Fatalf("read 2: %v", err)
	}
	if second["method"] != "thread/started" {
		t.Fatalf("read 2 wrong object: %#v", second)
	}

	if err := stream.WriteObject(map[string]any{"hello": "world"}); err != nil {
		t.Fatalf("write: %v", err)
	}
	if got := stdin.String(); got != `{"hello":"world"}`+"\n" {
		t.Fatalf("unexpected stdin payload: %q", got)
	}

	if err := stream.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if !stdin.closed {
		t.Fatalf("stdin not closed")
	}
}

type bytesWriteCloser struct {
	*bytes.Buffer
	closed bool
}

func (b *bytesWriteCloser) Close() error {
	b.closed = true
	return nil
}

// Keep io imported for future tests that plug in real pipes.
var _ io.Writer = (*bytesWriteCloser)(nil)
var _ = context.Background

// ---------------------------------------------------------------------------
// ThreadItem union decoder
// ---------------------------------------------------------------------------

func TestDecodeThreadItemVariants(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		json   string
		expect func(*testing.T, *ThreadItem)
	}{
		{
			name: "agentMessage",
			json: `{"id":"i1","type":"agentMessage","text":"hello","phase":"final_answer"}`,
			expect: func(t *testing.T, it *ThreadItem) {
				if it.Kind != ThreadItemAgentMessage {
					t.Fatalf("kind: %q", it.Kind)
				}
				if it.AgentMessage == nil || it.AgentMessage.Text != "hello" {
					t.Fatalf("body: %+v", it.AgentMessage)
				}
			},
		},
		{
			name: "reasoning",
			json: `{"id":"i2","type":"reasoning","content":["step 1"],"summary":["ok"]}`,
			expect: func(t *testing.T, it *ThreadItem) {
				if it.Kind != ThreadItemReasoning {
					t.Fatalf("kind: %q", it.Kind)
				}
				if it.Reasoning == nil || len(it.Reasoning.Content) != 1 {
					t.Fatalf("body: %+v", it.Reasoning)
				}
			},
		},
		{
			name: "commandExecution",
			json: `{"id":"i3","type":"commandExecution","command":"ls","cwd":"/tmp","status":"completed","exitCode":0,"aggregatedOutput":"a\nb"}`,
			expect: func(t *testing.T, it *ThreadItem) {
				if it.CommandExecution == nil {
					t.Fatalf("missing commandExecution body")
				}
				if it.CommandExecution.Command != "ls" {
					t.Fatalf("command: %q", it.CommandExecution.Command)
				}
				if it.CommandExecution.ExitCode == nil || *it.CommandExecution.ExitCode != 0 {
					t.Fatalf("exit code: %+v", it.CommandExecution.ExitCode)
				}
			},
		},
		{
			name: "fileChange",
			json: `{"id":"i4","type":"fileChange","status":"completed","changes":[{"path":"a.txt","diff":"+a"}]}`,
			expect: func(t *testing.T, it *ThreadItem) {
				if it.FileChange == nil || len(it.FileChange.Changes) != 1 {
					t.Fatalf("body: %+v", it.FileChange)
				}
			},
		},
		{
			name: "mcpToolCall",
			json: `{"id":"i5","type":"mcpToolCall","server":"s","tool":"t","status":"completed","arguments":{"q":"x"}}`,
			expect: func(t *testing.T, it *ThreadItem) {
				if it.McpToolCall == nil || it.McpToolCall.Server != "s" {
					t.Fatalf("body: %+v", it.McpToolCall)
				}
			},
		},
		{
			name: "webSearch",
			json: `{"id":"i6","type":"webSearch","query":"hi"}`,
			expect: func(t *testing.T, it *ThreadItem) {
				if it.WebSearch == nil || it.WebSearch.Query != "hi" {
					t.Fatalf("body: %+v", it.WebSearch)
				}
			},
		},
		{
			name: "dynamicToolCall",
			json: `{"id":"i7","type":"dynamicToolCall","tool":"t","status":"completed"}`,
			expect: func(t *testing.T, it *ThreadItem) {
				if it.DynamicToolCall == nil {
					t.Fatalf("missing dynamicToolCall body")
				}
			},
		},
		{
			name: "unknown variant",
			json: `{"id":"i8","type":"thisTypeDoesNotExistYet","foo":1}`,
			expect: func(t *testing.T, it *ThreadItem) {
				if it.Kind != ThreadItemUnknown {
					t.Fatalf("expected unknown kind, got %q", it.Kind)
				}
				if len(it.Raw) == 0 {
					t.Fatalf("Raw must be preserved for unknown variants")
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			it, err := DecodeThreadItem(json.RawMessage(tc.json))
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			if it.ID == "" {
				t.Fatalf("ID must be populated")
			}
			if len(it.Raw) == 0 {
				t.Fatalf("Raw must always be populated")
			}
			tc.expect(t, it)
		})
	}
}

// ---------------------------------------------------------------------------
// Translator dispatch
// ---------------------------------------------------------------------------

func TestTranslatorDispatchCoreFlow(t *testing.T) {
	t.Parallel()
	sink := &recordingSink{}
	tr := NewTranslator(sink, "run-123")

	// thread/started → captures threadID
	tr.Dispatch(NotifyThreadStarted, json.RawMessage(`{"thread":{"id":"t1"}}`))
	if tr.ThreadID() != "t1" {
		t.Fatalf("ThreadID not captured: %q", tr.ThreadID())
	}

	// turn/started → StreamRunStarted
	tr.Dispatch(NotifyTurnStarted, json.RawMessage(`{"threadId":"t1","turn":{"id":"turn-1","status":"inProgress"}}`))

	// item/started (agentMessage) → StreamTextStart
	tr.Dispatch(NotifyItemStarted, json.RawMessage(`{"threadId":"t1","turnId":"turn-1","item":{"id":"msg-1","type":"agentMessage","text":""}}`))

	// delta
	tr.Dispatch(NotifyItemAgentMessageDelta, json.RawMessage(`{"delta":"hel","itemId":"msg-1","threadId":"t1","turnId":"turn-1"}`))
	tr.Dispatch(NotifyItemAgentMessageDelta, json.RawMessage(`{"delta":"lo","itemId":"msg-1","threadId":"t1","turnId":"turn-1"}`))

	// item/completed (agentMessage) → StreamTextEnd
	tr.Dispatch(NotifyItemCompleted, json.RawMessage(`{"threadId":"t1","turnId":"turn-1","item":{"id":"msg-1","type":"agentMessage","text":"hello"}}`))

	// token usage
	tr.Dispatch(NotifyThreadTokenUsageUpdated, json.RawMessage(`{"threadId":"t1","turnId":"turn-1","tokenUsage":{"last":{"inputTokens":1,"outputTokens":2,"cachedInputTokens":0,"reasoningOutputTokens":0},"total":{"inputTokens":11,"outputTokens":22,"cachedInputTokens":3,"reasoningOutputTokens":0}}}`))

	// turn/completed WITHOUT body.Turn.Usage → should use cached usage
	tr.Dispatch(NotifyTurnCompleted, json.RawMessage(`{"threadId":"t1","turn":{"id":"turn-1","status":"completed"}}`))

	kinds := map[agentadaptor.StreamKind]int{}
	var lastSeq uint64
	var finishUsage *agentadaptor.Usage
	for _, p := range sink.streams {
		kinds[p.Kind]++
		if p.Sequence != 0 && p.Sequence <= lastSeq {
			t.Fatalf("non-monotonic sequence: %d after %d", p.Sequence, lastSeq)
		}
		if p.Sequence != 0 {
			lastSeq = p.Sequence
		}
		if p.RunID != "run-123" {
			t.Fatalf("RunID mismatch on %q: %q", p.Kind, p.RunID)
		}
		if p.Kind == agentadaptor.StreamRunFinished {
			finishUsage = p.Usage
		}
	}

	expected := map[agentadaptor.StreamKind]int{
		agentadaptor.StreamRunStarted:  1,
		agentadaptor.StreamTextStart:   1,
		agentadaptor.StreamTextContent: 2,
		agentadaptor.StreamTextEnd:     1,
		agentadaptor.StreamRunFinished: 1,
	}
	for kind, want := range expected {
		if got := kinds[kind]; got != want {
			t.Fatalf("kind %q: want %d got %d (full=%+v)", kind, want, got, kinds)
		}
	}
	if finishUsage == nil || finishUsage.InputTokens != 11 || finishUsage.OutputTokens != 22 || finishUsage.CachedInputTokens != 3 {
		t.Fatalf("StreamRunFinished.Usage not populated from tokenUsage cache: %+v", finishUsage)
	}
}

func TestTranslatorUnknownNotificationPassthrough(t *testing.T) {
	t.Parallel()
	sink := &recordingSink{}
	tr := NewTranslator(sink, "r")

	tr.Dispatch("custom/method", json.RawMessage(`{"foo":"bar"}`))

	if len(sink.streams) != 1 {
		t.Fatalf("want 1 payload, got %d", len(sink.streams))
	}
	p := sink.streams[0]
	if p.Kind != "" {
		t.Fatalf("unknown method should leave Kind empty, got %q", p.Kind)
	}
	if p.Name != "custom/method" {
		t.Fatalf("Name not set: %q", p.Name)
	}
	if p.Raw["foo"] != "bar" {
		t.Fatalf("Raw not preserved: %+v", p.Raw)
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

type recordingSink struct {
	mu      sync.Mutex
	events  []agentadaptor.RunEvent
	streams []agentadaptor.StreamPayload
}

func (r *recordingSink) Emit(event agentadaptor.RunEvent) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, event)
	return nil
}

func (r *recordingSink) EmitStream(payload agentadaptor.StreamPayload) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if payload.Sequence == 0 {
		payload.Sequence = uint64(len(r.streams) + 1)
	}
	r.streams = append(r.streams, payload)
	return nil
}

// ---------------------------------------------------------------------------
// collabAgentToolCall / collab_tool_call decoder tests
// ---------------------------------------------------------------------------

func TestDecodeCollabAgentToolCallCamelCase(t *testing.T) {
	// camelCase wire shape (app-server protocol)
	raw := json.RawMessage(`{
		"id": "c1",
		"type": "collabAgentToolCall",
		"tool": "spawnAgent",
		"status": "completed",
		"senderThreadId": "parent-t1",
		"receiverThreadIds": ["child-t2"],
		"agentsStates": {"child-t2": "running"}
	}`)
	it, err := DecodeThreadItem(raw)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if it.Kind != ThreadItemCollabAgentToolCall {
		t.Fatalf("kind: got %q want %q", it.Kind, ThreadItemCollabAgentToolCall)
	}
	if it.CollabAgentToolCall == nil {
		t.Fatal("CollabAgentToolCall body must not be nil")
	}
	b := it.CollabAgentToolCall
	if b.Tool != "spawnAgent" {
		t.Fatalf("Tool: %q", b.Tool)
	}
	if b.SenderThreadID != "parent-t1" {
		t.Fatalf("SenderThreadID: %q", b.SenderThreadID)
	}
	if len(b.ReceiverThreadIDs) != 1 || b.ReceiverThreadIDs[0] != "child-t2" {
		t.Fatalf("ReceiverThreadIDs: %v", b.ReceiverThreadIDs)
	}
}

func TestDecodeCollabAgentToolCallSnakeCase(t *testing.T) {
	// snake_case wire shape (exec --json path)
	raw := json.RawMessage(`{
		"id": "c2",
		"type": "collab_tool_call",
		"tool": "wait",
		"status": "in_progress",
		"sender_thread_id": "parent-t1",
		"receiver_thread_ids": [],
		"agents_states": {}
	}`)
	it, err := DecodeThreadItem(raw)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	// Snake-case wire normalises to the canonical kind.
	if it.Kind != ThreadItemCollabAgentToolCall {
		t.Fatalf("kind: got %q want %q", it.Kind, ThreadItemCollabAgentToolCall)
	}
	if it.CollabAgentToolCall == nil {
		t.Fatal("CollabAgentToolCall body must not be nil")
	}
	if it.CollabAgentToolCall.Tool != "wait" {
		t.Fatalf("Tool: %q", it.CollabAgentToolCall.Tool)
	}
	if it.CollabAgentToolCall.SenderThreadID != "parent-t1" {
		t.Fatalf("SenderThreadID: %q", it.CollabAgentToolCall.SenderThreadID)
	}
}

func TestDecodeSubAgentActivityForwardCompat(t *testing.T) {
	// subAgentActivity is a forward-compat v2 item; schema not yet vendored.
	// Kind must be preserved (not Unknown) to avoid colliding with Kind=="".
	raw := json.RawMessage(`{"id":"sa1","type":"subAgentActivity","agent_thread_id":"child-t3","agent_path":"/root/worker","kind":"started","tool_call_id":"spawn-3"}`)
	it, err := DecodeThreadItem(raw)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if it.Kind != ThreadItemSubAgentActivity {
		t.Fatalf("kind: got %q want %q", it.Kind, ThreadItemSubAgentActivity)
	}
	if len(it.Raw) == 0 {
		t.Fatal("Raw must be preserved for subAgentActivity")
	}
	if it.SubAgentActivity == nil {
		t.Fatal("SubAgentActivity body must be decoded")
	}
	if got := it.SubAgentActivity.AgentThreadID; got != "child-t3" {
		t.Fatalf("AgentThreadID: got %q", got)
	}
	if got := it.SubAgentActivity.AgentPath; got != "/root/worker" {
		t.Fatalf("AgentPath: got %q", got)
	}
	if got := it.SubAgentActivity.Kind; got != "started" {
		t.Fatalf("Kind: got %q", got)
	}
	if got := it.SubAgentActivity.ToolCallID; got != "spawn-3" {
		t.Fatalf("ToolCallID: got %q", got)
	}
}

// ---------------------------------------------------------------------------
// Translator collab item mapping
// ---------------------------------------------------------------------------

func TestTranslatorCollabWaitItemMapsToToolCallOnly(t *testing.T) {
	// collab:wait without receiver thread ids → only StreamToolCallStart/End
	// no subagent.start/end (exec --json degraded path per §8.3.4).
	t.Parallel()
	sink := &recordingSink{}
	tr := NewTranslator(sink, "run-collab")

	tr.Dispatch(NotifyThreadStarted, json.RawMessage(`{"thread":{"id":"t1"}}`))
	tr.Dispatch(NotifyTurnStarted, json.RawMessage(`{"threadId":"t1","turn":{"id":"turn-1","status":"inProgress"}}`))

	// item/started: collab_tool_call wait, no receiver
	tr.Dispatch(NotifyItemStarted, json.RawMessage(`{"threadId":"t1","turnId":"turn-1","item":{"id":"ci1","type":"collabAgentToolCall","tool":"wait","status":"in_progress","receiverThreadIds":[]}}`))
	// item/completed
	tr.Dispatch(NotifyItemCompleted, json.RawMessage(`{"threadId":"t1","turnId":"turn-1","item":{"id":"ci1","type":"collabAgentToolCall","tool":"wait","status":"completed","receiverThreadIds":[]}}`))

	tr.Dispatch(NotifyTurnCompleted, json.RawMessage(`{"threadId":"t1","turn":{"id":"turn-1","status":"completed"}}`))

	kinds := kindCounts(sink.streams)
	if kinds[StreamToolCallStart] != 1 {
		t.Fatalf("want 1 StreamToolCallStart, got %d", kinds[StreamToolCallStart])
	}
	if kinds[StreamToolCallEnd] != 1 {
		t.Fatalf("want 1 StreamToolCallEnd, got %d", kinds[StreamToolCallEnd])
	}
	if kinds[StreamSubagentStart] != 0 {
		t.Fatalf("collab:wait without child id must not emit StreamSubagentStart")
	}
	if kinds[StreamSubagentEnd] != 0 {
		t.Fatalf("collab:wait without child id must not emit StreamSubagentEnd")
	}
	// Verify tool name includes "collab:" prefix
	var toolName string
	for _, p := range sink.streams {
		if p.Kind == StreamToolCallStart {
			toolName = p.Name
		}
	}
	if toolName != "collab:wait" {
		t.Fatalf("tool name: got %q want %q", toolName, "collab:wait")
	}
}

func TestTranslatorCollabSpawnItemEmitsSubagentLifecycle(t *testing.T) {
	// collab:spawnAgent with receiver thread id → StreamToolCall* + StreamSubagentStart/End
	// and onChildThread callback is fired.
	t.Parallel()
	sink := &recordingSink{}
	tr := NewTranslator(sink, "run-spawn")

	var gotChildID, gotToolCallID string
	tr.mu.Lock()
	tr.onChildThread = func(childID, toolCallID string) {
		gotChildID = childID
		gotToolCallID = toolCallID
	}
	tr.mu.Unlock()

	tr.Dispatch(NotifyThreadStarted, json.RawMessage(`{"thread":{"id":"t1"}}`))
	tr.Dispatch(NotifyTurnStarted, json.RawMessage(`{"threadId":"t1","turn":{"id":"turn-1","status":"inProgress"}}`))

	// item/started with child thread id
	tr.Dispatch(NotifyItemStarted, json.RawMessage(`{"threadId":"t1","turnId":"turn-1","item":{"id":"spawn-1","type":"collabAgentToolCall","tool":"spawnAgent","receiverThreadIds":["child-t2"],"status":"in_progress"}}`))
	// item/completed
	tr.Dispatch(NotifyItemCompleted, json.RawMessage(`{"threadId":"t1","turnId":"turn-1","item":{"id":"spawn-1","type":"collabAgentToolCall","tool":"spawnAgent","receiverThreadIds":["child-t2"],"status":"completed"}}`))

	tr.Dispatch(NotifyTurnCompleted, json.RawMessage(`{"threadId":"t1","turn":{"id":"turn-1","status":"completed"}}`))

	kinds := kindCounts(sink.streams)
	if kinds[StreamToolCallStart] != 1 {
		t.Fatalf("want 1 StreamToolCallStart, got %d", kinds[StreamToolCallStart])
	}
	if kinds[StreamSubagentStart] != 1 {
		t.Fatalf("want 1 StreamSubagentStart, got %d", kinds[StreamSubagentStart])
	}
	if kinds[StreamSubagentEnd] != 1 {
		t.Fatalf("want 1 StreamSubagentEnd, got %d", kinds[StreamSubagentEnd])
	}
	endIndex, toolEndIndex := -1, -1
	for i, payload := range sink.streams {
		switch payload.Kind {
		case StreamSubagentEnd:
			endIndex = i
		case StreamToolCallEnd:
			toolEndIndex = i
		}
	}
	if endIndex < 0 || toolEndIndex < 0 || endIndex > toolEndIndex {
		t.Fatalf("subagent end must precede parent tool end: subagent=%d tool=%d", endIndex, toolEndIndex)
	}
	// Verify onChildThread was called with correct ids
	if gotChildID != "child-t2" {
		t.Fatalf("onChildThread childID: got %q want %q", gotChildID, "child-t2")
	}
	if gotToolCallID != "spawn-1" {
		t.Fatalf("onChildThread toolCallID: got %q want %q", gotToolCallID, "spawn-1")
	}
	// Subagent payloads must carry a SubagentRef
	for _, p := range sink.streams {
		if p.Kind == StreamSubagentStart || p.Kind == StreamSubagentEnd {
			if p.Subagent == nil {
				t.Fatalf("subagent payload %q must have non-nil Subagent", p.Kind)
			}
			if p.Subagent.ID != "child-t2" {
				t.Fatalf("Subagent.ID: got %q want %q", p.Subagent.ID, "child-t2")
			}
		}
	}
}

func TestTranslatorChildScopeEmitsSubagentEnd(t *testing.T) {
	// A child-scope Translator (subagentRef != nil) must emit StreamSubagentEnd
	// on turn/completed instead of StreamRunFinished.
	t.Parallel()
	sink := &recordingSink{}
	ref := &agentadaptor.SubagentRef{ID: "child-t9", Kind: "native", ToolCallID: "spawn-1"}
	childTr := NewTranslatorWithSubagent(sink, "run-child", ref)

	childTr.Dispatch(NotifyTurnStarted, json.RawMessage(`{"threadId":"child-t9","turn":{"id":"child-turn-1","status":"inProgress"}}`))
	// Simulate some child text
	childTr.Dispatch(NotifyItemStarted, json.RawMessage(`{"threadId":"child-t9","turnId":"child-turn-1","item":{"id":"cm1","type":"agentMessage","text":""}}`))
	childTr.Dispatch(NotifyItemAgentMessageDelta, json.RawMessage(`{"delta":"hello","itemId":"cm1","threadId":"child-t9","turnId":"child-turn-1"}`))
	childTr.Dispatch(NotifyItemCompleted, json.RawMessage(`{"threadId":"child-t9","turnId":"child-turn-1","item":{"id":"cm1","type":"agentMessage","text":"hello"}}`))
	childTr.Dispatch(NotifyTurnCompleted, json.RawMessage(`{"threadId":"child-t9","turn":{"id":"child-turn-1","status":"completed"}}`))

	kinds := kindCounts(sink.streams)

	// Must emit subagent.end, NOT run.finished
	if kinds[StreamSubagentEnd] != 1 {
		t.Fatalf("child turn/completed must emit StreamSubagentEnd, got counts: %v", kinds)
	}
	if kinds[StreamRunFinished] != 0 {
		t.Fatalf("child translator must NOT emit StreamRunFinished, got %d", kinds[StreamRunFinished])
	}
	// StreamRunStarted must NOT be emitted for child scope
	if kinds[StreamRunStarted] != 0 {
		t.Fatalf("child translator must NOT emit StreamRunStarted, got %d", kinds[StreamRunStarted])
	}
	// All payloads from child translator must carry the SubagentRef
	for _, p := range sink.streams {
		if p.Subagent == nil {
			t.Fatalf("child translator payload %q must have non-nil Subagent", p.Kind)
		}
		if p.Subagent.ID != "child-t9" {
			t.Fatalf("child payload %q Subagent.ID: got %q want %q", p.Kind, p.Subagent.ID, "child-t9")
		}
	}
	// subagent.end must have status=completed
	for _, p := range sink.streams {
		if p.Kind == StreamSubagentEnd {
			if p.Result["status"] != "completed" {
				t.Fatalf("subagent.end Result.status: %v", p.Result)
			}
		}
	}
}

func TestTranslatorChildScopeEmitsSubagentEndOnFailure(t *testing.T) {
	// Child-scope translator: turn/failed → StreamSubagentEnd(failed)
	t.Parallel()
	sink := &recordingSink{}
	ref := &agentadaptor.SubagentRef{ID: "child-fail", Kind: "native"}
	childTr := NewTranslatorWithSubagent(sink, "run-fail", ref)

	childTr.Dispatch(NotifyTurnStarted, json.RawMessage(`{"threadId":"child-fail","turn":{"id":"cf1","status":"inProgress"}}`))
	childTr.Dispatch(NotifyTurnFailed, json.RawMessage(`{"threadId":"child-fail","turn":{"id":"cf1","status":"failed","error":{"message":"timeout"}}}`))

	kinds := kindCounts(sink.streams)
	if kinds[StreamSubagentEnd] != 1 {
		t.Fatalf("child turn/failed must emit StreamSubagentEnd, got %v", kinds)
	}
	if kinds[StreamRunError] != 0 {
		t.Fatalf("child translator must NOT emit StreamRunError, got %d", kinds[StreamRunError])
	}
	for _, p := range sink.streams {
		if p.Kind == StreamSubagentEnd {
			if p.Result["status"] != "failed" {
				t.Fatalf("subagent.end on failure Result.status: %v", p.Result)
			}
		}
	}
}

func TestTranslatorSubAgentActivityFirstEmitsStartBeforeStatus(t *testing.T) {
	t.Parallel()
	sink := &recordingSink{}
	tr := NewTranslator(sink, "run-sa")

	activity := json.RawMessage(`{"threadId":"t1","turnId":"tn1","item":{"id":"sa1","type":"subAgentActivity","agentThreadId":"child-t5","agentPath":"/root/worker","kind":"working","toolCallId":"spawn-5"}}`)
	tr.Dispatch(NotifyItemCompleted, activity)
	tr.Dispatch(NotifyItemCompleted, activity)

	if len(sink.streams) != 3 {
		t.Fatalf("want exactly one start + two statuses, got %d payloads", len(sink.streams))
	}
	if got := kindCounts(sink.streams)[StreamSubagentStart]; got != 1 {
		t.Fatalf("repeated activity emitted %d starts", got)
	}
	start := sink.streams[0]
	if start.Kind != StreamSubagentStart {
		t.Fatalf("activity-first must open scope before status, got %q", start.Kind)
	}
	if start.Subagent == nil || start.Subagent.ID != "child-t5" ||
		start.Subagent.Name != "/root/worker" || start.Subagent.Kind != "native" ||
		start.Subagent.ToolCallID != "spawn-5" {
		t.Fatalf("unexpected activity-originated start ref: %#v", start.Subagent)
	}

	p := sink.streams[1]
	if p.Kind != StreamSubagentStatus {
		t.Fatalf("subAgentActivity must produce StreamSubagentStatus, got %q", p.Kind)
	}
	if p.Raw == nil {
		t.Fatal("Raw must be preserved for subAgentActivity")
	}
	if p.Subagent == nil {
		t.Fatal("stable agentThreadId must produce a correlated SubagentRef")
	}
	if p.Subagent.ID != "child-t5" || p.Subagent.Name != "/root/worker" ||
		p.Subagent.Kind != "native" || p.Subagent.ToolCallID != "spawn-5" {
		t.Fatalf("unexpected SubagentRef: %#v", p.Subagent)
	}
	if p.Result["status"] != "working" || p.Delta != "working" {
		t.Fatalf("activity status not normalized: delta=%q result=%#v", p.Delta, p.Result)
	}
}

func TestTranslatorCollabFirstDeduplicatesActivityStart(t *testing.T) {
	t.Parallel()
	sink := &recordingSink{}
	tr := NewTranslator(sink, "run-collab-first")

	tr.Dispatch(NotifyItemStarted, json.RawMessage(`{"threadId":"t1","turnId":"tn1","item":{"id":"spawn-6","type":"collabAgentToolCall","tool":"spawnAgent","receiverThreadIds":["child-t6"],"status":"in_progress"}}`))
	tr.Dispatch(NotifyItemCompleted, json.RawMessage(`{"threadId":"t1","turnId":"tn1","item":{"id":"sa6","type":"subAgentActivity","agentThreadId":"child-t6","agentPath":"/root/reviewer","kind":"working","toolCallId":"spawn-6"}}`))

	if got := kindCounts(sink.streams)[StreamSubagentStart]; got != 1 {
		t.Fatalf("collab-first plus activity emitted %d starts", got)
	}
	var startIndex, statusIndex = -1, -1
	for i, payload := range sink.streams {
		switch payload.Kind {
		case StreamSubagentStart:
			startIndex = i
		case StreamSubagentStatus:
			statusIndex = i
			if payload.Subagent == nil || payload.Subagent.ID != "child-t6" ||
				payload.Subagent.Name != "/root/reviewer" ||
				payload.Subagent.ToolCallID != "spawn-6" {
				t.Fatalf("activity status lost correlation: %#v", payload.Subagent)
			}
		}
	}
	if startIndex < 0 || statusIndex <= startIndex {
		t.Fatalf("start-before-status violated: start=%d status=%d", startIndex, statusIndex)
	}
}

func TestTranslatorSubAgentActivityWithoutStableIDRemainsRawInert(t *testing.T) {
	t.Parallel()
	sink := &recordingSink{}
	tr := NewTranslator(sink, "run-sa-opaque")

	tr.Dispatch(NotifyItemCompleted, json.RawMessage(`{"threadId":"t1","turnId":"tn1","item":{"id":"sa2","type":"subAgentActivity","agentPath":"/root/worker","kind":"working"}}`))

	if len(sink.streams) != 1 {
		t.Fatalf("want 1 payload, got %d", len(sink.streams))
	}
	p := sink.streams[0]
	if p.Kind != "" || p.Subagent != nil || p.Raw == nil {
		t.Fatalf("unstable activity must remain raw and inert: %#v", p)
	}
}

func TestChildTranslatorIgnoresActivityForDifferentScope(t *testing.T) {
	t.Parallel()
	sink := &recordingSink{}
	child := NewTranslatorWithSubagent(sink, "run-child-activity", &agentadaptor.SubagentRef{
		ID:         "bound-child",
		Name:       "/root/bound",
		Kind:       "native",
		ToolCallID: "spawn-bound",
	})

	child.Dispatch(NotifyItemCompleted, json.RawMessage(`{"threadId":"bound-child","turnId":"ct1","item":{"id":"nested","type":"subAgentActivity","agentThreadId":"different-child","agentPath":"/root/different","kind":"working","toolCallId":"spawn-different"}}`))

	if len(sink.streams) != 0 {
		t.Fatalf("child translator must ignore activity for another scope: %#v", sink.streams)
	}
}

func TestTranslatorDeduplicatesSubagentEndAcrossCollabAndChildTurn(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name       string
		childFirst bool
	}{
		{name: "child turn completes first", childFirst: true},
		{name: "collab item completes first", childFirst: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sink := &recordingSink{}
			parent := NewTranslator(sink, "run-dedupe")
			ref := &agentadaptor.SubagentRef{
				ID:         "child-dedupe",
				Name:       "collab:spawnAgent",
				Kind:       "native",
				ToolCallID: "spawn-dedupe",
			}
			child := parent.newChildTranslator(ref)

			start := json.RawMessage(`{"threadId":"parent","turnId":"pt","item":{"id":"spawn-dedupe","type":"collabAgentToolCall","tool":"spawnAgent","receiverThreadIds":["child-dedupe"],"status":"in_progress"}}`)
			completed := json.RawMessage(`{"threadId":"parent","turnId":"pt","item":{"id":"spawn-dedupe","type":"collabAgentToolCall","tool":"spawnAgent","receiverThreadIds":["child-dedupe"],"status":"completed"}}`)
			childCompleted := json.RawMessage(`{"threadId":"child-dedupe","turn":{"id":"ct","status":"completed"}}`)

			parent.Dispatch(NotifyItemStarted, start)
			if tc.childFirst {
				child.Dispatch(NotifyTurnCompleted, childCompleted)
				parent.Dispatch(NotifyItemCompleted, completed)
			} else {
				parent.Dispatch(NotifyItemCompleted, completed)
				child.Dispatch(NotifyTurnCompleted, childCompleted)
			}

			if got := kindCounts(sink.streams)[StreamSubagentEnd]; got != 1 {
				t.Fatalf("same Subagent.ID emitted %d terminal events", got)
			}
			for _, payload := range sink.streams {
				if payload.Kind != StreamSubagentEnd {
					continue
				}
				if payload.Subagent == nil || payload.Subagent.ID != "child-dedupe" ||
					payload.Subagent.ToolCallID != "spawn-dedupe" {
					t.Fatalf("terminal event lost correlated ref: %#v", payload.Subagent)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Helpers used by collab tests
// ---------------------------------------------------------------------------

// StreamKind aliases for readability in table assertions.
const (
	StreamToolCallStart  = agentadaptor.StreamToolCallStart
	StreamSubagentStart  = agentadaptor.StreamSubagentStart
	StreamSubagentEnd    = agentadaptor.StreamSubagentEnd
	StreamSubagentStatus = agentadaptor.StreamSubagentStatus
	StreamRunStarted     = agentadaptor.StreamRunStarted
	StreamRunFinished    = agentadaptor.StreamRunFinished
	StreamRunError       = agentadaptor.StreamRunError
	StreamToolCallEnd    = agentadaptor.StreamToolCallEnd
)

func kindCounts(payloads []agentadaptor.StreamPayload) map[agentadaptor.StreamKind]int {
	counts := map[agentadaptor.StreamKind]int{}
	for _, p := range payloads {
		counts[p.Kind]++
	}
	return counts
}

// Ensure fmt stays used when future tests add diagnostic messages.
var _ = fmt.Sprintf
