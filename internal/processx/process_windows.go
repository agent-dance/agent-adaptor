//go:build windows

package processx

import (
	"os/exec"
	"strconv"
)

// configureProcessGroup is a no-op on Windows. Tree termination is handled by
// taskkill /T in terminate, which walks and kills the whole process tree; this
// already covers the PowerShell/cmd shim case the original implementation
// targeted.
func configureProcessGroup(cmd *exec.Cmd) {}

// terminate kills the whole process tree via taskkill, falling back to killing
// just the leader if taskkill is unavailable or fails.
func terminate(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	if err := exec.Command("taskkill.exe", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid)).Run(); err == nil {
		return nil
	}
	return cmd.Process.Kill()
}
