// Inbound AG-UI contract.
//
// The types and helpers in this file let sse.Handler accept the
// canonical AG-UI HTTP request shape (RunAgentInput) without the host
// having to write a decoder.
//
// v1 coverage (deliberately narrow):
//
//   - prompt extraction: latest role=user message, text content only
//   - session binding: threadId → ("agui", threadId)
//
// v1 silently drops (tracked in docs/workstream-streaming-chat.md §18):
//
//   - image / audio / file / document content parts
//   - tools[] (frontend tool declarations)
//   - assistant tool_calls + role=tool results
//   - state, context, forwardedProps
//
// The JSON-level round trip preserves every field (no UnmarshalJSON
// validation that rejects extensions), but the bridge does not forward
// them to the adapter. Callers must not read RunAgentInput.Tools /
// State / Context / ForwardedProps outside this package until §18.2
// lands the unified pass-through — otherwise the bridge grows
// inconsistent read paths.

package agui

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// RunAgentInput is the canonical HTTP body an AG-UI client (CopilotKit
// HttpAgent, @ag-ui/client, AG-UI Dojo, etc.) sends when it invokes an
// agent. See https://docs.ag-ui.com/sdk/js/core/types#runagentinput for
// the authoritative TypeScript definition.
//
// Only the fields agent-adaptor currently consumes are typed. Every
// other field is captured as json.RawMessage so protocol extensions pass
// through untouched — the bridge promises to round-trip unknown payload
// slices rather than reject or lose them.
type RunAgentInput struct {
	// ThreadID identifies the conversation thread. agent-adaptor maps it
	// into the SDK session namespace so multi-turn conversations reuse
	// the same underlying adapter session.
	ThreadID string `json:"threadId"`

	// RunID uniquely identifies this invocation. We do not currently
	// correlate it with SDK RunHandle.RunID() (they live in independent
	// scopes), but the field is accepted and surfaced.
	RunID string `json:"runId"`

	// Messages is the ordered chat history. The latest user-role entry
	// becomes the adapter prompt; earlier messages are ignored because
	// adapters carry their own transcript via the session store.
	Messages []Message `json:"messages"`

	// State / Tools / Context / ForwardedProps are reserved AG-UI fields
	// that the bridge does not interpret but preserves verbatim so
	// downstream instrumentation can still see them.
	State          json.RawMessage   `json:"state,omitempty"`
	Tools          []json.RawMessage `json:"tools,omitempty"`
	Context        []json.RawMessage `json:"context,omitempty"`
	ForwardedProps json.RawMessage   `json:"forwardedProps,omitempty"`
}

// Message is one entry in RunAgentInput.Messages. Content is kept raw
// because the AG-UI spec allows either a plain string or a
// polymorphic parts array, and we decode the two shapes lazily.
type Message struct {
	ID      string          `json:"id"`
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
	Name    string          `json:"name,omitempty"`
}

// DecodeHTTPRequest parses an incoming AG-UI HTTP POST body into a
// RunAgentInput. It fails when the body cannot be read or is not valid
// JSON. It does not validate semantic constraints (presence of
// messages, role values, etc.) — those are the caller's responsibility.
//
// The request body is closed before returning so the caller does not
// have to.
func DecodeHTTPRequest(r *http.Request) (*RunAgentInput, error) {
	if r == nil || r.Body == nil {
		return nil, errors.New("agui: nil http.Request")
	}
	defer r.Body.Close()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, fmt.Errorf("agui: read body: %w", err)
	}
	if len(body) == 0 {
		return nil, errors.New("agui: empty request body")
	}
	var input RunAgentInput
	if err := json.Unmarshal(body, &input); err != nil {
		return nil, fmt.Errorf("agui: decode RunAgentInput: %w", err)
	}
	return &input, nil
}

// LastUserText returns the flattened text of the most recent message
// whose role is "user". It returns an empty string if no such message
// exists or its content is structurally empty.
//
// This is the single adapter-facing entrypoint for extracting the
// prompt from an AG-UI input: AG-UI clients (including CopilotKit) send
// the full chat history on every turn, but agent-adaptor's session
// store already carries the transcript inside the adapter, so only the
// newest user turn becomes the prompt.
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

// SessionKey derives the SDK SessionKey (namespace, key) pair for this
// input. The convention is "agui/<threadId>" so AG-UI threads collide
// with each other but not with other namespaces a host may use.
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
// adapter layer is text-only for v1. When both decode attempts fail the
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
