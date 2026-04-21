package testutil

import (
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"

	agentadaptor "github.com/agent-dance/agent-adaptor"
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
	events  []agentadaptor.RunEvent
	streams []agentadaptor.StreamPayload
}

func (r *EventRecorder) Emit(event agentadaptor.RunEvent) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, event)
	return nil
}

func (r *EventRecorder) EmitStream(payload agentadaptor.StreamPayload) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.streams = append(r.streams, payload)
	return nil
}

func (r *EventRecorder) Snapshot() []agentadaptor.RunEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]agentadaptor.RunEvent, len(r.events))
	copy(out, r.events)
	return out
}

// StreamSnapshot returns a copy of every StreamPayload recorded so far.
// Useful for asserting adapter stream output in tests.
func (r *EventRecorder) StreamSnapshot() []agentadaptor.StreamPayload {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]agentadaptor.StreamPayload, len(r.streams))
	copy(out, r.streams)
	return out
}
