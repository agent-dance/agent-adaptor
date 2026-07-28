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
	// normalCapacity is the consumer-configured event buffer. events has one
	// additional physical slot reserved exclusively for the authoritative
	// terminal event. Ordinary producers must never consume that slot.
	normalCapacity int

	mu       sync.Mutex
	closed   bool
	terminal bool
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
	if buffer == int(^uint(0)>>1) {
		panic("adaptor: event buffer is too large to reserve terminal capacity")
	}
	return &eventBroker{
		events:         make(chan Event, buffer+1),
		runID:          runID,
		threadKey:      threadKey,
		blocking:       blocking,
		normalCapacity: buffer,
		abortCh:        make(chan struct{}),
		done:           make(chan struct{}),
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
	if b.closed || b.terminal {
		return false
	}
	return b.publishLocked(ctx, ev, source)
}

// publishTerminal atomically seals the producer side and publishes the one
// authoritative terminal event. A producer already holding the broker lock is
// ordered before the terminal; every later producer is rejected. Keeping the
// seal and publication under the same lock prevents a timed-out run-service
// pump from inserting an event between RunFinished and close.
func (b *eventBroker) publishTerminal(ev Event, source *EventSourceMeta) bool {
	if ev == nil {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed || b.terminal {
		return false
	}
	b.terminal = true

	// On a normal completion, retain the existing guarantee that an aggregate
	// Dropped marker precedes the terminal. Cancellation is different: abort
	// must not wait for a consumer, and pending droppable deltas may be
	// abandoned. The terminal itself is never abandoned.
	if !b.abortedLocked() && b.dropped.count > 0 {
		if !b.flushDroppedLocked(context.Background(), true) {
			// The only normal failure is a concurrent abort. Discard the pending
			// aggregate so the reserved terminal slot remains independently usable.
			b.dropped = dropAggregate{}
		}
	}

	ev = b.stampNextLocked(ev, source)
	// Ordinary publication is capped at normalCapacity, while the physical
	// channel has normalCapacity+1 slots. With b.mu held, no other sender can
	// consume the reserve and the consumer can only create more space; this
	// non-blocking send is therefore guaranteed to succeed.
	select {
	case b.events <- ev:
		return true
	default:
		// Defensive only: reaching this branch would mean an ordinary sender
		// consumed the terminal reserve. Never silently degrade the hard
		// terminal-delivery contract if that internal invariant regresses.
		panic("adaptor: event broker terminal reserve exhausted")
	}
}

// publishLocked performs one publication while b.mu is held.
func (b *eventBroker) publishLocked(ctx context.Context, ev Event, source *EventSourceMeta) bool {
	if b.abortedLocked() {
		// Cancellation is an abort, not a second normal drain phase. Deltas
		// are discarded; a late terminal/critical event may use already-free
		// buffer space but is never allowed to block teardown.
		if eventMayDrop(ev) {
			return false
		}
		if len(b.events) >= b.normalCapacity {
			return false
		}
		ev = b.stampNextLocked(ev, source)
		b.events <- ev
		return true
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
	if len(b.events) >= b.normalCapacity {
		b.recordDropLocked(ev)
		return false
	}
	b.events <- ev
	return true
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
	if len(b.events) < b.normalCapacity {
		b.events <- ev
		return true
	}
	// The public receive-only channel cannot notify the broker when a consumer
	// drains one item. Recheck its length only on the saturated slow path; the
	// mutex keeps producers ordered and the consumer can only lower len(events).
	// This preserves one physical slot for the terminal without adding a second
	// event queue or making cancellation wait for the consumer.
	const probeInterval = time.Millisecond
	timer := time.NewTimer(probeInterval)
	defer func() {
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
	}()
	for len(b.events) >= b.normalCapacity {
		select {
		case <-b.abortCh:
			return false
		case <-ctx.Done():
			return false
		case <-timer.C:
			if len(b.events) >= b.normalCapacity {
				timer.Reset(probeInterval)
			}
		}
	}
	b.events <- ev
	return true
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
			"buffer":   b.normalCapacity,
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
		if len(b.events) < b.normalCapacity {
			b.events <- marker
			sent = true
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
	// publishTerminal owns the final ordering barrier. In normal operation it
	// flushes a pending Dropped marker before RunFinished. In abort mode the
	// contract permits pending events to be abandoned; never append a marker
	// after an already-published terminal.
	if !b.terminal {
		_ = b.flushDroppedLocked(context.Background(), !b.abortedLocked())
	}
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
