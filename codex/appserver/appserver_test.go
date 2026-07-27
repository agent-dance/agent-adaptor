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
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

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
	var lastSeq uint64
	var finishUsage *driver.Usage
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
	events  []driver.RunEvent
	streams []driver.StreamPayload
}

func (r *recordingSink) Emit(event driver.RunEvent) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, event)
	return nil
}

func (r *recordingSink) EmitStream(payload driver.StreamPayload) error {
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

func TestRunStateErrorNotificationIsNotTerminal(t *testing.T) {
	t.Parallel()
	state := newRunState("run-error", &recordingSink{})
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

func TestRunStateMalformedAndMissingTerminalNeverCheckpoint(t *testing.T) {
	t.Parallel()
	malformed := newRunState("run-malformed", &recordingSink{})
	malformed.setThread("thread-1")
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
	result, err := Run(context.Background(), Options{
		Command: fake,
		Env:     []driver.EnvBinding{{Name: "FAKE_APPSERVER_SCENARIO", Value: "success"}},
		Prompt:  "hello",
		Model:   "gpt-test",
		RunID:   "run-full-output",
	}, &recordingSink{})
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
}

func TestRunOfficialFailureStatusesAndProtocolFailures(t *testing.T) {
	fake := buildFakeAppserver(t)
	for _, status := range []string{"failed", "interrupted"} {
		t.Run(status, func(t *testing.T) {
			result, err := Run(context.Background(), Options{
				Command: fake,
				Env:     []driver.EnvBinding{{Name: "FAKE_APPSERVER_SCENARIO", Value: status}},
			}, &recordingSink{})
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if result.Failure == nil || result.Checkpoint != nil || result.RawStreams.Terminal == nil {
				t.Fatalf("failure result = %#v", result)
			}
		})
	}
	for _, scenario := range []string{"missing", "malformed"} {
		t.Run(scenario, func(t *testing.T) {
			result, err := Run(context.Background(), Options{
				Command: fake,
				Env:     []driver.EnvBinding{{Name: "FAKE_APPSERVER_SCENARIO", Value: scenario}},
			}, &recordingSink{})
			if err == nil {
				t.Fatal("expected protocol error")
			}
			if result.Checkpoint != nil || result.RawStreams == nil || result.RawStreams.Stdout == "" {
				t.Fatalf("partial result = %#v", result)
			}
		})
	}
}

func TestRunCancellationReturnsPartialRawWithoutCheckpoint(t *testing.T) {
	fake := buildFakeAppserver(t)
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	result, err := Run(ctx, Options{
		Command: fake,
		Env:     []driver.EnvBinding{{Name: "FAKE_APPSERVER_SCENARIO", Value: "cancel"}},
	}, &recordingSink{})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v", err)
	}
	if result.Checkpoint != nil || result.RawStreams == nil || result.RawStreams.Stdout == "" {
		t.Fatalf("cancelled partial result = %#v", result)
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
	for {
		var req request
		if err := decoder.Decode(&req); err != nil {
			return
		}
		switch req.Method {
		case "initialize":
			respond(req.ID, "{}")
		case "thread/start", "thread/resume":
			respond(req.ID, "{\"thread\":{\"id\":\"thread-fake\"}}")
		case "turn/start":
			respond(req.ID, "{\"turn\":{\"id\":\"turn-fake\",\"status\":\"inProgress\"}}")
			if scenario == "cancel" {
				continue
			}
			write("{\"method\":\"item/completed\",\"params\":{\"threadId\":\"thread-fake\",\"turnId\":\"turn-fake\",\"item\":{\"id\":\"msg-fake\",\"type\":\"agentMessage\",\"text\":\"hello from fake\"}}}")
			if scenario == "missing" {
				return
			}
			if scenario == "malformed" {
				write("{\"method\":")
				return
			}
			status := scenario
			if status == "success" || status == "" {
				status = "completed"
			}
			errorField := ""
			if status != "completed" {
				errorField = fmt.Sprintf(",\"error\":{\"message\":\"provider %s\"}", status)
			}
			frame := fmt.Sprintf("  {\"method\":\"turn/completed\",\"params\":{\"threadId\":\"thread-fake\",\"turn\":{\"id\":\"turn-fake\",\"status\":\"%s\"%s,\"usage\":{\"inputTokens\":7,\"outputTokens\":11}}}}", status, errorField)
			write(frame)
			write("{\"method\":\"custom/after-terminal\",\"params\":{\"preserved\":true}}")
		case "turn/interrupt":
			respond(req.ID, "{}")
			write("{\"method\":\"turn/completed\",\"params\":{\"threadId\":\"thread-fake\",\"turn\":{\"id\":\"turn-fake\",\"status\":\"interrupted\",\"error\":{\"message\":\"cancelled\"}}}}")
		}
	}
}
`
