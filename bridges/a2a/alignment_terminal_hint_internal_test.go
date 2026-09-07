package a2a

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	a2aproto "github.com/a2aproject/a2a-go/v2/a2a"
	adaptor "github.com/agent-dance/agent-adaptor"
)

// This barrier withholds the final event until the executor calls Cancel.
// Cancel-mode makes runCtx.Done ready before Stream returns, so a passing test
// must collect the terminal in the cancellation drain, not the normal select.
type r016DrainStream struct {
	events    chan adaptor.Event
	cancelled chan struct{}
	once      sync.Once
	result    *adaptor.Result
	err       error
	closed    atomic.Bool
	reads     atomic.Int32
	ids       atomic.Int32
}

func (s *r016DrainStream) Events() <-chan adaptor.Event { return s.events }
func (s *r016DrainStream) Result() (*adaptor.Result, error) {
	s.reads.Add(1)
	if !s.closed.Load() {
		return nil, errors.New("Result read before Events close")
	}
	return s.result, s.err
}
func (s *r016DrainStream) RunID() string { s.ids.Add(1); return "sdk-run" }
func (s *r016DrainStream) Cancel()       { s.once.Do(func() { close(s.cancelled) }) }

type r016DrainRunner struct {
	stream       *r016DrainStream
	beforeReturn func()
	runs         atomic.Int32
	streams      atomic.Int32
}

func (r *r016DrainRunner) Run(context.Context, string, ...adaptor.CallOption) (*adaptor.Result, error) {
	r.runs.Add(1)
	return nil, errors.New("unexpected Run")
}
func (r *r016DrainRunner) Stream(context.Context, string, ...adaptor.CallOption) adaptor.Stream {
	r.streams.Add(1)
	if r.beforeReturn != nil {
		r.beforeReturn()
	}
	return r.stream
}

func TestAlignmentR016EveryDrainPath(t *testing.T) {
	for _, mode := range []string{"normal", "cancel", "translation-error", "result-builder-error"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			original := errors.Join(&adaptor.ActiveExecutionTimeoutError{Limit: 777 * time.Second}, context.Canceled)
			stream := &r016DrainStream{events: make(chan adaptor.Event), cancelled: make(chan struct{}), err: original}
			defer stream.Cancel()
			runner := &r016DrainRunner{stream: stream}
			if mode == "cancel" {
				runner.beforeReturn = cancel
			}
			exposure := ExposurePolicy{IncludeCapabilityInvocations: true}
			exec := alignmentExecutor(runner, exposure)
			if mode == "result-builder-error" {
				stream.err = nil
				stream.result = &adaptor.Result{Text: "success", Summary: "success summary"}
				exec.resultBuilder = func(context.Context, InboundRequest, *adaptor.Result) (BuiltResult, error) {
					return BuiltResult{}, errors.New("builder-private-marker")
				}
			}
			final := adaptor.WithEventMeta(adaptor.RunFinished{RunID: "provider-run", Failed: true, Reason: adaptor.ReasonCancelled, Message: "terminal-private-marker"}, adaptor.EventMeta{RunID: "sdk-run", Sequence: 8})
			var invalid adaptor.Event
			if mode == "translation-error" {
				invalid = alignmentEvent(t, "capability-start")
				meta := invalid.Meta()
				meta.ThreadKey = strings.Repeat("opaque/", 10000)
				invalid = adaptor.WithEventMeta(invalid, meta)
			}
			producerDone := make(chan struct{})
			go func() {
				defer close(producerDone)
				if invalid != nil {
					stream.events <- invalid
				}
				if mode == "cancel" || mode == "translation-error" {
					<-stream.cancelled
				}
				stream.events <- final
				stream.closed.Store(true)
				close(stream.events)
			}()
			type outcome struct {
				events []a2aproto.Event
				err    error
			}
			done := make(chan outcome, 1)
			go func() {
				var out outcome
				for event, err := range exec.Execute(ctx, alignmentExecContext()) {
					if err != nil {
						out.err = err
					} else {
						out.events = append(out.events, event)
					}
				}
				done <- out
			}()
			var out outcome
			select {
			case out = <-done:
			case <-time.After(2 * time.Second):
				t.Fatal("execution did not cancel/drain within bound")
			}
			select {
			case <-producerDone:
			case <-time.After(time.Second):
				t.Fatal("producer not drained")
			}
			if stream.reads.Load() != 1 || stream.ids.Load() != 1 || runner.runs.Load() != 0 || runner.streams.Load() != 1 || !stream.closed.Load() {
				t.Fatalf("dispatch/drain reads=%d IDs=%d Run=%d Stream=%d", stream.reads.Load(), stream.ids.Load(), runner.runs.Load(), runner.streams.Load())
			}
			terminalCount, artifactCount := 0, 0
			var terminal *a2aproto.TaskStatusUpdateEvent
			for _, event := range out.events {
				if _, ok := event.(*a2aproto.TaskArtifactUpdateEvent); ok {
					artifactCount++
				}
				if status, ok := event.(*a2aproto.TaskStatusUpdateEvent); ok && (status.Status.State == a2aproto.TaskStateFailed || status.Status.State == a2aproto.TaskStateCanceled || status.Status.State == a2aproto.TaskStateCompleted) {
					terminalCount++
					terminal = status
				}
			}
			if mode == "translation-error" {
				if out.err == nil || terminalCount != 0 || artifactCount != 1 || strings.Contains(out.err.Error(), "opaque/") || strings.Contains(out.err.Error(), "terminal-private-marker") || errors.Is(out.err, original) {
					t.Fatalf("hint covered independent translator failure: %+v", out)
				}
				return
			}
			if out.err != nil || terminalCount != 1 || terminal.Status.Message == nil {
				t.Fatalf("terminal=%+v error=%v", terminal, out.err)
			}
			part := terminal.Status.Message.Parts[0]
			control := part.Metadata["agentadaptor.failure"].(map[string]any)
			expectedCode := "cancelled"
			expectedText := "task cancelled"
			expectedState := a2aproto.TaskStateCanceled
			if mode == "result-builder-error" {
				expectedCode = "infrastructure_error"
				expectedText = "execution infrastructure failed"
				expectedState = a2aproto.TaskStateFailed
			}
			text, ok := part.Content.(a2aproto.Text)
			if !ok || string(text) != expectedText || terminal.Status.State != expectedState || len(control) != 1 || control["code"] != expectedCode {
				t.Fatalf("drain classification=%+v text=%v control=%+v", terminal.Status, text, control)
			}
			if mode != "result-builder-error" && stream.err != original {
				t.Fatal("original error graph changed")
			}
		})
	}
}

func TestAlignmentR016HintRequiresStreamClosure(t *testing.T) {
	hint := terminalHint{runID: "sdk-run"}
	final := adaptor.WithEventMeta(adaptor.RunFinished{RunID: "unrelated-provider", Failed: true, Reason: adaptor.ReasonCancelled}, adaptor.EventMeta{RunID: "sdk-run"})
	hint.observe(final)
	if hint.reason() != "" {
		t.Fatal("unfinished stream supplied a hint")
	}
	hint.closed = true
	if hint.reason() != adaptor.ReasonCancelled {
		t.Fatal("closed matching terminal lost")
	}
}
