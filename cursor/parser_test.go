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
	if p.terminal == nil || p.terminal.Event == "" || !json.Valid(p.terminal.JSON) {
		t.Fatalf("terminal = %#v", p.terminal)
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

func TestCursorParserPureDeltaStreamDoesNotReuseOutputAsSummary(t *testing.T) {
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
	if got := p.finalSummary(); got != "" {
		t.Fatalf("Summary must remain empty without a provider terminal summary, got %q", got)
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
	if checkpoint := p.checkpoint(1); checkpoint != nil {
		t.Fatalf("failed terminal/non-zero exit must not produce a checkpoint, got %#v", checkpoint)
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

	if checkpoint := p.checkpoint(1); checkpoint != nil {
		t.Fatalf("session plus partial output without a successful terminal must be invalid, got %#v", checkpoint)
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

	if checkpoint := parseCheckpoint(stdout, 1); checkpoint != nil {
		t.Fatalf("non-zero exit must invalidate a snapshot checkpoint, got %#v", checkpoint)
	}
}

func TestCursorCheckpointOutcomeGates(t *testing.T) {
	p := snapshotCursorStdout("{\"type\":\"session\",\"session_id\":\"s\"}\n{\"type\":\"run.completed\",\"session_id\":\"s\"}\n")
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
	if cp := snapshotCursorStdout("{broken\n").checkpoint(0); cp != nil {
		t.Fatalf("malformed protocol produced checkpoint %#v", cp)
	}
}
