package agui_test

import (
	"context"
	"testing"
	"time"

	aguievents "github.com/ag-ui-protocol/ag-ui/sdks/community/go/pkg/core/events"

	agentadaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/pkg/bridges/agui"
)

func TestTranslatorHappyMessageLifecycle(t *testing.T) {
	t.Parallel()
	tr := agui.NewTranslator()

	events := translateAll(tr, []agentadaptor.StreamPayload{
		{Kind: agentadaptor.StreamRunStarted, ThreadID: "t", RunID: "r"},
		{Kind: agentadaptor.StreamTextStart, MessageID: "m1"},
		{Kind: agentadaptor.StreamTextContent, MessageID: "m1", Delta: "hi"},
		{Kind: agentadaptor.StreamTextContent, MessageID: "m1", Delta: " there"},
		{Kind: agentadaptor.StreamTextEnd, MessageID: "m1"},
		{Kind: agentadaptor.StreamRunFinished, ThreadID: "t", RunID: "r"},
	})

	gotTypes := typesOf(events)
	want := []aguievents.EventType{
		aguievents.EventTypeRunStarted,
		aguievents.EventTypeTextMessageStart,
		aguievents.EventTypeTextMessageContent,
		aguievents.EventTypeTextMessageContent,
		aguievents.EventTypeTextMessageEnd,
		aguievents.EventTypeRunFinished,
	}
	assertTypesEqual(t, want, gotTypes)
	assertVerified(t, events)
}

func TestTranslatorSynthesizesMissingTextStart(t *testing.T) {
	t.Parallel()
	tr := agui.NewTranslator()

	// Skip StreamTextStart; first StreamTextContent must synthesize it.
	events := translateAll(tr, []agentadaptor.StreamPayload{
		{Kind: agentadaptor.StreamRunStarted, ThreadID: "t", RunID: "r"},
		{Kind: agentadaptor.StreamTextContent, MessageID: "m1", Delta: "hello"},
		{Kind: agentadaptor.StreamRunFinished, ThreadID: "t", RunID: "r"},
	})

	want := []aguievents.EventType{
		aguievents.EventTypeRunStarted,
		aguievents.EventTypeTextMessageStart,
		aguievents.EventTypeTextMessageContent,
		aguievents.EventTypeTextMessageEnd, // closed on run finish
		aguievents.EventTypeRunFinished,
	}
	assertTypesEqual(t, want, typesOf(events))
	assertVerified(t, events)
}

func TestTranslatorDuplicateRunStartIgnored(t *testing.T) {
	t.Parallel()
	tr := agui.NewTranslator()
	events := translateAll(tr, []agentadaptor.StreamPayload{
		{Kind: agentadaptor.StreamRunStarted, ThreadID: "t", RunID: "r"},
		{Kind: agentadaptor.StreamRunStarted, ThreadID: "t", RunID: "r"},
		{Kind: agentadaptor.StreamRunFinished, ThreadID: "t", RunID: "r"},
	})
	want := []aguievents.EventType{
		aguievents.EventTypeRunStarted,
		aguievents.EventTypeRunFinished,
	}
	assertTypesEqual(t, want, typesOf(events))
	assertVerified(t, events)
}

func TestTranslatorRunErrorClosesOpenLifecycles(t *testing.T) {
	t.Parallel()
	tr := agui.NewTranslator()
	events := translateAll(tr, []agentadaptor.StreamPayload{
		{Kind: agentadaptor.StreamRunStarted, ThreadID: "t", RunID: "r"},
		{Kind: agentadaptor.StreamTextStart, MessageID: "m1"},
		{Kind: agentadaptor.StreamTextContent, MessageID: "m1", Delta: "partial"},
		{Kind: agentadaptor.StreamRunError, Error: &agentadaptor.RunFailure{Message: "boom", Code: "codex.error"}},
	})

	// Expect: RUN_STARTED, TEXT_MESSAGE_START, TEXT_MESSAGE_CONTENT,
	// then a synthesized TEXT_MESSAGE_END because the message never closed,
	// finally RUN_ERROR.
	want := []aguievents.EventType{
		aguievents.EventTypeRunStarted,
		aguievents.EventTypeTextMessageStart,
		aguievents.EventTypeTextMessageContent,
		aguievents.EventTypeTextMessageEnd,
		aguievents.EventTypeRunError,
	}
	assertTypesEqual(t, want, typesOf(events))
	assertVerified(t, events)
}

func TestTranslatorToolCallLifecycle(t *testing.T) {
	t.Parallel()
	tr := agui.NewTranslator()
	events := translateAll(tr, []agentadaptor.StreamPayload{
		{Kind: agentadaptor.StreamRunStarted, ThreadID: "t", RunID: "r"},
		{Kind: agentadaptor.StreamToolCallStart, ToolCallID: "tc1", Name: "shell"},
		{Kind: agentadaptor.StreamToolCallArgs, ToolCallID: "tc1", Delta: "line 1\n"},
		{Kind: agentadaptor.StreamToolCallEnd, ToolCallID: "tc1"},
		{Kind: agentadaptor.StreamToolCallResult, ToolCallID: "tc1", Result: map[string]any{"status": "completed"}},
		{Kind: agentadaptor.StreamRunFinished, ThreadID: "t", RunID: "r"},
	})
	want := []aguievents.EventType{
		aguievents.EventTypeRunStarted,
		aguievents.EventTypeToolCallStart,
		aguievents.EventTypeToolCallArgs,
		aguievents.EventTypeToolCallEnd,
		aguievents.EventTypeToolCallResult,
		aguievents.EventTypeRunFinished,
	}
	assertTypesEqual(t, want, typesOf(events))
	assertVerified(t, events)
}

func TestTranslatorToolCallArgsSynthesizesMissingStart(t *testing.T) {
	t.Parallel()
	tr := agui.NewTranslator()
	events := translateAll(tr, []agentadaptor.StreamPayload{
		{Kind: agentadaptor.StreamRunStarted, ThreadID: "t", RunID: "r"},
		{Kind: agentadaptor.StreamToolCallArgs, ToolCallID: "late", Name: "shell", Delta: "out\n"},
		{Kind: agentadaptor.StreamToolCallStart, ToolCallID: "late", Name: "shell"},
		{Kind: agentadaptor.StreamToolCallEnd, ToolCallID: "late"},
		{Kind: agentadaptor.StreamToolCallResult, ToolCallID: "late", Result: map[string]any{"status": "ok"}},
		{Kind: agentadaptor.StreamRunFinished, ThreadID: "t", RunID: "r"},
	})
	want := []aguievents.EventType{
		aguievents.EventTypeRunStarted,
		aguievents.EventTypeToolCallStart,
		aguievents.EventTypeToolCallArgs,
		aguievents.EventTypeToolCallEnd,
		aguievents.EventTypeToolCallResult,
		aguievents.EventTypeRunFinished,
	}
	assertTypesEqual(t, want, typesOf(events))
	assertVerified(t, events)
}

func TestTranslatorToolCallResultNilResultUsesEmptyJSONObject(t *testing.T) {
	t.Parallel()
	tr := agui.NewTranslator()
	events := translateAll(tr, []agentadaptor.StreamPayload{
		{Kind: agentadaptor.StreamRunStarted, ThreadID: "t", RunID: "r"},
		{Kind: agentadaptor.StreamToolCallStart, ToolCallID: "x", Name: "t"},
		{Kind: agentadaptor.StreamToolCallEnd, ToolCallID: "x"},
		{Kind: agentadaptor.StreamToolCallResult, ToolCallID: "x", Result: nil},
		{Kind: agentadaptor.StreamRunFinished, ThreadID: "t", RunID: "r"},
	})
	var content string
	for _, ev := range events {
		if r, ok := ev.(*aguievents.ToolCallResultEvent); ok {
			content = r.Content
			break
		}
	}
	if content != "{}" {
		t.Fatalf("expected content %q, got %q", "{}", content)
	}
	assertVerified(t, events)
}

func TestTranslatorToolCallResultUsesClaudeTextField(t *testing.T) {
	t.Parallel()
	tr := agui.NewTranslator()
	events := translateAll(tr, []agentadaptor.StreamPayload{
		{Kind: agentadaptor.StreamRunStarted, ThreadID: "t", RunID: "r"},
		{Kind: agentadaptor.StreamToolCallStart, ToolCallID: "tc1", Name: "Bash"},
		{Kind: agentadaptor.StreamToolCallEnd, ToolCallID: "tc1"},
		{Kind: agentadaptor.StreamToolCallResult, ToolCallID: "tc1", Result: map[string]any{
			"text":        "/Users/blurooo/project/agent-adaptor",
			"is_error":    false,
			"tool_use_id": "tc1",
		}},
		{Kind: agentadaptor.StreamRunFinished, ThreadID: "t", RunID: "r"},
	})

	want := []aguievents.EventType{
		aguievents.EventTypeRunStarted,
		aguievents.EventTypeToolCallStart,
		aguievents.EventTypeToolCallEnd,
		aguievents.EventTypeToolCallResult,
		aguievents.EventTypeRunFinished,
	}
	assertTypesEqual(t, want, typesOf(events))

	var result *aguievents.ToolCallResultEvent
	for _, ev := range events {
		if typed, ok := ev.(*aguievents.ToolCallResultEvent); ok {
			result = typed
			break
		}
	}
	if result == nil {
		t.Fatal("missing ToolCallResultEvent")
	}
	if result.Content != "/Users/blurooo/project/agent-adaptor" {
		t.Fatalf("tool result content: got %q", result.Content)
	}
	assertVerified(t, events)
}

func TestTranslatorUnknownKindBecomesCustomEventAfterRunStart(t *testing.T) {
	t.Parallel()
	tr := agui.NewTranslator()

	// A CUSTOM-type payload arriving before RUN_STARTED must be buffered,
	// not emitted; the very first event must always be RUN_STARTED so
	// CopilotKit / AG-UI conformant clients don't reject the stream with
	// INCOMPLETE_STREAM.
	before := tr.Translate(agentadaptor.StreamPayload{
		Kind: "",
		Name: "thread/tokenUsage/updated",
		Raw:  map[string]any{"foo": "bar"},
	})
	if len(before) != 0 {
		t.Fatalf("custom payload before RUN_STARTED should be buffered, got %+v", before)
	}

	started := tr.Translate(agentadaptor.StreamPayload{
		Kind: agentadaptor.StreamRunStarted, ThreadID: "t", RunID: "r",
	})
	if len(started) != 2 {
		t.Fatalf("RUN_STARTED should flush the pending CUSTOM event, got %+v", started)
	}
	if started[0].Type() != aguievents.EventTypeRunStarted {
		t.Fatalf("first event must be RUN_STARTED, got %s", started[0].Type())
	}
	if started[1].Type() != aguievents.EventTypeCustom {
		t.Fatalf("second event must be the buffered CUSTOM, got %s", started[1].Type())
	}

	// After RUN_STARTED, subsequent CUSTOM events flow through immediately.
	after := tr.Translate(agentadaptor.StreamPayload{
		Kind: "",
		Name: "thread/tokenUsage/updated",
		Raw:  map[string]any{"foo": "baz"},
	})
	if len(after) != 1 || after[0].Type() != aguievents.EventTypeCustom {
		t.Fatalf("expected a single CUSTOM event after start, got %+v", after)
	}
}

func TestTranslatorCodexOrderingRunStartedIsFirst(t *testing.T) {
	t.Parallel()
	// Reproduce the codex ordering bug: several pre-turn CUSTOM events
	// (mcpServer setup, thread status changes) arrive before RUN_STARTED.
	// The translator must hold them back until RUN_STARTED has been
	// emitted.
	tr := agui.NewTranslator()
	payloads := []agentadaptor.StreamPayload{
		{Kind: "", Name: "mcpServer/startupStatus/updated", Raw: map[string]any{"status": "starting"}},
		{Kind: "", Name: "thread/status/changed", Raw: map[string]any{"status": "active"}},
		{Kind: agentadaptor.StreamRunStarted, ThreadID: "t", RunID: "r"},
		{Kind: agentadaptor.StreamTextContent, MessageID: "m1", Delta: "hi"},
		{Kind: agentadaptor.StreamRunFinished, ThreadID: "t", RunID: "r"},
	}
	events := translateAll(tr, payloads)
	types := typesOf(events)

	if len(types) < 1 || types[0] != aguievents.EventTypeRunStarted {
		t.Fatalf("stream must start with RUN_STARTED, got %v", types)
	}
	want := []aguievents.EventType{
		aguievents.EventTypeRunStarted,
		aguievents.EventTypeCustom, // mcpServer/startupStatus/updated (flushed after RUN_STARTED)
		aguievents.EventTypeCustom, // thread/status/changed
		aguievents.EventTypeTextMessageStart,
		aguievents.EventTypeTextMessageContent,
		aguievents.EventTypeTextMessageEnd,
		aguievents.EventTypeRunFinished,
	}
	assertTypesEqual(t, want, types)
	assertVerified(t, events)
}

func TestTranslatorReasoningMessageStartRoleIsReasoning(t *testing.T) {
	t.Parallel()
	// AG-UI validates REASONING_MESSAGE_START.role as z.literal("reasoning").
	// Sending "assistant" triggers an invalid_literal Zod error on the
	// client and the whole chat fails. This test pins the role so future
	// changes cannot regress the wire format.
	tr := agui.NewTranslator()
	events := translateAll(tr, []agentadaptor.StreamPayload{
		{Kind: agentadaptor.StreamRunStarted, ThreadID: "t", RunID: "r"},
		{Kind: agentadaptor.StreamReasoningStart, MessageID: "rs1"},
		{Kind: agentadaptor.StreamReasoningContent, MessageID: "rs1", Delta: "thinking..."},
		{Kind: agentadaptor.StreamReasoningEnd, MessageID: "rs1"},
		{Kind: agentadaptor.StreamRunFinished, ThreadID: "t", RunID: "r"},
	})

	var startEvent *aguievents.ReasoningMessageStartEvent
	for _, ev := range events {
		if e, ok := ev.(*aguievents.ReasoningMessageStartEvent); ok {
			startEvent = e
			break
		}
	}
	if startEvent == nil {
		t.Fatalf("no ReasoningMessageStartEvent emitted: %v", typesOf(events))
	}
	if startEvent.Role != "reasoning" {
		t.Fatalf("REASONING_MESSAGE_START.role: want %q got %q", "reasoning", startEvent.Role)
	}
	assertVerified(t, events)
}

func TestTranslatorReasoningContentSynthesizesStartWithCorrectRole(t *testing.T) {
	t.Parallel()
	// Same rule applies when the translator has to synthesize a
	// REASONING_MESSAGE_START because the adapter jumped straight to
	// REASONING_MESSAGE_CONTENT.
	tr := agui.NewTranslator()
	events := translateAll(tr, []agentadaptor.StreamPayload{
		{Kind: agentadaptor.StreamRunStarted, ThreadID: "t", RunID: "r"},
		{Kind: agentadaptor.StreamReasoningContent, MessageID: "rs2", Delta: "thinking..."},
		{Kind: agentadaptor.StreamRunFinished, ThreadID: "t", RunID: "r"},
	})

	var startEvent *aguievents.ReasoningMessageStartEvent
	for _, ev := range events {
		if e, ok := ev.(*aguievents.ReasoningMessageStartEvent); ok {
			startEvent = e
			break
		}
	}
	if startEvent == nil {
		t.Fatalf("no synthesized ReasoningMessageStartEvent: %v", typesOf(events))
	}
	if startEvent.Role != "reasoning" {
		t.Fatalf("synthesized ReasoningMessageStart.role: want %q got %q", "reasoning", startEvent.Role)
	}
	assertVerified(t, events)
}

func TestTranslatorSynthesizesRunStartedBeforeFinishWhenMissing(t *testing.T) {
	t.Parallel()
	// Pathological edge case: adapter emits content and then RUN_FINISHED
	// without ever sending RUN_STARTED. The bridge must still output a
	// well-formed stream starting with RUN_STARTED.
	tr := agui.NewTranslator()
	events := translateAll(tr, []agentadaptor.StreamPayload{
		{Kind: agentadaptor.StreamTextContent, MessageID: "m1", Delta: "hi", ThreadID: "t", RunID: "r"},
		{Kind: agentadaptor.StreamRunFinished, ThreadID: "t", RunID: "r"},
	})
	types := typesOf(events)
	if len(types) < 1 || types[0] != aguievents.EventTypeRunStarted {
		t.Fatalf("synthesized stream must start with RUN_STARTED, got %v", types)
	}
	// Last event must be RUN_FINISHED; somewhere in between must be the
	// buffered text content.
	if types[len(types)-1] != aguievents.EventTypeRunFinished {
		t.Fatalf("last event must be RUN_FINISHED, got %s", types[len(types)-1])
	}
	sawContent := false
	for _, tp := range types {
		if tp == aguievents.EventTypeTextMessageContent {
			sawContent = true
		}
	}
	if !sawContent {
		t.Fatalf("buffered TextMessageContent missing in synthesized stream: %v", types)
	}
	assertVerified(t, events)
}

func TestWrapClosesChannelAndSynthesizesRunFinished(t *testing.T) {
	t.Parallel()
	h := &fakeHandle{
		stream:    make(chan agentadaptor.StreamPayload, 4),
		events:    make(chan agentadaptor.RunEvent),
		done:      make(chan struct{}),
		runID:     "run-xyz",
		runResult: agentadaptor.RunResult{RunID: "run-xyz"},
	}
	// Emit only a text message; no RUN_STARTED / RUN_FINISHED. The bridge
	// must synthesize the closing RUN_FINISHED.
	h.stream <- agentadaptor.StreamPayload{Kind: agentadaptor.StreamTextStart, MessageID: "m1", ThreadID: "t", RunID: "run-xyz"}
	h.stream <- agentadaptor.StreamPayload{Kind: agentadaptor.StreamTextContent, MessageID: "m1", Delta: "hi"}
	h.stream <- agentadaptor.StreamPayload{Kind: agentadaptor.StreamTextEnd, MessageID: "m1"}
	close(h.stream)
	close(h.done)
	close(h.events)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	out := agui.WrapWithContext(ctx, h)
	var got []aguievents.Event
	for ev := range out {
		got = append(got, ev)
	}
	// The adapter never emitted StreamRunStarted, yet an AG-UI-conformant
	// stream must always begin with RUN_STARTED. The bridge synthesizes it
	// before flushing the buffered text message events.
	want := []aguievents.EventType{
		aguievents.EventTypeRunStarted,
		aguievents.EventTypeTextMessageStart,
		aguievents.EventTypeTextMessageContent,
		aguievents.EventTypeTextMessageEnd,
		aguievents.EventTypeRunFinished,
	}
	assertTypesEqual(t, want, typesOf(got))
	assertVerified(t, got)
}

func TestTranslatorStepAndHitlAndDroppedCustomEvents(t *testing.T) {
	t.Parallel()
	tr := agui.NewTranslator()
	events := translateAll(tr, []agentadaptor.StreamPayload{
		{Kind: agentadaptor.StreamRunStarted, ThreadID: "t", RunID: "r"},
		{Kind: agentadaptor.StreamStepStarted, Name: "planning"},
		{Kind: agentadaptor.StreamStepFinished, Name: "planning"},
		{Kind: agentadaptor.StreamHITLRequested, Name: "approval", RunID: "r", ThreadID: "t", Raw: map[string]any{"kind": "read_file"}},
		{Kind: agentadaptor.StreamDropped, Raw: map[string]any{"dropped_count": 2.0}, RunID: "r", ThreadID: "t"},
		{Kind: agentadaptor.StreamTextContent, MessageID: "m1", Delta: "x"},
		{Kind: agentadaptor.StreamRunFinished, ThreadID: "t", RunID: "r"},
	})
	seenHITL, seenDropped := false, false
	for _, ev := range events {
		if ev.Type() == aguievents.EventTypeCustom {
			if ce, ok := ev.(*aguievents.CustomEvent); ok {
				if ce.Name == string(agentadaptor.StreamHITLRequested) {
					seenHITL = true
				}
				if ce.Name == string(agentadaptor.StreamDropped) {
					seenDropped = true
				}
			}
		}
	}
	if !seenHITL {
		t.Fatal("expected CUSTOM for StreamHITLRequested")
	}
	if !seenDropped {
		t.Fatal("expected CUSTOM for StreamDropped")
	}
	assertVerified(t, events)
}

// TestTranslatorUnknownKindBufferedThenFinished verifies a full run that
// flushes a pre-start CUSTOM, then text, and finishes — must pass
// assertVerified.
func TestTranslatorUnknownKindBufferedThenFinished(t *testing.T) {
	t.Parallel()
	tr := agui.NewTranslator()
	events := translateAll(tr, []agentadaptor.StreamPayload{
		{Kind: "", Name: "thread/prelude", Raw: map[string]any{"n": 1}, ThreadID: "t", RunID: "r"},
		{Kind: agentadaptor.StreamRunStarted, ThreadID: "t", RunID: "r"},
		{Kind: agentadaptor.StreamTextContent, MessageID: "m1", Delta: "hi", ThreadID: "t", RunID: "r"},
		{Kind: agentadaptor.StreamRunFinished, ThreadID: "t", RunID: "r"},
	})
	want := []aguievents.EventType{
		aguievents.EventTypeRunStarted,
		aguievents.EventTypeCustom, // thread/prelude
		aguievents.EventTypeTextMessageStart,
		aguievents.EventTypeTextMessageContent,
		aguievents.EventTypeTextMessageEnd,
		aguievents.EventTypeRunFinished,
	}
	assertTypesEqual(t, want, typesOf(events))
	assertVerified(t, events)
}

// ---------------------------------------------------------------------------
// Fixtures
// ---------------------------------------------------------------------------

type fakeHandle struct {
	stream    chan agentadaptor.StreamPayload
	events    chan agentadaptor.RunEvent
	done      chan struct{}
	runID     string
	runResult agentadaptor.RunResult
	runErr    error
}

func (f *fakeHandle) Events() <-chan agentadaptor.RunEvent            { return f.events }
func (f *fakeHandle) StreamEvents() <-chan agentadaptor.StreamPayload { return f.stream }
func (f *fakeHandle) RunID() string                                   { return f.runID }
func (f *fakeHandle) Cancel(context.Context) error                    { return nil }
func (f *fakeHandle) Wait(ctx context.Context) (agentadaptor.RunResult, error) {
	select {
	case <-ctx.Done():
		return agentadaptor.RunResult{}, ctx.Err()
	case <-f.done:
		return f.runResult, f.runErr
	}
}

func translateAll(tr *agui.Translator, ps []agentadaptor.StreamPayload) []aguievents.Event {
	var out []aguievents.Event
	for _, p := range ps {
		out = append(out, tr.Translate(p)...)
	}
	return out
}

func typesOf(events []aguievents.Event) []aguievents.EventType {
	out := make([]aguievents.EventType, len(events))
	for i, e := range events {
		out[i] = e.Type()
	}
	return out
}

func assertTypesEqual(t *testing.T, want, got []aguievents.EventType) {
	t.Helper()
	if len(want) != len(got) {
		t.Fatalf("event type count mismatch: want %d got %d (%v vs %v)", len(want), len(got), want, got)
	}
	for i := range want {
		if want[i] != got[i] {
			t.Fatalf("event[%d]: want %s got %s (full=%v)", i, want[i], got[i], got)
		}
	}
}

// assertVerified panics the test when the emitted AG-UI event sequence
// fails either the Go SDK's ValidateSequence or the supplementary
// CopilotKit-compat checks in verifier.go.
func assertVerified(t *testing.T, events []aguievents.Event) {
	t.Helper()
	if err := agui.VerifySequence(events); err != nil {
		t.Fatalf("AG-UI sequence verification failed: %v\nevents=%v", err, typesOf(events))
	}
}

// TestTranslatorCodexFixtureConformance feeds the translator a sequence
// mirroring what codex app-server actually emits during a small run
// (observed 2026-04 against codex-cli 0.120.0). This covers the specific
// ordering that previously triggered CopilotKit's `First event must be
// RUN_STARTED` and `invalid_literal` / `role "reasoning"` errors, and
// verifies the output against both the Go ValidateSequence and the
// supplementary CopilotKit compliance rules.
func TestTranslatorCodexFixtureConformance(t *testing.T) {
	t.Parallel()
	tr := agui.NewTranslator()
	const runID = "run-codex"
	const threadID = "thr-codex"

	seq := []agentadaptor.StreamPayload{
		// Codex setup chatter before the turn actually begins:
		{Kind: "", Name: "mcpServer/startupStatus/updated", Raw: map[string]any{"status": "starting"}, ThreadID: threadID, RunID: runID},
		{Kind: "", Name: "thread/status/changed", Raw: map[string]any{"status": "active"}, ThreadID: threadID, RunID: runID},
		{Kind: "", Name: "mcpServer/startupStatus/updated", Raw: map[string]any{"status": "ready"}, ThreadID: threadID, RunID: runID},

		// Turn starts — must yield RUN_STARTED as the first wire event.
		{Kind: agentadaptor.StreamRunStarted, ThreadID: threadID, RunID: runID, TurnID: "turn-1"},

		// Reasoning item lifecycle.
		{Kind: agentadaptor.StreamReasoningStart, MessageID: "rs1", ThreadID: threadID, RunID: runID},
		{Kind: agentadaptor.StreamReasoningContent, MessageID: "rs1", Delta: "thinking about haiku", ThreadID: threadID, RunID: runID},
		{Kind: agentadaptor.StreamReasoningEnd, MessageID: "rs1", ThreadID: threadID, RunID: runID},

		// Text delta stream.
		{Kind: agentadaptor.StreamTextStart, MessageID: "msg1", ThreadID: threadID, RunID: runID},
		{Kind: agentadaptor.StreamTextContent, MessageID: "msg1", Delta: "Words", ThreadID: threadID, RunID: runID},
		{Kind: agentadaptor.StreamTextContent, MessageID: "msg1", Delta: " arrive", ThreadID: threadID, RunID: runID},
		{Kind: agentadaptor.StreamTextContent, MessageID: "msg1", Delta: " like rain.", ThreadID: threadID, RunID: runID},
		{Kind: agentadaptor.StreamTextEnd, MessageID: "msg1", ThreadID: threadID, RunID: runID},

		// Token usage opaque pass-through.
		{Kind: "", Name: "thread/tokenUsage/updated", Raw: map[string]any{"total": map[string]any{"inputTokens": 10, "outputTokens": 20}}, ThreadID: threadID, RunID: runID},

		// Run completion.
		{Kind: agentadaptor.StreamRunFinished, ThreadID: threadID, RunID: runID, Usage: &agentadaptor.Usage{InputTokens: 10, OutputTokens: 20}},
	}

	events := translateAll(tr, seq)
	types := typesOf(events)
	if len(types) < 1 {
		t.Fatalf("translator produced no events")
	}
	if types[0] != aguievents.EventTypeRunStarted {
		t.Fatalf("first event must be RUN_STARTED, got %s (full=%v)", types[0], types)
	}
	if types[len(types)-1] != aguievents.EventTypeRunFinished {
		t.Fatalf("last event must be RUN_FINISHED, got %s (full=%v)", types[len(types)-1], types)
	}

	// Sanity checks: we must see reasoning + text + run lifecycle triads,
	// plus at least one CUSTOM pass-through for the codex chatter.
	seenReasoningStart, seenReasoningEnd, seenTextDelta, seenCustom := false, false, false, false
	for _, e := range events {
		switch e.Type() {
		case aguievents.EventTypeReasoningMessageStart:
			seenReasoningStart = true
			if rs, ok := e.(*aguievents.ReasoningMessageStartEvent); ok && rs.Role != agui.ReasoningRole {
				t.Fatalf("REASONING_MESSAGE_START.role must be %q, got %q", agui.ReasoningRole, rs.Role)
			}
		case aguievents.EventTypeReasoningMessageEnd:
			seenReasoningEnd = true
		case aguievents.EventTypeTextMessageContent:
			seenTextDelta = true
		case aguievents.EventTypeCustom:
			seenCustom = true
		}
	}
	if !seenReasoningStart || !seenReasoningEnd {
		t.Fatalf("reasoning lifecycle missing: start=%v end=%v", seenReasoningStart, seenReasoningEnd)
	}
	if !seenTextDelta {
		t.Fatal("no TEXT_MESSAGE_CONTENT events emitted")
	}
	if !seenCustom {
		t.Fatal("codex CUSTOM pass-through missing (mcpServer / thread/status events lost)")
	}

	// The end-to-end compliance gate: this sequence must satisfy both the
	// Go SDK's ValidateSequence and our CopilotKit-compat overlay.
	assertVerified(t, events)
}

// TestTranslatorPostFinishIsSuppressed proves the runtime guard against
// "The run has already finished" — if adapters emit late payloads after
// RUN_FINISHED, the Translator must swallow them silently.
func TestTranslatorPostFinishIsSuppressed(t *testing.T) {
	t.Parallel()
	tr := agui.NewTranslator()
	events := translateAll(tr, []agentadaptor.StreamPayload{
		{Kind: agentadaptor.StreamRunStarted, ThreadID: "t", RunID: "r"},
		{Kind: agentadaptor.StreamTextContent, MessageID: "m1", Delta: "hi"},
		{Kind: agentadaptor.StreamRunFinished, ThreadID: "t", RunID: "r"},
		// Everything below must be ignored.
		{Kind: agentadaptor.StreamTextContent, MessageID: "m2", Delta: "late"},
		{Kind: "", Name: "thread/tokenUsage/updated", Raw: map[string]any{"x": 1}},
		{Kind: agentadaptor.StreamRunError, Error: &agentadaptor.RunFailure{Message: "late"}},
	})
	last := events[len(events)-1].Type()
	if last != aguievents.EventTypeRunFinished {
		t.Fatalf("last event must be RUN_FINISHED, got %s", last)
	}
	assertVerified(t, events)
}
