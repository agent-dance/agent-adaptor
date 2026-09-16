package adaptor_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/capability"
	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/engine"
	"github.com/agent-dance/agent-adaptor/memory"
	"github.com/agent-dance/agent-adaptor/threadstore"
)

// An empty attachment must not change the lifecycle over a safe fallback.
type alignmentFallbackService struct{}

func (alignmentFallbackService) AttachRun(context.Context, string) (adaptor.RunAttachment, error) {
	return adaptor.RunAttachment{Events: func(context.Context, string) <-chan adaptor.Event {
		ch := make(chan adaptor.Event)
		close(ch)
		return ch
	}}, nil
}
func (alignmentFallbackService) DetachRun(context.Context, string) error { return nil }

type alignmentFallbackStore struct {
	threadstore.Store
	failNext atomic.Bool
	failure  error
}

func (s *alignmentFallbackStore) AcquireLease(ctx context.Context, target, owner string, ttl time.Duration) (threadstore.Lease, error) {
	if s.failNext.Swap(false) {
		return threadstore.Lease{}, s.failure
	}
	return s.Store.AcquireLease(ctx, target, owner, ttl)
}

func TestAlignmentResumeFallbackAttempt(t *testing.T) {
	for _, services := range []bool{false, true} {
		for _, streaming := range []bool{false, true} {
			for _, outcome := range []string{"success", "failure", "missing-terminal", "empty-coordinates", "resume-only", "prepare-failure", "rejected-again", "rejection-without-terminal"} {
				t.Run(fmt.Sprintf("services=%v/stream=%v/%s", services, streaming, outcome), func(t *testing.T) {
					alignmentCheckFallbackAttempt(t, services, streaming, outcome, false)
				})
			}
		}
	}
}

// Deliberately invalid post-terminal frames test the defensive fence separately
// from the valid start/text/tool/terminal sequence in the main regression.
func TestAlignmentResumeFallbackLatePayloadFence(t *testing.T) {
	for _, services := range []bool{false, true} {
		t.Run(fmt.Sprint(services), func(t *testing.T) {
			alignmentCheckFallbackAttempt(t, services, true, "empty-coordinates", true)
		})
	}
}

func alignmentCheckFallbackAttempt(t *testing.T, services, streaming bool, outcome string, late bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	base := memory.NewStore()
	prepareCause := errors.New("fresh lease failed")
	store := &alignmentFallbackStore{Store: base, failure: prepareCause}
	rejectCause := errors.New("provider refused resume before prompt delivery")
	rejected := &engine.ResumeRejectedError{Cause: rejectCause}
	freshCause := errors.New("fresh provider failed")
	d := newSessionFake("fallback")
	d.streamCaps = driver.StreamCapability{Native: true}
	descriptor := d.Descriptor()
	descriptor.Observation.Streaming = driver.ObservationSupport{MCP: true, Todos: true}
	d.descriptor = &descriptor
	original := d.runFunc
	var coreRunID string
	d.runFunc = func(ctx context.Context, req driver.Request, sink driver.EventSink) (driver.Response, error) {
		if !req.Streaming {
			t.Error("fixture requires the declared rich transport for both Run and Stream")
		}
		emit := func(p driver.StreamPayload) {
			t.Helper()
			if err := sink.EmitStream(p); err != nil {
				t.Errorf("emit %s: %v", p.Kind, err)
			}
		}
		if req.Prompt == "seed" {
			emit(driver.StreamPayload{Kind: driver.StreamRunStarted, RunID: "seed-run"})
			emit(driver.StreamPayload{Kind: driver.StreamRunFinished, RunID: "seed-run"})
			return original(ctx, req, sink)
		}
		isResume := req.Session.State != nil
		label, runID, threadID := "fresh", "fresh-run", "fresh-thread"
		if isResume {
			label, runID, threadID = "old", "old-run", "old-thread"
			coreRunID = req.RunID
		} else if req.RunID != coreRunID {
			t.Error("safe fallback changed public RunID")
		}
		if !isResume && outcome == "empty-coordinates" {
			runID, threadID = "", ""
		}
		emit(driver.StreamPayload{Kind: driver.StreamRunStarted, RunID: runID, ThreadID: threadID})
		item := driver.TranscriptItem{Kind: driver.TranscriptSystem, Text: label + " audit"}
		if err := sink.Emit(driver.RunEvent{Type: driver.RunEventItem, Item: &item}); err != nil {
			t.Error(err)
		}
		response := driver.Response{
			RawStreams: &driver.RawStreams{Stdout: label + " stdout;", Stderr: label + " stderr;", Terminal: &driver.TerminalPayload{Event: label, JSON: json.RawMessage(fmt.Sprintf(`{"attempt":%q}`, label))}},
			Transcript: []driver.TranscriptItem{item}, Usage: &driver.Usage{InputTokens: 3},
		}
		lateFrames := func() {
			if late {
				emit(driver.StreamPayload{Kind: driver.StreamTextContent, MessageID: "late", Delta: "late-" + label})
				emit(driver.StreamPayload{Kind: driver.StreamRunStarted, RunID: "late-" + label, ThreadID: "late-" + label})
			}
		}
		if isResume {
			if outcome != "rejection-without-terminal" {
				emit(driver.StreamPayload{Kind: driver.StreamRunError, RunID: runID, ThreadID: threadID, Error: &driver.RunFailure{Code: driver.FailureAgentError, Message: "safe resume rejection"}})
				lateFrames()
			}
			if outcome == "prepare-failure" {
				store.failNext.Store(true)
			}
			return response, rejected
		}
		emit(driver.StreamPayload{Kind: driver.StreamTextStart, MessageID: "fresh-message"})
		emit(driver.StreamPayload{Kind: driver.StreamTextContent, MessageID: "fresh-message", Delta: "fresh output"})
		emit(driver.StreamPayload{Kind: driver.StreamTextEnd, MessageID: "fresh-message"})
		emit(driver.StreamPayload{Kind: driver.StreamToolCallStart, ToolCallID: "fresh-tool", Name: "lookup", Args: map[string]any{"key": "value"}})
		emit(driver.StreamPayload{Kind: driver.StreamToolCallEnd, ToolCallID: "fresh-tool"})
		emit(driver.StreamPayload{Kind: driver.StreamToolCallResult, ToolCallID: "fresh-tool", Result: map[string]any{"ok": true}})
		fact := alignmentFact("fresh-invocation")
		emit(driver.StreamPayload{Kind: driver.StreamCapabilityInvocation, Capability: &fact})
		fact.Phase = capability.Completed
		zero := time.Duration(0)
		fact.Duration = &zero
		emit(driver.StreamPayload{Kind: driver.StreamCapabilityInvocation, Capability: &fact})
		snapshot := alignmentTodo()
		emit(driver.StreamPayload{Kind: driver.StreamTodoUpdated, Todo: &snapshot})
		response.Output, response.Usage = "fresh output", &driver.Usage{InputTokens: 13}
		if outcome == "failure" || outcome == "rejected-again" || outcome == "missing-terminal" {
			if outcome != "missing-terminal" {
				emit(driver.StreamPayload{Kind: driver.StreamRunError, RunID: runID, ThreadID: threadID, Error: &driver.RunFailure{Code: driver.FailureAgentError, Message: "fresh failed"}})
				lateFrames()
			} else {
				// Transport failure before a terminal is parsed still needs the
				// new attempt's coordinates and the core's synthesized terminal.
				response.RawStreams.Terminal = nil
			}
			if outcome == "rejected-again" {
				return response, rejected
			}
			return response, freshCause
		}
		emit(driver.StreamPayload{Kind: driver.StreamRunFinished, RunID: runID, ThreadID: threadID, Usage: response.Usage})
		lateFrames()
		response.Checkpoint = &driver.Checkpoint{Valid: true, State: &driver.SessionState{ResumeID: "fresh-checkpoint"}}
		return response, nil
	}
	opts := []adaptor.Option{adaptor.WithThreadStore(store)}
	if services {
		opts = append(opts, adaptor.WithRunServices(alignmentFallbackService{}))
	}
	a := adaptor.New(d, opts...)
	defer a.Close(context.Background())
	if _, err := a.Thread("key").Run(ctx, "seed"); err != nil {
		t.Fatal(err)
	}
	healthy := activeRecord(t, base, "key")
	thread := a.Thread("key")
	if outcome == "resume-only" {
		thread = a.Thread("key", adaptor.ResumeOnly())
	}
	var events []adaptor.Event
	var res *adaptor.Result
	var err error
	if streaming {
		st := thread.Stream(ctx, "retry")
		for ev := range st.Events() {
			events = append(events, ev)
		}
		res, err = st.Result()
	} else {
		res, err = thread.Run(ctx, "retry")
	}
	fresh := outcome != "resume-only" && outcome != "prepare-failure"
	success := outcome == "success" || outcome == "empty-coordinates" || outcome == "rejection-without-terminal"
	if success {
		if err != nil || res == nil {
			t.Fatalf("fresh success: %v", err)
		}
		after := activeRecord(t, base, "key")
		old, e := base.Resolve(ctx, threadstore.Query{ID: healthy.ID, IncludeArchived: true})
		if e != nil || after.ID == healthy.ID || after.State.ResumeID != "fresh-checkpoint" || old.Status != threadstore.StatusArchived {
			t.Fatal("healthy fresh checkpoint was not atomically rebound", after, old, e)
		}
	} else {
		var runErr *adaptor.RunError
		if res != nil || !errors.As(err, &runErr) || runErr.Result == nil || !errors.Is(err, rejectCause) {
			t.Fatalf("failed fallback lost original cause/audit: %v", err)
		}
		res = runErr.Result
		if outcome == "prepare-failure" && !errors.Is(err, prepareCause) || (outcome == "failure" || outcome == "missing-terminal") && !errors.Is(err, freshCause) {
			t.Fatal("failed fallback lost final cause", err)
		}
		if outcome == "resume-only" && !errors.Is(err, adaptor.ErrResumeRejected) {
			t.Fatal("ResumeOnly rejection identity lost", err)
		}
		if !reflect.DeepEqual(healthy, activeRecord(t, base, "key")) {
			t.Fatal("failed fallback changed healthy record")
		}
	}
	wantCalls, wantRaw, wantUsage, wantTranscript := 2, "old", 3, 1
	if fresh {
		wantCalls, wantRaw, wantUsage, wantTranscript = 3, "oldfresh", 16, 2
	}
	if d.runCount() != wantCalls || res.Usage == nil || res.Usage.InputTokens != wantUsage || len(res.Transcript()) != wantTranscript || strings.ReplaceAll(res.Raw().Stdout, " stdout;", "") != wantRaw || strings.ReplaceAll(res.Raw().Stderr, " stderr;", "") != wantRaw {
		t.Fatal("attempt count or accumulated audit changed", d.runCount(), res)
	}
	wantRawTerminal := "old"
	if fresh && outcome != "missing-terminal" {
		wantRawTerminal = "fresh"
	}
	if res.Raw().Terminal == nil || res.Raw().Terminal.Event != wantRawTerminal {
		t.Fatal("provider terminal audit lost", res.Raw())
	}
	if !streaming {
		return
	}
	starts, terminals, tools, results, facts, todos := 0, 0, 0, 0, 0, 0
	var text string
	var terminal adaptor.RunFinished
	for i, ev := range events {
		if ev.Meta().RunID != coreRunID || ev.Meta().Sequence != uint64(i+1) {
			t.Fatal("public envelope changed", ev.Meta())
		}
		switch e := ev.(type) {
		case adaptor.RunStarted:
			starts++
		case adaptor.RunFinished:
			terminals++
			terminal = e
			if i != len(events)-1 {
				t.Error("public terminal was not last")
			}
		case adaptor.TextDelta:
			text += e.Text
		case adaptor.ToolCall:
			tools++
		case adaptor.ToolResult:
			results++
		case adaptor.CapabilityInvocation:
			facts++
		case adaptor.TodoUpdated:
			todos++
		}
	}
	if starts != 1 || terminals != 1 || terminal.Failed != !success {
		t.Fatal("public lifecycle changed", starts, terminals, terminal)
	}
	wantRun, wantThread := "old-run", "old-thread"
	if fresh {
		wantRun, wantThread = "fresh-run", "fresh-thread"
		if text != "fresh output" || tools != 2 || results != 1 || facts != 2 || todos != 1 {
			t.Errorf("fresh rich facts lost or late payload escaped: text=%q tools=%d results=%d facts=%d todos=%d", text, tools, results, facts, todos)
		}
		if outcome == "empty-coordinates" {
			wantRun, wantThread = coreRunID, ""
		}
	} else if text != "" || tools+results+facts+todos != 0 {
		t.Error("unexpected fresh attempt payload")
	}
	if terminal.RunID != wantRun || terminal.ThreadID != wantThread {
		t.Error("stale attempt coordinates", terminal)
	}
	source := terminal.Meta().Source
	if outcome == "missing-terminal" || outcome == "empty-coordinates" {
		if source != nil {
			t.Error("old terminal source carried into fresh attempt", source)
		}
	} else if source == nil || source.RunID != wantRun || source.ThreadID != wantThread || source.Sequence != 0 {
		t.Error("wrong final attempt source", source)
	}
	if success {
		if terminal.Usage == nil || terminal.Usage.InputTokens != 13 {
			t.Error("fresh terminal usage changed", terminal.Usage)
		}
	} else if terminal.Usage != nil {
		// run.error does not carry Usage. Accumulated accounting remains on
		// RunError.Result, already checked above, rather than a fabricated fact.
		t.Error("error terminal acquired unobserved usage", terminal.Usage)
	}
}
