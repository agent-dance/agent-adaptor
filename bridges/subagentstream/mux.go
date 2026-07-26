package subagentstream

import (
	"context"
	"sync/atomic"
	"time"

	aguievents "github.com/ag-ui-protocol/ag-ui/sdks/community/go/pkg/core/events"

	agentadaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/bridges/agui"
	"github.com/agent-dance/agent-adaptor/hosttools/a2adelegation"
)

type EventBus interface {
	SubscribeRun(ctx context.Context, runID string) <-chan a2adelegation.DelegationEvent
}

type MuxOptions struct {
	Bus EventBus
}

const terminalFlushTimeout = 250 * time.Millisecond

type Event struct {
	ID       uint64
	AGUI     aguievents.Event
	Raw      *agentadaptor.StreamPayload
	Subagent *a2adelegation.DelegationEvent
}

func WrapAGUI(ctx context.Context, handle agentadaptor.RunHandle, opts MuxOptions) <-chan aguievents.Event {
	out := make(chan aguievents.Event, 32)
	go func() {
		defer close(out)
		for ev := range Wrap(ctx, handle, opts) {
			if ev.AGUI == nil {
				continue
			}
			if !sendAGUIWithCancelGrace(ctx, out, ev.AGUI) {
				return
			}
		}
	}()
	return out
}

func Wrap(ctx context.Context, handle agentadaptor.RunHandle, opts MuxOptions) <-chan Event {
	out := make(chan Event, 64)
	var seq atomic.Uint64

	sendSubagentEvent := func(ev a2adelegation.DelegationEvent, observeContext bool) bool {
		aguiEvent := AGUICustomEvent(ev)
		evCopy := ev
		wrapped := Event{ID: seq.Add(1), AGUI: aguiEvent, Subagent: &evCopy}
		if observeContext {
			select {
			case <-ctx.Done():
				return false
			case out <- wrapped:
				return true
			}
		}
		select {
		case out <- wrapped:
			return true
		case <-time.After(terminalFlushTimeout):
			return false
		}
	}
	sendAGUI := func(ev aguievents.Event) bool {
		wrapped := Event{ID: seq.Add(1), AGUI: ev}
		select {
		case <-ctx.Done():
			return false
		case out <- wrapped:
			return true
		}
	}
	sendSubagent := func(ev a2adelegation.DelegationEvent) bool {
		return sendSubagentEvent(ev, true)
	}
	active := newDelegationTracker()
	drainSubagents := func(subagents <-chan a2adelegation.DelegationEvent) bool {
		for subagents != nil {
			select {
			case ev, ok := <-subagents:
				if !ok {
					return true
				}
				active.Track(ev)
				if !sendSubagent(ev) {
					return false
				}
			default:
				return true
			}
		}
		return true
	}

	go func() {
		defer close(out)
		parent := agui.WrapWithContext(ctx, handle)
		var subagents <-chan a2adelegation.DelegationEvent
		var cancelSubagents context.CancelFunc
		if opts.Bus != nil {
			subagentCtx, cancel := context.WithCancel(ctx)
			cancelSubagents = cancel
			defer cancelSubagents()
			subagents = opts.Bus.SubscribeRun(subagentCtx, handle.RunID())
		}
		stopSubagents := func() {
			if cancelSubagents != nil {
				cancelSubagents()
				cancelSubagents = nil
			}
			subagents = nil
		}
		for parent != nil || subagents != nil {
			select {
			case <-ctx.Done():
				active.FlushSynthetic(a2adelegation.DelegationCancelled, "cancelled", &a2adelegation.DelegationError{Code: "parent_cancelled", Message: "parent run context cancelled"}, func(ev a2adelegation.DelegationEvent) bool {
					return sendSubagentEvent(ev, false)
				})
				stopSubagents()
				cancelHandle(handle)
				return
			case ev, ok := <-parent:
				if !ok {
					parent = nil
					if !drainSubagents(subagents) {
						return
					}
					if !active.FlushSynthetic(a2adelegation.DelegationFailed, "failed", parentFinishedError(), sendSubagent) {
						return
					}
					stopSubagents()
					continue
				}
				if isTerminalAGUI(ev) {
					if !drainSubagents(subagents) {
						return
					}
					if !active.FlushSynthetic(a2adelegation.DelegationFailed, "failed", parentFinishedError(), sendSubagent) {
						return
					}
					if !sendAGUI(ev) {
						return
					}
					parent = nil
					stopSubagents()
					continue
				}
				if !sendAGUI(ev) {
					return
				}
			case ev, ok := <-subagents:
				if !ok {
					subagents = nil
					continue
				}
				active.Track(ev)
				if !sendSubagent(ev) {
					return
				}
			}
		}
	}()
	return out
}

func sendAGUIWithCancelGrace(ctx context.Context, out chan<- aguievents.Event, ev aguievents.Event) bool {
	select {
	case out <- ev:
		return true
	default:
	}
	if ctx.Err() != nil {
		select {
		case out <- ev:
			return true
		case <-time.After(terminalFlushTimeout):
			return false
		}
	}
	select {
	case out <- ev:
		return true
	case <-ctx.Done():
		select {
		case out <- ev:
			return true
		case <-time.After(terminalFlushTimeout):
			return false
		}
	}
}

type delegationTracker struct {
	active map[string]a2adelegation.DelegationEvent
	order  []string
}

func newDelegationTracker() *delegationTracker {
	return &delegationTracker{active: map[string]a2adelegation.DelegationEvent{}}
}

func (t *delegationTracker) Track(ev a2adelegation.DelegationEvent) {
	if ev.DelegationID == "" {
		return
	}
	if isTerminalDelegation(ev.Kind) {
		delete(t.active, ev.DelegationID)
		return
	}
	if _, exists := t.active[ev.DelegationID]; !exists {
		t.order = append(t.order, ev.DelegationID)
	}
	t.active[ev.DelegationID] = ev
}

func (t *delegationTracker) FlushSynthetic(kind a2adelegation.DelegationEventKind, status string, err *a2adelegation.DelegationError, send func(a2adelegation.DelegationEvent) bool) bool {
	for _, delegationID := range t.order {
		ev, ok := t.active[delegationID]
		if !ok {
			continue
		}
		ev.Kind = kind
		ev.Status = status
		ev.Error = err
		ev.Time = time.Now()
		delete(t.active, delegationID)
		if !send(ev) {
			return false
		}
	}
	return true
}

func cancelHandle(handle agentadaptor.RunHandle) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = handle.Cancel(ctx)
}

func isTerminalAGUI(ev aguievents.Event) bool {
	if ev == nil {
		return false
	}
	switch ev.Type() {
	case aguievents.EventTypeRunFinished, aguievents.EventTypeRunError:
		return true
	default:
		return false
	}
}

func isTerminalDelegation(kind a2adelegation.DelegationEventKind) bool {
	switch kind {
	case a2adelegation.DelegationFinished, a2adelegation.DelegationFailed, a2adelegation.DelegationCancelled, a2adelegation.DelegationInputRequired:
		return true
	default:
		return false
	}
}

func parentFinishedError() *a2adelegation.DelegationError {
	return &a2adelegation.DelegationError{Code: "parent_finished", Message: "parent run finished before subagent terminal event"}
}

func AGUICustomEvent(ev a2adelegation.DelegationEvent) aguievents.Event {
	value := map[string]any{
		"runId":            ev.RunID,
		"parentToolCallId": ev.ParentToolCallID,
		"delegationId":     ev.DelegationID,
		"agentKey":         ev.AgentKey,
		"agentName":        ev.AgentName,
		"remoteProtocol":   ev.Protocol,
		"remoteTaskId":     ev.RemoteTaskID,
		"remoteContextId":  ev.RemoteContextID,
		"messageId":        ev.RemoteMessageID,
		"artifactId":       ev.RemoteArtifactID,
		"remoteToolCallId": ev.RemoteToolCallID,
		"toolName":         ev.ToolName,
		"args":             ev.Args,
		"result":           ev.Result,
		"delta":            ev.Delta,
		"text":             ev.Text,
		"status":           ev.Status,
	}
	if ev.Artifact != nil {
		value["artifact"] = ev.Artifact
	}
	if ev.Error != nil {
		value["error"] = ev.Error
	}
	for key, val := range value {
		if val == "" || val == nil {
			delete(value, key)
		}
	}
	return aguievents.NewCustomEvent(string(ev.Kind), aguievents.WithValue(value))
}

func StreamPayload(ev a2adelegation.DelegationEvent) agentadaptor.StreamPayload {
	toolCallID := ev.ParentToolCallID
	if ev.RemoteToolCallID != "" {
		toolCallID = ev.RemoteToolCallID
	}
	payload := agentadaptor.StreamPayload{
		Kind:       "",
		Name:       string(ev.Kind),
		RunID:      ev.RunID,
		ToolCallID: toolCallID,
		MessageID:  ev.RemoteMessageID,
		Delta:      ev.Delta,
		Raw: map[string]any{
			"delegation_id":       ev.DelegationID,
			"agent_key":           ev.AgentKey,
			"agent_name":          ev.AgentName,
			"parent_tool_call_id": ev.ParentToolCallID,
			"remote_protocol":     ev.Protocol,
			"remote_task_id":      ev.RemoteTaskID,
			"remote_context_id":   ev.RemoteContextID,
			"remote_message_id":   ev.RemoteMessageID,
			"remote_artifact_id":  ev.RemoteArtifactID,
			"remote_tool_call_id": ev.RemoteToolCallID,
			"tool_name":           ev.ToolName,
			"args":                ev.Args,
			"result":              ev.Result,
			"delta":               ev.Delta,
			"text":                ev.Text,
			"status":              ev.Status,
		},
	}
	if args, ok := ev.Args.(map[string]any); ok {
		payload.Args = args
	}
	if result, ok := ev.Result.(map[string]any); ok {
		payload.Result = result
	}
	return payload
}
