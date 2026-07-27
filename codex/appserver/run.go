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

	// Thread controls whether this run starts a new thread or resumes one.
	ResumeThreadID string
	Ephemeral      bool

	// Sandbox / Approval / Model overrides.
	Sandbox  string // "read-only" | "workspace-write" | "danger-full-access"
	Approval string // passed to TurnStartParams.ApprovalPolicy
	Model    string

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
		result := state.snapshot(opts, threadID, stdoutBuf.String(), stderrBuf.String(), exitCode, signal, timedOut)
		if primary != nil {
			return result, primary
		}
		if protocolErr := state.protocolError(); protocolErr != nil {
			return result, protocolErr
		}
		if !state.hasTerminal() {
			return result, fmt.Errorf("codex app-server protocol ended without turn/completed")
		}
		if waitFatal != nil {
			return result, fmt.Errorf("codex app-server wait: %w", waitFatal)
		}
		return result, nil
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
	if opts.ResumeThreadID != "" {
		resp, err := client.ThreadResume(ctx, ThreadResumeParams{ThreadID: opts.ResumeThreadID})
		if err != nil {
			return finish(classifyThreadError(err, opts.ResumeThreadID))
		}
		threadID = resp.Thread.ID
	} else {
		resp, err := client.ThreadStart(ctx, ThreadStartParams{
			CWD:       opts.CWD,
			Ephemeral: opts.Ephemeral,
			Sandbox:   opts.Sandbox,
			Model:     opts.Model,
		})
		if err != nil {
			return finish(fmt.Errorf("codex app-server thread/start: %w", err))
		}
		threadID = resp.Thread.ID
	}
	state.translator.SetThread(threadID)
	state.setThread(threadID)

	// 3. turn/start.
	turnParams := TurnStartParams{
		ThreadID: threadID,
		Input:    []UserInput{TextInput(opts.Prompt)},
		CWD:      opts.CWD,
		Model:    opts.Model,
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

	mu             sync.Mutex
	textByItemID   map[string]*strings.Builder
	itemOrder      []string
	transcript     []driver.TranscriptItem
	usage          *driver.Usage
	turnFailure    *driver.RunFailure
	terminal       *driver.TerminalPayload
	terminalStatus TurnStatus
	protocolErr    error
	threadID       string
	done           chan struct{}
	doneOnce       sync.Once
}

func (s *runState) setThread(threadID string) {
	if strings.TrimSpace(threadID) == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.threadID == threadID {
		return
	}
	s.threadID = threadID
	s.transcript = append(s.transcript, driver.TranscriptItem{
		Kind:      driver.TranscriptInit,
		SessionID: threadID,
	})
}

func newRunState(runID string, sink driver.EventSink) *runState {
	return &runState{
		translator:   NewTranslator(sink, runID),
		textByItemID: map[string]*strings.Builder{},
		done:         make(chan struct{}),
	}
}

// onNotification forwards the raw notification into the translator and then
// extracts run-level state (final text, usage, failure, completion
// signal).
func (s *runState) onNotification(method string, params json.RawMessage) {
	// Always forward to translator first so bridges see the event before
	// we signal completion (which may end the consumer loop).
	s.translator.Dispatch(method, params)

	switch method {
	case NotifyThreadStarted:
		var body ThreadStartedNotificationBody
		if err := json.Unmarshal(params, &body); err != nil {
			s.recordProtocolError(fmt.Errorf("decode thread/started: %w", err))
			return
		}
		s.setThread(body.Thread.ID)
	case NotifyItemAgentMessageDelta:
		var body AgentMessageDeltaNotification
		if err := json.Unmarshal(params, &body); err != nil {
			s.recordProtocolError(fmt.Errorf("decode item/agentMessage/delta: %w", err))
			return
		}
		s.mu.Lock()
		b := s.textBuilderLocked(body.ItemID)
		b.WriteString(body.Delta)
		s.mu.Unlock()
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
	case NotifyTurnCompleted:
		var body TurnCompletedNotificationBody
		if err := json.Unmarshal(params, &body); err != nil {
			s.recordProtocolError(fmt.Errorf("decode turn/completed: %w", err))
			s.signalDone()
			return
		}
		s.mu.Lock()
		if s.terminal != nil {
			s.recordProtocolErrorLocked(errors.New("duplicate turn/completed notification"))
			s.mu.Unlock()
			s.signalDone()
			return
		}
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
		s.transcript = append(s.transcript, item)
		s.mu.Unlock()
		s.signalDone()
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
		s.transcript = append(s.transcript, driver.TranscriptItem{
			Kind:    kind,
			Text:    body.Error.Message,
			Subtype: NotifyError,
			Data:    map[string]any{"will_retry": body.WillRetry},
		})
		s.mu.Unlock()
	}
}

func (s *runState) signalDone() {
	s.doneOnce.Do(func() { close(s.done) })
}

func (s *runState) textBuilderLocked(itemID string) *strings.Builder {
	b, ok := s.textByItemID[itemID]
	if !ok {
		b = &strings.Builder{}
		s.textByItemID[itemID] = b
		s.itemOrder = append(s.itemOrder, itemID)
	}
	return b
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
			s.transcript = append(s.transcript, *transcript)
		}
		return
	}
	if item.Kind == ThreadItemAgentMessage && item.AgentMessage != nil {
		b := s.textBuilderLocked(item.ID)
		b.Reset()
		b.WriteString(item.AgentMessage.Text)
	}
	if transcript := transcriptForCompletedItem(item); transcript != nil {
		s.transcript = append(s.transcript, *transcript)
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
	defer s.mu.Unlock()
	s.recordProtocolErrorLocked(err)
}

func (s *runState) recordProtocolErrorLocked(err error) {
	if err != nil && s.protocolErr == nil {
		s.protocolErr = fmt.Errorf("codex app-server protocol: %w", err)
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

func (s *runState) assembleOutputLocked() string {
	if len(s.itemOrder) == 0 {
		return ""
	}
	var b strings.Builder
	for _, id := range s.itemOrder {
		if sb, ok := s.textByItemID[id]; ok {
			if b.Len() > 0 {
				b.WriteString("\n")
			}
			b.WriteString(sb.String())
		}
	}
	return b.String()
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
