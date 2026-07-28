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

	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/processx"
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

// ErrStdinClosed is returned from StdinController.Write when the subprocess
// stdin has already been closed (either because the subprocess exited or
// because an earlier write failed). Callers treat this as an abort signal
// for any pending HITL flow.
var ErrStdinClosed = errors.New("clihelper: stdin closed")

// StdinController lets an adapter drive the subprocess stdin throughout the
// run instead of using the default one-shot prompt lifecycle.
//
// Intended for HITL-interactive adapters (for example Claude's bidirectional
// stream-json mode) that need to inject user tool_result frames after the
// CLI emits a tool_use event.
//
// Concurrency:
//   - Write is safe to call from the parser goroutine (clihelper serialises
//     writes internally).
//   - The caller does NOT need to call Close; the helper closes stdin when
//     the run ends (subprocess exit or ctx cancel). Close is provided for
//     adapters that want to signal "no more input" earlier than that.
type StdinController interface {
	Write(frame []byte) error
	Close() error
}

// NewStdinController returns a new StdinController tied to a single
// subprocess. It is populated by clihelper.Run when the caller sets
// CommandRequest.Stdin to this value; callers create it up front so they
// can start a goroutine that writes frames as soon as the subprocess
// spawns.
//
//	ctrl := clihelper.NewStdinController()
//	go func() {
//	    ctrl.Write([]byte(`{"type":"user",...}` + "\n"))
//	    // … react to parser events, write more frames …
//	}()
//	clihelper.Run(ctx, CommandRequest{Stdin: ctrl, …}, sink)
func NewStdinController() StdinController {
	return newStdinController()
}

// newStdinController is the internal constructor. It returns the concrete
// pointer type so helper code (clihelper.Run) can call unexported methods
// like signalReady / markDone / access .ch directly.
func newStdinController() *stdinController {
	return &stdinController{
		ch:      make(chan []byte, 16),
		ready:   make(chan struct{}),
		closing: make(chan struct{}),
		done:    make(chan struct{}),
	}
}

// stdinController is the concrete StdinController. Exported only via its
// interface — constructor returns the pointer type so callers can keep an
// unexported reference across their own API boundary.
type stdinController struct {
	mu     sync.Mutex
	closed bool

	// ch is the queue from Write() to the helper's writer goroutine.
	ch chan []byte
	// ready is closed once the helper has the subprocess stdin pipe and is
	// ready to drain ch. Write() blocks on this before enqueuing so early
	// calls (before cmd.Start()) do not deadlock on a full channel.
	ready chan struct{}
	// closing is closed by Close to stop accepting new frames while still
	// allowing the writer goroutine to drain whatever is already queued.
	closing chan struct{}
	// done is closed by the helper once the writer goroutine has finished
	// draining (or the subprocess exited). After done is closed, every
	// Write returns ErrStdinClosed.
	done chan struct{}
}

// Write enqueues a frame. Helper writes it to the subprocess stdin in FIFO
// order. Returns ErrStdinClosed if the helper has already drained and closed.
// Frames SHOULD end with a newline so the CLI's NDJSON parser commits them
// one at a time; callers are responsible for that convention.
func (s *stdinController) Write(frame []byte) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return ErrStdinClosed
	}
	s.mu.Unlock()

	// Wait for the helper to be ready (subprocess spawned). Done channel
	// covers the case where the subprocess died before we got to write.
	select {
	case <-s.ready:
	case <-s.closing:
		return ErrStdinClosed
	case <-s.done:
		return ErrStdinClosed
	}

	select {
	case s.ch <- frame:
		return nil
	case <-s.closing:
		return ErrStdinClosed
	case <-s.done:
		return ErrStdinClosed
	}
}

// Close signals "no more input". After Close returns, subsequent Writes
// return ErrStdinClosed. The helper drains any queued frames before the
// subprocess stdin is closed.
func (s *stdinController) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.mu.Unlock()
	select {
	case <-s.closing:
	default:
		close(s.closing)
	}
	return nil
}

// signalReady is called by the helper once the subprocess is spawned and
// the stdin pipe is available.
func (s *stdinController) signalReady() {
	select {
	case <-s.ready:
	default:
		close(s.ready)
	}
}

// markDone is called by the helper once the subprocess exited or the writer
// goroutine returned. After markDone, writers see ErrStdinClosed.
func (s *stdinController) markDone() {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	select {
	case <-s.done:
	default:
		close(s.done)
	}
}

type CommandRequest struct {
	Command string
	Args    []string
	CWD     string
	Env     []driver.EnvBinding
	Prompt  string

	// Observe is optional. When set, the helper calls it for every raw chunk
	// it reads from stdout/stderr. The helper itself never parses or
	// interprets the chunk bytes.
	Observe ChunkObserver

	// Stdin controls how the subprocess stdin is fed.
	//
	//   - Stdin == nil (default): the helper writes
	//     Prompt once and closes stdin immediately. Use this for one-shot
	//     prompt pipelines.
	//   - Stdin != nil: the helper enables long-lived stdin. Prompt (if
	//     non-empty) is written as the first frame, then the helper
	//     forwards every frame arriving via Stdin.Write until either
	//     Stdin.Close is called, ctx is cancelled, or the subprocess
	//     exits.
	//
	// Helper owns the lifecycle after Run returns: stdin is always closed
	// on the way out.
	Stdin StdinController
}

type CommandResult struct {
	RawStreams driver.RawStreams
	ExitCode   int
	Signal     string
	TimedOut   bool
}

const interruptedExitCode = -1

// Run executes the requested command and streams raw stdout/stderr chunks to
// sink and (optionally) the Observe callback. The returned CommandResult
// always contains the full captured RawStreams, regardless of exit status.
// Once a process starts, a non-zero exit, signal, or context-driven process
// termination is represented in CommandResult rather than returned as an
// error. This lets the Driver parse the complete provider protocol first;
// the common invocation boundary supplies a structured fallback when the
// provider protocol offers no more specific failure. Returned errors are
// reserved for launch, pipe, observation, and other helper failures.
func Run(ctx context.Context, req CommandRequest, sink driver.EventSink) (CommandResult, error) {
	command, args, err := prepareCommand(req.Command, req.Args)
	if err != nil {
		return CommandResult{}, err
	}

	cmd := exec.CommandContext(ctx, command, args...)
	processx.ConfigureCancellation(cmd)
	if req.CWD != "" {
		cmd.Dir = req.CWD
	}
	cmd.Env = mergeEnv(req.Env)

	_ = sink.Emit(driver.RunEvent{
		Type:      driver.RunEventInvocation,
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

	_ = sink.Emit(driver.RunEvent{
		Type:      driver.RunEventSpawn,
		Text:      "command spawned",
		Timestamp: time.Now().UTC(),
		Data: map[string]any{
			"pid":        cmd.Process.Pid,
			"started_at": time.Now().UTC().Format(time.RFC3339Nano),
		},
	})

	writeErr := make(chan error, 1)
	if req.Stdin == nil {
		// Default one-shot prompt path.
		go func() {
			_, copyErr := stdinPipe.Write([]byte(req.Prompt))
			closeErr := stdinPipe.Close()
			writeErr <- errors.Join(copyErr, closeErr)
		}()
	} else {
		// Long-lived stdin path. The helper writes Prompt (if any) as the
		// first frame, then forwards frames arriving on req.Stdin.Write
		// until the controller is closed, the subprocess exits, or ctx
		// fires.
		ctrl, ok := req.Stdin.(*stdinController)
		if !ok {
			return CommandResult{}, errors.New("clihelper: Stdin must be constructed via NewStdinController()")
		}
		go func() {
			defer func() {
				_ = stdinPipe.Close()
				ctrl.markDone()
			}()

			// Announce readiness before draining so early Write() calls can
			// proceed instead of blocking on ch forever.
			ctrl.signalReady()

			// Write the initial Prompt if one was supplied. Adapters using
			// stream-json bidirectional typically put the initial user
			// message here (a single NDJSON line) so the CLI starts turn 1.
			var firstErr error
			if req.Prompt != "" {
				if _, err := stdinPipe.Write([]byte(req.Prompt)); err != nil {
					firstErr = err
				}
			}

			// Drain frames until Close(), markDone(), or ctx fires. A non-nil
			// write error ends the loop — the subprocess likely closed its
			// stdin or exited.
			draining := false
			for firstErr == nil {
				if draining {
					select {
					case frame := <-ctrl.ch:
						if _, err := stdinPipe.Write(frame); err != nil {
							firstErr = err
						}
					default:
						writeErr <- nil
						return
					}
					continue
				}
				select {
				case frame := <-ctrl.ch:
					if _, err := stdinPipe.Write(frame); err != nil {
						firstErr = err
					}
				case <-ctrl.closing:
					// Caller called Close(); stop accepting new frames but
					// drain whatever was already queued first.
					draining = true
				case <-ctrl.done:
					writeErr <- nil
					return
				case <-ctx.Done():
					writeErr <- ctx.Err()
					return
				}
			}

			writeErr <- firstErr
		}()
	}

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

				_ = sink.Emit(driver.RunEvent{
					Type:      driver.RunEventChunk,
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

	// Long-lived stdin: force the writer goroutine to exit (otherwise it
	// may block on an empty ctrl.ch indefinitely now that the subprocess
	// is gone).
	if ctrl, ok := req.Stdin.(*stdinController); ok {
		ctrl.markDone()
	}

	if err := <-writeErr; err != nil {
		// ctx.Canceled / ctx.DeadlineExceeded are expected when the caller
		// aborts; surface them through the normal ctxErr path below but do
		// not double-report via recordErr (that would mask the real cause
		// the caller already observes via ctx).
		if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			recordErr(err)
		}
	}

	bufMu.Lock()
	result := CommandResult{
		RawStreams: driver.RawStreams{
			Stdout: stdoutBuf.String(),
			Stderr: stderrBuf.String(),
		},
	}
	bufMu.Unlock()

	errMu.Lock()
	fe := firstErr
	errMu.Unlock()
	return finalizeCommandResult(result, waitErr, fe, ctx.Err())
}

func finalizeCommandResult(result CommandResult, waitErr, firstErr, ctxErr error) (CommandResult, error) {
	if ctxErr != nil {
		result.TimedOut = errors.Is(ctxErr, context.DeadlineExceeded)
	}

	if waitErr == nil {
		if firstErr != nil {
			return result, firstErr
		}
		return result, nil
	}

	var exitErr *exec.ExitError
	if errors.As(waitErr, &exitErr) {
		result.ExitCode = exitErr.ExitCode()
		if state := exitErr.ProcessState; state != nil && !state.Exited() {
			result.Signal = state.String()
		}
		if firstErr != nil {
			return result, firstErr
		}
		return result, nil
	}

	if ctxErr != nil {
		if result.ExitCode == 0 {
			result.ExitCode = interruptedExitCode
		}
		if result.Signal == "" {
			result.Signal = ctxErr.Error()
		}
		if firstErr != nil {
			return result, firstErr
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

func mergeEnv(bindings []driver.EnvBinding) []string {
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

func bindingNames(bindings []driver.EnvBinding) []string {
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
