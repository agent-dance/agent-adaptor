// Package team composes host-selected local and Remote A2A members with a
// delegation-first leader through the public adaptor.Runner contract. Core
// continues to own Agent execution, message delivery and approval policy.
//
// The leader receives four capability-scoped MCP tools: delegate, send, status
// and cancel. Local workers receive send and read-only status. Host input goes
// to the leader; workers ask the leader, which relays to other workers. Roster
// data is injected as a preamble without changing Thread fingerprints.
//
// A Team opens one authenticated loopback MCP listener. Per-execution tokens
// bind scope, actor, task, run and tools, and are revoked on detach. Close stops
// the listener, executions and standbys without closing host-owned Agents.
//
// Send uses an anonymous private memory scope. Thread uses an opaque host key
// and Config.Store, which defaults to MemoryStore. Task-mode Thread.Send on an
// active execution or live Standby reports adaptor.ErrThreadBusy through the
// returned Stream. Supplements use Stream.Send; ongoing input uses
// Thread.Standby and Standby.Send. Standby has no overall Result; its lifetime
// is owned by the first context, and Cancel leaves it usable for later input.
//
// Agents and nested Teams get an isolated Thread for each PerTask task so
// explicit continue can reuse a healthy checkpoint. Other Runners use Send;
// continuation requires ConversationProvider. PerScope conversations serialize
// writers even when MaxActive is greater than one. EncodeKey derives internal
// keys with collision-free length prefixes; host keys have no special delimiter.
//
// Worker terminal notifications and explicit messages wake an idle leader
// using WakeWindow, or supplement a busy leader with Soon. Progress notes
// wait for the next wake. Local worker approvals share their original responder
// and carry adaptor.Origin; the member's own approval policy remains in force.
// One execution ends only after the leader and workers settle and wake-worthy
// input is drained. Its final Done agrees with Stream.Result.
//
// Results compose every leader execution's audit layers in order. Usage also
// includes observed worker usage; Subagents returns task summaries. Full local
// results and normalized RemoteSnapshot artifacts remain in TaskStore. The
// remaining MaxLeaderTurns caps core continuation without replacing policy.
// Worker activity also carries roster display names, live text, final text and
// decoded output for presentation bridges. Its result projection excludes raw
// streams, transcripts, service reports and runtime metadata.
//
// Durable TaskStore implementations must pass teamstoretest and preserve
// fenced scope leases, CAS tasks, atomic terminal/wake commits, inbox claims
// and immutable artifacts. Recovery marks nonterminal tasks interrupted; it
// does not restart workers. Hosts resubmit unconfirmed messages with original
// IDs. Messages requiring process-local execution options stay deferred until
// those options are restored by resubmission. Provider acceptance before a
// crash can precede receipt persistence, so replay remains possible. Durable
// Agent Thread stores are separately required for conversation continuity.
//
// Fork requires idle source/target scopes and copies terminal history and
// artifacts with durable lineage. Leader and supported local PerScope
// conversations fork lazily on first child execution; the parent's checkpoint
// is resolved then. PerTask conversation links and the inbox are not copied.
//
// RemoteSpec uses clients/a2a discovery and origin-restricted authentication.
// Remote supplements support Soon only. Input-required is lifecycle state
// governed by remote policy, not a locally answerable approval. Remote output
// schemas are rejected, and PerScope Remote members make Fork unsupported.
// Two minutes without observation fails the task with unknown_state; ambiguous
// starting requests are never replayed automatically. Remote members do not
// receive this process's private MCP tool grants.
package team
