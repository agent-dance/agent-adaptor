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
// Results include every leader execution's audit layers in order. Usage also
// includes all workers; Subagents contains their task summaries. Worker full
// results remain available in TaskStore.
package team
