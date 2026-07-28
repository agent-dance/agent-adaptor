package adaptor_test

// Table-driven errors.Is / errors.As coverage of the
// three error paths — business failure (*RunError with sentinel + carried
// Result), context cancellation/deadline, and process crash. One err, one
// verdict point.

import (
	"context"
	"errors"
	"testing"
	"time"

	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/driver"
)

func TestRunErrorPaths(t *testing.T) {
	crashErr := errors.New("codex process exited unexpectedly: signal: killed")

	type tc struct {
		name  string
		setup func(f *fakeDriver)
		run   func(t *testing.T, agent *adaptor.Agent) error

		// Business-failure expectations (wantRunError == true).
		wantRunError bool
		wantReason   adaptor.FailureReason
		wantSentinel error

		// Infrastructure expectations (wantRunError == false).
		wantIs error
	}

	businessFailure := func(code driver.FailureCode, msg string) func(*fakeDriver) {
		return func(f *fakeDriver) {
			f.response = driver.Response{
				Output:  "partial output before failure",
				Summary: "stopped: " + msg,
				Failure: &driver.RunFailure{Code: code, Message: msg},
			}
		}
	}
	plainRun := func(t *testing.T, agent *adaptor.Agent) error {
		t.Helper()
		_, err := agent.Run(context.Background(), "do the thing")
		return err
	}

	cases := []tc{
		// --- Path 1: business failure → *RunError carrying the Result ---
		{
			name:         "business/approval denied",
			setup:        businessFailure(driver.FailureReject, "operator rejected"),
			run:          plainRun,
			wantRunError: true,
			wantReason:   adaptor.ReasonApprovalDenied,
			wantSentinel: adaptor.ErrApprovalDenied,
		},
		{
			name:         "business/approval timeout",
			setup:        businessFailure(driver.FailureTimeout, "decision deadline elapsed"),
			run:          plainRun,
			wantRunError: true,
			wantReason:   adaptor.ReasonApprovalTimeout,
			wantSentinel: adaptor.ErrApprovalTimeout,
		},
		{
			name:         "business/agent error",
			setup:        businessFailure(driver.FailureAgentError, "non-zero exit"),
			run:          plainRun,
			wantRunError: true,
			wantReason:   adaptor.ReasonAgentError,
			wantSentinel: adaptor.ErrAgentFailed,
		},
		{
			name:         "business/cancelled (driver-classified)",
			setup:        businessFailure(driver.FailureCancelled, "handler aborted"),
			run:          plainRun,
			wantRunError: true,
			wantReason:   adaptor.ReasonCancelled,
			wantSentinel: adaptor.ErrRunCancelled,
		},
		{
			name:         "business/policy violation",
			setup:        businessFailure(driver.FailurePolicyError, "schema mode unsupported"),
			run:          plainRun,
			wantRunError: true,
			wantReason:   adaptor.ReasonPolicyViolation,
			wantSentinel: adaptor.ErrPolicyViolation,
		},

		// --- Path 2: context cancellation / deadline → plain error ---
		{
			name:  "context/cancelled",
			setup: func(f *fakeDriver) { f.blockUntilCancelled() },
			run: func(t *testing.T, agent *adaptor.Agent) error {
				t.Helper()
				ctx, cancel := context.WithCancel(context.Background())
				cancel() // cancelled before the driver can finish
				_, err := agent.Run(ctx, "do the thing")
				return err
			},
			wantIs: context.Canceled,
		},
		{
			name:  "context/deadline via WithTimeout",
			setup: func(f *fakeDriver) { f.blockUntilCancelled() },
			run: func(t *testing.T, agent *adaptor.Agent) error {
				t.Helper()
				_, err := agent.Run(context.Background(), "do the thing",
					adaptor.WithTimeout(20*time.Millisecond))
				return err
			},
			wantIs: context.DeadlineExceeded,
		},

		// --- Path 3: process crash → plain error wrapping the cause ---
		{
			name:   "infrastructure/process crash",
			setup:  func(f *fakeDriver) { f.err = crashErr },
			run:    plainRun,
			wantIs: crashErr,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fake := newFakeDriver()
			c.setup(fake)
			agent := adaptor.New(fake)

			err := c.run(t, agent)
			if err == nil {
				t.Fatal("Run returned nil error")
			}

			var runErr *adaptor.RunError
			gotRunError := errors.As(err, &runErr)
			if gotRunError != c.wantRunError {
				t.Fatalf("errors.As(*RunError) = %v, want %v (err = %v)", gotRunError, c.wantRunError, err)
			}

			if c.wantRunError {
				if runErr.Reason != c.wantReason {
					t.Errorf("Reason = %q, want %q", runErr.Reason, c.wantReason)
				}
				if !errors.Is(err, c.wantSentinel) {
					t.Errorf("errors.Is(err, %v) = false", c.wantSentinel)
				}
				// The completed-but-failed run keeps its full Result.
				if runErr.Result == nil {
					t.Fatal("RunError.Result is nil — partial results must survive the failure path")
				}
				if runErr.Result.Text != "partial output before failure" {
					t.Errorf("RunError.Result.Text = %q", runErr.Result.Text)
				}
				if runErr.Result.Summary == "" || runErr.Result.RunID == "" {
					t.Errorf("RunError.Result missing audit fields: %+v", runErr.Result)
				}
				// Exactly one sentinel matches: no cross-talk.
				for _, sentinel := range []error{
					adaptor.ErrApprovalDenied, adaptor.ErrApprovalTimeout,
					adaptor.ErrAgentFailed, adaptor.ErrRunCancelled,
					adaptor.ErrPolicyViolation,
				} {
					if sentinel != c.wantSentinel && errors.Is(err, sentinel) {
						t.Errorf("errors.Is unexpectedly matched %v", sentinel)
					}
				}
			} else {
				if !errors.Is(err, c.wantIs) {
					t.Errorf("errors.Is(err, %v) = false (err = %v)", c.wantIs, err)
				}
			}
		})
	}
}

// TestRunErrorMessage pins the error string shape used in host logs.
func TestRunErrorMessage(t *testing.T) {
	err := &adaptor.RunError{Reason: adaptor.ReasonApprovalDenied, Message: "operator rejected"}
	want := "adaptor: run failed: approval_denied: operator rejected"
	if err.Error() != want {
		t.Errorf("Error() = %q, want %q", err.Error(), want)
	}
}
