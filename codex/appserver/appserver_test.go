package appserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agent-dance/agent-adaptor/adaptertest"
	"github.com/agent-dance/agent-adaptor/driver"
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

func TestThreadForkParamsWireShape(t *testing.T) {
	t.Parallel()
	raw, err := json.Marshal(ThreadForkParams{
		ThreadID:       "parent",
		CWD:            "/repo",
		Ephemeral:      true,
		Sandbox:        "workspace-write",
		Model:          "gpt-test",
		ServiceTier:    "fast",
		ApprovalPolicy: "never",
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	want := map[string]any{
		"threadId":       "parent",
		"cwd":            "/repo",
		"ephemeral":      true,
		"sandbox":        "workspace-write",
		"model":          "gpt-test",
		"serviceTier":    "fast",
		"approvalPolicy": "never",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("thread/fork params = %#v, want %#v", got, want)
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

	kinds := map[driver.StreamKind]int{}
	var finishUsage *driver.Usage
	for _, p := range sink.streams {
		kinds[p.Kind]++
		if p.Sequence != 0 || p.Seq != 0 || !p.Timestamp.IsZero() {
			t.Fatalf("translator must leave ordering fields for core: %+v", p)
		}
		if p.RunID != "run-123" {
			t.Fatalf("RunID mismatch on %q: %q", p.Kind, p.RunID)
		}
		if p.Kind == driver.StreamRunFinished {
			finishUsage = p.Usage
		}
	}

	expected := map[driver.StreamKind]int{
		driver.StreamRunStarted:  1,
		driver.StreamTextStart:   1,
		driver.StreamTextContent: 2,
		driver.StreamTextEnd:     1,
		driver.StreamRunFinished: 1,
	}
	for kind, want := range expected {
		if got := kinds[kind]; got != want {
			t.Fatalf("kind %q: want %d got %d (full=%+v)", kind, want, got, kinds)
		}
	}
	if finishUsage == nil || finishUsage.InputTokens != 11 || finishUsage.OutputTokens != 22 || finishUsage.CachedInputTokens != 3 {
		t.Fatalf("StreamRunFinished.Usage not populated from tokenUsage cache: %+v", finishUsage)
	}
	assertNoViolations(t, adaptertest.VerifyStreamSequence(sink.streams))
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
	mu       sync.Mutex
	events   []driver.RunEvent
	streams  []driver.StreamPayload
	onStream func(driver.StreamPayload)
}

func (r *recordingSink) Emit(event driver.RunEvent) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, event)
	return nil
}

func (r *recordingSink) EmitStream(payload driver.StreamPayload) error {
	r.mu.Lock()
	r.streams = append(r.streams, payload)
	hook := r.onStream
	r.mu.Unlock()
	if hook != nil {
		hook(payload)
	}
	return nil
}

func assertNoViolations(t *testing.T, violations []adaptertest.Violation) {
	t.Helper()
	if len(violations) == 0 {
		return
	}
	for _, violation := range violations {
		t.Errorf("driver contract violation: %s", violation)
	}
	t.FailNow()
}

func verifyAppserverContracts(t *testing.T, sink *recordingSink, result driver.Response) {
	t.Helper()
	assertNoViolations(t, adaptertest.VerifyRunEvents(sink.events))
	assertNoViolations(t, adaptertest.VerifyTranscriptMirror(sink.events, result.Transcript))
	assertNoViolations(t, adaptertest.VerifyStreamSequence(sink.streams))
}

// Ensure fmt stays used when future tests add diagnostic messages.
var _ = fmt.Sprintf

func TestRunStateUsesTurnCompletedStatusAsSoleTerminal(t *testing.T) {
	t.Parallel()

	tests := []struct {
		status      TurnStatus
		wantFailure bool
		wantValid   bool
	}{
		{status: TurnStatusCompleted, wantValid: true},
		{status: TurnStatusFailed, wantFailure: true},
		{status: TurnStatusInterrupted, wantFailure: true},
	}
	for _, tc := range tests {
		t.Run(string(tc.status), func(t *testing.T) {
			t.Parallel()
			state := newRunState("run-status", &recordingSink{})
			state.setThread("thread-1")
			state.setTurn("turn-1")
			state.onNotification(NotifyItemCompleted, json.RawMessage(`{"threadId":"thread-1","turnId":"turn-1","item":{"id":"msg-1","type":"agentMessage","text":"assistant text"}}`))
			errorField := ""
			if tc.status != TurnStatusCompleted {
				errorField = `,"error":{"message":"provider stopped"}`
			}
			terminal := fmt.Sprintf(`{"threadId":"thread-1","turn":{"id":"turn-1","status":%q%s,"usage":{"inputTokens":3,"outputTokens":5}}}`, tc.status, errorField)
			state.onNotification(NotifyTurnCompleted, json.RawMessage(terminal))

			result := state.snapshot(Options{Model: "gpt-test"}, "thread-1", "wire stdout", "wire stderr", 0, "", false)
			if got := result.Output; got != "assistant text" {
				t.Fatalf("Output = %q", got)
			}
			if result.Summary != "" {
				t.Fatalf("Summary must not fall back to Output, got %q", result.Summary)
			}
			if (result.Failure != nil) != tc.wantFailure {
				t.Fatalf("Failure = %#v, want present=%v", result.Failure, tc.wantFailure)
			}
			if got := result.Checkpoint != nil && result.Checkpoint.Valid; got != tc.wantValid {
				t.Fatalf("valid checkpoint = %v, want %v", got, tc.wantValid)
			}
			if result.RawStreams == nil || result.RawStreams.Terminal == nil {
				t.Fatal("terminal payload missing")
			}
			if result.RawStreams.Terminal.Event != NotifyTurnCompleted || string(result.RawStreams.Terminal.JSON) != terminal {
				t.Fatalf("terminal = %#v", result.RawStreams.Terminal)
			}
			if len(result.Transcript) < 3 || result.Transcript[len(result.Transcript)-1].Kind != driver.TranscriptResult {
				t.Fatalf("Transcript = %#v", result.Transcript)
			}
		})
	}
}

func TestRunStateOutputUsesOnlyLastCompletedAgentMessage(t *testing.T) {
	t.Parallel()
	state := newRunState("run-final", &recordingSink{})
	state.setThread("thread-final")
	state.setTurn("turn-final")
	state.onNotification(NotifyItemCompleted, json.RawMessage(`{"threadId":"thread-final","turnId":"turn-final","item":{"id":"progress","type":"agentMessage","text":"intermediate progress"}}`))
	state.onNotification(NotifyItemCompleted, json.RawMessage(`{"threadId":"thread-final","turnId":"turn-final","item":{"id":"final","type":"agentMessage","text":"final answer"}}`))
	state.onNotification(NotifyTurnCompleted, json.RawMessage(`{"threadId":"thread-final","turn":{"id":"turn-final","status":"completed"}}`))

	result := state.snapshot(Options{}, "thread-final", "", "", 0, "", false)
	if result.Output != "final answer" {
		t.Fatalf("Output = %q, want final completed agent message", result.Output)
	}
	var assistant []string
	for _, item := range result.Transcript {
		if item.Kind == driver.TranscriptAssistant {
			assistant = append(assistant, item.Text)
		}
	}
	if !reflect.DeepEqual(assistant, []string{"intermediate progress", "final answer"}) {
		t.Fatalf("assistant transcript = %#v", assistant)
	}
}

func TestRunStateRejectsWrongTurnBeforeAbsorbingSemanticOutput(t *testing.T) {
	t.Parallel()
	sink := &recordingSink{}
	state := newRunState("run-wrong-turn", sink)
	state.setThread("thread-expected")
	state.setTurn("turn-expected")
	state.onNotification(NotifyItemCompleted, json.RawMessage(`{"threadId":"thread-expected","turnId":"turn-wrong","item":{"id":"wrong","type":"agentMessage","text":"must not escape"}}`))
	state.onNotification(NotifyItemCompleted, json.RawMessage(`{"threadId":"thread-expected","turnId":"turn-expected","item":{"id":"later","type":"agentMessage","text":"must also not escape"}}`))
	state.onNotification(NotifyTurnCompleted, json.RawMessage(`{"threadId":"thread-expected","turn":{"id":"turn-expected","status":"completed"}}`))
	if err := state.protocolError(); err == nil || !strings.Contains(err.Error(), "belongs to turn") {
		t.Fatalf("protocol error = %v", err)
	}
	result := state.snapshot(Options{}, "thread-expected", "raw", "", 0, "", false)
	if result.Output != "" || result.Checkpoint != nil {
		t.Fatalf("wrong-turn result = %#v", result)
	}
	for _, item := range result.Transcript {
		if item.Kind == driver.TranscriptAssistant || item.Kind == driver.TranscriptResult {
			t.Fatalf("semantic transcript was absorbed after scope failure: %#v", result.Transcript)
		}
	}
	state.finishPublicResult(result, state.protocolError())
	finished, failed := 0, 0
	for _, payload := range sink.streams {
		switch payload.Kind {
		case driver.StreamRunFinished:
			finished++
		case driver.StreamRunError:
			failed++
		}
	}
	if finished != 0 || failed != 1 {
		t.Fatalf("terminal lifecycle finished=%d error=%d streams=%#v", finished, failed, sink.streams)
	}
	select {
	case <-state.done:
	default:
		t.Fatal("wrong-turn semantic event did not terminate the run")
	}
}

func TestRunStateRejectsMalformedTypedDeltas(t *testing.T) {
	t.Parallel()
	for _, method := range []string{
		NotifyItemReasoningTextDelta,
		NotifyItemReasoningSummaryTextDelta,
		NotifyItemCommandExecutionOutputDelta,
		NotifyItemFileChangeOutputDelta,
		NotifyItemPlanDelta,
	} {
		t.Run(method, func(t *testing.T) {
			sink := &recordingSink{}
			state := newRunState("run-malformed-delta", sink)
			state.setThread("thread-delta")
			state.setTurn("turn-delta")
			state.onNotification(method, json.RawMessage(`{"threadId":"thread-delta","turnId":"turn-delta","itemId":"item-delta","delta":7}`))
			state.onNotification(NotifyItemCompleted, json.RawMessage(`{"threadId":"thread-delta","turnId":"turn-delta","item":{"id":"later","type":"agentMessage","text":"must not escape"}}`))
			state.onNotification(NotifyTurnCompleted, json.RawMessage(`{"threadId":"thread-delta","turn":{"id":"turn-delta","status":"completed"}}`))

			err := state.protocolError()
			if err == nil || !strings.Contains(err.Error(), "decode "+method) {
				t.Fatalf("protocol error = %v", err)
			}
			result := state.snapshot(Options{}, "thread-delta", "raw", "", 0, "", false)
			if result.Output != "" || result.Checkpoint != nil {
				t.Fatalf("malformed delta result = %#v", result)
			}
			state.finishPublicResult(result, err)
			assertTerminalLifecycle(t, sink.streams, 0, 1)
		})
	}
}

func TestRunStateQueuesNotificationsUntilRPCIdentitiesAreKnown(t *testing.T) {
	t.Parallel()
	sink := &recordingSink{}
	state := newRunState("run-pending", sink)
	state.onNotification("custom/before-identities", json.RawMessage(`{"audit":true}`))
	state.onNotification(NotifyThreadStarted, json.RawMessage(`{"thread":{"id":"thread-pending"}}`))
	state.onNotification(NotifyTurnStarted, json.RawMessage(`{"threadId":"thread-pending","turn":{"id":"turn-pending","status":"inProgress"}}`))
	state.onNotification(NotifyItemCompleted, json.RawMessage(`{"threadId":"thread-pending","turnId":"turn-pending","item":{"id":"final","type":"agentMessage","text":"final after RPC"}}`))
	state.onNotification(NotifyTurnCompleted, json.RawMessage(`{"threadId":"thread-pending","turn":{"id":"turn-pending","status":"completed"}}`))
	state.onNotification("custom/after-pending-terminal", json.RawMessage(`{"audit":true}`))
	select {
	case <-state.done:
		t.Fatal("pending notification completed before RPC identities were known")
	default:
	}

	state.setThread("thread-pending")
	state.setTurn("turn-pending")
	result := state.snapshot(Options{}, "thread-pending", "", "", 0, "", false)
	if result.Output != "final after RPC" || result.Checkpoint == nil || !result.Checkpoint.Valid {
		t.Fatalf("flushed pending result = %#v", result)
	}
	select {
	case <-state.done:
	default:
		t.Fatal("official pending terminal did not complete after RPC binding")
	}
	for _, payload := range sink.streams {
		if payload.Name == "custom/after-pending-terminal" {
			t.Fatalf("wire frame after pending terminal escaped to public stream: %+v", payload)
		}
	}
	foundEarly := false
	for _, payload := range sink.streams {
		if payload.Name == "custom/before-identities" {
			foundEarly = true
			if payload.ThreadID != "thread-pending" || payload.TurnID != "turn-pending" {
				t.Fatalf("queued custom notification identity = %q/%q", payload.ThreadID, payload.TurnID)
			}
		}
		if payload.Kind == driver.StreamRunStarted && (payload.ThreadID != "thread-pending" || payload.TurnID != "turn-pending") {
			t.Fatalf("queued run.started identity = %q/%q", payload.ThreadID, payload.TurnID)
		}
	}
	if !foundEarly {
		t.Fatalf("unknown notification queued before identities was not flushed: %#v", sink.streams)
	}
}

func TestRunStateErrorNotificationIsNotTerminal(t *testing.T) {
	t.Parallel()
	state := newRunState("run-error", &recordingSink{})
	state.setThread("t")
	state.setTurn("turn")
	state.onNotification(NotifyError, json.RawMessage(`{"threadId":"t","turnId":"turn","willRetry":false,"error":{"message":"transient"}}`))
	select {
	case <-state.done:
		t.Fatal("error notification must not terminate the run")
	default:
	}
	if state.hasTerminal() {
		t.Fatal("error notification must not create a terminal payload")
	}
}

func TestRunStateTranscriptAlwaysMirrorsItemEvents(t *testing.T) {
	t.Parallel()
	sink := &recordingSink{}
	state := newRunState("run-mirror", sink)
	state.setThread("thread-mirror")
	state.setTurn("turn-mirror")
	state.onNotification(NotifyTurnStarted, json.RawMessage(`{"threadId":"thread-mirror","turn":{"id":"turn-mirror","status":"inProgress"}}`))
	state.onNotification(NotifyItemStarted, json.RawMessage(`{"threadId":"thread-mirror","turnId":"turn-mirror","item":{"id":"tool-1","type":"commandExecution","command":"go test ./...","cwd":"/repo","status":"inProgress"}}`))
	state.onNotification(NotifyItemCompleted, json.RawMessage(`{"threadId":"thread-mirror","turnId":"turn-mirror","item":{"id":"tool-1","type":"commandExecution","command":"go test ./...","cwd":"/repo","status":"completed","exitCode":0,"aggregatedOutput":"ok"}}`))
	state.onNotification(NotifyError, json.RawMessage(`{"threadId":"thread-mirror","turnId":"turn-mirror","willRetry":true,"error":{"message":"retrying provider request"}}`))
	state.onNotification(NotifyTurnCompleted, json.RawMessage(`{"threadId":"thread-mirror","turn":{"id":"turn-mirror","status":"completed","usage":{"inputTokens":2,"outputTokens":3}}}`))

	result := state.snapshot(Options{}, "thread-mirror", "raw stdout", "", 0, "", false)
	state.finishPublicResult(result, nil)
	verifyAppserverContracts(t, sink, result)
	if len(result.Transcript) != 5 {
		t.Fatalf("Transcript = %#v, want init/tool call/tool result/system/result", result.Transcript)
	}
	eventsBefore := len(sink.events)
	streamsBefore := len(sink.streams)
	state.onNotification(NotifyError, json.RawMessage(`{"willRetry":false,"error":{"message":"after terminal"}}`))
	state.onNotification("custom/after-terminal", json.RawMessage(`{"preserved":true}`))
	if len(sink.events) != eventsBefore || len(sink.streams) != streamsBefore {
		t.Fatalf("terminal boundary leaked public events: events %d->%d streams %d->%d", eventsBefore, len(sink.events), streamsBefore, len(sink.streams))
	}
	if err := state.protocolError(); err != nil {
		t.Fatalf("non-terminal trailing frame poisoned completed run: %v", err)
	}
	if checkpoint := state.snapshot(Options{}, "thread-mirror", "", "", 0, "", false).Checkpoint; checkpoint == nil || !checkpoint.Valid {
		t.Fatalf("trailing audit-only frame cleared checkpoint: %#v", checkpoint)
	}
}

func TestRunStateMalformedAndMissingTerminalNeverCheckpoint(t *testing.T) {
	t.Parallel()
	malformed := newRunState("run-malformed", &recordingSink{})
	malformed.setThread("thread-1")
	malformed.setTurn("turn-1")
	malformed.onNotification(NotifyTurnCompleted, json.RawMessage(`{"turn":`))
	if malformed.protocolError() == nil {
		t.Fatal("malformed terminal must record a protocol error")
	}
	if result := malformed.snapshot(Options{}, "thread-1", "", "", 0, "", false); result.Checkpoint != nil {
		t.Fatalf("malformed terminal checkpoint = %#v", result.Checkpoint)
	}

	missing := newRunState("run-missing", &recordingSink{})
	missing.setThread("thread-1")
	if missing.hasTerminal() {
		t.Fatal("fresh state unexpectedly has terminal")
	}
	if result := missing.snapshot(Options{}, "thread-1", "", "", 0, "", false); result.Checkpoint != nil {
		t.Fatalf("missing terminal checkpoint = %#v", result.Checkpoint)
	}
}

func TestRunCapturesExactFullOutputAndOfficialTerminal(t *testing.T) {
	fake := buildFakeAppserver(t)
	sink := &recordingSink{}
	result, err := Run(context.Background(), Options{
		Command: fake,
		Env:     []driver.EnvBinding{{Name: "FAKE_APPSERVER_SCENARIO", Value: "success"}},
		Prompt:  "hello",
		Model:   "gpt-test",
		RunID:   "run-full-output",
	}, sink)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Output != "hello from fake" || result.Summary != "" {
		t.Fatalf("Output/Summary = %q/%q", result.Output, result.Summary)
	}
	if result.RawStreams == nil {
		t.Fatal("RawStreams missing")
	}
	if !strings.Contains(result.RawStreams.Stdout, "  {\"method\":\"turn/completed\"") {
		t.Fatalf("stdout lost exact terminal frame formatting: %q", result.RawStreams.Stdout)
	}
	if !strings.Contains(result.RawStreams.Stdout, `"method":"custom/after-terminal"`) {
		t.Fatalf("stdout snapshot occurred before reader EOF: %q", result.RawStreams.Stdout)
	}
	if result.RawStreams.Stderr != "stderr-after-terminal" {
		t.Fatalf("stderr snapshot occurred before process end: %q", result.RawStreams.Stderr)
	}
	if result.RawStreams.Terminal == nil || result.RawStreams.Terminal.Event != NotifyTurnCompleted || !json.Valid(result.RawStreams.Terminal.JSON) {
		t.Fatalf("terminal = %#v", result.RawStreams.Terminal)
	}
	if result.Checkpoint == nil || !result.Checkpoint.Valid || result.Checkpoint.State.ResumeID != "thread-fake" {
		t.Fatalf("checkpoint = %#v", result.Checkpoint)
	}
	if result.Usage == nil || result.Usage.InputTokens != 7 || result.Usage.OutputTokens != 11 {
		t.Fatalf("usage = %#v", result.Usage)
	}
	if len(result.Transcript) < 3 {
		t.Fatalf("transcript = %#v", result.Transcript)
	}
	verifyAppserverContracts(t, sink, result)
	if got := sink.streams[len(sink.streams)-1].Kind; got != driver.StreamRunFinished {
		t.Fatalf("last public payload = %q, want %q", got, driver.StreamRunFinished)
	}
	for _, payload := range sink.streams {
		if payload.Name == "custom/after-terminal" {
			t.Fatalf("after-terminal diagnostic escaped to public stream: %+v", payload)
		}
	}
}

func TestRunStagesTerminalUntilProcessOutcome(t *testing.T) {
	fake := buildFakeAppserver(t)
	sink := &recordingSink{}
	result, err := Run(context.Background(), Options{
		Command: fake,
		Env:     []driver.EnvBinding{{Name: "FAKE_APPSERVER_SCENARIO", Value: "nonzero-after-terminal"}},
		Prompt:  "hello",
		RunID:   "run-nonzero-after-terminal",
	}, sink)
	if err != nil {
		t.Fatalf("Run returned infrastructure error: %v", err)
	}
	if result.ExitCode != 17 || result.Failure == nil || result.Failure.Code != driver.FailureAgentError {
		t.Fatalf("process failure result = %#v", result)
	}
	if result.Checkpoint != nil {
		t.Fatalf("nonzero exit retained checkpoint: %#v", result.Checkpoint)
	}
	assertTerminalLifecycle(t, sink.streams, 0, 1)
}

func TestRunStagesTerminalUntilStructuredOutputValidation(t *testing.T) {
	fake := buildFakeAppserver(t)
	schema := &driver.OutputSchema{
		Format:     driver.OutputFormatJSONSchema,
		Mode:       driver.StructuredOutputNativeStrict,
		SchemaJSON: json.RawMessage(`{"type":"object","properties":{"answer":{"type":"integer"}},"required":["answer"],"additionalProperties":false}`),
		OnInvalid:  driver.StructuredOutputFailRun,
	}
	for _, tc := range []struct {
		name         string
		scenario     string
		wantFinished int
		wantError    int
		wantValid    bool
	}{
		{name: "valid", scenario: "structured-valid", wantFinished: 1, wantValid: true},
		{name: "invalid", scenario: "success", wantError: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sink := &recordingSink{}
			result, err := Run(context.Background(), Options{
				Command:      fake,
				Env:          []driver.EnvBinding{{Name: "FAKE_APPSERVER_SCENARIO", Value: tc.scenario}},
				ForkThreadID: "thread-parent",
				Prompt:       "structured",
				RunID:        "run-structured-" + tc.name,
				OutputSchema: schema,
			}, sink)
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if tc.wantValid {
				if result.Failure != nil || result.StructuredOutput == nil || !result.StructuredOutput.Valid || result.Checkpoint == nil || !result.Checkpoint.Valid {
					t.Fatalf("valid structured result = %#v", result)
				}
			} else {
				if result.Failure == nil || result.Failure.Code != driver.FailurePolicyError || result.StructuredOutput == nil || result.StructuredOutput.Valid || result.Checkpoint != nil {
					t.Fatalf("invalid structured result = %#v", result)
				}
			}
			assertTerminalLifecycle(t, sink.streams, tc.wantFinished, tc.wantError)
		})
	}
}

func TestRunProtocolFatalNotificationStopsSemanticIngestion(t *testing.T) {
	fake := buildFakeAppserver(t)
	sink := &recordingSink{}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	result, err := Run(ctx, Options{
		Command: fake,
		Env:     []driver.EnvBinding{{Name: "FAKE_APPSERVER_SCENARIO", Value: "malformed-item-live"}},
		Prompt:  "hello",
		RunID:   "run-malformed-live",
	}, sink)
	if err == nil || !strings.Contains(err.Error(), "protocol") {
		t.Fatalf("Run error = %v, want protocol failure", err)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("protocol failure waited for context deadline: %v", err)
	}
	if result.Output != "" || result.Checkpoint != nil {
		t.Fatalf("semantic state changed after malformed notification: %#v", result)
	}
	for _, item := range result.Transcript {
		if item.Kind == driver.TranscriptAssistant || item.Kind == driver.TranscriptResult {
			t.Fatalf("malformed notification admitted later semantic item: %#v", result.Transcript)
		}
	}
	assertTerminalLifecycle(t, sink.streams, 0, 1)
}

func TestRunRejectsWhitespaceRPCIdentities(t *testing.T) {
	fake := buildFakeAppserver(t)
	for _, tc := range []struct {
		name     string
		scenario string
		options  Options
	}{
		{name: "thread start", scenario: "whitespace-thread-start"},
		{name: "thread resume", scenario: "whitespace-thread-resume", options: Options{ResumeThreadID: "thread-parent"}},
		{name: "thread fork", scenario: "whitespace-thread-fork", options: Options{ForkThreadID: "thread-parent"}},
		{name: "turn start", scenario: "whitespace-turn-start"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opts := tc.options
			opts.Command = fake
			opts.Env = []driver.EnvBinding{{Name: "FAKE_APPSERVER_SCENARIO", Value: tc.scenario}}
			_, err := Run(context.Background(), opts, &recordingSink{})
			if err == nil || !strings.Contains(err.Error(), "empty") {
				t.Fatalf("Run error = %v, want empty identity rejection", err)
			}
		})
	}
}

func TestRunStateRejectsWhitespaceRPCIdentities(t *testing.T) {
	thread := newRunState("thread-space", &recordingSink{})
	thread.setThread(" \t")
	if err := thread.protocolError(); err == nil || !strings.Contains(err.Error(), "thread identity") {
		t.Fatalf("thread identity error = %v", err)
	}
	select {
	case <-thread.done:
	default:
		t.Fatal("whitespace thread identity did not terminate state")
	}

	turn := newRunState("turn-space", &recordingSink{})
	turn.setThread("thread-valid")
	turn.setTurn(" \t")
	if err := turn.protocolError(); err == nil || !strings.Contains(err.Error(), "turn identity") {
		t.Fatalf("turn identity error = %v", err)
	}
	select {
	case <-turn.done:
	default:
		t.Fatal("whitespace turn identity did not terminate state")
	}
}

func assertTerminalLifecycle(t *testing.T, streams []driver.StreamPayload, wantFinished, wantError int) {
	t.Helper()
	finished, failed := 0, 0
	for _, payload := range streams {
		switch payload.Kind {
		case driver.StreamRunFinished:
			finished++
		case driver.StreamRunError:
			failed++
		}
	}
	if finished != wantFinished || failed != wantError {
		t.Fatalf("terminal lifecycle finished=%d error=%d, want %d/%d; streams=%#v", finished, failed, wantFinished, wantError, streams)
	}
}

func TestRunForksParentAndRunsTurnOnChild(t *testing.T) {
	fake := buildFakeAppserver(t)
	result, err := Run(context.Background(), Options{
		Command:      fake,
		Env:          []driver.EnvBinding{{Name: "FAKE_APPSERVER_SCENARIO", Value: "success"}},
		ForkThreadID: "thread-parent",
		Prompt:       "forked work",
	}, &recordingSink{})
	if err != nil {
		t.Fatalf("Run fork: %v", err)
	}
	if result.Checkpoint == nil || result.Checkpoint.State == nil || result.Checkpoint.State.ResumeID != "thread-child" {
		t.Fatalf("fork checkpoint = %#v, want child thread", result.Checkpoint)
	}
	if result.Checkpoint.State.ResumeID == "thread-parent" {
		t.Fatal("fork returned the parent checkpoint")
	}
}

func TestRunResumeRejectsProviderThreadIdentityChange(t *testing.T) {
	fake := buildFakeAppserver(t)
	result, err := Run(context.Background(), Options{
		Command:        fake,
		Env:            []driver.EnvBinding{{Name: "FAKE_APPSERVER_SCENARIO", Value: "success"}},
		ResumeThreadID: "thread-parent",
	}, &recordingSink{})
	if err == nil || !strings.Contains(err.Error(), "returned thread id") {
		t.Fatalf("error = %v, want resume identity mismatch", err)
	}
	if result.Checkpoint != nil {
		t.Fatalf("resume identity mismatch produced checkpoint %#v", result.Checkpoint)
	}
}

func TestRunRejectsNotificationOutsideExpectedThreadAndTurn(t *testing.T) {
	fake := buildFakeAppserver(t)
	sink := &recordingSink{}
	result, err := Run(context.Background(), Options{
		Command: fake,
		Env:     []driver.EnvBinding{{Name: "FAKE_APPSERVER_SCENARIO", Value: "scope-mismatch"}},
	}, sink)
	if err == nil || !strings.Contains(err.Error(), "belongs to thread") {
		t.Fatalf("error = %v, want notification scope mismatch", err)
	}
	if result.Checkpoint != nil || result.Output != "" || result.RawStreams == nil || result.RawStreams.Stdout == "" {
		t.Fatalf("scope mismatch result = %#v", result)
	}
	for _, item := range result.Transcript {
		if item.Kind == driver.TranscriptAssistant || item.Kind == driver.TranscriptResult {
			t.Fatalf("wrong-thread semantic item escaped: %#v", result.Transcript)
		}
	}
	if len(sink.streams) == 0 || sink.streams[len(sink.streams)-1].Kind != driver.StreamRunError {
		t.Fatalf("scope mismatch stream terminal = %#v", sink.streams)
	}
}

func TestRunRejectsConflictingResumeAndForkBeforeSpawn(t *testing.T) {
	_, err := Run(context.Background(), Options{
		Command:        filepath.Join(t.TempDir(), "must-not-spawn"),
		ResumeThreadID: "resume",
		ForkThreadID:   "fork",
	}, &recordingSink{})
	if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("error = %v, want selector conflict", err)
	}
}

func TestRunOfficialFailureStatusesAndProtocolFailures(t *testing.T) {
	fake := buildFakeAppserver(t)
	for _, status := range []string{"failed", "interrupted"} {
		t.Run(status, func(t *testing.T) {
			sink := &recordingSink{}
			result, err := Run(context.Background(), Options{
				Command: fake,
				Env:     []driver.EnvBinding{{Name: "FAKE_APPSERVER_SCENARIO", Value: status}},
			}, sink)
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if result.Failure == nil || result.Checkpoint != nil || result.RawStreams.Terminal == nil {
				t.Fatalf("failure result = %#v", result)
			}
			verifyAppserverContracts(t, sink, result)
			if got := sink.streams[len(sink.streams)-1].Kind; got != driver.StreamRunError {
				t.Fatalf("last public payload = %q, want %q", got, driver.StreamRunError)
			}
		})
	}
	for _, scenario := range []string{"missing", "malformed"} {
		t.Run(scenario, func(t *testing.T) {
			sink := &recordingSink{}
			result, err := Run(context.Background(), Options{
				Command: fake,
				Env:     []driver.EnvBinding{{Name: "FAKE_APPSERVER_SCENARIO", Value: scenario}},
			}, sink)
			if err == nil {
				t.Fatal("expected protocol error")
			}
			if result.Checkpoint != nil || result.RawStreams == nil || result.RawStreams.Stdout == "" {
				t.Fatalf("partial result = %#v", result)
			}
			verifyAppserverContracts(t, sink, result)
			if got := sink.streams[len(sink.streams)-1].Kind; got != driver.StreamRunError {
				t.Fatalf("last public payload = %q, want %q", got, driver.StreamRunError)
			}
		})
	}
}

func TestRunCancellationReturnsPartialRawWithoutCheckpoint(t *testing.T) {
	fake := buildFakeAppserver(t)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	sink := &recordingSink{onStream: func(payload driver.StreamPayload) {
		if payload.Kind == driver.StreamRunStarted {
			cancel()
		}
	}}
	result, err := Run(ctx, Options{
		Command: fake,
		Env:     []driver.EnvBinding{{Name: "FAKE_APPSERVER_SCENARIO", Value: "cancel"}},
	}, sink)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v", err)
	}
	if result.Checkpoint != nil || result.RawStreams == nil || result.RawStreams.Stdout == "" {
		t.Fatalf("cancelled partial result = %#v", result)
	}
	verifyAppserverContracts(t, sink, result)
	if got := sink.streams[len(sink.streams)-1].Kind; got != driver.StreamRunError {
		t.Fatalf("last public payload = %q, want %q", got, driver.StreamRunError)
	}
}

func buildFakeAppserver(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	source := filepath.Join(dir, "fake_appserver.go")
	if err := os.WriteFile(source, []byte(fakeAppserverSource), 0o600); err != nil {
		t.Fatalf("write fake app-server: %v", err)
	}
	binary := filepath.Join(dir, "fake-appserver")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	cmd := exec.Command("go", "build", "-o", binary, source)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build fake app-server: %v\n%s", err, output)
	}
	return binary
}

const fakeAppserverSource = `package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
)

type request struct {
	ID json.RawMessage ` + "`json:\"id\"`" + `
	Method string ` + "`json:\"method\"`" + `
	Params json.RawMessage ` + "`json:\"params\"`" + `
}

func main() {
	scenario := os.Getenv("FAKE_APPSERVER_SCENARIO")
	decoder := json.NewDecoder(os.Stdin)
	writer := bufio.NewWriter(os.Stdout)
	defer func() {
		_ = writer.Flush()
		_, _ = fmt.Fprint(os.Stderr, "stderr-after-terminal")
	}()
	write := func(frame string) {
		_, _ = fmt.Fprintln(writer, frame)
		_ = writer.Flush()
	}
	respond := func(id json.RawMessage, result string) {
		write(fmt.Sprintf("{\"id\":%s,\"result\":%s}", id, result))
	}
	currentThread := ""
	for {
		var req request
		if err := decoder.Decode(&req); err != nil {
			return
		}
		switch req.Method {
		case "initialize":
			respond(req.ID, "{}")
		case "thread/start", "thread/resume":
			if (req.Method == "thread/start" && scenario == "whitespace-thread-start") || (req.Method == "thread/resume" && scenario == "whitespace-thread-resume") {
				respond(req.ID, "{\"thread\":{\"id\":\" \"}}")
				continue
			}
			currentThread = "thread-fake"
			respond(req.ID, "{\"thread\":{\"id\":\"thread-fake\"}}")
		case "thread/fork":
			if scenario == "whitespace-thread-fork" {
				respond(req.ID, "{\"thread\":{\"id\":\" \"}}")
				continue
			}
			var params struct { ThreadID string ` + "`json:\"threadId\"`" + ` }
			if err := json.Unmarshal(req.Params, &params); err != nil || params.ThreadID != "thread-parent" {
				write(fmt.Sprintf("{\"id\":%s,\"error\":{\"code\":-32602,\"message\":\"wrong fork parent\"}}", req.ID))
				continue
			}
			currentThread = "thread-child"
			respond(req.ID, "{\"thread\":{\"id\":\"thread-child\"}}")
		case "turn/start":
			var params struct { ThreadID string ` + "`json:\"threadId\"`" + ` }
			if err := json.Unmarshal(req.Params, &params); err != nil || params.ThreadID != currentThread {
				write(fmt.Sprintf("{\"id\":%s,\"error\":{\"code\":-32602,\"message\":\"turn started on wrong thread\"}}", req.ID))
				continue
			}
			if scenario == "whitespace-turn-start" {
				respond(req.ID, "{\"turn\":{\"id\":\" \",\"status\":\"inProgress\"}}")
				continue
			}
			respond(req.ID, "{\"turn\":{\"id\":\"turn-fake\",\"status\":\"inProgress\"}}")
			eventThread := currentThread
			if scenario == "scope-mismatch" {
				eventThread = "wrong-thread"
			}
			write(fmt.Sprintf("{\"method\":\"turn/started\",\"params\":{\"threadId\":%q,\"turn\":{\"id\":\"turn-fake\",\"status\":\"inProgress\"}}}", eventThread))
			if scenario == "cancel" {
				continue
			}
			if scenario == "malformed-item-live" {
				write(fmt.Sprintf("{\"method\":\"item/completed\",\"params\":{\"threadId\":%q,\"turnId\":\"turn-fake\",\"item\":{\"id\":\"bad\",\"type\":\"agentMessage\",\"text\":7}}}", eventThread))
			}
			message := "hello from fake"
			if scenario == "structured-valid" {
				message = ` + "`" + `{"answer":42}` + "`" + `
			}
			write(fmt.Sprintf("{\"method\":\"item/started\",\"params\":{\"threadId\":%q,\"turnId\":\"turn-fake\",\"item\":{\"id\":\"msg-fake\",\"type\":\"agentMessage\",\"text\":\"\"}}}", eventThread))
			write(fmt.Sprintf("{\"method\":\"item/agentMessage/delta\",\"params\":{\"delta\":%q,\"itemId\":\"msg-fake\",\"threadId\":%q,\"turnId\":\"turn-fake\"}}", message, eventThread))
			write(fmt.Sprintf("{\"method\":\"item/completed\",\"params\":{\"threadId\":%q,\"turnId\":\"turn-fake\",\"item\":{\"id\":\"msg-fake\",\"type\":\"agentMessage\",\"text\":%q}}}", eventThread, message))
			if scenario == "missing" {
				return
			}
			if scenario == "malformed" {
				write("{\"method\":")
				return
			}
			status := scenario
			if status == "scope-mismatch" {
				status = "completed"
			}
			if status == "success" || status == "" || status == "structured-valid" || status == "nonzero-after-terminal" || status == "malformed-item-live" {
				status = "completed"
			}
			errorField := ""
			if status != "completed" {
				errorField = fmt.Sprintf(",\"error\":{\"message\":\"provider %s\"}", status)
			}
			frame := fmt.Sprintf("  {\"method\":\"turn/completed\",\"params\":{\"threadId\":%q,\"turn\":{\"id\":\"turn-fake\",\"status\":\"%s\"%s,\"usage\":{\"inputTokens\":7,\"outputTokens\":11}}}}", eventThread, status, errorField)
			write(frame)
			write("{\"method\":\"custom/after-terminal\",\"params\":{\"preserved\":true}}")
			if scenario == "nonzero-after-terminal" {
				_ = writer.Flush()
				os.Exit(17)
			}
		case "turn/interrupt":
			respond(req.ID, "{}")
			write(fmt.Sprintf("{\"method\":\"turn/completed\",\"params\":{\"threadId\":%q,\"turn\":{\"id\":\"turn-fake\",\"status\":\"interrupted\",\"error\":{\"message\":\"cancelled\"}}}}", currentThread))
		}
	}
}
`
