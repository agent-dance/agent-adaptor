package claude

import (
	"context"
	"testing"
	"time"

	"github.com/agent-dance/agent-adaptor/v2/driver"
)

func TestMessageStopSegmentsHavePerTurnUsage(t *testing.T) {
	port := newMessageTestPort(driver.MessageItem{ID: "next", Text: "continue", Timing: driver.TimingAfterTurn})
	sink := &messageTestSink{}
	p := newClaudeParser(sink)
	s := newInitializedMessageTestSession(context.Background(), port)
	p.messages = s
	writes := make(chan []byte, 1)
	s.start(messageTestStdin{writes: writes}, func() {})
	defer s.finish(nil)
	feed := func(v any) {
		t.Helper()
		if err := p.onChunk("stdout", messageFrame(v), time.Time{}); err != nil {
			t.Fatal(err)
		}
	}
	assistant := func(text string, input, output int) map[string]any {
		return map[string]any{"type": "assistant", "session_id": "session", "message": map[string]any{
			"role": "assistant", "content": []any{map[string]any{"type": "text", "text": text}},
			"usage": map[string]any{"input_tokens": input, "output_tokens": output},
		}}
	}
	feed(assistant("first", 3, 4))
	feed(messageHook("Stop", "", ""))
	<-writes
	<-port.acked
	feed(assistant("second", 5, 6))
	feed(map[string]any{"type": "result", "subtype": "success", "is_error": false,
		"session_id": "session", "result": "second", "usage": map[string]any{"input_tokens": 8, "output_tokens": 10}})
	var turns []*driver.TurnCompleted
	for _, event := range sink.events {
		if event.Type == driver.RunEventTurnCompleted {
			turns = append(turns, event.Turn)
		}
	}
	if len(turns) != 2 {
		t.Fatalf("turns=%+v", turns)
	}
	for i, want := range []driver.Usage{{InputTokens: 3, OutputTokens: 4}, {InputTokens: 5, OutputTokens: 6}} {
		if got := turns[i].Usage; got == nil || *got != want {
			t.Errorf("turn %d usage=%+v, want observed per-turn usage=%+v", i, got, want)
		}
	}
	response, err := buildClaudeResponse(driver.Request{}, p, driver.RawStreams{}, 0, "", false, "", "", "")
	wantTotal := driver.Usage{InputTokens: 8, OutputTokens: 10}
	if err != nil || response.Usage == nil || *response.Usage != wantTotal {
		t.Fatalf("aggregate usage=%+v error=%v, want %+v", response.Usage, err, wantTotal)
	}
	if total := addClaudeUsage(turns[0].Usage, turns[1].Usage); total == nil || *total != wantTotal {
		t.Fatalf("summed turn usage=%+v, want %+v", total, wantTotal)
	}
}

func TestMessageUsageSnapshotsAndTerminalFallback(t *testing.T) {
	for _, terminalUsage := range []bool{false, true} {
		t.Run(map[bool]string{false: "assistant-only", true: "terminal-remainder"}[terminalUsage], func(t *testing.T) {
			sink := &messageTestSink{}
			p := newClaudeParser(sink)
			assistant := func(id, parent string, input, output, cached int) {
				p.handlePayload("", map[string]any{"type": "assistant", "parent_tool_use_id": parent,
					"message": map[string]any{"id": id, "usage": map[string]any{
						"input_tokens": input, "output_tokens": output, "cache_read_input_tokens": cached}}})
			}
			assistant("first", "", 3, 2, 1)
			assistant("first", "", 3, 4, 1) // updated snapshot, not a second model call
			assistant("worker", "parent", 100, 100, 100)
			assistant("tool", "", 2, 1, 0) // another model call in this segment
			p.emitTurnCompleted("first segment", p.turnUsage)
			terminal := map[string]any{"type": "result", "subtype": "success", "is_error": false,
				"session_id": "session", "result": "last segment"}
			if terminalUsage {
				terminal["usage"] = map[string]any{"input_tokens": 10, "output_tokens": 11, "cache_read_input_tokens": 3}
			} else {
				assistant("last", "", 5, 6, 2)
			}
			p.handlePayload(string(messageFrame(terminal)), terminal)
			p.completeStream(nil, 0, "", false)
			var turns []*driver.TurnCompleted
			for _, event := range sink.events {
				if event.Turn != nil {
					turns = append(turns, event.Turn)
				}
			}
			if len(turns) != 2 {
				t.Fatalf("turns=%+v", turns)
			}
			for i, want := range []driver.Usage{{InputTokens: 5, OutputTokens: 5, CachedInputTokens: 1}, {InputTokens: 5, OutputTokens: 6, CachedInputTokens: 2}} {
				if got := turns[i].Usage; got == nil || *got != want {
					t.Errorf("turn %d usage=%+v, want %+v", i, got, want)
				}
			}
			wantTotal := driver.Usage{InputTokens: 10, OutputTokens: 11, CachedInputTokens: 3}
			if p.usage == nil || *p.usage != wantTotal {
				t.Fatalf("total usage=%+v, want %+v", p.usage, wantTotal)
			}
		})
	}
}

func TestMessageUsageDistinguishesMissingAndObservedZero(t *testing.T) {
	for _, observed := range []bool{false, true} {
		t.Run(map[bool]string{false: "missing", true: "zero"}[observed], func(t *testing.T) {
			sink := &messageTestSink{}
			p := newClaudeParser(sink)
			if observed {
				p.handlePayload("", map[string]any{"type": "assistant", "message": map[string]any{
					"usage": map[string]any{"input_tokens": 0, "output_tokens": 0}}})
			}
			terminal := map[string]any{"type": "result", "subtype": "success", "is_error": false,
				"session_id": "session", "result": "done"}
			p.handlePayload(string(messageFrame(terminal)), terminal)
			p.completeStream(nil, 0, "", false)
			var turns int
			for _, event := range sink.events {
				if event.Turn != nil {
					turns++
					if (event.Turn.Usage != nil) != observed || (observed && *event.Turn.Usage != (driver.Usage{})) {
						t.Fatalf("observed=%t turn usage=%+v", observed, event.Turn.Usage)
					}
				}
			}
			if turns != 1 || (p.usage != nil) != observed {
				t.Fatalf("turns=%d observed=%t total usage=%+v", turns, observed, p.usage)
			}
		})
	}
}
