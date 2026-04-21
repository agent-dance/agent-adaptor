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
	if p.checkpoint(1) != nil {
		t.Fatalf("failed run must not produce a checkpoint")
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
