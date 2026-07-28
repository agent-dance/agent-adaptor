package sse_test

import (
	"bufio"
	"strings"
	"testing"
	"time"
)

type sseFrame struct {
	event string
	id    string
	data  string
}

func readSSEFrames(t *testing.T, reader interface {
	Read([]byte) (int, error)
}, timeout time.Duration) []sseFrame {
	t.Helper()
	done := make(chan []sseFrame, 1)
	go func() {
		scanner := bufio.NewScanner(readerFunc(reader.Read))
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		var frames []sseFrame
		var current sseFrame
		for scanner.Scan() {
			line := scanner.Text()
			if line == "" {
				if current.event != "" || current.data != "" || current.id != "" {
					frames = append(frames, current)
				}
				current = sseFrame{}
				continue
			}
			switch {
			case strings.HasPrefix(line, ":"):
			case strings.HasPrefix(line, "event: "):
				current.event = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "id: "):
				current.id = strings.TrimPrefix(line, "id: ")
			case strings.HasPrefix(line, "data: "):
				current.data = strings.TrimPrefix(line, "data: ")
			}
		}
		done <- frames
	}()
	select {
	case frames := <-done:
		return frames
	case <-time.After(timeout):
		t.Fatal("timeout reading SSE stream")
		return nil
	}
}

type readerFunc func([]byte) (int, error)

func (fn readerFunc) Read(buffer []byte) (int, error) { return fn(buffer) }
