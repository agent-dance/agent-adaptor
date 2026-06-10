package processx

import (
	"os/exec"
	"time"
)

const defaultWaitDelay = 5 * time.Second

// ConfigureCancellation makes exec.CommandContext cancellation reliable for
// local agent CLIs that spawn their own child processes (for example Claude
// Code plan-mode sub-agents, or a PowerShell/cmd shim on Windows).
//
// The problem it solves: drivers stream the CLI's stdout/stderr by draining the
// pipes to EOF before calling cmd.Wait (see internal/clihelper). A child process
// inherits those pipe file descriptors, so killing only the top-level process
// leaves the children holding the write end open. The drain never observes EOF,
// cmd.Wait is never reached, the driver Run never returns, and the surrounding
// SDK run hangs — which in turn keeps the session lease held (renewed) instead
// of releasing it. The next run on the same session key then fails with
// ErrSessionBusy even though the user already cancelled.
//
// The fix has two cooperating parts:
//   - configureProcessGroup (before Start) isolates the command so the whole
//     process tree can be addressed together.
//   - terminate (on cancel) signals the entire group/tree, not just the leader,
//     so inherited pipes are closed and the drain unblocks.
//
// WaitDelay is kept as a backstop for the rare case a descendant escapes the
// group (e.g. it called setsid itself).
func ConfigureCancellation(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	configureProcessGroup(cmd)
	cmd.Cancel = func() error {
		return terminate(cmd)
	}
	cmd.WaitDelay = defaultWaitDelay
}
