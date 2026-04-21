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

func loadFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture %q: %v", name, err)
	}
	return data
}

func TestCursorParserHappyAssistantProducesOutput(t *testing.T) {
	fixture := loadFixture(t, "happy-assistant.jsonl")

	sink := &recordingSink{}
	p := newCursorParser(sink)
	if err := p.onChunk("stdout", fixture, time.Now().UTC()); err != nil {
		t.Fatalf("feed fixture: %v", err)
	}
	p.finalize()

	if got, want := p.buildOutput(), "Cursor says hi."; got != want {
		t.Fatalf("assistant output: got %q want %q", got, want)
	}
	if p.summary != "Cursor says hi." {
		t.Fatalf("summary: got %q", p.summary)
	}
	if p.usage == nil || p.usage.InputTokens != 3 || p.usage.OutputTokens != 2 {
		t.Fatalf("usage: got %#v", p.usage)
	}
	checkpoint := p.checkpoint(0)
	if checkpoint == nil || checkpoint.State == nil || checkpoint.State.ResumeID != "cursor-happy" {
		t.Fatalf("checkpoint: got %#v", checkpoint)
	}
	if !reflect.DeepEqual(p.transcript, sink.items) {
		t.Fatalf("sink and transcript diverge")
	}
}

func TestCursorParserMultiMessageOutputJoinsAssistantBlocks(t *testing.T) {
	fixture := loadFixture(t, "multi-message.jsonl")

	p := newCursorParser(nil)
	_ = p.onChunk("stdout", fixture, time.Now().UTC())
	p.finalize()

	want := "First paragraph from Cursor.\n\nSecond paragraph closing it."
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

func TestCursorParserChunkBoundariesAreStable(t *testing.T) {
	fixture := loadFixture(t, "with-tool.jsonl")

	reference := newCursorParser(nil)
	_ = reference.onChunk("stdout", fixture, time.Now().UTC())
	reference.finalize()

	for _, granularity := range []int{1, 4, 32, 256} {
		p := newCursorParser(nil)
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

func TestCursorParserLongLineSurvives(t *testing.T) {
	text := strings.Repeat("u", 2*1024*1024)
	payload := map[string]any{
		"type":       "assistant",
		"session_id": "cursor-long",
		"text":       text,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	stdout := string(raw) + "\n"

	p := newCursorParser(nil)
	_ = p.onChunk("stdout", []byte(stdout), time.Now().UTC())
	p.finalize()

	if got := p.buildOutput(); got != text {
		t.Fatalf("expected long assistant text to survive parsing (len got %d want %d)", len(got), len(text))
	}
}

func TestCursorParserPureDeltaStreamFeedsOutputAndSummaryFallback(t *testing.T) {
	stdout := strings.Join([]string{
		`{"type":"session","session_id":"cursor-delta"}`,
		`{"type":"assistant.delta","delta":"Hello "}`,
		`{"type":"assistant.delta","delta":"from "}`,
		`{"type":"assistant.delta","delta":"deltas."}`,
		"",
	}, "\n")

	p := newCursorParser(nil)
	_ = p.onChunk("stdout", []byte(stdout), time.Now().UTC())
	p.finalize()

	if got, want := p.buildOutput(), "Hello from deltas."; got != want {
		t.Fatalf("delta output: got %q want %q", got, want)
	}
	if got := p.finalSummary(); got != "Hello from deltas." {
		t.Fatalf("delta summary fallback: got %q", got)
	}
}

func TestCursorParserTerminalSummaryOverridesAssistantText(t *testing.T) {
	stdout := strings.Join([]string{
		`{"type":"session","session_id":"cursor-mixed"}`,
		`{"type":"assistant","text":"Here are three bullets about foo..."}`,
		`{"type":"run.completed","session_id":"cursor-mixed","result":"summary: foo"}`,
		"",
	}, "\n")

	p := newCursorParser(nil)
	_ = p.onChunk("stdout", []byte(stdout), time.Now().UTC())
	p.finalize()

	if got, want := p.buildOutput(), "Here are three bullets about foo..."; got != want {
		t.Fatalf("Output must stay as assistant text, got %q", got)
	}
	if got := p.finalSummary(); got != "summary: foo" {
		t.Fatalf("Summary must come from terminal result, got %q", got)
	}
	if p.buildOutput() == p.finalSummary() {
		t.Fatalf("Output and Summary must be distinct in this fixture")
	}
}

func TestCursorParserFailureFixtureSurfacesErrorMessage(t *testing.T) {
	fixture := loadFixture(t, "failure.jsonl")

	p := newCursorParser(nil)
	_ = p.onChunk("stdout", fixture, time.Now().UTC())
	p.finalize()

	if p.errorMessage != "network unreachable" {
		t.Fatalf("errorMessage: got %q", p.errorMessage)
	}
	if p.checkpoint(1) != nil {
		t.Fatalf("failed run must not produce a checkpoint")
	}
}
