package processx

import (
	"errors"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const defaultWaitDelay = 5 * time.Second

// PrepareCommand resolves a command into a form exec.Command can launch on
// the current platform. Windows CLI installations commonly expose .cmd, .bat,
// or .ps1 shims instead of native executables; CreateProcess cannot execute
// those scripts directly, so route them through their owning shell. Keeping
// this below the Driver layer makes one-shot and persistent transports apply
// the same launch semantics.
func PrepareCommand(command string, args []string) (string, []string, error) {
	if runtime.GOOS != "windows" {
		return command, args, nil
	}

	resolved, err := exec.LookPath(command)
	if err != nil {
		return command, args, nil
	}

	switch strings.ToLower(filepath.Ext(resolved)) {
	case ".cmd", ".bat":
		return "cmd.exe", append([]string{"/d", "/s", "/c", "call", resolved}, args...), nil
	case ".ps1":
		powerShellCommand, err := resolvePowerShellCommand()
		if err != nil {
			return "", nil, err
		}
		return powerShellCommand, append([]string{"-NoLogo", "-NoProfile", "-File", resolved}, args...), nil
	default:
		return resolved, args, nil
	}
}

func resolvePowerShellCommand() (string, error) {
	for _, candidate := range []string{"pwsh.exe", "powershell.exe", "pwsh", "powershell"} {
		path, err := exec.LookPath(candidate)
		if err == nil {
			return path, nil
		}
	}
	return "", errors.New("could not locate PowerShell to execute .ps1 command")
}

// ConfigureCancellation makes exec.CommandContext cancellation reliable for
// local agent CLIs that spawn their own child processes (for example Claude
// Code plan-mode sub-agents, or a PowerShell/cmd shim on Windows).
//
// The command is isolated where supported, and cancellation terminates its
// process group/tree rather than just the leader. Output owners must still
// observe process exit independently of EOF and bound their reader joins:
// descendants can escape a group or retain inherited handles on any OS.
// WaitDelay bounds os/exec's own cancellation work; it does not bound reads
// from pipes owned by the caller (as used by clihelper and Codex app-server).
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
