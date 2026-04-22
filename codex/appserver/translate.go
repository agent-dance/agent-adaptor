package appserver

import (
	"encoding/json"
	"sync"

	agentadaptor "github.com/agent-dance/agent-adaptor"
)

// Translator converts codex app-server notifications into StreamPayload
// events understood by the SDK and downstream bridges. A translator is
// per-run and single-goroutine: jrpc2 delivers notifications sequentially so
// there is no internal locking beyond what tracks per-turn state.
type Translator struct {
	sink     agentadaptor.EventSink
	runID    string
	threadID string

	mu         sync.Mutex
	turnID     string
	endedText  map[string]bool // itemIDs whose text.end has been emitted
	endedReas  map[string]bool // itemIDs whose reasoning.end has been emitted
	endedTools map[string]bool // itemIDs whose tool_call.end has been emitted
	toolNames  map[string]string

	// latestUsage records the most recent token usage snapshot reported via
	// thread/tokenUsage/updated notifications. Codex splits usage out of
	// turn/completed in current builds, so the translator caches it and
	// attaches it to StreamRunFinished on turn completion.
	latestUsage *agentadaptor.Usage

	// runStarted tracks whether we already emitted StreamRunStarted. Codex
	// emits turn/started and thread/started separately; we fold thread info
	// into run.started so the bridge sees a single lifecycle boundary.
	runStarted bool
}

// NewTranslator creates a translator that attributes every emitted payload
// to runID.
func NewTranslator(sink agentadaptor.EventSink, runID string) *Translator {
	return &Translator{
		sink:       sink,
		runID:      runID,
		endedText:  map[string]bool{},
		endedReas:  map[string]bool{},
		endedTools: map[string]bool{},
		toolNames:  map[string]string{},
	}
}

// SetThread records the thread id associated with the active run. Bridges
// use it to fill StreamPayload.ThreadID on every emitted payload.
func (t *Translator) SetThread(id string) {
	t.mu.Lock()
	t.threadID = id
	t.mu.Unlock()
}

// ThreadID returns the thread id currently associated with the run. It is
// exposed for adapter code that needs to report the id back to the caller.
func (t *Translator) ThreadID() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.threadID
}

// Dispatch routes one notification to the appropriate handler. Unknown
// methods are forwarded as a StreamPayload with Kind "" and their raw body
// in Raw so downstream bridges can still surface them as custom events.
func (t *Translator) Dispatch(method string, params json.RawMessage) {
	switch method {
	case NotifyThreadStarted:
		t.handleThreadStarted(params)
	case NotifyThreadTokenUsageUpdated:
		t.handleThreadTokenUsageUpdated(params)
	case NotifyTurnStarted:
		t.handleTurnStarted(params)
	case NotifyTurnCompleted:
		t.handleTurnCompleted(params)
	case NotifyTurnFailed:
		t.handleTurnFailed(params)
	case NotifyItemStarted:
		t.handleItemStarted(params)
	case NotifyItemCompleted:
		t.handleItemCompleted(params)
	case NotifyItemAgentMessageDelta:
		t.handleAgentMessageDelta(params)
	case NotifyItemReasoningTextDelta, NotifyItemReasoningSummaryTextDelta, NotifyItemReasoningSummaryPartAdded:
		t.handleReasoningTextDelta(params)
	case NotifyItemCommandExecutionOutputDelta:
		t.handleCommandExecOutputDelta(params)
	case NotifyCommandExecOutputDelta:
		// command/exec/outputDelta belongs to the connection-scoped
		// command/exec surface rather than the turn/item pipeline. The
		// codex adapter never issues command/exec directly, so we surface
		// it opaquely for forward compatibility.
		t.emit(agentadaptor.StreamPayload{
			Kind: "",
			Name: NotifyCommandExecOutputDelta,
			Raw:  rawToMap(params, NotifyCommandExecOutputDelta),
		})
	case NotifyItemFileChangeOutputDelta:
		t.handleFileChangeOutputDelta(params)
	case NotifyItemPlanDelta:
		t.handlePlanDelta(method, params)
	case NotifyError:
		t.handleError(params)
	default:
		// Opaque pass-through for notifications we do not model. The Raw
		// payload lets downstream bridges (e.g. AG-UI) decide whether to
		// surface or ignore it.
		t.emit(agentadaptor.StreamPayload{
			Kind: "",
			Name: method,
			Raw:  rawToMap(params, method),
		})
	}
}

// ---------------------------------------------------------------------------
// Handlers
// ---------------------------------------------------------------------------

func (t *Translator) handleThreadStarted(params json.RawMessage) {
	var body ThreadStartedNotificationBody
	if err := json.Unmarshal(params, &body); err == nil && body.Thread.ID != "" {
		t.SetThread(body.Thread.ID)
	}
	// thread/started precedes turn/started; we defer StreamRunStarted until
	// the first turn to carry a TurnID.
}

func (t *Translator) handleThreadTokenUsageUpdated(params json.RawMessage) {
	// Cache the latest usage so handleTurnCompleted can attach it to
	// StreamRunFinished. Also pass through the raw payload for bridges that
	// want live token-meter rendering.
	var body ThreadTokenUsageUpdatedNotification
	if err := json.Unmarshal(params, &body); err == nil {
		t.mu.Lock()
		t.latestUsage = &agentadaptor.Usage{
			InputTokens:       body.TokenUsage.Total.InputTokens,
			OutputTokens:      body.TokenUsage.Total.OutputTokens,
			CachedInputTokens: body.TokenUsage.Total.CachedInputTokens,
		}
		t.mu.Unlock()
	}
	t.emit(agentadaptor.StreamPayload{
		Kind: "",
		Name: NotifyThreadTokenUsageUpdated,
		Raw:  rawToMap(params, NotifyThreadTokenUsageUpdated),
	})
}

func (t *Translator) handleTurnStarted(params json.RawMessage) {
	var body TurnStartedNotificationBody
	if err := json.Unmarshal(params, &body); err == nil {
		t.mu.Lock()
		t.turnID = body.Turn.ID
		if body.ThreadID != "" {
			t.threadID = body.ThreadID
		}
		t.mu.Unlock()
	}
	payload := t.basePayload()
	payload.Kind = agentadaptor.StreamRunStarted
	if !t.runStarted {
		t.runStarted = true
	}
	t.emit(payload)
}

func (t *Translator) handleTurnCompleted(params json.RawMessage) {
	var body TurnCompletedNotificationBody
	_ = json.Unmarshal(params, &body)
	payload := t.basePayload()
	payload.Kind = agentadaptor.StreamRunFinished
	if body.Turn.Usage != nil {
		payload.Usage = &agentadaptor.Usage{
			InputTokens:       body.Turn.Usage.InputTokens,
			OutputTokens:      body.Turn.Usage.OutputTokens,
			CachedInputTokens: body.Turn.Usage.CachedInputTokens,
		}
	} else {
		t.mu.Lock()
		if t.latestUsage != nil {
			copyUsage := *t.latestUsage
			payload.Usage = &copyUsage
		}
		t.mu.Unlock()
	}
	t.emit(payload)
}

func (t *Translator) handleTurnFailed(params json.RawMessage) {
	var body TurnFailedNotificationBody
	_ = json.Unmarshal(params, &body)
	payload := t.basePayload()
	payload.Kind = agentadaptor.StreamRunError
	if len(body.Turn.Error) > 0 {
		payload.Error = &agentadaptor.RunFailure{
			Message: "codex turn failed",
			Code:    "codex.turn_failed",
		}
		payload.Raw = map[string]any{"error": string(body.Turn.Error)}
	}
	t.emit(payload)
}

func (t *Translator) handleItemStarted(params json.RawMessage) {
	var body ItemStartedNotificationBody
	if err := json.Unmarshal(params, &body); err != nil {
		return
	}
	t.mu.Lock()
	if body.TurnID != "" {
		t.turnID = body.TurnID
	}
	if body.ThreadID != "" {
		t.threadID = body.ThreadID
	}
	t.mu.Unlock()

	item, err := DecodeThreadItem(body.Item)
	if err != nil {
		return
	}
	switch item.Kind {
	case ThreadItemAgentMessage:
		payload := t.basePayload()
		payload.Kind = agentadaptor.StreamTextStart
		payload.MessageID = item.ID
		t.emit(payload)
	case ThreadItemReasoning:
		payload := t.basePayload()
		payload.Kind = agentadaptor.StreamReasoningStart
		payload.MessageID = item.ID
		t.emit(payload)
	case ThreadItemCommandExecution:
		t.mu.Lock()
		t.toolNames[item.ID] = "shell"
		t.mu.Unlock()
		payload := t.basePayload()
		payload.Kind = agentadaptor.StreamToolCallStart
		payload.ToolCallID = item.ID
		payload.Name = "shell"
		if item.CommandExecution != nil {
			payload.Args = map[string]any{
				"command": item.CommandExecution.Command,
				"cwd":     item.CommandExecution.CWD,
			}
		}
		t.emit(payload)
	case ThreadItemFileChange:
		t.mu.Lock()
		t.toolNames[item.ID] = "apply_patch"
		t.mu.Unlock()
		payload := t.basePayload()
		payload.Kind = agentadaptor.StreamToolCallStart
		payload.ToolCallID = item.ID
		payload.Name = "apply_patch"
		t.emit(payload)
	case ThreadItemMcpToolCall:
		toolName := "mcp"
		if item.McpToolCall != nil && item.McpToolCall.Tool != "" {
			toolName = item.McpToolCall.Server + "/" + item.McpToolCall.Tool
		}
		t.mu.Lock()
		t.toolNames[item.ID] = toolName
		t.mu.Unlock()
		payload := t.basePayload()
		payload.Kind = agentadaptor.StreamToolCallStart
		payload.ToolCallID = item.ID
		payload.Name = toolName
		if item.McpToolCall != nil && len(item.McpToolCall.Arguments) > 0 {
			payload.Args = map[string]any{"arguments": string(item.McpToolCall.Arguments)}
		}
		t.emit(payload)
	case ThreadItemWebSearch:
		t.mu.Lock()
		t.toolNames[item.ID] = "web_search"
		t.mu.Unlock()
		payload := t.basePayload()
		payload.Kind = agentadaptor.StreamToolCallStart
		payload.ToolCallID = item.ID
		payload.Name = "web_search"
		if item.WebSearch != nil {
			payload.Args = map[string]any{"query": item.WebSearch.Query}
		}
		t.emit(payload)
	case ThreadItemDynamicToolCall:
		toolName := "dynamic"
		if item.DynamicToolCall != nil && item.DynamicToolCall.Tool != "" {
			toolName = item.DynamicToolCall.Tool
		}
		t.mu.Lock()
		t.toolNames[item.ID] = toolName
		t.mu.Unlock()
		payload := t.basePayload()
		payload.Kind = agentadaptor.StreamToolCallStart
		payload.ToolCallID = item.ID
		payload.Name = toolName
		if item.DynamicToolCall != nil && len(item.DynamicToolCall.Arguments) > 0 {
			payload.Args = map[string]any{"arguments": string(item.DynamicToolCall.Arguments)}
		}
		t.emit(payload)
	default:
		// Unknown or unmodelled item kind. Forward as raw.
		payload := t.basePayload()
		payload.Kind = ""
		payload.Name = NotifyItemStarted
		payload.Raw = rawToMap(body.Item, "item")
		t.emit(payload)
	}
}

func (t *Translator) handleItemCompleted(params json.RawMessage) {
	var body ItemCompletedNotificationBody
	if err := json.Unmarshal(params, &body); err != nil {
		return
	}
	item, err := DecodeThreadItem(body.Item)
	if err != nil {
		return
	}
	switch item.Kind {
	case ThreadItemAgentMessage:
		if !t.markTextEnded(item.ID) {
			return
		}
		payload := t.basePayload()
		payload.Kind = agentadaptor.StreamTextEnd
		payload.MessageID = item.ID
		t.emit(payload)
	case ThreadItemReasoning:
		if !t.markReasoningEnded(item.ID) {
			return
		}
		payload := t.basePayload()
		payload.Kind = agentadaptor.StreamReasoningEnd
		payload.MessageID = item.ID
		t.emit(payload)
	case ThreadItemCommandExecution:
		t.emitToolEnd(item.ID, toolResultFromCommand(item.CommandExecution))
	case ThreadItemFileChange:
		t.emitToolEnd(item.ID, toolResultFromFileChange(item.FileChange))
	case ThreadItemMcpToolCall:
		t.emitToolEnd(item.ID, toolResultFromMcp(item.McpToolCall))
	case ThreadItemWebSearch, ThreadItemDynamicToolCall:
		t.emitToolEnd(item.ID, nil)
	default:
		// Unknown: forward raw so bridges can decide.
		payload := t.basePayload()
		payload.Kind = ""
		payload.Name = NotifyItemCompleted
		payload.Raw = rawToMap(body.Item, "item")
		t.emit(payload)
	}
}

func (t *Translator) emitToolEnd(itemID string, result map[string]any) {
	t.mu.Lock()
	if t.endedTools[itemID] {
		t.mu.Unlock()
		return
	}
	t.endedTools[itemID] = true
	name := t.toolNames[itemID]
	t.mu.Unlock()

	endPayload := t.basePayload()
	endPayload.Kind = agentadaptor.StreamToolCallEnd
	endPayload.ToolCallID = itemID
	endPayload.Name = name
	t.emit(endPayload)

	if result != nil {
		resultPayload := t.basePayload()
		resultPayload.Kind = agentadaptor.StreamToolCallResult
		resultPayload.ToolCallID = itemID
		resultPayload.Name = name
		resultPayload.Result = result
		t.emit(resultPayload)
	}
}

func (t *Translator) handleAgentMessageDelta(params json.RawMessage) {
	var body AgentMessageDeltaNotification
	if err := json.Unmarshal(params, &body); err != nil {
		return
	}
	payload := t.basePayload()
	payload.Kind = agentadaptor.StreamTextContent
	payload.MessageID = body.ItemID
	payload.Delta = body.Delta
	if body.ThreadID != "" {
		payload.ThreadID = body.ThreadID
	}
	if body.TurnID != "" {
		payload.TurnID = body.TurnID
	}
	t.emit(payload)
}

func (t *Translator) handleReasoningTextDelta(params json.RawMessage) {
	// All three reasoning delta notifications share the same shape; the
	// generated type covers them correctly for ReasoningTextDelta, but the
	// adapter also consumes summaryTextDelta / summaryPartAdded, which use
	// the same wire schema.
	var body ReasoningTextDeltaNotification
	if err := json.Unmarshal(params, &body); err != nil {
		return
	}
	payload := t.basePayload()
	payload.Kind = agentadaptor.StreamReasoningContent
	payload.MessageID = body.ItemID
	payload.Delta = body.Delta
	if body.ThreadID != "" {
		payload.ThreadID = body.ThreadID
	}
	if body.TurnID != "" {
		payload.TurnID = body.TurnID
	}
	t.emit(payload)
}

func (t *Translator) handleCommandExecOutputDelta(params json.RawMessage) {
	// item/commandExecution/outputDelta delivers incremental command output
	// associated with an active commandExecution ThreadItem. We surface it
	// as streaming tool_call args so the bridge can treat command output
	// like a streaming argument buffer.
	var body CommandExecutionOutputDeltaNotification
	if err := json.Unmarshal(params, &body); err != nil {
		return
	}
	payload := t.basePayload()
	payload.Kind = agentadaptor.StreamToolCallArgs
	payload.ToolCallID = body.ItemID
	payload.Delta = body.Delta
	if body.ThreadID != "" {
		payload.ThreadID = body.ThreadID
	}
	if body.TurnID != "" {
		payload.TurnID = body.TurnID
	}
	t.emit(payload)
}

func (t *Translator) handleFileChangeOutputDelta(params json.RawMessage) {
	var body FileChangeOutputDeltaNotification
	if err := json.Unmarshal(params, &body); err != nil {
		return
	}
	payload := t.basePayload()
	payload.Kind = agentadaptor.StreamToolCallArgs
	payload.ToolCallID = body.ItemID
	payload.Delta = body.Delta
	if body.ThreadID != "" {
		payload.ThreadID = body.ThreadID
	}
	if body.TurnID != "" {
		payload.TurnID = body.TurnID
	}
	t.emit(payload)
}

func (t *Translator) handlePlanDelta(method string, params json.RawMessage) {
	// Plan deltas don't map cleanly onto the StreamKind alphabet yet. We
	// surface them as opaque custom events; future refinement can introduce
	// a dedicated Kind if we add native plan rendering.
	payload := t.basePayload()
	payload.Kind = ""
	payload.Name = method
	payload.Raw = rawToMap(params, method)
	t.emit(payload)
}

func (t *Translator) handleError(params json.RawMessage) {
	var body ErrorNotification
	_ = json.Unmarshal(params, &body)
	payload := t.basePayload()
	payload.Kind = agentadaptor.StreamRunError
	msg := body.Error.Message
	if msg == "" {
		msg = "codex app-server error"
	}
	failure := &agentadaptor.RunFailure{
		Message: msg,
		Code:    "codex.error",
	}
	if body.Error.AdditionalDetails != nil && *body.Error.AdditionalDetails != "" {
		failure.Metadata = map[string]any{"details": *body.Error.AdditionalDetails}
	}
	payload.Error = failure
	payload.Raw = rawToMap(params, NotifyError)
	t.emit(payload)
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func (t *Translator) basePayload() agentadaptor.StreamPayload {
	t.mu.Lock()
	defer t.mu.Unlock()
	return agentadaptor.StreamPayload{
		RunID:    t.runID,
		ThreadID: t.threadID,
		TurnID:   t.turnID,
	}
}

func (t *Translator) markTextEnded(id string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.endedText[id] {
		return false
	}
	t.endedText[id] = true
	return true
}

func (t *Translator) markReasoningEnded(id string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.endedReas[id] {
		return false
	}
	t.endedReas[id] = true
	return true
}

func (t *Translator) emit(payload agentadaptor.StreamPayload) {
	if t.sink == nil {
		return
	}
	if payload.RunID == "" && t.runID != "" {
		// Defensive: if basePayload was bypassed, backfill the run id so
		// downstream consumers always see a stable attribution.
		payload.RunID = t.runID
	}
	_ = t.sink.EmitStream(payload)
}

func rawToMap(raw json.RawMessage, name string) map[string]any {
	if len(raw) == 0 {
		return nil
	}
	var generic any
	if err := json.Unmarshal(raw, &generic); err != nil {
		return map[string]any{"raw": string(raw), "notification": name}
	}
	out, ok := generic.(map[string]any)
	if !ok {
		return map[string]any{"payload": generic, "notification": name}
	}
	return out
}

func toolResultFromCommand(body *ThreadItemCommandExecutionBody) map[string]any {
	if body == nil {
		return nil
	}
	out := map[string]any{
		"status": body.Status,
	}
	if body.ExitCode != nil {
		out["exitCode"] = *body.ExitCode
	}
	if body.AggregatedOutput != "" {
		out["output"] = body.AggregatedOutput
	}
	if body.DurationMs != nil {
		out["durationMs"] = *body.DurationMs
	}
	return out
}

func toolResultFromFileChange(body *ThreadItemFileChangeBody) map[string]any {
	if body == nil {
		return nil
	}
	changes := make([]map[string]any, 0, len(body.Changes))
	for _, c := range body.Changes {
		changes = append(changes, map[string]any{
			"path": c.Path,
			"diff": c.Diff,
		})
	}
	return map[string]any{
		"status":  body.Status,
		"changes": changes,
	}
}

func toolResultFromMcp(body *ThreadItemMcpToolCallBody) map[string]any {
	if body == nil {
		return nil
	}
	out := map[string]any{
		"status": body.Status,
		"server": body.Server,
		"tool":   body.Tool,
	}
	if len(body.Result) > 0 {
		out["result"] = string(body.Result)
	}
	if len(body.Error) > 0 {
		out["error"] = string(body.Error)
	}
	if body.DurationMs != nil {
		out["durationMs"] = *body.DurationMs
	}
	return out
}
