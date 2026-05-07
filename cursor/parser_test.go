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

func (r *recordingSink) EmitStream(agentadaptor.StreamPayload) error { return nil }

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
	// The CLI exited abnormally, but it still emitted a session event with a
	// real session_id before the failing terminal result. The session is
	// resumable server-side, so the checkpoint must be preserved.
	checkpoint := p.checkpoint(1)
	if checkpoint == nil || checkpoint.State == nil {
		t.Fatalf("abnormal exit with captured session_id must still produce a checkpoint, got %#v", checkpoint)
	}
	if checkpoint.State.ResumeID != "cursor-fail" {
		t.Fatalf("checkpoint ResumeID: got %q want %q", checkpoint.State.ResumeID, "cursor-fail")
	}
	if !checkpoint.Valid {
		t.Fatalf("checkpoint Valid must be true when session_id is captured")
	}
}

func TestCheckpoint_AbnormalExitWithSessionID(t *testing.T) {
	stdout := strings.Join([]string{
		`{"type":"session","session_id":"cursor-abnormal","model":"gpt-4"}`,
		`{"type":"assistant","session_id":"cursor-abnormal","message":{"content":[{"type":"text","text":"partial reply before crash"}]}}`,
		"",
	}, "\n")

	p := newCursorParser(nil)
	if err := p.onChunk("stdout", []byte(stdout), time.Now().UTC()); err != nil {
		t.Fatalf("feed stdout: %v", err)
	}
	p.finalize()

	// Simulate a non-zero exit (max_turns, upstream API error, etc.).
	checkpoint := p.checkpoint(1)
	if checkpoint == nil {
		t.Fatal("expected non-nil checkpoint when session_id was captured before abnormal exit")
	}
	if checkpoint.State == nil || checkpoint.State.ResumeID != "cursor-abnormal" {
		t.Fatalf("checkpoint ResumeID: got %#v", checkpoint.State)
	}
	if checkpoint.State.DisplayID != "cursor-abnormal" {
		t.Fatalf("checkpoint DisplayID should fall back to session id, got %q", checkpoint.State.DisplayID)
	}
	if !checkpoint.Valid {
		t.Fatalf("checkpoint Valid must be true; session is resumable server-side")
	}
}

func TestCheckpoint_AbnormalExitWithoutSessionID(t *testing.T) {
	// Defensive: if no session_id was ever observed, an abnormal exit
	// must still yield no checkpoint — there is nothing to resume.
	p := newCursorParser(nil)
	_ = p.onChunk("stdout", []byte("not json\n"), time.Now().UTC())
	p.finalize()

	if cp := p.checkpoint(1); cp != nil {
		t.Fatalf("expected nil checkpoint when session_id is absent, got %#v", cp)
	}
}

func TestParseCheckpoint_AbnormalExitWithSessionID(t *testing.T) {
	stdout := strings.Join([]string{
		`{"type":"session","session_id":"cursor-legacy"}`,
		`{"event":"run.completed","session_id":"cursor-legacy","display_id":"cursor-legacy-display"}`,
		"",
	}, "\n")

	checkpoint := parseCheckpoint(stdout, 1)
	if checkpoint == nil {
		t.Fatal("expected non-nil checkpoint for abnormal exit when session_id is present")
	}
	if checkpoint.State == nil || checkpoint.State.ResumeID != "cursor-legacy" {
		t.Fatalf("checkpoint ResumeID: got %#v", checkpoint.State)
	}
	if checkpoint.State.DisplayID != "cursor-legacy-display" {
		t.Fatalf("checkpoint DisplayID: got %q", checkpoint.State.DisplayID)
	}
	if !checkpoint.Valid {
		t.Fatalf("checkpoint Valid must be true")
	}
}
