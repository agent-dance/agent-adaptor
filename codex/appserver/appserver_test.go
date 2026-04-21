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
		agentadaptor.StreamRunStarted:   1,
		agentadaptor.StreamTextStart:    1,
		agentadaptor.StreamTextContent:  2,
		agentadaptor.StreamTextEnd:      1,
		agentadaptor.StreamRunFinished:  1,
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

// Ensure fmt stays used when future tests add diagnostic messages.
var _ = fmt.Sprintf
