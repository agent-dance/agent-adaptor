package appserver

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	agentadaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/internal/processx"
)

// Options bundles the driver-provided inputs for a single run of the codex
// app-server. The caller (codex/driver.go) populates it from
// DriverRunRequest and its own CodexConfig.
type Options struct {
	// Command is the codex binary; defaults to "codex".
	Command string
	// Args appended after "app-server --listen stdio://"; typically empty.
	ExtraArgs []string
	// CWD where the subprocess is launched. Empty means inherit.
	CWD string
	// Env is the environment set for the subprocess, using EnvBinding
	// semantics identical to codex exec runs.
	Env []agentadaptor.EnvBinding

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
// DriverRunResult once the turn completes.
//
// The returned DriverRunResult mirrors the minimum contract expected by
// codex/driver.go (Output, Transcript, ExitCode, Usage, Checkpoint, and
// RawStreams when available). Errors from the subprocess or JSON-RPC
// transport are propagated; if the turn itself fails, the error is surfaced
// inside DriverRunResult.Failure rather than as a returned error.
func Run(ctx context.Context, opts Options, sink agentadaptor.EventSink) (agentadaptor.DriverRunResult, error) {
	command := opts.Command
	if command == "" {
		command = "codex"
	}
	args := append([]string{"app-server", "--listen", "stdio://"}, opts.ExtraArgs...)

	cmd := exec.CommandContext(ctx, command, args...)
	processx.ConfigureCancellation(cmd)
	if opts.CWD != "" {
		cmd.Dir = opts.CWD
	}
	cmd.Env = toExecEnv(opts.Env)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return agentadaptor.DriverRunResult{}, fmt.Errorf("codex app-server stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return agentadaptor.DriverRunResult{}, fmt.Errorf("codex app-server stdout: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return agentadaptor.DriverRunResult{}, fmt.Errorf("codex app-server stderr: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return agentadaptor.DriverRunResult{}, fmt.Errorf("codex app-server start: %w", err)
	}

	// Emit a RunEvent so tooling can correlate spawns the same way the
	// exec --json path does via clihelper.
	_ = sink.Emit(agentadaptor.RunEvent{
		Type:      agentadaptor.RunEventSpawn,
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
	stderrBuf := &syncBuffer{}
	stderrDone := make(chan struct{})
	go func() {
		defer close(stderrDone)
		_, _ = io.Copy(stderrBuf, stderr)
	}()

	stream := newStdioStream(stdin, stdout)
	client := NewClient(ctx, stream)
	defer func() {
		// Closing the client closes the underlying stream which closes
		// stdin; codex then shuts down and cmd.Wait() returns.
		_ = client.Close()
		_ = cmd.Wait()
		<-stderrDone
	}()

	state := newRunState(opts.RunID, sink)
	// runState.onNotification forwards to the translator (so bridges see
	// every payload) and then accumulates run-level state needed to shape
	// the DriverRunResult.
	client.SetNotificationHandler(state.onNotification)

	// Wire follow-child: when the parent translator discovers a stable child
	// thread id from a collabAgentToolCall item, it calls back into runState
	// which subscribes to the child thread on the same connection and routes
	// its events through a child Translator with Subagent set (§8.3.5).
	state.initFollowChild(ctx, client)

	clientInfo := ClientInfo{
		Name:    nonEmpty(opts.ClientName, "agent-adaptor"),
		Version: nonEmpty(opts.ClientVersion, "0.0.0"),
	}

	// 1. initialize / initialized handshake.
	if _, err := client.Initialize(ctx, InitializeParams{ClientInfo: clientInfo}); err != nil {
		return agentadaptor.DriverRunResult{}, fmt.Errorf("codex app-server initialize: %w", err)
	}
	if err := client.NotifyInitialized(ctx); err != nil {
		return agentadaptor.DriverRunResult{}, fmt.Errorf("codex app-server initialized: %w", err)
	}

	// 2. thread start or resume.
	var threadID string
	if opts.ResumeThreadID != "" {
		resp, err := client.ThreadResume(ctx, ThreadResumeParams{ThreadID: opts.ResumeThreadID})
		if err != nil {
			return agentadaptor.DriverRunResult{}, classifyThreadError(err, opts.ResumeThreadID)
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
			return agentadaptor.DriverRunResult{}, fmt.Errorf("codex app-server thread/start: %w", err)
		}
		threadID = resp.Thread.ID
	}
	state.translator.SetThread(threadID)

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
	if _, err := client.TurnStart(ctx, turnParams); err != nil {
		return agentadaptor.DriverRunResult{}, fmt.Errorf("codex app-server turn/start: %w", err)
	}

	// 4. wait for the turn to complete or the context to expire.
	select {
	case <-state.done:
	case <-ctx.Done():
		// Best-effort interrupt; we ignore the error because the subprocess
		// may be tearing down.
		_ = client.TurnInterrupt(context.Background(), TurnInterruptParams{ThreadID: threadID})
		return agentadaptor.DriverRunResult{}, ctx.Err()
	}

	// 5. assemble result.
	text := state.assembleOutput()
	checkpoint := &agentadaptor.DriverCheckpoint{
		State: &agentadaptor.DriverSessionState{
			ResumeID:  threadID,
			DisplayID: threadID,
		},
		Valid: state.turnFailure == nil,
	}
	result := agentadaptor.DriverRunResult{
		Output:     text,
		ExitCode:   0,
		Usage:      state.usage,
		Checkpoint: checkpoint,
		Provider:   "openai",
		Model:      opts.Model,
		Summary:    text,
		Failure:    state.turnFailure,
		RawStreams: &agentadaptor.RawStreams{
			Stderr: stderrBuf.String(),
		},
	}
	if state.turnFailure != nil {
		result.ExitCode = 1
	}
	return result, nil
}

// ---------------------------------------------------------------------------
// Internal state machine
// ---------------------------------------------------------------------------

type runState struct {
	translator *Translator

	// childTranslators maps child thread ids to dedicated Translator instances
	// when follow-child (§8.3.5) is active. Protected by childMu.
	childMu          sync.RWMutex
	childTranslators map[string]*Translator

	mu           sync.Mutex
	textByItemID map[string]*strings.Builder
	itemOrder    []string
	usage        *agentadaptor.Usage
	turnFailure  *agentadaptor.RunFailure
	done         chan struct{}
	doneOnce     sync.Once
}

func newRunState(runID string, sink agentadaptor.EventSink) *runState {
	return &runState{
		translator:       NewTranslator(sink, runID),
		textByItemID:     map[string]*strings.Builder{},
		childTranslators: map[string]*Translator{},
		done:             make(chan struct{}),
	}
}

// initFollowChild sets the onChildThread callback on the parent translator
// so that collabAgentToolCall spawn events trigger child thread subscription.
// Must be called after the client is created and before notifications arrive.
func (s *runState) initFollowChild(ctx context.Context, client *Client) {
	s.translator.mu.Lock()
	s.translator.onChildThread = func(childThreadID, parentItemID string) {
		s.subscribeChild(ctx, client, childThreadID, parentItemID)
	}
	s.translator.mu.Unlock()
}

// subscribeChild registers a child translator and asynchronously calls
// thread/resume on the same app-server connection to begin receiving child
// thread events. It is called from within the JRPC notification handler, so
// ThreadResume MUST be called in a goroutine to avoid deadlocking the
// sequential JRPC dispatcher.
//
// Architecture note (§8.3.5): the same single app-server connection supports
// multiple concurrent thread subscriptions via thread/resume. Once subscribed,
// the child thread emits the same turn/* + item/* notifications as the parent,
// distinguished by the threadId field in each notification body.
func (s *runState) subscribeChild(ctx context.Context, client *Client, childThreadID, parentItemID string) {
	ref := &agentadaptor.SubagentRef{
		ID:         childThreadID,
		Kind:       "native",
		ToolCallID: parentItemID,
	}
	childTr := s.translator.newChildTranslator(ref)
	childTr.SetThread(childThreadID)

	s.childMu.Lock()
	s.childTranslators[childThreadID] = childTr
	s.childMu.Unlock()

	// ThreadResume is called in a goroutine to avoid deadlocking the JRPC
	// dispatcher: sourcegraph/jsonrpc2 dispatches notifications synchronously
	// and a Call from inside the handler would wait for the response, which
	// can only arrive after the handler returns.
	go func() {
		if _, err := client.ThreadResume(ctx, ThreadResumeParams{ThreadID: childThreadID}); err != nil {
			// Subscription failed: remove the child translator so its slot is
			// cleaned up. The parent's tool_call.* boundary still marks the
			// delegation; subagent.end will be emitted by handleCollabItemCompleted.
			s.childMu.Lock()
			delete(s.childTranslators, childThreadID)
			s.childMu.Unlock()
		}
	}()
}

// extractThreadIDFromNotification reads the threadId field that every
// app-server notification body includes. Returns "" on any error.
func extractThreadIDFromNotification(params json.RawMessage) string {
	if len(params) == 0 {
		return ""
	}
	var head struct {
		ThreadID string `json:"threadId"`
	}
	_ = json.Unmarshal(params, &head)
	return head.ThreadID
}

// onNotification forwards the raw notification into the translator and then
// extracts run-level state (final text, usage, failure, completion
// signal).
//
// When follow-child is active, notifications whose threadId matches a child
// thread are routed to the corresponding child Translator instead of the
// parent. Child turn/completed and turn/failed do NOT signal state.done;
// only the parent turn completion does.
func (s *runState) onNotification(method string, params json.RawMessage) {
	// Route child-thread events to the dedicated child Translator.
	// extractThreadIDFromNotification is cheap (single json.Unmarshal into
	// a one-field struct) and runs for every notification.
	if threadID := extractThreadIDFromNotification(params); threadID != "" {
		s.childMu.RLock()
		childTr, isChild := s.childTranslators[threadID]
		s.childMu.RUnlock()
		if isChild {
			// Dispatch to child translator. Child turn/completed emits
			// StreamSubagentEnd via handleTurnCompleted (subagentRef != nil).
			// Parent state.done is NOT signalled for child completions.
			childTr.Dispatch(method, params)
			return
		}
	}

	// Always forward to translator first so bridges see the event before
	// we signal completion (which may end the consumer loop).
	s.translator.Dispatch(method, params)

	switch method {
	case NotifyItemAgentMessageDelta:
		var body AgentMessageDeltaNotification
		if err := json.Unmarshal(params, &body); err == nil {
			s.mu.Lock()
			b, ok := s.textByItemID[body.ItemID]
			if !ok {
				b = &strings.Builder{}
				s.textByItemID[body.ItemID] = b
				s.itemOrder = append(s.itemOrder, body.ItemID)
			}
			b.WriteString(body.Delta)
			s.mu.Unlock()
		}
	case NotifyItemCompleted:
		// When codex finishes an agent message we also receive the
		// authoritative final text; replace the accumulated deltas to avoid
		// any drift between streamed vs final content.
		var body ItemCompletedNotificationBody
		if err := json.Unmarshal(params, &body); err != nil {
			return
		}
		item, err := DecodeThreadItem(body.Item)
		if err != nil || item.Kind != ThreadItemAgentMessage || item.AgentMessage == nil {
			return
		}
		s.mu.Lock()
		b, ok := s.textByItemID[item.ID]
		if !ok {
			b = &strings.Builder{}
			s.textByItemID[item.ID] = b
			s.itemOrder = append(s.itemOrder, item.ID)
		}
		b.Reset()
		b.WriteString(item.AgentMessage.Text)
		s.mu.Unlock()
	case NotifyThreadTokenUsageUpdated:
		// Codex 0.120.0 reports the authoritative token tally via
		// thread/tokenUsage/updated notifications that arrive right before
		// turn/completed. We keep the latest snapshot so DriverRunResult
		// reflects it even when turn/completed omits the usage field.
		var body ThreadTokenUsageUpdatedNotification
		if err := json.Unmarshal(params, &body); err == nil {
			s.mu.Lock()
			s.usage = &agentadaptor.Usage{
				InputTokens:       body.TokenUsage.Total.InputTokens,
				OutputTokens:      body.TokenUsage.Total.OutputTokens,
				CachedInputTokens: body.TokenUsage.Total.CachedInputTokens,
			}
			s.mu.Unlock()
		}
	case NotifyTurnCompleted:
		var body TurnCompletedNotificationBody
		if err := json.Unmarshal(params, &body); err == nil && body.Turn.Usage != nil {
			s.mu.Lock()
			s.usage = &agentadaptor.Usage{
				InputTokens:       body.Turn.Usage.InputTokens,
				OutputTokens:      body.Turn.Usage.OutputTokens,
				CachedInputTokens: body.Turn.Usage.CachedInputTokens,
			}
			s.mu.Unlock()
		}
		s.signalDone()
	case NotifyTurnFailed:
		var body TurnFailedNotificationBody
		_ = json.Unmarshal(params, &body)
		s.mu.Lock()
		s.turnFailure = &agentadaptor.RunFailure{
			Message: "codex turn failed",
			Code:    "codex.turn_failed",
		}
		if len(body.Turn.Error) > 0 {
			s.turnFailure.Metadata = map[string]any{"error": string(body.Turn.Error)}
		}
		s.mu.Unlock()
		s.signalDone()
	case NotifyError:
		// error notifications may be retriable; only treat them as fatal
		// when willRetry=false.
		var body ErrorNotification
		if err := json.Unmarshal(params, &body); err == nil && !body.WillRetry {
			s.mu.Lock()
			if s.turnFailure == nil {
				s.turnFailure = &agentadaptor.RunFailure{
					Message: body.Error.Message,
					Code:    "codex.error",
				}
			}
			s.mu.Unlock()
			s.signalDone()
		}
	}
}

func (s *runState) signalDone() {
	s.doneOnce.Do(func() { close(s.done) })
}

func (s *runState) assembleOutput() string {
	s.mu.Lock()
	defer s.mu.Unlock()
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

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func toExecEnv(bindings []agentadaptor.EnvBinding) []string {
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
		return &agentadaptor.ResumeRejectedError{
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
