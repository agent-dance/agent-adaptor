package cursor

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agent-dance/agent-adaptor/driver"
)

type recordingSink struct {
	mu    sync.Mutex
	items []driver.TranscriptItem
}

func (r *recordingSink) Emit(event driver.RunEvent) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if event.Type == driver.RunEventItem && event.Item != nil {
		r.items = append(r.items, *event.Item)
	}
	return nil
}

func (r *recordingSink) EmitStream(driver.StreamPayload) error { return nil }

func loadFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture %q: %v", name, err)
	}
	return data
}

func parseCursorFixture(t *testing.T, name string, sink driver.EventSink) *cursorParser {
	t.Helper()
	p := newCursorParser(sink)
	if err := p.onChunk("stdout", loadFixture(t, name), time.Now().UTC()); err != nil {
		t.Fatalf("feed fixture: %v", err)
	}
	p.finalize()
	return p
}

func TestCursorParserOfficialAssistantDeltasProduceOutput(t *testing.T) {
	sink := &recordingSink{}
	p := parseCursorFixture(t, "happy-assistant.jsonl", sink)

	if got, want := p.buildOutput(), "Cursor says hi."; got != want {
		t.Fatalf("assistant output = %q, want %q", got, want)
	}
	if got := p.finalSummary(); got != "" {
		t.Fatalf("Cursor has no bounded provider summary; got %q", got)
	}
	if p.usage != nil {
		t.Fatalf("undocumented usage must not be inferred: %#v", p.usage)
	}
	checkpoint := p.checkpoint(0)
	if checkpoint == nil || checkpoint.State == nil || checkpoint.State.ResumeID != "cursor-happy" {
		t.Fatalf("checkpoint = %#v", checkpoint)
	}
	if p.terminal == nil || p.terminal.Event != "result" || !json.Valid(p.terminal.JSON) {
		t.Fatalf("terminal = %#v", p.terminal)
	}
	if !reflect.DeepEqual(p.transcript, sink.items) {
		t.Fatal("sink and transcript diverge")
	}

	var deltas strings.Builder
	for _, item := range p.transcript {
		if item.Kind == driver.TranscriptAssistant {
			if !item.Delta {
				t.Fatalf("assistant stream-json item is not marked delta: %#v", item)
			}
			deltas.WriteString(item.Text)
		}
	}
	if got := deltas.String(); got != p.buildOutput() {
		t.Fatalf("concatenated transcript deltas = %q, Output = %q", got, p.buildOutput())
	}
}

func TestCursorParserAssistantDeltaWhitespaceIsPreserved(t *testing.T) {
	p := parseCursorFixture(t, "multi-message.jsonl", nil)
	want := "First paragraph from Cursor.\n\nSecond paragraph closing it."
	if got := p.buildOutput(); got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
	if got := p.finalSummary(); got != "" {
		t.Fatalf("Summary must not reuse the full result text, got %q", got)
	}
}

func TestCursorParserOfficialToolStartAndCompletion(t *testing.T) {
	p := parseCursorFixture(t, "with-tool.jsonl", nil)

	callIndex, resultIndex := -1, -1
	var call, result driver.TranscriptItem
	for i, item := range p.transcript {
		switch item.Kind {
		case driver.TranscriptToolCall:
			callIndex, call = i, item
		case driver.TranscriptToolResult:
			resultIndex, result = i, item
		}
	}
	if callIndex < 0 || resultIndex <= callIndex {
		t.Fatalf("tool lifecycle order is invalid: call=%d result=%d transcript=%#v", callIndex, resultIndex, p.transcript)
	}
	if call.ToolUseID != "toolu-read-1" || call.ToolName != "readToolCall" {
		t.Fatalf("tool call = %#v", call)
	}
	args, ok := call.Input.(map[string]any)
	if !ok || args["path"] != "README.md" {
		t.Fatalf("tool args = %#v", call.Input)
	}
	if result.ToolUseID != call.ToolUseID || result.ToolName != call.ToolName || result.IsError || result.Text != "# Project\n" {
		t.Fatalf("tool result = %#v", result)
	}
	if result.Data == nil || result.Data["result"] == nil {
		t.Fatalf("structured provider result was lost: %#v", result.Data)
	}
	if got := p.buildOutput(); got != "I'll read the file. Done reading." {
		t.Fatalf("Output = %q", got)
	}
}

func TestCursorParserChunkBoundariesAreStable(t *testing.T) {
	fixture := loadFixture(t, "with-tool.jsonl")
	reference := newCursorParser(nil)
	_ = reference.onChunk("stdout", fixture, time.Now().UTC())
	reference.finalize()

	for _, granularity := range []int{1, 4, 32, 256} {
		p := newCursorParser(nil)
		for i := 0; i < len(fixture); i += granularity {
			end := min(i+granularity, len(fixture))
			if err := p.onChunk("stdout", append([]byte(nil), fixture[i:end]...), time.Now().UTC()); err != nil {
				t.Fatalf("chunk feed: %v", err)
			}
		}
		p.finalize()
		if p.buildOutput() != reference.buildOutput() {
			t.Fatalf("output diverged for granularity %d", granularity)
		}
		if !reflect.DeepEqual(p.transcript, reference.transcript) {
			t.Fatalf("transcript diverged for granularity %d", granularity)
		}
	}
}

func TestCursorParserLongOfficialAssistantLineSurvives(t *testing.T) {
	text := strings.Repeat("u", 2*1024*1024)
	payload := map[string]any{
		"type":       "assistant",
		"session_id": "cursor-long",
		"message": map[string]any{
			"role":    "assistant",
			"content": []any{map[string]any{"type": "text", "text": text}},
		},
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	p := newCursorParser(nil)
	_ = p.onChunk("stdout", append(raw, '\n'), time.Now().UTC())
	p.finalize()
	if got := p.buildOutput(); got != text {
		t.Fatalf("long assistant text len = %d, want %d", len(got), len(text))
	}
}

func TestCursorParserTerminalTextIsOutputFallbackNotSummary(t *testing.T) {
	p := snapshotCursorStdout(strings.Join([]string{
		`{"type":"system","subtype":"init","session_id":"cursor-terminal"}`,
		`{"type":"result","subtype":"success","is_error":false,"result":"terminal full reply","session_id":"cursor-terminal"}`,
		"",
	}, "\n"))
	if got := p.buildOutput(); got != "terminal full reply" {
		t.Fatalf("Output fallback = %q", got)
	}
	if got := p.finalSummary(); got != "" {
		t.Fatalf("Summary = %q, want empty", got)
	}
}

func TestCursorParserNonZeroExitDoesNotCheckpoint(t *testing.T) {
	p := parseCursorFixture(t, "failure.jsonl", nil)
	if got := p.buildOutput(); got != "Partial reply before process failure" {
		t.Fatalf("partial Output = %q", got)
	}
	if p.terminal != nil {
		t.Fatalf("failed official stream must not synthesize terminal JSON: %#v", p.terminal)
	}
	if checkpoint := p.checkpoint(1); checkpoint != nil {
		t.Fatalf("non-zero exit produced checkpoint %#v", checkpoint)
	}
}

func TestCursorParserUnknownEventIsPreservedWithoutGuessing(t *testing.T) {
	p := parseCursorFixture(t, "unknown-event.jsonl", nil)
	if got := p.buildOutput(); got != "Recovered." {
		t.Fatalf("Output = %q", got)
	}
	if cp := p.checkpoint(0); cp == nil || !cp.Valid {
		t.Fatalf("unknown additive event invalidated official success: %#v", cp)
	}
	var unknown *driver.TranscriptItem
	for i := range p.transcript {
		if p.transcript[i].Kind == driver.TranscriptSystem && strings.Contains(p.transcript[i].Text, `"type":"connection"`) {
			unknown = &p.transcript[i]
		}
	}
	if unknown == nil || unknown.Data == nil {
		t.Fatalf("unknown event not preserved: %#v", p.transcript)
	}
}

func TestCursorCheckpointRequiresSuccessfulTerminalSession(t *testing.T) {
	withoutTerminalSession := snapshotCursorStdout(strings.Join([]string{
		`{"type":"system","subtype":"init","session_id":"cursor-init"}`,
		`{"type":"result","subtype":"success","is_error":false,"result":"done"}`,
		"",
	}, "\n"))
	if cp := withoutTerminalSession.checkpoint(0); cp != nil {
		t.Fatalf("init session must not substitute for terminal session: %#v", cp)
	}

	unknownResult := snapshotCursorStdout(`{"type":"result","subtype":"error","is_error":true,"session_id":"cursor-error"}` + "\n")
	if unknownResult.terminal != nil {
		t.Fatalf("undocumented result subtype became terminal: %#v", unknownResult.terminal)
	}
	if cp := unknownResult.checkpoint(0); cp != nil {
		t.Fatalf("unknown result subtype produced checkpoint: %#v", cp)
	}
}

func TestCursorSuccessTerminalRequiresDocumentedFields(t *testing.T) {
	for _, tc := range []struct {
		name string
		line string
	}{
		{
			name: "missing is_error",
			line: `{"type":"result","subtype":"success","result":"done","session_id":"s"}`,
		},
		{
			name: "wrong is_error type",
			line: `{"type":"result","subtype":"success","is_error":"false","result":"done","session_id":"s"}`,
		},
		{
			name: "missing result",
			line: `{"type":"result","subtype":"success","is_error":false,"session_id":"s"}`,
		},
		{
			name: "wrong result type",
			line: `{"type":"result","subtype":"success","is_error":false,"result":{},"session_id":"s"}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := snapshotCursorStdout(tc.line + "\n")
			if !p.protocolMalformed || p.terminalSuccess {
				t.Fatalf("parser state = malformed:%v success:%v, want malformed unsuccessful terminal", p.protocolMalformed, p.terminalSuccess)
			}
			if checkpoint := p.checkpoint(0); checkpoint != nil {
				t.Fatalf("checkpoint = %#v, want nil", checkpoint)
			}
		})
	}
}

func TestCursorCheckpointOutcomeGates(t *testing.T) {
	p := snapshotCursorStdout("{\"type\":\"system\",\"subtype\":\"init\",\"session_id\":\"s\"}\n{\"type\":\"result\",\"subtype\":\"success\",\"is_error\":false,\"result\":\"ok\",\"session_id\":\"s\"}\n")
	if cp := p.checkpointForOutcome(0, "", false, nil); cp == nil || !cp.Valid {
		t.Fatalf("clean official success = %#v, want valid checkpoint", cp)
	}
	for _, tc := range []struct {
		name     string
		exitCode int
		signal   string
		timedOut bool
		failure  *driver.RunFailure
	}{
		{name: "nonzero", exitCode: 1},
		{name: "signal", signal: "SIGTERM"},
		{name: "timeout", timedOut: true},
		{name: "failure", failure: &driver.RunFailure{Code: driver.FailureAgentError}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if cp := p.checkpointForOutcome(tc.exitCode, tc.signal, tc.timedOut, tc.failure); cp != nil {
				t.Fatalf("unsafe outcome produced checkpoint %#v", cp)
			}
		})
	}
	if cp := snapshotCursorStdout("{broken\n").checkpoint(0); cp != nil {
		t.Fatalf("malformed protocol produced checkpoint %#v", cp)
	}
}

func TestCursorCheckpointRejectsPayloadAfterTerminal(t *testing.T) {
	p := snapshotCursorStdout(strings.Join([]string{
		`{"type":"result","subtype":"success","is_error":false,"result":"ok","session_id":"s"}`,
		`{"type":"connection","subtype":"reconnected"}`,
		"",
	}, "\n"))
	if cp := p.checkpoint(0); cp != nil {
		t.Fatalf("payload after terminal produced checkpoint: %#v", cp)
	}
}
