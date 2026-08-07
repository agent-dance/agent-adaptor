//go:build windows

package testutil

import "syscall"

const windowsStillActive = 259

// ProcessAlive reports whether pid still names a live process. Opening the
// process handle avoids localized tasklist output and gives Windows lifecycle
// tests the same reliable assertion as kill(pid, 0) on POSIX.
func ProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	handle, err := syscall.OpenProcess(syscall.PROCESS_QUERY_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer syscall.CloseHandle(handle)
	var exitCode uint32
	return syscall.GetExitCodeProcess(handle, &exitCode) == nil && exitCode == windowsStillActive
}
