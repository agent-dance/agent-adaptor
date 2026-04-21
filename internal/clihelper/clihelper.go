package clihelper

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	agentadaptor "github.com/agent-dance/agent-adaptor"
)

// ChunkObserver is the adapter-supplied callback invoked once per raw chunk
// read from stdout or stderr.
//
// Semantics:
//   - stream is either "stdout" or "stderr".
//   - chunk may not be a complete line; adapters are responsible for any
//     line buffering.
//   - helper emits the RunEventChunk event BEFORE invoking Observe so that
//     downstream consumers can still correlate the raw event with any
//     transcript item the adapter subsequently emits.
//   - Returning a non-nil error aborts the run with that error.
type ChunkObserver func(stream string, chunk []byte, ts time.Time) error

type CommandRequest struct {
	Command string
	Args    []string
	CWD     string
	Env     []agentadaptor.EnvBinding
	Prompt  string

	// Observe is optional. When set, the helper calls it for every raw chunk
	// it reads from stdout/stderr. The helper itself never parses or
	// interprets the chunk bytes.
	Observe ChunkObserver
}

type CommandResult struct {
	RawStreams agentadaptor.RawStreams
	ExitCode   int
	Signal     string
	TimedOut   bool
}

// Run executes the requested command and streams raw stdout/stderr chunks to
// sink and (optionally) the Observe callback. The returned CommandResult
// always contains the full captured RawStreams, regardless of exit status.
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

	var (
		stdoutBuf bytes.Buffer
		stderrBuf bytes.Buffer
		bufMu     sync.Mutex
		observeMu sync.Mutex
		firstErr  error
		errMu     sync.Mutex
	)

	recordErr := func(e error) {
		if e == nil {
			return
		}
		errMu.Lock()
		if firstErr == nil {
			firstErr = e
		}
		errMu.Unlock()
	}

	readPump := func(pipe io.Reader, stream string, buf *bytes.Buffer) {
		readBuf := make([]byte, 32*1024)
		for {
			n, readErr := pipe.Read(readBuf)
			if n > 0 {
				chunk := make([]byte, n)
				copy(chunk, readBuf[:n])
				ts := time.Now().UTC()

				bufMu.Lock()
				buf.Write(chunk)
				bufMu.Unlock()

				_ = sink.Emit(agentadaptor.RunEvent{
					Type:      agentadaptor.RunEventChunk,
					Timestamp: ts,
					Stream:    stream,
					Bytes:     append([]byte(nil), chunk...),
				})

				if req.Observe != nil {
					observeMu.Lock()
					obsErr := req.Observe(stream, chunk, ts)
					observeMu.Unlock()
					if obsErr != nil {
						recordErr(obsErr)
						_ = cmd.Process.Kill()
						return
					}
				}
			}
			if readErr != nil {
				if !errors.Is(readErr, io.EOF) {
					recordErr(readErr)
				}
				return
			}
		}
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		readPump(stdoutPipe, "stdout", &stdoutBuf)
	}()
	go func() {
		defer wg.Done()
		readPump(stderrPipe, "stderr", &stderrBuf)
	}()

	// Drain the output pipes completely before Wait() so that cmd.Wait()
	// does not close the pipe out from under a reader. This matches the
	// contract described in os/exec.Cmd.StdoutPipe.
	wg.Wait()
	waitErr := cmd.Wait()
	if err := <-writeErr; err != nil {
		recordErr(err)
	}

	bufMu.Lock()
	result := CommandResult{
		RawStreams: agentadaptor.RawStreams{
			Stdout: stdoutBuf.String(),
			Stderr: stderrBuf.String(),
		},
	}
	bufMu.Unlock()

	if ctxErr := ctx.Err(); ctxErr != nil {
		result.TimedOut = errors.Is(ctxErr, context.DeadlineExceeded)
	}

	if waitErr == nil {
		errMu.Lock()
		fe := firstErr
		errMu.Unlock()
		if fe != nil {
			return result, fe
		}
		return result, nil
	}
	var exitErr *exec.ExitError
	if errors.As(waitErr, &exitErr) {
		result.ExitCode = exitErr.ExitCode()
		if state := exitErr.ProcessState; state != nil && !state.Exited() {
			result.Signal = state.String()
		}
		errMu.Lock()
		fe := firstErr
		errMu.Unlock()
		if fe != nil {
			return result, fe
		}
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
