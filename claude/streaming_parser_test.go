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
	counts := map[agentadaptor.StreamKind]int{}
	for _, pl := range sink.snapshot() {
		if pl.ToolCallID != "tool-stream-1" {
			continue
		}
		counts[pl.Kind]++
		if pl.Kind == agentadaptor.StreamToolCallArgs {
			argFragments.WriteString(pl.Delta)
		}
		if pl.Kind == agentadaptor.StreamToolCallResult {
			if pl.Result == nil || pl.Result["is_error"] != false {
				t.Fatalf("tool result: %+v", pl.Result)
			}
		}
	}
	if counts[agentadaptor.StreamToolCallStart] != 1 || counts[agentadaptor.StreamToolCallEnd] != 1 || counts[agentadaptor.StreamToolCallResult] != 1 {
		t.Fatalf("tool lifecycle must not duplicate full assistant replay: %+v", counts)
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

func TestStreamingCompleteAssistantToolUseEmitsLifecycle(t *testing.T) {
	fixture := loadFixture(t, "streaming-nonstream-tool-use.jsonl")
	sink := &streamSink{}
	p := newClaudeParser(sink)
	p.enableStreaming("run-complete-tools")

	if err := p.onChunk("stdout", fixture, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	p.finalize()

	want := map[string]struct {
		name string
		args map[string]any
	}{
		"tool-read-1": {
			name: "Read",
			args: map[string]any{"file_path": "/tmp/a.go"},
		},
		"tool-grep-1": {
			name: "Grep",
			args: map[string]any{"pattern": "TODO", "path": "/tmp"},
		},
	}
	counts := map[string]map[agentadaptor.StreamKind]int{}
	sequences := map[string][]agentadaptor.StreamKind{}
	for _, pl := range sink.snapshot() {
		expected, ok := want[pl.ToolCallID]
		if !ok {
			continue
		}
		if counts[pl.ToolCallID] == nil {
			counts[pl.ToolCallID] = map[agentadaptor.StreamKind]int{}
		}
		counts[pl.ToolCallID][pl.Kind]++
		switch pl.Kind {
		case agentadaptor.StreamToolCallStart, agentadaptor.StreamToolCallEnd, agentadaptor.StreamToolCallResult:
			sequences[pl.ToolCallID] = append(sequences[pl.ToolCallID], pl.Kind)
		}
		if pl.Kind == agentadaptor.StreamToolCallStart {
			if pl.Name != expected.name || !reflect.DeepEqual(pl.Args, expected.args) {
				t.Fatalf("tool start %s: got name=%q args=%#v want name=%q args=%#v", pl.ToolCallID, pl.Name, pl.Args, expected.name, expected.args)
			}
		}
	}

	for id := range want {
		got := counts[id]
		if got[agentadaptor.StreamToolCallStart] != 1 || got[agentadaptor.StreamToolCallEnd] != 1 || got[agentadaptor.StreamToolCallResult] != 1 {
			t.Errorf("tool lifecycle %s: %+v", id, got)
		}
		if got[agentadaptor.StreamToolCallArgs] != 0 {
			t.Errorf("complete tool use %s emitted synthetic args deltas: %+v", id, got)
		}
		wantSequence := []agentadaptor.StreamKind{
			agentadaptor.StreamToolCallStart,
			agentadaptor.StreamToolCallEnd,
			agentadaptor.StreamToolCallResult,
		}
		if !reflect.DeepEqual(sequences[id], wantSequence) {
			t.Errorf("tool lifecycle sequence %s: got %v want %v", id, sequences[id], wantSequence)
		}
	}
}

func TestStreamingMCPToolUseEmitsNamedArgumentDeltas(t *testing.T) {
	fixture := loadFixture(t, "streaming-mcp-tool-use.jsonl")
	sink := &streamSink{}
	p := newClaudeParser(sink)
	p.enableStreaming("run-mcp-tool")

	if err := p.onChunk("stdout", fixture, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	p.finalize()

	const (
		toolCallID = "tool-mcp-1"
		toolName   = "mcp__docs__search"
	)
	counts := map[agentadaptor.StreamKind]int{}
	var argFragments strings.Builder
	for _, pl := range sink.snapshot() {
		if pl.ToolCallID != toolCallID {
			continue
		}
		counts[pl.Kind]++
		switch pl.Kind {
		case agentadaptor.StreamToolCallStart:
			if pl.Name != toolName || len(pl.Args) != 0 {
				t.Fatalf("MCP start: name=%q args=%#v", pl.Name, pl.Args)
			}
		case agentadaptor.StreamToolCallArgs:
			if pl.Name != toolName {
				t.Fatalf("MCP args name: got %q want %q", pl.Name, toolName)
			}
			argFragments.WriteString(pl.Delta)
		}
	}

	if counts[agentadaptor.StreamToolCallStart] != 1 || counts[agentadaptor.StreamToolCallEnd] != 1 || counts[agentadaptor.StreamToolCallResult] != 1 {
		t.Fatalf("MCP tool lifecycle: %+v", counts)
	}
	want := map[string]any{"query": "streaming tool arguments", "limit": float64(5)}
	if got := decodeJSONFlexible(argFragments.String()); !reflect.DeepEqual(got, want) {
		t.Fatalf("MCP args: got %#v want %#v raw=%q", got, want, argFragments.String())
	}
}

func TestStreamingToolStartCarriesUnwrappedInputSnapshot(t *testing.T) {
	sink := &streamSink{}
	p := newClaudeParser(sink)
	p.enableStreaming("run-tool-snapshot")
	p.stream.handleContentBlockStart(map[string]any{
		"index": float64(0),
		"content_block": map[string]any{
			"type":  "tool_use",
			"id":    "tool-snapshot-1",
			"name":  "Agent",
			"input": map[string]any{"subagent_type": "Explore"},
		},
	})

	for _, pl := range sink.snapshot() {
		if pl.Kind != agentadaptor.StreamToolCallStart {
			continue
		}
		want := map[string]any{"subagent_type": "Explore"}
		if !reflect.DeepEqual(pl.Args, want) {
			t.Fatalf("tool start args: got %#v want %#v", pl.Args, want)
		}
		return
	}
	t.Fatal("missing tool-call start")
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
