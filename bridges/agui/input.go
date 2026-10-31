package agui

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/bridges/internal/bridgekey"
)

// maxHTTPRequestBytes bounds the canonical AG-UI request before it is held in
// memory. Four MiB leaves room for normal multi-turn chat state while keeping
// an unauthenticated bridge endpoint from becoming an unbounded allocation
// primitive.
const maxHTTPRequestBytes int64 = 4 << 20

// RunAgentInput is the canonical HTTP body an AG-UI client (CopilotKit
// HttpAgent, @ag-ui/client, AG-UI Dojo, etc.) sends when it invokes an
// agent. See https://docs.ag-ui.com/sdk/js/core/types#runagentinput for
// the authoritative TypeScript definition.
//
// Known members are validated against AG-UI 1.0. Unknown members are
// retained in UnknownMembers by JSON Pointer and stripped from execution.
type RunAgentInput struct {
	// ProtocolVersion declares the client protocol version when present.
	ProtocolVersion string `json:"protocolVersion,omitempty"`
	// ParentRunID identifies the run that spawned this request.
	ParentRunID string `json:"parentRunId,omitempty"`
	// Resume contains interrupt answers; non-empty input is rejected for execution.
	Resume []ResumeEntry `json:"resume,omitempty"`
	// UnknownMembers retains stripped members by their original JSON Pointer.
	UnknownMembers map[string]json.RawMessage `json:"-"`
	// Diagnostics contains request-wide validation warnings.
	Diagnostics []Diagnostic `json:"-"`
	// partDiagnostics retains original part paths before unsupported parts are removed.
	partDiagnostics []Diagnostic

	// ThreadID identifies the conversation thread. The bridge encodes the
	// AG-UI identity into one collision-free adaptor Thread key.
	ThreadID string `json:"threadId"`

	// RunID is the AG-UI invocation identity. It is accepted and surfaced but
	// remains independent from the adaptor Stream RunID.
	RunID string `json:"runId"`

	// Messages is the complete ordered history. PrepareInput enforces trailing
	// user selection and an explicit checkpoint/history-fallback policy.
	Messages []Message `json:"messages"`

	// State preserves AG-UI state without interpreting it.
	State json.RawMessage `json:"state,omitempty"`
	// Tools contains validated frontend declarations; execution strips them with a warning.
	Tools []Tool `json:"tools,omitempty"`
	// Context contains ambient entries appended deterministically by PrepareInput.
	Context []Context `json:"context,omitempty"`
	// ForwardedProps preserves application-defined forwarded properties.
	ForwardedProps json.RawMessage `json:"forwardedProps,omitempty"`
}

// Message is one entry in RunAgentInput.Messages. Content is kept raw
// because the AG-UI spec allows either a plain string or a
// polymorphic parts array (or an object for activity messages). Known variants
// are schema-validated at the HTTP boundary.
type Message struct {
	// ToolCalls contains historical function invocations made by an assistant.
	ToolCalls []ToolCall `json:"toolCalls,omitempty"`
	// ToolCallID identifies the invocation answered by a tool message.
	ToolCallID string `json:"toolCallId,omitempty"`
	// Error describes a tool failure alongside any partial content.
	Error string `json:"error,omitempty"`
	// EncryptedValue preserves an opaque provider artefact.
	EncryptedValue string `json:"encryptedValue,omitempty"`
	// ActivityType identifies the kind of an activity message.
	ActivityType string `json:"activityType,omitempty"`
	// SubagentRunID attributes the message to a delegated invocation.
	SubagentRunID string `json:"subagentRunId,omitempty"`
	// Metadata carries application-defined message information.
	Metadata map[string]any `json:"metadata,omitempty"`
	// ID is the client-supplied message identifier.
	ID string `json:"id"`
	// Role identifies the message author.
	Role string `json:"role"`
	// Content preserves an AG-UI string, parts array, or activity object.
	Content json.RawMessage `json:"content"`
	// Name optionally identifies the message author or tool.
	Name string `json:"name,omitempty"`
}

// DecodeHTTPRequest parses an incoming AG-UI HTTP POST body into a
// RunAgentInput. It fails when the body cannot be read or is not valid
// JSON or a known member violates the AG-UI 1.0 schema. Bodies are limited
// to 4 MiB; an oversized body returns an error that
// wraps *http.MaxBytesError so an HTTP adapter can return status 413. It does
// validate schema constraints; PrepareInput additionally checks execution policy.
//
// The request body is closed before returning so the caller does not
// have to.
func DecodeHTTPRequest(r *http.Request) (*RunAgentInput, error) {
	if r == nil || r.Body == nil {
		return nil, inputError("agui.invalid_input", "", "nil http.Request")
	}
	limited := http.MaxBytesReader(nil, r.Body, maxHTTPRequestBytes)
	defer limited.Close()
	body, err := io.ReadAll(limited)
	if err != nil {
		return nil, fmt.Errorf("agui: read body: %w", err)
	}
	if len(body) == 0 {
		return nil, inputError("agui.invalid_json", "", "empty request body")
	}
	var input RunAgentInput
	if err := decodeInput(body, &input); err != nil {
		return nil, fmt.Errorf("agui: decode RunAgentInput: %w", err)
	}
	return &input, nil
}

// LastUserText returns the latest non-empty user text, scanning older messages.
//
// Deprecated: use PrepareInput for execution; this historical helper does not
// enforce trailing-user or prior-history policy and must not select a prompt.
func (in *RunAgentInput) LastUserText() string {
	if in == nil {
		return ""
	}
	for i := len(in.Messages) - 1; i >= 0; i-- {
		m := in.Messages[i]
		if m.Role != "user" {
			continue
		}
		if text := contentAsString(m.Content); text != "" {
			return text
		}
	}
	return ""
}

// LastUserMessageID returns the AG-UI message id of the most recent
// non-empty user text message. It returns an empty string when the client did
// not supply an id or there is no usable user message. Use UserTurnEvents
// when a deterministic fallback id is required.
func (in *RunAgentInput) LastUserMessageID() string {
	_, id, text := lastUserMessageWithID(in)
	if text == "" {
		return ""
	}
	return id
}

// UserTurnEvents emits a well-formed user TextDelta start/content/end
// lifecycle for the latest
// non-empty user message, preserving the client message id or synthesizing a
// deterministic id from runID and the message index.
//
// EventMeta carries the caller-provided run id and the collision-free AG-UI
// thread tuple. The three events share one observation time; their sequence
// remains zero because only a live Agent sink may assign authoritative run
// ordering.
func (in *RunAgentInput) UserTurnEvents(runID string) []adaptor.Event {
	idx, messageID, text := lastUserMessageWithID(in)
	if text == "" {
		return nil
	}
	if messageID == "" {
		messageID = synthesizeUserMessageID(runID, idx)
	}
	meta := adaptor.EventMeta{RunID: runID, Time: time.Now()}
	if in.ThreadID != "" {
		meta.ThreadKey = bridgekey.Encode("agui", in.ThreadID)
	}
	return []adaptor.Event{
		adaptor.WithEventMeta(adaptor.TextDelta{MessageID: messageID, Role: adaptor.RoleUser, Phase: adaptor.PhaseStart}, meta),
		adaptor.WithEventMeta(adaptor.TextDelta{MessageID: messageID, Text: text, Role: adaptor.RoleUser, Phase: adaptor.PhaseContent}, meta),
		adaptor.WithEventMeta(adaptor.TextDelta{MessageID: messageID, Role: adaptor.RoleUser, Phase: adaptor.PhaseEnd}, meta),
	}
}

// lastUserMessageWithID is the ID-aware variant of LastUserText. It
// returns the index, ID and flattened text of the most recent user
// message with non-empty text content; if none is found it returns
// (-1, "", "").
func lastUserMessageWithID(in *RunAgentInput) (int, string, string) {
	if in == nil {
		return -1, "", ""
	}
	for i := len(in.Messages) - 1; i >= 0; i-- {
		m := in.Messages[i]
		if m.Role != "user" {
			continue
		}
		if text := contentAsString(m.Content); text != "" {
			return i, m.ID, text
		}
	}
	return -1, "", ""
}

// synthesizeUserMessageID builds a stable fallback MessageID when the
// AG-UI client did not supply Message.ID. The shape is
// "user-<runID>-<index>" so the same input deterministically yields the
// same id within one request.
func synthesizeUserMessageID(runID string, idx int) string {
	if runID == "" {
		return fmt.Sprintf("user-msg-%d", idx)
	}
	return fmt.Sprintf("user-%s-%d", runID, idx)
}

// SessionKey derives the AG-UI session identity tuple for this input. The
// SSE bridge passes the two values through its
// collision-free tuple encoder rather than joining them with a delimiter.
//
// When ThreadID is empty the function returns ("", "") — callers must
// treat that as "no session binding" and run the adapter stateless.
func (in *RunAgentInput) SessionKey() (namespace, key string) {
	if in == nil || in.ThreadID == "" {
		return "", ""
	}
	return "agui", in.ThreadID
}

// contentAsString flattens one Message.Content into plain text. It
// accepts both AG-UI content shapes:
//
//   - a bare JSON string, e.g. "hello"
//   - a parts array, e.g. [{"type":"text","text":"hello"}, ...]
//
// Non-text parts (image / audio / tool) are silently skipped; the
// Driver-facing prompt projection is text-only. When both decode attempts fail the
// function returns "" rather than panicking, on the assumption that an
// unknown content shape is still better treated as empty than as a
// fatal error here.
func contentAsString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var asString string
	if err := json.Unmarshal(raw, &asString); err == nil {
		return asString
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &parts); err == nil {
		var b strings.Builder
		for _, p := range parts {
			if p.Type != "text" || p.Text == "" {
				continue
			}
			if b.Len() > 0 {
				b.WriteString("\n")
			}
			b.WriteString(p.Text)
		}
		return b.String()
	}
	return ""
}
