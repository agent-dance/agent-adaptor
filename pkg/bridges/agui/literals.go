package agui

// This file centralises AG-UI protocol literal string values the bridge
// emits. Sending a literal that does not match the TypeScript Zod schemas
// makes the CopilotKit / AG-UI client reject the stream with
// `invalid_literal` before any of our events are delivered to the UI.
//
// The Go SDK's New*Event constructors accept arbitrary `string` arguments
// and the Go `Validate()` methods only enforce non-emptiness; the wire
// format's literal constraints are defined on the TypeScript side in
//   sdks/typescript/packages/core/src/events.ts
//   sdks/typescript/packages/core/src/types.ts
// and are enforced by CopilotKit at decode time.
//
// Every constant here mirrors a `z.literal(...)` / `z.enum([...])` value
// on the wire. When upgrading AG-UI, audit both TS files and update this
// single file accordingly.

// Role literals accepted by AG-UI messages. These come from the message
// role discriminated union. The only values we actively emit today are
// ReasoningRole (for REASONING_MESSAGE_START) and ToolRole (for the
// TOOL_CALL_RESULT role, already baked into NewToolCallResultEvent inside
// the Go SDK).
const (
	// RoleAssistant is the role for plain assistant messages. AG-UI
	// TEXT_MESSAGE_START accepts any role listed here, but our bridge
	// does not pass a role today (the Go SDK defaults it appropriately).
	RoleAssistant = "assistant"
	// RoleUser is the role echoed when surfacing user messages to the UI.
	RoleUser = "user"
	// RoleSystem is the role for system messages (unused by this bridge).
	RoleSystem = "system"
	// RoleDeveloper is the role for developer messages (unused today).
	RoleDeveloper = "developer"
	// RoleTool is the role for tool result messages. The Go SDK's
	// NewToolCallResultEvent already fills this literal internally, so
	// the bridge never has to pass it explicitly; the constant exists so
	// future callers can refer to it without string duplication.
	RoleTool = "tool"
	// RoleActivity is the role for activity updates.
	RoleActivity = "activity"
	// ReasoningRole is the required literal for REASONING_MESSAGE_START.
	// CopilotKit validates this with z.literal("reasoning") and refuses
	// the stream otherwise—sending "assistant" is the intuitive mistake
	// the bridge must never make again. This constant exists purely to
	// stop that regression at compile-time (via refactoring reviews).
	ReasoningRole = "reasoning"
)

// Default fallback identifiers used when the adapter does not provide
// thread / run ids before we have to emit RUN_STARTED. AG-UI accepts any
// string here; we pick stable sentinels so hosts can recognise them in
// logs and filter them out when correlating with their own id space.
const (
	fallbackThreadID = "agent-adaptor-unknown-thread"
	fallbackRunID    = "agent-adaptor-unknown-run"
)
