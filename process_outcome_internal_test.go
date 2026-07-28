package adaptor

import (
	"context"
	"errors"
	"testing"

	"github.com/agent-dance/agent-adaptor/driver"
)

func TestPendingApprovalFailureWinsConcurrentCancellation(t *testing.T) {
	sink := newEventSink(eventSinkConfig{runID: "run-pending-priority"})
	defer sink.abort()
	sink.setPendingFailure(&driver.RunFailure{
		Code:    driver.FailureReject,
		Message: "approval denied before cancellation",
		HumanDecision: &driver.HumanDecisionFailure{
			Kind:     driver.HumanDecisionPermission,
			Decision: driver.DecisionRejected,
		},
	})
	resp := driver.Response{
		Output:     "partial",
		ExitCode:   -1,
		RawStreams: &driver.RawStreams{Stdout: "partial raw"},
	}

	res, err := finalizeRun("run-pending-priority", sink, resp, context.Canceled)
	if res != nil {
		t.Fatalf("Result = %#v, want nil", res)
	}
	var runErr *RunError
	if !errors.As(err, &runErr) || !errors.Is(err, ErrApprovalDenied) {
		t.Fatalf("error = %T %v, want approval *RunError", err, err)
	}
	if errors.Is(err, context.Canceled) {
		t.Fatalf("pending approval failure was replaced by cancellation: %v", err)
	}
	if runErr.Result == nil || runErr.Result.Text != "partial" || runErr.Result.Raw().Stdout != "partial raw" {
		t.Fatalf("partial Result = %#v", runErr.Result)
	}
}
