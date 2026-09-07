// Package agui translates adaptor requests, events, and approvals to and from
// the AG-UI protocol. It consumes the public Runner, Stream, Event, and Result
// contracts and does not create a separate execution path. If a Driver emits
// no assistant text delta, Events preserves a non-empty final Result.Text as
// one complete assistant message immediately before the terminal event.
// SubagentUpdate lifecycles become AG-UI Activity messages with activityType
// "subagent", allowing clients to render live delegated work outside the
// parent assistant transcript.
// Each Subagent activity snapshot and delta owns its tool list and nested
// Args, Result and Error JSON containers. Later translation or CloseResult
// does not mutate delivered events, so consumers may retain or serialize
// them while translation continues. Consumer edits do not change the tracker.
// Concrete JSON container types, numbers and nil/empty values are preserved.
// CapabilityInvocation and TodoUpdated become CUSTOM events named
// adapter.capability.invocation and adapter.todo.updated. Their values retain
// the authoritative meta, nested source chain, parent coordinates and full
// snake_case observation payload. Empty todo items are an explicit clear.
// AG-UI tool IDs use aa1: plus the base64url-encoded JSON tuple
// ["tool", runID, scopeID, ID]; adapter.tool.parent precedes each tool start
// because the native AG-UI tool shape has no scoped parent field. Consumers
// must use that custom value to reconstruct parent graphs.
// Approval CUSTOM events own independent choice and nested detail snapshots;
// retaining or editing a translated event does not alter the live request.
// RunError.Reason remains authoritative when Cause includes cancellation.
package agui
