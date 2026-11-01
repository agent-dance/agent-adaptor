// Package team composes local Runners into an event-driven team. The leader
// coordinates work through four capability-scoped MCP tools; workers receive
// only messaging and read-only status tools. A Team owns its listener and
// executions, but does not own or close the Agents supplied by the host.
//
// Named Threads retain task history in TaskStore. Anonymous Send executions
// always use a private in-memory store. Thread keys are encoded by EncodeKey;
// no delimiter in a host key has special meaning.
//
// Agents and nested teams use an isolated Thread for each PerTask task so
// explicit continue can retain the original context. Other Runners use Send;
// continue is rejected unless they implement ConversationProvider. PerScope
// conversations serialize writers even when MaxActive is greater than one.
// Durable task storage does not replace the host's Agent Thread stores.
//
// Text, Usage, Turns and Subagents describe the complete composite execution.
// The root Result API does not expose an audit/message composition hook:
// opaque Raw, Transcript, Services and Messages layers retain the final leader
// execution's values. Preserve TurnEnd results and TaskStore task results for
// the other audits. A team.audit_scope notice makes this boundary explicit.
package team
