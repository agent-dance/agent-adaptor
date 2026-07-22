package appserver

import (
	"encoding/json"
	"fmt"
)

// This file contains hand-written protocol types for the codex app-server
// surfaces that go-jsonschema cannot represent faithfully:
//
//  1. Request/response envelopes the client sends directly (Initialize,
//     ThreadStart, ThreadResume, TurnStart, TurnInterrupt). We only model
//     the fields the codex adapter actually uses; unknown fields are
//     preserved via json.RawMessage for response extras.
//
//  2. Discriminated unions nested inside schemas (ThreadItem, UserInput,
//     SandboxPolicy, CommandAction, WebSearchAction). go-jsonschema emits
//     these as `interface{}`, which is too loose; we decode them by
//     inspecting the "type" tag.
//
// When codex-cli evolves, rerun the generator and reconcile any fields
// listed here against the new schema under schema/. See
// docs/workstream-streaming-chat.md §16 for the upgrade contract.

// ---------------------------------------------------------------------------
// Initialize
// ---------------------------------------------------------------------------

// InitializeParams is the payload for the "initialize" request.
type InitializeParams struct {
	ClientInfo   ClientInfo      `json:"clientInfo"`
	Capabilities json.RawMessage `json:"capabilities,omitempty"`
}

// ClientInfo identifies the calling application during initialize.
type ClientInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// InitializeResponse is the server's initialize reply. codex-cli 0.120.0
// returns opaque diagnostic metadata; we keep it as RawMessage because no
// current codex adapter code path inspects individual fields.
type InitializeResponse struct {
	UserAgent      string          `json:"userAgent,omitempty"`
	CodexHome      string          `json:"codexHome,omitempty"`
	PlatformFamily string          `json:"platformFamily,omitempty"`
	PlatformOs     string          `json:"platformOs,omitempty"`
	Extras         json.RawMessage `json:"-"`
}

// ---------------------------------------------------------------------------
// thread/start & thread/resume
// ---------------------------------------------------------------------------

// ThreadStartParams matches the v2 ThreadStartParams schema. All fields are
// optional per the codex protocol; we only set the ones the adapter needs.
type ThreadStartParams struct {
	CWD       string          `json:"cwd,omitempty"`
	Ephemeral bool            `json:"ephemeral,omitempty"`
	Sandbox   string          `json:"sandbox,omitempty"`
	Model     string          `json:"model,omitempty"`
	Extras    json.RawMessage `json:"-"`
}

// ThreadRef is the minimal surface of the server's Thread object exposed
// through thread/start and thread/started.
type ThreadRef struct {
	ID        string          `json:"id"`
	Ephemeral bool            `json:"ephemeral,omitempty"`
	CreatedAt int64           `json:"createdAt,omitempty"`
	UpdatedAt int64           `json:"updatedAt,omitempty"`
	Extras    json.RawMessage `json:"-"`
}

// ThreadStartResponse is the reply to thread/start.
type ThreadStartResponse struct {
	Thread ThreadRef `json:"thread"`
}

// ThreadResumeParams is the payload for the "thread/resume" request.
type ThreadResumeParams struct {
	ThreadID string `json:"threadId"`
}

// ThreadResumeResponse is the reply to thread/resume.
type ThreadResumeResponse struct {
	Thread ThreadRef `json:"thread"`
}

// ThreadStartedNotificationBody is the minimal view the adapter takes of
// the "thread/started" notification.
type ThreadStartedNotificationBody struct {
	Thread ThreadRef `json:"thread"`
}

// ---------------------------------------------------------------------------
// turn/start & turn/interrupt
// ---------------------------------------------------------------------------

// TurnStartParams matches the v2 TurnStartParams schema. The adapter always
// attaches a text-only input; richer UserInput variants are future work.
type TurnStartParams struct {
	ThreadID       string          `json:"threadId"`
	Input          []UserInput     `json:"input"`
	ApprovalPolicy string          `json:"approvalPolicy,omitempty"`
	SandboxPolicy  *SandboxPolicy  `json:"sandboxPolicy,omitempty"`
	Model          string          `json:"model,omitempty"`
	Effort         string          `json:"effort,omitempty"`
	CWD            string          `json:"cwd,omitempty"`
	OutputSchema   json.RawMessage `json:"outputSchema,omitempty"`
}

// TurnRef carries the minimum turn metadata the adapter tracks.
type TurnRef struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

// TurnStartResponse is the reply to turn/start.
type TurnStartResponse struct {
	Turn TurnRef `json:"turn"`
}

// TurnInterruptParams is the payload for the "turn/interrupt" request.
type TurnInterruptParams struct {
	ThreadID string `json:"threadId"`
	TurnID   string `json:"turnId,omitempty"`
}

// TurnInterruptResponse is the reply to turn/interrupt.
type TurnInterruptResponse struct{}

// ---------------------------------------------------------------------------
// turn/started & turn/completed notification bodies (trimmed)
// ---------------------------------------------------------------------------

// TurnStartedNotificationBody is the adapter's view of "turn/started".
type TurnStartedNotificationBody struct {
	ThreadID string  `json:"threadId"`
	Turn     TurnRef `json:"turn"`
}

// TurnCompletedTurn is the trimmed turn object embedded in
// "turn/completed". Usage is the important field; Error surfaces on failure.
type TurnCompletedTurn struct {
	ID          string            `json:"id"`
	Status      string            `json:"status"`
	Error       json.RawMessage   `json:"error,omitempty"`
	Usage       *TurnUsage        `json:"usage,omitempty"`
	CompletedAt int64             `json:"completedAt,omitempty"`
	Items       []json.RawMessage `json:"items,omitempty"`
}

// TurnUsage carries the per-turn token counts reported on completion.
type TurnUsage struct {
	InputTokens       int `json:"inputTokens"`
	OutputTokens      int `json:"outputTokens"`
	CachedInputTokens int `json:"cachedInputTokens,omitempty"`
}

// TurnCompletedNotificationBody is the adapter's view of "turn/completed".
type TurnCompletedNotificationBody struct {
	ThreadID string            `json:"threadId"`
	Turn     TurnCompletedTurn `json:"turn"`
}

// TurnFailedNotificationBody is the adapter's view of "turn/failed".
type TurnFailedNotificationBody struct {
	ThreadID string            `json:"threadId"`
	Turn     TurnCompletedTurn `json:"turn"`
}

// ---------------------------------------------------------------------------
// Item lifecycle notifications
// ---------------------------------------------------------------------------

// ItemStartedNotificationBody is the adapter's view of "item/started". The
// item field is a discriminated union decoded via DecodeThreadItem.
type ItemStartedNotificationBody struct {
	ThreadID string          `json:"threadId"`
	TurnID   string          `json:"turnId"`
	Item     json.RawMessage `json:"item"`
}

// ItemCompletedNotificationBody is the adapter's view of "item/completed".
type ItemCompletedNotificationBody struct {
	ThreadID string          `json:"threadId"`
	TurnID   string          `json:"turnId"`
	Item     json.RawMessage `json:"item"`
}

// ---------------------------------------------------------------------------
// UserInput — discriminated union on "type"
// ---------------------------------------------------------------------------

// UserInputKind enumerates the variants of UserInput the adapter emits. Only
// the "text" variant is wired today; image variants round-trip via Extras.
type UserInputKind string

const (
	// UserInputKindText carries plain text input.
	UserInputKindText UserInputKind = "text"
	// UserInputKindImage carries remote image input.
	UserInputKindImage UserInputKind = "image"
	// UserInputKindLocalImage carries local image input.
	UserInputKindLocalImage UserInputKind = "localImage"
	// UserInputKindSkill carries a skill reference input.
	UserInputKindSkill UserInputKind = "skill"
	// UserInputKindMention carries a structured mention input.
	UserInputKindMention UserInputKind = "mention"
)

// UserInput is the tagged union carried by TurnStartParams.Input.
type UserInput struct {
	Type UserInputKind `json:"type"`

	// Fields by type; absent fields are omitted on the wire.
	Text string `json:"text,omitempty"`
	URL  string `json:"url,omitempty"`
	Path string `json:"path,omitempty"`
	Name string `json:"name,omitempty"`
}

// TextInput is a shorthand constructor for the default input case.
func TextInput(text string) UserInput { return UserInput{Type: UserInputKindText, Text: text} }

// ---------------------------------------------------------------------------
// SandboxPolicy — discriminated union on "type"
// ---------------------------------------------------------------------------

// SandboxPolicyKind lists the sandbox policy variants.
type SandboxPolicyKind string

const (
	// SandboxPolicyKindDangerFull requests unrestricted local execution.
	SandboxPolicyKindDangerFull SandboxPolicyKind = "dangerFullAccess"
	// SandboxPolicyKindReadOnly requests read-only sandboxing.
	SandboxPolicyKindReadOnly SandboxPolicyKind = "readOnly"
	// SandboxPolicyKindExternal delegates sandboxing outside codex.
	SandboxPolicyKindExternal SandboxPolicyKind = "externalSandbox"
	// SandboxPolicyKindWorkspaceWrite allows writes inside the workspace.
	SandboxPolicyKindWorkspaceWrite SandboxPolicyKind = "workspaceWrite"
)

// SandboxPolicy is the sandbox policy override passed on TurnStart. Only the
// variants the codex adapter needs at call sites are modelled; extra fields
// per variant round-trip through Extras.
type SandboxPolicy struct {
	Type          SandboxPolicyKind `json:"type"`
	NetworkAccess *bool             `json:"networkAccess,omitempty"`
	Extras        json.RawMessage   `json:"-"`
}

// ---------------------------------------------------------------------------
// ThreadItem — discriminated union on "type"
// ---------------------------------------------------------------------------

// ThreadItemKind enumerates the ThreadItem variants we model. We cover the
// ones the codex adapter maps into StreamPayload; other variants are
// preserved through the Unknown/Raw path so downstream code can still emit
// them as StreamPayload.Raw.
type ThreadItemKind string

const (
	// ThreadItemAgentMessage is assistant text.
	ThreadItemAgentMessage ThreadItemKind = "agentMessage"
	// ThreadItemReasoning is model reasoning/thinking content.
	ThreadItemReasoning ThreadItemKind = "reasoning"
	// ThreadItemCommandExecution is a shell command execution item.
	ThreadItemCommandExecution ThreadItemKind = "commandExecution"
	// ThreadItemFileChange is a file-change item.
	ThreadItemFileChange ThreadItemKind = "fileChange"
	// ThreadItemMcpToolCall is an MCP tool-call item.
	ThreadItemMcpToolCall ThreadItemKind = "mcpToolCall"
	// ThreadItemWebSearch is a web-search item.
	ThreadItemWebSearch ThreadItemKind = "webSearch"
	// ThreadItemDynamicToolCall is a dynamic tool-call item.
	ThreadItemDynamicToolCall ThreadItemKind = "dynamicToolCall"
	// ThreadItemPlan is a plan update item.
	ThreadItemPlan ThreadItemKind = "plan"
	// ThreadItemUserMessage is a user message item.
	ThreadItemUserMessage ThreadItemKind = "userMessage"
	// ThreadItemImageView is an image-view item.
	ThreadItemImageView ThreadItemKind = "imageView"
	// ThreadItemImageGeneration is an image-generation item.
	ThreadItemImageGeneration ThreadItemKind = "imageGeneration"
	// ThreadItemContextCompaction is a context-compaction item.
	ThreadItemContextCompaction ThreadItemKind = "contextCompaction"
	// ThreadItemCollabAgentToolCall is the camelCase variant used by the
	// app-server protocol for collaborative multi-agent tool calls.
	// Wire shape: {"type":"collabAgentToolCall","tool":"spawnAgent|wait|...","receiverThreadIds":[...]}
	ThreadItemCollabAgentToolCall ThreadItemKind = "collabAgentToolCall"

	// ThreadItemSubAgentActivity is a forward-compat kind for the v2
	// multi-agent lifecycle item. The vendored schema may not include it yet;
	// items of this type are preserved through Raw for future mapping.
	// Do NOT treat as unknown: preserving the Kind prevents it from colliding
	// with the opaque Kind=="" notification path.
	ThreadItemSubAgentActivity ThreadItemKind = "subAgentActivity"

	// ThreadItemUnknown preserves unknown variants through the Raw path.
	ThreadItemUnknown ThreadItemKind = ""
)

// ThreadItem is the discriminated union payload observed on item/started and
// item/completed notifications. The concrete variant is accessible through
// the exported ThreadItem* structs; unrecognised variants retain their
// original JSON in Raw for forward compatibility.
type ThreadItem struct {
	ID   string         `json:"id"`
	Kind ThreadItemKind `json:"-"`

	// Exactly one of the following pointers is set when Kind matches.
	AgentMessage        *ThreadItemAgentMessageBody        `json:"-"`
	Reasoning           *ThreadItemReasoningBody           `json:"-"`
	CommandExecution    *ThreadItemCommandExecutionBody    `json:"-"`
	FileChange          *ThreadItemFileChangeBody          `json:"-"`
	McpToolCall         *ThreadItemMcpToolCallBody         `json:"-"`
	WebSearch           *ThreadItemWebSearchBody           `json:"-"`
	DynamicToolCall     *ThreadItemDynamicToolCallBody     `json:"-"`
	CollabAgentToolCall *ThreadItemCollabAgentToolCallBody `json:"-"`
	SubAgentActivity    *ThreadItemSubAgentActivityBody    `json:"-"`

	// Raw preserves the original JSON representation. It is always non-nil;
	// use it when Kind is unknown or when a caller needs a field this
	// package does not yet model.
	Raw json.RawMessage `json:"-"`
}

// ThreadItemAgentMessageBody is the "agentMessage" variant.
type ThreadItemAgentMessageBody struct {
	Text  string `json:"text"`
	Phase string `json:"phase,omitempty"`
}

// ThreadItemReasoningBody is the "reasoning" variant.
type ThreadItemReasoningBody struct {
	Content []string `json:"content,omitempty"`
	Summary []string `json:"summary,omitempty"`
}

// ThreadItemCommandExecutionBody is the "commandExecution" variant.
type ThreadItemCommandExecutionBody struct {
	Command          string `json:"command"`
	CWD              string `json:"cwd,omitempty"`
	Status           string `json:"status"`
	ExitCode         *int   `json:"exitCode,omitempty"`
	AggregatedOutput string `json:"aggregatedOutput,omitempty"`
	DurationMs       *int64 `json:"durationMs,omitempty"`
	ProcessID        string `json:"processId,omitempty"`
	Source           string `json:"source,omitempty"`
}

// ThreadItemFileChangeBody is the "fileChange" variant.
type ThreadItemFileChangeBody struct {
	Changes []FileChange `json:"changes"`
	Status  string       `json:"status"`
}

// FileChange is a single entry in a fileChange item.
type FileChange struct {
	Path string          `json:"path"`
	Diff string          `json:"diff"`
	Kind json.RawMessage `json:"kind,omitempty"`
}

// ThreadItemMcpToolCallBody is the "mcpToolCall" variant.
type ThreadItemMcpToolCallBody struct {
	Server     string          `json:"server"`
	Tool       string          `json:"tool"`
	Arguments  json.RawMessage `json:"arguments,omitempty"`
	Result     json.RawMessage `json:"result,omitempty"`
	Error      json.RawMessage `json:"error,omitempty"`
	Status     string          `json:"status"`
	DurationMs *int64          `json:"durationMs,omitempty"`
}

// ThreadItemWebSearchBody is the "webSearch" variant.
type ThreadItemWebSearchBody struct {
	Query  string          `json:"query"`
	Action json.RawMessage `json:"action,omitempty"`
}

// ThreadItemDynamicToolCallBody is the "dynamicToolCall" variant.
type ThreadItemDynamicToolCallBody struct {
	Tool       string          `json:"tool"`
	Arguments  json.RawMessage `json:"arguments,omitempty"`
	Status     string          `json:"status"`
	Success    *bool           `json:"success,omitempty"`
	DurationMs *int64          `json:"durationMs,omitempty"`
}

// ThreadItemCollabAgentToolCallBody is the body for "collabAgentToolCall" (app-server
// camelCase) and "collab_tool_call" (exec --json snake_case). Both wire variants
// decode into this struct; field access uses the normalized names below.
type ThreadItemCollabAgentToolCallBody struct {
	// Tool identifies the collaboration action performed by the item.
	// Known values: "spawnAgent", "wait", "sendInput", "resumeAgent", "closeAgent".
	// exec --json only exposes "wait" in current builds.
	Tool string
	// Status is the completion status of the item.
	Status string
	// SenderThreadID is the thread that initiated this collaboration action.
	SenderThreadID string
	// ReceiverThreadIDs holds child thread ids that were spawned or targeted.
	// When non-empty, ReceiverThreadIDs[0] is the stable child thread id usable
	// as SubagentRef.ID and as the argument to thread/resume for follow-child.
	// exec --json typically emits an empty slice; app-server spawnAgent may populate it.
	ReceiverThreadIDs []string
	// AgentsStates preserves the raw agents_states / agentsStates map for
	// bridge / host-level inspection.
	AgentsStates json.RawMessage
}

// ThreadItemSubAgentActivityBody is the forward-compatible subset of the
// multi-agent v2 lifecycle item that is stable enough to correlate.
type ThreadItemSubAgentActivityBody struct {
	AgentThreadID string
	AgentPath     string
	Kind          string
	ToolCallID    string
}

// decodeCollabAgentToolCallBody decodes a raw ThreadItem JSON into a
// ThreadItemCollabAgentToolCallBody. It handles both the camelCase wire shape
// used by the app-server protocol and the snake_case wire shape emitted by
// codex exec --json, normalising them into a single struct.
func decodeCollabAgentToolCallBody(raw json.RawMessage) (*ThreadItemCollabAgentToolCallBody, error) {
	// Decode into a map of raw values so we can probe both naming conventions.
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("appserver: decode collabAgentToolCall: %w", err)
	}
	b := &ThreadItemCollabAgentToolCallBody{}
	jsonStringField(m, &b.Tool, "tool")
	jsonStringField(m, &b.Status, "status")
	// SenderThreadID: camelCase first, then snake_case fallback.
	jsonStringField(m, &b.SenderThreadID, "senderThreadId", "sender_thread_id")
	// ReceiverThreadIDs: camelCase first, then snake_case fallback.
	if v, ok := m["receiverThreadIds"]; ok {
		_ = json.Unmarshal(v, &b.ReceiverThreadIDs)
	} else if v, ok := m["receiver_thread_ids"]; ok {
		_ = json.Unmarshal(v, &b.ReceiverThreadIDs)
	}
	// AgentsStates: camelCase first, then snake_case fallback.
	if v, ok := m["agentsStates"]; ok {
		b.AgentsStates = append(json.RawMessage(nil), v...)
	} else if v, ok := m["agents_states"]; ok {
		b.AgentsStates = append(json.RawMessage(nil), v...)
	}
	return b, nil
}

func decodeSubAgentActivityBody(raw json.RawMessage) (*ThreadItemSubAgentActivityBody, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("appserver: decode subAgentActivity: %w", err)
	}
	b := &ThreadItemSubAgentActivityBody{}
	jsonStringField(m, &b.AgentThreadID, "agentThreadId", "agent_thread_id")
	jsonStringField(m, &b.AgentPath, "agentPath", "agent_path")
	jsonStringField(m, &b.Kind, "kind", "status")
	jsonStringField(m, &b.ToolCallID, "toolCallId", "tool_call_id", "parentToolCallId", "parent_tool_call_id")
	return b, nil
}

// jsonStringField sets *dst from the first non-empty string value found
// under any of the given keys in m.
func jsonStringField(m map[string]json.RawMessage, dst *string, keys ...string) {
	for _, key := range keys {
		v, ok := m[key]
		if !ok {
			continue
		}
		var s string
		if err := json.Unmarshal(v, &s); err == nil && s != "" {
			*dst = s
			return
		}
	}
}

// DecodeThreadItem parses a ThreadItem from its raw JSON, dispatching on the
// "type" discriminator. Unknown types are returned with Kind == ThreadItemUnknown
// and Raw populated; this lets translate.go emit them as StreamPayload.Raw
// without failing the whole run.
func DecodeThreadItem(raw json.RawMessage) (*ThreadItem, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("appserver: empty thread item payload")
	}
	var head struct {
		ID   string         `json:"id"`
		Type ThreadItemKind `json:"type"`
	}
	if err := json.Unmarshal(raw, &head); err != nil {
		return nil, fmt.Errorf("appserver: decode thread item head: %w", err)
	}
	item := &ThreadItem{
		ID:   head.ID,
		Kind: head.Type,
		Raw:  append(json.RawMessage(nil), raw...),
	}
	switch head.Type {
	case ThreadItemAgentMessage:
		var body ThreadItemAgentMessageBody
		if err := json.Unmarshal(raw, &body); err != nil {
			return nil, fmt.Errorf("appserver: decode agentMessage item: %w", err)
		}
		item.AgentMessage = &body
	case ThreadItemReasoning:
		var body ThreadItemReasoningBody
		if err := json.Unmarshal(raw, &body); err != nil {
			return nil, fmt.Errorf("appserver: decode reasoning item: %w", err)
		}
		item.Reasoning = &body
	case ThreadItemCommandExecution:
		var body ThreadItemCommandExecutionBody
		if err := json.Unmarshal(raw, &body); err != nil {
			return nil, fmt.Errorf("appserver: decode commandExecution item: %w", err)
		}
		item.CommandExecution = &body
	case ThreadItemFileChange:
		var body ThreadItemFileChangeBody
		if err := json.Unmarshal(raw, &body); err != nil {
			return nil, fmt.Errorf("appserver: decode fileChange item: %w", err)
		}
		item.FileChange = &body
	case ThreadItemMcpToolCall:
		var body ThreadItemMcpToolCallBody
		if err := json.Unmarshal(raw, &body); err != nil {
			return nil, fmt.Errorf("appserver: decode mcpToolCall item: %w", err)
		}
		item.McpToolCall = &body
	case ThreadItemWebSearch:
		var body ThreadItemWebSearchBody
		if err := json.Unmarshal(raw, &body); err != nil {
			return nil, fmt.Errorf("appserver: decode webSearch item: %w", err)
		}
		item.WebSearch = &body
	case ThreadItemDynamicToolCall:
		var body ThreadItemDynamicToolCallBody
		if err := json.Unmarshal(raw, &body); err != nil {
			return nil, fmt.Errorf("appserver: decode dynamicToolCall item: %w", err)
		}
		item.DynamicToolCall = &body
	case ThreadItemCollabAgentToolCall, "collab_tool_call":
		// Both the app-server camelCase "collabAgentToolCall" and the exec --json
		// snake_case "collab_tool_call" wire variants decode into the same body.
		// Normalise Kind to ThreadItemCollabAgentToolCall regardless of wire format.
		body, err := decodeCollabAgentToolCallBody(raw)
		if err != nil {
			return nil, fmt.Errorf("appserver: decode collabAgentToolCall item: %w", err)
		}
		item.Kind = ThreadItemCollabAgentToolCall
		item.CollabAgentToolCall = body
	case ThreadItemSubAgentActivity:
		body, err := decodeSubAgentActivityBody(raw)
		if err != nil {
			return nil, err
		}
		item.Kind = ThreadItemSubAgentActivity
		item.SubAgentActivity = body
	default:
		item.Kind = ThreadItemUnknown
	}
	return item, nil
}
