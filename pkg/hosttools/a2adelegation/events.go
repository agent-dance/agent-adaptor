package a2adelegation

import (
	"context"
	"sync"
	"time"
)

const subscriberBuffer = 32

type EventBus struct {
	mu          sync.Mutex
	subscribers map[string]map[chan DelegationEvent]struct{}
	replay      map[string][]DelegationEvent
	replayLimit int
	terminal    map[string]map[string]struct{}
}

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
		select {
		case ch <- ev:
		default:
		}
	}
	return true
}

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
		close(out)
	}()
	return out
}

func isTerminal(kind DelegationEventKind) bool {
	switch kind {
	case DelegationFinished, DelegationFailed, DelegationCancelled, DelegationInputRequired:
		return true
	default:
		return false
	}
}
