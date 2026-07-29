package codebuddy

import (
	"encoding/json"
	"strings"
	"testing"

	driver "github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/testutil"
)

// realStreamingOutput is a trimmed `codebuddy -p --output-format stream-json
// --include-partial-messages` transcript (Anthropic Messages API-shaped
// stream_event frames) covering text, thinking, tool_use, tool_result and the
// terminal result.
const realStreamingOutput = `{"type":"system","subtype":"init","session_id":"cb-stream-1","model":"claude-haiku-4.5"}
{"type":"stream_event","session_id":"cb-stream-1","event":{"type":"message_start","message":{"id":"msg_1","usage":{"input_tokens":10}}}}
{"type":"stream_event","event":{"type":"content_block_start","index":0,"content_block":{"type":"text"}}}
{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hel"}}}
{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"lo"}}}
{"type":"stream_event","event":{"type":"content_block_stop","index":0}}
{"type":"stream_event","event":{"type":"content_block_start","index":1,"content_block":{"type":"thinking"}}}
{"type":"stream_event","event":{"type":"content_block_delta","index":1,"delta":{"type":"thinking_delta","thinking":"pondering"}}}
{"type":"stream_event","event":{"type":"content_block_stop","index":1}}
{"type":"stream_event","event":{"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"tool_1","name":"Read"}}}
{"type":"stream_event","event":{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"{\"path\":\"go.mod\"}"}}}
{"type":"stream_event","event":{"type":"content_block_stop","index":2}}
{"type":"stream_event","event":{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":5}}}
{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"tool_1","content":"module x","is_error":false}]}}
{"type":"result","subtype":"success","is_error":false,"result":"Hello","session_id":"cb-stream-1","num_turns":1,"usage":{"input_tokens":10,"output_tokens":5}}
`

func TestStreamingParserEmitsFullLifecycle(t *testing.T) {
	rec := &testutil.EventRecorder{}
	p := newParser(rec)
	p.enableStreaming("run-stream-1")

	// Feed the transcript in two chunks split mid-line to exercise the line
	// buffering in onChunk.
	half := len(realStreamingOutput) / 2
	if err := p.onChunk("stdout", []byte(realStreamingOutput[:half]), timeNow()); err != nil {
		t.Fatalf("onChunk 1: %v", err)
	}
	if err := p.onChunk("stdout", []byte(realStreamingOutput[half:]), timeNow()); err != nil {
		t.Fatalf("onChunk 2: %v", err)
	}
	p.finalize()
	p.completeStream(p.failureForOutcome(0), 0, "", false)

	counts := map[driver.StreamKind]int{}
	var textDelta strings.Builder
	var toolArgs strings.Builder
	var finished *driver.StreamPayload
	for _, pl := range rec.StreamSnapshot() {
		counts[pl.Kind]++
		switch pl.Kind {
		case driver.StreamTextContent:
			textDelta.WriteString(pl.Delta)
		case driver.StreamToolCallArgs:
			toolArgs.WriteString(pl.Delta)
		case driver.StreamRunFinished:
			clone := pl
			finished = &clone
		}
	}

	want := map[driver.StreamKind]int{
		driver.StreamRunStarted:       1,
		driver.StreamTextStart:        1,
		driver.StreamTextContent:      2,
		driver.StreamTextEnd:          1,
		driver.StreamReasoningStart:   1,
		driver.StreamReasoningContent: 1,
		driver.StreamReasoningEnd:     1,
		driver.StreamToolCallStart:    1,
		driver.StreamToolCallArgs:     1,
		driver.StreamToolCallEnd:      1,
		driver.StreamToolCallResult:   1,
		driver.StreamRunFinished:      1,
	}
	for kind, n := range want {
		if counts[kind] != n {
			t.Errorf("stream kind %q count = %d, want %d (all=%v)", kind, counts[kind], n, counts)
		}
	}

	if got := textDelta.String(); got != "Hello" {
		t.Errorf("assembled text = %q, want Hello", got)
	}
	if got := strings.TrimSpace(toolArgs.String()); got != `{"path":"go.mod"}` {
		t.Errorf("assembled tool args = %q", got)
	}
	if finished == nil {
		t.Fatalf("missing StreamRunFinished")
	}
	if finished.Usage == nil || finished.Usage.InputTokens != 10 || finished.Usage.OutputTokens != 5 {
		t.Errorf("finished usage = %+v", finished.Usage)
	}
	if finished.Raw == nil || finished.Raw["stop_reason"] != "end_turn" {
		t.Errorf("finished raw = %+v", finished.Raw)
	}

	// The parser must also reconstruct the final output from the text
	// deltas when there is no aggregated assistant message frame.
	if got := p.buildOutput(); got != "Hello" {
		t.Errorf("buildOutput = %q, want Hello", got)
	}
	if p.terminal == nil || p.terminal.Event != "result" || !json.Valid(p.terminal.JSON) {
		t.Fatalf("terminal = %#v", p.terminal)
	}
}

func TestNonStreamingControlParserReconstructsTranscriptWithoutStreamEvents(t *testing.T) {
	rec := &testutil.EventRecorder{}
	p := newParser(rec)
	p.enableOutputReconstruction("run-control-batch")

	if err := p.onChunk("stdout", []byte(realStreamingOutput), timeNow()); err != nil {
		t.Fatalf("onChunk: %v", err)
	}
	p.finalize()

	// ResultMessage.result remains the v1-authoritative Output.
	if got := p.buildOutput(); got != "Hello" {
		t.Fatalf("buildOutput = %q, want official terminal result", got)
	}
	if streams := rec.StreamSnapshot(); len(streams) != 0 {
		t.Fatalf("non-streaming reconstruction leaked %d StreamPayload events: %+v", len(streams), streams)
	}
	if len(p.transcript) < 2 ||
		p.transcript[len(p.transcript)-2].Kind != driver.TranscriptAssistant ||
		p.transcript[len(p.transcript)-2].Text != "Hello" ||
		p.transcript[len(p.transcript)-1].Kind != driver.TranscriptResult {
		t.Fatalf("reconstructed transcript does not end assistant -> result: %+v", p.transcript)
	}
}

// TestStreamingParserAPIRetryExhaustion verifies the api_retry escalation path
// surfaces a StreamRunError after repeated 5xx retries.
func TestStreamingParserAPIRetryExhaustion(t *testing.T) {
	rec := &testutil.EventRecorder{}
	p := newParser(rec)
	p.enableStreaming("run-retry-1")

	retries := strings.Join([]string{
		`{"type":"system","subtype":"init","session_id":"cb-retry","model":"claude-haiku-4.5"}`,
		`{"type":"system","subtype":"api_retry","error_status":503,"will_retry":true}`,
		`{"type":"system","subtype":"api_retry","error_status":503,"will_retry":true}`,
		`{"type":"system","subtype":"api_retry","error_status":503,"will_retry":true,"message":"upstream 503"}`,
	}, "\n") + "\n"
	if err := p.onChunk("stdout", []byte(retries), timeNow()); err != nil {
		t.Fatalf("onChunk: %v", err)
	}
	p.finalize()

	sawRunError := false
	for _, pl := range rec.StreamSnapshot() {
		if pl.Kind == driver.StreamRunError {
			sawRunError = true
		}
	}
	if !sawRunError {
		t.Fatalf("expected StreamRunError after 3 consecutive 5xx retries, got %+v", rec.StreamSnapshot())
	}
}
