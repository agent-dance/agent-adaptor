package adaptor_test

// Contract tests for the unified Event stream.
//
// The unified stream contract requires:
//   - every driver.RunEvent type and every driver.StreamPayload kind has a
//     typed destination on the one Events() channel — no information loss;
//   - Events() closes when the run ends and Result() is then immediately
//     available (channel-close timing);
//   - Run ≡ Stream + drain + Result() (single execution path);
//   - Cancel() ends the run as an infrastructure cancellation (D1).

import (
	"context"
	"errors"
	"testing"

	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/driver"
)

// collect drains the stream and returns all events plus the final outcome.
func collect(st adaptor.Stream) ([]adaptor.Event, *adaptor.Result, error) {
	var events []adaptor.Event
	for ev := range st.Events() {
		events = append(events, ev)
	}
	res, err := st.Result()
	return events, res, err
}

// TestStreamTranslationCoversAllKinds drives every RunEvent type and every
// StreamPayload kind through the sink and asserts the typed destination of
// each — the StreamKind mapping table as an executable contract.
func TestStreamTranslationCoversAllKinds(t *testing.T) {
	fake := newFakeDriver()
	fake.streamCaps = driver.StreamCapability{Native: true, TokenLevel: true}
	fake.runFunc = func(ctx context.Context, req driver.Request, sink driver.EventSink) (driver.Response, error) {
		if !req.Streaming {
			t.Error("rich driver capability must select provider streaming")
		}
		// Operational RunEvents — all 6 types plus an unknown extension.
		_ = sink.Emit(driver.RunEvent{Type: driver.RunEventSpawn, Text: "codex --json"})
		_ = sink.Emit(driver.RunEvent{Type: driver.RunEventChunk, Stream: "stdout", Bytes: []byte("out")})
		_ = sink.Emit(driver.RunEvent{Type: driver.RunEventChunk, Stream: "stderr", Bytes: []byte("err")})
		_ = sink.Emit(driver.RunEvent{Type: driver.RunEventItem, Item: &driver.TranscriptItem{Text: "did a thing"}})
		_ = sink.Emit(driver.RunEvent{Type: driver.RunEventInvocation, Text: "invoked"})
		_ = sink.Emit(driver.RunEvent{Type: driver.RunEventRuntime, Text: "mcp up"})
		_ = sink.Emit(driver.RunEvent{Type: driver.RunEventLifecycle, Text: "run started"})
		_ = sink.Emit(driver.RunEvent{Type: driver.RunEventType("custom.op"), Text: "vendor"})

		// Semantic StreamPayloads — all 18 kinds plus an unknown extension.
		emit := func(p driver.StreamPayload) { _ = sink.EmitStream(p) }
		emit(driver.StreamPayload{Kind: driver.StreamRunStarted, RunID: "prov-run", ThreadID: "th-1"})
		emit(driver.StreamPayload{Kind: driver.StreamStepStarted, Name: "plan"})
		emit(driver.StreamPayload{Kind: driver.StreamStepFinished, Name: "plan"})
		emit(driver.StreamPayload{Kind: driver.StreamTextStart, MessageID: "m1"})
		emit(driver.StreamPayload{Kind: driver.StreamTextContent, MessageID: "m1", Delta: "hello"})
		emit(driver.StreamPayload{Kind: driver.StreamTextEnd, MessageID: "m1"})
		emit(driver.StreamPayload{Kind: driver.StreamReasoningStart, MessageID: "m1"})
		emit(driver.StreamPayload{Kind: driver.StreamReasoningContent, MessageID: "m1", Delta: "hmm"})
		emit(driver.StreamPayload{Kind: driver.StreamReasoningEnd, MessageID: "m1"})
		emit(driver.StreamPayload{Kind: driver.StreamToolCallStart, ToolCallID: "c1", Name: "bash", Args: map[string]any{"cmd": "ls"}})
		emit(driver.StreamPayload{Kind: driver.StreamToolCallArgs, ToolCallID: "c1", Delta: `{"cmd":`})
		emit(driver.StreamPayload{Kind: driver.StreamToolCallEnd, ToolCallID: "c1", Result: map[string]any{"exit": 0}})
		emit(driver.StreamPayload{Kind: driver.StreamToolCallResult, ToolCallID: "c1", Result: map[string]any{"stdout": "files"}})
		emit(driver.StreamPayload{Kind: driver.StreamHITLRequested, HITLRequested: &driver.HITLRequestedPayload{RequestID: "d1", Kind: driver.HumanDecisionPermission, Source: "tool:bash", Prompt: "allow?"}})
		emit(driver.StreamPayload{Kind: driver.StreamHITLResolved, HITLResolved: &driver.HITLResolvedPayload{RequestID: "d1", Kind: driver.HumanDecisionPermission, Result: driver.DecisionApproved}})
		emit(driver.StreamPayload{Kind: driver.StreamDropped, Raw: map[string]any{"dropped_count": 7}})
		emit(driver.StreamPayload{Kind: driver.StreamKind("x.vendor"), Delta: "custom", Raw: map[string]any{"k": "v"}})
		emit(driver.StreamPayload{Kind: driver.StreamRunFinished, RunID: "prov-run", Usage: &driver.Usage{OutputTokens: 9}})

		return driver.Response{Output: "done"}, nil
	}

	agent := adaptor.New(fake)
	events, res, err := collect(agent.Stream(context.Background(), "map everything"))
	if err != nil {
		t.Fatalf("Result: %v", err)
	}
	if res.Text != "done" {
		t.Fatalf("res.Text = %q", res.Text)
	}

	i := 0
	next := func() adaptor.Event {
		if i >= len(events) {
			t.Fatalf("event stream ended early at index %d (got %d events)", i, len(events))
		}
		ev := events[i]
		i++
		return ev
	}

	// --- RunEvent translations ---
	if e, ok := next().(adaptor.ProcessInfo); !ok || e.Kind != adaptor.ProcessSpawn || e.Text != "codex --json" {
		t.Errorf("spawn → ProcessInfo{spawn}, got %#v", events[i-1])
	}
	if e, ok := next().(adaptor.ProcessInfo); !ok || e.Kind != adaptor.ProcessStdout || string(e.Bytes) != "out" {
		t.Errorf("chunk(stdout) → ProcessInfo{stdout}, got %#v", events[i-1])
	}
	if e, ok := next().(adaptor.ProcessInfo); !ok || e.Kind != adaptor.ProcessStderr || string(e.Bytes) != "err" {
		t.Errorf("chunk(stderr) → ProcessInfo{stderr}, got %#v", events[i-1])
	}
	if e, ok := next().(adaptor.Notice); !ok || e.Kind != adaptor.NoticeTranscriptItem || e.Item == nil || e.Text != "did a thing" {
		t.Errorf("item → Notice{transcript.item}, got %#v", events[i-1])
	}
	if e, ok := next().(adaptor.Notice); !ok || e.Kind != adaptor.NoticeInvocation {
		t.Errorf("invocation → Notice{invocation}, got %#v", events[i-1])
	}
	if e, ok := next().(adaptor.Notice); !ok || e.Kind != adaptor.NoticeRuntime {
		t.Errorf("runtime → Notice{runtime}, got %#v", events[i-1])
	}
	if e, ok := next().(adaptor.Notice); !ok || e.Kind != adaptor.NoticeLifecycle {
		t.Errorf("lifecycle → Notice{lifecycle}, got %#v", events[i-1])
	}
	if e, ok := next().(adaptor.Notice); !ok || e.Kind != "custom.op" || e.Text != "vendor" {
		t.Errorf("unknown RunEvent type must pass through as Notice, got %#v", events[i-1])
	}

	// --- StreamPayload translations ---
	if e, ok := next().(adaptor.RunStarted); !ok || e.RunID != "prov-run" || e.ThreadID != "th-1" {
		t.Errorf("run.started → RunStarted, got %#v", events[i-1])
	}
	if e, ok := next().(adaptor.Notice); !ok || e.Kind != adaptor.NoticeStep || e.Text != "plan" || e.Data["phase"] != "started" {
		t.Errorf("step.started → Notice{step/started}, got %#v", events[i-1])
	}
	if e, ok := next().(adaptor.Notice); !ok || e.Kind != adaptor.NoticeStep || e.Data["phase"] != "finished" {
		t.Errorf("step.finished → Notice{step/finished}, got %#v", events[i-1])
	}
	if e, ok := next().(adaptor.TextDelta); !ok || e.Phase != adaptor.PhaseStart || e.MessageID != "m1" {
		t.Errorf("text.start → TextDelta{PhaseStart}, got %#v", events[i-1])
	}
	if e, ok := next().(adaptor.TextDelta); !ok || e.Phase != adaptor.PhaseContent || e.Text != "hello" {
		t.Errorf("text.content → TextDelta{content}, got %#v", events[i-1])
	}
	if e, ok := next().(adaptor.TextDelta); !ok || e.Phase != adaptor.PhaseEnd {
		t.Errorf("text.end → TextDelta{PhaseEnd}, got %#v", events[i-1])
	}
	if e, ok := next().(adaptor.Thinking); !ok || e.Phase != adaptor.PhaseStart {
		t.Errorf("reasoning.start → Thinking{PhaseStart}, got %#v", events[i-1])
	}
	if e, ok := next().(adaptor.Thinking); !ok || e.Text != "hmm" || e.Phase != adaptor.PhaseContent {
		t.Errorf("reasoning.content → Thinking{content}, got %#v", events[i-1])
	}
	if e, ok := next().(adaptor.Thinking); !ok || e.Phase != adaptor.PhaseEnd {
		t.Errorf("reasoning.end → Thinking{PhaseEnd}, got %#v", events[i-1])
	}
	if e, ok := next().(adaptor.ToolCall); !ok || e.Phase != adaptor.PhaseStart || e.ID != "c1" || e.Name != "bash" || e.Args["cmd"] != "ls" {
		t.Errorf("tool_call.start → ToolCall{PhaseStart}, got %#v", events[i-1])
	}
	if e, ok := next().(adaptor.ToolCall); !ok || e.ArgsDelta != `{"cmd":` || e.Phase != adaptor.PhaseContent {
		t.Errorf("tool_call.args → ToolCall{ArgsDelta}, got %#v", events[i-1])
	}
	if e, ok := next().(adaptor.ToolCall); !ok || e.Phase != adaptor.PhaseEnd || e.Result["exit"] != 0 {
		t.Errorf("tool_call.end → ToolCall{PhaseEnd,Result}, got %#v", events[i-1])
	}
	if e, ok := next().(adaptor.ToolResult); !ok || e.ID != "c1" || e.Result["stdout"] != "files" {
		t.Errorf("tool_call.result → ToolResult, got %#v", events[i-1])
	}
	if e, ok := next().(adaptor.Notice); !ok || e.Kind != adaptor.NoticeApprovalRequested || e.Data["request_id"] != "d1" || e.Text != "allow?" {
		t.Errorf("hitl.requested (driver broadcast) → Notice{approval.requested}, got %#v", events[i-1])
	}
	if e, ok := next().(adaptor.Notice); !ok || e.Kind != adaptor.NoticeApprovalResolved || e.Data["result"] != string(driver.DecisionApproved) {
		t.Errorf("hitl.resolved → Notice{approval.resolved}, got %#v", events[i-1])
	}
	if e, ok := next().(adaptor.Dropped); !ok || e.Count != 7 {
		t.Errorf("stream.dropped → Dropped{7}, got %#v", events[i-1])
	}
	if e, ok := next().(adaptor.Notice); !ok || e.Kind != "x.vendor" || e.Text != "custom" || e.Data["k"] != "v" {
		t.Errorf("unknown StreamKind must pass through as Notice, got %#v", events[i-1])
	}
	if e, ok := next().(adaptor.RunFinished); !ok || e.Failed || e.Usage == nil || e.Usage.OutputTokens != 9 {
		t.Errorf("run.finished → RunFinished{Usage}, got %#v", events[i-1])
	}
	if i != len(events) {
		t.Errorf("unexpected trailing events: %#v", events[i:])
	}

	// RunError and RunFinished are alternative terminal kinds; exercising
	// both in one run would violate the one-terminal lifecycle contract and
	// should now be rejected by the broker seal. Cover RunError in its own
	// valid lifecycle instead.
	errorFake := newFakeDriver()
	errorFake.runFunc = func(_ context.Context, req driver.Request, sink driver.EventSink) (driver.Response, error) {
		failure := &driver.RunFailure{Code: driver.FailureAgentError, Message: "boom"}
		_ = sink.EmitStream(driver.StreamPayload{Kind: driver.StreamRunStarted, RunID: req.RunID})
		_ = sink.EmitStream(driver.StreamPayload{Kind: driver.StreamRunError, RunID: req.RunID, Error: failure})
		return driver.Response{Failure: failure}, nil
	}
	errorEvents, _, runErr := collect(adaptor.New(errorFake).Stream(context.Background(), "map run error"))
	if runErr == nil {
		t.Fatal("RunError mapping fixture must finish with a business error")
	}
	if len(errorEvents) != 2 {
		t.Fatalf("RunError events = %#v, want RunStarted and RunFinished", errorEvents)
	}
	if e, ok := errorEvents[1].(adaptor.RunFinished); !ok || !e.Failed || e.Reason != adaptor.ReasonAgentError || e.Message != "boom" {
		t.Errorf("run.error → RunFinished{Failed}, got %#v", errorEvents[1])
	}
}

// TestStreamCloseTiming pins the channel-close contract: RunID is available
// before the first event, Events() closes when the run ends, and Result()
// then returns without any further consumer action.
func TestStreamCloseTiming(t *testing.T) {
	fake := newFakeDriver()
	fake.runFunc = func(ctx context.Context, req driver.Request, sink driver.EventSink) (driver.Response, error) {
		_ = sink.EmitStream(driver.StreamPayload{Kind: driver.StreamTextContent, Delta: "a"})
		_ = sink.EmitStream(driver.StreamPayload{Kind: driver.StreamTextContent, Delta: "b"})
		return driver.Response{Output: "final"}, nil
	}
	agent := adaptor.New(fake)

	st := agent.Stream(context.Background(), "go")
	if st.RunID() == "" {
		t.Fatal("RunID must be available immediately")
	}

	var text string
	for ev := range st.Events() {
		if d, ok := ev.(adaptor.TextDelta); ok {
			text += d.Text
		}
	}
	// The channel is closed — the outcome must already be decided.
	res, err := st.Result()
	if err != nil {
		t.Fatalf("Result after drain: %v", err)
	}
	if text != "ab" {
		t.Errorf("streamed text = %q, want ab", text)
	}
	if res.Text != "final" {
		t.Errorf("res.Text = %q", res.Text)
	}
	if res.RunID != st.RunID() {
		t.Errorf("res.RunID %q != st.RunID() %q", res.RunID, st.RunID())
	}
	// Result is repeatable from any goroutine.
	res2, err2 := st.Result()
	if err2 != nil || res2 != res {
		t.Errorf("second Result() = (%v, %v), want same outcome", res2, err2)
	}
}

// TestRunIsStreamPlusDrain proves Run rides the same pipeline: an
// event-emitting driver behaves identically under Run, and the business
// failure contract (D1) is unchanged.
func TestRunIsStreamPlusDrain(t *testing.T) {
	fake := newFakeDriver()
	fake.streamCaps = driver.StreamCapability{Native: true}
	fake.runFunc = func(ctx context.Context, req driver.Request, sink driver.EventSink) (driver.Response, error) {
		if !req.Streaming {
			t.Error("provider transport must be independent of the consumer Run method")
		}
		_ = sink.EmitStream(driver.StreamPayload{Kind: driver.StreamTextContent, Delta: "ignored"})
		return driver.Response{
			Output:  "partial output before failure",
			Failure: &driver.RunFailure{Code: driver.FailureReject, Message: "operator rejected"},
		}, nil
	}
	agent := adaptor.New(fake)

	res, err := agent.Run(context.Background(), "do it")
	if res != nil {
		t.Fatalf("failed run must return nil Result on the success slot, got %+v", res)
	}
	var runErr *adaptor.RunError
	if !errors.As(err, &runErr) {
		t.Fatalf("want *RunError, got %T: %v", err, err)
	}
	if !errors.Is(err, adaptor.ErrApprovalDenied) {
		t.Errorf("errors.Is(ErrApprovalDenied) must hold, got %v", err)
	}
	if runErr.Result == nil || runErr.Result.Text != "partial output before failure" {
		t.Errorf("RunError.Result must carry the partial output, got %+v", runErr.Result)
	}
}

// TestStreamCancel pins Cancel(): the run ends, Events() closes, and the
// outcome is a plain infrastructure cancellation (not a *RunError).
func TestStreamCancel(t *testing.T) {
	fake := newFakeDriver()
	started := make(chan struct{})
	fake.runFunc = func(ctx context.Context, _ driver.Request, _ driver.EventSink) (driver.Response, error) {
		close(started)
		<-ctx.Done()
		return driver.Response{}, ctx.Err()
	}
	agent := adaptor.New(fake)

	st := agent.Stream(context.Background(), "hang")
	<-started
	st.Cancel()
	st.Cancel() // idempotent

	events, res, err := collect(st)
	if len(events) != 0 {
		t.Errorf("no events expected, got %#v", events)
	}
	if res != nil {
		t.Errorf("cancelled run must not return a Result, got %+v", res)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
	var runErr *adaptor.RunError
	if errors.As(err, &runErr) {
		t.Errorf("bare cancellation must not be a *RunError (D1), got %+v", runErr)
	}
}

// TestStreamInfraError: a driver crash surfaces on Result() as a plain
// wrapped error after the (empty) event stream closes.
func TestStreamInfraError(t *testing.T) {
	fake := newFakeDriver()
	sentinel := errors.New("process exploded")
	fake.err = sentinel
	agent := adaptor.New(fake)

	events, res, err := collect(agent.Stream(context.Background(), "boom"))
	if len(events) != 0 || res != nil {
		t.Errorf("events=%v res=%v, want none", events, res)
	}
	if !errors.Is(err, sentinel) {
		t.Fatalf("want wrapped driver error, got %v", err)
	}
}
