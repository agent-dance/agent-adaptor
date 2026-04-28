package clihelper

import (
	"context"
	"errors"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	agentadaptor "github.com/agent-dance/agent-adaptor"
)

// captureSink collects Emit/EmitStream calls for assertion.
type captureSink struct {
	mu     sync.Mutex
	events []agentadaptor.RunEvent
}

func (c *captureSink) Emit(e agentadaptor.RunEvent) error {
	c.mu.Lock()
	c.events = append(c.events, e)
	c.mu.Unlock()
	return nil
}
func (c *captureSink) EmitStream(agentadaptor.StreamPayload) error { return nil }

// TestStdinController_WriteBlocksUntilReady verifies Write does not deadlock
// when called before the subprocess has spawned — the controller exposes a
// ready gate for exactly that reason. Uses the internal constructor so the
// test can reach unexported helper methods.
func TestStdinController_WriteBlocksUntilReady(t *testing.T) {
	ctrl := newStdinController()

	// Simulate the helper's signalReady arriving 50ms later.
	go func() {
		time.Sleep(50 * time.Millisecond)
		ctrl.signalReady()
		// Drain the channel so Write can send into it.
		go func() {
			for range ctrl.ch {
			}
		}()
	}()

	done := make(chan error, 1)
	go func() { done <- ctrl.Write([]byte("frame\n")) }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Write: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Write did not return within 1s")
	}

	// Cleanup.
	ctrl.markDone()
}

// TestStdinController_WriteAfterCloseReturnsErr
func TestStdinController_WriteAfterCloseReturnsErr(t *testing.T) {
	ctrl := NewStdinController()
	if err := ctrl.Close(); err != nil {
		t.Fatal(err)
	}
	err := ctrl.Write([]byte("frame\n"))
	if !errors.Is(err, ErrStdinClosed) {
		t.Fatalf("got %v, want ErrStdinClosed", err)
	}
}

// TestStdinController_MarkDoneUnblocksWrite ensures a blocked Write returns
// ErrStdinClosed once markDone fires (simulating subprocess exit).
func TestStdinController_MarkDoneUnblocksWrite(t *testing.T) {
	ctrl := newStdinController()
	ctrl.signalReady()

	// Fill the buffer (cap 16) so the next Write blocks on the send.
	for i := 0; i < cap(ctrl.ch); i++ {
		if err := ctrl.Write([]byte("x\n")); err != nil {
			t.Fatalf("initial fill %d: %v", i, err)
		}
	}

	done := make(chan error, 1)
	go func() { done <- ctrl.Write([]byte("overflow\n")) }()

	time.Sleep(30 * time.Millisecond)
	ctrl.markDone()

	select {
	case err := <-done:
		if !errors.Is(err, ErrStdinClosed) {
			t.Fatalf("got %v want ErrStdinClosed", err)
		}
	case <-time.After(time.Second):
		t.Fatal("blocked Write did not unblock after markDone")
	}
}

// TestRun_LongLivedStdinForwardsFrames runs a real subprocess (cat, a POSIX
// builtin) and verifies:
//
//  1. Prompt is written first
//  2. Frames enqueued via StdinController.Write reach stdout (via `cat`)
//  3. Closing the controller is enough to let the subprocess exit cleanly
func TestRun_LongLivedStdinForwardsFrames(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX cat not available on Windows")
	}

	ctrl := NewStdinController()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	done := make(chan struct{})
	var (
		result CommandResult
		runErr error
	)
	go func() {
		defer close(done)
		result, runErr = Run(ctx, CommandRequest{
			Command: "cat",
			Prompt:  "prompt-first\n",
			Stdin:   ctrl,
		}, &captureSink{})
	}()

	// Wait briefly for the helper to signalReady before we start writing.
	time.Sleep(100 * time.Millisecond)

	if err := ctrl.Write([]byte("frame-1\n")); err != nil {
		t.Fatalf("write frame-1: %v", err)
	}
	if err := ctrl.Write([]byte("frame-2\n")); err != nil {
		t.Fatalf("write frame-2: %v", err)
	}

	// Close tells the helper "no more input" so `cat` sees EOF and exits.
	if err := ctrl.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after Stdin.Close")
	}

	if runErr != nil {
		t.Fatalf("Run: %v", runErr)
	}
	stdout := result.RawStreams.Stdout
	for _, want := range []string{"prompt-first", "frame-1", "frame-2"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout missing %q; got %q", want, stdout)
		}
	}
}

// TestRun_LegacyPromptPathUnchanged verifies the backward-compat one-shot
// path still works (Stdin==nil writes Prompt once and closes).
func TestRun_LegacyPromptPathUnchanged(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX cat not available on Windows")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result, err := Run(ctx, CommandRequest{
		Command: "cat",
		Prompt:  "legacy-hello\n",
	}, &captureSink{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(result.RawStreams.Stdout, "legacy-hello") {
		t.Errorf("stdout: %q", result.RawStreams.Stdout)
	}
}

func TestFinalizeCommandResultContextCancelMasksCancelFailure(t *testing.T) {
	result, err := finalizeCommandResult(
		CommandResult{},
		errors.New("exec: canceling Cmd: TerminateProcess: Access is denied."),
		nil,
		context.DeadlineExceeded,
	)
	if err != nil {
		t.Fatalf("finalizeCommandResult: %v", err)
	}
	if !result.TimedOut {
		t.Fatal("expected TimedOut for context deadline")
	}
	if result.ExitCode != interruptedExitCode {
		t.Fatalf("exit code = %d, want %d", result.ExitCode, interruptedExitCode)
	}
	if result.Signal != context.DeadlineExceeded.Error() {
		t.Fatalf("signal = %q, want %q", result.Signal, context.DeadlineExceeded.Error())
	}
}

func TestFinalizeCommandResultPreservesNonContextWaitError(t *testing.T) {
	waitErr := errors.New("wait failed")
	_, err := finalizeCommandResult(CommandResult{}, waitErr, nil, nil)
	if !errors.Is(err, waitErr) {
		t.Fatalf("err = %v, want %v", err, waitErr)
	}
}

func TestFinalizeCommandResultPreservesFirstError(t *testing.T) {
	firstErr := errors.New("parser failed")
	_, err := finalizeCommandResult(
		CommandResult{},
		errors.New("exec: canceling Cmd: TerminateProcess: Access is denied."),
		firstErr,
		context.Canceled,
	)
	if !errors.Is(err, firstErr) {
		t.Fatalf("err = %v, want %v", err, firstErr)
	}
}
