package a2adelegation

import (
	"context"
	"sync"
	"time"
)

const subscriberBuffer = 32

// EventBus publishes DelegationEvent values by leader RunID, retains a bounded
// replay window for late subscribers, and suppresses duplicate terminal events
// per delegation. It is safe for concurrent publishers and subscribers.
type EventBus struct {
	mu          sync.Mutex
	subscribers map[string]map[chan DelegationEvent]struct{}
	replay      map[string][]DelegationEvent
	replayLimit int
	terminal    map[string]map[string]struct{}
}

// NewEventBus constructs an EventBus. replayLimit is the maximum retained
// event count per run; zero disables replay and negative values become zero.
func NewEventBus(replayLimit int) *EventBus {
	if replayLimit < 0 {
		replayLimit = 0
	}
	return &EventBus{
		subscribers: map[string]map[chan DelegationEvent]struct{}{},
		replay:      map[string][]DelegationEvent{},
		replayLimit: replayLimit,
		terminal:    map[string]map[string]struct{}{},
	}
}

// Publish accepts one event and returns whether it entered the bus. Events
// without RunID and duplicate terminal events are rejected. Subscriber
// backpressure is summarized as DelegationStreamDropped.
func (b *EventBus) Publish(ev DelegationEvent) bool {
	if b == nil || ev.RunID == "" {
		return false
	}
	if ev.Time.IsZero() {
		ev.Time = time.Now().UTC()
	}
	b.mu.Lock()
	defer b.mu.Unlock()

	if isTerminal(ev.Kind) {
		if b.terminal[ev.RunID] == nil {
			b.terminal[ev.RunID] = map[string]struct{}{}
		}
		if _, exists := b.terminal[ev.RunID][ev.DelegationID]; exists {
			return false
		}
		b.terminal[ev.RunID][ev.DelegationID] = struct{}{}
	}

	if b.replayLimit > 0 {
		buf := append(b.replay[ev.RunID], ev)
		if len(buf) > b.replayLimit {
			buf = buf[len(buf)-b.replayLimit:]
		}
		b.replay[ev.RunID] = buf
	}
	for ch := range b.subscribers[ev.RunID] {
		deliverSubscriber(ch, ev)
	}
	return true
}

func deliverSubscriber(ch chan DelegationEvent, ev DelegationEvent) {
	select {
	case ch <- ev:
		return
	default:
	}
	if !isPriorityEvent(ev) {
		var oldest DelegationEvent
		select {
		case oldest = <-ch:
		default:
			return
		}
		dropEvent := backpressureEvent(ev, []DelegationEvent{oldest, ev})
		select {
		case ch <- dropEvent:
		default:
		}
		return
	}
	dropped := make([]DelegationEvent, 0, 2)
	for i := 0; i < 2; i++ {
		select {
		case oldest := <-ch:
			dropped = append(dropped, oldest)
		default:
		}
	}
	dropEvent := backpressureEvent(ev, dropped)
	select {
	case ch <- dropEvent:
	default:
	}
	select {
	case ch <- ev:
	default:
	}
}

func backpressureEvent(current DelegationEvent, dropped []DelegationEvent) DelegationEvent {
	event := current
	event.Kind = DelegationStreamDropped
	event.Sequence = 0
	event.Delta = ""
	event.Args = nil
	event.Result = nil
	event.Artifact = nil
	event.Raw = map[string]any{
		"reason":        "event_bus_backpressure",
		"dropped_count": len(dropped),
	}
	details := make([]map[string]any, 0, len(dropped))
	for _, item := range dropped {
		details = append(details, map[string]any{
			"delegation_id": item.DelegationID,
			"kind":          string(item.Kind),
			"sequence":      item.Sequence,
		})
	}
	event.Raw["dropped_events"] = details
	return event
}

func isPriorityEvent(ev DelegationEvent) bool {
	return ev.Kind == DelegationTextEnd || ev.Kind == DelegationReasoningEnd ||
		ev.Kind == DelegationToolCallEnd || ev.Kind == DelegationStreamDropped || isTerminal(ev.Kind)
}

// ClearRun closes current subscribers and removes replay and terminal state for
// runID. It does not remove results recorded by Service.
func (b *EventBus) ClearRun(runID string) {
	if b == nil || runID == "" {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for ch := range b.subscribers[runID] {
		closeDelegationChannel(ch)
	}
	delete(b.subscribers, runID)
	delete(b.replay, runID)
	delete(b.terminal, runID)
}

// SubscribeRun returns the retained replay followed by live events for runID.
// Canceling ctx removes the subscription and closes the channel.
func (b *EventBus) SubscribeRun(ctx context.Context, runID string) <-chan DelegationEvent {
	if b == nil || runID == "" {
		out := make(chan DelegationEvent)
		close(out)
		return out
	}
	b.mu.Lock()
	replay := append([]DelegationEvent(nil), b.replay[runID]...)
	out := make(chan DelegationEvent, len(replay)+subscriberBuffer)
	for _, ev := range replay {
		out <- ev
	}
	if b.subscribers[runID] == nil {
		b.subscribers[runID] = map[chan DelegationEvent]struct{}{}
	}
	b.subscribers[runID][out] = struct{}{}
	b.mu.Unlock()

	go func() {
		<-ctx.Done()
		b.mu.Lock()
		delete(b.subscribers[runID], out)
		if len(b.subscribers[runID]) == 0 {
			delete(b.subscribers, runID)
		}
		b.mu.Unlock()
		closeDelegationChannel(out)
	}()
	return out
}

func closeDelegationChannel(ch chan DelegationEvent) {
	defer func() { _ = recover() }()
	close(ch)
}

func isTerminal(kind DelegationEventKind) bool {
	switch kind {
	case DelegationFinished, DelegationFailed, DelegationCancelled, DelegationInputRequired:
		return true
	default:
		return false
	}
}
