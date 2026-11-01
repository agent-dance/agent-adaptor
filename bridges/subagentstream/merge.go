package subagentstream

import (
	"context"
	adaptor "github.com/agent-dance/agent-adaptor/v2"
	"sync"
	"time"
)

// Merge incorporates host-produced typed worker updates into an existing
// execution. Team streams already include their workers and need no merge.
// updates must close after the host's workers finish; the parent Done remains
// last. No task lifecycle or synthetic failures are inferred by this bridge.
// Per-source order is preserved and provider coordinates survive in Source.
// Nil updates returns stream unchanged. Cancel cancels the parent and detaches
// the update source; unread critical events, including the parent's Done, remain
// available to subscribers. Unsubscribed completed streams retain their events
// without a forwarding goroutine. Result remains identical to the parent's result.
func Merge(ctx context.Context, stream adaptor.Stream, updates <-chan adaptor.SubagentUpdate) adaptor.Stream {
	if stream == nil || updates == nil {
		return stream
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithCancel(ctx)
	merged := &mergedStream{parent: stream, cancel: cancel, events: make(chan adaptor.Event), ready: make(chan struct{}), subscribe: make(chan struct{})}
	go merged.pump(ctx, updates)
	return merged
}

type mergedStream struct {
	parent     adaptor.Stream
	cancel     context.CancelFunc
	once       sync.Once
	events     chan adaptor.Event
	ready      chan struct{}
	mu         sync.Mutex
	subscribed bool
	subscribe  chan struct{}
}

func (s *mergedStream) Events() <-chan adaptor.Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.subscribed {
		s.subscribed = true
		close(s.subscribe)
	}
	return s.events
}
func (s *mergedStream) RunID() string { return s.parent.RunID() }
func (s *mergedStream) Send(ctx context.Context, text string, opts ...adaptor.MessageOption) error {
	return s.parent.Send(ctx, text, opts...)
}
func (s *mergedStream) Cancel()                          { s.once.Do(func() { s.cancel(); s.parent.Cancel() }) }
func (s *mergedStream) Result() (*adaptor.Result, error) { <-s.ready; return s.parent.Result() }

func (s *mergedStream) pump(ctx context.Context, updates <-chan adaptor.SubagentUpdate) {
	defer func() { close(s.events) }()
	defer s.cancel()
	parent := s.parent.Events()
	var queue, terminals []adaptor.Event
	var seq uint64
	var ready bool
	cancelled := ctx.Done()
	subscribe := s.subscribe
	var subscribed bool
	stamp := func(event adaptor.Event) adaptor.Event {
		meta := event.Meta()
		if meta.Source == nil && meta.RunID != "" {
			meta.Source = &adaptor.EventSourceMeta{RunID: meta.RunID, ThreadID: meta.ThreadKey, TurnID: meta.TurnID, Sequence: meta.Sequence, Timestamp: meta.Time}
		}
		seq++
		meta.RunID = s.RunID()
		meta.Sequence = seq
		meta.Time = time.Now().UTC()
		return adaptor.WithEventMeta(event, meta)
	}
	enqueue := func(event adaptor.Event) {
		// Retain critical events; aggregate only incremental worker text.
		if update, ok := event.(adaptor.SubagentUpdate); ok && update.Kind == adaptor.SubagentDelta && len(queue) >= 256 {
			if last, ok := queue[len(queue)-1].(adaptor.Dropped); ok && last.Source == "subagentstream" {
				seq++
				last.Count++
				last.ByKind["subagent.delta"]++
				last.LastSequence = seq
				queue[len(queue)-1] = last
				return
			}
			event = adaptor.Dropped{Count: 1, ByKind: map[string]int{"subagent.delta": 1}, FirstSequence: seq + 1, LastSequence: seq + 1, Reason: "backpressure", Source: "subagentstream"}
		}
		queue = append(queue, stamp(event))
	}
	for {
		if parent == nil && updates == nil && !ready {
			for _, e := range terminals {
				enqueue(e)
			}
			s.mu.Lock()
			subscribed = s.subscribed
			if !subscribed {
				// No public channel has escaped. Preserve the complete tail in
				// a closed buffer, as core does for an unsubscribed stream.
				s.events = make(chan adaptor.Event, len(queue))
				for _, event := range queue {
					s.events <- event
				}
			}
			s.mu.Unlock()
			close(s.ready)
			if !subscribed {
				return
			}
			ready = true
		}
		if ready && len(queue) == 0 {
			return
		}
		var out chan adaptor.Event
		var next adaptor.Event
		if subscribed && len(queue) > 0 {
			out = s.events
			next = queue[0]
		}
		select {
		case <-subscribe:
			subscribe = nil
			subscribed = true
		case <-cancelled:
			s.Cancel()
			cancelled = nil
			updates = nil
		case e, ok := <-parent:
			if !ok {
				parent = nil
				continue
			}
			switch e.(type) {
			case adaptor.Done, adaptor.RunFinished:
				terminals = append(terminals, e)
			default:
				enqueue(e)
			}
		case e, ok := <-updates:
			if !ok {
				updates = nil
				continue
			}
			enqueue(e)
		case out <- next:
			queue[0] = nil
			queue = queue[1:]
		}
	}
}
