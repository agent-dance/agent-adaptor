package testutil

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ProtocolCommand runs the current test binary as a hermetic CLI. Its owning
// test package must expose TestProtocolProcess calling ProtocolProcess.
func ProtocolCommand(t testing.TB, stdout, stderr string, exitCode int) (command, capture string) {
	t.Helper()
	dir := t.TempDir()
	for name, content := range map[string]string{"stdout": stdout, "stderr": stderr} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	capture = filepath.Join(dir, "capture.json")
	t.Setenv("AGENT_ADAPTOR_PROTOCOL_HELPER", dir)
	t.Setenv("AGENT_ADAPTOR_PROTOCOL_EXIT", fmt.Sprint(exitCode))
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
	command = WriteCommand(t, dir, "provider", "#!/bin/sh\nexec "+quote(executable)+" -test.run=^TestProtocolProcess$ -- \"$@\"\n", "@echo off\r\n\""+executable+"\" -test.run=TestProtocolProcess -- %*\r\nexit /b %errorlevel%\r\n")
	return command, capture
}

// ProtocolProcess captures only the tested invocation and a named config
// overlay, never the parent process's environment or credentials.
func ProtocolProcess(t *testing.T) {
	dir := os.Getenv("AGENT_ADAPTOR_PROTOCOL_HELPER")
	if dir == "" {
		return
	}
	for _, arg := range os.Args {
		if arg == "rpc" {
			PiRPCProcess(dir)
			return
		}
		if arg == "serve" {
			OpenCodeServerProcess(dir)
			return
		}
		if arg == "db" {
			name := "permission-query.json"
			if _, err := os.Stat(filepath.Join(dir, "permission-cache-loaded")); err == nil {
				if _, err := os.Stat(filepath.Join(dir, "permission-query-after.json")); err == nil {
					name = "permission-query-after.json"
				}
			}
			raw, err := os.ReadFile(filepath.Join(dir, name))
			if os.IsNotExist(err) {
				raw = []byte("[]")
			}
			_, _ = os.Stdout.Write(raw)
			if f, err := os.OpenFile(filepath.Join(dir, "permission-probes.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600); err == nil {
				fmt.Fprintln(f, "probe")
				f.Close()
			}
			os.Exit(0)
		}
	}
	prompt, err := io.ReadAll(os.Stdin)
	if err != nil {
		os.Exit(91)
	}
	cwd, _ := os.Getwd()
	raw, _ := json.Marshal(struct {
		Args   []string
		Prompt string
		CWD    string
		Config string
	}{os.Args, string(prompt), cwd, os.Getenv("OPENCODE_CONFIG_CONTENT")})
	if os.WriteFile(filepath.Join(dir, "capture.json"), raw, 0600) != nil {
		os.Exit(92)
	}
	stdout, _ := os.ReadFile(filepath.Join(dir, "stdout"))
	stderr, _ := os.ReadFile(filepath.Join(dir, "stderr"))
	_, _ = os.Stdout.Write(stdout)
	_, _ = os.Stderr.Write(stderr)
	if os.Getenv("AGENT_ADAPTOR_PROTOCOL_HANG") == "1" {
		time.Sleep(5 * time.Minute)
	}
	var code int
	_, _ = fmt.Sscan(os.Getenv("AGENT_ADAPTOR_PROTOCOL_EXIT"), &code)
	os.Exit(code)
}
