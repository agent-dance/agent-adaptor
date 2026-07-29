package codex

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
	"github.com/agent-dance/agent-adaptor/internal/engine"
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

func (r *recordingSink) Snapshot() []driver.TranscriptItem {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]driver.TranscriptItem, len(r.items))
	copy(out, r.items)
	return out
}

func loadFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture %q: %v", name, err)
	}
	return data
}

func TestCodexParserHappyAssistantProducesOutputAndTranscript(t *testing.T) {
	fixture := loadFixture(t, "happy-assistant.jsonl")

	sink := &recordingSink{}
	p := newCodexParser(sink)
	if err := p.onChunk("stdout", fixture, time.Now().UTC()); err != nil {
		t.Fatalf("feed fixture: %v", err)
	}
	p.finalize()

	if got, want := p.buildOutput(), "Hello, world."; got != want {
		t.Fatalf("assistant output: got %q want %q", got, want)
	}
	if p.usage == nil || p.usage.InputTokens != 5 || p.usage.OutputTokens != 3 {
		t.Fatalf("usage: got %#v", p.usage)
	}
	checkpoint := p.checkpoint(0)
	if checkpoint == nil || checkpoint.State == nil || checkpoint.State.ResumeID != "thread-happy-1" {
		t.Fatalf("checkpoint: got %#v", checkpoint)
	}
	if p.terminal == nil || p.terminal.Event != "turn.completed" || !json.Valid(p.terminal.JSON) {
		t.Fatalf("terminal = %#v", p.terminal)
	}

	sinkItems := sink.Snapshot()
	if !reflect.DeepEqual(sinkItems, p.transcript) {
		t.Fatalf("sink items diverged from final transcript: sink=%#v final=%#v", sinkItems, p.transcript)
	}

	kinds := make([]driver.TranscriptKind, 0, len(p.transcript))
	for _, item := range p.transcript {
		kinds = append(kinds, item.Kind)
	}
	want := []driver.TranscriptKind{
		driver.TranscriptInit,
		driver.TranscriptAssistant,
		driver.TranscriptResult,
	}
	if !reflect.DeepEqual(kinds, want) {
		t.Fatalf("transcript kinds: got %#v want %#v", kinds, want)
	}
}

func TestCodexParserMultiMessageOutputUsesLastAssistantBlock(t *testing.T) {
	fixture := loadFixture(t, "multi-message.jsonl")

	p := newCodexParser(nil)
	_ = p.onChunk("stdout", fixture, time.Now().UTC())
	p.finalize()

	want := "Second chunk that completes it."
	if got := p.buildOutput(); got != want {
		t.Fatalf("output: got %q want %q", got, want)
	}
}

func TestCodexParserPreservesIntermediateMessagesOnlyInTranscript(t *testing.T) {
	stdout := strings.Join([]string{
		`{"type":"thread.started","thread_id":"thread-final"}`,
		`{"type":"item.completed","item":{"id":"progress","type":"agent_message","text":"intermediate progress"}}`,
		`{"type":"item.completed","item":{"id":"final","type":"agent_message","text":"final answer"}}`,
		`{"type":"turn.completed"}`,
	}, "\n")
	p := snapshotCodexStdout(stdout)
	if got := p.buildOutput(); got != "final answer" {
		t.Fatalf("Output = %q, want final official agent_message", got)
	}
	var assistant []string
	for _, item := range p.transcript {
		if item.Kind == driver.TranscriptAssistant {
			assistant = append(assistant, item.Text)
		}
	}
	if !reflect.DeepEqual(assistant, []string{"intermediate progress", "final answer"}) {
		t.Fatalf("assistant transcript = %#v", assistant)
	}
}

func TestCodexParserDoesNotFallBackPastEmptyFinalAgentMessage(t *testing.T) {
	p := snapshotCodexStdout(strings.Join([]string{
		`{"type":"thread.started","thread_id":"thread-empty-final"}`,
		`{"type":"item.completed","item":{"id":"progress","type":"agent_message","text":"intermediate progress"}}`,
		`{"type":"item.completed","item":{"id":"final","type":"agent_message","text":""}}`,
		`{"type":"turn.completed"}`,
	}, "\n"))
	if got := p.buildOutput(); got != "" {
		t.Fatalf("Output = %q, must reflect the empty final agent_message", got)
	}
	var assistant []string
	for _, item := range p.transcript {
		if item.Kind == driver.TranscriptAssistant {
			assistant = append(assistant, item.Text)
		}
	}
	if !reflect.DeepEqual(assistant, []string{"intermediate progress", ""}) {
		t.Fatalf("assistant transcript = %#v", assistant)
	}
}

func TestCodexParserChunkBoundariesAreStable(t *testing.T) {
	fixture := loadFixture(t, "multi-message.jsonl")

	reference := newCodexParser(nil)
	_ = reference.onChunk("stdout", fixture, time.Now().UTC())
	reference.finalize()

	for _, granularity := range []int{1, 3, 17, 64, 512} {
		granularity := granularity
		t.Run(sprintfGranularity(granularity), func(t *testing.T) {
			p := newCodexParser(nil)
			for i := 0; i < len(fixture); i += granularity {
				end := i + granularity
				if end > len(fixture) {
					end = len(fixture)
				}
				if err := p.onChunk("stdout", append([]byte(nil), fixture[i:end]...), time.Now().UTC()); err != nil {
					t.Fatalf("chunk feed: %v", err)
				}
			}
			p.finalize()
			if p.buildOutput() != reference.buildOutput() {
				t.Fatalf("output diverged for granularity %d: got %q want %q", granularity, p.buildOutput(), reference.buildOutput())
			}
			if !reflect.DeepEqual(p.transcript, reference.transcript) {
				t.Fatalf("transcript diverged for granularity %d", granularity)
			}
		})
	}
}

func TestCodexParserHandlesLongLinesWithoutTruncation(t *testing.T) {
	bigText := strings.Repeat("x", 2*1024*1024)
	payload := map[string]any{
		"type": "item.completed",
		"item": map[string]any{"type": "agent_message", "text": bigText},
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	stdout := string(raw) + "\n"

	p := newCodexParser(nil)
	_ = p.onChunk("stdout", []byte(stdout), time.Now().UTC())
	p.finalize()

	if got := p.buildOutput(); got != bigText {
		t.Fatalf("expected long assistant text to survive parsing (len got %d want %d)", len(got), len(bigText))
	}
}

func TestCodexParserKeepsAssistantOutputOutOfSummaryAndTerminal(t *testing.T) {
	stdout := strings.Join([]string{
		`{"type":"thread.started","thread_id":"thread-mix"}`,
		`{"type":"item.completed","item":{"id":"msg-mix","type":"agent_message","text":"Long-form assistant reply that should stay in Output."}}`,
		`{"type":"turn.completed","usage":{"input_tokens":3,"output_tokens":9}}`,
		"",
	}, "\n")

	p := newCodexParser(nil)
	_ = p.onChunk("stdout", []byte(stdout), time.Now().UTC())
	p.finalize()

	if got, want := p.buildOutput(), "Long-form assistant reply that should stay in Output."; got != want {
		t.Fatalf("Output must stay as assistant text, got %q", got)
	}
	if got := p.finalSummary(); got != "" {
		t.Fatalf("Summary must remain empty when the official terminal has no summary field, got %q", got)
	}
	if p.terminal == nil || strings.Contains(string(p.terminal.JSON), "Long-form assistant") {
		t.Fatalf("terminal Result must not absorb assistant Output: %#v", p.terminal)
	}
}

func TestCodexParserDoesNotGuessCostFromUnsupportedTerminalFields(t *testing.T) {
	stdout := strings.Join([]string{
		`{"type":"thread.started","thread_id":"thread-cost"}`,
		`{"type":"turn.completed","cost_usd":1.25,"costUSD":2.5,"cost":3.75}`,
		"",
	}, "\n")

	p := newCodexParser(nil)
	_ = p.onChunk("stdout", []byte(stdout), time.Now().UTC())
	p.finalize()

	if len(p.transcript) == 0 || p.transcript[len(p.transcript)-1].Kind != driver.TranscriptResult {
		t.Fatalf("terminal transcript = %#v", p.transcript)
	}
	if got := p.transcript[len(p.transcript)-1].CostUSD; got != nil {
		t.Fatalf("unsupported cost aliases populated CostUSD: %v", *got)
	}
}

func TestCodexParserFailureFixtureSurfacesErrorMessage(t *testing.T) {
	fixture := loadFixture(t, "failure.jsonl")

	p := newCodexParser(nil)
	_ = p.onChunk("stdout", fixture, time.Now().UTC())
	p.finalize()

	if p.errorMessage != "model overloaded" {
		t.Fatalf("errorMessage: got %q", p.errorMessage)
	}
	if p.checkpoint(1) != nil {
		t.Fatalf("failed run must not produce a checkpoint")
	}
}

func TestCodexCheckpointRequiresOfficialSuccessAndCleanOutcome(t *testing.T) {
	success := snapshotCodexStdout("{\"type\":\"thread.started\",\"thread_id\":\"t-1\"}\n{\"type\":\"turn.completed\"}\n")
	if cp := success.checkpointForOutcome(0, "", false, nil); cp == nil || !cp.Valid {
		t.Fatalf("clean thread.started + turn.completed = %#v, want valid", cp)
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
			if cp := success.checkpointForOutcome(tc.exitCode, tc.signal, tc.timedOut, tc.failure); cp != nil {
				t.Fatalf("unsafe outcome produced checkpoint %#v", cp)
			}
		})
	}
	for name, stdout := range map[string]string{
		"init_only":           "{\"type\":\"thread.started\",\"thread_id\":\"t-1\"}\n",
		"terminal_only":       "{\"type\":\"turn.completed\"}\n",
		"unrecognized_result": "{\"type\":\"thread.started\",\"thread_id\":\"t-1\"}\n{\"type\":\"result\"}\n",
		"malformed":           "{\"type\":\"thread.started\",\"thread_id\":\"t-1\"}\n{broken\n{\"type\":\"turn.completed\"}\n",
	} {
		t.Run(name, func(t *testing.T) {
			if cp := snapshotCodexStdout(stdout).checkpoint(0); cp != nil {
				t.Fatalf("incomplete/malformed protocol produced checkpoint %#v", cp)
			}
		})
	}
}

func TestCodexCheckpointRejectsUnofficialTypeAndThreadIDAliases(t *testing.T) {
	for name, stdout := range map[string]string{
		"event discriminator": `{"event":"thread.started","thread_id":"alias"}
{"type":"turn.completed"}`,
		"kind discriminator": `{"kind":"thread.started","thread_id":"alias"}
{"type":"turn.completed"}`,
		"camel thread id": `{"type":"thread.started","threadId":"alias"}
{"type":"turn.completed"}`,
		"legacy session event": `{"type":"session.updated","session_id":"alias"}
{"type":"turn.completed"}`,
		"missing thread id": `{"type":"thread.started"}
{"type":"turn.completed"}`,
		"duplicate thread start": `{"type":"thread.started","thread_id":"first"}
{"type":"thread.started","thread_id":"second"}
{"type":"turn.completed"}`,
	} {
		t.Run(name, func(t *testing.T) {
			if checkpoint := snapshotCodexStdout(stdout).checkpoint(0); checkpoint != nil {
				t.Fatalf("unofficial alias produced checkpoint %#v", checkpoint)
			}
		})
	}
}

func TestCodexParserWithToolFixtureEmitsToolCallAndResult(t *testing.T) {
	fixture := loadFixture(t, "with-tool.jsonl")

	p := newCodexParser(nil)
	_ = p.onChunk("stdout", fixture, time.Now().UTC())
	p.finalize()

	kinds := make([]driver.TranscriptKind, 0, len(p.transcript))
	for _, item := range p.transcript {
		kinds = append(kinds, item.Kind)
	}
	want := []driver.TranscriptKind{
		driver.TranscriptInit,
		driver.TranscriptToolCall,
		driver.TranscriptToolResult,
		driver.TranscriptAssistant,
		driver.TranscriptResult,
	}
	if !reflect.DeepEqual(kinds, want) {
		t.Fatalf("transcript kinds: got %#v want %#v", kinds, want)
	}
	call := p.transcript[1]
	if call.ToolUseID != "item-1" || call.ToolName != "shell" || !reflect.DeepEqual(call.Input, map[string]any{"command": "ls"}) {
		t.Fatalf("unexpected command call: %#v", call)
	}
	result := p.transcript[2]
	if result.ToolUseID != "item-1" || result.Text != "file-a\nfile-b\n" || result.IsError || result.Data["status"] != "completed" || result.Data["exit_code"] != float64(0) {
		t.Fatalf("unexpected command result: %#v", result)
	}
}

func TestCodexParserOfficialToolItemPhasesAndResults(t *testing.T) {
	stdout := strings.Join([]string{
		`{"type":"thread.started","thread_id":"thread-tools"}`,
		`{"type":"item.started","item":{"id":"cmd","type":"command_execution","command":"false","status":"in_progress"}}`,
		`{"type":"item.completed","item":{"id":"cmd","type":"command_execution","command":"false","aggregated_output":"permission denied","exit_code":17,"status":"failed"}}`,
		`{"type":"item.started","item":{"id":"patch","type":"file_change","changes":[{"path":"main.go","kind":"update"}],"status":"in_progress"}}`,
		`{"type":"item.completed","item":{"id":"patch","type":"file_change","changes":[{"path":"main.go","kind":"update"}],"status":"completed"}}`,
		`{"type":"item.started","item":{"id":"mcp","type":"mcp_tool_call","server":"github","tool":"get_issue","arguments":{"number":7},"status":"in_progress"}}`,
		`{"type":"item.completed","item":{"id":"mcp","type":"mcp_tool_call","server":"github","tool":"get_issue","arguments":{"number":7},"result":{"title":"fixed"},"status":"completed"}}`,
		`{"type":"item.started","item":{"id":"search","type":"web_search","query":"Codex exec JSONL","action":{"type":"search"}}}`,
		`{"type":"item.completed","item":{"id":"search","type":"web_search","query":"Codex exec JSONL","action":{"type":"search"}}}`,
		`{"type":"turn.completed"}`,
		"",
	}, "\n")

	p := snapshotCodexStdout(stdout)
	var tools []driver.TranscriptItem
	for _, item := range p.transcript {
		if item.Kind == driver.TranscriptToolCall || item.Kind == driver.TranscriptToolResult {
			tools = append(tools, item)
		}
	}
	if len(tools) != 8 {
		t.Fatalf("each official start/completed pair must produce one call and one result, got %#v", tools)
	}
	for i := 0; i < len(tools); i += 2 {
		if tools[i].Kind != driver.TranscriptToolCall || tools[i+1].Kind != driver.TranscriptToolResult || tools[i].ToolUseID != tools[i+1].ToolUseID {
			t.Fatalf("invalid tool lifecycle at %d: call=%#v result=%#v", i/2, tools[i], tools[i+1])
		}
	}
	if !tools[1].IsError || tools[1].Text != "permission denied" || tools[1].Data["status"] != "failed" || tools[1].Data["exit_code"] != float64(17) {
		t.Fatalf("command failure/status was not preserved: %#v", tools[1])
	}
	if tools[2].ToolName != "apply_patch" || tools[3].IsError || tools[3].Data["status"] != "completed" {
		t.Fatalf("file_change lifecycle mismatch: call=%#v result=%#v", tools[2], tools[3])
	}
	if tools[4].ToolName != "github/get_issue" || !reflect.DeepEqual(tools[4].Input, map[string]any{"number": float64(7)}) || tools[5].Text != `{"title":"fixed"}` {
		t.Fatalf("mcp_tool_call lifecycle mismatch: call=%#v result=%#v", tools[4], tools[5])
	}
	if tools[6].ToolName != "web_search" || tools[7].Kind != driver.TranscriptToolResult {
		t.Fatalf("web_search lifecycle mismatch: call=%#v result=%#v", tools[6], tools[7])
	}
}

func TestCodexNativeStructuredOutputRequiresOfficialTerminalAndCleanOutcome(t *testing.T) {
	message := `{"type":"item.completed","item":{"id":"msg","type":"agent_message","text":"{\"ok\":true}"}}`
	official := "{\"type\":\"thread.started\",\"thread_id\":\"t\"}\n" + message + "\n{\"type\":\"turn.completed\"}\n"

	tests := []struct {
		name     string
		stdout   string
		exitCode int
		signal   string
		timedOut bool
		failure  *driver.RunFailure
	}{
		{name: "clean", stdout: official},
		{name: "missing terminal", stdout: message + "\n", exitCode: 0},
		{name: "synthetic top-level result", stdout: message + "\n{\"type\":\"result\",\"result\":{\"ok\":true}}\n"},
		{name: "failed terminal", stdout: message + "\n{\"type\":\"turn.failed\",\"error\":{\"message\":\"failed\"}}\n"},
		{name: "malformed protocol", stdout: "{broken\n" + official},
		{name: "event after terminal", stdout: official + message + "\n"},
		{name: "nonzero exit", stdout: official, exitCode: 2},
		{name: "signal", stdout: official, signal: "SIGTERM"},
		{name: "timeout", stdout: official, timedOut: true},
		{name: "business failure", stdout: official, failure: &driver.RunFailure{Code: driver.FailureAgentError}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := snapshotCodexStdout(tc.stdout).nativeStructuredOutputForOutcome(tc.exitCode, tc.signal, tc.timedOut, tc.failure)
			if tc.name == "clean" {
				if got == nil || string(got.RawJSON) != `{"ok":true}` {
					t.Fatalf("clean official outcome = %#v", got)
				}
				return
			}
			if got != nil {
				t.Fatalf("unsafe outcome exposed structured output %#v", got)
			}
		})
	}
}

func TestCodexInvalidNativeJSONIsRejectedByCoreSchemaValidator(t *testing.T) {
	parsed := snapshotCodexStdout("{\"type\":\"thread.started\",\"thread_id\":\"t-invalid\"}\n{\"type\":\"item.completed\",\"item\":{\"id\":\"msg\",\"type\":\"agent_message\",\"text\":\"not json\"}}\n{\"type\":\"turn.completed\"}\n")
	candidate := parsed.nativeStructuredOutputForOutcome(0, "", false, nil)
	if candidate == nil || string(candidate.RawJSON) != "not json" {
		t.Fatalf("parser must preserve invalid candidate for core diagnostics, got %#v", candidate)
	}
	schema := &driver.OutputSchema{
		Format:     driver.OutputFormatJSONSchema,
		SchemaJSON: json.RawMessage(`{"type":"object"}`),
		OnInvalid:  driver.StructuredOutputFailRun,
	}
	validated, failure := engine.FinalizeStructuredOutput(schema, driver.StructuredOutputSourceNative, parsed.buildOutput(), candidate, nil)
	if validated == nil || validated.Valid || len(validated.ValidationErrors) == 0 || failure == nil || failure.Code != driver.FailurePolicyError {
		t.Fatalf("invalid native JSON escaped core validation: value=%#v failure=%#v", validated, failure)
	}
	if checkpoint := parsed.checkpointForOutcome(0, "", false, failure); checkpoint != nil {
		t.Fatalf("structured-output policy failure produced a checkpoint: %#v", checkpoint)
	}
}

func sprintfGranularity(n int) string {
	return "granularity-" + strings.TrimSpace(itoa(n))
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}
