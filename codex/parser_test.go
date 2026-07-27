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
	if p.summary != "Hello, world." {
		t.Fatalf("summary: got %q", p.summary)
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

func TestCodexParserMultiMessageOutputJoinsAssistantBlocks(t *testing.T) {
	fixture := loadFixture(t, "multi-message.jsonl")

	p := newCodexParser(nil)
	_ = p.onChunk("stdout", fixture, time.Now().UTC())
	p.finalize()

	want := "First chunk of the reply.\n\nSecond chunk that completes it."
	if got := p.buildOutput(); got != want {
		t.Fatalf("output: got %q want %q", got, want)
	}
	if p.summary != "Second chunk that completes it." {
		t.Fatalf("summary should reflect last agent message, got %q", p.summary)
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

func TestCodexParserTerminalSummaryOverridesAssistantText(t *testing.T) {
	stdout := strings.Join([]string{
		`{"type":"thread.started","thread_id":"thread-mix"}`,
		`{"type":"item.completed","item":{"type":"agent_message","text":"Long-form assistant reply that should stay in Output."}}`,
		`{"type":"turn.completed","session_id":"thread-mix","summary":"short terminal summary"}`,
		"",
	}, "\n")

	p := newCodexParser(nil)
	_ = p.onChunk("stdout", []byte(stdout), time.Now().UTC())
	p.finalize()

	if got, want := p.buildOutput(), "Long-form assistant reply that should stay in Output."; got != want {
		t.Fatalf("Output must stay as assistant text, got %q", got)
	}
	if got := p.finalSummary(); got != "short terminal summary" {
		t.Fatalf("Summary must come from terminal result, got %q", got)
	}
	if p.buildOutput() == p.finalSummary() {
		t.Fatalf("Output and Summary must be distinct in this fixture")
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
		"init_only":     "{\"type\":\"thread.started\",\"thread_id\":\"t-1\"}\n",
		"terminal_only": "{\"type\":\"turn.completed\"}\n",
		"legacy_result": "{\"type\":\"thread.started\",\"thread_id\":\"t-1\"}\n{\"type\":\"result\"}\n",
		"malformed":     "{\"type\":\"thread.started\",\"thread_id\":\"t-1\"}\n{broken\n{\"type\":\"turn.completed\"}\n",
	} {
		t.Run(name, func(t *testing.T) {
			if cp := snapshotCodexStdout(stdout).checkpoint(0); cp != nil {
				t.Fatalf("incomplete/malformed protocol produced checkpoint %#v", cp)
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
