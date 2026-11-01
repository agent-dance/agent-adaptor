# Teams

Import `github.com/agent-dance/agent-adaptor/v2/hosttools/team`. A Team is an
optional host component implementing `adaptor.Runner`; core does not choose
members or business roles. `team.New` opens one private loopback MCP listener
for the Team. `Close(ctx)` cancels executions and releases the listener; the
host still owns its Agents.

Configure a leader Agent and `Member{Key, Runner}` or `Member{Key, Remote}`.
Exactly one execution target is required. Local members can be Agents or
nested Teams. Member descriptions and skills form the roster in the leader's
preamble; they do not change Thread fingerprints. Four tools are available to
the leader, regardless of roster size: delegate, send, status and cancel.
Workers receive only send and status. Capability tokens determine the sender.

`Send(ctx, text)` creates an anonymous memory-only scope. `Thread(key).Send`
retains the opaque host key and rejects concurrent task or standby use with
`adaptor.ErrThreadBusy`. Use the execution's Stream.Send for supplements, or
open `Thread(key).Standby(lifetime)` and call Standby.Send for ongoing input.
Standby.Cancel cancels current processing and queued messages while leaving
standby available. Caller message IDs, authors and timing survive routing and
queued cancellation. Execution options queued in standby apply to their own
next execution.

The leader delegates without waiting in a tool call. Worker completion,
questions and host messages join a busy leader or wake an idle one. Progress
notes wait for the next wake. Team completion requires leader and workers to
be idle with no wake-worthy inbox messages. Result.Text is the last leader
text; Usage sums leader and worker usage. Raw stdout/stderr, Transcript,
Services and Messages include every leader execution in order; message turn numbers
follow the composite leader turn sequence. Raw.Terminal
retains the last observed terminal payload, matching core's single terminal
slot. Subagents includes each current task's final summary and usage; full
local worker results and normalized RemoteSnapshot artifacts are available from TaskStore. Result and Done agree.

The team passes the remaining MaxLeaderTurns into the leader's core message
budget before each leaf execution. Reaching a single Agent's turn cap ends
normally and returns pending messages with MessageLimitExceeded. A team that
must abort workers because of a limit returns RunError with
ReasonLimitExceeded and its partial Result.

TaskStore owns scope leases, CAS task revisions, atomic task/wake commits,
claim tokens, inbox ordering and immutable artifacts. Implementations must
pass `teamstoretest.Run`. Scope owners and claim tokens fence stale writers.
Expired delivery attempts can be reclaimed and carry MaybeDuplicate. Host
messages are persisted before provider admission using the exact caller ID;
already delivered IDs deduplicate and failed IDs can be resubmitted. Deferred
standby messages become eligible on dispatch. Messages without execution options
can also become eligible on owner recovery; records requiring process-local
execution options stay deferred until the host explicitly resubmits them.
Execution-level CallOptions are process-local values; hosts must reconstruct
those options when restoring standby after a restart. Durable message records
preserve ID, text, author and timing, rather than serializing Go closures.

A restarted named scope marks every nonterminal task interrupted before the
first leader invocation and includes those records in the preamble. The
leader decides whether to delegate again. Local Agent Thread stores must also
be durable for provider conversation continuity; TaskStore does not replace
them. Standby is explicitly reopened after a restart.

`Thread.Fork(newKey)` requires idle source and target scopes and no live
standby. It copies terminal tasks and artifacts atomically and stores durable
lineage. Leader and PerScope conversations use core's lazy validated Fork on
first execution; unused member conversations start fresh. Parent history stays
unchanged. As with core Fork, the parent's checkpoint is resolved when the
child first executes. PerTask task contexts are not copied, so copied PerTask
records cannot be used to write back into the parent with continue.

Remote members use clients/a2a for discovery, authentication origin checks and
protocol negotiation. RemotePolicy retains timeout, streaming requirement,
input-required, polling and artifact limits. AgentCard optionally pins the
observed fingerprint; discovery still requires AgentCardURL. PerScope reuses
an observed remote context; explicit continue requires one. A2A has no portable
conversation Fork, so a team with a PerScope remote rejects Fork explicitly.
Remote output schema negotiation is unavailable and Member.Output is rejected
at construction for remote members. The remote applies its own output policy.

A remote team_send uses the same task ID and reports rejection as a tool error.
It supports Soon; other timing requests fail with ErrMessageUnsupported rather
than changing timing silently. Disconnection is observed for at most two
minutes before a failed unknown_state task wakes the leader. Starting requests
are never replayed automatically. Streaming progress and polling observations
are forwarded as SubagentUpdate events.

Portable A2A input-required exposes no actionable approval-response contract.
It is reported as waiting_approval and the remote's own policy applies when
AllowInputRequired permits waiting. The current client has no negotiated
approval-response extension, so lifecycle-only HITL payloads cannot be turned
into bound ApprovalRequests. Local workers forward bound ApprovalRequests with
Origin and keep their own approval policy.

`bridges/subagentstream.Project` maps typed worker events to wire envelopes.
`Merge` remains available for hosts with an external typed worker source;
Teams already include worker events and do not need another merge. Hosts close
the external source after worker completion; Merge preserves the parent's Done
as the final event and never invents task failures or approval policy.
