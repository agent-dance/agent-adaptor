package subagentstream

import (
	"context"
	"sync/atomic"
	"time"

	aguievents "github.com/ag-ui-protocol/ag-ui/sdks/community/go/pkg/core/events"

	agentadaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/pkg/bridges/agui"
	"github.com/agent-dance/agent-adaptor/pkg/hosttools/a2adelegation"
)

type EventBus interface {
	SubscribeRun(ctx context.Context, runID string) <-chan a2adelegation.DelegationEvent
}

type MuxOptions struct {
	Bus EventBus
}

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
			select {
			case <-ctx.Done():
				return
			case out <- ev.AGUI:
			}
		}
	}()
	return out
}

func Wrap(ctx context.Context, handle agentadaptor.RunHandle, opts MuxOptions) <-chan Event {
	out := make(chan Event, 64)
	var seq atomic.Uint64

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
		aguiEvent := AGUICustomEvent(ev)
		evCopy := ev
		wrapped := Event{ID: seq.Add(1), AGUI: aguiEvent, Subagent: &evCopy}
		select {
		case <-ctx.Done():
			return false
		case out <- wrapped:
			return true
		}
	}
	drainSubagents := func(subagents <-chan a2adelegation.DelegationEvent) bool {
		for subagents != nil {
			select {
			case ev, ok := <-subagents:
				if !ok {
					return true
				}
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
		if opts.Bus != nil {
			subagents = opts.Bus.SubscribeRun(ctx, handle.RunID())
		}
		for parent != nil || subagents != nil {
			select {
			case <-ctx.Done():
				cancelHandle(handle)
				return
			case ev, ok := <-parent:
				if !ok {
					parent = nil
					if !drainSubagents(subagents) {
						return
					}
					subagents = nil
					continue
				}
				if isTerminalAGUI(ev) {
					if !drainSubagents(subagents) {
						return
					}
					if !sendAGUI(ev) {
						return
					}
					parent = nil
					subagents = nil
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
				if !sendSubagent(ev) {
					return
				}
			}
		}
	}()
	return out
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
	if ev.Raw != nil {
		value["raw"] = ev.Raw
	}
	for key, val := range value {
		if val == "" || val == nil {
			delete(value, key)
		}
	}
	return aguievents.NewCustomEvent(string(ev.Kind), aguievents.WithValue(value))
}

func StreamPayload(ev a2adelegation.DelegationEvent) agentadaptor.StreamPayload {
	return agentadaptor.StreamPayload{
		Kind:       "",
		Name:       string(ev.Kind),
		RunID:      ev.RunID,
		ToolCallID: ev.ParentToolCallID,
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
			"delta":               ev.Delta,
			"text":                ev.Text,
			"status":              ev.Status,
		},
	}
}
