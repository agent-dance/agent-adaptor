package testutil

import (
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"

	"github.com/agent-dance/agent-adaptor/driver"
)

func WriteCommand(t testing.TB, dir, name, posixBody, windowsBody string) string {
	t.Helper()

	path := filepath.Join(dir, name)
	content := posixBody
	mode := os.FileMode(0o755)
	if runtime.GOOS == "windows" {
		path += ".cmd"
		content = windowsBody
		mode = 0o644
	} else {
		path += ".sh"
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatalf("write command %s: %v", path, err)
	}
	return path
}

type EventRecorder struct {
	mu      sync.Mutex
	events  []driver.RunEvent
	streams []driver.StreamPayload
}

// ChunkCancelRecorder records events and invokes cancel after every requested
// raw stream has delivered at least one non-empty chunk. It lets cancellation
// tests synchronize on observed process output instead of machine-dependent
// startup delays.
type ChunkCancelRecorder struct {
	EventRecorder

	chunkMu    sync.Mutex
	required   map[string]struct{}
	seen       map[string]struct{}
	cancel     func()
	cancelOnce sync.Once
}

func NewChunkCancelRecorder(cancel func(), streams ...string) *ChunkCancelRecorder {
	required := make(map[string]struct{}, len(streams))
	for _, stream := range streams {
		required[stream] = struct{}{}
	}
	return &ChunkCancelRecorder{
		required: required,
		seen:     make(map[string]struct{}, len(required)),
		cancel:   cancel,
	}
}

func (r *ChunkCancelRecorder) Emit(event driver.RunEvent) error {
	if err := r.EventRecorder.Emit(event); err != nil {
		return err
	}
	if event.Type != driver.RunEventChunk || len(event.Bytes) == 0 {
		return nil
	}
	r.chunkMu.Lock()
	if _, wanted := r.required[event.Stream]; wanted {
		r.seen[event.Stream] = struct{}{}
	}
	ready := len(r.seen) == len(r.required)
	r.chunkMu.Unlock()
	if ready && r.cancel != nil {
		// Do not cancel CommandContext from inside clihelper's pipe reader: the
		// process wait path may need that reader to return before reaping the
		// process tree. Dispatching cancellation keeps the observation callback
		// non-blocking and mirrors cancellation from a host goroutine.
		r.cancelOnce.Do(func() { go r.cancel() })
	}
	return nil
}

func (r *EventRecorder) Emit(event driver.RunEvent) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, event)
	return nil
}

func (r *EventRecorder) EmitStream(payload driver.StreamPayload) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.streams = append(r.streams, payload)
	return nil
}

func (r *EventRecorder) Snapshot() []driver.RunEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]driver.RunEvent, len(r.events))
	copy(out, r.events)
	return out
}

// StreamSnapshot returns a copy of every StreamPayload recorded so far.
// Useful for asserting adapter stream output in tests.
func (r *EventRecorder) StreamSnapshot() []driver.StreamPayload {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]driver.StreamPayload, len(r.streams))
	copy(out, r.streams)
	return out
}
