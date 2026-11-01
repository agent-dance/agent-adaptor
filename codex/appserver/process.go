package appserver

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"time"

	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/processx"
)

// Process owns one initialized app-server connection and one loaded Codex
// thread. Callers must serialize RunTurn calls; Process also enforces that
// rule so an accidental second writer cannot interleave JSON-RPC turns.
type Process struct {
	client   *Client
	stream   *stdioStream
	cmd      *exec.Cmd
	cancel   context.CancelFunc
	threadID string
	stdout   *syncBuffer
	stderr   *syncBuffer

	turnMu        sync.Mutex
	closeMu       sync.Mutex
	closed        bool
	stopRequested bool          // distinguishes our shutdown from an independent peer exit
	exitCh        chan struct{} // process exit, independent of inherited output writers
	waitCh        chan struct{} // exit plus both reader joins
	waitErr       error
}

// Open spawns and initializes an app-server and starts or resumes one thread,
// but deliberately sends no user prompt. The returned Process can therefore
// be pre-warmed without risking duplicate model/tool side effects.
func Open(ctx context.Context, opts Options, sink driver.EventSink) (*Process, error) {
	if opts.ResumeThreadID != "" && opts.ForkThreadID != "" {
		return nil, errors.New("codex app-server: resume and fork thread ids are mutually exclusive")
	}
	command := opts.Command
	if command == "" {
		command = "codex"
	}
	args := append([]string{"app-server", "--listen", "stdio://"}, opts.ExtraArgs...)
	command, args, err := processx.PrepareCommand(command, args)
	if err != nil {
		return nil, fmt.Errorf("codex app-server command: %w", err)
	}
	procCtx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(procCtx, command, args...)
	processx.ConfigureCancellation(cmd)
	if opts.CWD != "" {
		cmd.Dir = opts.CWD
	}
	cmd.Env = toExecEnv(opts.Env)

	output, err := newProcessOutput(cmd, opts.processHooks)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("codex app-server output pipes: %w", err)
	}
	stdinRead, stdin, err := processx.NewPipe(opts.processHooks.closed)
	if err != nil {
		output.close()
		cancel()
		return nil, fmt.Errorf("codex app-server stdin: %w", err)
	}
	cmd.Stdin = stdinRead.File
	if err := cmd.Start(); err != nil {
		_ = stdinRead.Close()
		_ = stdin.Close()
		output.close()
		cancel()
		return nil, fmt.Errorf("codex app-server start: %w", err)
	}
	_ = stdinRead.Close() // Start borrowed the read end; the child has its own copy.

	opts.processHooks.started(output)
	output.closeWriters()

	if sink != nil {
		_ = sink.Emit(driver.RunEvent{
			Type:      driver.RunEventSpawn,
			Timestamp: time.Now().UTC(),
			Text:      "codex app-server spawned",
			Metadata:  map[string]string{"command": command, "mode": "app-server"},
			Data: map[string]any{
				"pid":  cmd.Process.Pid,
				"args": append([]string(nil), args...),
			},
		})
	}

	stdoutBuf := &syncBuffer{}
	stderrBuf := &syncBuffer{}
	stderrDone := make(chan struct{})
	stderrReader := opts.processHooks.stderr(output.stderr)
	go func() {
		defer close(stderrDone)
		_, _ = io.Copy(stderrBuf, stderrReader)
	}()
	stream := newStdioStream(stdin, io.TeeReader(opts.processHooks.stdout(outputPipeReader{output.stdout}), stdoutBuf))
	client := NewClient(procCtx, stream)
	p := &Process{
		client: client, stream: stream, cmd: cmd, cancel: cancel,
		stdout: stdoutBuf, stderr: stderrBuf, exitCh: make(chan struct{}), waitCh: make(chan struct{}),
	}
	go func() {
		_ = output.wait(cmd, stream, stderrDone, func(err error) {
			_ = stdin.Close()
			p.waitErr = err
			close(p.exitCh)
		}, opts.processHooks)
		p.closeMu.Lock()
		p.closed = true
		p.closeMu.Unlock()
		close(p.waitCh)
	}()
	fail := func(cause error) (*Process, error) {
		_ = p.TerminateAndWait(context.Background())
		return nil, cause
	}

	clientInfo := ClientInfo{
		Name:    nonEmpty(opts.ClientName, "agent-adaptor"),
		Version: nonEmpty(opts.ClientVersion, "0.0.0"),
	}
	if _, err := client.Initialize(ctx, InitializeParams{ClientInfo: clientInfo}); err != nil {
		return fail(fmt.Errorf("codex app-server initialize: %w", err))
	}
	if err := client.NotifyInitialized(ctx); err != nil {
		return fail(fmt.Errorf("codex app-server initialized: %w", err))
	}

	var threadID string
	switch {
	case opts.ForkThreadID != "":
		resp, err := client.ThreadFork(ctx, ThreadForkParams{
			ThreadID: opts.ForkThreadID, CWD: opts.CWD, Ephemeral: opts.Ephemeral,
			Sandbox: opts.Sandbox, Model: opts.Model, ServiceTier: opts.ServiceTier,
			ApprovalPolicy: opts.Approval,
		})
		if err != nil {
			return fail(classifyThreadError(err, opts.ForkThreadID))
		}
		threadID = resp.Thread.ID
	case opts.ResumeThreadID != "":
		resp, err := client.ThreadResume(ctx, ThreadResumeParams{ThreadID: opts.ResumeThreadID})
		if err != nil {
			return fail(classifyThreadError(err, opts.ResumeThreadID))
		}
		threadID = resp.Thread.ID
	default:
		resp, err := client.ThreadStart(ctx, ThreadStartParams{
			CWD: opts.CWD, Ephemeral: opts.Ephemeral, Sandbox: opts.Sandbox,
			Model: opts.Model, ServiceTier: opts.ServiceTier,
		})
		if err != nil {
			return fail(fmt.Errorf("codex app-server thread/start: %w", err))
		}
		threadID = resp.Thread.ID
	}
	if threadID == "" {
		return fail(errors.New("codex app-server returned an empty thread id"))
	}
	p.threadID = threadID
	return p, nil
}

// ThreadID is the provider thread loaded by this process.
func (p *Process) ThreadID() string {
	if p == nil {
		return ""
	}
	return p.threadID
}

// Done closes after process exit and both stdout and stderr readers finish.
func (p *Process) Done() <-chan struct{} {
	if p == nil {
		done := make(chan struct{})
		close(done)
		return done
	}
	return p.waitCh
}

// IsClosed reports whether shutdown has started or the peer disconnected.
func (p *Process) IsClosed() bool {
	if p == nil {
		return true
	}
	p.closeMu.Lock()
	closed := p.closed
	p.closeMu.Unlock()
	if closed {
		return true
	}
	select {
	case <-p.exitCh:
		return true
	case <-p.client.DisconnectNotify():
		return true
	default:
		return false
	}
}

// RunTurn sends exactly one prompt on the loaded thread. promptSent becomes
// true immediately before turn/start because an RPC error cannot prove the
// peer did not receive the request; callers must not replay in that case.
// Healthy turns retain the live process and return bytes observed during the
// turn. Failed/cancelled/disconnected turns join process exit and both readers
// before capturing RawStreams, with draining bounded to five seconds after exit.
func (p *Process) RunTurn(ctx context.Context, opts Options, sink driver.EventSink) (result driver.Response, promptSent bool, err error) {
	if p == nil {
		return result, false, errors.New("codex app-server process is nil")
	}
	p.turnMu.Lock()
	defer p.turnMu.Unlock()
	if err := ctx.Err(); err != nil {
		return result, false, err
	}
	if p.IsClosed() {
		return result, false, errors.New("codex app-server process is closed")
	}

	stdoutStart := p.stdout.Len()
	stderrStart := p.stderr.Len()
	state := newRunState(opts.RunID, sink)
	state.setThread(p.threadID)
	p.client.SetNotificationHandler(state.onNotification)
	defer p.client.SetNotificationHandler(nil)

	turnParams := TurnStartParams{
		ThreadID:    p.threadID,
		Input:       []UserInput{TextInput(opts.Prompt)},
		CWD:         opts.CWD,
		Model:       opts.Model,
		Effort:      opts.Effort,
		ServiceTier: opts.ServiceTier,
	}
	if opts.OutputSchema != nil {
		turnParams.OutputSchema = append([]byte(nil), opts.OutputSchema.SchemaJSON...)
	}
	if opts.Approval != "" {
		turnParams.ApprovalPolicy = opts.Approval
	}
	if opts.Sandbox != "" {
		turnParams.SandboxPolicy = sandboxPolicyFor(opts.Sandbox)
	}

	promptSent = true
	turn, startErr := p.client.TurnStart(ctx, turnParams)
	if startErr != nil {
		err = fmt.Errorf("codex app-server turn/start: %w", startErr)
	} else {
		state.setTurn(turn.Turn.ID)
	}
	if err == nil {
		select {
		case <-state.done:
		case <-ctx.Done():
			interruptCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 500*time.Millisecond)
			_ = p.client.TurnInterrupt(interruptCtx, TurnInterruptParams{ThreadID: p.threadID, TurnID: turn.Turn.ID})
			cancel()
			err = ctx.Err()
		case <-p.client.DisconnectNotify():
			if !state.hasTerminal() {
				err = errors.New("codex app-server disconnected before turn completion")
			}
		case <-p.exitCh:
			// Exit can precede decoding buffered terminal frames. Preserve them
			// by joining the bounded drain before judging turn completeness.
			<-p.waitCh
			if p.waitErr != nil {
				err = fmt.Errorf("codex app-server exited before turn completion: %w", p.waitErr)
			} else if !state.hasTerminal() {
				err = errors.New("codex app-server exited before turn completion")
			}
		}
	}
	if err == nil {
		if protocolErr := state.protocolError(); protocolErr != nil {
			err = protocolErr
		} else if !state.hasTerminal() {
			err = errors.New("codex app-server protocol ended without turn/completed")
		}
	}

	// Capture external cancellation before initiating our own cleanup, so a
	// failed provider/transport is never reclassified as a host cancellation.
	cancellation := p.cancellation(ctx)
	if cancellation != nil {
		err = cancellation
	}
	if err != nil {
		if cancellation != nil {
			_ = p.terminateAndWait(context.Background(), false)
		} else {
			// Protocol/RPC failure need not imply process failure. Let the peer
			// finish writing diagnostics before escalating to a group kill.
			_ = p.closeGracefully(context.Background(), 500*time.Millisecond, false)
		}
	}
	if p.IsClosed() {
		_ = p.closeGracefully(context.Background(), 500*time.Millisecond, false)
		if readErr := p.stream.ReadError(); readErr != nil {
			state.recordProtocolError(fmt.Errorf("decode JSON-RPC stdout: %w", readErr))
			if err == nil {
				err = state.protocolError()
			}
		}
		// Disconnect and exit can both be ready after the drain cutoff. Keep
		// the actual exit cause regardless of which signal won the select.
		if p.waitErr != nil && cancellation == nil && !errors.Is(err, p.waitErr) {
			err = fmt.Errorf("codex app-server exited: %w", errors.Join(p.waitErr, err))
		}
	}
	// Validate semantic output before deciding whether to retain a live writer.
	// Raw streams are attached only after any required shutdown/reader join.
	exitCode, signal, timedOut := 0, "", false
	if err != nil {
		exitCode, signal, timedOut = -1, err.Error(), errors.Is(err, context.DeadlineExceeded)
	}
	result = state.snapshot(opts, p.threadID, "", "", exitCode, signal, timedOut, cancellation)
	result = finalizeAppServerStructuredOutput(opts.OutputSchema, result)
	if result.Failure != nil || result.Checkpoint == nil {
		_ = p.closeGracefully(context.Background(), 500*time.Millisecond, false)
	}
	// Close or caller cancellation can race output validation and shutdown.
	if cancellation = p.cancellation(ctx); cancellation != nil {
		err = cancellation
		_ = p.terminateAndWait(context.Background(), false)
		result = state.snapshot(opts, p.threadID, "", "", -1, cancellation.Error(), errors.Is(cancellation, context.DeadlineExceeded), cancellation)
		result = finalizeAppServerStructuredOutput(opts.OutputSchema, result)
	}
	result.RawStreams.Stdout = p.stdout.Since(stdoutStart)
	result.RawStreams.Stderr = p.stderr.Since(stderrStart)
	state.finishPublicResult(result, err)
	return result, promptSent, err
}

// TerminateAndWait kills the independently grouped process and reaps it.
// An active turn reports FailureCancelled and cannot produce a checkpoint.
func (p *Process) TerminateAndWait(ctx context.Context) error {
	return p.terminateAndWait(ctx, true)
}

func (p *Process) terminateAndWait(ctx context.Context, requested bool) error {
	if p == nil {
		return nil
	}
	p.closeMu.Lock()
	if requested {
		select {
		case <-p.waitCh:
		default:
			p.stopRequested = true
		}
	}
	if !p.closed {
		p.closed = true
		_ = p.client.Close()
	}
	p.cancel()
	p.closeMu.Unlock()
	select {
	case <-p.waitCh:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// CloseGracefully closes stdin, then force-terminates after grace or ctx.
// An active turn reports FailureCancelled and cannot produce a checkpoint.
func (p *Process) CloseGracefully(ctx context.Context, grace time.Duration) error {
	return p.closeGracefully(ctx, grace, true)
}

func (p *Process) closeGracefully(ctx context.Context, grace time.Duration, requested bool) error {
	if p == nil {
		return nil
	}
	p.closeMu.Lock()
	if requested {
		select {
		case <-p.waitCh:
		default:
			p.stopRequested = true
		}
	}
	if !p.closed {
		p.closed = true
		_ = p.client.Close()
	}
	p.closeMu.Unlock()
	if grace <= 0 {
		grace = 2 * time.Second
	}
	timer := time.NewTimer(grace)
	defer timer.Stop()
	select {
	case <-p.waitCh:
		return nil
	case <-p.exitCh:
		// The process is already gone; its independent drain has a deadline.
		select {
		case <-p.waitCh:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	case <-ctx.Done():
		p.cancel()
		return ctx.Err()
	case <-timer.C:
		p.cancel()
	}
	select {
	case <-p.waitCh:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// cancellation excludes shutdown initiated solely to clean up a failed turn.
func (p *Process) cancellation(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	p.closeMu.Lock()
	defer p.closeMu.Unlock()
	if p.stopRequested {
		return context.Canceled
	}
	return nil
}
