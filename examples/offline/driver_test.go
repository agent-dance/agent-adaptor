package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/agent-dance/agent-adaptor/adaptertest"
	"github.com/agent-dance/agent-adaptor/capability"
	"github.com/agent-dance/agent-adaptor/driver"
)

// Record the SPI directly: the SDK must not supply a missing lifecycle frame.
// This sink accepts a frame before reporting an injected transport error, so
// cleanup must close observed starts without retrying or duplicating frames.
type demoRecordingSink struct {
	payloads []driver.StreamPayload
	failKind driver.StreamKind
	failErr  error
	decision func(context.Context) (driver.DecisionResponse, error)
}

func (*demoRecordingSink) Emit(driver.RunEvent) error { return nil }
func (s *demoRecordingSink) EmitStream(p driver.StreamPayload) error {
	if p.Capability != nil {
		invocation := *p.Capability
		p.Capability = &invocation
	}
	if p.Todo != nil {
		snapshot := *p.Todo
		snapshot.Items = append(snapshot.Items[:0:0], snapshot.Items...)
		p.Todo = &snapshot
	}
	s.payloads = append(s.payloads, p)
	if p.Kind == s.failKind {
		s.failKind = ""
		return s.failErr
	}
	return nil
}
func (s *demoRecordingSink) RequestDecision(ctx context.Context, _ driver.DecisionRequest) (driver.DecisionResponse, error) {
	return s.decision(ctx)
}

func TestDemoDriverReviewLifecycles(t *testing.T) {
	emitErr := errors.New("scripted sink failure")
	for _, tc := range []struct {
		name       string
		answer     driver.DecisionResponse
		cancel     bool
		failKind   driver.StreamKind
		wantErr    bool
		wantCause  error
		wantPhase  capability.Phase
		wantCode   capability.ErrorCode
		wantRunErr driver.FailureCode
	}{
		{name: "normal", answer: driver.DecisionResponse{Result: driver.DecisionAnswered, Choice: "docs"}, wantPhase: capability.Completed},
		{name: "rejected", answer: driver.DecisionResponse{Result: driver.DecisionRejected}, wantErr: true, wantPhase: capability.Interrupted, wantCode: capability.RunInterrupted, wantRunErr: driver.FailureAgentError},
		{name: "different answer", answer: driver.DecisionResponse{Result: driver.DecisionAnswered, Choice: "src"}, wantErr: true, wantPhase: capability.Interrupted, wantCode: capability.RunInterrupted, wantRunErr: driver.FailureAgentError},
		{name: "cancelled decision", cancel: true, wantErr: true, wantCause: context.Canceled, wantPhase: capability.Cancelled, wantCode: capability.RunCancelled, wantRunErr: driver.FailureCancelled},
		{name: "capability emit error", failKind: driver.StreamCapabilityInvocation, wantErr: true, wantCause: emitErr, wantPhase: capability.Interrupted, wantCode: capability.RunInterrupted, wantRunErr: driver.FailureAgentError},
		{name: "todo emit error", failKind: driver.StreamTodoUpdated, wantErr: true, wantCause: emitErr, wantPhase: capability.Interrupted, wantCode: capability.RunInterrupted, wantRunErr: driver.FailureAgentError},
		{name: "text start emit error", answer: driver.DecisionResponse{Result: driver.DecisionAnswered, Choice: "docs"}, failKind: driver.StreamTextStart, wantErr: true, wantCause: emitErr, wantPhase: capability.Interrupted, wantCode: capability.RunInterrupted, wantRunErr: driver.FailureAgentError},
		{name: "text content emit error", answer: driver.DecisionResponse{Result: driver.DecisionAnswered, Choice: "docs"}, failKind: driver.StreamTextContent, wantErr: true, wantCause: emitErr, wantPhase: capability.Interrupted, wantCode: capability.RunInterrupted, wantRunErr: driver.FailureAgentError},
		{name: "text end emit error", answer: driver.DecisionResponse{Result: driver.DecisionAnswered, Choice: "docs"}, failKind: driver.StreamTextEnd, wantErr: true, wantCause: emitErr, wantPhase: capability.Interrupted, wantCode: capability.RunInterrupted, wantRunErr: driver.FailureAgentError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			sink := &demoRecordingSink{failKind: tc.failKind, failErr: emitErr,
				decision: func(ctx context.Context) (driver.DecisionResponse, error) {
					if tc.cancel {
						cancel()
						return driver.DecisionResponse{}, ctx.Err()
					}
					return tc.answer, nil
				},
			}
			response, err := (demoDriver{}).Run(ctx, driver.Request{Prompt: "review demo", Streaming: true}, sink)
			if (err != nil) != tc.wantErr || (tc.wantCause != nil && !errors.Is(err, tc.wantCause)) {
				t.Errorf("Run error = %v, want error=%t cause=%v", err, tc.wantErr, tc.wantCause)
			}
			if !tc.wantErr && response.Output != "Reviewed docs." {
				t.Errorf("Output = %q", response.Output)
			}
			verifyDemoRun(t, sink.payloads, tc.wantRunErr)
			var facts []capability.Invocation
			for _, p := range sink.payloads {
				if p.Kind == driver.StreamCapabilityInvocation && p.Capability != nil {
					facts = append(facts, *p.Capability)
				}
			}
			if len(facts) != 2 {
				t.Fatalf("capability facts = %+v, want exactly start and terminal", facts)
			}
			if facts[0].Phase != capability.Started || facts[0].ErrorCode != "" || facts[1].Phase != tc.wantPhase || facts[1].ErrorCode != tc.wantCode {
				t.Errorf("capability lifecycle = %+v", facts)
			}
			if facts[0].InvocationID != "demo-call" || facts[1].InvocationID != facts[0].InvocationID || facts[1].Ref != facts[0].Ref {
				t.Errorf("capability identity changed: %+v", facts)
			}
			if len(sink.payloads) < 2 || sink.payloads[len(sink.payloads)-2].Kind != driver.StreamCapabilityInvocation {
				t.Error("capability terminal must precede provider terminal")
			}
		})
	}
}

func TestDemoDriverOtherRunLifecycles(t *testing.T) {
	for _, prompt := range []string{"show channels", "unknown script", "wait for budget"} {
		t.Run(prompt, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			var wantCode driver.FailureCode
			switch prompt {
			case "unknown script":
				wantCode = driver.FailureAgentError
			case "wait for budget":
				cancel()
				wantCode = driver.FailureCancelled
			}
			sink := &demoRecordingSink{}
			response, err := (demoDriver{}).Run(ctx, driver.Request{Prompt: prompt, Streaming: true}, sink)
			if (err != nil) != (wantCode != "") {
				t.Errorf("Run error = %v", err)
			}
			if prompt == "wait for budget" && (!errors.Is(err, context.Canceled) || response.Output != "work started" || response.RawStreams == nil || response.RawStreams.Stdout == "") {
				t.Errorf("cancelled response = %+v, error = %v", response, err)
			}
			verifyDemoRun(t, sink.payloads, wantCode)
		})
	}
}

func verifyDemoRun(t *testing.T, payloads []driver.StreamPayload, failure driver.FailureCode) {
	t.Helper()
	for _, violation := range adaptertest.VerifyStreamSequence(payloads) {
		t.Errorf("SPI lifecycle: %v", violation)
	}
	if len(payloads) < 2 {
		t.Errorf("stream has %d payloads, want start and terminal", len(payloads))
		return
	}
	last := payloads[len(payloads)-1]
	if failure == "" {
		if last.Kind != driver.StreamRunFinished || last.Error != nil {
			t.Errorf("successful terminal = %+v", last)
		}
	} else if last.Kind != driver.StreamRunError || last.Error == nil || last.Error.Code != failure || last.Error.Message == "" {
		t.Errorf("failure terminal = %+v, want %q", last, failure)
	}
}
