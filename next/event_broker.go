package adaptor

import (
	"context"
	"sync"
	"time"
)

// eventBroker is the sole owner of event ordering and channel lifecycle for
// one run. The mutex is intentionally held through publication: concurrent
// driver and run-service producers therefore have one deterministic receive
// order, and normal close cannot race a send or close the channel twice.
// Explicit abort closes abortCh without taking the mutex first, which releases
// a producer blocked by backpressure before abort waits to seal the channel.
type eventBroker struct {
	events chan Event

	runID     string
	threadKey string
	blocking  bool

	mu       sync.Mutex
	closed   bool
	sequence uint64
	dropped  dropAggregate

	abortOnce sync.Once
	abortCh   chan struct{}
	done      chan struct{}
}

type dropAggregate struct {
	count  int
	byKind map[string]int
	first  uint64
	last   uint64
}

func newEventBroker(runID, threadKey string, buffer int, blocking bool) *eventBroker {
	if buffer <= 0 {
		buffer = defaultEventBuffer
	}
	return &eventBroker{
		events:    make(chan Event, buffer),
		runID:     runID,
		threadKey: threadKey,
		blocking:  blocking,
		abortCh:   make(chan struct{}),
		done:      make(chan struct{}),
	}
}

func (b *eventBroker) publish(ev Event, source *EventSourceMeta) bool {
	return b.publishContext(context.Background(), ev, source)
}

func (b *eventBroker) publishContext(ctx context.Context, ev Event, source *EventSourceMeta) bool {
	if ev == nil {
		return true
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return false
	}
	if b.abortedLocked() {
		// Cancellation is an abort, not a second normal drain phase. Deltas
		// are discarded; a late terminal/critical event may use already-free
		// buffer space but is never allowed to block teardown.
		if eventMayDrop(ev) {
			return false
		}
		ev = b.stampNextLocked(ev, source)
		select {
		case b.events <- ev:
			return true
		default:
			return false
		}
	}

	reliable := b.blocking || !eventMayDrop(ev)
	// A pending marker must receive and be delivered with a lower sequence
	// than the next surviving event. If it cannot fit in drop mode, stamp the
	// current delta afterwards and add it to the same aggregate.
	if b.dropped.count > 0 && !b.flushDroppedLocked(ctx, reliable) {
		if !reliable {
			ev = b.stampNextLocked(ev, source)
			b.recordDropLocked(ev)
		}
		return false
	}

	ev = b.stampNextLocked(ev, source)
	if reliable {
		return b.sendLocked(ctx, ev)
	}
	select {
	case b.events <- ev:
		return true
	default:
		b.recordDropLocked(ev)
		return false
	}
}

func (b *eventBroker) stampNextLocked(ev Event, source *EventSourceMeta) Event {
	b.sequence++
	meta := EventMeta{
		RunID:     b.runID,
		ThreadKey: b.threadKey,
		Sequence:  b.sequence,
		Time:      time.Now().UTC(),
		Source:    cloneEventSourceMeta(source),
	}
	if source != nil {
		meta.TurnID = source.TurnID
	}
	return stampEvent(ev, meta)
}

func (b *eventBroker) sendLocked(ctx context.Context, ev Event) bool {
	select {
	case b.events <- ev:
		return true
	case <-b.abortCh:
		return false
	case <-ctx.Done():
		return false
	}
}

func (b *eventBroker) recordDropLocked(ev Event) {
	seq := ev.Meta().Sequence
	if b.dropped.count == 0 {
		b.dropped.byKind = make(map[string]int)
		b.dropped.first = seq
	}
	b.dropped.count++
	b.dropped.byKind[eventKind(ev)]++
	b.dropped.last = seq
}

func (b *eventBroker) flushDroppedLocked(ctx context.Context, reliable bool) bool {
	if b.dropped.count == 0 {
		return true
	}
	markerSequence := b.sequence + 1
	marker := stampEvent(Dropped{
		Count:         b.dropped.count,
		ByKind:        b.dropped.byKind,
		FirstSequence: b.dropped.first,
		LastSequence:  b.dropped.last,
		Reason:        "slow_consumer",
		Source:        "sdk.event_broker",
		Details: map[string]any{
			"buffer":   cap(b.events),
			"strategy": "drop_deltas",
		},
	}, EventMeta{
		RunID:     b.runID,
		ThreadKey: b.threadKey,
		Sequence:  markerSequence,
		Time:      time.Now().UTC(),
	})

	var sent bool
	if reliable {
		sent = b.sendLocked(ctx, marker)
	} else {
		select {
		case b.events <- marker:
			sent = true
		default:
		}
	}
	if sent {
		b.sequence = markerSequence
		b.dropped = dropAggregate{}
	}
	return sent
}

// close drains every event accepted during normal operation, including a
// final Dropped marker, then closes Events. It can block under backpressure;
// consumers which stop draining must call Stream.Cancel, whose abort path
// releases this wait.
func (b *eventBroker) close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	_ = b.flushDroppedLocked(context.Background(), !b.abortedLocked())
	b.closed = true
	close(b.events)
	close(b.done)
}

// abort is the explicit cancellation path. It immediately releases blocking
// publishers. The execution teardown remains responsible for closing Events,
// which permits an already-produced terminal service event to use free buffer
// space without ever blocking cancellation.
func (b *eventBroker) abort() {
	b.abortOnce.Do(func() { close(b.abortCh) })
}

func (b *eventBroker) abortedLocked() bool {
	select {
	case <-b.abortCh:
		return true
	default:
		return false
	}
}

func cloneEventSourceMeta(in *EventSourceMeta) *EventSourceMeta {
	if in == nil {
		return nil
	}
	out := *in
	return &out
}
