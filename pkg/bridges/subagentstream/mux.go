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
	// SubagentMode controls how delegation events are translated to AG-UI events.
	// Default (zero value) is SubagentAsActivity: each SubagentID is aggregated
	// into ACTIVITY_SNAPSHOT / ACTIVITY_DELTA events.
	// Use SubagentAsCustom to preserve the legacy CUSTOM event mapping.
	SubagentMode agui.SubagentMode
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

	// activityAgg is used in SubagentAsActivity mode; active tracker is used in
	// SubagentAsCustom mode for FlushSynthetic with DelegationEvent callbacks.
	activityMode := opts.SubagentMode != agui.SubagentAsCustom
	agg := newActivityAggregator()
	active := newDelegationTracker() // used in custom mode for lifecycle tracking

	// sendSubagentEvents forwards one or more AG-UI events produced from a
	// DelegationEvent. observeContext controls whether ctx.Done() is checked.
	sendSubagentEvents := func(aguiEvents []aguievents.Event, ev a2adelegation.DelegationEvent, observeContext bool) bool {
		for _, ae := range aguiEvents {
			if ae == nil {
				continue
			}
			evCopy := ev
			wrapped := Event{ID: seq.Add(1), AGUI: ae, Subagent: &evCopy}
			if observeContext {
				select {
				case <-ctx.Done():
					return false
				case out <- wrapped:
				}
			} else {
				select {
				case out <- wrapped:
				case <-time.After(terminalFlushTimeout):
					return false
				}
			}
		}
		return true
	}

	processDelegation := func(ev a2adelegation.DelegationEvent, observeContext bool) bool {
		if activityMode {
			aguiEvents := agg.Process(ev)
			return sendSubagentEvents(aguiEvents, ev, observeContext)
		}
		// Legacy custom mode: single CUSTOM event per delegation event.
		return sendSubagentEvents([]aguievents.Event{AGUICustomEvent(ev)}, ev, observeContext)
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
		return processDelegation(ev, true)
	}

	drainSubagents := func(subagents <-chan a2adelegation.DelegationEvent) bool {
		for subagents != nil {
			select {
			case ev, ok := <-subagents:
				if !ok {
					return true
				}
				if !activityMode {
					active.Track(ev)
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

	flushSynthetic := func(kind a2adelegation.DelegationEventKind, status string, err *a2adelegation.DelegationError, observeContext bool) bool {
		if activityMode {
			aguiEvents := agg.FlushSynthetic(status, err)
			// Synthesize a fake DelegationEvent for Subagent field population.
			syntheticEv := a2adelegation.DelegationEvent{Kind: kind, Status: status, Error: err, Time: time.Now()}
			return sendSubagentEvents(aguiEvents, syntheticEv, observeContext)
		}
		return active.FlushSynthetic(kind, status, err, func(ev a2adelegation.DelegationEvent) bool {
			return sendSubagentEvents([]aguievents.Event{AGUICustomEvent(ev)}, ev, observeContext)
		})
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
				flushSynthetic(a2adelegation.DelegationCancelled, "cancelled", &a2adelegation.DelegationError{Code: "parent_cancelled", Message: "parent run context cancelled"}, false)
				stopSubagents()
				cancelHandle(handle)
				return
			case ev, ok := <-parent:
				if !ok {
					parent = nil
					if !drainSubagents(subagents) {
						return
					}
					if !flushSynthetic(a2adelegation.DelegationFailed, "failed", parentFinishedError(), true) {
						return
					}
					stopSubagents()
					continue
				}
				if isTerminalAGUI(ev) {
					if !drainSubagents(subagents) {
						return
					}
					if !flushSynthetic(a2adelegation.DelegationFailed, "failed", parentFinishedError(), true) {
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
				if !activityMode {
					active.Track(ev)
				}
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

// StreamPayload maps a DelegationEvent to the canonical strongly typed stream
// contract. It is retained as the original public entry point.
func StreamPayload(ev a2adelegation.DelegationEvent) agentadaptor.StreamPayload {
	return MapToStreamPayload(ev)
}

// MapToStreamPayload maps a DelegationEvent to an agentadaptor.StreamPayload
// with proper StreamKind constants and a strongly typed SubagentRef. Raw is
// reserved for A2A-specific remote identifiers, artifacts, and status details.
//
// Mapping rules:
//   - subagent.started  → StreamSubagentStart
//   - subagent.status   → StreamSubagentStatus
//   - subagent.text.*   → StreamText*
//   - subagent.reasoning.* → StreamReasoning*
//   - subagent.tool_call.* → StreamToolCall*
//   - terminal events   → StreamSubagentEnd
//   - subagent.artifact → StreamSubagentStatus
func MapToStreamPayload(ev a2adelegation.DelegationEvent) agentadaptor.StreamPayload {
	if ev.DelegationID == "" {
		return agentadaptor.StreamPayload{}
	}
	name := defaultStr(ev.AgentName, ev.AgentKey)
	base := agentadaptor.StreamPayload{
		RunID: ev.RunID,
		Name:  name,
		Delta: ev.Delta,
		Subagent: &agentadaptor.SubagentRef{
			ID:         ev.DelegationID,
			Name:       name,
			Kind:       "delegated",
			Protocol:   defaultStr(ev.Protocol, a2adelegation.ProtocolA2A),
			ToolCallID: ev.ParentToolCallID,
		},
		Raw: map[string]any{
			"remote_task_id":      ev.RemoteTaskID,
			"remote_context_id":   ev.RemoteContextID,
			"remote_message_id":   ev.RemoteMessageID,
			"remote_artifact_id":  ev.RemoteArtifactID,
			"remote_tool_call_id": ev.RemoteToolCallID,
			"parent_tool_call_id": ev.ParentToolCallID,
		},
	}
	if args, ok := ev.Args.(map[string]any); ok {
		base.Args = args
	}
	if result, ok := ev.Result.(map[string]any); ok {
		base.Result = result
	}
	switch ev.Kind {
	case a2adelegation.DelegationStarted:
		base.Kind = agentadaptor.StreamSubagentStart
		base.Raw["status"] = "started"
	case a2adelegation.DelegationStatus:
		base.Kind = agentadaptor.StreamSubagentStatus
		base.Result = map[string]any{"status": ev.Status}
		base.Raw["status"] = ev.Status
	case a2adelegation.DelegationTextStart:
		base.Kind = agentadaptor.StreamTextStart
		base.MessageID = ev.RemoteMessageID
	case a2adelegation.DelegationTextDelta:
		base.Kind = agentadaptor.StreamTextContent
		base.MessageID = ev.RemoteMessageID
	case a2adelegation.DelegationTextEnd:
		base.Kind = agentadaptor.StreamTextEnd
		base.MessageID = ev.RemoteMessageID
	case a2adelegation.DelegationReasoningStart:
		base.Kind = agentadaptor.StreamReasoningStart
		base.MessageID = firstNonEmpty(ev.RemoteMessageID, ev.RemoteArtifactID)
	case a2adelegation.DelegationReasoningDelta:
		base.Kind = agentadaptor.StreamReasoningContent
		base.MessageID = firstNonEmpty(ev.RemoteMessageID, ev.RemoteArtifactID)
	case a2adelegation.DelegationReasoningEnd:
		base.Kind = agentadaptor.StreamReasoningEnd
		base.MessageID = firstNonEmpty(ev.RemoteMessageID, ev.RemoteArtifactID)
	case a2adelegation.DelegationToolCallStart:
		base.Kind = agentadaptor.StreamToolCallStart
		base.ToolCallID = ev.RemoteToolCallID
		base.Name = ev.ToolName
	case a2adelegation.DelegationToolCallArgs:
		base.Kind = agentadaptor.StreamToolCallArgs
		base.ToolCallID = ev.RemoteToolCallID
		base.Name = ev.ToolName
	case a2adelegation.DelegationToolCallResult:
		base.Kind = agentadaptor.StreamToolCallResult
		base.ToolCallID = ev.RemoteToolCallID
		base.Name = ev.ToolName
	case a2adelegation.DelegationToolCallEnd:
		base.Kind = agentadaptor.StreamToolCallEnd
		base.ToolCallID = ev.RemoteToolCallID
		base.Name = ev.ToolName
	case a2adelegation.DelegationArtifactCreated:
		base.Kind = agentadaptor.StreamSubagentStatus
		base.Result = map[string]any{"status": ev.Status}
		base.Raw["status"] = ev.Status
		if ev.Artifact != nil {
			base.Raw["artifact"] = ev.Artifact
		}
	case a2adelegation.DelegationFinished:
		base.Kind = agentadaptor.StreamSubagentEnd
		base.Result = map[string]any{"status": "completed", "text": ev.Text}
		base.Raw["status"] = "completed"
	case a2adelegation.DelegationFailed:
		base.Kind = agentadaptor.StreamSubagentEnd
		base.Result = map[string]any{"status": "failed", "text": ev.Text}
		base.Raw["status"] = "failed"
		if ev.Error != nil {
			base.Error = &agentadaptor.RunFailure{
				Code:    agentadaptor.FailureCode(ev.Error.Code),
				Message: ev.Error.Message,
			}
		}
	case a2adelegation.DelegationCancelled:
		base.Kind = agentadaptor.StreamSubagentEnd
		base.Result = map[string]any{"status": "cancelled", "text": ev.Text}
		base.Raw["status"] = "cancelled"
		if ev.Error != nil {
			base.Error = &agentadaptor.RunFailure{
				Code:    agentadaptor.FailureCode(ev.Error.Code),
				Message: ev.Error.Message,
			}
		}
	case a2adelegation.DelegationInputRequired:
		base.Kind = agentadaptor.StreamSubagentEnd
		base.Result = map[string]any{"status": "input_required", "text": ev.Text}
		base.Raw["status"] = "input_required"
	case a2adelegation.DelegationStreamDropped:
		base.Kind = agentadaptor.StreamDropped
	default:
		// Unknown kind: emit as subagent.status rather than empty Kind to avoid
		// collision with Codex opaque notification handling.
		base.Kind = agentadaptor.StreamSubagentStatus
		base.Result = map[string]any{"status": ev.Status}
		base.Raw["status"] = ev.Status
	}
	for key, value := range base.Raw {
		if value == "" || value == nil {
			delete(base.Raw, key)
		}
	}
	return base
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
