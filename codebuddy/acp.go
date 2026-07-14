package codebuddy

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

	agentadaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/internal/processx"
	"github.com/sourcegraph/jsonrpc2"
)

// ACP (Agent Client Protocol) method names used by the CodeBuddy interactive
// engine. Only the subset the driver drives or answers is named; other
// notifications are forwarded verbatim through StreamPayload.Raw.
const (
	acpInitialize        = "initialize"
	acpSessionNew        = "session/new"
	acpSessionLoad       = "session/load"
	acpSessionPrompt     = "session/prompt"
	acpSessionUpdate     = "session/update"
	acpRequestPermission = "session/request_permission"
	acpSessionCancel     = "session/cancel"
)

// acpProtocolVersion is the ACP protocol version negotiated with CodeBuddy
// (observed from a live `codebuddy --acp` initialize handshake).
const acpProtocolVersion = 1

// acpUsageGrace 限定 session/prompt 返回后，等待尾随 usage_update 通知的时长。
const acpUsageGrace = 3 * time.Second

// runACP drives the interactive ACP engine: it spawns
// `codebuddy --acp --acp-transport stdio`, performs the initialize /
// session.new(or load) / session.prompt handshake, forwards session/update
// notifications into the sink, and answers session/request_permission requests
// through the run policy / host decision path.
func (adapter) runACP(ctx context.Context, cfg agentadaptor.CodeBuddyConfig, command string, req agentadaptor.DriverRunRequest, sink agentadaptor.EventSink, prep runPrep) (agentadaptor.DriverRunResult, error) {
	args := append([]string{"--acp", "--acp-transport", "stdio"}, cfg.ExtraArgs...)

	// runCtx 让清理阶段能主动触发 processx 的进程组终止。CodeBuddy 的 `--acp`
	// 是常驻服务进程：关闭 stdin 并不足以让它退出，普通的 cmd.Wait 会一直阻塞到
	// ctx deadline。绑定到 runCtx 后，cancelRun 会触发 cmd.Cancel（进程组 kill），
	// 并由 cmd.WaitDelay 兜底强制关闭管道。
	runCtx, cancelRun := context.WithCancel(ctx)
	defer cancelRun()

	cmd := exec.CommandContext(runCtx, command, args...)
	processx.ConfigureCancellation(cmd)
	if prep.effectiveCWD != "" {
		cmd.Dir = prep.effectiveCWD
	}
	cmd.Env = toExecEnv(prep.env)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return agentadaptor.DriverRunResult{}, fmt.Errorf("codebuddy acp stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return agentadaptor.DriverRunResult{}, fmt.Errorf("codebuddy acp stdout: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return agentadaptor.DriverRunResult{}, fmt.Errorf("codebuddy acp stderr: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return agentadaptor.DriverRunResult{}, fmt.Errorf("codebuddy acp start: %w", err)
	}

	_ = sink.Emit(agentadaptor.RunEvent{
		Type:      agentadaptor.RunEventSpawn,
		Timestamp: time.Now().UTC(),
		Text:      "codebuddy acp spawned",
		Metadata:  map[string]string{"command": command, "mode": "acp"},
		Data:      map[string]any{"pid": cmd.Process.Pid, "args": append([]string(nil), args...)},
	})

	stderrBuf := &syncBuffer{}
	stderrDone := make(chan struct{})
	go func() {
		defer close(stderrDone)
		_, _ = io.Copy(stderrBuf, stderr)
	}()

	state := newACPState(req.RunID, sink, req.Policy.HumanDecision)
	stream := newStdioStream(stdin, stdout)
	conn := jsonrpc2.NewConn(ctx, stream, &acpHandler{state: state})
	state.conn = conn
	defer func() {
		_ = conn.Close()
		// 主动取消 runCtx 触发进程组终止（由 cmd.WaitDelay 兜底），否则常驻的
		// `--acp` 服务不会因 stdin 关闭而退出，cmd.Wait 会阻塞到 ctx deadline。
		cancelRun()
		_ = cmd.Wait()
		<-stderrDone
	}()

	// 1. initialize handshake — declare fs read/write as unsupported so the
	//    agent does not delegate filesystem access back to the SDK.
	var initResult json.RawMessage
	initParams := map[string]any{
		"protocolVersion": acpProtocolVersion,
		"clientCapabilities": map[string]any{
			"fs": map[string]any{"readTextFile": false, "writeTextFile": false},
		},
	}
	if err := conn.Call(ctx, acpInitialize, initParams, &initResult); err != nil {
		return agentadaptor.DriverRunResult{}, fmt.Errorf("codebuddy acp initialize: %w", err)
	}

	// 2. session new / load.
	sessionID, err := establishACPSession(ctx, conn, req, prep)
	if err != nil {
		return agentadaptor.DriverRunResult{}, err
	}
	state.setSession(sessionID)

	// 3. session/prompt — blocks until the turn completes; permission
	//    requests and notifications are handled on the dispatcher goroutine.
	promptParams := map[string]any{
		"sessionId": sessionID,
		"prompt":    []map[string]any{{"type": "text", "text": prep.prompt}},
	}
	var promptResult struct {
		StopReason string `json:"stopReason"`
	}
	promptErr := conn.Call(ctx, acpSessionPrompt, promptParams, &promptResult)
	if promptErr != nil {
		if ctx.Err() != nil {
			_ = conn.Notify(context.Background(), acpSessionCancel, map[string]any{"sessionId": sessionID})
			return agentadaptor.DriverRunResult{}, ctx.Err()
		}
		return agentadaptor.DriverRunResult{}, fmt.Errorf("codebuddy acp session/prompt: %w", promptErr)
	}

	// 3b. 等待尾随的 usage_update 落地（若尚未收到），再装配结果。CodeBuddy 的
	//     usage_update 通常在 session/prompt 响应前就已到达，这里只是对偶发的尾随
	//     场景做一次有界兜底。
	state.waitUsage(ctx, acpUsageGrace)

	// 3c. 补发 StreamRunFinished（携带用量）。ACP 的通知流本身不含终止帧，需由
	//     引擎显式补发，与 headless / claude / codex 引擎的流式收尾保持一致。
	state.emitRunFinished()

	// 4. assemble result.
	checkpoint := &agentadaptor.DriverCheckpoint{
		State: &agentadaptor.DriverSessionState{
			ResumeID:  sessionID,
			DisplayID: sessionID,
			Data: map[string]string{
				agentadaptor.SessionParamCWD:                prep.effectiveCWD,
				agentadaptor.SessionParamWorkspaceID:        req.Workspace.ID,
				agentadaptor.SessionParamProfileFingerprint: req.ProfilePayload.Fingerprint,
			},
		},
		Valid: true,
	}
	return state.result(promptResult.StopReason, checkpoint, prep.reportedModel, stderrBuf.String()), nil
}

func establishACPSession(ctx context.Context, conn *jsonrpc2.Conn, req agentadaptor.DriverRunRequest, prep runPrep) (string, error) {
	if req.Session != nil && req.Session.State != nil && req.Session.State.ResumeID != "" {
		loadParams := map[string]any{
			"sessionId":  req.Session.State.ResumeID,
			"cwd":        prep.effectiveCWD,
			"mcpServers": []any{},
		}
		var loadResult json.RawMessage
		if err := conn.Call(ctx, acpSessionLoad, loadParams, &loadResult); err != nil {
			if isSessionNotFound(err) {
				return "", &agentadaptor.ResumeRejectedError{Reason: fmt.Sprintf("codebuddy session %q is unavailable: %s", req.Session.State.ResumeID, err.Error())}
			}
			return "", fmt.Errorf("codebuddy acp session/load: %w", err)
		}
		return req.Session.State.ResumeID, nil
	}

	newParams := map[string]any{
		"cwd":        prep.effectiveCWD,
		"mcpServers": []any{},
	}
	var newResult struct {
		SessionID string `json:"sessionId"`
	}
	if err := conn.Call(ctx, acpSessionNew, newParams, &newResult); err != nil {
		return "", fmt.Errorf("codebuddy acp session/new: %w", err)
	}
	if newResult.SessionID == "" {
		return "", errors.New("codebuddy acp session/new returned empty sessionId")
	}
	return newResult.SessionID, nil
}

func isSessionNotFound(err error) bool {
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "not found") || strings.Contains(msg, "unknown session") || strings.Contains(msg, "no session")
}

// ---------------------------------------------------------------------------
// JSON-RPC handler
// ---------------------------------------------------------------------------

type acpHandler struct {
	state *acpState
}

func (h *acpHandler) Handle(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) {
	if req.Notif {
		var raw json.RawMessage
		if req.Params != nil {
			raw = *req.Params
		}
		h.state.onNotification(req.Method, raw)
		return
	}

	switch req.Method {
	case acpRequestPermission:
		var raw json.RawMessage
		if req.Params != nil {
			raw = *req.Params
		}
		result, err := h.state.handlePermission(ctx, raw)
		if err != nil {
			_ = conn.ReplyWithError(ctx, req.ID, &jsonrpc2.Error{Code: jsonrpc2.CodeInternalError, Message: err.Error()})
			return
		}
		_ = conn.Reply(ctx, req.ID, result)
	default:
		// Any other server-initiated request (fs/*, terminal/*) is declined:
		// the SDK advertised no such client capabilities.
		_ = conn.ReplyWithError(ctx, req.ID, &jsonrpc2.Error{
			Code:    jsonrpc2.CodeMethodNotFound,
			Message: fmt.Sprintf("codebuddy adapter does not accept server-initiated request %q", req.Method),
		})
	}
}

// ---------------------------------------------------------------------------
// Run state
// ---------------------------------------------------------------------------

type acpState struct {
	runID  string
	sink   agentadaptor.EventSink
	policy agentadaptor.HumanDecisionPolicy
	conn   *jsonrpc2.Conn

	mu              sync.Mutex
	sessionID       string
	assistantText   strings.Builder
	transcript      []agentadaptor.TranscriptItem
	usage           *agentadaptor.Usage
	pendingFailure  *agentadaptor.RunFailure
	decisionSeq     int
	plansByToolCall map[string]string

	// usageReady 在首次收到 usage_update 时关闭。CodeBuddy 把 usage_update 作为
	// 一条尾随通知发出，可能刚好晚于 session/prompt 的响应，因此 runACP 在装配
	// 结果前用它做一次有界等待，避免漏掉本轮用量统计。
	usageReady chan struct{}
	usageOnce  sync.Once
}

func newACPState(runID string, sink agentadaptor.EventSink, policy agentadaptor.HumanDecisionPolicy) *acpState {
	return &acpState{
		runID:           runID,
		sink:            sink,
		policy:          policy,
		plansByToolCall: make(map[string]string),
		usageReady:      make(chan struct{}),
	}
}

func (s *acpState) setSession(id string) {
	s.mu.Lock()
	s.sessionID = id
	s.mu.Unlock()
}

func (s *acpState) threadID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sessionID
}

func (s *acpState) emitItem(item agentadaptor.TranscriptItem) {
	s.mu.Lock()
	s.transcript = append(s.transcript, item)
	s.mu.Unlock()
	if s.sink == nil {
		return
	}
	clone := item
	_ = s.sink.Emit(agentadaptor.RunEvent{Type: agentadaptor.RunEventItem, Timestamp: time.Now().UTC(), Item: &clone})
}

func (s *acpState) emitStream(pl agentadaptor.StreamPayload) {
	if s.sink == nil {
		return
	}
	if pl.RunID == "" {
		pl.RunID = s.runID
	}
	if pl.ThreadID == "" {
		pl.ThreadID = s.threadID()
	}
	_ = s.sink.EmitStream(pl)
}

// onNotification handles session/update notifications and forwards anything
// unrecognized as raw StreamPayload.
func (s *acpState) onNotification(method string, params json.RawMessage) {
	if method != acpSessionUpdate {
		s.emitStream(agentadaptor.StreamPayload{Name: method, Raw: rawToMap(params)})
		return
	}
	var body struct {
		SessionID string          `json:"sessionId"`
		Update    json.RawMessage `json:"update"`
	}
	if err := json.Unmarshal(params, &body); err != nil {
		return
	}
	var kindProbe struct {
		SessionUpdate string `json:"sessionUpdate"`
	}
	_ = json.Unmarshal(body.Update, &kindProbe)

	switch kindProbe.SessionUpdate {
	case "agent_message_chunk":
		text := acpChunkText(body.Update)
		if text == "" {
			return
		}
		s.mu.Lock()
		s.assistantText.WriteString(text)
		s.mu.Unlock()
		s.emitStream(agentadaptor.StreamPayload{Kind: agentadaptor.StreamTextContent, MessageID: "acp", Delta: text})
	case "agent_thought_chunk":
		text := acpChunkText(body.Update)
		if text == "" {
			return
		}
		s.emitStream(agentadaptor.StreamPayload{Kind: agentadaptor.StreamReasoningContent, MessageID: "acp-thought", Delta: text})
	case "tool_call", "tool_call_update":
		s.capturePlanContent(body.Update)
		s.emitStream(agentadaptor.StreamPayload{Name: kindProbe.SessionUpdate, Raw: rawToMap(body.Update)})
	case "plan":
		s.emitItem(agentadaptor.TranscriptItem{Kind: agentadaptor.TranscriptSystem, Subtype: "plan", Data: map[string]any{"update": rawToMap(body.Update)}})
	case "usage_update":
		s.mergeUsage(body.Update)
	default:
		s.emitStream(agentadaptor.StreamPayload{Name: kindProbe.SessionUpdate, Raw: rawToMap(body.Update)})
	}
}

func (s *acpState) capturePlanContent(update json.RawMessage) {
	var body struct {
		ToolCallID string         `json:"toolCallId"`
		Meta       map[string]any `json:"_meta"`
	}
	if err := json.Unmarshal(update, &body); err != nil || body.ToolCallID == "" {
		return
	}
	plan, _ := body.Meta["codebuddy.ai/planContent"].(string)
	if strings.TrimSpace(plan) == "" {
		return
	}
	s.mu.Lock()
	s.plansByToolCall[body.ToolCallID] = plan
	s.mu.Unlock()
}

func (s *acpState) planForToolCall(toolCallID string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.plansByToolCall[toolCallID]
}

func (s *acpState) mergeUsage(update json.RawMessage) {
	var body struct {
		Used int `json:"used"`
		Size int `json:"size"`
	}
	if err := json.Unmarshal(update, &body); err != nil {
		return
	}
	s.mu.Lock()
	if s.usage == nil {
		s.usage = &agentadaptor.Usage{}
	}
	s.usage.InputTokens = body.Used
	s.mu.Unlock()
	s.usageOnce.Do(func() { close(s.usageReady) })
}

// hasUsage 报告是否已收到过 usage_update。
func (s *acpState) hasUsage() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.usage != nil
}

// emitRunFinished 补发本轮的 StreamRunFinished 终止帧，携带已累计的用量。
func (s *acpState) emitRunFinished() {
	s.mu.Lock()
	usage := s.usage
	s.mu.Unlock()
	s.emitStream(agentadaptor.StreamPayload{Kind: agentadaptor.StreamRunFinished, Usage: usage})
}

// waitUsage 在结果装配前对尾随的 usage_update 做一次有界等待：已收到则立即返回，
// 否则最多等待 d，或 ctx 取消时提前返回。
func (s *acpState) waitUsage(ctx context.Context, d time.Duration) {
	if s.hasUsage() {
		return
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-s.usageReady:
	case <-timer.C:
	case <-ctx.Done():
	}
}

// handlePermission answers a session/request_permission request by consulting
// the run policy: AutoApprove / AutoReject resolve locally, Ask routes to the
// host through the DecisionCapableSink.
func (s *acpState) handlePermission(ctx context.Context, params json.RawMessage) (map[string]any, error) {
	var body acpPermissionParams
	if err := json.Unmarshal(params, &body); err != nil {
		return nil, err
	}
	kind := acpDecisionKind(body.ToolCall)
	req := s.buildDecisionRequest(kind, body)

	effective := agentadaptor.EffectiveHumanDecisionPolicy(s.policy)
	var decision agentadaptor.DecisionResponse
	switch {
	case kind == agentadaptor.HumanDecisionQuestion:
		decision = s.resolveQuestion(ctx, effective, req)
	default:
		decision = s.resolvePermissionLike(ctx, kind, effective, req)
	}

	s.recordRejectFailure(kind, req, decision, effective)
	return acpOutcome(body.Options, decision), nil
}

func (s *acpState) resolvePermissionLike(ctx context.Context, kind agentadaptor.HumanDecisionKind, effective agentadaptor.HumanDecisionPolicy, req agentadaptor.DecisionRequest) agentadaptor.DecisionResponse {
	mode := effective.Permission
	if kind == agentadaptor.HumanDecisionPlanReview {
		mode = effective.PlanReview
	}
	switch mode {
	case agentadaptor.HumanDecisionAutoApprove:
		return agentadaptor.DecisionResponse{RequestID: req.RequestID, Result: agentadaptor.DecisionApproved}
	case agentadaptor.HumanDecisionAutoReject:
		return agentadaptor.DecisionResponse{RequestID: req.RequestID, Result: agentadaptor.DecisionRejected}
	default:
		return s.ask(ctx, req)
	}
}

func (s *acpState) resolveQuestion(ctx context.Context, effective agentadaptor.HumanDecisionPolicy, req agentadaptor.DecisionRequest) agentadaptor.DecisionResponse {
	switch effective.Question {
	case agentadaptor.QuestionAutoReject:
		return agentadaptor.DecisionResponse{RequestID: req.RequestID, Result: agentadaptor.DecisionRejected}
	default:
		return s.ask(ctx, req)
	}
}

// ask routes a decision to the host and emits the observability pair. When the
// sink cannot take decisions (should not happen for runner-issued runs) it
// falls back to a rejection.
func (s *acpState) ask(ctx context.Context, req agentadaptor.DecisionRequest) agentadaptor.DecisionResponse {
	ic, ok := s.sink.(agentadaptor.DecisionCapableSink)
	if !ok {
		return agentadaptor.DecisionResponse{RequestID: req.RequestID, Result: agentadaptor.DecisionRejected, Text: "decision sink unavailable"}
	}
	requestedAt := time.Now().UTC()
	s.emitStream(agentadaptor.StreamPayload{
		Kind:  agentadaptor.StreamHITLRequested,
		Name:  req.Source,
		RunID: s.runID,
		HITLRequested: &agentadaptor.HITLRequestedPayload{
			RequestID:  req.RequestID,
			Kind:       req.Kind,
			Source:     req.Source,
			ToolCallID: req.ToolCallID,
			Prompt:     req.Prompt,
			Payload:    req.Payload,
			Choices:    append([]agentadaptor.DecisionChoice(nil), req.Choices...),
			CreatedAt:  req.CreatedAt,
		},
	})
	resp, err := ic.RequestDecision(ctx, req)
	if err != nil {
		resp = agentadaptor.DecisionResponse{RequestID: req.RequestID, Result: agentadaptor.DecisionRejected, Text: "user decision aborted"}
	}
	resolvedAt := time.Now().UTC()
	s.emitStream(agentadaptor.StreamPayload{
		Kind:  agentadaptor.StreamHITLResolved,
		Name:  req.Source,
		RunID: s.runID,
		HITLResolved: &agentadaptor.HITLResolvedPayload{
			RequestID:  req.RequestID,
			Kind:       req.Kind,
			Source:     req.Source,
			Result:     resp.Result,
			ResolvedAt: resolvedAt,
			Latency:    resolvedAt.Sub(requestedAt),
		},
	})
	return resp
}

func (s *acpState) buildDecisionRequest(kind agentadaptor.HumanDecisionKind, body acpPermissionParams) agentadaptor.DecisionRequest {
	s.mu.Lock()
	s.decisionSeq++
	seq := s.decisionSeq
	s.mu.Unlock()

	toolName := body.ToolCall.toolName()
	payload := map[string]any{}
	if len(body.ToolCall.RawInput) > 0 {
		payload["rawInput"] = rawToMap(body.ToolCall.RawInput)
	}
	if toolName != "" {
		payload["tool"] = toolName
	}
	if body.ToolCall.Title != "" {
		payload["title"] = body.ToolCall.Title
	}
	if body.ToolCall.Kind != "" {
		payload["tool_kind"] = body.ToolCall.Kind
	}
	if kind == agentadaptor.HumanDecisionPlanReview {
		if plan := s.planForToolCall(body.ToolCall.ToolCallID); plan != "" {
			payload["plan"] = plan
		}
	}
	// Prompt 兜底：权限帧无 title 时用工具名，避免透出给 host 的请求为空。
	prompt := body.ToolCall.Title
	if prompt == "" {
		prompt = toolName
	}
	choices := make([]agentadaptor.DecisionChoice, 0, len(body.Options))
	for _, opt := range body.Options {
		choices = append(choices, agentadaptor.DecisionChoice{Key: opt.OptionID, Label: opt.Name, Description: opt.Kind})
	}
	return agentadaptor.DecisionRequest{
		RequestID:  fmt.Sprintf("%s-dec-codebuddy-%d", s.runID, seq),
		RunID:      s.runID,
		ThreadID:   s.threadID(),
		Kind:       kind,
		Source:     acpSourceForKind(kind),
		ToolCallID: body.ToolCall.ToolCallID,
		Prompt:     prompt,
		Payload:    payload,
		Choices:    choices,
		CreatedAt:  time.Now().UTC(),
	}
}

func (s *acpState) recordRejectFailure(kind agentadaptor.HumanDecisionKind, req agentadaptor.DecisionRequest, resp agentadaptor.DecisionResponse, effective agentadaptor.HumanDecisionPolicy) {
	if resp.Result != agentadaptor.DecisionRejected {
		return
	}
	if effective.OnReject != agentadaptor.FailureAbort && effective.OnReject != agentadaptor.FailureActionUnset {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pendingFailure != nil {
		return
	}
	snapshot := req
	s.pendingFailure = &agentadaptor.RunFailure{
		Code:    agentadaptor.FailureReject,
		Message: acpRejectMessage(kind),
		HumanDecision: &agentadaptor.HumanDecisionFailure{
			Kind:     kind,
			Source:   req.Source,
			Decision: agentadaptor.DecisionRejected,
			Request:  &snapshot,
			Attempts: 1,
		},
	}
}

func (s *acpState) result(stopReason string, checkpoint *agentadaptor.DriverCheckpoint, model, stderr string) agentadaptor.DriverRunResult {
	s.mu.Lock()
	defer s.mu.Unlock()
	output := s.assistantText.String()
	exitCode := 0
	if s.pendingFailure != nil {
		exitCode = 1
	}
	return agentadaptor.DriverRunResult{
		Output:     output,
		Transcript: s.transcript,
		ExitCode:   exitCode,
		Usage:      s.usage,
		Checkpoint: checkpoint,
		Provider:   "codebuddy",
		Model:      model,
		Summary:    output,
		Failure:    s.pendingFailure,
		RawStreams: &agentadaptor.RawStreams{Stderr: stderr},
		Metadata:   map[string]string{"acp_stop_reason": stopReason},
	}
}

// ---------------------------------------------------------------------------
// ACP payload shapes / helpers
// ---------------------------------------------------------------------------

type acpPermissionParams struct {
	SessionID string             `json:"sessionId"`
	ToolCall  acpToolCall        `json:"toolCall"`
	Options   []acpPermissionOpt `json:"options"`
}

type acpToolCall struct {
	ToolCallID string          `json:"toolCallId"`
	Title      string          `json:"title"`
	Kind       string          `json:"kind"`
	RawInput   json.RawMessage `json:"rawInput"`
	Meta       map[string]any  `json:"_meta"`
}

// toolName 解析工具名：session/request_permission 帧不带顶层 title/kind，
// 工具名放在 CodeBuddy 私有的 _meta["codebuddy.ai/toolName"]，优先取它，
// 缺失时回退到 title。
func (tc acpToolCall) toolName() string {
	if tc.Meta != nil {
		if v, ok := tc.Meta["codebuddy.ai/toolName"].(string); ok && v != "" {
			return v
		}
	}
	return tc.Title
}

type acpPermissionOpt struct {
	OptionID string `json:"optionId"`
	Name     string `json:"name"`
	Kind     string `json:"kind"`
}

// acpDecisionKind maps an ACP tool call to an SDK decision kind. Plan review
// and AskUserQuestion are detected from the tool name / title / kind;
// everything else is treated as a tool permission decision. 权限帧无顶层
// title/kind，故一并参考 _meta 里的工具名。
func acpDecisionKind(tc acpToolCall) agentadaptor.HumanDecisionKind {
	name := strings.ToLower(tc.toolName())
	title := strings.ToLower(tc.Title)
	kind := strings.ToLower(tc.Kind)
	if strings.Contains(name, "askuserquestion") || strings.Contains(title, "askuserquestion") || strings.Contains(title, "ask user") || strings.Contains(kind, "question") {
		return agentadaptor.HumanDecisionQuestion
	}
	if strings.Contains(name, "exitplanmode") || strings.Contains(title, "exitplanmode") || strings.Contains(title, "plan mode") || strings.Contains(kind, "plan") {
		return agentadaptor.HumanDecisionPlanReview
	}
	return agentadaptor.HumanDecisionPermission
}

func acpSourceForKind(kind agentadaptor.HumanDecisionKind) string {
	switch kind {
	case agentadaptor.HumanDecisionPlanReview:
		return "ExitPlanMode"
	case agentadaptor.HumanDecisionQuestion:
		return "AskUserQuestion"
	default:
		return "permission"
	}
}

// acpOutcome converts an SDK decision into the ACP request_permission result,
// choosing the option whose kind matches the decision (allow_* for approve,
// reject_* for reject). If no matching option exists it returns a cancelled
// outcome.
func acpOutcome(options []acpPermissionOpt, resp agentadaptor.DecisionResponse) map[string]any {
	approve := resp.Result == agentadaptor.DecisionApproved || resp.Result == agentadaptor.DecisionAnswered
	var chosen string
	// Prefer the "once" variants so approvals/rejections do not leak into
	// later turns.
	order := []string{"allow_once", "allow_always"}
	if !approve {
		order = []string{"reject_once", "reject_always"}
	}
	for _, want := range order {
		for _, opt := range options {
			if strings.EqualFold(opt.Kind, want) {
				chosen = opt.OptionID
				break
			}
		}
		if chosen != "" {
			break
		}
	}
	if chosen == "" {
		return map[string]any{"outcome": map[string]any{"outcome": "cancelled"}}
	}
	return map[string]any{"outcome": map[string]any{"outcome": "selected", "optionId": chosen}}
}

func acpRejectMessage(kind agentadaptor.HumanDecisionKind) string {
	switch kind {
	case agentadaptor.HumanDecisionPlanReview:
		return "CodeBuddy plan was not approved; no changes applied."
	case agentadaptor.HumanDecisionQuestion:
		return "CodeBuddy asked the user a question and the request was rejected."
	default:
		return "CodeBuddy tool permission was denied by the user."
	}
}

func acpChunkText(update json.RawMessage) string {
	var body struct {
		Content struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(update, &body); err != nil {
		return ""
	}
	return body.Content.Text
}

func rawToMap(raw json.RawMessage) map[string]any {
	if len(raw) == 0 {
		return nil
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil
	}
	return out
}

// ---------------------------------------------------------------------------
// stdio transport (mirrors codex/appserver/codec.go)
// ---------------------------------------------------------------------------

type stdioStream struct {
	stdin   io.WriteCloser
	decoder *json.Decoder
	enc     *json.Encoder
	wmu     sync.Mutex
}

func newStdioStream(stdin io.WriteCloser, stdout io.Reader) *stdioStream {
	return &stdioStream{stdin: stdin, decoder: json.NewDecoder(stdout), enc: json.NewEncoder(stdin)}
}

func (s *stdioStream) ReadObject(v interface{}) error { return s.decoder.Decode(v) }

func (s *stdioStream) WriteObject(v interface{}) error {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	return s.enc.Encode(v)
}

func (s *stdioStream) Close() error { return s.stdin.Close() }

func toExecEnv(bindings []agentadaptor.EnvBinding) []string {
	env := os.Environ()
	for _, b := range bindings {
		env = append(env, b.Name+"="+b.Value)
	}
	return env
}

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
