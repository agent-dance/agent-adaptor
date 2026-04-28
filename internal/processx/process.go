package processx

import (
	"os/exec"
	"runtime"
	"strconv"
	"time"
)

const defaultWaitDelay = 5 * time.Second

// ConfigureCancellation makes exec.CommandContext cancellation less brittle
// for local agent CLIs. On Windows the runnable command is often a PowerShell
// or cmd shim; terminating only that wrapper can leave the real provider
// process alive with stdout/stderr pipes open.
func ConfigureCancellation(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	cmd.Cancel = func() error {
		return terminate(cmd)
	}
	cmd.WaitDelay = defaultWaitDelay
}

func terminate(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	if runtime.GOOS == "windows" {
		if err := exec.Command("taskkill.exe", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid)).Run(); err == nil {
			return nil
		}
	}
	return cmd.Process.Kill()
}
