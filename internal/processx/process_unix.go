//go:build !windows

package processx

import (
	"errors"
	"os/exec"
	"syscall"
)

// configureProcessGroup starts the child in a new process group so the entire
// process tree (including agent-spawned sub-processes) can be signalled at once
// via the negative PGID. Children inherit the group unless they deliberately
// leave it, which is exactly what we need to reap sub-agents on cancel.
func configureProcessGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

// terminate kills the child's whole process group. Because Setpgid makes the
// child a group leader, its PGID equals its PID, so signalling -PGID reaches the
// leader and every descendant that stayed in the group. This ensures inherited
// stdout/stderr pipes are closed promptly so the driver's pipe drain reaches EOF
// and cmd.Wait can return.
//
// If the group lookup or group kill fails for any reason it falls back to
// killing just the leader, preserving the previous behaviour.
func terminate(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	pid := cmd.Process.Pid
	pgid, err := syscall.Getpgid(pid)
	if err != nil {
		pgid = pid
	}
	if err := syscall.Kill(-pgid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		return cmd.Process.Kill()
	}
	return nil
}
