//go:build !windows

package testutil

import "syscall"

// ProcessAlive reports whether pid still names a live process. It is intended
// only for lifecycle tests that own the child process being inspected.
func ProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	return syscall.Kill(pid, 0) == nil
}
