package a2adelegation

import (
	"context"
	"errors"
	adaptor "github.com/agent-dance/agent-adaptor"
	clienta2a "github.com/agent-dance/agent-adaptor/clients/a2a"
	"github.com/agent-dance/agent-adaptor/driver"
	"io"
	"testing"
	"time"
)

type terminalCancelService struct {
	cancel context.CancelCauseFunc
	cause  error
}

func (s terminalCancelService) AttachRun(ctx context.Context, _ string) (adaptor.RunAttachment, error) {
	s.cancel(s.cause)
	<-ctx.Done()
	return adaptor.RunAttachment{}, context.Cause(ctx)
}
func (terminalCancelService) DetachRun(context.Context, string) error { return nil }
func terminalTask(t *testing.T, c *localClient, ctx context.Context, streaming bool) clienta2a.Task {
	t.Helper()
	if !streaming {
		task, err := c.Send(ctx, clienta2a.SendRequest{})
		if err != nil {
			t.Fatal(err)
		}
		return task
	}
	s, err := c.SendStream(ctx, clienta2a.SendRequest{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var task clienta2a.Task
	for {
		e, err := s.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if e.Task != nil {
			task = *e.Task
		}
	}
	return task
}
func TestAlignmentTerminalReasonRealPreDriverParent(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		t.Run(map[bool]string{false: "Send", true: "SendStream"}[streaming], func(t *testing.T) {
			ctx, cancel := context.WithCancelCause(context.Background())
			defer cancel(nil)
			inherited := &adaptor.ActiveExecutionTimeoutError{Limit: 777 * time.Second}
			driverCalls := 0
			member := adaptor.New(alignmentDriver{run: func(context.Context, driver.Request, driver.EventSink) (driver.Response, error) {
				driverCalls++
				return driver.Response{}, nil
			}}, adaptor.WithRunServices(terminalCancelService{cancel, inherited}))
			defer member.Close(context.Background())
			c := newLocalClient("member", member)
			task := terminalTask(t, c, ctx, streaming)
			failure, invalid := failureFromStatus(task.Status)
			if invalid || failure == nil || failure.Code != "cancelled" || task.Status.State != clienta2a.TaskStateCanceled || failure.Metadata["limit_ms"] != nil || driverCalls != 0 {
				t.Fatalf("parent active misclassified: %+v failure=%+v driver=%d", task, failure, driverCalls)
			}
			if len(task.Messages) != 0 || len(task.Artifacts) != 0 {
				t.Fatal("pre-Driver bare failure manufactured Result payload")
			}
			original := c.taskFailure(task.ID).Cause
			var re *adaptor.RunError
			if !errors.Is(original, inherited) || errors.As(original, &re) {
				t.Fatalf("not real pre-Driver bare graph: %v", original)
			}
		})
	}
}

type terminalProbeRunner struct {
	stream        adaptor.Stream
	runs, streams int
}

func (r *terminalProbeRunner) Run(context.Context, string, ...adaptor.CallOption) (*adaptor.Result, error) {
	r.runs++
	return nil, errors.New("Run must not be called")
}
func (r *terminalProbeRunner) Stream(context.Context, string, ...adaptor.CallOption) adaptor.Stream {
	r.streams++
	return r.stream
}

type terminalProbeStream struct {
	id     string
	events chan adaptor.Event
	result *adaptor.Result
	err    error
	cancel func()
}

func (s *terminalProbeStream) Events() <-chan adaptor.Event     { return s.events }
func (s *terminalProbeStream) RunID() string                    { return s.id }
func (s *terminalProbeStream) Result() (*adaptor.Result, error) { return s.result, s.err }
func (s *terminalProbeStream) Cancel() {
	if s.cancel != nil {
		s.cancel()
	}
}
func terminalFact(meta, body string, reason adaptor.FailureReason, failed bool) adaptor.Event {
	return adaptor.WithEventMeta(adaptor.RunFinished{RunID: body, Failed: failed, Reason: reason, Message: "secret provider text"}, adaptor.EventMeta{RunID: meta})
}
func TestAlignmentTerminalReasonQualificationAndCarrier(t *testing.T) {
	parent := &adaptor.ActiveExecutionTimeoutError{Limit: 777 * time.Second}
	own := &adaptor.ActiveExecutionTimeoutError{Limit: 100 * time.Millisecond}
	bare := errors.Join(parent, context.Canceled)
	valid := terminalFact("core", "provider", adaptor.ReasonCancelled, true)
	partial := &adaptor.Result{Text: "carrier partial", Summary: "carrier summary"}
	cases := []struct {
		name, id string
		events   []adaptor.Event
		err      error
		want     string
		limit    int64
	}{
		{"provider_body", "core", []adaptor.Event{valid}, bare, "cancelled", 0},
		{"empty_body", "core", []adaptor.Event{terminalFact("core", "", adaptor.ReasonCancelled, true)}, bare, "cancelled", 0},
		{"meta_empty", "core", []adaptor.Event{terminalFact("", "core", adaptor.ReasonCancelled, true)}, bare, "active_execution_timeout", 777000},
		{"meta_other", "core", []adaptor.Event{terminalFact("other", "core", adaptor.ReasonCancelled, true)}, bare, "active_execution_timeout", 777000},
		{"stream_empty", "", []adaptor.Event{valid}, bare, "active_execution_timeout", 777000},
		{"absent", "core", nil, bare, "active_execution_timeout", 777000},
		{"not_failed", "core", []adaptor.Event{terminalFact("core", "", adaptor.ReasonCancelled, false)}, bare, "active_execution_timeout", 777000},
		{"unknown", "core", []adaptor.Event{terminalFact("core", "", "unknown", true)}, bare, "active_execution_timeout", 777000},
		{"empty_reason", "core", []adaptor.Event{terminalFact("core", "", "", true)}, bare, "active_execution_timeout", 777000},
		{"same_twice", "core", []adaptor.Event{valid, valid}, bare, "active_execution_timeout", 777000},
		{"conflict", "core", []adaptor.Event{terminalFact("core", "", adaptor.ReasonActiveExecutionTimeout, true), valid}, bare, "active_execution_timeout", 777000},
		{"after_terminal", "core", []adaptor.Event{valid, adaptor.Notice{}}, bare, "active_execution_timeout", 777000},
		{"carrier_approval", "core", []adaptor.Event{terminalFact("core", "", adaptor.ReasonActiveExecutionTimeout, true)}, &adaptor.RunError{Reason: adaptor.ReasonApprovalDenied, Result: partial, Cause: bare}, "approval_denied", 0},
		{"carrier_unknown", "core", []adaptor.Event{valid}, &adaptor.RunError{Reason: "future", Result: partial, Cause: bare}, "remote_failed", 0},
		{"carrier_empty", "core", []adaptor.Event{valid}, &adaptor.RunError{Result: partial, Cause: bare}, "remote_failed", 0},
		{"success", "core", []adaptor.Event{valid}, nil, "", 0},
		{"own_then_parent", "core", []adaptor.Event{terminalFact("core", "", adaptor.ReasonActiveExecutionTimeout, true)}, errors.Join(own, parent, context.Canceled), "active_execution_timeout", 100},
		{"active_no_typed", "core", []adaptor.Event{terminalFact("core", "", adaptor.ReasonActiveExecutionTimeout, true)}, errors.New("bare"), "active_execution_timeout", 0},
	}
	for _, reason := range []adaptor.FailureReason{adaptor.ReasonActiveExecutionTimeout, adaptor.ReasonApprovalDenied, adaptor.ReasonApprovalTimeout, adaptor.ReasonCancelled, adaptor.ReasonDeadlineExceeded, adaptor.ReasonAgentError, adaptor.ReasonPolicyViolation, adaptor.ReasonInfrastructure} {
		cases = append(cases, struct {
			name, id string
			events   []adaptor.Event
			err      error
			want     string
			limit    int64
		}{"closed_" + string(reason), "core", []adaptor.Event{terminalFact("core", "provider", reason, true)}, errors.New("bare infrastructure"), string(reason), 0})
	}
	// Pointer terminals have exactly the same qualification/count semantics.
	ptr := adaptor.WithEventMeta(&adaptor.RunFinished{Failed: true, Reason: adaptor.ReasonCancelled}, adaptor.EventMeta{RunID: "core"})
	cases = append(cases, struct {
		name, id string
		events   []adaptor.Event
		err      error
		want     string
		limit    int64
	}{"pointer", "core", []adaptor.Event{ptr}, bare, "cancelled", 0})
	cases = append(cases, struct {
		name, id string
		events   []adaptor.Event
		err      error
		want     string
		limit    int64
	}{"nil_pointer", "core", []adaptor.Event{(*adaptor.RunFinished)(nil)}, bare, "active_execution_timeout", 777000})
	for _, tc := range cases {
		for _, streaming := range []bool{false, true} {
			t.Run(tc.name+map[bool]string{false: "/Send", true: "/SendStream"}[streaming], func(t *testing.T) {
				ch := make(chan adaptor.Event, len(tc.events))
				for _, ev := range tc.events {
					ch <- ev
				}
				close(ch)
				result := &adaptor.Result{Text: "public result", Summary: "public summary"}
				stream := &terminalProbeStream{id: tc.id, events: ch, result: result, err: tc.err}
				runner := &terminalProbeRunner{stream: stream}
				c := newLocalClient("member", runner)
				task := terminalTask(t, c, context.Background(), streaming)
				if runner.runs != 0 || runner.streams != 1 {
					t.Fatalf("executed more than one Stream: %+v", runner)
				}
				if tc.err == nil {
					if task.Status.State != clienta2a.TaskStateCompleted || textFromMessage(task.Messages[0]) != result.Text || c.taskFailure(task.ID) != nil {
						t.Fatal("event manufactured failure")
					}
					return
				}
				failure := c.taskFailure(task.ID)
				if failure == nil || failure.Code != tc.want || failure.Cause != tc.err {
					t.Fatalf("classification/object changed: %+v want=%s", failure, tc.want)
				}
				if tc.limit == 0 {
					if _, ok := failure.Metadata["limit_ms"]; ok {
						t.Fatal("untrusted limit promoted")
					}
				} else if failure.Metadata["limit_ms"] != tc.limit {
					t.Fatalf("limit=%+v want=%d", failure.Metadata, tc.limit)
				}
				var re *adaptor.RunError
				if errors.As(tc.err, &re) && re != nil {
					if textFromMessage(task.Messages[0]) != partial.Text || len(task.Artifacts) != 1 || !errors.Is(failure, tc.err) || re.Result != partial {
						t.Fatal("carrier partial/object lost")
					}
				}
			})
		}
	}
}
func TestAlignmentTerminalReasonCancelDrain(t *testing.T) {
	runCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := make(chan adaptor.Event)
	original := errors.Join(&adaptor.ActiveExecutionTimeoutError{Limit: 777 * time.Second}, context.Canceled)
	stream := &terminalProbeStream{id: "core", events: events, err: original}
	runner := &terminalProbeRunner{stream: stream}
	c := newLocalClient("member", runner)
	raw, err := c.SendStream(runCtx, clienta2a.SendRequest{})
	if err != nil {
		t.Fatal(err)
	}
	s := raw.(*localA2AStream)
	cancel()
	<-runCtx.Done() // The terminal becomes available strictly after cancellation.
	go func() { events <- terminalFact("core", "provider", adaptor.ReasonCancelled, true); close(events) }()
	cleanup, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	task, got := s.cancelledTask(cleanup)
	failure := c.taskFailure(task.ID)
	if got != original || failure == nil || failure.Code != "cancelled" || failure.Metadata["limit_ms"] != nil || runner.runs != 0 || runner.streams != 1 {
		t.Fatalf("cancel drain lost hint: %+v %v", failure, got)
	}
}
