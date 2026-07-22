package cursor

import (
	"strings"
	"sync"
	"testing"
	"time"

	agentadaptor "github.com/agent-dance/agent-adaptor"
)

// streamRecordingSink records both TranscriptItems (via Emit) and StreamPayloads
// (via EmitStream) for assertion in subagent streaming tests.
type streamRecordingSink struct {
	mu      sync.Mutex
	items   []agentadaptor.TranscriptItem
	streams []agentadaptor.StreamPayload
}

func (s *streamRecordingSink) Emit(event agentadaptor.RunEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if event.Type == agentadaptor.RunEventItem && event.Item != nil {
		s.items = append(s.items, *event.Item)
	}
	return nil
}

func (s *streamRecordingSink) EmitStream(p agentadaptor.StreamPayload) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.streams = append(s.streams, p)
	return nil
}

func (s *streamRecordingSink) streamsByKind(kind agentadaptor.StreamKind) []agentadaptor.StreamPayload {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []agentadaptor.StreamPayload
	for _, p := range s.streams {
		if p.Kind == kind {
			out = append(out, p)
		}
	}
	return out
}

// TestTaskToolCallFixtureHappyPath verifies that a full task-tool-call.jsonl
// fixture produces the expected transcript items and stream payloads.
func TestTaskToolCallFixtureHappyPath(t *testing.T) {
	fixture := loadFixture(t, "task-tool-call.jsonl")

	sink := &streamRecordingSink{}
	p := newCursorParserWithRunID(sink, "run-001")
	if err := p.onChunk("stdout", fixture, time.Now().UTC()); err != nil {
		t.Fatalf("feed fixture: %v", err)
	}
	p.finalize()

	// Transcript must contain: tool_call (Task), tool_result, assistant, result.
	toolCalls := filterTranscript(sink.items, agentadaptor.TranscriptToolCall)
	if len(toolCalls) != 1 {
		t.Fatalf("expected 1 tool_call transcript item, got %d", len(toolCalls))
	}
	if toolCalls[0].ToolName != "Task" {
		t.Errorf("tool_call ToolName: got %q want %q", toolCalls[0].ToolName, "Task")
	}
	if toolCalls[0].ToolUseID != "call-abc123" {
		t.Errorf("tool_call ToolUseID: got %q want %q", toolCalls[0].ToolUseID, "call-abc123")
	}

	toolResults := filterTranscript(sink.items, agentadaptor.TranscriptToolResult)
	if len(toolResults) != 1 {
		t.Fatalf("expected 1 tool_result transcript item, got %d", len(toolResults))
	}
	if !strings.Contains(toolResults[0].Text, "README") {
		t.Errorf("tool_result text does not mention README: %q", toolResults[0].Text)
	}

	// Streaming: expect the parent tool lifecycle around the subagent scope.
	starts := sink.streamsByKind(agentadaptor.StreamToolCallStart)
	if len(starts) != 1 {
		t.Fatalf("expected 1 StreamToolCallStart, got %d", len(starts))
	}
	if starts[0].Name != "Task" {
		t.Errorf("StreamToolCallStart Name: got %q want %q", starts[0].Name, "Task")
	}
	if starts[0].ToolCallID != "call-abc123" {
		t.Errorf("StreamToolCallStart ToolCallID: got %q want %q", starts[0].ToolCallID, "call-abc123")
	}
	if starts[0].Subagent != nil {
		t.Errorf("parent StreamToolCallStart Subagent must be nil, got %#v", starts[0].Subagent)
	}

	subagentStarts := sink.streamsByKind(agentadaptor.StreamSubagentStart)
	if len(subagentStarts) != 1 {
		t.Fatalf("expected 1 subagent.start, got %d", len(subagentStarts))
	}
	ssStart := subagentStarts[0]
	if ssStart.Subagent == nil {
		t.Fatal("subagent.start Subagent must be non-nil")
	}
	if ssStart.Subagent.ID != "req-7a8b9c0d" {
		t.Errorf("subagent.start Subagent.ID: got %q want req-7a8b9c0d", ssStart.Subagent.ID)
	}
	if ssStart.Subagent.Name != "Probe README summary" {
		t.Errorf("subagent.start Subagent.Name: got %q want Probe README summary", ssStart.Subagent.Name)
	}
	if ssStart.Subagent.Kind != "native" {
		t.Errorf("subagent.start Subagent.Kind: got %q want native", ssStart.Subagent.Kind)
	}
	if ssStart.Subagent.ToolCallID != "call-abc123" {
		t.Errorf("subagent.start Subagent.ToolCallID: got %q want call-abc123", ssStart.Subagent.ToolCallID)
	}
	if ssStart.Raw != nil {
		t.Errorf("subagent.start Raw must be nil, got %#v", ssStart.Raw)
	}

	subagentEnds := sink.streamsByKind(agentadaptor.StreamSubagentEnd)
	if len(subagentEnds) != 1 {
		t.Fatalf("expected 1 subagent.end, got %d", len(subagentEnds))
	}
	ssEnd := subagentEnds[0]
	if ssEnd.Subagent == nil {
		t.Fatal("subagent.end Subagent must be non-nil")
	}
	// scope ID must match the started ID (args.agentId) for stability.
	if ssEnd.Subagent.ID != "req-7a8b9c0d" {
		t.Errorf("subagent.end Subagent.ID: got %q want req-7a8b9c0d", ssEnd.Subagent.ID)
	}
	if ssEnd.Subagent.ToolCallID != "call-abc123" {
		t.Errorf("subagent.end Subagent.ToolCallID: got %q want call-abc123", ssEnd.Subagent.ToolCallID)
	}
	if endText, _ := ssEnd.Result["text"].(string); !strings.Contains(endText, "README") {
		t.Errorf("subagent.end Result text does not mention README: %q", endText)
	}

	tcResults := sink.streamsByKind(agentadaptor.StreamToolCallResult)
	if len(tcResults) != 1 {
		t.Fatalf("expected 1 StreamToolCallResult, got %d", len(tcResults))
	}
	tcEnds := sink.streamsByKind(agentadaptor.StreamToolCallEnd)
	if len(tcEnds) != 1 {
		t.Fatalf("expected 1 StreamToolCallEnd, got %d", len(tcEnds))
	}
	if tcEnds[0].Subagent != nil || tcResults[0].Subagent != nil {
		t.Error("parent tool end/result must remain in the root scope")
	}

	// RunID must be propagated.
	if ssStart.RunID != "run-001" {
		t.Errorf("subagent.start RunID: got %q want run-001", ssStart.RunID)
	}

	// No pending subagents must remain after completion.
	if len(p.pendingSubagents) != 0 {
		t.Errorf("expected empty pendingSubagents, got %d", len(p.pendingSubagents))
	}

	// No child text must be added to parent Output.
	output := p.buildOutput()
	if !strings.Contains(output, "subagent has summarized") {
		t.Errorf("parent output must contain the parent assistant text, got %q", output)
	}
}

// TestTaskToolCallAgentIDDiffFixture verifies the scope-ID stability mapping
// when args.agentId differs from result.success.agentId.
func TestTaskToolCallAgentIDDiffFixture(t *testing.T) {
	fixture := loadFixture(t, "task-tool-call-agentid-diff.jsonl")

	sink := &streamRecordingSink{}
	p := newCursorParserWithRunID(sink, "run-diff")
	if err := p.onChunk("stdout", fixture, time.Now().UTC()); err != nil {
		t.Fatalf("feed fixture: %v", err)
	}
	p.finalize()

	subagentStarts := sink.streamsByKind(agentadaptor.StreamSubagentStart)
	subagentEnds := sink.streamsByKind(agentadaptor.StreamSubagentEnd)

	if len(subagentStarts) != 1 || len(subagentEnds) != 1 {
		t.Fatalf("expected 1 start and 1 end, got %d starts %d ends", len(subagentStarts), len(subagentEnds))
	}
	if subagentStarts[0].Subagent == nil || subagentEnds[0].Subagent == nil {
		t.Fatal("subagent start/end refs must be non-nil")
	}

	startID := subagentStarts[0].Subagent.ID
	endID := subagentEnds[0].Subagent.ID

	// Scope ID must be stable: same value on start and end.
	if startID != endID {
		t.Errorf("scope ID not stable: start=%v end=%v", startID, endID)
	}
	// Scope ID must be args.agentId (req-handle-aaa).
	if startID != "req-handle-aaa" {
		t.Errorf("scope ID: got %v want req-handle-aaa", startID)
	}

	// result_agent_id must record the execution-side ID.
	resultAgentID := subagentEnds[0].Raw["result_agent_id"]
	if resultAgentID != "exec-bbb-different" {
		t.Errorf("result_agent_id: got %v want exec-bbb-different", resultAgentID)
	}

	// Two conversationSteps must be joined.
	endText, _ := subagentEnds[0].Result["text"].(string)
	if !strings.Contains(endText, "82%") || !strings.Contains(endText, "critical paths") {
		t.Errorf("subagent.end Result text should contain both steps: %q", endText)
	}
}

func TestTaskToolCallCompletedOnlySynthesizesStartBeforeEnd(t *testing.T) {
	completed := `{"type":"tool_call","subtype":"completed","call_id":"call-completed-only","tool_call":{"taskToolCall":{"args":{"description":"Recovered task","agentId":"request-id"},"result":{"success":{"conversationSteps":[{"assistantMessage":{"text":"Recovered result."}}],"agentId":"execution-id","durationMs":"42"}}}}}`
	stdout := strings.Join([]string{
		`{"type":"session","session_id":"cursor-completed-only"}`,
		completed,
		completed, // duplicate provider frame must not produce a second end.
		`{"type":"run.completed","session_id":"cursor-completed-only","result":"done"}`,
		"",
	}, "\n")

	sink := &streamRecordingSink{}
	p := newCursorParserWithRunID(sink, "run-completed-only")
	_ = p.onChunk("stdout", []byte(stdout), time.Now().UTC())
	p.finalize()

	want := []agentadaptor.StreamKind{
		agentadaptor.StreamToolCallStart,
		agentadaptor.StreamSubagentStart,
		agentadaptor.StreamSubagentEnd,
		agentadaptor.StreamToolCallEnd,
		agentadaptor.StreamToolCallResult,
		agentadaptor.StreamRunFinished,
	}
	if len(sink.streams) != len(want) {
		t.Fatalf("stream count: got %d want %d: %#v", len(sink.streams), len(want), sink.streams)
	}
	for i, kind := range want {
		if sink.streams[i].Kind != kind {
			t.Errorf("stream[%d].Kind: got %q want %q", i, sink.streams[i].Kind, kind)
		}
	}
	start := sink.streams[1].Subagent
	end := sink.streams[2].Subagent
	if start == nil || end == nil {
		t.Fatal("completed-only subagent start/end refs must be non-nil")
	}
	if start.ID != "execution-id" || end.ID != start.ID {
		t.Errorf("completed-only scope IDs: start=%q end=%q want stable execution-id", start.ID, end.ID)
	}
}

// TestTaskToolCallNoTextDeltaInsideScope verifies that no child token delta
// events are emitted between subagent.start and subagent.end (headless path
// does not project internal taskToolCallDelta).
func TestTaskToolCallNoTextDeltaInsideScope(t *testing.T) {
	fixture := loadFixture(t, "task-tool-call.jsonl")

	sink := &streamRecordingSink{}
	p := newCursorParserWithRunID(sink, "run-nodelay")
	_ = p.onChunk("stdout", fixture, time.Now().UTC())
	p.finalize()

	subagentStarts := sink.streamsByKind(agentadaptor.StreamSubagentStart)
	subagentEnds := sink.streamsByKind(agentadaptor.StreamSubagentEnd)
	if len(subagentStarts) == 0 || len(subagentEnds) == 0 {
		t.Fatal("fixture must produce subagent.start and subagent.end")
	}

	// Find stream positions of start and end.
	startSeq := -1
	endSeq := -1
	for i, pl := range sink.streams {
		if pl.Kind == agentadaptor.StreamSubagentStart {
			startSeq = i
		}
		if pl.Kind == agentadaptor.StreamSubagentEnd {
			endSeq = i
		}
	}

	// Between start and end, there must be no text.content events with
	// non-empty Delta (would imply fake child token streaming).
	for i := startSeq + 1; i < endSeq; i++ {
		pl := sink.streams[i]
		if pl.Kind == agentadaptor.StreamTextContent && pl.Delta != "" {
			t.Errorf("unexpected text.content delta inside subagent scope at stream position %d: %q", i, pl.Delta)
		}
	}
}

// TestTaskToolCallChildTextNotInParentOutput verifies that subagent
// conversationSteps text does not leak into the parent Output field.
func TestTaskToolCallChildTextNotInParentOutput(t *testing.T) {
	fixture := loadFixture(t, "task-tool-call.jsonl")

	p := newCursorParserWithRunID(nil, "run-isolation")
	_ = p.onChunk("stdout", fixture, time.Now().UTC())
	p.finalize()

	output := p.buildOutput()
	// The subagent text "installation steps" must NOT appear in parent Output.
	if strings.Contains(output, "installation steps") {
		t.Errorf("parent Output must not contain subagent conversationSteps text, got: %q", output)
	}
}

// TestTaskToolCallStreamCapability verifies that the cursor adapter declares
// itself as StreamAwareDriver and reports honest capabilities.
func TestTaskToolCallStreamCapability(t *testing.T) {
	a := adapter{}
	cap := a.StreamCapability()
	if !cap.Native {
		t.Error("StreamCapability.Native must be true for stream-json mode")
	}
	if cap.TokenLevel {
		t.Error("StreamCapability.TokenLevel must be false: headless path does not project token deltas")
	}
	if cap.Reasoning {
		t.Error("StreamCapability.Reasoning must be false for cursor headless path")
	}
	if !cap.Subagents {
		t.Error("StreamCapability.Subagents must be true")
	}
	if cap.SubagentNesting {
		t.Error("StreamCapability.SubagentNesting must be false")
	}
	if !cap.SubagentToolLinkage {
		t.Error("StreamCapability.SubagentToolLinkage must be true")
	}
	if cap.SubagentTextDelta {
		t.Error("StreamCapability.SubagentTextDelta must be false")
	}
}

func TestTaskToolCallStartedWithoutCompletedSynthesizesEndBeforeRunTerminal(t *testing.T) {
	tests := []struct {
		name         string
		terminalLine string
		terminalKind agentadaptor.StreamKind
	}{
		{
			name:         "finished",
			terminalLine: `{"type":"run.completed","session_id":"cursor-orphan","result":"run ended"}`,
			terminalKind: agentadaptor.StreamRunFinished,
		},
		{
			name:         "error",
			terminalLine: `{"type":"run.failed","session_id":"cursor-orphan","message":"run failed"}`,
			terminalKind: agentadaptor.StreamRunError,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stdout := strings.Join([]string{
				`{"type":"session","session_id":"cursor-orphan"}`,
				`{"type":"tool_call","subtype":"started","call_id":"call-orphan","tool_call":{"taskToolCall":{"args":{"description":"Orphan task","agentId":"req-orphan"}}}}`,
				tt.terminalLine,
				"",
			}, "\n")

			sink := &streamRecordingSink{}
			p := newCursorParserWithRunID(sink, "run-orphan")
			_ = p.onChunk("stdout", []byte(stdout), time.Now().UTC())
			p.finalize()
			p.finalize() // idempotence: must not emit a duplicate synthetic end.

			starts := sink.streamsByKind(agentadaptor.StreamSubagentStart)
			ends := sink.streamsByKind(agentadaptor.StreamSubagentEnd)
			terminals := sink.streamsByKind(tt.terminalKind)
			if len(starts) != 1 || len(ends) != 1 || len(terminals) != 1 {
				t.Fatalf("start/end/terminal counts: got %d/%d/%d want 1/1/1", len(starts), len(ends), len(terminals))
			}
			if starts[0].Subagent == nil || ends[0].Subagent == nil ||
				ends[0].Subagent.ID != starts[0].Subagent.ID {
				t.Fatalf("synthetic end scope mismatch: start=%#v end=%#v", starts[0].Subagent, ends[0].Subagent)
			}
			if ends[0].Result["status"] != "failed" || ends[0].Result["synthetic"] != true {
				t.Errorf("synthetic end Result: got %#v", ends[0].Result)
			}
			if len(p.pendingSubagents) != 0 {
				t.Errorf("pendingSubagents must be empty after synthetic flush, got %d", len(p.pendingSubagents))
			}
			checkpoint := p.checkpoint(1)
			if checkpoint == nil || checkpoint.State == nil || checkpoint.State.ResumeID != "cursor-orphan" {
				t.Errorf("synthetic close must not alter parent checkpoint: got %#v", checkpoint)
			}

			endIndex, terminalIndex := -1, -1
			for i, payload := range sink.streams {
				if payload.Role != agentadaptor.RoleAssistant {
					t.Errorf("stream[%d] Role: got %q want zero value", i, payload.Role)
				}
				if payload.Kind == agentadaptor.StreamSubagentEnd {
					endIndex = i
				}
				if payload.Kind == tt.terminalKind {
					terminalIndex = i
				}
			}
			if endIndex < 0 || terminalIndex < 0 || endIndex >= terminalIndex {
				t.Errorf("synthetic end must precede run terminal: end=%d terminal=%d", endIndex, terminalIndex)
			}
		})
	}
}

func TestTaskToolCallTruncatedRunSynthesizesEndBeforeExitTerminal(t *testing.T) {
	stdout := strings.Join([]string{
		`{"type":"session","session_id":"cursor-truncated"}`,
		`{"type":"tool_call","subtype":"started","call_id":"call-truncated","tool_call":{"taskToolCall":{"args":{"description":"Truncated task","agentId":"req-truncated"}}}}`,
		"",
	}, "\n")

	sink := &streamRecordingSink{}
	p := newCursorParserWithRunID(sink, "run-truncated")
	_ = p.onChunk("stdout", []byte(stdout), time.Now().UTC())
	p.finalize()
	p.finishRun(1)
	p.finishRun(1) // terminal and synthetic end must both remain exactly once.

	ends := sink.streamsByKind(agentadaptor.StreamSubagentEnd)
	errors := sink.streamsByKind(agentadaptor.StreamRunError)
	if len(ends) != 1 || len(errors) != 1 {
		t.Fatalf("synthetic end/run.error counts: got %d/%d want 1/1", len(ends), len(errors))
	}
	endIndex, errorIndex := -1, -1
	for i, payload := range sink.streams {
		switch payload.Kind {
		case agentadaptor.StreamSubagentEnd:
			endIndex = i
		case agentadaptor.StreamRunError:
			errorIndex = i
		}
	}
	if endIndex < 0 || errorIndex < 0 || endIndex >= errorIndex {
		t.Errorf("synthetic end must precede exit-derived run.error: end=%d error=%d", endIndex, errorIndex)
	}
}

// TestTaskToolCallFlatToolCallStillWorks ensures that non-discriminated
// (flat) tool_call events are still parsed correctly after the discriminated
// branch was added.
func TestTaskToolCallFlatToolCallStillWorks(t *testing.T) {
	fixture := loadFixture(t, "with-tool.jsonl")

	p := newCursorParserWithRunID(nil, "run-flat")
	_ = p.onChunk("stdout", fixture, time.Now().UTC())
	p.finalize()

	toolCalls := filterTranscript(p.transcript, agentadaptor.TranscriptToolCall)
	if len(toolCalls) != 1 {
		t.Fatalf("expected 1 flat tool_call, got %d", len(toolCalls))
	}
	if toolCalls[0].ToolName != "search" {
		t.Errorf("ToolName: got %q want search", toolCalls[0].ToolName)
	}
}

// TestTaskToolCallRunIDPropagated verifies that RunID is populated on
// streaming payloads when provided to the parser.
func TestTaskToolCallRunIDPropagated(t *testing.T) {
	stdout := strings.Join([]string{
		`{"type":"session","session_id":"cursor-runid","model":"composer-2.5-fast"}`,
		`{"type":"tool_call","subtype":"started","call_id":"call-runid","tool_call":{"taskToolCall":{"args":{"description":"RunID test","agentId":"req-runid"}}}}`,
		`{"type":"tool_call","subtype":"completed","call_id":"call-runid","tool_call":{"taskToolCall":{"args":{"description":"RunID test","agentId":"req-runid"},"result":{"success":{"conversationSteps":[{"assistantMessage":{"text":"done"}}],"agentId":"req-runid","durationMs":"100"}}}}}`,
		"",
	}, "\n")

	sink := &streamRecordingSink{}
	p := newCursorParserWithRunID(sink, "my-run-42")
	_ = p.onChunk("stdout", []byte(stdout), time.Now().UTC())
	p.finalize()

	for _, pl := range sink.streams {
		if pl.RunID != "my-run-42" {
			t.Errorf("payload kind=%s has RunID=%q, want my-run-42", pl.Kind, pl.RunID)
		}
	}
}

// TestTaskToolCallCheckpointDoesNotIncludeSubagentIDs verifies that subagent
// agent IDs do not appear in the parent DriverCheckpoint.
func TestTaskToolCallCheckpointDoesNotIncludeSubagentIDs(t *testing.T) {
	fixture := loadFixture(t, "task-tool-call.jsonl")

	p := newCursorParserWithRunID(nil, "run-checkpoint")
	_ = p.onChunk("stdout", fixture, time.Now().UTC())
	p.finalize()

	cp := p.checkpoint(0)
	if cp == nil || cp.State == nil {
		t.Fatal("expected valid checkpoint")
	}
	// Checkpoint State must not contain subagent IDs.
	for k, v := range cp.State.Data {
		if strings.Contains(v, "req-7a8b9c0d") || strings.Contains(v, "exec-") {
			t.Errorf("checkpoint State.Data[%q]=%q must not contain subagent IDs", k, v)
		}
	}
	if strings.Contains(cp.State.ResumeID, "req-7a8b9c0d") {
		t.Errorf("checkpoint ResumeID must not be a subagent ID, got %q", cp.State.ResumeID)
	}
}

// TestTaskToolCallUnknownDiscriminatedVariant verifies that an unknown
// discriminated tool_call variant produces a system transcript item instead
// of being silently dropped.
func TestTaskToolCallUnknownDiscriminatedVariant(t *testing.T) {
	stdout := strings.Join([]string{
		`{"type":"session","session_id":"cursor-unknown"}`,
		`{"type":"tool_call","subtype":"started","call_id":"call-unk","tool_call":{"futureVariant":{"data":"opaque"}}}`,
		"",
	}, "\n")

	p := newCursorParserWithRunID(nil, "run-unknown")
	_ = p.onChunk("stdout", []byte(stdout), time.Now().UTC())
	p.finalize()

	sys := filterTranscript(p.transcript, agentadaptor.TranscriptSystem)
	if len(sys) == 0 {
		t.Error("unknown discriminated tool_call variant must emit a system transcript item")
	}
}

// filterTranscript returns all TranscriptItems of the given kind.
func filterTranscript(items []agentadaptor.TranscriptItem, kind agentadaptor.TranscriptKind) []agentadaptor.TranscriptItem {
	var out []agentadaptor.TranscriptItem
	for _, it := range items {
		if it.Kind == kind {
			out = append(out, it)
		}
	}
	return out
}
