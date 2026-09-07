package a2adelegation

// The neutral private controller only measures this call. It does not invoke
// the core engine, dispatch a Driver, or read a parent budget through context.
import (
	"context"
	"errors"
	"time"

	"github.com/agent-dance/agent-adaptor/capability"
	clienta2a "github.com/agent-dance/agent-adaptor/clients/a2a"
)

func (r *delegationRun) contextFailure() *DelegationError {
	if r.budget.SelectedCause() == r.expired {
		return &DelegationError{Code: "active_execution_timeout", Message: safeFailure("active_execution_timeout"), Cause: errors.Join(r.expired, context.Cause(r.ctx), context.Cause(r.parent)), Metadata: map[string]any{"limit_ms": milliseconds(r.expired.Limit)}}
	}
	if r.ctx.Err() == nil && r.parent.Err() == nil {
		return nil
	}
	code := "cancelled"
	if r.parent.Err() == context.DeadlineExceeded {
		code = "deadline_exceeded"
	}
	return &DelegationError{Code: code, Message: safeFailure(code), Retryable: code == "cancelled", Cause: errors.Join(context.Cause(r.parent), context.Cause(r.ctx), r.parent.Err())}
}
func (r *delegationRun) complete(err error) *DelegationError {
	if err != nil {
		d := delegationErr(err)
		switch d.Code {
		case "workflow_before_failed", "agent_unavailable", "stream_unavailable", "cancelled", "stream_interrupted", "delegation_error":
			if cause := r.contextFailure(); cause != nil {
				cause.Cause = errors.Join(cause.Cause, err)
				return cause
			}
		}
		copy := *d
		copy.Metadata = cloneAnyMap(d.Metadata)
		// A previously classified primary is retained; context is only evidence.
		copy.Cause = errors.Join(d.Cause, context.Cause(r.ctx), context.Cause(r.parent))
		return &copy
	}
	if finishErr := r.budget.FinishExecution(); finishErr != nil {
		if d := r.contextFailure(); d != nil {
			return d
		}
		return &DelegationError{Code: "infrastructure_error", Message: "execution budget finalization failed", Cause: finishErr}
	}
	return nil
}
func outerInvocation(base DelegationEvent, phase capability.Phase, at time.Time) capability.Invocation {
	return capability.Invocation{InvocationID: relayTuple("delegation", base.DelegationID), Ref: capability.Ref{Kind: capability.Subagent, Key: base.AgentKey, Operation: "spawn"}, Phase: phase, Source: capability.Host, Evidence: capability.HostLifecycle, ScopeID: base.ScopeID, ParentScopeID: base.ParentScopeID, ParentToolCallID: base.ParentToolCallID, OccurredAt: at.UTC()}
}
func boundedHook(ctx context.Context, call func() error) error {
	result := make(chan error, 1)
	go func() {
		var err error
		defer func() {
			if recover() != nil {
				err = errors.New("delegation lifecycle hook panic")
			}
			result <- err
		}()
		err = call()
	}()
	select {
	case err := <-result:
		return err
	case <-ctx.Done():
		return context.Cause(ctx)
	}
}
func (r *delegationRun) cancelKnown(ctx context.Context, out *DelegationResult) {
	cancelCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), remoteCancelTimeout)
	defer cancel()
	err := boundedHook(cancelCtx, func() error {
		_, err := r.client.CancelTask(cancelCtx, clienta2a.CancelTaskRequest{TaskID: r.taskID, Tenant: r.tenant})
		return err
	})
	if out.RemoteTaskID == "" {
		out.RemoteTaskID = r.taskID
	}
	if err != nil {
		if out.Metadata == nil {
			out.Metadata = map[string]any{}
		}
		value := "failed"
		if cancelCtx.Err() != nil || errors.Is(err, context.DeadlineExceeded) {
			value = "timeout"
		}
		out.Metadata["remote_cancel"] = value
	}
}
func (r *delegationRun) eventMapper(base DelegationEvent) *eventMapper {
	if r.mapper == nil {
		r.mapper = newEventMapper(base, r.statusDecoders...)
	}
	return r.mapper
}
func partialTaskResult(base DelegationResult, task clienta2a.Task, policy DelegationPolicy, full bool) DelegationResult {
	allowed := make([]clienta2a.Artifact, 0, len(task.Artifacts))
	for _, artifact := range task.Artifacts {
		if policyErrorForTask(clienta2a.Task{Artifacts: []clienta2a.Artifact{artifact}}, policy) == nil {
			allowed = append(allowed, artifact)
		}
	}
	task.Artifacts = allowed
	return resultFromTask(base, task, full)
}
