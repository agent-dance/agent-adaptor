# Teams

`hosttools/team` is an optional host component that implements `adaptor.Runner`.
The host chooses the leader, roster, workspaces and workflow. Core still owns
message delivery, Thread coordination, policy and each Agent execution. A Team
adds delegation and combines its leader and workers into one Event stream.

## Construct a Team

This example uses local Codex and Claude Agents. Run it from a repository with
both CLIs installed and authenticated. The leader coordinates in a read-only
workspace; the implementer can write there. The host owns all three lifetimes.

```go
package main

import (
	"context"
	"fmt"
	"log"
	"time"

	adaptor "github.com/agent-dance/agent-adaptor/v2"
	"github.com/agent-dance/agent-adaptor/v2/claude"
	"github.com/agent-dance/agent-adaptor/v2/codex"
	"github.com/agent-dance/agent-adaptor/v2/hosttools/team"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
	defer cancel()
	leader := adaptor.New(codex.Driver(codex.Config{}),
		adaptor.WithWorkspace("."), adaptor.WithPolicy(adaptor.PolicyReadOnly))
	worker := adaptor.New(claude.Driver(claude.Config{}),
		adaptor.WithWorkspace("."), adaptor.WithPolicy(adaptor.PolicyWorkspaceWrite))
	defer leader.Close(context.Background())
	defer worker.Close(context.Background())

	t, err := team.New(team.Config{
		Leader: leader,
		Members: []team.Member{{
			Key: "impl", Name: "Implementer", Description: "Implement and test Go changes",
			Runner: worker, Continuity: team.PerScope, Timeout: 20 * time.Minute,
		}},
	})
	if err != nil {
		log.Print(err)
		return
	}
	defer t.Close(context.Background())
	result, err := t.Thread("issue-123").Send(ctx, "Implement the change in TASK.md").Result()
	if err != nil {
		log.Print(err)
		return
	}
	fmt.Println(result.Text)
	for _, task := range result.Subagents() {
		fmt.Printf("%s %s: %s\n", task.Member, task.State, task.Summary)
	}
}
```

`team.New` validates configuration and opens one private loopback MCP listener.
`Team.Close(ctx)` is an idempotent host-resource cleanup operation: it rejects
new work, cancels executions and Standbys, and stops the listener. It does not
close the host's Agents or nested Teams. `Stream.Cancel()` cancels one team
execution, including its queued and running tasks.

Each member needs a unique, nonempty `Key` and exactly one of `Runner` or
`Remote`. `Name`, `Description` and `Skills` become roster data in the leader's
preamble. `Skills` describes expertise; it does not install skills. The roster
preamble does not change Thread fingerprints. Configure real skills, sandbox,
MCP and workspace on each Agent. Shared workspace write conflicts remain a
host/leader responsibility; separate workspaces when needed.

Local Agents and nested Teams provide conversations directly. A custom Runner
can implement `team.ConversationProvider` to return `adaptor.ThreadRunner`.
A plain Runner works for isolated tasks but cannot support explicit continuation
without a conversation provider. Use a distinct `Surface.Prefix`, such as
`inner_`, for nested teams that also receive their parent's tools.

`Member.Input` is a JSON Schema validated before task registration; invalid
input returns `invalid_member_input` with the schema. `Member.Output` supplies
a structured-output schema to local member executions. Schemas must be JSON
serializable; external schema references are disabled.

## Local and Remote continuity

| Setting | Conversation used | Concurrency |
| --- | --- | --- |
| `PerTask` (default) | Agents/nested Teams get an isolated Thread per task; plain Runners use their own `Send` | Bounded by member and team limits |
| `PerScope` | One member conversation shared within the named team scope | One writer per conversation, even with `MaxActive > 1` |
| `continue: task` | A new task handle reuses that finished task's conversation | Requires the same member and a healthy local checkpoint or observed Remote context |

Task history is append-only from the leader's perspective: continuing work
creates another task instead of overwriting the previous result. `team_send`
rejects finished tasks with `task_finished`, and queued tasks with `task_queued`.
Use `team_delegate` with `continue` after completion.

Team keys are opaque and preserved verbatim. Internal keys use the length-prefix
encoding `team.EncodeKey`: `(scope, "leader")`, `(scope, "member", memberKey)`
for PerScope, and `(scope, "task", taskID)` for PerTask.

A Remote member is host-curated A2A configuration:

```go
func RemoteReviewer(cardURL string) team.Member {
	return team.Member{
		Key: "review", Description: "Review repository changes",
		Continuity: team.PerScope,
		Remote: &team.RemoteSpec{
			AgentCardURL: cardURL,
			Auth:         clienta2a.BearerTokenFromEnv("REVIEW_TOKEN"),
			Policy: team.RemotePolicy{
				MaxTimeout:       10 * time.Minute,
				RequireStreaming: true,
			},
		},
	}
}
```

Import `clienta2a "github.com/agent-dance/agent-adaptor/v2/clients/a2a"` for this
example. `AgentCardURL` is always required; an optional `AgentCard.Fingerprint`
pins discovery. Auth, tenant, trusted origins, accepted output modes, transport
preferences and HTTP client belong to `RemoteSpec`, never model tool arguments.
Remote PerScope reuses the last observed A2A context; explicit `continue`
requires an observed context ID.

Remote limits are deliberate:

- Supplemental `team_send` supports `soon` only. `interrupt` and `after_turn`
  fail with `ErrMessageUnsupported`; rejected same-task A2A messages are tool errors.
- No portable approval-response extension is negotiated. `input-required`
  reports `waiting_approval`; with `AllowInputRequired: true` the runtime polls
  while the remote's own policy resolves it. The default rejects waiting and
  fails the task. No bound local `ApprovalRequest` is fabricated.
- A2A exposes no conversation Fork. A Team with any PerScope Remote member
  rejects Fork. PerTask Remote history can be copied, but cannot be continued
  from the branch into the parent's context.
- Output-schema negotiation is unavailable: `Member.Output` on a Remote member
  is rejected by `team.New`. Accepted media types are not schema constraints.
- After two minutes without successful observation (including a silent stream),
  the task fails with `unknown_state` and wakes the leader. Starting requests
  are never automatically replayed after an ambiguous failure.

`RemotePolicy` defaults to one-second polling, 1,800 observation calls and a
1 MiB limit on each JSON-encoded artifact. `MaxTimeout` can shorten the member
timeout. Streaming is preferred when advertised; `RequireStreaming` rejects a
nonstreaming endpoint. See [A2A](a2a.md) for client, bridge and exposure details.
Remote members are not given this process's loopback MCP tools; their own
service controls its tool and approval surfaces.

## Task mode and Standby

`t.Send(ctx, text)` starts an anonymous scope with private memory storage, even
when `Config.Store` is durable. `t.Thread(key).Send(ctx, text)` starts a bounded
team execution in a named scope. Its context bounds the whole team execution.
The execution ends after leader finalization, workers and queued tasks finish,
and no wake-worthy inbox messages or deliveries remain. `Done{Result, Err}` is
its last event and agrees with `Stream.Result()`.

A task-mode `Thread.Send` never joins existing work. If that scope has an active
execution, live Standby (even idle), or another process's valid scope lease,
the returned failed Stream reports `adaptor.ErrThreadBusy` through `Done.Err`
and `Result()`. `team.ErrThreadBusy` is the same error. Supplements go through
the existing `Stream.Send`; ongoing conversations use `Standby.Send`.

`Team.Thread(key).Standby(lifetime)` gets or creates the process-local Standby.
The first context owns its lifetime; the Standby holds the scope lease while
idle. It has `Send`, `Events` and `Cancel`, with no overall `Result` or `RunID`.
Each leader turn emits `TurnEnd`, and each bounded team execution emits `Done`.
Subscribe once and broadcast to multiple pages through a host-owned hub.
Events before the first `Events()` subscription are not buffered.

`Standby.Send` accepts call options. Message-only input can join the current
team execution; input with execution options waits for its own execution.
`Standby.Cancel()` cancels current processing and queued messages while keeping
the Standby usable. End the lifetime context to leave Standby.

## Leader, tools and wakes

The injected briefing requires **delegation first**: understand, decompose,
delegate, steer, accept, integrate and answer as leader. Work covered by a
member's description or skills must be delegated. The leader can do work no
member can take. This is a model instruction; a read-only leader sandbox can
enforce a stricter coordinator-only setup.

Delegation returns a registered task handle immediately. The leader ends its
turn when it needs worker results and is woken later; it never blocks in a
wait tool. Host messages always target the leader. The leader relays to workers;
workers can message only the leader, with no worker-to-worker addressing.

| Tool | Leader arguments and behavior | Local worker surface |
| --- | --- | --- |
| `team_delegate` | `{member, objective, input?, continue?}` → `{task, state:"queued"}` | Unavailable through the outer team |
| `team_send` | `{task, text, when?}` supplements a running task; `when` is `soon` (default), `interrupt` or `after_turn`, subject to capability | `{text, expect_reply?}` sends to leader; no author/target field |
| `team_status` | `{tasks?}` returns roster, active counts and task states/summaries/reasons | Read-only access to the same scope |
| `team_cancel` | `{task, reason}` cancels one task | Unavailable |

There are four team tools for the leader and two for each local worker,
independent of roster size; member-owned tools stay in member executions.
`Surface.Prefix` replaces `team_`. `expect_reply` is a hint in the worker
schema; every explicit worker send wakes the leader and returns admission,
without synchronously waiting for an answer.

Task completion/failure, worker questions or messages, and host messages wake
the leader. A leader-initiated task cancellation does not wake it again.
Worker notifications supplement a busy leader with `Soon`, at its earliest
supported boundary. There is no universal tool-boundary latency guarantee:
the Driver may deliver immediately, at a tool/stop boundary, or via turn-end
continuation. Idle-leader worker notifications coalesce within `WakeWindow`
(default two seconds); host input can start the idle leader immediately.

Worker text progress emits task-bound notes. Notes alone do not wake the
leader; the next wake carries them with other notifications. A task's terminal
notification normally provides that wake. Notes left unread after a
leader-initiated cancellation appear in that task's `Unread` summary.

## Limits, timeouts and retries

Zero fields select defaults; negative values are rejected.

| Setting | Default | Scope |
| --- | --- | --- |
| `Limits.MaxLeaderTurns` | 50 | Provider turns across all leader executions in one team execution |
| `Limits.MaxTasks` | 20 | New task registrations in one team execution |
| `Limits.MaxActiveTasks` | 4 | Concurrent workers; excess tasks queue |
| `Limits.MaxAgentMessages` | 10 | Explicit team sends per leader turn |
| `Limits.MaxSenderMessages` | 3 | Explicit team sends per sender per leader turn |
| `Member.MaxActive` | 1 | Concurrent tasks for that member, also serialized by conversation |
| `Member.Timeout` | 30 minutes | Each started worker task; no infinite timeout |
| `Limits.WakeWindow` | 2 seconds | Idle-leader notification coalescing |
| `Limits.RetryBackoff` | 100 milliseconds | Minimum leader retry delay |

Approaching a limit emits `Notice{Kind:"limit.approaching"}` and, while budget
remains, a system wrap-up notification. At the hard limit the runtime stops
further admissions and cancels unfinished tasks after leader finalization.
When work must be aborted it returns an error matching `team.ErrLimitExceeded`
and a `RunError{Reason: ReasonLimitExceeded}` with the partial Result. A clean
finish at the cap can succeed.

Before each leader execution, Team applies `RunSettings.CapMessageTurns` after
caller options to lower the core message-turn budget to the remaining team
budget. It preserves other policy fields and cannot raise a stricter caller
cap. Core's default per-execution message budget is 20 turns. Hitting that leaf
cap ends the leaf execution and fails pending messages with
`MessageLimitExceeded`; the Team can still coordinate within its remaining
budget. `MaxLeaderTurns` does not bound the duration of a single turn; set
leader timeout policy and the team execution context accordingly.

A leader failure is retried at most twice when its error implements
`team.RetryableError` and `RetryAfter() >= 0`; cancellation/deadline errors are
not retried. Workers continue during retry backoff. Exhausted or nonretryable
leader failures cancel workers and return the available partial Result.
Team does not add an automatic worker liveness reminder; worker timeouts and
any host watchdog provide that bound.

Standby remains available after an execution failure. Its separate retry
requires `RetryableError` and undelivered input, and happens at most once.
Carried input otherwise waits for the next host message. An ID can report
`failed` with `run_failed` and later `delivered` after carry/resubmission;
acceptance alone is not a delivery receipt.

## Approvals and events

A local worker's bound `*adaptor.ApprovalRequest` is forwarded on the same
team stream with `Origin{Member, Task}`. The host uses the existing
`Approve(ctx)`, `Deny(ctx, reason)` or `Answer(ctx, choice)` methods; forwarded
and original requests share one exactly-once responder. The task reports
`waiting_approval` while waiting. The member's `OnApproval` and
`Policy.Approvals` control response, timeout and fallback; leader options do not
replace worker policy. With synchronous-only result consumption the member's
no-consumer fallback applies. Team adds no approval policy, and the leader
cannot approve on behalf of the host. Task questions for the leader use
`team_send`; native Question approvals still ask the host/user.

A host event loop can keep UI callbacks and the terminal result together:

```go
func Execute(ctx context.Context, t *team.Team, objective string,
	ask func(*adaptor.ApprovalRequest), publish func(adaptor.Event),
) (*adaptor.Result, error) {
	s := t.Send(ctx, objective)
	defer s.Cancel()
	for event := range s.Events() {
		switch e := event.(type) {
		case *adaptor.ApprovalRequest:
			// Display e.Origin().Member and e.Origin().Task; ask responds to e.
			ask(e)
		case adaptor.Done:
			return e.Result, e.Err
		default:
			publish(event)
		}
	}
	return s.Result()
}
```

Leader events keep their semantic types. Worker activity uses typed
`SubagentUpdate{Task, Member, Kind, State, Delta, Summary, Data}`: queued state,
started, delta, peer message, state changes and finished. States are `queued`,
`running`, `waiting_approval`, `succeeded`, `failed`, `cancelled`, `interrupted`.
Remote protocol observations are state updates, not invented token deltas.
Worker tool/thinking/turn events are not all forwarded as top-level leader
events; use `Task.Result` for full local worker audit data.

Team assigns one RunID and receive-order sequence to the execution stream;
source coordinates are retained where supplied. Incremental events may be
dropped under backpressure with a counted `Dropped` marker; approval and
terminal events are retained. `bridges/subagentstream.Project` projects these
existing typed updates to wire envelopes. Teams need no `Merge`; it is for
hosts adding an external typed worker source, whose channel must close after
its workers finish. The parent's `Done` remains last.

## Result composition

Composite authors can read caller IDs, authors and timing with
`adaptor.MessageAttributes(opts...)` without applying execution options or
generating an ID. Team uses `adaptor.ComposeResults(runID, parts...)` for leader
executions in order. `Text`, `Summary`, `Model`, `Provider`, metadata and structured output
come from the last leader result. Raw stdout/stderr, Transcript, Services and
message outcomes concatenate; message turn numbers are rebased across leader
executions. `Raw().Terminal` retains the last observed provider terminal payload
because RawStreams has one terminal slot. `Turns` counts leader provider turns.

`Usage` includes observed leader and worker usage without counting worker usage
twice. Unobserved usage stays absent; Remote protocol data does not fabricate
local CLI accounting. `Subagents()` returns detached task summaries with
`Member`, `Task`, `State`, `Summary`, `Usage` and `Unread`. Full worker results
are retained in `TaskStore.Tasks(ctx, scope)` as `Task.Result`; Remote task IDs,
context IDs and normalized non-text artifacts are retained in
`RemoteTask`, `RemoteContext`, and `RemoteSnapshot`. They are not appended to
leader raw output. A worker failure can be handled by the leader and still
produce a successful team result; inspect task summaries for worker outcomes.

## Persistence, recovery and Fork

`Config.Store` defaults to `team.NewMemoryStore()`, which retains records until
discarded and provides no crash durability or automatic retention limit.
Supply a durable `team.TaskStore` for named scopes that must survive restart.
It must pass `hosttools/team/teamstoretest.Run` and transactionally preserve:

- exclusive scope leases fenced by owner and token, CAS task revisions;
- atomic terminal-task plus wake-message commits;
- inbox order and `pending → delivering → delivered/failed`, exclusive claim
  tokens, expired-claim recovery and deduplication;
- detached task/result/Remote snapshots, immutable artifacts and Fork lineage.

TaskStore is separate from Agent Thread storage. Persist the leader and local
members' Thread stores as well to retain provider conversations. Recreate the
Team with the same configuration and explicitly reopen Standby after restart.
Once the old lease expires, the next named execution marks every nonterminal
task `interrupted` with reason `execution_lost` before invoking the leader.
The preamble includes interrupted tasks; the leader decides whether to delegate
again. Recovery does not silently restart workers or remote tasks.

Host messages are persisted before leader admission with the exact supplied
`MessageID`, author and timing. Resubmit with the original ID and original
content/author/timing: delivered IDs deduplicate; identical failed IDs can be
readmitted; conflicting content for an existing ID is rejected. Unconfirmed
claims can be retried with `MaybeDuplicate`. A crash after provider acceptance
but before receipt persistence can replay a message; this is not an exactly-once
provider execution guarantee.

Queued Standby input with execution options has `RequiresOptions` and is held
`Deferred` after owner loss. Go call options/closures are process-local and
are not serialized. Restore the original options, such as `WithModel`, and
resubmit under the original ID through the reopened Standby; dispatch then
activates the held record. Option-free input can become eligible after lease
recovery, but opening Standby alone does not execute it: host input must start
an execution. Keep original IDs and reconstructable options in host storage.

`branch, err := t.Thread(parentKey).Fork(newKey)` requires both scopes idle,
including no live Standby, a healthy parent leader checkpoint, and an unused
target. Busy scopes return `ErrThreadBusy`; target history/lineage or a target
leader checkpoint yields a conflict. Terminal tasks and immutable artifacts
copy atomically; the inbox does not. The parent remains unchanged.

Fork stores lineage first. Leader and supported local PerScope conversations
branch lazily on their first child execution, using core's compatibility and
provider checks. The parent's checkpoint is resolved then, not frozen at the
Fork call; provider/configuration failures can therefore surface later. An
unused member conversation starts fresh. Agent and nested Team PerScope members
are supported; an arbitrary conversation provider may reject lazy Fork.
PerTask conversation links are cleared in copied records, so `continue` cannot
write back into the parent's task contexts. Any PerScope Remote member makes
Fork unsupported.

## Multica-style host walkthroughs

The following functions accept already configured Teams and host callbacks;
combine them with construction above. They make no assumptions about issue
storage, HTTP routing, UI framework or provider version tables.

### Comments, corrections and receipts (M1, M3, M4)

A host with its own task scheduler can use the same message contract without
Team's MCP surface. Feed claimed comments to a running Stream, record admission
errors immediately, and record delivery from `MessageUpdate`. If the execution
finished, the host schedules a fresh named task rather than treating acceptance
as delivery. Use `AuthorAgent` for peer corrections and `AuthorSystem` for
CI/watchdog input; only real user input has human authority.

```go
type Comment struct {
	ID, Text string
	From     adaptor.Author
}

func RunIssue(ctx context.Context, t *team.Team, key, objective string,
	comments <-chan Comment, publish func(adaptor.Event),
	receipt func(string, bool, string),
) (*adaptor.Result, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	s := t.Thread(key).Send(ctx, objective)
	defer s.Cancel()
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case c, ok := <-comments:
				if !ok {
					return
				}
				err := s.Send(ctx, c.Text, adaptor.MessageID(c.ID), adaptor.From(c.From))
				if err != nil {
					receipt(c.ID, false, err.Error()) // Host retains/requeues the comment.
				}
			}
		}
	}()
	for event := range s.Events() {
		if e, ok := event.(adaptor.MessageUpdate); ok && e.State != adaptor.MessageAccepted {
			receipt(e.ID, e.State == adaptor.MessageDelivered, string(e.Reason))
		}
		publish(event) // Includes worker approvals for the host UI to answer.
	}
	return s.Result()
}
```

The callbacks above must be safe for concurrent use. Leader-to-worker correction
inside Team uses `team_send` with a task handle; worker-to-leader sends are
coalesced/supplemented by the runtime. Multica can instead keep its own blackboard,
permissions, claim/ack and scheduling, using Agent Streams directly.

### Web chat or IM with Standby (M2, M5)

Use a server lifetime for Standby and a request context for each admission.
An IM host can reply on `TurnEnd` and use its `MessageIDs` for correlation.
A UI stop button calls `sb.Cancel()`; closing the conversation cancels its
lifetime. This example stops when the inbound channel closes.

```go
type ChatMessage struct {
	ID, Text, UserID, Model string
}

func ServeChat(ctx context.Context, t *team.Team, key string,
	incoming <-chan ChatMessage, emit func(adaptor.Event),
) error {
	lifetime, stop := context.WithCancel(ctx)
	sb, err := t.Thread(key).Standby(lifetime)
	if err != nil {
		stop()
		return err
	}
	drained := make(chan struct{})
	events := sb.Events()
	go func() {
		defer close(drained)
		for event := range events {
			emit(event)
		}
	}()
	defer func() { stop(); <-drained }()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case m, ok := <-incoming:
			if !ok {
				return nil
			}
			opts := []adaptor.CallOption{
				adaptor.MessageID(m.ID),
				adaptor.From(adaptor.Author{Kind: adaptor.AuthorHuman, ID: m.UserID}),
			}
			if m.Model != "" {
				opts = append(opts, adaptor.WithModel(m.Model))
			}
			if err := sb.Send(ctx, m.Text, opts...); err != nil {
				return err
			}
		}
	}
}
```

### Verify, follow up and restart (M6–M8)

Keep a named Thread for follow-up attempts; each has its own Result. The
verification callback belongs to the host and supplies the actual test report.
Import `fmt` for the exhausted-verification error.

```go
func ImplementAndVerify(ctx context.Context, t *team.Team, key, objective string,
	verify func(context.Context) (report string, passed bool),
) (*adaptor.Result, error) {
	th := t.Thread(key)
	result, err := th.Send(ctx, objective).Result()
	for attempt := 0; err == nil; attempt++ {
		report, passed := verify(ctx)
		if passed {
			return result, nil
		}
		if attempt == 3 {
			return result, fmt.Errorf("verification failed after three follow-ups: %s", report)
		}
		result, err = th.Send(ctx, "Tests failed; delegate fixes and verify again:\n"+report).Result()
	}
	return result, err
}
```

Inspect each Driver's actual message capability; no common version table
promises identical delivery boundaries. A driver with in-run continuation but
no resume can fail a message claimed in the final-boundary race; it reports the
failure instead of silently losing it. Cursor in-run delivery also requires
the host binary's hook-relay entry point; see [streaming](streaming.md).

On daemon restart, reopen the named scope with durable stores and resubmit
unconfirmed host messages under their original IDs and options as described
above. For long-running tasks, host watchdogs can send a system wrap-up reminder
before cancelling; Team itself enforces task timeouts and turn/message caps.
For a live local plan → implement → review workflow, see the
[showcase](../examples/showcases/team-agent-workflow).

## Security boundary

Each Team owns one IPv4 loopback-only MCP listener, not one sidecar per task.
Per-execution signed bearer capability tokens bind scope, actor, task, run,
visible tools and reachable targets. They are injected through secret
environment bindings and revoked on detach. Sender identity comes from the
token; strict tool schemas reject attempts to add a forged `from` or target.
Workers cannot delegate/cancel through the outer Team or access another scope.
Tokens never belong in result metadata, service reports or logs.

Peer/system messages are framed as input without human authority. Inline
notification/send content over 64 KiB and merged notification batches over
256 KiB are preserved as immutable scope-isolated MCP resources with references,
not silently truncated. Hosts can also read artifacts from TaskStore.
Remote discovery/auth retains client origin restrictions; remote policy and
network exposure remain host responsibilities. The Team listener is private
transport for capabilities, not a general HTTP server or automatic agent router.
