package team

import (
	"sync"
	"time"

	adaptor "github.com/agent-dance/agent-adaptor/v2"
)

type eventLog struct {
	mu         sync.Mutex
	queue      []adaptor.Event
	notify     chan struct{}
	out        chan adaptor.Event
	finished   bool
	subscribed bool
	standby    bool
	runID      string
	key        string
	sequence   uint64
}

func newEventLog(runID, key string, standby bool) *eventLog {
	return &eventLog{notify: make(chan struct{}, 1), out: make(chan adaptor.Event), runID: runID, key: key, standby: standby}
}

func (log *eventLog) push(event adaptor.Event) {
	log.mu.Lock()
	defer log.mu.Unlock()
	if log.finished || log.standby && !log.subscribed {
		return
	}
	log.sequence++
	if kind := incrementalKind(event); len(log.queue) >= 256 && kind != "" {
		if dropped, ok := log.queue[len(log.queue)-1].(adaptor.Dropped); ok && dropped.Source == "team" {
			dropped.Count++
			dropped.ByKind[kind]++
			dropped.LastSequence = log.sequence
			log.queue[len(log.queue)-1] = dropped
			return
		}
		event = adaptor.Dropped{Count: 1, ByKind: map[string]int{kind: 1}, FirstSequence: log.sequence, LastSequence: log.sequence, Reason: "backpressure", Source: "team"}
	}
	if !log.standby {
		original := event.Meta()
		source := original.Source
		if source == nil && original.RunID != "" {
			source = &adaptor.EventSourceMeta{RunID: original.RunID, ThreadID: original.ThreadKey, TurnID: original.TurnID, Sequence: original.Sequence, Timestamp: original.Time}
		}
		event = adaptor.WithEventMeta(event, adaptor.EventMeta{RunID: log.runID, ThreadKey: log.key, Sequence: log.sequence, Time: time.Now().UTC(), Source: source})
	}
	log.queue = append(log.queue, event)
	select {
	case log.notify <- struct{}{}:
	default:
	}
}

func (log *eventLog) events() <-chan adaptor.Event {
	log.mu.Lock()
	if !log.subscribed {
		log.subscribed = true
		if !log.finished {
			go log.pump()
		}
	}
	log.mu.Unlock()
	return log.out
}

func (log *eventLog) pump() {
	defer close(log.out)
	for {
		log.mu.Lock()
		if len(log.queue) > 0 {
			event := log.queue[0]
			log.queue[0] = nil
			log.queue = log.queue[1:]
			log.mu.Unlock()
			log.out <- event
			continue
		}
		finished := log.finished
		log.mu.Unlock()
		if finished {
			return
		}
		<-log.notify
	}
}

func incrementalKind(event adaptor.Event) string {
	switch typed := event.(type) {
	case adaptor.TextDelta:
		return "text"
	case adaptor.Thinking:
		return "thinking"
	case adaptor.SubagentUpdate:
		if typed.Kind == adaptor.SubagentDelta {
			return "subagent.delta"
		}
	case adaptor.ProcessInfo:
		if typed.Kind == adaptor.ProcessStdout || typed.Kind == adaptor.ProcessStderr {
			return "process.chunk"
		}
	}
	return ""
}

func (log *eventLog) finish() {
	log.mu.Lock()
	if log.finished {
		log.mu.Unlock()
		return
	}
	log.finished = true
	if !log.subscribed {
		// Match core's Result-only path: retain the unread tail in a closed
		// channel without leaving a forwarding goroutine behind.
		log.out = make(chan adaptor.Event, len(log.queue))
		for _, event := range log.queue {
			log.out <- event
		}
		log.queue = nil
		close(log.out)
	}
	log.mu.Unlock()
	select {
	case log.notify <- struct{}{}:
	default:
	}
}
