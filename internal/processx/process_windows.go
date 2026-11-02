//go:build windows

package processx

import (
	"errors"
	"os"
	"os/exec"
	"strconv"
	"syscall"

	"golang.org/x/sys/windows"
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
	// Retain the original process handle across taskkill and Kill, even if
	// Wait concurrently releases it. Looking the handle up by PID could bind
	// a replacement process after the original process has been reaped.
	var terminateErr error
	handleErr := cmd.Process.WithHandle(func(handle uintptr) {
		if err := exec.Command("taskkill.exe", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid)).Run(); err == nil {
			return
		}
		terminateErr = windowsTerminationError(cmd.Process.Kill(), windows.Handle(handle))
	})
	if handleErr != nil {
		// Windows always supplies a process handle at Start. Wait releases it
		// before os/exec joins its cancellation watcher; that watcher must learn
		// that it no longer owns a process to cancel, without a fresh PID lookup.
		return windowsProcessHandleError(handleErr)
	}
	return terminateErr
}

func windowsProcessHandleError(err error) error {
	// Go 1.26's Windows Wait uses the released state for compatibility. Its
	// WithHandle error has no public sentinel; match only that exact known
	// error. Unknown or future handle failures must retain their diagnostics.
	if errors.Is(err, os.ErrProcessDone) || (err != nil && err.Error() == "os: process already released") {
		return os.ErrProcessDone
	}
	return err
}

func windowsTerminationError(err error, handle windows.Handle) error {
	// Kill reports AccessDenied for an exited, unreaped process. If Wait
	// released os.Process after WithHandle retained the original handle, Kill
	// instead reports syscall.EINVAL. Both need independent proof of exit.
	if (errors.Is(err, windows.ERROR_ACCESS_DENIED) || errors.Is(err, syscall.EINVAL)) && handle != 0 {
		status, waitErr := windows.WaitForSingleObject(handle, 0)
		if waitErr == nil && status == windows.WAIT_OBJECT_0 {
			return os.ErrProcessDone
		}
	}
	return err
}
