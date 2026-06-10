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
// WaitDelay only bounds the window inside cmd.Wait; it is NOT a backstop for a
// descendant that escapes the group (e.g. by calling setsid) while still
// holding an inherited stdout/stderr pipe. In that case clihelper's drain
// (wg.Wait) blocks before cmd.Wait is ever reached, so WaitDelay never fires
// and the run still hangs. That escape is a known residual risk; the group kill
// above is what covers the common case.
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
