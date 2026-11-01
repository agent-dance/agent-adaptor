package adaptor

import (
	"context"
	"sync"
	"time"
)

// eventBroker is the sole owner of event ordering and channel lifecycle for
// one run. The mutex serializes admission into one ordered queue. Critical
// events never wait for capacity; ordinary blocking-mode producers release the
// mutex while waiting, so publication from inside the message-hub lock cannot
// deadlock a consumer that sends a message. Abort wakes blocked producers.
type eventBroker struct {
	events              chan Event
	spool               bool
	subscribed          bool
	queue               []Event
	wake                chan struct{}
	pumping             bool
	discardOrdinary     bool
	discardUnsubscribed bool
	multipleRuns        bool
	capacityChanged     chan struct{}

	runID     string
	threadKey string
	blocking  bool
	// normalCapacity bounds combined ordinary channel/queue occupancy. Critical
	// execution events may exceed it. The extra physical channel slot also
	// supports reserved terminal delivery for standalone sinks.
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
	count      int
	byKind     map[string]int
	first      uint64
	last       uint64
	unobserved bool
	runID      string
}

func newEventBroker(runID, threadKey string, buffer int, blocking bool) *eventBroker {
	if buffer <= 0 {
		buffer = defaultEventBuffer
	}
	if buffer == int(^uint(0)>>1) {
		panic("adaptor: event buffer is too large to reserve terminal capacity")
	}
	return &eventBroker{
		events:          make(chan Event, buffer+1),
		runID:           runID,
		threadKey:       threadKey,
		blocking:        blocking,
		normalCapacity:  buffer,
		abortCh:         make(chan struct{}),
		done:            make(chan struct{}),
		wake:            make(chan struct{}, 1),
		capacityChanged: make(chan struct{}),
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
// pump from inserting an event between Done and close.
func (b *eventBroker) publishTerminal(ev Event, source *EventSourceMeta) bool {
	if ev == nil {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed || b.terminal {
		return false
	}
	if b.spool {
		if !b.subscribed {
			b.pruneUnobservedLocked()
		}
		if b.dropped.count > 0 {
			b.spoolDroppedLocked()
		}
		b.terminal = true
		b.enqueueLocked(b.stampNextLocked(ev, source))
		return true
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
	if b.discardUnsubscribed && !b.subscribed {
		return true
	}
	if b.spool {
		critical := eventCritical(ev)
		if !critical {
			if b.blocking && !b.waitCapacityLocked(ctx) {
				return false
			}
			if b.closed || b.terminal {
				return false
			}
			if b.discardOrdinary || ((ctx.Err() != nil || b.abortedLocked()) && eventMayDrop(ev)) || (!b.blocking && eventMayDrop(ev) && len(b.events)+len(b.queue) >= b.normalCapacity) {
				b.recordDropLocked(b.stampNextLocked(ev, source))
				b.dropped.unobserved = b.dropped.unobserved || b.discardOrdinary
				return false
			}
		}
		if b.dropped.count > 0 {
			b.spoolDroppedLocked()
		}
		// A drop summary is itself critical and can fill the available capacity.
		if !critical && b.blocking && !b.waitCapacityLocked(ctx) {
			return false
		}
		if b.closed || b.terminal {
			return false
		}
		if !critical && (b.discardOrdinary || ((ctx.Err() != nil || b.abortedLocked()) && eventMayDrop(ev)) || (!b.blocking && eventMayDrop(ev) && len(b.events)+len(b.queue) >= b.normalCapacity)) {
			b.recordDropLocked(b.stampNextLocked(ev, source))
			b.dropped.unobserved = b.dropped.unobserved || b.discardOrdinary
			return false
		}
		b.enqueueLocked(b.stampNextLocked(ev, source))
		return true
	}
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
	if b.multipleRuns {
		original := ev.Meta()
		meta.RunID = original.RunID
		meta.TurnID = original.TurnID
		meta.Source = cloneEventSourceMeta(original.Source)
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
		b.dropped.runID = ev.Meta().RunID
	}
	if seq < b.dropped.first {
		b.dropped.first = seq
	}
	if seq > b.dropped.last {
		b.dropped.last = seq
	}
	b.dropped.count++
	b.dropped.byKind[eventKind(ev)]++
}

func (b *eventBroker) mergeDropsLocked(count int, byKind map[string]int, first, last uint64, unobserved bool) {
	if count == 0 {
		return
	}
	if b.dropped.count == 0 {
		b.dropped.byKind = make(map[string]int)
		b.dropped.first = first
	}
	if first < b.dropped.first {
		b.dropped.first = first
	}
	if last > b.dropped.last {
		b.dropped.last = last
	}
	b.dropped.count += count
	for kind, n := range byKind {
		b.dropped.byKind[kind] += n
	}
	b.dropped.unobserved = b.dropped.unobserved || unobserved
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

// close seals publication and releases capacity waiters. Execution subscribers
// receive the queued tail before Events closes. Without a subscriber, ordinary
// events are compacted into a loss summary and the critical log closes eagerly.
// Standalone sinks retain their reserved-terminal drain behavior.
func (b *eventBroker) close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	if b.spool {
		if !b.terminal && !b.subscribed {
			b.pruneUnobservedLocked()
		}
		if !b.terminal && b.dropped.count > 0 {
			b.spoolDroppedLocked()
		}
		b.closed = true
		b.notifyCapacityLocked()
		close(b.done)
		if !b.subscribed {
			ch := make(chan Event, len(b.events)+len(b.queue))
			for len(b.events) > 0 {
				ch <- <-b.events
			}
			for _, ev := range b.queue {
				ch <- ev
			}
			b.queue = nil
			b.events = ch
			close(ch)
		} else {
			b.startPumpLocked()
			b.wakeLocked()
		}
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

// abort immediately releases capacity waiters. Execution teardown still owns
// closure, and critical events can enqueue throughout cancellation cleanup.
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

// subscribe freezes the public channel identity before returning it. Executions
// with no subscriber retain a critical-only closed log after finalization.
// Result-only callers release ordinary producers without an event reader.
func (b *eventBroker) subscribe() <-chan Event {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.subscribed = true
	if !b.closed {
		b.discardOrdinary = false
	}
	if b.spool && !b.closed {
		b.startPumpLocked()
	}
	return b.events
}
func (b *eventBroker) wakeLocked() {
	select {
	case b.wake <- struct{}{}:
	default:
	}
}
func (b *eventBroker) enqueueLocked(ev Event) {
	if len(b.queue) == 0 && len(b.events) < b.normalCapacity {
		b.events <- ev
		return
	}
	b.queue = append(b.queue, ev)
	b.wakeLocked()
	if b.subscribed {
		b.startPumpLocked()
	}
}
func (b *eventBroker) spoolDroppedLocked() {
	d := b.dropped
	b.dropped = dropAggregate{}
	reason, strategy := "slow_consumer", "drop_deltas"
	if d.unobserved {
		reason, strategy = "events_unobserved", "retain_critical"
	}
	marker := stampEvent(Dropped{Count: d.count, ByKind: d.byKind, FirstSequence: d.first, LastSequence: d.last, Reason: reason, Source: "sdk.event_broker", Details: map[string]any{"buffer": b.normalCapacity, "strategy": strategy}}, EventMeta{RunID: d.runID})
	b.enqueueLocked(b.stampNextLocked(marker, nil))
}

// waitCapacityLocked returns with mu held. A receive-only channel has no
// notification hook for receives, so a timer probes capacity only while full.
// Queue changes, Result-only consumption, cancellation and close wake it early.
// Cancellation releases backpressure, but publication still retains reliable
// events and accounts for droppable deltas before the execution's Done.
func (b *eventBroker) waitCapacityLocked(ctx context.Context) bool {
	for !b.closed && !b.terminal && !b.discardOrdinary && !b.abortedLocked() && ctx.Err() == nil && len(b.events)+len(b.queue) >= b.normalCapacity {
		changed := b.capacityChanged
		timer := time.NewTimer(time.Millisecond)
		b.mu.Unlock()
		select {
		case <-changed:
		case <-timer.C:
		case <-b.abortCh:
		case <-ctx.Done():
		}
		timer.Stop()
		b.mu.Lock()
	}
	return !b.closed && !b.terminal
}
func (b *eventBroker) notifyCapacityLocked() {
	close(b.capacityChanged)
	b.capacityChanged = make(chan struct{})
}

// awaitResult lets Result-only consumers complete even with blocking delivery.
// If Events wins the subscription race, ordinary backpressure remains active.
func (b *eventBroker) awaitResult() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.spool || b.subscribed || b.closed || b.terminal {
		return
	}
	b.discardOrdinary = true
	b.pruneUnobservedLocked()
	b.notifyCapacityLocked()
}

// pruneUnobservedLocked runs only before the public channel is exposed.
// Preserve critical-event order and merge SDK loss reports into one aggregate
// stamped before Done; provider loss reports keep their original identity.
func (b *eventBroker) pruneUnobservedLocked() {
	pending := make([]Event, 0, len(b.events)+len(b.queue))
	for len(b.events) > 0 {
		pending = append(pending, <-b.events)
	}
	pending = append(pending, b.queue...)
	b.queue = nil
	for _, ev := range pending {
		if d, ok := ev.(Dropped); ok && d.Source == "sdk.event_broker" {
			b.mergeDropsLocked(d.Count, d.ByKind, d.FirstSequence, d.LastSequence, true)
		} else if eventCritical(ev) {
			b.queue = append(b.queue, ev)
		} else {
			b.recordDropLocked(ev)
			b.dropped.unobserved = true
		}
	}
}
func (b *eventBroker) startPumpLocked() {
	if b.pumping {
		return
	}
	b.pumping = true
	go func() {
		for {
			b.mu.Lock()
			if len(b.queue) == 0 {
				if b.closed {
					close(b.events)
					b.mu.Unlock()
					return
				}
				b.mu.Unlock()
				<-b.wake
				continue
			}
			// Retain the head until handoff completes, so concurrent critical
			// publishers append behind it. The potentially blocking send holds no
			// broker lock and consumes no CPU while a subscriber is stalled.
			ev := b.queue[0]
			b.mu.Unlock()
			b.events <- ev
			b.mu.Lock()
			b.queue[0] = nil
			b.queue = b.queue[1:]
			if len(b.queue) == 0 {
				b.queue = nil
			}
			b.notifyCapacityLocked()
			b.mu.Unlock()
		}
	}()
}
