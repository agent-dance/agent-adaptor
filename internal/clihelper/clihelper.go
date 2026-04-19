package clihelper

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	agentadaptor "github.com/agent-dance/agent-adaptor"
)

type CommandRequest struct {
	Command string
	Args    []string
	CWD     string
	Env     []agentadaptor.EnvBinding
	Prompt  string
}

type CommandResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

func Run(ctx context.Context, req CommandRequest, sink agentadaptor.EventSink) (CommandResult, error) {
	command, args, err := prepareCommand(req.Command, req.Args)
	if err != nil {
		return CommandResult{}, err
	}

	cmd := exec.CommandContext(ctx, command, args...)
	if req.CWD != "" {
		cmd.Dir = req.CWD
	}
	cmd.Env = mergeEnv(req.Env)

	_ = sink.Emit(agentadaptor.RunEvent{
		Type:      agentadaptor.RunEventInvocation,
		Text:      "starting command",
		Timestamp: time.Now().UTC(),
		Data: map[string]any{
			"command":  command,
			"args":     append([]string(nil), args...),
			"cwd":      req.CWD,
			"env_keys": bindingNames(req.Env),
		},
	})

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return CommandResult{}, err
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return CommandResult{}, err
	}
	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		return CommandResult{}, err
	}

	if err := cmd.Start(); err != nil {
		return CommandResult{}, err
	}

	_ = sink.Emit(agentadaptor.RunEvent{
		Type:      agentadaptor.RunEventSpawn,
		Text:      "command spawned",
		Timestamp: time.Now().UTC(),
		Data: map[string]any{
			"pid":        cmd.Process.Pid,
			"started_at": time.Now().UTC().Format(time.RFC3339Nano),
		},
	})

	writeErr := make(chan error, 1)
	go func() {
		_, copyErr := stdinPipe.Write([]byte(req.Prompt))
		closeErr := stdinPipe.Close()
		writeErr <- errors.Join(copyErr, closeErr)
	}()

	var stdoutBuf, stderrBuf bytes.Buffer
	done := make(chan struct{}, 2)

	go scanPipe(stdoutPipe, &stdoutBuf, agentadaptor.RunEventStdout, sink, done)
	go scanPipe(stderrPipe, &stderrBuf, agentadaptor.RunEventStderr, sink, done)

	waitErr := cmd.Wait()
	for range 2 {
		<-done
	}
	if err := <-writeErr; err != nil {
		return CommandResult{}, err
	}

	result := CommandResult{
		Stdout:   stdoutBuf.String(),
		Stderr:   stderrBuf.String(),
		ExitCode: 0,
	}
	if waitErr == nil {
		return result, nil
	}
	var exitErr *exec.ExitError
	if errors.As(waitErr, &exitErr) {
		result.ExitCode = exitErr.ExitCode()
		return result, nil
	}
	return result, waitErr
}

func prepareCommand(command string, args []string) (string, []string, error) {
	if runtime.GOOS != "windows" {
		return command, args, nil
	}

	resolved, err := exec.LookPath(command)
	if err != nil {
		return command, args, nil
	}

	switch strings.ToLower(filepath.Ext(resolved)) {
	case ".cmd", ".bat":
		return "cmd.exe", append([]string{"/d", "/s", "/c", resolved}, args...), nil
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

func mergeEnv(bindings []agentadaptor.EnvBinding) []string {
	env := map[string]string{}
	for _, item := range os.Environ() {
		parts := strings.SplitN(item, "=", 2)
		if len(parts) == 2 {
			env[parts[0]] = parts[1]
		}
	}
	for _, binding := range bindings {
		env[binding.Name] = binding.Value
	}
	ensureWindowsRuntimeEnv(env)
	out := make([]string, 0, len(env))
	for key, value := range env {
		out = append(out, key+"="+value)
	}
	return out
}

func ensureWindowsRuntimeEnv(env map[string]string) {
	if runtime.GOOS != "windows" {
		return
	}

	windowsRoot := envValue(env, "SystemRoot", "SYSTEMROOT", "windir", "WINDIR")
	if windowsRoot == "" {
		windowsRoot = `C:\Windows`
	}
	windowsRoot = filepath.Clean(windowsRoot)

	setEnvValue(env, "SystemRoot", windowsRoot)
	setEnvValue(env, "windir", windowsRoot)

	comSpec := envValue(env, "ComSpec", "COMSPEC")
	if comSpec == "" {
		comSpec = filepath.Join(windowsRoot, "System32", "cmd.exe")
	}
	setEnvValue(env, "ComSpec", filepath.Clean(comSpec))
}

func envValue(env map[string]string, names ...string) string {
	for _, name := range names {
		for key, value := range env {
			if strings.EqualFold(key, name) && strings.TrimSpace(value) != "" {
				return value
			}
		}
	}
	return ""
}

func setEnvValue(env map[string]string, name, value string) {
	for key := range env {
		if strings.EqualFold(key, name) {
			env[key] = value
			return
		}
	}
	env[name] = value
}

func bindingNames(bindings []agentadaptor.EnvBinding) []string {
	if len(bindings) == 0 {
		return nil
	}
	out := make([]string, 0, len(bindings))
	for _, binding := range bindings {
		if strings.TrimSpace(binding.Name) == "" {
			continue
		}
		out = append(out, binding.Name)
	}
	return out
}

func scanPipe(pipe interface{ Read([]byte) (int, error) }, buf *bytes.Buffer, eventType agentadaptor.RunEventType, sink agentadaptor.EventSink, done chan<- struct{}) {
	defer func() { done <- struct{}{} }()
	scanner := bufio.NewScanner(pipe)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		buf.WriteString(line)
		buf.WriteByte('\n')
		transcript := transcriptItemForLine(eventType, line)
		_ = sink.Emit(agentadaptor.RunEvent{
			Type:      eventType,
			Text:      line,
			Timestamp: time.Now().UTC(),
			Data: map[string]any{
				"transcript": transcript,
			},
		})
	}
}

func transcriptItemForLine(eventType agentadaptor.RunEventType, line string) agentadaptor.TranscriptItem {
	text := strings.TrimSpace(line)
	item := agentadaptor.TranscriptItem{
		Text: text,
	}
	switch eventType {
	case agentadaptor.RunEventStderr:
		item.Type = agentadaptor.TranscriptDiagnostic
	default:
		item.Type = agentadaptor.TranscriptOutput
	}
	if text == "" {
		return item
	}
	if !strings.HasPrefix(text, "{") && !strings.HasPrefix(text, "[") {
		return item
	}
	var payload any
	if err := json.Unmarshal([]byte(text), &payload); err != nil {
		return item
	}
	item.Type = agentadaptor.TranscriptStructured
	item.Metadata = map[string]string{
		"stream": string(eventType),
	}
	item.Data = map[string]any{
		"payload": payload,
	}
	return item
}
