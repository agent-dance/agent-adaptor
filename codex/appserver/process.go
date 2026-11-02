package appserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"time"

	"github.com/agent-dance/agent-adaptor/v2/driver"
	"github.com/agent-dance/agent-adaptor/v2/internal/processx"
)

// Process owns one initialized app-server connection and one loaded Codex
// thread. Callers must serialize RunTurn calls; Process also enforces that
// rule so an accidental second writer cannot interleave JSON-RPC turns.
type Process struct {
	client         *Client
	stream         *stdioStream
	cmd            *exec.Cmd
	cancel         context.CancelFunc
	threadID       string
	notificationMu sync.Mutex
	state          *runState
	usageTotal     *driver.Usage // latest cumulative thread usage; guarded by notificationMu
	pendingUsage   []json.RawMessage
	freshThread    bool
	stdout         *syncBuffer
	stderr         *syncBuffer

	turnMu  sync.Mutex
	closeMu sync.Mutex
	closed  bool
	waitCh  chan struct{}
	waitErr error
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

	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("codex app-server stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("codex app-server stdout: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("codex app-server stderr: %w", err)
	}
	if err := cmd.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("codex app-server start: %w", err)
	}
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
	go func() {
		defer close(stderrDone)
		_, _ = io.Copy(stderrBuf, stderr)
	}()
	stream := newStdioStream(stdin, io.TeeReader(stdout, stdoutBuf))
	client := NewClient(procCtx, stream)
	p := &Process{
		client: client, stream: stream, cmd: cmd, cancel: cancel,
		stdout: stdoutBuf, stderr: stderrBuf, waitCh: make(chan struct{}),
		freshThread: opts.ResumeThreadID == "" && opts.ForkThreadID == "",
	}
	if p.freshThread {
		p.usageTotal = &driver.Usage{}
	}
	// A resume can replay usage before its RPC response. Keep the dispatcher
	// installed throughout the handshake and bind buffered usage to the
	// returned thread identity before exposing the process.
	client.SetNotificationHandler(p.onNotification)
	go func() {
		p.waitErr = cmd.Wait()
		<-stderrDone
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
	p.bindThread(threadID)
	return p, nil
}

// ThreadID is the provider thread loaded by this process.
func (p *Process) ThreadID() string {
	if p == nil {
		return ""
	}
	return p.threadID
}

// Done closes after the subprocess has exited and stderr has been drained.
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
	case <-p.waitCh:
		return true
	case <-p.client.DisconnectNotify():
		return true
	default:
		return false
	}
}

// RunTurn sends a prompt and any in-run continuations on the loaded thread. promptSent becomes
// true immediately before turn/start because an RPC error cannot prove the
// peer did not receive the request; callers must not replay in that case.
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
	state.ctx = ctx
	state.messages = opts.Messages
	state.setThread(p.threadID)
	p.notificationMu.Lock()
	state.freshThread = p.freshThread
	state.usageBaseline = cloneUsage(p.usageTotal)
	state.historicalUsage = cloneUsage(p.usageTotal)
	p.state = state
	p.notificationMu.Unlock()
	stopRequests := p.client.handleRunRequests(ctx, opts.RunID, sink, state)
	defer stopRequests()
	defer func() {
		p.notificationMu.Lock()
		p.state = nil
		p.notificationMu.Unlock()
	}()

	promptSent = true
	err = runTurns(ctx, p.client, state, opts, p.threadID)
	stopRequests()
	if err == nil {
		err = state.executionError()
	}
	if cancelErr := ctx.Err(); cancelErr != nil && !state.failedBeforeCancel() {
		err = errors.Join(cancelErr, err)
	} else if state.failedBeforeCancel() && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) {
		err = nil
	}
	if err == nil {
		if protocolErr := state.protocolError(); protocolErr != nil {
			err = protocolErr
		} else if !state.hasTerminal() {
			err = errors.New("codex app-server protocol ended without turn/completed")
		}
	}

	exitCode, signal, timedOut := 0, "", false
	if err != nil {
		exitCode = -1
		signal = err.Error()
		timedOut = errors.Is(err, context.DeadlineExceeded)
	}
	// Seal attribution and snapshot under the same dispatcher lock. Later
	// frames are idle history for the next execution, never mutations of a
	// response whose final accounting has already been copied.
	p.notificationMu.Lock()
	p.state = nil
	result = state.snapshot(opts, p.threadID, p.stdout.Since(stdoutStart), p.stderr.Since(stderrStart), exitCode, signal, timedOut)
	baseline := cloneUsage(state.usageBaseline)
	for _, id := range state.usageOrder {
		usage := state.usageTurns[id]
		if total := usage.total; total != nil {
			// A cumulative counter also closes any earlier unobserved gap.
			baseline = cloneUsage(total)
		} else if baseline != nil && usage.fixed != nil {
			baseline = addUsage(baseline, usage.fixed)
		} else {
			// Result.Usage can contain only a partial observed sum. A turn
			// with no reported usage invalidates the cumulative baseline.
			baseline = nil
		}
	}
	// Include counters queued before turn/start returned and later attributed
	// by runState, even if the dispatcher could not recognize them earlier.
	p.usageTotal = baseline
	p.freshThread = false
	p.notificationMu.Unlock()
	result = finalizeAppServerResult(opts.OutputSchema, result, err)
	state.finishPublicResult(result, err)
	return result, promptSent, err
}

// TerminateAndWait kills the independently grouped process and reaps it.
func (p *Process) TerminateAndWait(ctx context.Context) error {
	if p == nil {
		return nil
	}
	p.closeMu.Lock()
	if !p.closed {
		p.closed = true
		_ = p.client.Close()
		p.cancel()
	}
	p.closeMu.Unlock()
	select {
	case <-p.waitCh:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// CloseGracefully closes stdin, then force-terminates after grace or ctx.
func (p *Process) CloseGracefully(ctx context.Context, grace time.Duration) error {
	if p == nil {
		return nil
	}
	p.closeMu.Lock()
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

// onNotification stays installed while idle so a late usage snapshot advances
// the next execution's history baseline without publishing into a finished run.
func (p *Process) onNotification(method string, params json.RawMessage) {
	p.notificationMu.Lock()
	defer p.notificationMu.Unlock()
	if p.state != nil {
		p.state.onNotification(method, params)
	}
	if method == NotifyThreadTokenUsageUpdated {
		if p.threadID == "" {
			p.pendingUsage = append(p.pendingUsage, append(json.RawMessage(nil), params...))
			return
		}
		p.absorbUsageTotalLocked(params)
	}
}

func (p *Process) bindThread(threadID string) {
	p.notificationMu.Lock()
	defer p.notificationMu.Unlock()
	p.threadID = threadID
	for _, params := range p.pendingUsage {
		p.absorbUsageTotalLocked(params)
	}
	p.pendingUsage = nil
}

func (p *Process) absorbUsageTotalLocked(params json.RawMessage) {
	var body ThreadTokenUsageUpdatedNotification
	if json.Unmarshal(params, &body) != nil || body.ThreadID != p.threadID || body.TurnID == "" {
		return
	}
	if p.freshThread {
		if p.state == nil {
			return
		}
		p.state.mu.Lock()
		knownTurn := p.state.usageTurns[body.TurnID] != nil
		p.state.mu.Unlock()
		if !knownTurn {
			return
		}
	}
	total := &driver.Usage{InputTokens: body.TokenUsage.Total.InputTokens, OutputTokens: body.TokenUsage.Total.OutputTokens, CachedInputTokens: body.TokenUsage.Total.CachedInputTokens}
	last := &driver.Usage{InputTokens: body.TokenUsage.Last.InputTokens, OutputTokens: body.TokenUsage.Last.OutputTokens, CachedInputTokens: body.TokenUsage.Last.CachedInputTokens}
	if nonnegativeUsage(total) && nonnegativeUsage(last) && nonnegativeUsage(subtractUsage(total, last)) &&
		(p.usageTotal == nil || nonnegativeUsage(subtractUsage(total, p.usageTotal))) {
		p.usageTotal = total
	}
}
