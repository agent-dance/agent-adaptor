package agui_test

// P4.2 acceptance: the v1 EventTranslator must produce AG-UI protocol frames
// byte-equivalent (modulo the constructor timestamp) to the legacy Translator
// for the same semantic input. The table below pairs every legacy
// StreamPayload script with its unified-event (adaptor.Event) counterpart and
// compares the two wire outputs frame by frame.

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	aguievents "github.com/ag-ui-protocol/ag-ui/sdks/community/go/pkg/core/events"

	agentadaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/bridges/agui"
	"github.com/agent-dance/agent-adaptor/driver"
	adaptor "github.com/agent-dance/agent-adaptor/next"
)

func translateAllEvents(tr *agui.EventTranslator, evs []adaptor.Event) []aguievents.Event {
	var out []aguievents.Event
	for _, ev := range evs {
		out = append(out, tr.Translate(ev)...)
	}
	return out
}

// frameMap renders one AG-UI event as its wire JSON, minus the constructor
// timestamp (the only field allowed to differ between the two translators).
func frameMap(t *testing.T, ev aguievents.Event) map[string]any {
	t.Helper()
	raw, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("marshal AG-UI event: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal AG-UI event: %v", err)
	}
	delete(m, "timestamp")
	return m
}

func assertFrameEquivalence(t *testing.T, legacy, v1 []aguievents.Event) {
	t.Helper()
	if len(legacy) != len(v1) {
		t.Fatalf("frame count mismatch: legacy %d (%v) vs v1 %d (%v)",
			len(legacy), typesOf(legacy), len(v1), typesOf(v1))
	}
	for i := range legacy {
		lm, vm := frameMap(t, legacy[i]), frameMap(t, v1[i])
		if !reflect.DeepEqual(lm, vm) {
			lj, _ := json.Marshal(lm)
			vj, _ := json.Marshal(vm)
			t.Errorf("frame[%d] differs:\n  legacy: %s\n  v1:     %s", i, lj, vj)
		}
	}
}

func TestEventTranslatorMatchesLegacyFrames(t *testing.T) {
	t.Parallel()

	deadline := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	hitlChoices := []driver.DecisionChoice{
		{Key: "yes", Label: "Approve"},
		{Key: "no", Label: "Reject", Description: "stop here"},
	}

	cases := []struct {
		name   string
		legacy []agentadaptor.StreamPayload
		v1     []adaptor.Event
	}{
		{
			name: "run lifecycle",
			legacy: []agentadaptor.StreamPayload{
				{Kind: agentadaptor.StreamRunStarted, ThreadID: "t", RunID: "r"},
				{Kind: agentadaptor.StreamRunFinished, ThreadID: "t", RunID: "r"},
			},
			v1: []adaptor.Event{
				adaptor.RunStarted{RunID: "r", ThreadID: "t"},
				adaptor.RunFinished{RunID: "r", ThreadID: "t"},
			},
		},
		{
			name: "text lifecycle",
			legacy: []agentadaptor.StreamPayload{
				{Kind: agentadaptor.StreamRunStarted, ThreadID: "t", RunID: "r"},
				{Kind: agentadaptor.StreamTextStart, MessageID: "m1"},
				{Kind: agentadaptor.StreamTextContent, MessageID: "m1", Delta: "hi"},
				{Kind: agentadaptor.StreamTextContent, MessageID: "m1", Delta: " there"},
				{Kind: agentadaptor.StreamTextEnd, MessageID: "m1"},
				{Kind: agentadaptor.StreamRunFinished, ThreadID: "t", RunID: "r"},
			},
			v1: []adaptor.Event{
				adaptor.RunStarted{RunID: "r", ThreadID: "t"},
				adaptor.TextDelta{MessageID: "m1", Phase: adaptor.PhaseStart},
				adaptor.TextDelta{MessageID: "m1", Text: "hi"},
				adaptor.TextDelta{MessageID: "m1", Text: " there"},
				adaptor.TextDelta{MessageID: "m1", Phase: adaptor.PhaseEnd},
				adaptor.RunFinished{RunID: "r", ThreadID: "t"},
			},
		},
		{
			name: "implicit text start and user role",
			legacy: []agentadaptor.StreamPayload{
				{Kind: agentadaptor.StreamRunStarted, ThreadID: "t", RunID: "r"},
				{Kind: agentadaptor.StreamTextContent, MessageID: "m2", Delta: "echo", Role: driver.RoleUser},
				{Kind: agentadaptor.StreamRunFinished, ThreadID: "t", RunID: "r"},
			},
			v1: []adaptor.Event{
				adaptor.RunStarted{RunID: "r", ThreadID: "t"},
				adaptor.TextDelta{MessageID: "m2", Text: "echo", Role: adaptor.RoleUser},
				adaptor.RunFinished{RunID: "r", ThreadID: "t"},
			},
		},
		{
			name: "reasoning lifecycle",
			legacy: []agentadaptor.StreamPayload{
				{Kind: agentadaptor.StreamRunStarted, ThreadID: "t", RunID: "r"},
				{Kind: agentadaptor.StreamReasoningStart, MessageID: "rs1"},
				{Kind: agentadaptor.StreamReasoningContent, MessageID: "rs1", Delta: "mulling"},
				{Kind: agentadaptor.StreamReasoningEnd, MessageID: "rs1"},
				{Kind: agentadaptor.StreamRunFinished, ThreadID: "t", RunID: "r"},
			},
			v1: []adaptor.Event{
				adaptor.RunStarted{RunID: "r", ThreadID: "t"},
				adaptor.Thinking{MessageID: "rs1", Phase: adaptor.PhaseStart},
				adaptor.Thinking{MessageID: "rs1", Text: "mulling"},
				adaptor.Thinking{MessageID: "rs1", Phase: adaptor.PhaseEnd},
				adaptor.RunFinished{RunID: "r", ThreadID: "t"},
			},
		},
		{
			name: "tool call explicit lifecycle and result",
			legacy: []agentadaptor.StreamPayload{
				{Kind: agentadaptor.StreamRunStarted, ThreadID: "t", RunID: "r"},
				{Kind: agentadaptor.StreamToolCallStart, ToolCallID: "tc1", Name: "shell"},
				{Kind: agentadaptor.StreamToolCallArgs, ToolCallID: "tc1", Delta: `{"cmd":"ls"}`},
				{Kind: agentadaptor.StreamToolCallEnd, ToolCallID: "tc1"},
				{Kind: agentadaptor.StreamToolCallResult, ToolCallID: "tc1", Result: map[string]any{"text": "ok"}},
				{Kind: agentadaptor.StreamRunFinished, ThreadID: "t", RunID: "r"},
			},
			v1: []adaptor.Event{
				adaptor.RunStarted{RunID: "r", ThreadID: "t"},
				adaptor.ToolCall{ID: "tc1", Name: "shell", Phase: adaptor.PhaseStart},
				adaptor.ToolCall{ID: "tc1", ArgsDelta: `{"cmd":"ls"}`},
				adaptor.ToolCall{ID: "tc1", Phase: adaptor.PhaseEnd},
				adaptor.ToolResult{ID: "tc1", Result: map[string]any{"text": "ok"}},
				adaptor.RunFinished{RunID: "r", ThreadID: "t"},
			},
		},
		{
			name: "tool call args-first synthesis",
			legacy: []agentadaptor.StreamPayload{
				{Kind: agentadaptor.StreamRunStarted, ThreadID: "t", RunID: "r"},
				{Kind: agentadaptor.StreamToolCallArgs, ToolCallID: "tc2", Name: "search", Delta: `{"q":`},
				{Kind: agentadaptor.StreamToolCallEnd, ToolCallID: "tc2"},
				{Kind: agentadaptor.StreamToolCallResult, ToolCallID: "tc2", Result: map[string]any{"status": "completed"}},
				{Kind: agentadaptor.StreamRunFinished, ThreadID: "t", RunID: "r"},
			},
			v1: []adaptor.Event{
				adaptor.RunStarted{RunID: "r", ThreadID: "t"},
				adaptor.ToolCall{ID: "tc2", Name: "search", ArgsDelta: `{"q":`},
				adaptor.ToolCall{ID: "tc2", Phase: adaptor.PhaseEnd},
				adaptor.ToolResult{ID: "tc2", Result: map[string]any{"status": "completed"}},
				adaptor.RunFinished{RunID: "r", ThreadID: "t"},
			},
		},
		{
			name: "pre-start buffering",
			legacy: []agentadaptor.StreamPayload{
				{Kind: agentadaptor.StreamTextContent, MessageID: "m3", Delta: "early"},
				{Kind: agentadaptor.StreamRunStarted, ThreadID: "t", RunID: "r"},
				{Kind: agentadaptor.StreamRunFinished, ThreadID: "t", RunID: "r"},
			},
			v1: []adaptor.Event{
				adaptor.TextDelta{MessageID: "m3", Text: "early"},
				adaptor.RunStarted{RunID: "r", ThreadID: "t"},
				adaptor.RunFinished{RunID: "r", ThreadID: "t"},
			},
		},
		{
			name: "steps",
			legacy: []agentadaptor.StreamPayload{
				{Kind: agentadaptor.StreamRunStarted, ThreadID: "t", RunID: "r"},
				{Kind: agentadaptor.StreamStepStarted, Name: "plan"},
				{Kind: agentadaptor.StreamStepFinished, Name: "plan"},
				{Kind: agentadaptor.StreamRunFinished, ThreadID: "t", RunID: "r"},
			},
			v1: []adaptor.Event{
				adaptor.RunStarted{RunID: "r", ThreadID: "t"},
				adaptor.Notice{Kind: adaptor.NoticeStep, Text: "plan", Data: map[string]any{"phase": "started"}},
				adaptor.Notice{Kind: adaptor.NoticeStep, Text: "plan", Data: map[string]any{"phase": "finished"}},
				adaptor.RunFinished{RunID: "r", ThreadID: "t"},
			},
		},
		{
			// Vocabulary caveat: only agent_error / cancelled share their
			// code string between the legacy FailureCode and the v1
			// FailureReason; approval codes diverge by design (P4 report).
			name: "run error",
			legacy: []agentadaptor.StreamPayload{
				{Kind: agentadaptor.StreamRunStarted, ThreadID: "t", RunID: "r"},
				{Kind: agentadaptor.StreamRunError, Error: &agentadaptor.RunFailure{Message: "boom", Code: driver.FailureAgentError}},
			},
			v1: []adaptor.Event{
				adaptor.RunStarted{RunID: "r", ThreadID: "t"},
				adaptor.RunFinished{RunID: "r", ThreadID: "t", Failed: true, Reason: adaptor.ReasonAgentError, Message: "boom"},
			},
		},
		{
			name: "terminal closes open lifecycles",
			legacy: []agentadaptor.StreamPayload{
				{Kind: agentadaptor.StreamRunStarted, ThreadID: "t", RunID: "r"},
				{Kind: agentadaptor.StreamTextStart, MessageID: "m4"},
				{Kind: agentadaptor.StreamTextContent, MessageID: "m4", Delta: "partial"},
				{Kind: agentadaptor.StreamRunFinished, ThreadID: "t", RunID: "r"},
			},
			v1: []adaptor.Event{
				adaptor.RunStarted{RunID: "r", ThreadID: "t"},
				adaptor.TextDelta{MessageID: "m4", Phase: adaptor.PhaseStart},
				adaptor.TextDelta{MessageID: "m4", Text: "partial"},
				adaptor.RunFinished{RunID: "r", ThreadID: "t"},
			},
		},
		{
			name: "dropped marker",
			legacy: []agentadaptor.StreamPayload{
				{Kind: agentadaptor.StreamRunStarted, ThreadID: "t", RunID: "r"},
				{Kind: agentadaptor.StreamDropped, Raw: map[string]any{"dropped_count": 2}},
				{Kind: agentadaptor.StreamRunFinished, ThreadID: "t", RunID: "r"},
			},
			v1: []adaptor.Event{
				adaptor.RunStarted{RunID: "r", ThreadID: "t"},
				adaptor.Dropped{Count: 2},
				adaptor.RunFinished{RunID: "r", ThreadID: "t"},
			},
		},
		{
			// The v1 side pairs the full-fidelity *ApprovalRequest event with
			// the resolved sink Notice; both projections must reproduce the
			// legacy dec-<id> tool-call frames key for key.
			name: "hitl tool-call projection",
			legacy: []agentadaptor.StreamPayload{
				{Kind: agentadaptor.StreamRunStarted, ThreadID: "t", RunID: "r"},
				{Kind: agentadaptor.StreamHITLRequested, HITLRequested: &agentadaptor.HITLRequestedPayload{
					RequestID:  "q1",
					Kind:       driver.HumanDecisionPlanReview,
					Source:     "claude.exit_plan_mode",
					ToolCallID: "tc9",
					Prompt:     "approve the plan?",
					Payload:    map[string]any{"plan": "ship the docs"},
					Choices:    hitlChoices,
					Deadline:   deadline,
				}},
				{Kind: agentadaptor.StreamHITLResolved, HITLResolved: &agentadaptor.HITLResolvedPayload{
					RequestID: "q1",
					Kind:      driver.HumanDecisionPlanReview,
					Source:    "claude.exit_plan_mode",
					Result:    driver.DecisionApproved,
					Latency:   1500 * time.Millisecond,
				}},
				{Kind: agentadaptor.StreamRunFinished, ThreadID: "t", RunID: "r"},
			},
			v1: []adaptor.Event{
				adaptor.RunStarted{RunID: "r", ThreadID: "t"},
				&adaptor.ApprovalRequest{
					ID:         "q1",
					RunID:      "r",
					Kind:       adaptor.ApprovalPlanReview,
					Title:      "approve the plan?",
					Source:     "claude.exit_plan_mode",
					ToolCallID: "tc9",
					Choices:    hitlChoices,
					Details:    map[string]any{"plan": "ship the docs"},
					Deadline:   deadline,
				},
				adaptor.Notice{Kind: adaptor.NoticeApprovalResolved, Data: map[string]any{
					"request_id": "q1",
					"kind":       string(adaptor.ApprovalPlanReview),
					"source":     "claude.exit_plan_mode",
					"result":     "approved",
					"choice":     "",
					"attempt":    0,
					"latency":    1500 * time.Millisecond,
				}},
				adaptor.RunFinished{RunID: "r", ThreadID: "t"},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			legacyEvents := translateAll(agui.NewTranslator(), tc.legacy)
			v1Events := translateAllEvents(agui.NewEventTranslator(), tc.v1)
			assertFrameEquivalence(t, legacyEvents, v1Events)
			assertVerified(t, v1Events)
		})
	}
}

// TestEventTranslatorCloseRunCodes anchors the v1 cancel / failure
// classification: stream.Result() errors map to the same closing codes the
// legacy Wrap synthesis used ("run.cancelled" / "run.error"), and business
// failures surface their typed FailureReason.
func TestEventTranslatorCloseRunCodes(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name        string
		err         error
		wantType    aguievents.EventType
		wantCode    string
		wantMessage string
	}{
		{
			name:     "nil error finishes the run",
			err:      nil,
			wantType: aguievents.EventTypeRunFinished,
		},
		{
			name:        "context cancellation",
			err:         errors.New("adaptor: run r1: context canceled"),
			wantType:    aguievents.EventTypeRunError,
			wantCode:    "run.error", // plain errors without the sentinel stay generic
			wantMessage: "adaptor: run r1: context canceled",
		},
		{
			name:        "wrapped context.Canceled",
			err:         &wrapErr{msg: "adaptor: run r1: context canceled", inner: context.Canceled},
			wantType:    aguievents.EventTypeRunError,
			wantCode:    "run.cancelled",
			wantMessage: "adaptor: run r1: context canceled",
		},
		{
			name:        "typed RunError carries its reason",
			err:         &adaptor.RunError{Reason: adaptor.ReasonApprovalDenied, Message: "operator rejected"},
			wantType:    aguievents.EventTypeRunError,
			wantCode:    string(adaptor.ReasonApprovalDenied),
			wantMessage: "operator rejected",
		},
		{
			name:        "infrastructure error",
			err:         errors.New("write: broken pipe"),
			wantType:    aguievents.EventTypeRunError,
			wantCode:    "run.error",
			wantMessage: "write: broken pipe",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tr := agui.NewEventTranslator()
			_ = tr.Translate(adaptor.RunStarted{RunID: "r1", ThreadID: "t1"})

			events := tr.CloseRun(tc.err)
			if len(events) == 0 {
				t.Fatal("CloseRun produced no events")
			}
			last := events[len(events)-1]
			if last.Type() != tc.wantType {
				t.Fatalf("terminal type = %s, want %s", last.Type(), tc.wantType)
			}
			if tc.wantType == aguievents.EventTypeRunError {
				re, ok := last.(*aguievents.RunErrorEvent)
				if !ok {
					t.Fatalf("terminal event is %T, want *RunErrorEvent", last)
				}
				code := ""
				if re.Code != nil {
					code = *re.Code
				}
				if code != tc.wantCode {
					t.Errorf("code = %q, want %q", code, tc.wantCode)
				}
				if re.Message != tc.wantMessage {
					t.Errorf("message = %q, want %q", re.Message, tc.wantMessage)
				}
				if re.RunIDValue != "r1" {
					t.Errorf("runId = %q, want %q", re.RunIDValue, "r1")
				}
			}

			// Terminal latch: a second close (or any further traffic) is
			// suppressed.
			if extra := tr.CloseRun(tc.err); len(extra) != 0 {
				t.Errorf("second CloseRun emitted %v", typesOf(extra))
			}
			if extra := tr.Translate(adaptor.TextDelta{MessageID: "late", Text: "x"}); len(extra) != 0 {
				t.Errorf("post-terminal Translate emitted %v", typesOf(extra))
			}
		})
	}
}

// wrapErr wraps an inner error without using fmt.Errorf so the test controls
// the exact message.
type wrapErr struct {
	msg   string
	inner error
}

func (e *wrapErr) Error() string { return e.msg }
func (e *wrapErr) Unwrap() error { return e.inner }

// TestEventTranslatorDegradationWarningVisible anchors the capability
// degradation semantics: the run-policy retry-unsupported warning (emitted as
// a lifecycle Notice by the v1 sink) must reach the AG-UI wire as a CUSTOM
// event, exactly as the legacy lifecycle RunEvent projection did.
func TestEventTranslatorDegradationWarningVisible(t *testing.T) {
	t.Parallel()
	tr := agui.NewEventTranslator()
	_ = tr.Translate(adaptor.RunStarted{RunID: "r", ThreadID: "t"})

	events := tr.Translate(adaptor.Notice{
		Kind: adaptor.NoticeLifecycle,
		Text: "approval plan_review does not support retry; degrading to abort",
		Data: map[string]any{
			"kind":    "plan_review",
			"action":  "retry",
			"warning": "human_decision_retry_unsupported",
		},
	})
	if len(events) != 1 {
		t.Fatalf("got %d events (%v), want 1 CUSTOM", len(events), typesOf(events))
	}
	custom, ok := events[0].(*aguievents.CustomEvent)
	if !ok {
		t.Fatalf("event is %T, want *CustomEvent", events[0])
	}
	if custom.Name != adaptor.NoticeLifecycle {
		t.Errorf("custom name = %q, want %q", custom.Name, adaptor.NoticeLifecycle)
	}
	value, ok := custom.Value.(map[string]any)
	if !ok {
		t.Fatalf("custom value is %T, want map", custom.Value)
	}
	if value["warning"] != "human_decision_retry_unsupported" {
		t.Errorf("value[warning] = %v, want human_decision_retry_unsupported", value["warning"])
	}
	if value["text"] != "approval plan_review does not support retry; degrading to abort" {
		t.Errorf("value[text] = %v, want the degradation message", value["text"])
	}
}

// TestEventTranslatorDecisionAsCustom covers the legacy CUSTOM projection
// mode on the v1 input vocabulary.
func TestEventTranslatorDecisionAsCustom(t *testing.T) {
	t.Parallel()
	tr := agui.NewEventTranslator(agui.WithEventDecisionMode(agui.DecisionAsCustom))
	_ = tr.Translate(adaptor.RunStarted{RunID: "r", ThreadID: "t"})

	events := tr.Translate(&adaptor.ApprovalRequest{
		ID:     "q9",
		Kind:   adaptor.ApprovalPermission,
		Source: "bash",
		Title:  "run ls?",
	})
	if len(events) != 1 {
		t.Fatalf("got %d events (%v), want 1 CUSTOM", len(events), typesOf(events))
	}
	custom, ok := events[0].(*aguievents.CustomEvent)
	if !ok {
		t.Fatalf("event is %T, want *CustomEvent", events[0])
	}
	if custom.Name != "hitl.requested" {
		t.Errorf("custom name = %q, want hitl.requested", custom.Name)
	}
	value, ok := custom.Value.(map[string]any)
	if !ok {
		t.Fatalf("custom value is %T, want map", custom.Value)
	}
	if value["request_id"] != "q9" {
		t.Errorf("value[request_id] = %v, want q9", value["request_id"])
	}
}

// fakeEventDriver is a minimal scripted driver.Driver for the Events()
// end-to-end tests.
type fakeEventDriver struct {
	payloads []driver.StreamPayload
	response driver.Response
}

func (f *fakeEventDriver) Descriptor() driver.Descriptor {
	return driver.Descriptor{Type: "fake", DisplayName: "Fake"}
}

func (f *fakeEventDriver) ValidateConfig(any) error { return nil }

func (f *fakeEventDriver) Run(_ context.Context, _ driver.Request, sink driver.EventSink) (driver.Response, error) {
	for _, p := range f.payloads {
		if err := sink.EmitStream(p); err != nil {
			return driver.Response{}, err
		}
	}
	return f.response, nil
}

// TestEventsStreamEndToEnd drives agui.Events over a real v1 Stream: the
// bridge must open with RUN_STARTED, forward content, and synthesize the
// closing RUN_FINISHED from stream.Result() when the producer never emitted a
// terminal marker (legacy Wrap contract).
func TestEventsStreamEndToEnd(t *testing.T) {
	t.Parallel()
	fake := &fakeEventDriver{
		payloads: []driver.StreamPayload{
			{Kind: driver.StreamRunStarted, ThreadID: "t", RunID: "r"},
			{Kind: driver.StreamTextContent, MessageID: "m1", Delta: "hello"},
		},
		response: driver.Response{Output: "hello"},
	}
	agent := adaptor.New(fake)
	stream := agent.Stream(context.Background(), "hi")

	var events []aguievents.Event
	for ev := range agui.Events(stream) {
		events = append(events, ev)
	}
	if len(events) < 3 {
		t.Fatalf("got %d events (%v), want at least RUN_STARTED..RUN_FINISHED", len(events), typesOf(events))
	}
	if events[0].Type() != aguievents.EventTypeRunStarted {
		t.Errorf("first event = %s, want RUN_STARTED", events[0].Type())
	}
	if events[len(events)-1].Type() != aguievents.EventTypeRunFinished {
		t.Errorf("last event = %s, want RUN_FINISHED", events[len(events)-1].Type())
	}
	sawContent := false
	for _, ev := range events {
		if ev.Type() == aguievents.EventTypeTextMessageContent {
			sawContent = true
		}
	}
	if !sawContent {
		t.Errorf("no TEXT_MESSAGE_CONTENT in %v", typesOf(events))
	}
	assertVerified(t, events)
}

// TestEventsStreamEndToEndBusinessFailure: a driver-classified failure must
// end the AG-UI stream with exactly one RUN_ERROR carrying the typed reason.
func TestEventsStreamEndToEndBusinessFailure(t *testing.T) {
	t.Parallel()
	fake := &fakeEventDriver{
		response: driver.Response{
			Output:  "partial",
			Failure: &driver.RunFailure{Code: driver.FailureAgentError, Message: "boom"},
		},
	}
	agent := adaptor.New(fake)
	stream := agent.Stream(context.Background(), "hi")

	var events []aguievents.Event
	for ev := range agui.Events(stream) {
		events = append(events, ev)
	}
	if len(events) == 0 {
		t.Fatal("no events")
	}
	last, ok := events[len(events)-1].(*aguievents.RunErrorEvent)
	if !ok {
		t.Fatalf("last event is %T (%v), want *RunErrorEvent", events[len(events)-1], typesOf(events))
	}
	if last.Code == nil || *last.Code != string(adaptor.ReasonAgentError) {
		t.Errorf("code = %v, want %q", last.Code, adaptor.ReasonAgentError)
	}
	errorCount := 0
	for _, ev := range events {
		if ev.Type() == aguievents.EventTypeRunError {
			errorCount++
		}
	}
	if errorCount != 1 {
		t.Errorf("RUN_ERROR count = %d, want exactly 1 (%v)", errorCount, typesOf(events))
	}
	assertVerified(t, events)
}
