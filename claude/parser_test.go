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

	agentadaptor "github.com/agent-dance/agent-adaptor"
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
	if p.summary != "Hello from Claude." {
		t.Fatalf("summary: got %q", p.summary)
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
	if p.resultFinal == nil {
		t.Fatal("expected terminal result payload")
	}

	if !reflect.DeepEqual(p.transcript, sink.items) {
		t.Fatalf("sink and final transcript diverge")
	}
}

func TestClaudeParserMultiMessageOutputJoinsAssistantBlocks(t *testing.T) {
	fixture := loadFixture(t, "multi-message.jsonl")

	p := newClaudeParser(nil)
	_ = p.onChunk("stdout", fixture, time.Now().UTC())
	p.finalize()

	want := "First paragraph from Claude.\n\nSecond paragraph that wraps it up."
	if got := p.buildOutput(); got != want {
		t.Fatalf("output: got %q want %q", got, want)
	}
	if got := p.finalSummary(); got != "wrapped multi-message reply" {
		t.Fatalf("summary should come from terminal result, got %q", got)
	}
	if p.buildOutput() == p.finalSummary() {
		t.Fatalf("Output and Summary must be distinct in this fixture")
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
	stdout := string(raw) + "\n"

	p := newClaudeParser(nil)
	_ = p.onChunk("stdout", []byte(stdout), time.Now().UTC())
	p.finalize()

	if got := p.buildOutput(); got != text {
		t.Fatalf("expected long assistant text to survive parsing (len got %d want %d)", len(got), len(text))
	}
}

func TestClaudeParserTerminalSummaryOverridesAssistantText(t *testing.T) {
	stdout := strings.Join([]string{
		`{"type":"system","subtype":"init","session_id":"claude-mix"}`,
		`{"type":"assistant","session_id":"claude-mix","message":{"content":[{"type":"text","text":"Multi-paragraph assistant reply that should stay in Output."}]}}`,
		`{"type":"result","subtype":"success","session_id":"claude-mix","result":"short terminal summary"}`,
		"",
	}, "\n")

	p := newClaudeParser(nil)
	_ = p.onChunk("stdout", []byte(stdout), time.Now().UTC())
	p.finalize()

	if got, want := p.buildOutput(), "Multi-paragraph assistant reply that should stay in Output."; got != want {
		t.Fatalf("Output must stay as assistant text, got %q", got)
	}
	if got := p.finalSummary(); got != "short terminal summary" {
		t.Fatalf("Summary must come from terminal result, got %q", got)
	}
	if p.buildOutput() == p.finalSummary() {
		t.Fatalf("Output and Summary must be distinct in this fixture")
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
		`{"type":"system","subtype":"init","session_id":"claude-legacy"}`,
		`{"event":"turn.completed","session_id":"claude-legacy","display_id":"claude-legacy-display"}`,
		"",
	}, "\n")

	if checkpoint := parseCheckpoint(stdout, 1); checkpoint != nil {
		t.Fatalf("non-zero exit must invalidate a snapshot checkpoint, got %#v", checkpoint)
	}
}

func TestClaudeCheckpointOutcomeGates(t *testing.T) {
	p := snapshotClaudeStdout("{\"type\":\"system\",\"subtype\":\"init\",\"session_id\":\"s\"}\n{\"type\":\"result\",\"subtype\":\"success\",\"session_id\":\"s\"}\n")
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
