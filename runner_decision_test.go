package agentadaptor

import (
	"context"
	"errors"
	"testing"
	"time"
)

// These tests exercise the HITL v2 dispatcher on dualSink directly. They
// avoid spawning a full Runner so the state transitions documented in
// docs/workstream-hitl-v2.md §3.11 can be asserted in isolation.

func newBoundSink(t *testing.T, policy HumanDecisionPolicy, handlers decisionHandlers) *dualSink {
	t.Helper()
	s := newDualSink("run-test", true, 8, 8, BackpressureDropStream)
	s.bindRun("run-test", policy, handlers)
	return s
}

func TestDualSink_AutoApprove_PermissionShortCircuits(t *testing.T) {
	s := newBoundSink(t, HumanDecisionPolicy{Permission: HumanDecisionAutoApprove}, decisionHandlers{})
	defer s.close()

	resp, err := s.RequestDecision(context.Background(), DecisionRequest{Kind: HumanDecisionPermission, Source: "test.perm"})
	if err != nil {
		t.Fatalf("auto-approve should not error: %v", err)
	}
	if resp.Result != DecisionApproved {
		t.Fatalf("got %q want approved", resp.Result)
	}
}

func TestDualSink_AutoReject_PermissionAborts(t *testing.T) {
	s := newBoundSink(t, HumanDecisionPolicy{
		Permission: HumanDecisionAutoReject,
		OnReject:   FailureAbort,
	}, decisionHandlers{})
	defer s.close()

	_, err := s.RequestDecision(context.Background(), DecisionRequest{Kind: HumanDecisionPermission, Source: "test.perm"})
	if !errors.Is(err, errDecisionAbort) {
		t.Fatalf("expected abort, got %v", err)
	}
	f := s.pendingFailure()
	if f == nil || f.Code != FailureReject || f.HumanDecision == nil {
		t.Fatalf("expected FailureReject with HumanDecision, got %+v", f)
	}
}

func TestDualSink_AutoReject_ContinueDoesNotAbort(t *testing.T) {
	s := newBoundSink(t, HumanDecisionPolicy{
		Permission: HumanDecisionAutoReject,
		OnReject:   FailureContinue,
	}, decisionHandlers{})
	defer s.close()

	resp, err := s.RequestDecision(context.Background(), DecisionRequest{Kind: HumanDecisionPermission, Source: "test.perm"})
	if err != nil {
		t.Fatalf("continue should not error: %v", err)
	}
	if resp.Result != DecisionRejected {
		t.Fatalf("got %q want rejected", resp.Result)
	}
	if f := s.pendingFailure(); f != nil {
		t.Fatalf("continue must not set failure, got %+v", f)
	}
}

func TestDualSink_PermissionHandler_ReturnsApproved(t *testing.T) {
	called := false
	handler := PermissionHandler(func(ctx context.Context, req PermissionRequest) (PermissionResponse, error) {
		called = true
		if req.Prompt != "please" {
			t.Errorf("prompt: %q", req.Prompt)
		}
		return PermissionResponse{RequestID: req.RequestID, Result: ApprovalApproved}, nil
	})
	s := newBoundSink(t, HumanDecisionPolicy{Permission: HumanDecisionAsk}, decisionHandlers{Permission: handler})
	defer s.close()

	resp, err := s.RequestDecision(context.Background(), DecisionRequest{Kind: HumanDecisionPermission, Prompt: "please"})
	if err != nil {
		t.Fatalf("handler approve: %v", err)
	}
	if !called {
		t.Error("handler not invoked")
	}
	if resp.Result != DecisionApproved {
		t.Errorf("resp: %q", resp.Result)
	}
}

func TestDualSink_PermissionHandler_ReturnsRejectedTriggersAbort(t *testing.T) {
	handler := PermissionHandler(func(_ context.Context, req PermissionRequest) (PermissionResponse, error) {
		return PermissionResponse{RequestID: req.RequestID, Result: ApprovalRejected}, nil
	})
	s := newBoundSink(t, HumanDecisionPolicy{Permission: HumanDecisionAsk, OnReject: FailureAbort}, decisionHandlers{Permission: handler})
	defer s.close()

	_, err := s.RequestDecision(context.Background(), DecisionRequest{Kind: HumanDecisionPermission})
	if !errors.Is(err, errDecisionAbort) {
		t.Fatalf("expected abort, got %v", err)
	}
	f := s.pendingFailure()
	if f == nil || f.Code != FailureReject || f.HumanDecision.Attempts != 1 {
		t.Fatalf("failure: %+v", f)
	}
}

func TestDualSink_Handler_Error_MapsToFailureCancelled(t *testing.T) {
	handler := PermissionHandler(func(_ context.Context, _ PermissionRequest) (PermissionResponse, error) {
		return PermissionResponse{}, errors.New("boom")
	})
	s := newBoundSink(t, HumanDecisionPolicy{Permission: HumanDecisionAsk}, decisionHandlers{Permission: handler})
	defer s.close()

	_, err := s.RequestDecision(context.Background(), DecisionRequest{Kind: HumanDecisionPermission})
	if err == nil || err.Error() != "boom" {
		t.Fatalf("expected handler error, got %v", err)
	}
	f := s.pendingFailure()
	if f == nil || f.Code != FailureCancelled {
		t.Fatalf("failure: %+v", f)
	}
}

func TestDualSink_Handler_Panic_MapsToFailureAgentError(t *testing.T) {
	handler := PermissionHandler(func(_ context.Context, _ PermissionRequest) (PermissionResponse, error) {
		panic("oops")
	})
	s := newBoundSink(t, HumanDecisionPolicy{Permission: HumanDecisionAsk}, decisionHandlers{Permission: handler})
	defer s.close()

	_, err := s.RequestDecision(context.Background(), DecisionRequest{Kind: HumanDecisionPermission})
	if err == nil {
		t.Fatal("expected error after panic")
	}
	f := s.pendingFailure()
	if f == nil || f.Code != FailureAgentError {
		t.Fatalf("failure: %+v", f)
	}
}

func TestDualSink_Handler_NilResponse_MapsToFailureAgentError(t *testing.T) {
	handler := PermissionHandler(func(_ context.Context, _ PermissionRequest) (PermissionResponse, error) {
		return PermissionResponse{}, nil
	})
	s := newBoundSink(t, HumanDecisionPolicy{Permission: HumanDecisionAsk}, decisionHandlers{Permission: handler})
	defer s.close()

	_, err := s.RequestDecision(context.Background(), DecisionRequest{Kind: HumanDecisionPermission})
	if err == nil {
		t.Fatal("expected error for handler returning empty response")
	}
	f := s.pendingFailure()
	if f == nil || f.Code != FailureAgentError {
		t.Fatalf("failure: %+v", f)
	}
}

func TestDualSink_Timeout_AbortsWithFailureTimeout(t *testing.T) {
	s := newBoundSink(t, HumanDecisionPolicy{
		Permission: HumanDecisionAsk,
		Timeout:    25 * time.Millisecond,
		OnTimeout:  FailureAbort,
	}, decisionHandlers{})
	defer s.close()

	_, err := s.RequestDecision(context.Background(), DecisionRequest{Kind: HumanDecisionPermission})
	if !errors.Is(err, errDecisionAbort) {
		t.Fatalf("expected abort, got %v", err)
	}
	f := s.pendingFailure()
	if f == nil || f.Code != FailureTimeout {
		t.Fatalf("failure: %+v", f)
	}
}

func TestDualSink_ChannelDispatch_Resolve(t *testing.T) {
	s := newBoundSink(t, HumanDecisionPolicy{Permission: HumanDecisionAsk, Timeout: time.Second}, decisionHandlers{})
	defer s.close()

	type res struct {
		resp DecisionResponse
		err  error
	}
	out := make(chan res, 1)
	go func() {
		r, err := s.RequestDecision(context.Background(), DecisionRequest{Kind: HumanDecisionPermission})
		out <- res{r, err}
	}()

	req := <-s.decisionRequests
	if err := s.resolveDecisionFromHandle(req.RequestID, DecisionResponse{Result: DecisionApproved}); err != nil {
		t.Fatalf("resolve: %v", err)
	}

	select {
	case r := <-out:
		if r.err != nil {
			t.Fatalf("channel resolve err: %v", r.err)
		}
		if r.resp.Result != DecisionApproved {
			t.Errorf("result: %q", r.resp.Result)
		}
	case <-time.After(time.Second):
		t.Fatal("RequestDecision did not return after resolve")
	}
}

func TestDualSink_Resolve_UnknownID_ReturnsExpired(t *testing.T) {
	s := newBoundSink(t, HumanDecisionPolicy{}, decisionHandlers{})
	defer s.close()
	err := s.resolveDecisionFromHandle("nope", DecisionResponse{Result: DecisionApproved})
	if !errors.Is(err, ErrDecisionRequestExpired) {
		t.Fatalf("want ErrDecisionRequestExpired, got %v", err)
	}
}

func TestDualSink_Resolve_KindMismatch_ReturnsError(t *testing.T) {
	s := newBoundSink(t, HumanDecisionPolicy{Question: QuestionAsk, Timeout: time.Second}, decisionHandlers{})
	defer s.close()

	go func() {
		_, _ = s.RequestDecision(context.Background(), DecisionRequest{Kind: HumanDecisionQuestion})
	}()

	req := <-s.decisionRequests
	// Kind=Question cannot accept Approved.
	err := s.resolveDecisionFromHandle(req.RequestID, DecisionResponse{Result: DecisionApproved})
	if !errors.Is(err, ErrDecisionResultKindMismatch) {
		t.Fatalf("want kind mismatch, got %v", err)
	}
	// Now resolve legitimately so the RequestDecision goroutine can unwind.
	if err := s.resolveDecisionFromHandle(req.RequestID, DecisionResponse{Result: DecisionAnswered, Answer: map[string]any{"k": 1}}); err != nil {
		t.Fatalf("legit resolve: %v", err)
	}
}

func TestDualSink_Retry_ExhaustsAndAborts(t *testing.T) {
	attempts := 0
	handler := PermissionHandler(func(_ context.Context, _ PermissionRequest) (PermissionResponse, error) {
		attempts++
		return PermissionResponse{Result: ApprovalRejected}, nil
	})
	s := newBoundSink(t, HumanDecisionPolicy{
		Permission: HumanDecisionAsk,
		OnReject:   FailureRetry,
		MaxRetries: 2,
	}, decisionHandlers{Permission: handler})
	defer s.close()

	_, err := s.RequestDecision(context.Background(), DecisionRequest{Kind: HumanDecisionPermission})
	if !errors.Is(err, errDecisionAbort) {
		t.Fatalf("expected abort after retries, got %v", err)
	}
	if attempts != 3 {
		t.Errorf("attempts: got %d want 3 (first call + 2 retries)", attempts)
	}
	f := s.pendingFailure()
	if f == nil || f.HumanDecision.Attempts != 3 {
		t.Fatalf("failure attempts: %+v", f)
	}
}

func TestDualSink_EmitsRequestedAndResolvedStream(t *testing.T) {
	handler := PermissionHandler(func(_ context.Context, _ PermissionRequest) (PermissionResponse, error) {
		return PermissionResponse{Result: ApprovalApproved}, nil
	})
	s := newBoundSink(t, HumanDecisionPolicy{Permission: HumanDecisionAsk}, decisionHandlers{Permission: handler})
	defer s.close()

	_, err := s.RequestDecision(context.Background(), DecisionRequest{Kind: HumanDecisionPermission, Source: "t.perm"})
	if err != nil {
		t.Fatal(err)
	}

	// Drain stream channel.
	var requested, resolved int
	deadline := time.After(250 * time.Millisecond)
loop:
	for {
		select {
		case pl, ok := <-s.stream:
			if !ok {
				break loop
			}
			if pl.Kind == StreamHITLRequested {
				requested++
			}
			if pl.Kind == StreamHITLResolved {
				resolved++
				if pl.HITLResolved.Result != DecisionApproved {
					t.Errorf("resolved.Result: %q", pl.HITLResolved.Result)
				}
			}
		case <-deadline:
			break loop
		}
		if requested == 1 && resolved == 1 {
			break loop
		}
	}
	if requested != 1 || resolved != 1 {
		t.Fatalf("requested=%d resolved=%d", requested, resolved)
	}
}

// TestStreamPayloadSeqMonotonic covers §8.4.6: run-local Seq must be
// strictly monotonic starting from 0.
func TestStreamPayloadSeqMonotonic(t *testing.T) {
	sink := newDualSink("run-x", true, 8, 32, BackpressureDropStream)
	defer sink.close()

	wrapped := wrapWithSeq(sink)
	for i := 0; i < 8; i++ {
		if err := wrapped.EmitStream(StreamPayload{Kind: StreamTextContent, Delta: "x"}); err != nil {
			t.Fatal(err)
		}
	}

	var seqs []uint64
	timeout := time.After(100 * time.Millisecond)
	for i := 0; i < 8; i++ {
		select {
		case p := <-sink.stream:
			seqs = append(seqs, p.Seq)
		case <-timeout:
			t.Fatalf("timed out after %d payloads: %v", i, seqs)
		}
	}
	for i, s := range seqs {
		if s != uint64(i) {
			t.Fatalf("seq[%d] = %d (expected %d), all=%v", i, s, i, seqs)
		}
	}
}
