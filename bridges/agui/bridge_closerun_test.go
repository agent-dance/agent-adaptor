package agui_test

import (
	"context"
	"errors"
	"testing"

	aguievents "github.com/ag-ui-protocol/ag-ui/sdks/community/go/pkg/core/events"

	agentadaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/bridges/agui"
)

func TestCloseRunEmitsFinishedOnNilError(t *testing.T) {
	t.Parallel()
	tr := agui.NewTranslator()

	preamble := tr.Translate(agentadaptor.StreamPayload{Kind: agentadaptor.StreamRunStarted, ThreadID: "t", RunID: "r"})
	closing := tr.CloseRun(nil)
	events := append([]aguievents.Event{}, preamble...)
	events = append(events, closing...)

	assertTypesEqual(t, []aguievents.EventType{
		aguievents.EventTypeRunStarted,
		aguievents.EventTypeRunFinished,
	}, typesOf(events))
	assertVerified(t, events)
}

func TestCloseRunEmitsErrorWithAgentErrorCodeOnRegularError(t *testing.T) {
	t.Parallel()
	tr := agui.NewTranslator()

	tr.Translate(agentadaptor.StreamPayload{Kind: agentadaptor.StreamRunStarted, ThreadID: "t", RunID: "r"})
	closing := tr.CloseRun(errors.New("boom"))

	if len(closing) != 1 {
		t.Fatalf("want 1 terminal event, got %d: %v", len(closing), typesOf(closing))
	}
	ev, ok := closing[0].(*aguievents.RunErrorEvent)
	if !ok {
		t.Fatalf("want *RunErrorEvent, got %T", closing[0])
	}
	if ev.Code == nil || *ev.Code != string(agentadaptor.FailureAgentError) {
		got := "<nil>"
		if ev.Code != nil {
			got = *ev.Code
		}
		t.Fatalf("want code=%s, got %s", agentadaptor.FailureAgentError, got)
	}
	if ev.Message != "boom" {
		t.Fatalf("want message=boom, got %q", ev.Message)
	}
}

func TestCloseRunClassifiesContextCanceledAsCancelled(t *testing.T) {
	t.Parallel()
	tr := agui.NewTranslator()

	tr.Translate(agentadaptor.StreamPayload{Kind: agentadaptor.StreamRunStarted, ThreadID: "t", RunID: "r"})
	closing := tr.CloseRun(context.Canceled)

	if len(closing) != 1 {
		t.Fatalf("want 1 terminal event, got %d", len(closing))
	}
	ev, ok := closing[0].(*aguievents.RunErrorEvent)
	if !ok {
		t.Fatalf("want *RunErrorEvent, got %T", closing[0])
	}
	if ev.Code == nil || *ev.Code != string(agentadaptor.FailureCancelled) {
		got := "<nil>"
		if ev.Code != nil {
			got = *ev.Code
		}
		t.Fatalf("want code=%s, got %s", agentadaptor.FailureCancelled, got)
	}
}

// TestCloseRunClassifiesWrappedContextCanceled guards the errors.Is contract —
// wrapping context.Canceled must still produce FailureCancelled so hosts that
// annotate ctx errors with extra context don't regress into FailureAgentError.
func TestCloseRunClassifiesWrappedContextCanceled(t *testing.T) {
	t.Parallel()
	tr := agui.NewTranslator()

	tr.Translate(agentadaptor.StreamPayload{Kind: agentadaptor.StreamRunStarted})
	wrapped := errors.Join(errors.New("forwardStream abort"), context.Canceled)
	closing := tr.CloseRun(wrapped)

	ev, ok := closing[0].(*aguievents.RunErrorEvent)
	if !ok {
		t.Fatalf("want *RunErrorEvent, got %T", closing[0])
	}
	if ev.Code == nil || *ev.Code != string(agentadaptor.FailureCancelled) {
		t.Fatalf("wrapped context.Canceled must map to FailureCancelled")
	}
}

// TestCloseRunIdempotent locks the translator's terminal-latch contract —
// calling CloseRun twice must drop the second call silently so hosts that
// hook into multiple lifecycle hooks (Wait() + forward goroutine aborting)
// don't double-write terminal events.
func TestCloseRunIdempotent(t *testing.T) {
	t.Parallel()
	tr := agui.NewTranslator()

	tr.Translate(agentadaptor.StreamPayload{Kind: agentadaptor.StreamRunStarted})
	first := tr.CloseRun(nil)
	second := tr.CloseRun(errors.New("boom"))

	if len(first) == 0 {
		t.Fatalf("first CloseRun must emit the terminal event, got none")
	}
	if len(second) != 0 {
		t.Fatalf("second CloseRun must be a no-op, got %v", typesOf(second))
	}
}

// TestCloseRunSynthesizesRunStarted locks the translator's RUN_STARTED-first
// invariant — if CloseRun is the very first call (e.g. driver exits 1 before
// emitting StreamRunStarted), the run still starts with RUN_STARTED before
// the terminal event.
func TestCloseRunSynthesizesRunStartedWhenMissing(t *testing.T) {
	t.Parallel()
	tr := agui.NewTranslator()

	events := tr.CloseRun(errors.New("driver died before emitting RunStarted"))
	assertTypesEqual(t, []aguievents.EventType{
		aguievents.EventTypeRunStarted,
		aguievents.EventTypeRunError,
	}, typesOf(events))
}
