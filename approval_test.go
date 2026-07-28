package adaptor_test

// Approval request, policy, callback, and event contracts.
//
// The current approval policy matrix covers:
//   3 kinds (Permission / PlanReview / Question)
//   × 2 consumption forms (OnApproval callback / *ApprovalRequest event)
//   × outcomes (approve / deny / answer / timeout)
//   with the OnReject / OnTimeout fallbacks (abort / continue / retry),
//   capability-gated retry degradation, exactly-once responder semantics
//   (duplicate answers, answers after run end), and the stable error contract.
//
// Concurrency is channel-synchronized throughout — no sleeps; the only
// elapsed time is real approval Timeouts that deterministically fire
// because no responder ever answers.

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/driver"
)

// Compile-scope proofs for approval options: OnApproval is
// dual-scope, WithBlockingEvents is construction-only.
var (
	_ adaptor.SharedOption = adaptor.OnApproval(nil)
	_ adaptor.Option       = adaptor.WithBlockingEvents()
)

// askOnce returns a runFunc that requests one decision of the given kind
// and encodes the decision outcome in the Response so tests can assert
// which path the driver took.
func askOnce(kind driver.HumanDecisionKind, choices []driver.DecisionChoice) func(context.Context, driver.Request, driver.EventSink) (driver.Response, error) {
	return func(ctx context.Context, _ driver.Request, sink driver.EventSink) (driver.Response, error) {
		ds, ok := sink.(driver.DecisionCapableSink)
		if !ok {
			return driver.Response{}, errors.New("sink is not decision-capable")
		}
		resp, err := ds.RequestDecision(ctx, driver.DecisionRequest{
			Kind:       kind,
			Source:     "tool:bash",
			Prompt:     "allow?",
			ToolCallID: "call-1",
			Choices:    choices,
		})
		if err != nil {
			return driver.Response{Output: "partial output before failure"}, err
		}
		switch resp.Result {
		case driver.DecisionApproved:
			return driver.Response{Output: "approved-path"}, nil
		case driver.DecisionAnswered:
			return driver.Response{Output: "answered:" + resp.Text + "/" + resp.Choice}, nil
		case driver.DecisionRejected:
			return driver.Response{Output: "rejected-continue"}, nil
		case driver.DecisionTimedOut:
			return driver.Response{Output: "timeout-continue"}, nil
		default:
			return driver.Response{Output: "unexpected:" + string(resp.Result)}, nil
		}
	}
}

var questionChoices = []driver.DecisionChoice{
	{Key: "ship-it", Label: "Ship it"},
	{Key: "hold", Label: "Hold the release"},
}

// ---------------------------------------------------------------------------
// Form A · callback (OnApproval)
// ---------------------------------------------------------------------------

func TestApprovalCallbackApprovePermission(t *testing.T) {
	fake := newFakeDriver()
	fake.runFunc = askOnce(driver.HumanDecisionPermission, nil)

	gotReq := make(chan *adaptor.ApprovalRequest, 1)
	agent := adaptor.New(fake, adaptor.OnApproval(func(ctx context.Context, req *adaptor.ApprovalRequest) error {
		gotReq <- req
		return req.Approve(ctx)
	}))

	st := agent.Stream(context.Background(), "deploy")
	events, res, err := collect(st)
	if err != nil {
		t.Fatalf("Result: %v", err)
	}
	if res.Text != "approved-path" {
		t.Fatalf("res.Text = %q, want approved-path", res.Text)
	}

	req := <-gotReq
	if req.Kind != adaptor.ApprovalPermission {
		t.Errorf("Kind = %q", req.Kind)
	}
	if req.Title != "allow?" || req.Source != "tool:bash" || req.ToolCallID != "call-1" {
		t.Errorf("request fields = %q/%q/%q", req.Title, req.Source, req.ToolCallID)
	}
	if req.RunID != st.RunID() {
		t.Errorf("req.RunID = %q, want %q", req.RunID, st.RunID())
	}
	if !strings.HasPrefix(req.ID, st.RunID()+"-dec-") {
		t.Errorf("req.ID = %q, want SDK-minted decision id", req.ID)
	}
	if req.Attempt != 0 {
		t.Errorf("Attempt = %d, want 0", req.Attempt)
	}
	if req.Deadline.IsZero() || req.CreatedAt.IsZero() {
		t.Error("CreatedAt/Deadline must be materialized")
	}

	// Callback form still broadcasts the approval lifecycle on the stream.
	var requested, resolved bool
	for _, ev := range events {
		if n, ok := ev.(adaptor.Notice); ok {
			switch n.Kind {
			case adaptor.NoticeApprovalRequested:
				requested = true
			case adaptor.NoticeApprovalResolved:
				resolved = true
				if n.Data["result"] != string(driver.DecisionApproved) {
					t.Errorf("resolved notice result = %v", n.Data["result"])
				}
			}
		}
		if _, ok := ev.(*adaptor.ApprovalRequest); ok {
			t.Error("callback form must not also enqueue an ApprovalRequest event")
		}
	}
	if !requested || !resolved {
		t.Errorf("approval lifecycle notices missing: requested=%v resolved=%v", requested, resolved)
	}
}

func TestApprovalCallbackDenyAbortsByDefault(t *testing.T) {
	fake := newFakeDriver()
	fake.runFunc = askOnce(driver.HumanDecisionPermission, nil)
	agent := adaptor.New(fake, adaptor.OnApproval(adaptor.DenyAll("not on my watch")))

	res, err := agent.Run(context.Background(), "rm -rf")
	if res != nil {
		t.Fatalf("denied run must not return a Result, got %+v", res)
	}
	var runErr *adaptor.RunError
	if !errors.As(err, &runErr) || !errors.Is(err, adaptor.ErrApprovalDenied) {
		t.Fatalf("want *RunError(ErrApprovalDenied), got %v", err)
	}
	if runErr.Reason != adaptor.ReasonApprovalDenied {
		t.Errorf("Reason = %q", runErr.Reason)
	}
	if !strings.Contains(runErr.Message, "kind=permission") {
		t.Errorf("Message = %q, want kind=permission context", runErr.Message)
	}
	if runErr.Result == nil || runErr.Result.Text != "partial output before failure" {
		t.Errorf("RunError.Result must carry partial output, got %+v", runErr.Result)
	}
}

func TestApprovalCallbackPlanReview(t *testing.T) {
	// Approve → run proceeds.
	fake := newFakeDriver()
	fake.runFunc = askOnce(driver.HumanDecisionPlanReview, nil)
	agent := adaptor.New(fake, adaptor.OnApproval(adaptor.ApproveAll()))
	res, err := agent.Run(context.Background(), "plan")
	if err != nil || res.Text != "approved-path" {
		t.Fatalf("approve: res=%v err=%v", res, err)
	}

	// Deny + OnReject FallbackContinue → the rejection is forwarded to the
	// driver instead of aborting.
	fake2 := newFakeDriver()
	fake2.runFunc = askOnce(driver.HumanDecisionPlanReview, nil)
	agent2 := adaptor.New(fake2,
		adaptor.WithPolicy(adaptor.Policy{Approvals: adaptor.ApprovalPolicy{OnReject: adaptor.FallbackContinue}}),
		adaptor.OnApproval(adaptor.DenyAll("needs rework")),
	)
	res2, err2 := agent2.Run(context.Background(), "plan")
	if err2 != nil || res2.Text != "rejected-continue" {
		t.Fatalf("deny+continue: res=%v err=%v", res2, err2)
	}
}

func TestApprovalQuestionAnswer(t *testing.T) {
	fake := newFakeDriver()
	fake.runFunc = askOnce(driver.HumanDecisionQuestion, questionChoices)
	agent := adaptor.New(fake,
		adaptor.WithPolicy(adaptor.Policy{Approvals: adaptor.ApprovalPolicy{Question: adaptor.QuestionAsk}}),
		adaptor.OnApproval(func(ctx context.Context, req *adaptor.ApprovalRequest) error {
			if req.Kind != adaptor.ApprovalQuestion {
				t.Errorf("Kind = %q", req.Kind)
			}
			if len(req.Choices) != 2 || req.Choices[0].Key != "ship-it" {
				t.Errorf("Choices = %+v", req.Choices)
			}
			return req.Answer(ctx, "ship-it")
		}),
	)

	res, err := agent.Run(context.Background(), "release?")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	// Text carries the option; Choice is set because it matched a key.
	if res.Text != "answered:ship-it/ship-it" {
		t.Errorf("res.Text = %q", res.Text)
	}
}

func TestApprovalQuestionFreeTextAnswer(t *testing.T) {
	fake := newFakeDriver()
	fake.runFunc = askOnce(driver.HumanDecisionQuestion, questionChoices)
	agent := adaptor.New(fake,
		adaptor.WithPolicy(adaptor.Policy{Approvals: adaptor.ApprovalPolicy{Question: adaptor.QuestionAsk}}),
		adaptor.OnApproval(func(ctx context.Context, req *adaptor.ApprovalRequest) error {
			return req.Answer(ctx, "wait for the design review")
		}),
	)

	res, err := agent.Run(context.Background(), "release?")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Text != "answered:wait for the design review/" {
		t.Errorf("res.Text = %q (free text answers carry no Choice)", res.Text)
	}
}

func TestApprovalQuestionDefaultAutoDenied(t *testing.T) {
	// Question mode defaults to auto-deny: the handler is never consulted
	// and the run aborts through OnReject (the conservative default policy).
	fake := newFakeDriver()
	fake.runFunc = askOnce(driver.HumanDecisionQuestion, questionChoices)
	calls := 0
	agent := adaptor.New(fake, adaptor.OnApproval(func(ctx context.Context, req *adaptor.ApprovalRequest) error {
		calls++
		return req.Answer(ctx, "ship-it")
	}))

	_, err := agent.Run(context.Background(), "release?")
	if !errors.Is(err, adaptor.ErrApprovalDenied) {
		t.Fatalf("want ErrApprovalDenied, got %v", err)
	}
	if calls != 0 {
		t.Errorf("handler consulted %d times on an auto-denied question, want 0", calls)
	}
}

func TestApprovalKindMismatch(t *testing.T) {
	// Answer on a permission request is a typed error and does NOT resolve
	// the request; the handler can still respond correctly afterwards.
	fake := newFakeDriver()
	fake.runFunc = askOnce(driver.HumanDecisionPermission, nil)
	var mismatch error
	agent := adaptor.New(fake, adaptor.OnApproval(func(ctx context.Context, req *adaptor.ApprovalRequest) error {
		mismatch = req.Answer(ctx, "ship-it")
		return req.Approve(ctx)
	}))
	res, err := agent.Run(context.Background(), "deploy")
	if err != nil || res.Text != "approved-path" {
		t.Fatalf("res=%v err=%v", res, err)
	}
	if !errors.Is(mismatch, adaptor.ErrApprovalKindMismatch) {
		t.Errorf("Answer on permission = %v, want ErrApprovalKindMismatch", mismatch)
	}

	// Approve on a question is the symmetric mismatch.
	fake2 := newFakeDriver()
	fake2.runFunc = askOnce(driver.HumanDecisionQuestion, questionChoices)
	var mismatch2 error
	agent2 := adaptor.New(fake2,
		adaptor.WithPolicy(adaptor.Policy{Approvals: adaptor.ApprovalPolicy{Question: adaptor.QuestionAsk}}),
		adaptor.OnApproval(func(ctx context.Context, req *adaptor.ApprovalRequest) error {
			mismatch2 = req.Approve(ctx)
			return req.Answer(ctx, "hold")
		}),
	)
	res2, err2 := agent2.Run(context.Background(), "release?")
	if err2 != nil || res2.Text != "answered:hold/hold" {
		t.Fatalf("res=%v err=%v", res2, err2)
	}
	if !errors.Is(mismatch2, adaptor.ErrApprovalKindMismatch) {
		t.Errorf("Approve on question = %v, want ErrApprovalKindMismatch", mismatch2)
	}
}

func TestApproveAllPresetDeniesQuestions(t *testing.T) {
	fake := newFakeDriver()
	fake.runFunc = askOnce(driver.HumanDecisionQuestion, questionChoices)
	agent := adaptor.New(fake,
		adaptor.WithPolicy(adaptor.Policy{Approvals: adaptor.ApprovalPolicy{Question: adaptor.QuestionAsk}}),
		adaptor.OnApproval(adaptor.ApproveAll()),
	)
	_, err := agent.Run(context.Background(), "release?")
	if !errors.Is(err, adaptor.ErrApprovalDenied) {
		t.Fatalf("ApproveAll must deny questions, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Policy auto modes (no human in the loop)
// ---------------------------------------------------------------------------

func TestApprovalsAutoDenyPreset(t *testing.T) {
	fake := newFakeDriver()
	fake.runFunc = askOnce(driver.HumanDecisionPermission, nil)
	calls := 0
	agent := adaptor.New(fake,
		adaptor.WithPolicy(adaptor.Policy{Approvals: adaptor.ApprovalsAutoDeny}),
		adaptor.OnApproval(func(ctx context.Context, req *adaptor.ApprovalRequest) error {
			calls++
			return req.Approve(ctx)
		}),
	)
	_, err := agent.Run(context.Background(), "deploy")
	if !errors.Is(err, adaptor.ErrApprovalDenied) {
		t.Fatalf("want ErrApprovalDenied, got %v", err)
	}
	if calls != 0 {
		t.Errorf("auto-deny consulted the handler %d times, want 0", calls)
	}
}

func TestApprovalAutoApprove(t *testing.T) {
	fake := newFakeDriver()
	fake.runFunc = askOnce(driver.HumanDecisionPermission, nil)
	agent := adaptor.New(fake, adaptor.WithPolicy(adaptor.Policy{
		Approvals: adaptor.ApprovalPolicy{Permission: adaptor.ApprovalAutoApprove},
	}))
	res, err := agent.Run(context.Background(), "deploy")
	if err != nil || res.Text != "approved-path" {
		t.Fatalf("res=%v err=%v", res, err)
	}
}

// ---------------------------------------------------------------------------
// Handler failure modes
// ---------------------------------------------------------------------------

func TestApprovalHandlerErrorSurfacesVerbatim(t *testing.T) {
	fake := newFakeDriver()
	fake.runFunc = askOnce(driver.HumanDecisionPermission, nil)
	cause := errors.New("front-end went away")
	agent := adaptor.New(fake, adaptor.OnApproval(func(context.Context, *adaptor.ApprovalRequest) error {
		return cause
	}))

	res, err := agent.Run(context.Background(), "deploy")
	if res != nil {
		t.Fatalf("res = %+v, want nil", res)
	}
	if !errors.Is(err, cause) {
		t.Fatalf("handler error must surface verbatim on the plain path, got %v", err)
	}
	var runErr *adaptor.RunError
	if errors.As(err, &runErr) {
		t.Errorf("handler error is not a business failure, got %+v", runErr)
	}
}

func TestApprovalHandlerPanicIsAgentError(t *testing.T) {
	fake := newFakeDriver()
	fake.runFunc = askOnce(driver.HumanDecisionPermission, nil)
	agent := adaptor.New(fake, adaptor.OnApproval(func(context.Context, *adaptor.ApprovalRequest) error {
		panic("card renderer exploded")
	}))

	_, err := agent.Run(context.Background(), "deploy")
	var runErr *adaptor.RunError
	if !errors.As(err, &runErr) || !errors.Is(err, adaptor.ErrAgentFailed) {
		t.Fatalf("want *RunError(ErrAgentFailed), got %v", err)
	}
	if runErr.Reason != adaptor.ReasonAgentError || !strings.Contains(runErr.Message, "approval handler panic: card renderer exploded") {
		t.Errorf("Reason=%q Message=%q", runErr.Reason, runErr.Message)
	}
}

func TestApprovalHandlerUnresolvedReturnIsAgentError(t *testing.T) {
	fake := newFakeDriver()
	fake.runFunc = askOnce(driver.HumanDecisionPermission, nil)
	agent := adaptor.New(fake, adaptor.OnApproval(func(context.Context, *adaptor.ApprovalRequest) error {
		return nil // never resolved the request
	}))

	_, err := agent.Run(context.Background(), "deploy")
	var runErr *adaptor.RunError
	if !errors.As(err, &runErr) || runErr.Reason != adaptor.ReasonAgentError {
		t.Fatalf("want agent-error *RunError, got %v", err)
	}
	if !strings.Contains(runErr.Message, "without resolving") {
		t.Errorf("Message = %q", runErr.Message)
	}
}

// ---------------------------------------------------------------------------
// Timeout fallbacks · 2 forms × abort/continue
// ---------------------------------------------------------------------------

func TestApprovalTimeoutFallbackMatrix(t *testing.T) {
	const timeout = 30 * time.Millisecond

	cases := []struct {
		name      string
		onTimeout adaptor.FallbackAction // zero value = inherit (abort)
		callback  bool
		wantText  string // "" means expect ErrApprovalTimeout
	}{
		{name: "callback abort", callback: true},
		{name: "callback continue", callback: true, onTimeout: adaptor.FallbackContinue, wantText: "timeout-continue"},
		{name: "event abort"},
		{name: "event continue", onTimeout: adaptor.FallbackContinue, wantText: "timeout-continue"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := newFakeDriver()
			fake.runFunc = askOnce(driver.HumanDecisionPermission, nil)
			opts := []adaptor.Option{adaptor.WithPolicy(adaptor.Policy{
				Approvals: adaptor.ApprovalPolicy{Timeout: timeout, OnTimeout: tc.onTimeout},
			})}
			unblock := make(chan struct{})
			defer close(unblock)
			if tc.callback {
				// The handler never responds within the deadline: the
				// dispatcher must fall back on its own.
				opts = append(opts, adaptor.OnApproval(func(context.Context, *adaptor.ApprovalRequest) error {
					<-unblock
					return nil
				}))
			}
			agent := adaptor.New(fake, opts...)

			st := agent.Stream(context.Background(), "deploy")
			var captured *adaptor.ApprovalRequest
			for ev := range st.Events() {
				if req, ok := ev.(*adaptor.ApprovalRequest); ok {
					captured = req // deliberately unanswered
				}
			}
			res, err := st.Result()

			if tc.wantText != "" {
				if err != nil || res.Text != tc.wantText {
					t.Fatalf("res=%v err=%v, want %q", res, err, tc.wantText)
				}
			} else {
				var runErr *adaptor.RunError
				if !errors.As(err, &runErr) || !errors.Is(err, adaptor.ErrApprovalTimeout) {
					t.Fatalf("want *RunError(ErrApprovalTimeout), got %v", err)
				}
				if runErr.Reason != adaptor.ReasonApprovalTimeout {
					t.Errorf("Reason = %q", runErr.Reason)
				}
				if !strings.Contains(runErr.Message, "timed out") {
					t.Errorf("Message = %q", runErr.Message)
				}
			}

			if !tc.callback {
				if captured == nil {
					t.Fatal("event form must deliver the *ApprovalRequest")
				}
				// The run has moved on: late answers fail fast.
				if got := captured.Approve(context.Background()); !errors.Is(got, adaptor.ErrApprovalResolved) {
					t.Errorf("late Approve = %v, want ErrApprovalResolved", got)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Retry fallback: capability gating, bounds, renewal
// ---------------------------------------------------------------------------

func TestApprovalRetryUnsupportedDegradesToAbort(t *testing.T) {
	fake := newFakeDriver() // all retry caps remain false
	fake.runFunc = askOnce(driver.HumanDecisionPermission, nil)
	calls := 0
	agent := adaptor.New(fake,
		adaptor.WithPolicy(adaptor.Policy{Approvals: adaptor.ApprovalPolicy{OnReject: adaptor.FallbackRetry}}),
		adaptor.OnApproval(func(ctx context.Context, req *adaptor.ApprovalRequest) error {
			calls++
			return req.Deny(ctx, "no")
		}),
	)

	st := agent.Stream(context.Background(), "deploy")
	events, _, err := collect(st)
	if !errors.Is(err, adaptor.ErrApprovalDenied) {
		t.Fatalf("degraded retry must abort as a denial, got %v", err)
	}
	if calls != 1 {
		t.Errorf("handler called %d times, want 1 (no retry without capability)", calls)
	}
	warned := false
	for _, ev := range events {
		if n, ok := ev.(adaptor.Notice); ok && n.Kind == adaptor.NoticeLifecycle && n.Data["warning"] == "human_decision_retry_unsupported" {
			warned = true
		}
	}
	if !warned {
		t.Error("missing the one-time retry-degradation warning Notice")
	}
}

func TestApprovalRetryExhausted(t *testing.T) {
	fake := newFakeDriver()
	fake.caps = driver.RunPolicyCapabilities{Permission: driver.HumanDecisionSupport{Retry: true}}
	fake.runFunc = askOnce(driver.HumanDecisionPermission, nil)

	var attempts []int
	var ids []string
	agent := adaptor.New(fake,
		adaptor.WithPolicy(adaptor.Policy{Approvals: adaptor.ApprovalPolicy{OnReject: adaptor.FallbackRetry, MaxRetries: 1}}),
		adaptor.OnApproval(func(ctx context.Context, req *adaptor.ApprovalRequest) error {
			attempts = append(attempts, req.Attempt)
			ids = append(ids, req.ID)
			return req.Deny(ctx, "still no")
		}),
	)

	_, err := agent.Run(context.Background(), "deploy")
	var runErr *adaptor.RunError
	if !errors.As(err, &runErr) || !errors.Is(err, adaptor.ErrApprovalDenied) {
		t.Fatalf("want *RunError(ErrApprovalDenied), got %v", err)
	}
	if !strings.Contains(runErr.Message, "exhausted retries") {
		t.Errorf("Message = %q", runErr.Message)
	}
	if len(attempts) != 2 || attempts[0] != 0 || attempts[1] != 1 {
		t.Errorf("attempts = %v, want [0 1] (MaxRetries=1 bounds the re-ask)", attempts)
	}
	if len(ids) == 2 && ids[0] == ids[1] {
		t.Error("retry must renew the request ID")
	}
}

func TestApprovalRetryThenApprove(t *testing.T) {
	fake := newFakeDriver()
	fake.caps = driver.RunPolicyCapabilities{Permission: driver.HumanDecisionSupport{Retry: true}}
	fake.runFunc = askOnce(driver.HumanDecisionPermission, nil)

	calls := 0
	agent := adaptor.New(fake,
		adaptor.WithPolicy(adaptor.Policy{Approvals: adaptor.ApprovalPolicy{OnReject: adaptor.FallbackRetry}}),
		adaptor.OnApproval(func(ctx context.Context, req *adaptor.ApprovalRequest) error {
			calls++
			if calls == 1 {
				return req.Deny(ctx, "look again")
			}
			return req.Approve(ctx)
		}),
	)

	res, err := agent.Run(context.Background(), "deploy")
	if err != nil || res.Text != "approved-path" {
		t.Fatalf("res=%v err=%v", res, err)
	}
	if calls != 2 {
		t.Errorf("handler called %d times, want 2 (deny then retry then approve)", calls)
	}
}

// ---------------------------------------------------------------------------
// Form B · event form: exactly-once responder, duplicates, run end, cancel
// ---------------------------------------------------------------------------

func TestApprovalEventFormApprove(t *testing.T) {
	fake := newFakeDriver()
	fake.runFunc = askOnce(driver.HumanDecisionPermission, nil)
	agent := adaptor.New(fake)

	st := agent.Stream(context.Background(), "deploy")
	var captured *adaptor.ApprovalRequest
	for ev := range st.Events() {
		if req, ok := ev.(*adaptor.ApprovalRequest); ok {
			captured = req
			if err := req.Approve(context.Background()); err != nil {
				t.Errorf("Approve: %v", err)
			}
			// Duplicate responses fail fast, in any combination.
			if err := req.Approve(context.Background()); !errors.Is(err, adaptor.ErrApprovalResolved) {
				t.Errorf("duplicate Approve = %v, want ErrApprovalResolved", err)
			}
			if err := req.Deny(context.Background(), "changed my mind"); !errors.Is(err, adaptor.ErrApprovalResolved) {
				t.Errorf("Deny after Approve = %v, want ErrApprovalResolved", err)
			}
		}
	}
	res, err := st.Result()
	if err != nil || res.Text != "approved-path" {
		t.Fatalf("res=%v err=%v", res, err)
	}
	if captured == nil {
		t.Fatal("no *ApprovalRequest event delivered")
	}
	// After the run ended the answer surface stays closed.
	if err := captured.Approve(context.Background()); !errors.Is(err, adaptor.ErrApprovalResolved) {
		t.Errorf("post-run Approve = %v, want ErrApprovalResolved", err)
	}
}

func TestApprovalEventFormQuestion(t *testing.T) {
	fake := newFakeDriver()
	fake.runFunc = askOnce(driver.HumanDecisionQuestion, questionChoices)
	agent := adaptor.New(fake, adaptor.WithPolicy(adaptor.Policy{
		Approvals: adaptor.ApprovalPolicy{Question: adaptor.QuestionAsk},
	}))

	st := agent.Stream(context.Background(), "release?")
	for ev := range st.Events() {
		if req, ok := ev.(*adaptor.ApprovalRequest); ok {
			if err := req.Answer(context.Background(), "hold"); err != nil {
				t.Errorf("Answer: %v", err)
			}
		}
	}
	res, err := st.Result()
	if err != nil || res.Text != "answered:hold/hold" {
		t.Fatalf("res=%v err=%v", res, err)
	}
}

// TestApprovalEventFormConcurrentDuplicate races two responders on one
// request: exactly one wins, the loser gets ErrApprovalResolved, and the
// run outcome matches the winner. Channel-gated, no sleeps.
func TestApprovalEventFormConcurrentDuplicate(t *testing.T) {
	fake := newFakeDriver()
	fake.runFunc = askOnce(driver.HumanDecisionPermission, nil)
	agent := adaptor.New(fake)

	st := agent.Stream(context.Background(), "deploy")
	var approveErr, denyErr error
	for ev := range st.Events() {
		if req, ok := ev.(*adaptor.ApprovalRequest); ok {
			start := make(chan struct{})
			var wg sync.WaitGroup
			wg.Add(2)
			go func() { defer wg.Done(); <-start; approveErr = req.Approve(context.Background()) }()
			go func() { defer wg.Done(); <-start; denyErr = req.Deny(context.Background(), "no") }()
			close(start)
			wg.Wait()
		}
	}
	res, err := st.Result()

	if (approveErr == nil) == (denyErr == nil) {
		t.Fatalf("exactly one responder must win: approve=%v deny=%v", approveErr, denyErr)
	}
	switch {
	case approveErr == nil:
		if !errors.Is(denyErr, adaptor.ErrApprovalResolved) {
			t.Errorf("losing Deny = %v", denyErr)
		}
		if err != nil || res.Text != "approved-path" {
			t.Errorf("winner approve, but res=%v err=%v", res, err)
		}
	default:
		if !errors.Is(approveErr, adaptor.ErrApprovalResolved) {
			t.Errorf("losing Approve = %v", approveErr)
		}
		if !errors.Is(err, adaptor.ErrApprovalDenied) {
			t.Errorf("winner deny, but err=%v", err)
		}
	}
}

func TestApprovalCancelDuringPending(t *testing.T) {
	fake := newFakeDriver()
	fake.runFunc = askOnce(driver.HumanDecisionPermission, nil)
	agent := adaptor.New(fake)

	st := agent.Stream(context.Background(), "deploy")
	var captured *adaptor.ApprovalRequest
	for ev := range st.Events() {
		if req, ok := ev.(*adaptor.ApprovalRequest); ok {
			captured = req
			st.Cancel() // operator closes the tab mid-approval
		}
	}
	_, err := st.Result()
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
	var runErr *adaptor.RunError
	if errors.As(err, &runErr) {
		t.Errorf("bare cancellation must stay a plain error (D1), got %+v", runErr)
	}
	if captured == nil {
		t.Fatal("no *ApprovalRequest delivered")
	}
	if got := captured.Approve(context.Background()); !errors.Is(got, adaptor.ErrApprovalResolved) {
		t.Errorf("Approve after cancel = %v, want ErrApprovalResolved", got)
	}
}

// OnApproval is dual-scope: the call site overrides the agent default.
func TestOnApprovalNearerScopeWins(t *testing.T) {
	fake := newFakeDriver()
	fake.runFunc = askOnce(driver.HumanDecisionPermission, nil)
	agent := adaptor.New(fake, adaptor.OnApproval(adaptor.DenyAll("default says no")))

	res, err := agent.Run(context.Background(), "deploy", adaptor.OnApproval(adaptor.ApproveAll()))
	if err != nil || res.Text != "approved-path" {
		t.Fatalf("call-site ApproveAll must win: res=%v err=%v", res, err)
	}

	// The agent default is untouched for the next run.
	fake.runFunc = askOnce(driver.HumanDecisionPermission, nil)
	_, err = agent.Run(context.Background(), "deploy")
	if !errors.Is(err, adaptor.ErrApprovalDenied) {
		t.Fatalf("agent default DenyAll must still apply, got %v", err)
	}
}
