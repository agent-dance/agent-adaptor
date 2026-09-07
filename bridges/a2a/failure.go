package a2a

import (
	"context"
	"errors"
	"time"

	a2aproto "github.com/a2aproject/a2a-go/v2/a2a"
	adaptor "github.com/agent-dance/agent-adaptor"
)

// failureStatus translates the existing error outcome; it never chooses a new
// execution outcome. A carrier's primary Reason wins over all matching causes,
// including a typed active budget inherited from another run. A qualified final
// hint only classifies bare errors; the caller already established failure.
func failureStatus(info a2aproto.TaskInfoProvider, err error, exposure ExposurePolicy, hint adaptor.FailureReason) *a2aproto.TaskStatusUpdateEvent {
	var re *adaptor.RunError
	var reason adaptor.FailureReason
	var details map[string]any
	if errors.As(err, &re) && re != nil {
		reason = re.Reason
		details = failureDetails(re, exposure)
	} else {
		switch {
		case knownFailureReason(hint):
			reason = hint
		case errors.Is(err, adaptor.ErrActiveExecutionTimeout):
			reason = adaptor.ReasonActiveExecutionTimeout
		case errors.Is(err, context.Canceled), errors.Is(err, adaptor.ErrRunCancelled):
			reason = adaptor.ReasonCancelled
		case errors.Is(err, context.DeadlineExceeded):
			reason = adaptor.ReasonDeadlineExceeded
		}
		details = failureControl(reason, err, nil, exposure)
	}
	state := a2aproto.TaskStateFailed
	if reason == adaptor.ReasonCancelled {
		state = a2aproto.TaskStateCanceled
	}
	return a2aproto.NewStatusUpdateEvent(info, state, failureMessage(info, failureText(reason), details))
}

// failureDetails emits the closed control vocabulary in Text Part.Metadata.
// The core places its selected local budget first in Cause; inspect only that
// carrier's Cause, not outer wrappers or arbitrary Details. Invalid or absent
// limits stay absent rather than borrowing another run's configured budget.
func failureDetails(re *adaptor.RunError, exposure ExposurePolicy) map[string]any {
	if re == nil {
		return nil
	}
	return failureControl(re.Reason, re.Cause, re.Details, exposure)
}

func knownFailureReason(reason adaptor.FailureReason) bool {
	switch reason {
	case adaptor.ReasonActiveExecutionTimeout, adaptor.ReasonApprovalDenied,
		adaptor.ReasonApprovalTimeout, adaptor.ReasonCancelled,
		adaptor.ReasonDeadlineExceeded, adaptor.ReasonAgentError,
		adaptor.ReasonPolicyViolation, adaptor.ReasonInfrastructure:
		return true
	default:
		return false
	}
}

func failureControl(reason adaptor.FailureReason, cause error, details map[string]any, exposure ExposurePolicy) map[string]any {
	if !knownFailureReason(reason) {
		return nil
	}
	out := map[string]any{"code": string(reason)}
	if reason == adaptor.ReasonActiveExecutionTimeout {
		var budget *adaptor.ActiveExecutionTimeoutError
		if errors.As(cause, &budget) && budget != nil && budget.Limit > 0 {
			ms := budget.Limit / time.Millisecond
			if budget.Limit%time.Millisecond != 0 {
				ms++
			}
			out["limit_ms"] = int64(ms)
		}
	}
	if exposure.Diagnostics.IncludeMetadata {
		if metadata := sanitizeRemoteMap(details); len(metadata) > 0 {
			out["metadata"] = metadata
		}
	}
	return out
}

// Status text is category-only even when diagnostics are enabled. Error.Error,
// Message and arbitrary error bodies are not safe presentation text.
func failureText(reason adaptor.FailureReason) string {
	switch reason {
	case adaptor.ReasonActiveExecutionTimeout:
		return "active execution budget exhausted"
	case adaptor.ReasonApprovalDenied:
		return "approval denied"
	case adaptor.ReasonApprovalTimeout:
		return "approval timed out"
	case adaptor.ReasonCancelled:
		return "task cancelled"
	case adaptor.ReasonDeadlineExceeded:
		return "execution deadline exceeded"
	case adaptor.ReasonPolicyViolation:
		return "execution policy violated"
	case adaptor.ReasonInfrastructure:
		return "execution infrastructure failed"
	default:
		return "agent run failed"
	}
}
