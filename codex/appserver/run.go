package appserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/engine"
	"github.com/agent-dance/agent-adaptor/internal/processx"
)

// Options bundles the driver-provided inputs for a single run of the codex
// app-server. The caller (codex/driver.go) populates it from
// [driver.Request] and its package-owned codex.Config.
type Options struct {
	// Command is the codex binary; defaults to "codex".
	Command string
	// Args appended after "app-server --listen stdio://"; typically empty.
	ExtraArgs []string
	// CWD where the subprocess is launched. Empty means inherit.
	CWD string
	// Env is the environment set for the subprocess, using EnvBinding
	// semantics identical to codex exec runs.
	Env []driver.EnvBinding

	// ClientName and ClientVersion identify the caller in the initialize
	// handshake. Codex surfaces these in its diagnostic logs.
	ClientName    string
	ClientVersion string

	// Prompt is the user input for the single turn.
	Prompt string

	// Thread controls whether this run starts, resumes, or forks a thread.
	// ResumeThreadID and ForkThreadID are mutually exclusive. A fork returns a
	// checkpoint for the newly created child and never runs a turn on the parent.
	ResumeThreadID string
	ForkThreadID   string
	Ephemeral      bool

	// Sandbox / Approval / Model overrides.
	Sandbox  string // "read-only" | "workspace-write" | "danger-full-access"
	Approval string // passed to TurnStartParams.ApprovalPolicy
	Model    string
	Effort   string
	// ServiceTier is the official app-server service tier override (for
	// example, "fast").
	ServiceTier string
	// OutputSchema is forwarded to turn/start and validated before the final
	// public lifecycle event when native structured output is selected.
	OutputSchema *driver.OutputSchema

	// RunID identifies the run for StreamPayload attribution.
	RunID string
}

// Run spawns a codex app-server subprocess, completes the initialize /
// thread / turn handshake, forwards every relevant notification into the
// supplied sink as a StreamPayload, and returns the accumulated
// [driver.Response] once the turn completes.
//
// The returned [driver.Response] satisfies the Driver SPI output contract:
// codex/driver.go (Output, Transcript, ExitCode, Usage, Checkpoint, and
// RawStreams when available). Errors from the subprocess or JSON-RPC
// transport are propagated; if the turn itself fails, the error is surfaced
// inside [driver.Response.Failure] rather than as a returned error.
func Run(ctx context.Context, opts Options, sink driver.EventSink) (driver.Response, error) {
	if opts.ResumeThreadID != "" && opts.ForkThreadID != "" {
		return driver.Response{}, errors.New("codex app-server: resume and fork thread ids are mutually exclusive")
	}
	command := opts.Command
	if command == "" {
		command = "codex"
	}
	args := append([]string{"app-server", "--listen", "stdio://"}, opts.ExtraArgs...)

	processCtx, stopProcess := context.WithCancel(ctx)
	defer stopProcess()
	cmd := exec.CommandContext(processCtx, command, args...)
	processx.ConfigureCancellation(cmd)
	if opts.CWD != "" {
		cmd.Dir = opts.CWD
	}
	cmd.Env = toExecEnv(opts.Env)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return driver.Response{}, fmt.Errorf("codex app-server stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return driver.Response{}, fmt.Errorf("codex app-server stdout: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return driver.Response{}, fmt.Errorf("codex app-server stderr: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return driver.Response{}, fmt.Errorf("codex app-server start: %w", err)
	}

	// Emit a RunEvent so tooling can correlate spawns the same way the
	// exec --json path does via clihelper.
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

	// Drain stderr into the sink so operators can see codex log messages
	// alongside the stream. We don't fail the run on stderr errors.
	stdoutBuf := &syncBuffer{}
	stderrBuf := &syncBuffer{}
	stderrDone := make(chan struct{})
	go func() {
		defer close(stderrDone)
		_, _ = io.Copy(stderrBuf, stderr)
	}()

	// Tee stdout before the JSON-RPC decoder sees it. This preserves the exact
	// inbound bytes (including whitespace, frame formatting, unknown fields,
	// and trailing frames) instead of reconstructing JSON from decoded values.
	stream := newStdioStream(stdin, io.TeeReader(stdout, stdoutBuf))
	client := NewClient(context.Background(), stream)

	state := newRunState(opts.RunID, sink)
	// runState.onNotification forwards to the translator (so bridges see
	// every payload) and then accumulates run-level state needed to shape
	// the driver.Response.
	client.SetNotificationHandler(state.onNotification)

	// shutdown closes only the child's stdin first, allowing the JSON-RPC
	// reader to drain stdout to EOF. cmd.Wait runs only after that reader has
	// ended, as required by os/exec's StdoutPipe contract. A bounded fallback
	// cancels the configured process tree if a broken app-server ignores EOF.
	shutdown := func() error {
		_ = stdin.Close()
		select {
		case <-client.DisconnectNotify():
		case <-time.After(5 * time.Second):
			stopProcess()
			<-client.DisconnectNotify()
		}
		waitErr := cmd.Wait()
		<-stderrDone
		_ = client.Close()
		return waitErr
	}

	threadID := ""
	turnID := ""
	finish := func(primary error) (driver.Response, error) {
		waitErr := shutdown()
		if readErr := stream.ReadError(); readErr != nil {
			state.recordProtocolError(fmt.Errorf("decode JSON-RPC stdout: %w", readErr))
		}
		var cancellation error
		if errors.Is(primary, context.Canceled) || errors.Is(primary, context.DeadlineExceeded) {
			cancellation = ctx.Err()
		}
		exitCode, signal, timedOut, waitFatal := processOutcome(waitErr, cancellation)
		var finalErr error
		if primary != nil {
			finalErr = primary
		} else if protocolErr := state.protocolError(); protocolErr != nil {
			finalErr = protocolErr
		} else if !state.hasTerminal() {
			finalErr = fmt.Errorf("codex app-server protocol ended without turn/completed")
		} else if waitFatal != nil {
			finalErr = fmt.Errorf("codex app-server wait: %w", waitFatal)
		}
		result := state.snapshot(opts, threadID, stdoutBuf.String(), stderrBuf.String(), exitCode, signal, timedOut)
		result = finalizeAppServerStructuredOutput(opts.OutputSchema, result)
		// The official terminal is staged until the process outcome and native
		// structured-output validation are known. This makes the final public
		// lifecycle agree with the returned Response on every exit path.
		state.finishPublicResult(result, finalErr)
		return result, finalErr
	}

	clientInfo := ClientInfo{
		Name:    nonEmpty(opts.ClientName, "agent-adaptor"),
		Version: nonEmpty(opts.ClientVersion, "0.0.0"),
	}

	// 1. initialize / initialized handshake.
	if _, err := client.Initialize(ctx, InitializeParams{ClientInfo: clientInfo}); err != nil {
		return finish(fmt.Errorf("codex app-server initialize: %w", err))
	}
	if err := client.NotifyInitialized(ctx); err != nil {
		return finish(fmt.Errorf("codex app-server initialized: %w", err))
	}

	// 2. thread start or resume.
	if opts.ForkThreadID != "" {
		resp, err := client.ThreadFork(ctx, ThreadForkParams{
			ThreadID:       opts.ForkThreadID,
			CWD:            opts.CWD,
			Ephemeral:      opts.Ephemeral,
			Sandbox:        opts.Sandbox,
			Model:          opts.Model,
			ServiceTier:    opts.ServiceTier,
			ApprovalPolicy: opts.Approval,
		})
		if err != nil {
			return finish(classifyThreadError(err, opts.ForkThreadID))
		}
		threadID = resp.Thread.ID
	} else if opts.ResumeThreadID != "" {
		resp, err := client.ThreadResume(ctx, ThreadResumeParams{ThreadID: opts.ResumeThreadID})
		if err != nil {
			return finish(classifyThreadError(err, opts.ResumeThreadID))
		}
		threadID = resp.Thread.ID
	} else {
		resp, err := client.ThreadStart(ctx, ThreadStartParams{
			CWD:         opts.CWD,
			Ephemeral:   opts.Ephemeral,
			Sandbox:     opts.Sandbox,
			Model:       opts.Model,
			ServiceTier: opts.ServiceTier,
		})
		if err != nil {
			return finish(fmt.Errorf("codex app-server thread/start: %w", err))
		}
		threadID = resp.Thread.ID
	}
	state.setThread(threadID)
	if err := state.protocolError(); err != nil {
		return finish(err)
	}

	// 3. turn/start.
	var outputSchema json.RawMessage
	if opts.OutputSchema != nil {
		outputSchema = append(json.RawMessage(nil), opts.OutputSchema.SchemaJSON...)
	}
	turnParams := TurnStartParams{
		ThreadID:     threadID,
		Input:        []UserInput{TextInput(opts.Prompt)},
		CWD:          opts.CWD,
		Model:        opts.Model,
		Effort:       opts.Effort,
		ServiceTier:  opts.ServiceTier,
		OutputSchema: outputSchema,
	}
	if opts.Approval != "" {
		turnParams.ApprovalPolicy = opts.Approval
	}
	if opts.Sandbox != "" {
		turnParams.SandboxPolicy = sandboxPolicyFor(opts.Sandbox)
	}
	turn, err := client.TurnStart(ctx, turnParams)
	if err != nil {
		return finish(fmt.Errorf("codex app-server turn/start: %w", err))
	}
	turnID = turn.Turn.ID
	state.setTurn(turnID)
	if err := state.protocolError(); err != nil {
		return finish(err)
	}

	// 4. wait for the turn to complete or the context to expire.
	select {
	case <-state.done:
	case <-ctx.Done():
		// Best-effort interrupt uses a cancellation-detached, bounded context;
		// the process may already be terminating through CommandContext.
		interruptCtx, cancelInterrupt := context.WithTimeout(context.WithoutCancel(ctx), 500*time.Millisecond)
		_ = client.TurnInterrupt(interruptCtx, TurnInterruptParams{ThreadID: threadID, TurnID: turnID})
		cancelInterrupt()
		return finish(ctx.Err())
	case <-client.DisconnectNotify():
		// The state check after the reader/process join distinguishes a clean
		// terminal EOF from malformed JSON or a missing terminal notification.
	}

	return finish(nil)
}

// ---------------------------------------------------------------------------
// Internal state machine
// ---------------------------------------------------------------------------

type runState struct {
	translator *Translator
	sink       driver.EventSink
	notifyMu   sync.Mutex

	mu             sync.Mutex
	finalAgentText string
	transcript     []driver.TranscriptItem
	usage          *driver.Usage
	turnFailure    *driver.RunFailure
	terminal       *driver.TerminalPayload
	terminalStatus TurnStatus
	protocolErr    error
	threadID       string
	turnID         string
	pending        []pendingNotification
	publicClosed   bool
	publicStarted  bool
	itemsEmitted   int
	done           chan struct{}
	doneOnce       sync.Once
}

type pendingNotification struct {
	method string
	params json.RawMessage
}

func (s *runState) setThread(threadID string) {
	s.notifyMu.Lock()
	defer s.notifyMu.Unlock()
	if strings.TrimSpace(threadID) == "" {
		s.recordProtocolError(errors.New("RPC returned an empty thread identity"))
		return
	}
	s.mu.Lock()
	if s.threadID != "" && s.threadID != threadID {
		s.recordProtocolErrorLocked(fmt.Errorf("RPC thread identity changed from %q to %q", s.threadID, threadID))
		s.mu.Unlock()
		return
	}
	if s.threadID == "" {
		s.threadID = threadID
		s.appendTranscriptLocked(driver.TranscriptItem{
			Kind:      driver.TranscriptInit,
			SessionID: threadID,
		})
	}
	s.mu.Unlock()
	s.translator.SetThread(threadID)
	s.flushPendingNotificationsLocked()
}

func (s *runState) setTurn(turnID string) {
	s.notifyMu.Lock()
	defer s.notifyMu.Unlock()
	if strings.TrimSpace(turnID) == "" {
		s.recordProtocolError(errors.New("RPC returned an empty turn identity"))
		return
	}
	s.mu.Lock()
	if s.turnID != "" && s.turnID != turnID {
		s.recordProtocolErrorLocked(fmt.Errorf("RPC turn identity changed from %q to %q", s.turnID, turnID))
		s.mu.Unlock()
		return
	}
	s.turnID = turnID
	s.mu.Unlock()
	s.translator.SetTurn(turnID)
	s.flushPendingNotificationsLocked()
}

func newRunState(runID string, sink driver.EventSink) *runState {
	return &runState{
		translator: NewTranslator(sink, runID),
		sink:       sink,
		done:       make(chan struct{}),
	}
}

// onNotification forwards the raw notification into the translator and then
// extracts run-level state (final text, usage, failure, completion
// signal).
func (s *runState) onNotification(method string, params json.RawMessage) {
	s.notifyMu.Lock()
	defer s.notifyMu.Unlock()
	s.handleNotificationLocked(method, params)
}

func (s *runState) handleNotificationLocked(method string, params json.RawMessage) {
	// Once the official terminal has closed normalized semantics, trailing
	// frames remain audit-only Raw stdout. A duplicate official terminal is a
	// protocol violation; every other trailing frame is ignored here.
	s.mu.Lock()
	if s.publicClosed {
		if method == NotifyTurnCompleted {
			s.recordProtocolErrorLocked(errors.New("duplicate turn/completed notification"))
		}
		s.mu.Unlock()
		return
	}
	// Once an earlier wire notification is waiting for an RPC identity, every
	// later notification must wait behind it, including unknown diagnostic
	// frames. Otherwise an unscoped frame written after turn/completed can be
	// published before the queued terminal and escape the terminal boundary.
	if s.threadID == "" || s.turnID == "" || len(s.pending) != 0 {
		s.pending = append(s.pending, pendingNotification{
			method: method,
			params: append(json.RawMessage(nil), params...),
		})
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()

	deferred, err := s.bindNotificationScopeLocked(method, params)
	if deferred {
		return
	}
	if err != nil {
		s.recordProtocolError(err)
		s.signalDone()
		return
	}
	if err := validateAppServerNotification(method, params); err != nil {
		s.recordProtocolError(fmt.Errorf("decode %s: %w", method, err))
		return
	}
	// turn/completed is the unique public terminal boundary.
	if method == NotifyTurnCompleted {
		s.mu.Lock()
		s.publicClosed = true
		s.mu.Unlock()
	}

	if method == NotifyTurnCompleted {
		s.handleTurnCompleted(params)
		return
	}

	// Non-terminal normalized payloads retain wire order. Transcript items
	// parsed below are emitted in their own append order by
	// appendTranscriptLocked.
	if method == NotifyTurnStarted {
		// Let the official notification populate TurnID before run.started,
		// then release any init transcript accumulated during the handshake.
		s.translator.Dispatch(method, params)
		s.markPublicStarted()
	} else if method == NotifyThreadStarted {
		// thread/started only contributes the pending init transcript; the
		// public lifecycle starts at the first turn-scoped notification.
		s.translator.Dispatch(method, params)
	} else {
		s.startPublic()
		s.translator.Dispatch(method, params)
	}

	switch method {
	case NotifyItemAgentMessageDelta:
		var body AgentMessageDeltaNotification
		if err := json.Unmarshal(params, &body); err != nil {
			s.recordProtocolError(fmt.Errorf("decode item/agentMessage/delta: %w", err))
		}
	case NotifyItemStarted:
		s.absorbItem(params, true)
	case NotifyItemCompleted:
		s.absorbItem(params, false)
	case NotifyThreadTokenUsageUpdated:
		// Codex 0.120.0 reports the authoritative token tally via
		// thread/tokenUsage/updated notifications that arrive right before
		// turn/completed. We keep the latest snapshot so driver.Response
		// reflects it even when turn/completed omits the usage field.
		var body ThreadTokenUsageUpdatedNotification
		if err := json.Unmarshal(params, &body); err != nil {
			s.recordProtocolError(fmt.Errorf("decode thread/tokenUsage/updated: %w", err))
			return
		}
		s.mu.Lock()
		s.usage = &driver.Usage{
			InputTokens:       body.TokenUsage.Total.InputTokens,
			OutputTokens:      body.TokenUsage.Total.OutputTokens,
			CachedInputTokens: body.TokenUsage.Total.CachedInputTokens,
		}
		s.mu.Unlock()
	case NotifyError:
		var body ErrorNotification
		if err := json.Unmarshal(params, &body); err != nil {
			s.recordProtocolError(fmt.Errorf("decode error notification: %w", err))
			return
		}
		kind := driver.TranscriptFailure
		if body.WillRetry {
			kind = driver.TranscriptSystem
		}
		s.mu.Lock()
		s.appendTranscriptLocked(driver.TranscriptItem{
			Kind:    kind,
			Text:    body.Error.Message,
			Subtype: NotifyError,
			Data:    map[string]any{"will_retry": body.WillRetry},
		})
		s.mu.Unlock()
	}
}

func (s *runState) flushPendingNotificationsLocked() {
	if len(s.pending) == 0 {
		return
	}
	pending := s.pending
	s.pending = nil
	for _, notification := range pending {
		s.handleNotificationLocked(notification.method, notification.params)
	}
}

func (s *runState) bindNotificationScopeLocked(method string, params json.RawMessage) (bool, error) {
	threadID, turnID, scoped, turnScoped, err := appServerNotificationScope(method, params)
	if err != nil || !scoped {
		return false, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.threadID == "" || (turnScoped && s.turnID == "") {
		s.pending = append(s.pending, pendingNotification{
			method: method,
			params: append(json.RawMessage(nil), params...),
		})
		return true, nil
	}
	if threadID != s.threadID {
		return false, fmt.Errorf("codex app-server notification %s belongs to thread %q, want %q", method, threadID, s.threadID)
	}
	if turnScoped && turnID != s.turnID {
		return false, fmt.Errorf("codex app-server notification %s belongs to turn %q, want %q", method, turnID, s.turnID)
	}
	return false, nil
}

func appServerNotificationScope(method string, params json.RawMessage) (threadID, turnID string, scoped, turnScoped bool, err error) {
	var envelope struct {
		ThreadID string `json:"threadId"`
		TurnID   string `json:"turnId"`
		Thread   struct {
			ID string `json:"id"`
		} `json:"thread"`
		Turn struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	switch method {
	case NotifyThreadStarted, NotifyThreadStatusChanged:
		scoped = true
	case NotifyTurnStarted, NotifyTurnCompleted,
		NotifyItemStarted, NotifyItemCompleted,
		NotifyItemAgentMessageDelta, NotifyItemReasoningTextDelta,
		NotifyItemReasoningSummaryTextDelta, NotifyItemReasoningSummaryPartAdded,
		NotifyItemCommandExecutionOutputDelta,
		NotifyItemFileChangeOutputDelta, NotifyItemPlanDelta,
		NotifyThreadTokenUsageUpdated, NotifyError:
		scoped = true
		turnScoped = true
	default:
		return "", "", false, false, nil
	}
	if err := json.Unmarshal(params, &envelope); err != nil {
		return "", "", true, turnScoped, fmt.Errorf("decode %s scope: %w", method, err)
	}
	threadID = envelope.ThreadID
	if method == NotifyThreadStarted {
		threadID = envelope.Thread.ID
	}
	turnID = envelope.TurnID
	if method == NotifyTurnStarted || method == NotifyTurnCompleted {
		turnID = envelope.Turn.ID
	}
	if strings.TrimSpace(threadID) == "" {
		return "", "", true, turnScoped, fmt.Errorf("codex app-server notification %s has no thread id", method)
	}
	if turnScoped && strings.TrimSpace(turnID) == "" {
		return "", "", true, true, fmt.Errorf("codex app-server notification %s has no turn id", method)
	}
	return threadID, turnID, true, turnScoped, nil
}

func validateAppServerNotification(method string, params json.RawMessage) error {
	var target any
	switch method {
	case NotifyThreadStarted:
		target = &ThreadStartedNotificationBody{}
	case NotifyTurnStarted:
		target = &TurnStartedNotificationBody{}
	case NotifyTurnCompleted:
		target = &TurnCompletedNotificationBody{}
	case NotifyItemAgentMessageDelta:
		target = &AgentMessageDeltaNotification{}
	case NotifyItemReasoningTextDelta:
		target = &ReasoningTextDeltaNotification{}
	case NotifyItemReasoningSummaryTextDelta:
		target = &ReasoningSummaryTextDeltaNotification{}
	case NotifyItemReasoningSummaryPartAdded:
		target = &ReasoningSummaryPartAddedNotification{}
	case NotifyItemCommandExecutionOutputDelta:
		target = &CommandExecutionOutputDeltaNotification{}
	case NotifyCommandExecOutputDelta:
		target = &CommandExecOutputDeltaNotification{}
	case NotifyItemFileChangeOutputDelta:
		target = &FileChangeOutputDeltaNotification{}
	case NotifyItemPlanDelta:
		target = &PlanDeltaNotification{}
	case NotifyThreadTokenUsageUpdated:
		target = &ThreadTokenUsageUpdatedNotification{}
	case NotifyError:
		target = &ErrorNotification{}
	case NotifyItemStarted:
		var body ItemStartedNotificationBody
		if err := json.Unmarshal(params, &body); err != nil {
			return err
		}
		_, err := DecodeThreadItem(body.Item)
		return err
	case NotifyItemCompleted:
		var body ItemCompletedNotificationBody
		if err := json.Unmarshal(params, &body); err != nil {
			return err
		}
		_, err := DecodeThreadItem(body.Item)
		return err
	default:
		return nil
	}
	return json.Unmarshal(params, target)
}

func (s *runState) handleTurnCompleted(params json.RawMessage) {
	s.startPublic()
	var body TurnCompletedNotificationBody
	if err := json.Unmarshal(params, &body); err != nil {
		s.recordProtocolError(fmt.Errorf("decode turn/completed: %w", err))
		// A malformed official terminal closes semantic ingestion but the public
		// run.error is staged until shutdown confirms the final process outcome.
		s.signalDone()
		return
	}

	s.mu.Lock()
	s.terminal = &driver.TerminalPayload{
		Event: NotifyTurnCompleted,
		JSON:  append(json.RawMessage(nil), params...),
	}
	s.terminalStatus = body.Turn.Status
	if body.Turn.Usage != nil {
		s.usage = &driver.Usage{
			InputTokens:       body.Turn.Usage.InputTokens,
			OutputTokens:      body.Turn.Usage.OutputTokens,
			CachedInputTokens: body.Turn.Usage.CachedInputTokens,
		}
	}
	switch body.Turn.Status {
	case TurnStatusCompleted:
		if body.Turn.Error != nil {
			s.recordProtocolErrorLocked(errors.New("completed turn carried an error payload"))
		}
	case TurnStatusFailed, TurnStatusInterrupted:
		s.turnFailure = failureFromCompletedTurn(body.Turn)
	default:
		s.recordProtocolErrorLocked(fmt.Errorf("turn/completed reported invalid status %q", body.Turn.Status))
	}
	usage := cloneUsage(s.usage)
	terminalData := map[string]any{}
	_ = json.Unmarshal(params, &terminalData)
	item := driver.TranscriptItem{
		Kind:    driver.TranscriptResult,
		Subtype: string(body.Turn.Status),
		IsError: body.Turn.Status != TurnStatusCompleted,
		Usage:   usage,
		Data:    map[string]any{"payload": terminalData},
	}
	if s.turnFailure != nil {
		item.Text = s.turnFailure.Message
	}
	// The terminal TranscriptResult must reach the unified public Event flow
	// before run.finished/run.error, because the lifecycle terminal is strict
	// last across all provider-originated public events.
	s.appendTranscriptLocked(item)
	s.mu.Unlock()

	s.signalDone()
}

func (s *runState) finishPublicResult(result driver.Response, runErr error) {
	s.startPublic()
	if runErr != nil {
		s.translator.FinishError(runErr)
		return
	}
	if result.Failure != nil {
		s.translator.FinishFailure(result.Failure, result.RawStreams, result.Usage)
		return
	}
	if result.RawStreams == nil || result.RawStreams.Terminal == nil {
		s.translator.FinishError(errors.New("codex app-server protocol ended without a usable terminal"))
		return
	}
	s.translator.Dispatch(result.RawStreams.Terminal.Event, result.RawStreams.Terminal.JSON)
}

func (s *runState) signalDone() {
	s.doneOnce.Do(func() { close(s.done) })
}

func (s *runState) absorbItem(params json.RawMessage, started bool) {
	var rawItem json.RawMessage
	if started {
		var body ItemStartedNotificationBody
		if err := json.Unmarshal(params, &body); err != nil {
			s.recordProtocolError(fmt.Errorf("decode item/started: %w", err))
			return
		}
		rawItem = body.Item
	} else {
		var body ItemCompletedNotificationBody
		if err := json.Unmarshal(params, &body); err != nil {
			s.recordProtocolError(fmt.Errorf("decode item/completed: %w", err))
			return
		}
		rawItem = body.Item
	}
	item, err := DecodeThreadItem(rawItem)
	if err != nil {
		s.recordProtocolError(err)
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if started {
		if transcript := transcriptForStartedItem(item); transcript != nil {
			s.appendTranscriptLocked(*transcript)
		}
		return
	}
	if item.Kind == ThreadItemAgentMessage && item.AgentMessage != nil {
		s.finalAgentText = item.AgentMessage.Text
	}
	if transcript := transcriptForCompletedItem(item); transcript != nil {
		s.appendTranscriptLocked(*transcript)
	}
}

// appendTranscriptLocked is the sole transcript write path. Items parsed
// during the handshake are held until run.started is public; from that point
// the Response slice and RunEventItem emission advance together, making their
// order and values identical by construction. Callers must hold s.mu.
func (s *runState) appendTranscriptLocked(item driver.TranscriptItem) {
	s.transcript = append(s.transcript, item)
	s.emitPendingTranscriptLocked()
}

func (s *runState) startPublic() {
	s.translator.start()
	s.markPublicStarted()
}

func (s *runState) markPublicStarted() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.publicStarted {
		return
	}
	s.publicStarted = true
	s.emitPendingTranscriptLocked()
}

func (s *runState) emitPendingTranscriptLocked() {
	if !s.publicStarted || s.sink == nil {
		return
	}
	for s.itemsEmitted < len(s.transcript) {
		clone := s.transcript[s.itemsEmitted]
		_ = s.sink.Emit(driver.RunEvent{
			Type:      driver.RunEventItem,
			Timestamp: time.Now().UTC(),
			Item:      &clone,
		})
		s.itemsEmitted++
	}
}

func transcriptForStartedItem(item *ThreadItem) *driver.TranscriptItem {
	if item == nil {
		return nil
	}
	transcript := &driver.TranscriptItem{Kind: driver.TranscriptToolCall, ToolUseID: item.ID}
	switch item.Kind {
	case ThreadItemCommandExecution:
		transcript.ToolName = "shell"
		if item.CommandExecution != nil {
			transcript.Input = map[string]any{"command": item.CommandExecution.Command, "cwd": item.CommandExecution.CWD}
		}
	case ThreadItemFileChange:
		transcript.ToolName = "file_change"
	case ThreadItemMcpToolCall:
		if item.McpToolCall != nil {
			transcript.ToolName = item.McpToolCall.Server + "/" + item.McpToolCall.Tool
			transcript.Input = decodeJSONValue(item.McpToolCall.Arguments)
		}
	case ThreadItemWebSearch:
		transcript.ToolName = "web_search"
		if item.WebSearch != nil {
			transcript.Input = map[string]any{"query": item.WebSearch.Query}
		}
	case ThreadItemDynamicToolCall:
		if item.DynamicToolCall != nil {
			transcript.ToolName = item.DynamicToolCall.Tool
			transcript.Input = decodeJSONValue(item.DynamicToolCall.Arguments)
		}
	default:
		return nil
	}
	return transcript
}

func transcriptForCompletedItem(item *ThreadItem) *driver.TranscriptItem {
	if item == nil {
		return nil
	}
	switch item.Kind {
	case ThreadItemAgentMessage:
		if item.AgentMessage != nil {
			return &driver.TranscriptItem{Kind: driver.TranscriptAssistant, Text: item.AgentMessage.Text}
		}
	case ThreadItemReasoning:
		if item.Reasoning != nil {
			text := strings.Join(item.Reasoning.Content, "\n")
			if text == "" {
				text = strings.Join(item.Reasoning.Summary, "\n")
			}
			return &driver.TranscriptItem{Kind: driver.TranscriptThinking, Text: text}
		}
	case ThreadItemCommandExecution:
		if item.CommandExecution != nil {
			return &driver.TranscriptItem{
				Kind:      driver.TranscriptToolResult,
				ToolUseID: item.ID,
				Text:      item.CommandExecution.AggregatedOutput,
				IsError:   item.CommandExecution.Status == "failed",
			}
		}
	case ThreadItemFileChange:
		if item.FileChange != nil {
			return &driver.TranscriptItem{Kind: driver.TranscriptToolResult, ToolUseID: item.ID, IsError: item.FileChange.Status == "failed"}
		}
	case ThreadItemMcpToolCall:
		if item.McpToolCall != nil {
			return &driver.TranscriptItem{
				Kind:      driver.TranscriptToolResult,
				ToolUseID: item.ID,
				IsError:   len(item.McpToolCall.Error) > 0 || item.McpToolCall.Status == "failed",
				Data:      map[string]any{"result": decodeJSONValue(item.McpToolCall.Result), "error": decodeJSONValue(item.McpToolCall.Error)},
			}
		}
	case ThreadItemWebSearch, ThreadItemDynamicToolCall:
		return &driver.TranscriptItem{Kind: driver.TranscriptToolResult, ToolUseID: item.ID}
	}
	return nil
}

func decodeJSONValue(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return append(json.RawMessage(nil), raw...)
	}
	return value
}

func (s *runState) recordProtocolError(err error) {
	s.mu.Lock()
	s.recordProtocolErrorLocked(err)
	s.mu.Unlock()
	if err != nil {
		// A protocol-fatal decoder error may be followed by an otherwise valid
		// terminal while the long-lived app-server stays alive. Wake Run now;
		// public lifecycle emission remains staged until shutdown completes.
		s.signalDone()
	}
}

func (s *runState) recordProtocolErrorLocked(err error) {
	if err != nil && s.protocolErr == nil {
		s.protocolErr = fmt.Errorf("codex app-server protocol: %w", err)
		// Protocol corruption is a semantic terminal. Later frames are retained
		// only in Raw stdout and must not repair or contaminate normalized state.
		s.publicClosed = true
	}
}

func (s *runState) protocolError() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.protocolErr
}

func (s *runState) hasTerminal() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.terminal != nil
}

func (s *runState) snapshot(opts Options, threadID, stdout, stderr string, exitCode int, signal string, timedOut bool) driver.Response {
	s.mu.Lock()
	defer s.mu.Unlock()

	failure := cloneRunFailure(s.turnFailure)
	if exitCode != 0 && failure == nil && !timedOut {
		failure = &driver.RunFailure{
			Message: fmt.Sprintf("codex app-server exited with status %d", exitCode),
			Code:    driver.FailureAgentError,
		}
	}
	var checkpoint *driver.Checkpoint
	if threadID != "" && exitCode == 0 && signal == "" && !timedOut && s.protocolErr == nil && s.terminalStatus == TurnStatusCompleted && failure == nil {
		checkpoint = &driver.Checkpoint{
			State: &driver.SessionState{ResumeID: threadID, DisplayID: threadID},
			Valid: true,
		}
	}
	return driver.Response{
		Output:     s.assembleOutputLocked(),
		RawStreams: &driver.RawStreams{Stdout: stdout, Stderr: stderr, Terminal: cloneTerminal(s.terminal)},
		Transcript: append([]driver.TranscriptItem(nil), s.transcript...),
		ExitCode:   exitCode,
		Signal:     signal,
		TimedOut:   timedOut,
		Usage:      cloneUsage(s.usage),
		Checkpoint: checkpoint,
		Provider:   "openai",
		Model:      opts.Model,
		Failure:    failure,
	}
}

func finalizeAppServerStructuredOutput(schema *driver.OutputSchema, result driver.Response) driver.Response {
	if schema == nil || schema.Mode == driver.StructuredOutputPromptValidate {
		return result
	}
	var candidate *driver.StructuredOutput
	if strings.TrimSpace(result.Output) != "" && result.Failure == nil {
		candidate = &driver.StructuredOutput{
			Format:  driver.OutputFormatJSONSchema,
			Source:  driver.StructuredOutputSourceNative,
			RawJSON: append([]byte(nil), result.Output...),
		}
	}
	result.StructuredOutput, result.Failure = engine.FinalizeStructuredOutput(
		schema,
		driver.StructuredOutputSourceNative,
		result.Output,
		candidate,
		result.Failure,
	)
	if result.Failure != nil {
		result.Checkpoint = nil
	}
	return result
}

func (s *runState) assembleOutputLocked() string {
	return s.finalAgentText
}

func cloneUsage(usage *driver.Usage) *driver.Usage {
	if usage == nil {
		return nil
	}
	clone := *usage
	return &clone
}

func cloneTerminal(terminal *driver.TerminalPayload) *driver.TerminalPayload {
	if terminal == nil {
		return nil
	}
	clone := *terminal
	clone.JSON = append(json.RawMessage(nil), terminal.JSON...)
	return &clone
}

func cloneRunFailure(failure *driver.RunFailure) *driver.RunFailure {
	if failure == nil {
		return nil
	}
	clone := *failure
	if failure.Metadata != nil {
		clone.Metadata = make(map[string]any, len(failure.Metadata))
		for key, value := range failure.Metadata {
			clone.Metadata[key] = value
		}
	}
	return &clone
}

func processOutcome(waitErr, cancellation error) (exitCode int, signal string, timedOut bool, fatal error) {
	if cancellation != nil {
		timedOut = errors.Is(cancellation, context.DeadlineExceeded)
		signal = cancellation.Error()
	}
	if waitErr == nil {
		return 0, signal, timedOut, nil
	}
	var exitErr *exec.ExitError
	if errors.As(waitErr, &exitErr) {
		exitCode = exitErr.ExitCode()
		if signal == "" && exitErr.ProcessState != nil && !exitErr.ProcessState.Exited() {
			signal = exitErr.ProcessState.String()
		}
		return exitCode, signal, timedOut, nil
	}
	if cancellation != nil {
		return -1, signal, timedOut, nil
	}
	return 0, "", false, waitErr
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func toExecEnv(bindings []driver.EnvBinding) []string {
	env := os.Environ()
	for _, b := range bindings {
		env = append(env, b.Name+"="+b.Value)
	}
	return env
}

func nonEmpty(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}

func classifyThreadError(err error, threadID string) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	lower := strings.ToLower(msg)
	if strings.Contains(lower, "not found") || strings.Contains(lower, "unknown thread") || strings.Contains(lower, "no rollout found") || strings.Contains(lower, "missing rollout") {
		return &engine.ResumeRejectedError{
			Reason: fmt.Sprintf("codex thread %q is unavailable: %s", threadID, msg),
		}
	}
	return err
}

func sandboxPolicyFor(kind string) *SandboxPolicy {
	switch kind {
	case "read-only":
		return &SandboxPolicy{Type: SandboxPolicyKindReadOnly}
	case "workspace-write":
		return &SandboxPolicy{Type: SandboxPolicyKindWorkspaceWrite}
	case "danger-full-access":
		return &SandboxPolicy{Type: SandboxPolicyKindDangerFull}
	default:
		return nil
	}
}

// syncBuffer is a zero-dependency concurrent-safe bytes.Buffer equivalent
// used for stderr capture. We avoid pulling in bytes.Buffer + sync.Mutex
// boilerplate at callsites.
type syncBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func (s *syncBuffer) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Len()
}

func (s *syncBuffer) Since(offset int) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	value := s.b.String()
	if offset < 0 || offset > len(value) {
		offset = 0
	}
	return value[offset:]
}
