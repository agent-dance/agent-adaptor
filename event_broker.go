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
	// normalCapacity is the consumer-configured event buffer. events has two
	// additional physical slots reserved for the final loss summary and
	// authoritative terminal event. Ordinary producers must never consume those slots.
	normalCapacity int

	// observe runs after stamping, before user enqueue, under this receive-order lock.
	observe  func(Event) []Event
	mu       eventPublicationGate
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
	if buffer > int(^uint(0)>>1)-2 {
		panic("adaptor: event buffer is too large to reserve terminal capacity")
	}
	return &eventBroker{
		events:         make(chan Event, buffer+2),
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
	return b.publishGuarded(ctx, ev, source, nil)
}
func (b *eventBroker) publishGuarded(ctx context.Context, ev Event, source *EventSourceMeta, valid func() bool) bool {
	if ev == nil {
		return true
	}
	if err := b.mu.LockContext(ctx); err != nil {
		return false
	}
	defer b.mu.Unlock()
	if b.closed || b.terminal || (valid != nil && !valid()) {
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

	// Two private slots preserve both the full cancellation loss summary and
	// the terminal, even when the consumer never drains the normal buffer.
	if b.dropped.count > 0 {
		marker := b.dropMarkerLocked()
		b.sequence = marker.Meta().Sequence
		b.dropped = dropAggregate{}
		b.events <- marker
	}

	ev = b.stampNextLocked(ev, source)
	// Ordinary publication is capped at normalCapacity, while the physical
	// channel has normalCapacity+2 slots. With b.mu held, no other sender can
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
	reliable := b.blocking || !eventMayDrop(ev)
	var pendingMarker Event
	if !b.abortedLocked() && b.dropped.count > 0 {
		if reliable {
			// Reserve both coordinates before observing the fact, but do not
			// wait for any user send until that observation completes.
			pendingMarker = b.dropMarkerLocked()
			b.sequence = pendingMarker.Meta().Sequence
		} else if !b.flushDroppedLocked(ctx, false) {
			ev = b.stampNextLocked(ev, source)
			b.recordDropLocked(ev)
			return false
		}
	}

	ev = b.stampNextLocked(ev, source)
	var notices []Event
	if b.observe != nil {
		notices = b.observe(ev)
	}
	if pendingMarker != nil {
		if b.sendLocked(ctx, pendingMarker) {
			b.dropped = dropAggregate{}
		} else {
			// Its original losses remain outstanding. The assigned summary
			// event was also not delivered, so it has its own dropped count.
			b.recordDropLocked(pendingMarker)
		}
	}
	var sent bool
	if b.abortedLocked() {
		if !eventMayDrop(ev) && len(b.events) < b.normalCapacity {
			b.events <- ev
			sent = true
		}
	} else if reliable {
		sent = b.sendLocked(ctx, ev)
	} else if len(b.events) < b.normalCapacity {
		b.events <- ev
		sent = true
	}
	if !sent {
		b.recordDropLocked(ev)
	}
	for _, notice := range notices {
		b.publishLocked(ctx, notice, nil)
	}
	return sent
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
	// This preserves the two final publication slots without adding a second
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
	marker := b.dropMarkerLocked()
	markerSequence := marker.Meta().Sequence

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
	seen := map[*EventSourceMeta]*EventSourceMeta{in: &out}
	dst, src := &out, in
	for src.Upstream != nil {
		if prior, ok := seen[src.Upstream]; ok {
			dst.Upstream = prior
			break
		}
		next := *src.Upstream
		dst.Upstream = &next
		src = src.Upstream
		dst = &next
		seen[src] = dst
	}
	return &out
}

func (b *eventBroker) dropMarkerLocked() Event {
	markerSequence := b.sequence + 1
	return stampEvent(Dropped{
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

}

// eventPublicationGate permits a publisher to abandon its wait before its
// event is accepted. Abort remains independent from this receive-order gate.
type eventPublicationGate struct {
	once  sync.Once
	token chan struct{}
}

func (g *eventPublicationGate) LockContext(ctx context.Context) error {
	g.once.Do(func() { g.token = make(chan struct{}, 1); g.token <- struct{}{} })
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-g.token:
		if err := ctx.Err(); err != nil {
			g.token <- struct{}{}
			return err
		}
		return nil
	}
}
func (g *eventPublicationGate) Lock()   { _ = g.LockContext(context.Background()) }
func (g *eventPublicationGate) Unlock() { g.token <- struct{}{} }
