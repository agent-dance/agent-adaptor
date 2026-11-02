package main

import "sync"

// historyBarrier tracks admission and its subsequent history writes as one
// operation. The event owner waits only after Events closes, when later sends
// can no longer succeed. This keeps queue backpressure independent of recording
// the terminal and permits rejected sends without creating user history.
type historyBarrier struct {
	mu      sync.Mutex
	pending int
	ready   chan struct{}
}

func (b *historyBarrier) begin() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.pending == 0 {
		b.ready = make(chan struct{})
	}
	b.pending++
}

func (b *historyBarrier) end() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.pending--
	if b.pending == 0 {
		close(b.ready)
	}
}

func (b *historyBarrier) wait() {
	b.mu.Lock()
	var ready <-chan struct{}
	if b.pending > 0 {
		ready = b.ready
	}
	b.mu.Unlock()
	if ready != nil {
		<-ready
	}
}
