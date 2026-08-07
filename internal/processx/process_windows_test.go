//go:build windows

package processx

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/agent-dance/agent-adaptor/internal/testutil"
)

const processWindowsHelperEnv = "GO_WANT_AGENT_ADAPTOR_PROCESS_WINDOWS_HELPER"

func TestMain(m *testing.M) {
	if mode := os.Getenv(processWindowsHelperEnv); mode != "" {
		os.Exit(runProcessWindowsHelper(mode))
	}
	os.Exit(m.Run())
}

func TestPrepareCommandLaunchesBatchShim(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	shimDir := filepath.Join(t.TempDir(), "shim dir")
	if err := os.MkdirAll(shimDir, 0o700); err != nil {
		t.Fatal(err)
	}
	shim := filepath.Join(shimDir, "persistent-helper.cmd")
	body := "@echo off\r\n\"%PROCESSX_TEST_EXE%\" %*\r\n"
	if err := os.WriteFile(shim, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	command, args, err := PrepareCommand(shim, []string{"alpha", "two words", `quote"value`, `%PROCESSX_UNSET_VALUE%`})
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(command, args...)
	cmd.Env = append(os.Environ(),
		processWindowsHelperEnv+"=echo",
		"PROCESSX_TEST_EXE="+executable,
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("launch batch shim: %v\n%s", err, output)
	}
	if got := strings.TrimSpace(string(output)); got != `alpha|two words|quote"value|%PROCESSX_UNSET_VALUE%` {
		t.Fatalf("batch shim args=%q", got)
	}
}

func TestConfigureCancellationTerminatesWindowsProcessTree(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, executable)
	cmd.Env = append(os.Environ(), processWindowsHelperEnv+"=parent")
	ConfigureCancellation(cmd)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}

	scanner := bufio.NewScanner(stdout)
	if !scanner.Scan() {
		t.Fatalf("helper did not report child pid: %v", scanner.Err())
	}
	parts := strings.Split(strings.TrimSpace(scanner.Text()), ":")
	if len(parts) != 2 || parts[0] != "ready" {
		t.Fatalf("unexpected helper readiness %q", scanner.Text())
	}
	childPID, err := strconv.Atoi(parts[1])
	if err != nil || childPID <= 0 {
		t.Fatalf("invalid child pid %q", parts[1])
	}

	eof := make(chan struct{})
	go func() {
		for scanner.Scan() {
		}
		close(eof)
	}()
	wait := make(chan error, 1)
	go func() { wait <- cmd.Wait() }()
	cancel()

	select {
	case <-eof:
	case <-time.After(5 * time.Second):
		t.Fatal("stdout did not reach EOF after cancellation")
	}
	select {
	case <-wait:
	case <-time.After(5 * time.Second):
		t.Fatal("process tree did not exit after cancellation")
	}
	deadline := time.Now().Add(3 * time.Second)
	for testutil.ProcessAlive(childPID) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if testutil.ProcessAlive(childPID) {
		t.Fatalf("descendant pid %d survived process-tree cancellation", childPID)
	}
}

func runProcessWindowsHelper(mode string) int {
	switch mode {
	case "echo":
		_, _ = fmt.Fprintln(os.Stdout, strings.Join(os.Args[1:], "|"))
		return 0
	case "child":
		for {
			time.Sleep(time.Hour)
		}
	case "parent":
		executable, err := os.Executable()
		if err != nil {
			return 2
		}
		child := exec.Command(executable)
		child.Env = append(os.Environ(), processWindowsHelperEnv+"=child")
		child.Stdout = os.Stdout
		child.Stderr = os.Stderr
		if err := child.Start(); err != nil {
			return 3
		}
		_, _ = fmt.Fprintf(os.Stdout, "ready:%d\n", child.Process.Pid)
		for {
			time.Sleep(time.Hour)
		}
	default:
		return 4
	}
}
