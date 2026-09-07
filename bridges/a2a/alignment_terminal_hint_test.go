package a2a_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	adaptor "github.com/agent-dance/agent-adaptor"
	a2a "github.com/agent-dance/agent-adaptor/bridges/a2a"
	clienta2a "github.com/agent-dance/agent-adaptor/clients/a2a"
	"github.com/agent-dance/agent-adaptor/driver"
)

// r016HTTP consumes one public Runner.Stream via the real server and client;
// it never directly invokes Run or creates a second stream to classify errors.
func r016HTTP(t *testing.T, runner adaptor.Runner, streaming bool) (clienta2a.Task, []clienta2a.Event) {
	t.Helper()
	mux := http.NewServeMux()
	httpServer := httptest.NewServer(mux)
	defer httpServer.Close()
	card := testCard()
	card.URL = httpServer.URL + "/rpc"
	card.Capabilities.Streaming = a2a.CapabilityEnabled
	server := a2a.NewServer(runner, a2a.ServerOptions{AgentCard: card})
	mux.Handle("/rpc", server.Handler())
	mux.Handle("/.well-known/agent-card.json", server.AgentCardHandler())
	client := clienta2a.New(clienta2a.Options{AgentCardURL: httpServer.URL, HTTPClient: httpServer.Client()})
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	request := clienta2a.SendRequest{ContextID: "r016-context", Message: clienta2a.Message{ID: "r016-message", Role: "user", Parts: []clienta2a.Part{{Kind: clienta2a.PartText, Text: "safe prompt"}}}}
	if !streaming {
		task, err := client.Send(ctx, request)
		if err != nil {
			t.Fatal(err)
		}
		return task, nil
	}
	stream, err := client.SendStream(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	var events []clienta2a.Event
	taskID := ""
	finalCount := 0
	var finalStatus *clienta2a.TaskStatus
	for {
		event, err := stream.RecvContext(ctx)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		events = append(events, event)
		if event.TaskID != "" {
			if taskID != "" && taskID != event.TaskID {
				t.Fatal("task identity changed")
			}
			taskID = event.TaskID
		}
		if event.RecoveredState {
			t.Fatal("expected live terminal, got recovery")
		}
		if event.Status != nil && event.Status.State.Terminal() {
			finalCount++
			finalStatus = event.Status
		}
	}
	if finalCount != 1 || len(events) == 0 || events[len(events)-1].Status != finalStatus {
		t.Fatalf("final count=%d, events=%+v", finalCount, events)
	}
	task, err := client.GetTask(ctx, clienta2a.GetTaskRequest{TaskID: taskID})
	if err != nil {
		t.Fatal(err)
	}
	if task.Status.State != finalStatus.State {
		t.Fatal("GetTask disagrees with live terminal")
	}
	assertBudgetJSON(t, task.Status.Message.Parts, finalStatus.Message.Parts)
	return task, events
}

type r016CancelService struct {
	cancel context.CancelCauseFunc
	cause  error
	calls  *atomic.Int32
}

func (s r016CancelService) AttachRun(ctx context.Context, _ string) (adaptor.RunAttachment, error) {
	s.calls.Add(1)
	s.cancel(s.cause)
	<-ctx.Done()
	return adaptor.RunAttachment{}, ctx.Err()
}
func (r016CancelService) DetachRun(context.Context, string) error { return nil }

type r016Audit struct {
	mu     sync.Mutex
	events []adaptor.Event
	result *adaptor.Result
	err    error
	id     string
	reads  int
	closed bool
}
type r016CoreRunner struct {
	t           *testing.T
	parentCause *adaptor.ActiveExecutionTimeoutError
	streams     atomic.Int32
	runs        atomic.Int32
	drivers     atomic.Int32
	attachments atomic.Int32
	audit       r016Audit
}

func (r *r016CoreRunner) Run(context.Context, string, ...adaptor.CallOption) (*adaptor.Result, error) {
	r.runs.Add(1)
	return nil, errors.New("unexpected Run")
}
func (r *r016CoreRunner) Stream(ctx context.Context, prompt string, opts ...adaptor.CallOption) adaptor.Stream {
	r.streams.Add(1)
	parent, cancel := context.WithCancelCause(ctx)
	fake := &scriptedDriver{run: func(int, driver.Request, driver.EventSink) (driver.Response, error) {
		r.drivers.Add(1)
		return driver.Response{}, nil
	}}
	// No local expiry is triggered: the first and only termination action is the
	// explicit parent cancellation inside the pre-Driver RunService attachment.
	agent := adaptor.New(fake, adaptor.WithPolicy(adaptor.Policy{ActiveExecutionTimeout: time.Hour}), adaptor.WithRunServices(r016CancelService{cancel: cancel, cause: r.parentCause, calls: &r.attachments}))
	r.t.Cleanup(func() { cancel(nil); _ = agent.Close(context.Background()) })
	stream := agent.Stream(parent, prompt, opts...)
	events := make(chan adaptor.Event)
	r.audit.mu.Lock()
	r.audit.id = stream.RunID()
	r.audit.mu.Unlock()
	go func() {
		defer close(events)
		for event := range stream.Events() {
			r.audit.mu.Lock()
			r.audit.events = append(r.audit.events, event)
			r.audit.mu.Unlock()
			events <- event
		}
		r.audit.mu.Lock()
		r.audit.closed = true
		r.audit.mu.Unlock()
	}()
	return &r016AuditedStream{Stream: stream, events: events, audit: &r.audit}
}

type r016AuditedStream struct {
	adaptor.Stream
	events <-chan adaptor.Event
	audit  *r016Audit
}

func (s *r016AuditedStream) Events() <-chan adaptor.Event { return s.events }
func (s *r016AuditedStream) Result() (*adaptor.Result, error) {
	result, err := s.Stream.Result()
	s.audit.mu.Lock()
	defer s.audit.mu.Unlock()
	s.audit.reads++
	s.audit.result = result
	s.audit.err = err
	return result, err
}

func TestAlignmentR016ParentCausePreDriver(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		t.Run(fmt.Sprint(streaming), func(t *testing.T) {
			parentCause := &adaptor.ActiveExecutionTimeoutError{Limit: 777 * time.Second}
			runner := &r016CoreRunner{t: t, parentCause: parentCause}
			task, _ := r016HTTP(t, runner, streaming)
			runner.audit.mu.Lock()
			defer runner.audit.mu.Unlock()
			audit := &runner.audit
			var re *adaptor.RunError
			var typed *adaptor.ActiveExecutionTimeoutError
			if runner.runs.Load() != 0 || runner.streams.Load() != 1 || runner.drivers.Load() != 0 || runner.attachments.Load() != 1 || audit.reads != 1 || !audit.closed || audit.result != nil || errors.As(audit.err, &re) || !errors.Is(audit.err, parentCause) || !errors.As(audit.err, &typed) || typed != parentCause {
				t.Fatalf("core pre-Driver evidence changed: counts=%d/%d/%d/%d audit=%+v", runner.runs.Load(), runner.streams.Load(), runner.drivers.Load(), runner.attachments.Load(), audit)
			}
			count := 0
			var terminal adaptor.RunFinished
			for _, event := range audit.events {
				if value, ok := event.(adaptor.RunFinished); ok {
					count++
					terminal = value
				}
			}
			lastIsTerminal := false
			if len(audit.events) > 0 {
				_, lastIsTerminal = audit.events[len(audit.events)-1].(adaptor.RunFinished)
			}
			if count != 1 || !lastIsTerminal || !terminal.Failed || terminal.Reason != adaptor.ReasonCancelled || terminal.Meta().RunID == "" || terminal.Meta().RunID != audit.id {
				t.Fatalf("core terminal=%+v, count=%d, streamID=%s", terminal, count, audit.id)
			}
			text, control := budgetControl(t, task)
			if task.Status.State != clienta2a.TaskStateCanceled || text != "task cancelled" {
				t.Fatalf("R016 real pre-Driver: core=%s wire=%s text=%q control=%+v", terminal.Reason, task.Status.State, text, control)
			}
			assertBudgetJSON(t, control, map[string]any{"code": "cancelled"})
		})
	}
}

// The static cases replay public shapes already accepted from G03. No private
// core timer/controller is imported to recreate T10's timing tests here.
type r016StaticRunner struct {
	events  []adaptor.Event
	id      string
	result  *adaptor.Result
	err     error
	runs    atomic.Int32
	streams atomic.Int32
	reads   atomic.Int32
	ids     atomic.Int32
	drained atomic.Bool
}

func (r *r016StaticRunner) Run(context.Context, string, ...adaptor.CallOption) (*adaptor.Result, error) {
	r.runs.Add(1)
	return nil, errors.New("unexpected Run")
}
func (r *r016StaticRunner) Stream(context.Context, string, ...adaptor.CallOption) adaptor.Stream {
	r.streams.Add(1)
	events := make(chan adaptor.Event, len(r.events))
	for _, event := range r.events {
		events <- event
	}
	close(events)
	return &r016StaticStream{runner: r, events: events}
}

type r016StaticStream struct {
	runner *r016StaticRunner
	events chan adaptor.Event
}

func (s *r016StaticStream) Events() <-chan adaptor.Event { return s.events }
func (s *r016StaticStream) Result() (*adaptor.Result, error) {
	s.runner.reads.Add(1)
	s.runner.drained.Store(len(s.events) == 0)
	return s.runner.result, s.runner.err
}
func (s *r016StaticStream) RunID() string {
	if s.runner.ids.Add(1) != 1 {
		return "late-changed-id"
	}
	return s.runner.id
}
func (*r016StaticStream) Cancel() {}
func r016Terminal(metaID, providerID string, reason adaptor.FailureReason) adaptor.RunFinished {
	return adaptor.WithEventMeta(adaptor.RunFinished{RunID: providerID, ThreadID: "provider-thread", Failed: true, Reason: reason, Message: "terminal-private-marker", Usage: &adaptor.Usage{InputTokens: 777}}, adaptor.EventMeta{RunID: metaID, Sequence: 10}).(adaptor.RunFinished)
}
func r016StaticOutcome(t *testing.T, runner *r016StaticRunner, streaming bool) (clienta2a.Task, []clienta2a.Event) {
	t.Helper()
	task, events := r016HTTP(t, runner, streaming)
	if runner.runs.Load() != 0 || runner.streams.Load() != 1 || runner.reads.Load() != 1 || runner.ids.Load() != 1 || !runner.drained.Load() {
		t.Fatalf("run/stream/result/id=%d/%d/%d/%d drained=%v", runner.runs.Load(), runner.streams.Load(), runner.reads.Load(), runner.ids.Load(), runner.drained.Load())
	}
	return task, events
}

func TestAlignmentR016SelectedLocalBudgetHint(t *testing.T) {
	for _, typed := range []bool{false, true} {
		for _, streaming := range []bool{false, true} {
			t.Run(fmt.Sprintf("typed=%v/stream=%v", typed, streaming), func(t *testing.T) {
				var err error = adaptor.ErrActiveExecutionTimeout
				expected := map[string]any{"code": "active_execution_timeout"}
				if typed {
					err = errors.Join(&adaptor.ActiveExecutionTimeoutError{Limit: 100 * time.Millisecond}, &adaptor.ActiveExecutionTimeoutError{Limit: 777 * time.Second}, context.Canceled)
					expected["limit_ms"] = 100
				}
				runner := &r016StaticRunner{id: "sdk-run", err: err, events: []adaptor.Event{r016Terminal("sdk-run", "provider-run", adaptor.ReasonActiveExecutionTimeout)}}
				task, _ := r016StaticOutcome(t, runner, streaming)
				text, control := budgetControl(t, task)
				if task.Status.State != clienta2a.TaskStateFailed || text != "active execution budget exhausted" || len(task.Artifacts) != 0 || runner.err != err {
					t.Fatalf("hint changed outcome/error: %+v", task)
				}
				assertBudgetJSON(t, control, expected)
			})
		}
	}
}

func TestAlignmentR016HintEligibilityAndFallback(t *testing.T) {
	valid := r016Terminal("sdk-run", "provider-run", adaptor.ReasonCancelled)
	blankProvider := r016Terminal("sdk-run", "", adaptor.ReasonCancelled)
	noMeta := r016Terminal("", "sdk-run", adaptor.ReasonCancelled)
	otherMeta := r016Terminal("other-run", "sdk-run", adaptor.ReasonCancelled)
	notFailed := valid
	notFailed.Failed = false
	emptyReason := valid
	emptyReason.Reason = ""
	unknown := valid
	unknown.Reason = "private-reason-marker"
	conflict := r016Terminal("sdk-run", "provider-run", adaptor.ReasonApprovalDenied)
	lateEvent := adaptor.Notice{Kind: "late-event"}
	var nilTerminal *adaptor.RunFinished
	for _, test := range []struct {
		name, id string
		events   []adaptor.Event
		hint     bool
	}{
		{"different-provider-id", "sdk-run", []adaptor.Event{valid}, true},
		{"empty-provider-id", "sdk-run", []adaptor.Event{blankProvider}, true},
		{"pointer-terminal", "sdk-run", []adaptor.Event{&valid}, true},
		{"empty-meta-id", "sdk-run", []adaptor.Event{noMeta}, false},
		{"foreign-meta-id", "sdk-run", []adaptor.Event{otherMeta}, false},
		{"empty-stream-id", "", []adaptor.Event{valid}, false},
		{"missing-terminal", "sdk-run", nil, false},
		{"not-failed", "sdk-run", []adaptor.Event{notFailed}, false},
		{"empty-reason", "sdk-run", []adaptor.Event{emptyReason}, false},
		{"unknown-reason", "sdk-run", []adaptor.Event{unknown}, false},
		{"duplicate-identical", "sdk-run", []adaptor.Event{valid, valid}, false},
		{"duplicate-conflict", "sdk-run", []adaptor.Event{valid, conflict}, false},
		{"terminal-not-last", "sdk-run", []adaptor.Event{valid, lateEvent}, false},
		{"nil-pointer", "sdk-run", []adaptor.Event{nilTerminal}, false},
		{"nil-before-terminal", "sdk-run", []adaptor.Event{nilTerminal, valid}, false},
		{"value-pointer-duplicate", "sdk-run", []adaptor.Event{valid, &valid}, false},
		{"invalid-before-terminal", "sdk-run", []adaptor.Event{unknown, valid}, false},
	} {
		for _, streaming := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%v", test.name, streaming), func(t *testing.T) {
				original := errors.Join(&adaptor.ActiveExecutionTimeoutError{Limit: 777 * time.Second}, context.Canceled)
				runner := &r016StaticRunner{id: test.id, err: original, events: test.events}
				task, _ := r016StaticOutcome(t, runner, streaming)
				text, control := budgetControl(t, task)
				wantState := clienta2a.TaskStateFailed
				wantText := "active execution budget exhausted"
				want := map[string]any{"code": "active_execution_timeout", "limit_ms": 777000}
				if test.hint {
					wantState = clienta2a.TaskStateCanceled
					wantText = "task cancelled"
					want = map[string]any{"code": "cancelled"}
				}
				if task.Status.State != wantState || text != wantText || len(task.Artifacts) != 0 || runner.err != original || !errors.Is(runner.err, context.Canceled) {
					t.Fatalf("hint=%v state/text=%s/%q", test.hint, task.Status.State, text)
				}
				assertBudgetJSON(t, control, want)
			})
		}
	}
}

func TestAlignmentR016CarrierAlwaysWins(t *testing.T) {
	partial := budgetPartial(t, true)
	for _, reason := range []adaptor.FailureReason{adaptor.ReasonApprovalDenied, "private-reason-marker", ""} {
		for _, streaming := range []bool{false, true} {
			t.Run(fmt.Sprintf("reason=%s/stream=%v", reason, streaming), func(t *testing.T) {
				parent := &adaptor.ActiveExecutionTimeoutError{Limit: 777 * time.Second}
				re := &adaptor.RunError{Reason: reason, Result: partial, Message: "carrier-private-marker", Cause: errors.Join(parent, context.Canceled), Details: budgetDetails()}
				original := fmt.Errorf("wrapper-private-marker: %w", re)
				runner := &r016StaticRunner{id: "sdk-run", err: original, events: []adaptor.Event{r016Terminal("sdk-run", "provider-run", adaptor.ReasonActiveExecutionTimeout)}}
				task, events := r016StaticOutcome(t, runner, streaming)
				text, control := budgetControl(t, task)
				expected := map[string]any(nil)
				expectedText := "agent run failed"
				if reason == adaptor.ReasonApprovalDenied {
					expected = map[string]any{"code": "approval_denied"}
					expectedText = "approval denied"
				}
				if task.Status.State != clienta2a.TaskStateFailed || text != expectedText || runner.err != original || re.Result != partial || re.Reason != reason || !errors.Is(original, parent) || !errors.Is(original, context.Canceled) {
					t.Fatalf("carrier altered: %+v", task)
				}
				assertBudgetJSON(t, control, expected)
				if len(task.Artifacts) != 1 {
					t.Fatal("partial artifact lost")
				}
				if task.Artifacts[0].Parts[0].Data.(map[string]any)["summary"] != "safe partial summary" {
					t.Fatal("partial summary lost")
				}
				if streaming {
					artifactAt := -1
					for i, event := range events {
						if event.Artifact != nil {
							artifactAt = i
						}
					}
					if artifactAt < 0 || artifactAt >= len(events)-1 {
						t.Fatal("partial artifact did not precede terminal")
					}
				}
			})
		}
	}
}

func TestAlignmentR016SuccessIgnoresFailedHint(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		t.Run(fmt.Sprint(streaming), func(t *testing.T) {
			result := &adaptor.Result{Text: "successful reply", Summary: "success summary"}
			runner := &r016StaticRunner{id: "sdk-run", result: result, events: []adaptor.Event{r016Terminal("sdk-run", "provider-run", adaptor.ReasonActiveExecutionTimeout)}}
			task, _ := r016StaticOutcome(t, runner, streaming)
			if task.Status.State != clienta2a.TaskStateCompleted || task.Status.Message == nil || task.Status.Message.Parts[0].Text != result.Text || len(task.Artifacts) != 1 || runner.err != nil || runner.result != result {
				t.Fatalf("event manufactured failure: %+v", task)
			}
			if _, ok := task.Status.Message.Parts[0].Metadata["agentadaptor.failure"]; ok {
				t.Fatal("success carries failure control")
			}
		})
	}
}

func TestAlignmentR016ClosedReasonSet(t *testing.T) {
	for _, reason := range []adaptor.FailureReason{adaptor.ReasonActiveExecutionTimeout, adaptor.ReasonApprovalDenied, adaptor.ReasonApprovalTimeout, adaptor.ReasonCancelled, adaptor.ReasonDeadlineExceeded, adaptor.ReasonAgentError, adaptor.ReasonPolicyViolation, adaptor.ReasonInfrastructure} {
		t.Run(string(reason), func(t *testing.T) {
			original := errors.New("bare-private-marker")
			runner := &r016StaticRunner{id: "sdk-run", err: original, events: []adaptor.Event{r016Terminal("sdk-run", "provider-run", reason)}}
			task, _ := r016StaticOutcome(t, runner, true)
			_, control := budgetControl(t, task)
			assertBudgetJSON(t, control, map[string]any{"code": string(reason)})
			expected := clienta2a.TaskStateFailed
			if reason == adaptor.ReasonCancelled {
				expected = clienta2a.TaskStateCanceled
			}
			if task.Status.State != expected || runner.err != original {
				t.Fatalf("reason/status=%s/%s", reason, task.Status.State)
			}
		})
	}
}
