//go:build !windows

package processx

import (
	"context"
	"io"
	"os/exec"
	"testing"
	"time"
)

// TestConfigureCancellationReapsChildHoldingStdout reproduces the cancellation
// hang: a CLI spawns a child that inherits stdout and outlives the leader. The
// driver pattern drains stdout to EOF before Wait, so unless the whole process
// group is killed on cancel, the child keeps the pipe open and the drain blocks
// for the child's full lifetime.
//
// With the process-group fix, cancelling the context must close stdout promptly
// (the child is killed too). Against the old single-process kill this test fails
// by timing out, because the `sleep 30` child keeps the pipe open.
func TestConfigureCancellationReapsChildHoldingStdout(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Leader spawns a background child that inherits stdout, then the leader
	// itself stays alive. Both must die when the group is killed.
	cmd := exec.CommandContext(ctx, "sh", "-c", "sleep 30 & sleep 30")
	ConfigureCancellation(cmd)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}

	// Let the process group come up before cancelling.
	time.Sleep(200 * time.Millisecond)
	cancel()

	eof := make(chan struct{})
	go func() {
		_, _ = io.Copy(io.Discard, stdout)
		close(eof)
	}()

	select {
	case <-eof:
		// stdout reached EOF quickly -> the child holding the pipe was reaped.
	case <-time.After(5 * time.Second):
		t.Fatal("stdout did not reach EOF within 5s after cancel: child process survived (process group not killed)")
	}

	_ = cmd.Wait()
}

// TestTerminateNilSafe guards the nil/unstarted paths.
func TestTerminateNilSafe(t *testing.T) {
	if err := terminate(nil); err != nil {
		t.Fatalf("terminate(nil): %v", err)
	}
	if err := terminate(exec.Command("true")); err != nil {
		t.Fatalf("terminate(unstarted): %v", err)
	}
}
