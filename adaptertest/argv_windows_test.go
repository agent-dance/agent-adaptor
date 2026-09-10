//go:build windows

package adaptertest

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/agent-dance/agent-adaptor/internal/processx"
	"github.com/agent-dance/agent-adaptor/internal/systemprompt"
)

// This helper is the test executable, never a provider CLI. It exposes the
// actual argv observed after CreateProcess/PowerShell, rather than a quoting
// function's output. No helper mode is active in the parent process.
func TestAlignmentWindowsArgvHelper(t *testing.T) {
	if os.Getenv("AGENT_ADAPTOR_T23_ARGV_HELPER") != "1" {
		return
	}
	for i, arg := range os.Args {
		if arg == "--alignment-argv" {
			if err := json.NewEncoder(os.Stdout).Encode(os.Args[i+1:]); err != nil {
				os.Exit(2)
			}
			os.Exit(0)
		}
	}
	os.Exit(3)
}
func TestAlignmentWindowsNativeArgvRoundTrip(t *testing.T) {
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	expected := []string{"甲\n\"乙\"", "space value", `trailing \\`, "emoji 😀", ""}
	args := append([]string{"-test.run=^TestAlignmentWindowsArgvHelper$", "--", "--alignment-argv"}, expected...)
	alignmentWindowsArgv(t, binary, args, expected, true)
}
func TestAlignmentWindowsPowerShellArgvRoundTrip(t *testing.T) {
	// PowerShell receives the original provider-native text through the same
	// process preparation boundary used by the Drivers; its script reports args.
	dir := filepath.Join(t.TempDir(), "中文 script path")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(dir, "echo.ps1")
	body := "[Console]::OutputEncoding = [System.Text.UTF8Encoding]::new($false)\nConvertTo-Json -InputObject @($args) -Compress\n"
	if err := os.WriteFile(script, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	expected := []string{"甲\n\"乙\"", "space value", `trailing \\`, "emoji 😀"}
	alignmentWindowsArgv(t, script, expected, expected, false)
}
func alignmentWindowsArgv(t *testing.T, command string, args, expected []string, helper bool) {
	t.Helper()
	command, args, err := processx.PrepareCommand(command, args)
	if err != nil {
		t.Fatal(err)
	}
	if err := systemprompt.ValidateCommandLine("fixture", command, args, "windows"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, command, args...)
	cmd.Env = os.Environ()
	if helper {
		cmd.Env = append(cmd.Env, "AGENT_ADAPTOR_T23_ARGV_HELPER=1")
	}
	processx.ConfigureCancellation(cmd)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("native argument fixture: %v: %s", err, output)
	}
	var actual []string
	if err := json.Unmarshal(output, &actual); err != nil {
		t.Fatalf("native argument JSON: %v: %s", err, output)
	}
	if !reflect.DeepEqual(actual, expected) {
		t.Fatalf("native arguments=%q, want %q", actual, expected)
	}
}
