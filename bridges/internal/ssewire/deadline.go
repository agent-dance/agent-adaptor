package ssewire

import (
	"errors"
	"net/http"
	"time"
)

// DeadlineWriter applies a fresh deadline to every low-level write.
// This covers multi-write AG-UI frames, Raw frames, flushes, and keep-alives
// without starting an unbounded goroutine per frame. Response writers that do
// not support write deadlines (notably httptest.ResponseRecorder) keep their
// normal behavior.
type DeadlineWriter struct {
	http.ResponseWriter
	controller *http.ResponseController
	timeout    time.Duration
}

// NewDeadlineWriter wraps w with a fresh timeout for each write.
func NewDeadlineWriter(w http.ResponseWriter, timeout time.Duration) *DeadlineWriter {
	return &DeadlineWriter{
		ResponseWriter: w,
		controller:     http.NewResponseController(w),
		timeout:        timeout,
	}
}

// Write arms the deadline before delivering bytes.
func (w *DeadlineWriter) Write(p []byte) (int, error) {
	if err := w.armDeadline(); err != nil {
		return 0, err
	}
	return w.ResponseWriter.Write(p)
}

// Flush arms the deadline before flushing buffered bytes.
func (w *DeadlineWriter) Flush() {
	if w.armDeadline() != nil {
		return
	}
	_ = w.controller.Flush()
}

func (w *DeadlineWriter) armDeadline() error {
	err := w.controller.SetWriteDeadline(time.Now().Add(w.timeout))
	if errors.Is(err, http.ErrNotSupported) {
		return nil
	}
	return err
}

// ClearDeadline removes the per-write deadline when the handler exits.
func (w *DeadlineWriter) ClearDeadline() {
	err := w.controller.SetWriteDeadline(time.Time{})
	if err != nil && !errors.Is(err, http.ErrNotSupported) {
		return
	}
}
