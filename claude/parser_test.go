package claude

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	agentadaptor "github.com/agent-dance/agent-adaptor/driver"
)

type recordingSink struct {
	mu    sync.Mutex
	items []agentadaptor.TranscriptItem
}

func (r *recordingSink) Emit(event agentadaptor.RunEvent) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if event.Type == agentadaptor.RunEventItem && event.Item != nil {
		r.items = append(r.items, *event.Item)
	}
	return nil
}

func (r *recordingSink) EmitStream(agentadaptor.StreamPayload) error { return nil }

func loadFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture %q: %v", name, err)
	}
	return data
}

func TestClaudeParserHappyAssistantProducesOutputAndUsage(t *testing.T) {
	fixture := loadFixture(t, "happy-assistant.jsonl")

	sink := &recordingSink{}
	p := newClaudeParser(sink)
	if err := p.onChunk("stdout", fixture, time.Now().UTC()); err != nil {
		t.Fatalf("feed fixture: %v", err)
	}
	p.finalize()

	if got, want := p.buildOutput(), "Hello from Claude."; got != want {
		t.Fatalf("assistant output: got %q want %q", got, want)
	}
	if got := p.finalSummary(); got != "" {
		t.Fatalf("summary: got %q", got)
	}
	if p.usage == nil || p.usage.InputTokens != 7 || p.usage.OutputTokens != 4 || p.usage.CachedInputTokens != 1 {
		t.Fatalf("usage: got %#v", p.usage)
	}
	if p.cost == nil || *p.cost != 0.0012 {
		t.Fatalf("cost: got %#v", p.cost)
	}
	checkpoint := p.checkpoint(0)
	if checkpoint == nil || checkpoint.State == nil || checkpoint.State.ResumeID != "claude-happy" {
		t.Fatalf("checkpoint: got %#v", checkpoint)
	}
	if p.terminal == nil || p.terminal.Event != "result" || !json.Valid(p.terminal.JSON) {
		t.Fatal("expected terminal result payload")
	}

	if !reflect.DeepEqual(p.transcript, sink.items) {
		t.Fatalf("sink and final transcript diverge")
	}
}

func TestClaudeParserTerminalResultOverridesIntermediateAssistantBlocks(t *testing.T) {
	fixture := loadFixture(t, "multi-message.jsonl")

	p := newClaudeParser(nil)
	_ = p.onChunk("stdout", fixture, time.Now().UTC())
	p.finalize()

	want := "wrapped multi-message reply"
	if got := p.buildOutput(); got != want {
		t.Fatalf("output: got %q want %q", got, want)
	}
	if got := p.finalSummary(); got != "" {
		t.Fatalf("Claude has no official bounded run summary, got %q", got)
	}
}

func TestClaudeParserChunkBoundariesAreStable(t *testing.T) {
	fixture := loadFixture(t, "with-tool.jsonl")

	reference := newClaudeParser(nil)
	_ = reference.onChunk("stdout", fixture, time.Now().UTC())
	reference.finalize()

	for _, granularity := range []int{1, 5, 32, 256} {
		p := newClaudeParser(nil)
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
			t.Fatalf("output diverged for granularity %d", granularity)
		}
		if !reflect.DeepEqual(p.transcript, reference.transcript) {
			t.Fatalf("transcript diverged for granularity %d", granularity)
		}
	}
}

func TestClaudeParserLongLineSurvives(t *testing.T) {
	text := strings.Repeat("c", 2*1024*1024)
	payload := map[string]any{
		"type":       "assistant",
		"session_id": "claude-long",
		"message":    map[string]any{"content": []any{map[string]any{"type": "text", "text": text}}},
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	terminal, err := json.Marshal(map[string]any{
		"type":       "result",
		"subtype":    "success",
		"is_error":   false,
		"session_id": "claude-long",
		"result":     text,
	})
	if err != nil {
		t.Fatalf("marshal terminal: %v", err)
	}
	stdout := string(raw) + "\n" + string(terminal) + "\n"

	p := newClaudeParser(nil)
	_ = p.onChunk("stdout", []byte(stdout), time.Now().UTC())
	p.finalize()

	if got := p.buildOutput(); got != text {
		t.Fatalf("expected long assistant text to survive parsing (len got %d want %d)", len(got), len(text))
	}
}

func TestClaudeParserFinalResultIsNotSummary(t *testing.T) {
	fullText := strings.Repeat("long final assistant response ", 400) + "done"
	assistant, err := json.Marshal(map[string]any{
		"type":       "assistant",
		"session_id": "claude-mix",
		"message": map[string]any{"content": []any{map[string]any{
			"type": "text", "text": "intermediate tool-loop narration must not become Result.Text",
		}}},
	})
	if err != nil {
		t.Fatalf("marshal assistant: %v", err)
	}
	terminal, err := json.Marshal(map[string]any{
		"type":              "result",
		"subtype":           "success",
		"is_error":          false,
		"session_id":        "claude-mix",
		"result":            fullText,
		"summary":           "undocumented summary must not be promoted",
		"structured_output": map[string]any{"answer": 42},
	})
	if err != nil {
		t.Fatalf("marshal terminal: %v", err)
	}
	stdout := strings.Join([]string{
		`{"type":"system","subtype":"init","session_id":"claude-mix"}`,
		string(assistant),
		string(terminal),
		"",
	}, "\n")

	p := newClaudeParser(nil)
	_ = p.onChunk("stdout", []byte(stdout), time.Now().UTC())
	p.finalize()

	if got := p.buildOutput(); got != fullText {
		t.Fatalf("Output length = %d, want %d", len(got), len(fullText))
	}
	if got := p.finalSummary(); got != "" {
		t.Fatalf("Summary must remain empty without an official bounded field, got %q", got)
	}
	if p.terminal == nil || string(p.terminal.JSON) != string(terminal) {
		t.Fatalf("terminal Raw mismatch: got %#v", p.terminal)
	}
	if p.structuredOutput == nil || string(p.structuredOutput.RawJSON) != `{"answer":42}` {
		t.Fatalf("structured output = %#v", p.structuredOutput)
	}
}

func TestClaudeParserTerminalResultPreservesWhitespace(t *testing.T) {
	p := snapshotClaudeStdout("{\"type\":\"result\",\"subtype\":\"success\",\"is_error\":false,\"session_id\":\"s\",\"result\":\"  final text\\n\"}\n")
	if got, want := p.buildOutput(), "  final text\n"; got != want {
		t.Fatalf("Output = %q, want exact terminal result %q", got, want)
	}
}

func TestClaudeParserOnlyFormalSnakeCaseFieldsHaveSemantics(t *testing.T) {
	p := snapshotClaudeStdout(strings.Join([]string{
		`{"event":"result","subtype":"success","sessionId":"alias-session","result":"alias output","structuredOutput":{"answer":1}}`,
		`{"kind":"result","subtype":"success","sessionID":"alias-session-2","result":"alias output 2"}`,
		"",
	}, "\n"))

	if p.terminalSeen {
		t.Fatal("event/kind aliases must not become formal terminal events")
	}
	if got := p.buildOutput(); got != "" {
		t.Fatalf("alias payload produced Output %q", got)
	}
	if p.structuredOutput != nil {
		t.Fatalf("structuredOutput alias produced structured output %#v", p.structuredOutput)
	}
	if cp := p.checkpoint(0); cp != nil {
		t.Fatalf("alias fields produced checkpoint %#v", cp)
	}
	for _, item := range p.transcript {
		if item.Kind == agentadaptor.TranscriptResult {
			t.Fatalf("alias payload was promoted to TranscriptResult: %#v", item)
		}
	}

	formalWithAliases := snapshotClaudeStdout(`{"type":"result","subtype":"success","is_error":false,"sessionId":"alias-session","result":"done","structuredOutput":{"answer":1}}` + "\n")
	if !formalWithAliases.terminalSeen || formalWithAliases.buildOutput() != "done" {
		t.Fatalf("formal result was not parsed: terminal=%v output=%q", formalWithAliases.terminalSeen, formalWithAliases.buildOutput())
	}
	if formalWithAliases.structuredOutput != nil {
		t.Fatalf("structuredOutput alias produced structured output %#v", formalWithAliases.structuredOutput)
	}
	if cp := formalWithAliases.checkpoint(0); cp != nil {
		t.Fatalf("sessionId alias produced checkpoint %#v", cp)
	}
}

func TestClaudeCheckpointRequiresTerminalSessionID(t *testing.T) {
	p := snapshotClaudeStdout(strings.Join([]string{
		`{"type":"system","subtype":"init","session_id":"init-only"}`,
		`{"type":"result","subtype":"success","is_error":false,"result":"done"}`,
		"",
	}, "\n"))
	if failure := p.failureForOutcome(0, "", false); failure == nil || failure.Code != agentadaptor.FailureAgentError {
		t.Fatalf("success without terminal session_id = %#v, want agent_error", failure)
	}
	if cp := p.checkpoint(0); cp != nil {
		t.Fatalf("init-only session ID produced checkpoint %#v", cp)
	}
}

func TestClaudeParserRejectsSessionSwitch(t *testing.T) {
	p := snapshotClaudeStdout(strings.Join([]string{
		`{"type":"system","subtype":"init","session_id":"parent"}`,
		`{"type":"result","subtype":"success","is_error":false,"session_id":"other","result":"done"}`,
		"",
	}, "\n"))
	if !p.protocolMalformed {
		t.Fatal("session ID switch must mark protocol malformed")
	}
	if failure := p.failureForOutcome(0, "", false); failure == nil {
		t.Fatal("session ID switch on clean exit must fail")
	}
	if cp := p.checkpoint(0); cp != nil {
		t.Fatalf("session ID switch produced checkpoint %#v", cp)
	}
}

func TestClaudeParserCleanExitProtocolFailures(t *testing.T) {
	tests := []struct {
		name   string
		stdout string
	}{
		{name: "empty stdout"},
		{name: "plain stdout", stdout: "unexpected text\n"},
		{name: "malformed JSON before success", stdout: "{broken\n{\"type\":\"result\",\"subtype\":\"success\",\"is_error\":false,\"session_id\":\"s\",\"result\":\"done\"}\n"},
		{name: "missing terminal", stdout: "{\"type\":\"system\",\"subtype\":\"init\",\"session_id\":\"s\"}\n"},
		{name: "non-success terminal", stdout: "{\"type\":\"result\",\"subtype\":\"error\",\"is_error\":true,\"session_id\":\"s\",\"result\":\"failed\"}\n"},
		{name: "success missing is_error", stdout: "{\"type\":\"result\",\"subtype\":\"success\",\"session_id\":\"s\",\"result\":\"done\"}\n"},
		{name: "success wrong is_error type", stdout: "{\"type\":\"result\",\"subtype\":\"success\",\"is_error\":\"false\",\"session_id\":\"s\",\"result\":\"done\"}\n"},
		{name: "payload after terminal", stdout: "{\"type\":\"result\",\"subtype\":\"success\",\"is_error\":false,\"session_id\":\"s\",\"result\":\"done\"}\n{\"type\":\"system\",\"subtype\":\"status\"}\n"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := snapshotClaudeStdout(tc.stdout)
			if failure := p.failureForOutcome(0, "", false); failure == nil || failure.Code != agentadaptor.FailureAgentError {
				t.Fatalf("clean protocol failure = %#v, want agent_error", failure)
			}
			if cp := p.checkpointForOutcome(0, "", false, p.failureForOutcome(0, "", false)); cp != nil {
				t.Fatalf("failed protocol produced checkpoint %#v", cp)
			}
		})
	}
}

func TestClaudeParserAbnormalProcessRemainsCoreClassified(t *testing.T) {
	p := snapshotClaudeStdout("partial stdout\n")
	if failure := p.failureForOutcome(7, "", false); failure != nil {
		t.Fatalf("non-zero exit without provider failure = %#v, want core classification", failure)
	}
}

func TestClaudeParserFailureFixtureSurfacesErrorMessage(t *testing.T) {
	fixture := loadFixture(t, "failure.jsonl")

	p := newClaudeParser(nil)
	_ = p.onChunk("stdout", fixture, time.Now().UTC())
	p.finalize()

	if p.errorMessage != "upstream error" {
		t.Fatalf("errorMessage: got %q", p.errorMessage)
	}
	if checkpoint := p.checkpoint(1); checkpoint != nil {
		t.Fatalf("failed terminal/non-zero exit must not produce a checkpoint, got %#v", checkpoint)
	}
}

func TestCheckpoint_AbnormalExitWithSessionID(t *testing.T) {
	stdout := strings.Join([]string{
		`{"type":"system","subtype":"init","session_id":"claude-abnormal","model":"claude-3-5-sonnet"}`,
		`{"type":"assistant","session_id":"claude-abnormal","message":{"content":[{"type":"text","text":"partial reply before crash"}]}}`,
		"",
	}, "\n")

	p := newClaudeParser(nil)
	if err := p.onChunk("stdout", []byte(stdout), time.Now().UTC()); err != nil {
		t.Fatalf("feed stdout: %v", err)
	}
	p.finalize()

	if checkpoint := p.checkpoint(1); checkpoint != nil {
		t.Fatalf("init plus partial output without a successful terminal must be invalid, got %#v", checkpoint)
	}
}

func TestCheckpoint_AbnormalExitWithoutSessionID(t *testing.T) {
	// Defensive: if no session_id was ever observed, an abnormal exit
	// must still yield no checkpoint — there is nothing to resume.
	p := newClaudeParser(nil)
	_ = p.onChunk("stdout", []byte("not json\n"), time.Now().UTC())
	p.finalize()

	if cp := p.checkpoint(1); cp != nil {
		t.Fatalf("expected nil checkpoint when session_id is absent, got %#v", cp)
	}
}

func TestParseCheckpoint_AbnormalExitWithSessionID(t *testing.T) {
	stdout := strings.Join([]string{
		`{"type":"system","subtype":"init","session_id":"claude-observed"}`,
		`{"type":"result","subtype":"success","is_error":false,"session_id":"claude-observed","display_id":"claude-observed-display","result":"done"}`,
		"",
	}, "\n")

	if checkpoint := parseCheckpoint(stdout, 1); checkpoint != nil {
		t.Fatalf("non-zero exit must invalidate a snapshot checkpoint, got %#v", checkpoint)
	}
}

func TestClaudeCheckpointOutcomeGates(t *testing.T) {
	p := snapshotClaudeStdout("{\"type\":\"system\",\"subtype\":\"init\",\"session_id\":\"s\"}\n{\"type\":\"result\",\"subtype\":\"success\",\"is_error\":false,\"session_id\":\"s\",\"result\":\"\"}\n")
	if cp := p.checkpointForOutcome(0, "", false, nil); cp == nil || !cp.Valid {
		t.Fatalf("clean official success = %#v, want valid checkpoint", cp)
	}
	for _, tc := range []struct {
		name     string
		exitCode int
		signal   string
		timedOut bool
		failure  *agentadaptor.RunFailure
	}{
		{name: "nonzero", exitCode: 1},
		{name: "signal", signal: "SIGTERM"},
		{name: "timeout", timedOut: true},
		{name: "failure", failure: &agentadaptor.RunFailure{Code: agentadaptor.FailureAgentError}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if cp := p.checkpointForOutcome(tc.exitCode, tc.signal, tc.timedOut, tc.failure); cp != nil {
				t.Fatalf("unsafe outcome produced checkpoint %#v", cp)
			}
		})
	}
	if cp := snapshotClaudeStdout("{broken\n").checkpoint(0); cp != nil {
		t.Fatalf("malformed protocol produced checkpoint %#v", cp)
	}
}

func TestClaudeParserWithToolExposesToolCallAndResult(t *testing.T) {
	fixture := loadFixture(t, "with-tool.jsonl")

	p := newClaudeParser(nil)
	_ = p.onChunk("stdout", fixture, time.Now().UTC())
	p.finalize()

	kinds := make([]agentadaptor.TranscriptKind, 0, len(p.transcript))
	for _, item := range p.transcript {
		kinds = append(kinds, item.Kind)
	}
	want := []agentadaptor.TranscriptKind{
		agentadaptor.TranscriptInit,
		agentadaptor.TranscriptToolCall,
		agentadaptor.TranscriptToolResult,
		agentadaptor.TranscriptAssistant,
		agentadaptor.TranscriptResult,
	}
	if !reflect.DeepEqual(kinds, want) {
		t.Fatalf("transcript kinds: got %#v want %#v", kinds, want)
	}
}
