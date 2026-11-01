package claude

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"

	"github.com/agent-dance/agent-adaptor/v2/internal/testutil"
)

const claudeScriptedHelperArg = "--agent-adaptor-claude-scripted-helper"

// writeClaudeCommand keeps static protocol scripts usable with both batch
// stdin and stream-json input. The shared helper handles initialization and
// passes only the user prompt to scripts that read their input through EOF.
func writeClaudeCommand(t testing.TB, dir, name, posixBody, windowsBody string) string {
	t.Helper()
	script := testutil.WriteCommand(t, dir, name+"-protocol", posixBody, windowsBody)
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
	return testutil.WriteCommand(t, dir, name,
		"#!/bin/sh\nexec "+quote(executable)+" "+claudeScriptedHelperArg+" "+quote(script)+" \"$@\"\n",
		"@echo off\r\n\""+executable+"\" "+claudeScriptedHelperArg+" \""+script+"\" %*\r\n",
	)
}

// acknowledgeClaudeInitialize uses the same success envelope as Claude's
// control protocol and echoes the request's ID rather than treating it as a
// user turn. Both shared scripted providers use this handshake.
func acknowledgeClaudeInitialize(frame map[string]any, enc *json.Encoder) (bool, error) {
	if claudeTopLevelString(frame, "type") != "control_request" ||
		claudeTopLevelString(claudeTopLevelObject(frame, "request"), "subtype") != "initialize" {
		return false, nil
	}
	return true, enc.Encode(map[string]any{
		"type": "control_response", "response": map[string]any{
			"subtype": "success", "request_id": claudeTopLevelString(frame, "request_id"), "response": map[string]any{},
		},
	})
}

func runClaudeScriptedHelper() int {
	if len(os.Args) < 3 {
		return 20
	}
	args := os.Args[3:]
	cmd := exec.Command(os.Args[2], args...)
	if runtime.GOOS == "windows" {
		cmd = exec.Command("cmd", append([]string{"/c", os.Args[2]}, args...)...)
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if claudePersistentInputMode(args) {
		dec, enc := json.NewDecoder(os.Stdin), json.NewEncoder(os.Stdout)
		for {
			var frame map[string]any
			if err := dec.Decode(&frame); err != nil {
				return 21
			}
			if handled, err := acknowledgeClaudeInitialize(frame, enc); handled {
				if err != nil {
					return 22
				}
				continue
			}
			if claudeTopLevelString(frame, "type") != "user" {
				return 23
			}
			cmd.Stdin = strings.NewReader(claudeTopLevelString(claudeTopLevelObject(frame, "message"), "content"))
			break
		}
	}
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return exitErr.ExitCode()
		}
		return 24
	}
	return 0
}
