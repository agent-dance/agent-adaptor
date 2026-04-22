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

type streamSink struct {
	mu       sync.Mutex
	payloads []agentadaptor.StreamPayload
}

func (s *streamSink) Emit(agentadaptor.RunEvent) error { return nil }

func (s *streamSink) EmitStream(p agentadaptor.StreamPayload) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.payloads = append(s.payloads, p)
	return nil
}

func (s *streamSink) snapshot() []agentadaptor.StreamPayload {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]agentadaptor.StreamPayload, len(s.payloads))
	copy(out, s.payloads)
	return out
}

func TestStreamingHappyFixture_TextDeltasMatchAssistant(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "streaming-happy.jsonl"))
	if err != nil {
		t.Fatal(err)
	}

	sink := &streamSink{}
	p := newClaudeParser(sink)
	p.enableStreaming("run-fixture")

	if err := p.onChunk("stdout", data, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	p.finalize()

	if got, want := p.buildOutput(), "Hi there"; got != want {
		t.Fatalf("buildOutput: got %q want %q", got, want)
	}

	stream := sink.snapshot()
	var assembled strings.Builder
	textContents := 0
	for _, pl := range stream {
		switch pl.Kind {
		case agentadaptor.StreamTextContent:
			textContents++
			assembled.WriteString(pl.Delta)
		case agentadaptor.StreamRunFinished:
			if pl.Usage == nil || pl.Usage.InputTokens != 10 || pl.Usage.OutputTokens != 5 {
				t.Fatalf("finished usage: %+v", pl.Usage)
			}
		}
	}
	if textContents < 3 {
		t.Fatalf("expected >=3 StreamTextContent, got %d kinds", textContents)
	}
	if assembled.String() != "Hi there" {
		t.Fatalf("assembled deltas: got %q", assembled.String())
	}

	counts := map[agentadaptor.StreamKind]int{}
	for _, pl := range stream {
		counts[pl.Kind]++
	}
	if counts[agentadaptor.StreamRunStarted] != 1 || counts[agentadaptor.StreamRunFinished] != 1 {
		t.Fatalf("lifecycle counts: %+v payloads=%d", counts, len(stream))
	}
	if counts[agentadaptor.StreamTextStart] != 1 || counts[agentadaptor.StreamTextEnd] != 1 {
		t.Fatalf("text lifecycle: %+v", counts)
	}
}

func TestStreamingBatchOutputEquivalence(t *testing.T) {
	fixture := loadFixture(t, "streaming-happy.jsonl")

	raw := newClaudeParser(nil)
	if err := raw.onChunk("stdout", fixture, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	raw.finalize()

	sink := &streamSink{}
	streamed := newClaudeParser(sink)
	streamed.enableStreaming("run-fixture")
	if err := streamed.onChunk("stdout", fixture, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	streamed.finalize()

	if raw.buildOutput() != streamed.buildOutput() {
		t.Fatalf("output mismatch batch=%q streamed=%q", raw.buildOutput(), streamed.buildOutput())
	}
	if raw.usage == nil || streamed.usage == nil {
		t.Fatal("usage missing")
	}
	if raw.usage.InputTokens != streamed.usage.InputTokens || raw.usage.OutputTokens != streamed.usage.OutputTokens {
		t.Fatalf("usage differs raw=%v streamed=%v", raw.usage, streamed.usage)
	}
}

func TestStreamingToolFixture_ArgsFragmentsConcatenateToJSON(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "streaming-tool-use.jsonl"))
	if err != nil {
		t.Fatal(err)
	}

	sink := &streamSink{}
	p := newClaudeParser(sink)
	p.enableStreaming("run-tool")

	if err := p.onChunk("stdout", data, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	p.finalize()

	var argFragments strings.Builder
	sawStart, sawEnd, sawResult := false, false, false
	for _, pl := range sink.snapshot() {
		if pl.Kind == agentadaptor.StreamToolCallArgs {
			argFragments.WriteString(pl.Delta)
		}
		if pl.Kind == agentadaptor.StreamToolCallStart && pl.ToolCallID == "tool-stream-1" {
			sawStart = true
		}
		if pl.Kind == agentadaptor.StreamToolCallEnd && pl.ToolCallID == "tool-stream-1" {
			sawEnd = true
		}
		if pl.Kind == agentadaptor.StreamToolCallResult && pl.ToolCallID == "tool-stream-1" {
			sawResult = true
			if pl.Result == nil || pl.Result["is_error"] != false {
				t.Fatalf("tool result: %+v", pl.Result)
			}
		}
	}
	if !sawStart || !sawEnd || !sawResult {
		t.Fatalf("tool lifecycle start=%v end=%v result=%v", sawStart, sawEnd, sawResult)
	}

	input := map[string]any{"path": "/tmp/x"}
	wantBytes, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	gotObj := decodeJSONFlexible(argFragments.String())
	wantObj := decodeJSONFlexible(string(wantBytes))
	if !reflect.DeepEqual(gotObj, wantObj) {
		t.Fatalf("args concat decode: got %#v want %#v (raw concat %q)", gotObj, wantObj, argFragments.String())
	}
}

func decodeJSONFlexible(s string) map[string]any {
	s = strings.TrimSpace(s)
	var out map[string]any
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return map[string]any{"_error": err.Error()}
	}
	return out
}

func TestStreamingThinkingFixture(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "streaming-thinking.jsonl"))
	if err != nil {
		t.Fatal(err)
	}

	sink := &streamSink{}
	p := newClaudeParser(sink)
	p.enableStreaming("run-think")

	if err := p.onChunk("stdout", data, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	p.finalize()

	counts := map[agentadaptor.StreamKind]int{}
	var reasoning strings.Builder
	for _, pl := range sink.snapshot() {
		counts[pl.Kind]++
		if pl.Kind == agentadaptor.StreamReasoningContent {
			reasoning.WriteString(pl.Delta)
		}
	}
	if counts[agentadaptor.StreamReasoningStart] < 1 || counts[agentadaptor.StreamReasoningEnd] < 1 {
		t.Fatalf("reasoning lifecycle: %+v", counts)
	}
	if reasoning.String() != "Let me think." {
		t.Fatalf("reasoning text: got %q", reasoning.String())
	}
	if got, want := p.buildOutput(), "Done."; got != want {
		t.Fatalf("output: got %q want %q", got, want)
	}
}
