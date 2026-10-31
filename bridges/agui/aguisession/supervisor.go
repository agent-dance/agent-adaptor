package aguisession

import (
	"context"
	"net/http"

	"github.com/agent-dance/agent-adaptor/bridges/agui"
)

type ownedRun struct {
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
}

func (s *Session) admit() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.lifetime.Err() != nil {
		return &agui.InputError{Status: 503, Code: "agui.session_closed", Message: "session is shut down"}
	}
	if s.opts.Unattended && s.running >= s.opts.MaxConcurrentRuns {
		return &agui.InputError{
			Status: http.StatusTooManyRequests, Code: "agui.capacity",
			Message: "unattended run capacity reached",
		}
	}
	s.running++
	return nil
}

func (s *Session) finishAdmission() {
	s.mu.Lock()
	s.running--
	if s.closed && s.running == 0 {
		close(s.shutdownDone)
	}
	s.mu.Unlock()
}

// Shutdown rejects new runs, cancels unattended runs and waits for their drains.
// A deadline bounds waiting; subsequent calls can continue waiting for cleanup.
func (s *Session) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	if !s.closed {
		s.closed = true
		s.stop()
		if s.running == 0 {
			close(s.shutdownDone)
		}
	}
	s.mu.Unlock()
	// Completed cleanup is stable even when a repeated caller has an expired context.
	select {
	case <-s.shutdownDone:
		return nil
	default:
	}
	select {
	case <-s.shutdownDone:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Cancel cancels unattended admission or execution at an authorized opaque key
// and waits for cleanup (and terminal recording if its boundary was committed).
// HTTP callers should use CancelHandler.
func (s *Session) Cancel(ctx context.Context, key string) error {
	s.mu.Lock()
	run := s.runs[key]
	s.mu.Unlock()
	if run == nil {
		return nil
	}
	run.cancel()
	select {
	case <-run.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Revoke cleanly detaches current observers at key without cancelling execution.
// Future observers are authorized independently by the host resolver.
func (s *Session) Revoke(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, cancel := range s.observers[key] {
		cancel()
	}
}

// CancelHandler authorizes OpCancel for ?thread_id=T and cancels the active run.
func (s *Session) CancelHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			methodError(w, http.MethodPost)
			return
		}
		id := r.URL.Query().Get("thread_id")
		if id == "" {
			writeError(w, &agui.InputError{Code: "agui.identity_required", Message: "thread_id required"})
			return
		}
		key, err := s.resolve(r, Access{Op: OpCancel, ThreadID: id})
		if err == nil {
			err = s.Cancel(r.Context(), key)
		}
		if err != nil {
			writeError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}
