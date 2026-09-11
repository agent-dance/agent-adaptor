package adaptor

import (
	"context"
	"fmt"
	"time"

	"github.com/agent-dance/agent-adaptor/capability"
	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/capabilityobs"
	"github.com/agent-dance/agent-adaptor/internal/todoobs"
)

const observationTimeout = 100 * time.Millisecond

type observerContextKey struct{}
type runObserverState struct {
	callback RunEventObserver
	disabled bool
}

func invalidRunEvent() error { return fmt.Errorf("adaptor: invalid run event") }
func validSourceChain(source *EventSourceMeta) bool {
	seen := map[*EventSourceMeta]bool{}
	for source != nil {
		if seen[source] || len(seen) >= 8 {
			return false
		}
		seen[source] = true
		for _, s := range []string{source.RunID, source.ThreadID, source.TurnID, source.ScopeID, source.ToolCallID, source.InvocationID, source.DelegationID} {
			if !capabilityobs.ValidText(s, 2048, false) {
				return false
			}
		}
		source = source.Upstream
	}
	return true
}
func validObservationEvent(ev Event) bool {
	if ev == nil || !validSourceChain(ev.Meta().Source) {
		return false
	}
	switch e := ev.(type) {
	case CapabilityInvocation:
		return capabilityobs.Validate(e.Invocation) == nil
	case TodoUpdated:
		return todoobs.Validate(e.Snapshot) == nil
	default:
		return true
	}
}
func validHostEvent(ev Event) bool {
	ev = cloneEventValue(ev)
	if !validObservationEvent(ev) {
		return false
	}
	switch e := ev.(type) {
	case CapabilityInvocation:
		return e.Invocation.Source == capability.Host || e.Invocation.Source == capability.Relay
	case TodoUpdated, SubagentUpdate, Notice, Dropped:
		return true
	default:
		return false
	}
}
func validateObservationPayload(p driver.StreamPayload) error {
	if p.Kind != driver.StreamCapabilityInvocation && p.Kind != driver.StreamTodoUpdated {
		if p.Capability != nil || p.Todo != nil {
			return invalidRunEvent()
		}
		return nil
	}
	if p.Args != nil || p.Result != nil || p.Raw != nil || p.HITLRequested != nil || p.HITLResolved != nil || p.Role != "" || p.Delta != "" || p.Name != "" || p.Error != nil || p.Usage != nil || p.MessageID != "" || p.ToolCallID != "" || p.ScopeID != "" || p.ParentScopeID != "" || p.ParentToolCallID != "" {
		return invalidRunEvent()
	}
	switch p.Kind {
	case driver.StreamCapabilityInvocation:
		if p.Capability == nil || p.Todo != nil || capabilityobs.Validate(*p.Capability) != nil {
			return invalidRunEvent()
		}
	case driver.StreamTodoUpdated:
		if p.Todo == nil || p.Capability != nil || todoobs.Validate(*p.Todo) != nil {
			return invalidRunEvent()
		}
	}
	return nil
}
func (s *eventSink) installObservers(ctx context.Context, info RunEventInfo, observers []RunEventObserver) {
	s.broker.mu.Lock()
	defer s.broker.mu.Unlock()
	s.observerCtx = ctx
	s.observerInfo = info
	s.observers = make([]runObserverState, len(observers))
	for i, callback := range observers {
		s.observers[i].callback = callback
	}
	s.broker.observe = s.observeEvent
}
func (s *eventSink) stopObservers() {
	s.broker.mu.Lock()
	defer s.broker.mu.Unlock()
	s.broker.observe = nil
	s.observers = nil
}

// limitObserverCleanup installs the earliest total cleanup deadline without
// taking the publication gate; later callbacks cannot renew that budget.
func (s *eventSink) limitObserverCleanup(deadline time.Time) {
	next := deadline.UnixNano()
	for {
		current := s.observerDeadline.Load()
		if current != 0 && current <= next {
			return
		}
		if s.observerDeadline.CompareAndSwap(current, next) {
			return
		}
	}
}
func (s *eventSink) observeEvent(ev Event) []Event {
	switch ev.(type) {
	case CapabilityInvocation, TodoUpdated:
	default:
		return nil
	}
	var notices []Event
	for i := range s.observers {
		state := &s.observers[i]
		if state.callback == nil || state.disabled {
			continue
		}
		parent := s.observerCtx
		if parent.Err() != nil {
			s.limitObserverCleanup(time.Now().Add(runResourceCleanupTimeout))
			parent = context.WithoutCancel(parent)
		}
		deadline := time.Now().Add(observationTimeout)
		if cleanup := s.observerDeadline.Load(); cleanup != 0 && cleanup < deadline.UnixNano() {
			deadline = time.Unix(0, cleanup)
		}
		if !time.Now().Before(deadline) {
			state.disabled = true
			notices = append(notices, Notice{Kind: NoticeRuntime, Data: map[string]any{"code": "observation_disabled", "reason": "timeout", "observer_index": i}})
			continue
		}
		ctx, cancel := context.WithDeadline(context.WithValue(parent, observerContextKey{}, s), deadline)
		type outcome struct {
			err      error
			panicked bool
		}
		done := make(chan outcome, 1)
		callback := state.callback
		private := WithEventMeta(ev, ev.Meta())
		info := s.observerInfo
		go func() {
			out := outcome{}
			defer func() {
				if recover() != nil {
					out.panicked = true
				}
				done <- out
			}()
			out.err = callback(ctx, info, private)
		}()
		reason := ""
		select {
		case out := <-done:
			// A callback may return nil precisely when its context expires.
			// Completion cannot win over the authoritative cancellation fence.
			if ctx.Err() != nil || !time.Now().Before(deadline) {
				reason = "timeout"
			} else if out.panicked {
				reason = "panic"
			} else if out.err != nil {
				reason = "error"
			}
		case <-ctx.Done():
			reason = "timeout"
		}
		cancel()
		if reason != "" {
			state.disabled = true
			notices = append(notices, Notice{Kind: NoticeRuntime, Data: map[string]any{"code": "observation_disabled", "reason": reason, "observer_index": i}})
		}
	}
	return notices
}
