package agui_test

// Contract tests for the adaptor.Event to AG-UI state machine.

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	aguievents "github.com/ag-ui-protocol/ag-ui/sdks/community/go/pkg/core/events"

	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/bridges/agui"
	"github.com/agent-dance/agent-adaptor/driver"
)

func typesOf(events []aguievents.Event) []aguievents.EventType {
	out := make([]aguievents.EventType, len(events))
	for i, event := range events {
		out[i] = event.Type()
	}
	return out
}

func assertVerified(t *testing.T, events []aguievents.Event) {
	t.Helper()
	if err := agui.VerifySequence(events); err != nil {
		t.Fatalf("AG-UI sequence verification failed: %v\nevents=%v", err, typesOf(events))
	}
}

func textContents(events []aguievents.Event) []string {
	var out []string
	for _, event := range events {
		if content, ok := event.(*aguievents.TextMessageContentEvent); ok {
			out = append(out, content.Delta)
		}
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

// TestEventTranslatorCloseRunCodes anchors v1 cancel/failure classification
// and typed FailureReason projection.
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

func TestEventTranslatorCloseResultTextFallback(t *testing.T) {
	t.Run("cursor-like result without deltas", func(t *testing.T) {
		tr := agui.NewEventTranslator()
		events := tr.Translate(adaptor.RunStarted{RunID: "cursor-run", ThreadID: "thread"})
		events = append(events, tr.CloseResult(&adaptor.Result{Text: "cursor final answer"}, nil)...)

		if got := textContents(events); !reflect.DeepEqual(got, []string{"cursor final answer"}) {
			t.Fatalf("assistant contents = %v, want one result fallback", got)
		}
		if got := typesOf(events); !reflect.DeepEqual(got, []aguievents.EventType{
			aguievents.EventTypeRunStarted,
			aguievents.EventTypeTextMessageStart,
			aguievents.EventTypeTextMessageContent,
			aguievents.EventTypeTextMessageEnd,
			aguievents.EventTypeRunFinished,
		}) {
			t.Fatalf("event order = %v", got)
		}
		assertVerified(t, events)
	})

	t.Run("existing assistant delta is not duplicated", func(t *testing.T) {
		tr := agui.NewEventTranslator()
		events := tr.Translate(adaptor.RunStarted{RunID: "streaming-run", ThreadID: "thread"})
		events = append(events, tr.Translate(adaptor.TextDelta{MessageID: "assistant", Text: "streamed answer"})...)
		events = append(events, tr.CloseResult(&adaptor.Result{Text: "streamed answer"}, nil)...)

		if got := textContents(events); !reflect.DeepEqual(got, []string{"streamed answer"}) {
			t.Fatalf("assistant contents = %v, want streamed content exactly once", got)
		}
		if events[len(events)-1].Type() != aguievents.EventTypeRunFinished {
			t.Fatalf("last event = %s, want RUN_FINISHED", events[len(events)-1].Type())
		}
		assertVerified(t, events)
	})

	t.Run("empty result creates no assistant bubble", func(t *testing.T) {
		tr := agui.NewEventTranslator()
		events := tr.CloseResult(&adaptor.Result{Text: " \n\t"}, nil)
		if got := textContents(events); len(got) != 0 {
			t.Fatalf("assistant contents = %v, want none", got)
		}
		if got := typesOf(events); !reflect.DeepEqual(got, []aguievents.EventType{
			aguievents.EventTypeRunStarted, aguievents.EventTypeRunFinished,
		}) {
			t.Fatalf("event order = %v", got)
		}
		assertVerified(t, events)
	})

	t.Run("user text does not suppress assistant fallback", func(t *testing.T) {
		tr := agui.NewEventTranslator()
		events := tr.Translate(adaptor.RunStarted{RunID: "user-run", ThreadID: "thread"})
		events = append(events, tr.Translate(adaptor.TextDelta{MessageID: "user", Role: adaptor.RoleUser, Text: "question"})...)
		events = append(events, tr.CloseResult(&adaptor.Result{Text: "answer"}, nil)...)
		if got := textContents(events); !reflect.DeepEqual(got, []string{"question", "answer"}) {
			t.Fatalf("text contents = %v, want user text plus assistant fallback", got)
		}
		assertVerified(t, events)
	})

	t.Run("business failure keeps partial text before run error", func(t *testing.T) {
		tr := agui.NewEventTranslator()
		runErr := &adaptor.RunError{
			Reason:  adaptor.ReasonAgentError,
			Message: "provider failed",
			Result:  &adaptor.Result{Text: "partial answer"},
		}
		events := tr.CloseResult(nil, runErr)
		if got := textContents(events); !reflect.DeepEqual(got, []string{"partial answer"}) {
			t.Fatalf("assistant contents = %v, want partial result text", got)
		}
		if events[len(events)-1].Type() != aguievents.EventTypeRunError {
			t.Fatalf("last event = %s, want RUN_ERROR", events[len(events)-1].Type())
		}
		assertVerified(t, events)
	})
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
// a lifecycle Notice by the sink) must reach the AG-UI wire as a CUSTOM
// event without being dropped.
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

// TestEventTranslatorDecisionAsCustom covers the explicit CUSTOM projection.
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
// terminal marker (the bridge's end-of-stream contract).
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

func TestEventsStreamSynthesizesNonStreamingResultText(t *testing.T) {
	t.Parallel()
	fake := &fakeEventDriver{
		payloads: []driver.StreamPayload{{Kind: driver.StreamRunStarted, ThreadID: "t", RunID: "cursor-run"}},
		response: driver.Response{Output: "cursor final answer"},
	}
	stream := adaptor.New(fake).Stream(context.Background(), "hi")

	var events []aguievents.Event
	for event := range agui.Events(stream) {
		events = append(events, event)
	}
	if got := textContents(events); !reflect.DeepEqual(got, []string{"cursor final answer"}) {
		t.Fatalf("assistant contents = %v, want one final-result fallback", got)
	}
	if events[len(events)-1].Type() != aguievents.EventTypeRunFinished {
		t.Fatalf("last event = %s, want RUN_FINISHED", events[len(events)-1].Type())
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
	if got := textContents(events); !reflect.DeepEqual(got, []string{"partial"}) {
		t.Errorf("partial assistant contents = %v, want one result fallback", got)
	}
	assertVerified(t, events)
}

func TestEventTranslatorPreservesCompleteToolSnapshots(t *testing.T) {
	tr := agui.NewEventTranslator()
	_ = tr.Translate(adaptor.RunStarted{RunID: "run", ThreadID: "thread"})
	events := tr.Translate(adaptor.ToolCall{
		ID: "tool", Name: "shell", Phase: adaptor.PhaseStart,
		Args: map[string]any{"cmd": "go test", "count": 2},
	})
	if len(events) != 2 || events[0].Type() != aguievents.EventTypeToolCallStart || events[1].Type() != aguievents.EventTypeToolCallArgs {
		t.Fatalf("start snapshot = %v", typesOf(events))
	}
	args := events[1].(*aguievents.ToolCallArgsEvent)
	var decoded map[string]any
	if err := json.Unmarshal([]byte(args.Delta), &decoded); err != nil || decoded["cmd"] != "go test" {
		t.Fatalf("args delta = %q, decoded=%#v err=%v", args.Delta, decoded, err)
	}

	events = tr.Translate(adaptor.ToolCall{
		ID: "tool", Phase: adaptor.PhaseEnd,
		Result: map[string]any{"output": "ok", "exitCode": 0},
	})
	if len(events) != 2 || events[0].Type() != aguievents.EventTypeToolCallEnd || events[1].Type() != aguievents.EventTypeToolCallResult {
		t.Fatalf("end snapshot = %v", typesOf(events))
	}
	if got := events[1].(*aguievents.ToolCallResultEvent).Content; got != "ok\n[exit=0]" {
		t.Fatalf("result content = %q", got)
	}

	// A provider may expose only its completed snapshot. The bridge keeps
	// the AG-UI lifecycle valid and does not discard the attached result.
	tr = agui.NewEventTranslator()
	_ = tr.Translate(adaptor.RunStarted{RunID: "run", ThreadID: "thread"})
	events = tr.Translate(adaptor.ToolCall{ID: "end-only", Name: "search", Phase: adaptor.PhaseEnd, Result: map[string]any{"text": "found"}})
	if got := typesOf(events); !reflect.DeepEqual(got, []aguievents.EventType{
		aguievents.EventTypeToolCallStart, aguievents.EventTypeToolCallEnd, aguievents.EventTypeToolCallResult,
	}) {
		t.Fatalf("end-only result lifecycle = %v", got)
	}
}

func TestEventTranslatorResultOverridesInformationalRunFinished(t *testing.T) {
	fake := &fakeEventDriver{
		payloads: []driver.StreamPayload{{Kind: driver.StreamRunFinished, RunID: "driver-run", ThreadID: "thread"}},
		response: driver.Response{Failure: &driver.RunFailure{Code: driver.FailureAgentError, Message: "authoritative failure"}},
	}
	stream := adaptor.New(fake).Stream(context.Background(), "prompt")
	var events []aguievents.Event
	for event := range agui.Events(stream) {
		events = append(events, event)
	}
	finished, failed := 0, 0
	for _, event := range events {
		switch event.Type() {
		case aguievents.EventTypeRunFinished:
			finished++
		case aguievents.EventTypeRunError:
			failed++
		}
	}
	if finished != 0 || failed != 1 {
		t.Fatalf("terminal counts finished=%d failed=%d events=%v", finished, failed, typesOf(events))
	}
}

type cancellationStream struct {
	events    chan adaptor.Event
	cancelled chan struct{}
	once      sync.Once
}

func (s *cancellationStream) Events() <-chan adaptor.Event     { return s.events }
func (s *cancellationStream) Result() (*adaptor.Result, error) { return nil, context.Canceled }
func (s *cancellationStream) RunID() string                    { return "run" }
func (s *cancellationStream) Cancel()                          { s.once.Do(func() { close(s.cancelled) }) }

func TestEventsContextCancellationUnblocksFanout(t *testing.T) {
	stream := &cancellationStream{events: make(chan adaptor.Event), cancelled: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	out := agui.EventsContext(ctx, stream)
	cancel()
	select {
	case <-stream.cancelled:
	case <-time.After(time.Second):
		t.Fatal("context cancellation did not cancel stream")
	}
	select {
	case _, ok := <-out:
		if ok {
			t.Fatal("fanout produced an event after cancellation")
		}
	case <-time.After(time.Second):
		t.Fatal("fanout channel did not close")
	}
}

func TestEventTranslatorClosesLifecyclesDeterministically(t *testing.T) {
	var baseline []string
	for iteration := 0; iteration < 20; iteration++ {
		tr := agui.NewEventTranslator()
		_ = tr.Translate(adaptor.RunStarted{RunID: "run", ThreadID: "thread"})
		_ = tr.Translate(adaptor.TextDelta{MessageID: "z", Text: "z"})
		_ = tr.Translate(adaptor.TextDelta{MessageID: "a", Text: "a"})
		_ = tr.Translate(adaptor.Thinking{MessageID: "y", Text: "y"})
		_ = tr.Translate(adaptor.Thinking{MessageID: "b", Text: "b"})
		_ = tr.Translate(adaptor.ToolCall{ID: "x", Name: "x", Phase: adaptor.PhaseStart})
		_ = tr.Translate(adaptor.ToolCall{ID: "c", Name: "c", Phase: adaptor.PhaseStart})
		closed := tr.CloseRun(nil)
		wire := make([]string, 0, len(closed))
		for _, event := range closed {
			raw, _ := json.Marshal(frameMap(t, event))
			wire = append(wire, string(raw))
		}
		if iteration == 0 {
			baseline = wire
		} else if !reflect.DeepEqual(wire, baseline) {
			t.Fatalf("iteration %d lifecycle close order differs\nfirst=%v\ngot=%v", iteration, baseline, wire)
		}
	}
}
