package adaptor

import (
	"context"
	"errors"
	"maps"
	"sync"

	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/activebudget"
)

// invocationTerminal is the sole first-terminal record. Its mutex covers only
// candidate comparison and copying, never Driver code, events, callbacks or IO.
type invocationTerminal struct {
	mu      sync.Mutex
	ctx     context.Context
	parent  context.Context
	expired *ActiveExecutionTimeoutError
	reason  FailureReason
	message string
	details map[string]any
	cause   error
	ended   bool
}
type terminalOutcome struct {
	reason  FailureReason
	message string
	details map[string]any
	cause   error
}

func (s *eventSink) bindBudget(ctx context.Context, budget *activebudget.Controller, expired *ActiveExecutionTimeoutError) {
	s.terminal.mu.Lock()
	s.budget = budget
	s.terminal.parent = s.terminal.ctx
	s.terminal.ctx = ctx
	s.terminal.expired = expired
	s.terminal.mu.Unlock()
	context.AfterFunc(ctx, func() { s.recordContext(ctx); s.abort() })
}
func (s *eventSink) recordContext(ctx context.Context) {
	if ctx == nil || ctx.Err() == nil {
		return
	}
	s.recordOutcome(ctx, nil, nil, nil)
}
func (s *eventSink) recordCancellation(ctx context.Context) {
	if ctx.Err() != nil {
		s.recordContext(ctx)
		return
	}
	s.recordOutcome(nil, nil, context.Canceled, nil)
}
func (s *eventSink) recordOutcome(ctx context.Context, failure *driver.RunFailure, err, coordination error) {
	t := &s.terminal
	t.mu.Lock()
	defer t.mu.Unlock()
	s.recordOutcomeLocked(ctx, failure, err, coordination)
}
func (s *eventSink) recordOutcomeLocked(ctx context.Context, failure *driver.RunFailure, err, coordination error) {
	t := &s.terminal
	if t.ended {
		return
	}
	if ctx == nil {
		ctx = t.ctx
	}
	var contextErr, errorCause error
	if ctx != nil && ctx.Err() != nil {
		contextErr = ctx.Err()
		errorCause = context.Cause(ctx)
	}
	t.cause = errors.Join(t.cause, err, coordination, contextErr, errorCause)
	// Selection precedes cancellation propagation. Read the controller's own
	// locked selection, not whichever cause reached the standard child first.
	// The lock order is terminal -> controller; the controller never calls core.
	selected := s.budget.SelectedCause()
	t.cause = errors.Join(t.cause, selected)
	ownExpiry := t.expired != nil && selected == t.expired
	if !ownExpiry && t.parent != nil && t.parent.Err() != nil {
		contextErr, errorCause = t.parent.Err(), context.Cause(t.parent)
		t.cause = errors.Join(t.cause, contextErr, errorCause)
	}
	if t.reason != "" {
		return
	}
	switch {
	case ownExpiry:
		t.reason, t.message = ReasonActiveExecutionTimeout, "active execution budget exhausted"
		// A later parent may carry the same error type. Keep the selected
		// primary instance first for errors.As, retaining all secondary causes.
		// This runs only when selecting the primary, never over an older reason.
		t.cause = errors.Join(selected, t.cause)
	case contextErr != nil:
		switch {
		case contextErr == context.DeadlineExceeded:
			t.reason, t.message = ReasonDeadlineExceeded, "execution deadline exceeded"
		default:
			t.reason, t.message = ReasonCancelled, "execution cancelled"
		}
	case failure != nil:
		t.reason, t.message, t.details = failureReason(failure.Code), failure.Message, maps.Clone(failure.Metadata)
	case coordination != nil:
		t.reason, t.message = ReasonInfrastructure, "Thread coordination failed"
	case err != nil:
		t.reason, t.message = executionErrorReason(err)
	}
}

// executionErrorReason is the fallback when no provider, approval or Thread
// coordination verdict has already been selected. Both terminal selection and
// Result finalization preserve this precedence and the same diagnostic text.
func executionErrorReason(err error) (FailureReason, string) {
	switch {
	case errors.Is(err, ErrActiveExecutionTimeout):
		return ReasonActiveExecutionTimeout, "active execution budget exhausted"
	case errors.Is(err, context.DeadlineExceeded):
		return ReasonDeadlineExceeded, "execution deadline exceeded"
	case errors.Is(err, context.Canceled):
		return ReasonCancelled, "execution cancelled"
	default:
		return ReasonInfrastructure, "execution failed"
	}
}
func (s *eventSink) finishTerminal() {
	s.terminal.mu.Lock()
	s.terminal.ended = true
	s.terminal.mu.Unlock()
}
func (s *eventSink) terminalSnapshot() terminalOutcome {
	s.terminal.mu.Lock()
	defer s.terminal.mu.Unlock()
	return terminalOutcome{s.terminal.reason, s.terminal.message, maps.Clone(s.terminal.details), s.terminal.cause}
}

// finishExecution orders the health seal against every concrete terminal
// candidate. The budget mutex never calls back here, so the lock order is one-way.
func (s *eventSink) finishExecution(ctx context.Context) error {
	s.terminal.mu.Lock()
	defer s.terminal.mu.Unlock()
	s.recordOutcomeLocked(ctx, nil, nil, nil)
	if s.terminal.reason != "" {
		return errors.Join(errApprovalAbort, s.terminal.cause)
	}
	err := s.budget.FinishExecution()
	if err != nil {
		s.recordOutcomeLocked(ctx, nil, err, nil)
	}
	return err
}
