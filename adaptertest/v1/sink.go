package adaptertest

import (
	"sync"

	"github.com/agent-dance/agent-adaptor/driver"
)

// RecordingSink is a concurrency-safe driver.EventSink that records every
// RunEvent and StreamPayload exactly as the driver emitted them. Unlike the
// SDK's production sink it performs no Sequence/Seq/Timestamp backfill, so
// the verifiers can check the driver-side halves of the contract (EVT-10,
// RUN-03: drivers leave those fields zero).
type RecordingSink struct {
	mu     sync.Mutex
	events []driver.RunEvent
	stream []driver.StreamPayload
}

var _ driver.EventSink = (*RecordingSink)(nil)

// NewRecordingSink returns an empty RecordingSink.
func NewRecordingSink() *RecordingSink { return &RecordingSink{} }

// Emit records event and never fails.
func (s *RecordingSink) Emit(event driver.RunEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, event)
	return nil
}

// EmitStream records payload and never fails.
func (s *RecordingSink) EmitStream(payload driver.StreamPayload) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stream = append(s.stream, payload)
	return nil
}

// Events returns a copy of the recorded RunEvents in emission order.
func (s *RecordingSink) Events() []driver.RunEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]driver.RunEvent, len(s.events))
	copy(out, s.events)
	return out
}

// Stream returns a copy of the recorded StreamPayloads in emission order.
func (s *RecordingSink) Stream() []driver.StreamPayload {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]driver.StreamPayload, len(s.stream))
	copy(out, s.stream)
	return out
}
