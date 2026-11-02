# A2A bridge, client, and Remote members

`agent-adaptor` keeps A2A at the host integration boundary:

- `bridges/a2a` publishes any `adaptor.Runner` as an A2A agent.
- `clients/a2a` calls remote A2A agents through stable, protocol-shaped DTOs.
- `hosttools/team` composes a leader with curated local and remote members.

Core execution remains protocol-independent. There is no A2A Driver, automatic remote-agent routing, built-in production server, or A2A-specific execution entry point.

## Dependency choice

The bridge and client use the official `github.com/a2aproject/a2a-go/v2` SDK for Agent Card conversion, JSON-RPC/SSE transport, task events, protocol errors, and request handling.

- Reliability: protocol parsing and transport behavior stay with the official implementation instead of a parallel handwritten stack.
- Maintainability: the dependency has upstream documentation, versioning, issue tracking, and protocol evolution.
- Localization: only `bridges/a2a` and `clients/a2a` import the A2A SDK. Core, Drivers, and unrelated bridges do not.

## Publishing a Runner

`a2a.NewServer` accepts the same `adaptor.Runner` implemented by `*adaptor.Agent`, `*adaptor.Thread`, and host decorators. The bridge starts an execution through `Runner.Send`, consumes its one typed `Event` channel, and obtains the authoritative terminal `Result` and error from `Stream.Result()`. It does not dispatch a Driver directly or reproduce option merging, Thread coordination, error policy, or result construction.

```go
func Publish(agent *adaptor.Agent) *http.ServeMux {
	server := bridgea2a.NewServer(agent, bridgea2a.ServerOptions{
		AgentCard: bridgea2a.AgentCard{
			Name: "Local coding agent", Description: "Runs coding tasks",
			Version: "2.0.0", URL: "https://host.example/a2a",
			Skills: []bridgea2a.Skill{{
				ID: "coding", Name: "Coding", Description: "Implement repository changes",
				Tags: []string{"coding"},
			}},
		},
		Session: bridgea2a.ThreadByContextID(),
		Options: []adaptor.CallOption{adaptor.WithPolicy(adaptor.PolicyReadOnly)},
		TaskLifecycle: bridgea2a.TaskLifecycleOptions{
			Ephemeral: &bridgea2a.EphemeralTaskStoreOptions{
				MaxTasks: 512, TTL: 2 * time.Hour,
			},
		},
	})
	mux := http.NewServeMux()
	mux.Handle("/.well-known/agent-card.json", server.AgentCardHandler())
	mux.Handle("/a2a", server.Handler())
	return mux
}
```

Use imports `adaptor "github.com/agent-dance/agent-adaptor/v2"`,
`bridgea2a "github.com/agent-dance/agent-adaptor/v2/bridges/a2a"`, `net/http`
and `time` for this example. Hosts own route layout, authentication, authorization, TLS, tenancy, rate limiting, observability, and durable task storage. `Server` only supplies mountable handlers.

### Session binding

`ServerOptions.Session` has two final modes:

- `a2a.Stateless()` invokes the configured Runner as-is and adds no context-based binding. It is the default; a preselected Thread retains that Thread's own continuity.
- `a2a.ThreadByContextID()` maps a non-empty A2A `contextID` to a collision-free adaptor Thread key. Follow-up requests with the same `contextID` continue the same conversation.

`ThreadByContextID` requires the base Runner to be an `*adaptor.Agent`; Agents
use a default memory Thread store unless the host injects another store. A
missing `contextID` invokes the base Runner as-is. Passing an already selected
`*adaptor.Thread` or `*team.Thread` with `Stateless` invokes that conversation.
`ThreadByContextID` does not select Team conversations automatically; publish a
selected team Thread or provide host routing around separate handlers.

### Follow-up messages and busy conversations

Sequential requests with the same bound context use the same Thread. A message
with an active taskId routes to that task's Stream.Send rather than starting a
second execution. The wire message ID becomes MessageID; user/agent roles map to
human/agent authors, and a remote sender cannot claim host-system authority.
A supplied contextId must agree with the task. Supplements may carry only
message options; execution options cannot reconfigure active work.

A request with a busy contextId but no taskId returns an explicit invalid-request
error with `code: "task_in_progress"` and the activeTaskId. A terminal task returns
`task_finished`; other rejected input returns `message_rejected` with task/message
IDs. The bridge never silently creates replacement work for a finished task.

Before dispatching a supplement, the router checks the request context, then
explicitly passes context.WithoutCancel(ctx) to Stream.Send. Once dispatch begins,
HTTP disconnection cannot withdraw that input, and a queue-space wait can continue
until capacity is available or the execution ends/cancels. A successful response
acknowledges admission, not delivery: MessageUpdate receipts remain on the original
task stream. Streaming sends subscribe before admission so fast receipts cannot
outrun their subscription. If the network leaves admission uncertain, retry with
the same MessageID. This protocol lifetime is chosen by the A2A router; native
Stream.Send retains its cancellation-until-Claim contract.

### Prompt and call options

The default prompt is the last non-blank text part of the inbound A2A message. Set `ServerOptions.Prompt` to a `PromptBuilder` when the host needs domain-specific projection of data, files, or multiple message parts. `ServerOptions.Options` supplies the shared call options; the builder returns the prompt plus request-specific `[]adaptor.CallOption`, which are applied afterward.

Both sources remain call-scoped. They do not create a second set of Agent defaults.

### Event mapping and the intentional wire schema

The bridge maps adaptor events to A2A task updates in one order:

1. submitted and working task status;
2. one working `TaskStatusUpdateEvent` per exposed adaptor `Event`;
3. terminal result artifacts when allowed by exposure policy and the outcome;
4. exactly one completed, failed, or canceled terminal outcome.

Intermediate typed events use DataParts with schema `adapter.stream.v1`. The exported names `AdapterStreamSchemaV1`, `AdapterStreamEnvelopeV1`, `AdapterStreamEventV1`, and `DecodeAdapterEventV1` intentionally keep the `V1` suffix: they version an interoperable wire format, not a temporary Go API. Encoding remains an internal bridge responsibility. The server advertises `urn:agent-adaptor:stream:v1` in its Agent Card, and each DataPart is self-describing.

The wire envelope preserves adaptor `EventMeta`, opt-in provider source
coordinates, complete tool snapshots, approval lifecycle fields and detailed
dropped-event markers for the supported event kinds. MessageUpdate and TurnEnd
use `message.update` and `turn.end` DataParts. The final protocol status carries
one `done` DataPart after terminal artifacts, derived from the authoritative
Result/error; lifecycle snapshots expose only safe fields unless diagnostics
are enabled. Process output, ordinary notices and SubagentUpdate remain local,
so a published Team does not expose its complete worker event stream over this
wire schema. Use the local Event stream or AG-UI for task/member updates.
Required `message.update`, `turn.end` and `done` envelopes have no bridge size
cap, on either encoding or decoding. Their complete text remains inline in the
same DataPart, subject to the usual exposure policy and secret redaction. Hosts
needing input size limits must reject before admission; a wire limit must not
discard an already-admitted outcome. Optional frames retain the 64 KiB cap and
produce explicit dropped markers when oversized or unencodable.
Unknown DataPart schemas
remain protocol data; the bridge does not guess provider semantics.

`IncludeHITL` exposes lifecycle data only. `DecodeAdapterEventV1` cannot recreate
a remote responder: decoded approval fields are not an approval-response
transport. This bridge/client pair negotiates no portable extension for answering
approvals; the serving Agent's callback/policy must handle them.

`DecodeAdapterEventV1` preserves opaque payload numbers as `json.Number`.
Approval attempts and dropped-event counts/sequences accept exact integral
decimal/exponent forms and ignore fractions or overflow. Latency conversion
accepts recorded nanoseconds and decoded milliseconds with range checks before
multiplication. Outbound DataParts retain the interoperable JSON safe-integer
limit, checked exactly before converting integer decimal/exponent forms.

### Result and exposure

Successful `Stream.Result()` values produce the completed status and terminal artifacts. `*adaptor.RunError` produces a failed A2A task with the available partial Result; cancellation produces a canceled task. Infrastructure errors are mapped to failed terminal status. The bridge never treats a non-nil execution error as success.

The default `agent-adaptor-result` artifact contains only the safe summary. `ExposurePolicy` must explicitly opt in to reasoning, tool calls, HITL, metadata, usage, provider terminal payload, transcript, or raw streams. Enabled diagnostics are sanitized before leaving the bridge.

`ServerOptions.ResultBuilder` may append or replace terminal artifacts and override completed status text. It runs only after successful adaptor execution and cannot rewrite already emitted intermediate events.

Task retention is explicit. With no custom `TaskLifecycle.Store`, the bridge uses a bounded in-memory store (default 256 tasks, one-hour TTL). Configure `TaskLifecycle.Ephemeral` to change those bounds or inject an upstream `taskstore.Store` for durable or cross-process retention.

Capability advertisement is strict:

- `Capabilities.Streaming` is tri-state; the zero value enables A2A streaming, while `CapabilityDisabled` advertises `false`.
- push notifications require both card advertisement and `ServerOptions.PushNotifications` with a config store and sender.
- an extended Agent Card requires both card advertisement and `ServerOptions.ExtendedAgentCard` with a static card or provider.

Construction-time mismatches panic because they are host programming errors, not per-run failures.

## Calling a remote A2A agent

`clients/a2a` intentionally returns A2A tasks, messages, artifacts, and events. It does not pretend that a remote protocol task has local CLI stdout/stderr or an adaptor `Result`.

```go
func Review(ctx context.Context) (clienta2a.Task, error) {
	client := clienta2a.New(clienta2a.Options{
		AgentCardURL: "https://remote.example/.well-known/agent-card.json",
		Auth:         clienta2a.BearerTokenFromEnv("REMOTE_A2A_TOKEN"),
	})
	defer client.Close()
	card, err := client.AgentCard(ctx)
	if err != nil {
		return clienta2a.Task{}, err
	}
	return client.Send(ctx, clienta2a.SendRequest{
		Message: clienta2a.Message{
			ID: "review-123", Role: "user",
			Parts: []clienta2a.Part{{
				Kind: clienta2a.PartText, Text: "Review this change", MediaType: "text/plain",
			}},
		},
		AcceptedOutputModes: card.DefaultOutputModes,
	})
}
```

Import `context` and `clienta2a "github.com/agent-dance/agent-adaptor/v2/clients/a2a"`.
`SendStream` and `Subscribe` return ordered protocol events. A message, final task/status, or `TASK_STATE_INPUT_REQUIRED` completes the stream. Duplicate or late events after the first terminal event are ignored. If transport fails after a task ID is known, the client attempts one `GetTask` recovery and marks a recovered terminal event with `RecoveredState`.

`GetTaskRequest` and `CancelTaskRequest` carry a tenant selector; cancellation also accepts operation metadata:

```go
func CancelReview(ctx context.Context, client *clienta2a.Client) error {
	task, err := client.GetTask(ctx, clienta2a.GetTaskRequest{
		TaskID: "task-123", Tenant: "tenant-a",
	})
	if err != nil {
		return err
	}
	if task.Status.State.Terminal() {
		return nil
	}
	_, err = client.CancelTask(ctx, clienta2a.CancelTaskRequest{
		TaskID: task.ID, Tenant: "tenant-a",
		Metadata: map[string]any{"reason": "parent_cancelled"},
	})
	return err
}
```

Outbound DataPart and metadata values must encode as non-null JSON. Integer-valued numbers must remain within the interoperable IEEE-754 safe range; encode larger IDs and counters as strings. Invalid values fail before sending, and unsafe inbound DataParts return protocol errors rather than silently rounding.

Credentials are origin-pinned. Auth is sent to the Agent Card origin by default; cross-origin interfaces require `TrustedAuthOrigins`, and redirects to untrusted origins lose authorization headers. `SubscribeRequest.Since` is rejected because A2A 1.0 has no cursor replay field.

See [`examples/a2a-server`](../examples/a2a-server) for an end-to-end server and client using the final API.

## Curated local and Remote teams

`hosttools/team` composes host-selected members with a delegation-first leader.
The leader gets four tools (`team_delegate`, `team_send`, `team_status`,
`team_cancel`); local workers get send/status. User and worker messages go
through the leader. A Remote member uses `clients/a2a` for discovery,
authentication and transport negotiation:

```go
func NewReviewTeam(leader, planner *adaptor.Agent, reviewCardURL string) (*team.Team, error) {
	return team.New(team.Config{
		Leader: leader,
		Members: []team.Member{
			{Key: "plan", Description: "Plan implementation", Runner: planner},
			{Key: "review", Description: "Review changes", Remote: &team.RemoteSpec{
				AgentCardURL: reviewCardURL,
				Auth:         clienta2a.BearerTokenFromEnv("REVIEW_TOKEN"),
				Policy:       team.RemotePolicy{MaxTimeout: 2 * time.Minute},
			}},
		},
	})
}
```

Import `team "github.com/agent-dance/agent-adaptor/v2/hosttools/team"` as well.
The caller closes the Team during host shutdown. Local tool delivery uses one
capability-scoped loopback listener; Remote members are not given that listener's
tools. See the complete [team guide](team.md) for construction, task mode,
Standby, limits, receipts, persistence/recovery and Fork.

### Remote protocol limits

| Concern | Implemented contract |
| --- | --- |
| Running-task input | `team_send` sends to the same A2A task/context; success requires acceptance for that task. Rejection is a tool error. |
| Supplemental timing | `Soon` only; `Interrupt` and `AfterTurn` return `ErrMessageUnsupported`. There is no negotiated timing extension. |
| Approvals | Portable `input-required` has no actionable approval-response extension. It reports `waiting_approval`; `AllowInputRequired` permits polling while the remote policy resolves it. Default policy fails instead of waiting. |
| Continuity | PerScope uses the last observed remote context; explicit `continue` requires one. Context IDs remain internal task details. |
| Fork | A2A has no portable conversation Fork. Any PerScope Remote member makes Team Fork fail; copied PerTask history cannot continue the parent's context. |
| Output schema | No negotiation; Remote `Member.Output` is rejected at construction. Output MIME modes do not constrain JSON shape. |
| Observation loss | Two minutes without successful observation, including a silent stream, fails the task with `unknown_state` and wakes the leader. Initial requests are not replayed after ambiguous failure. |

`RemotePolicy` also controls maximum timeout, streaming requirements, polling
(default one second, at most 1,800 calls) and maximum JSON-encoded artifact
size (default 1 MiB each). Successful protocol completion alone succeeds the
remote task; input-required, rejected, failed or canceled tasks do not.

Streaming/poll observations become typed `SubagentUpdate` events in the local
Team stream. `Task.RemoteSnapshot` preserves the normalized task, non-text
artifacts and protocol data; Remote task/context IDs are retained alongside it.
A Remote's adaptor Result is a local projection of observed text/status, not
local CLI Raw/Transcript or invented token accounting. The leader's Result
contains worker summaries and observed usage; full snapshots stay in TaskStore.

## Security boundaries

- Drivers remain unaware of A2A and delegation.
- The bridge and Team never bypass `Runner` to dispatch a Driver.
- Remote protocol dumps are not appended to a leader's raw stdout/stderr.
- Remote URL artifacts remain references; the runtime does not automatically fetch their content.
- Bearer tokens do not enter runtime-service reports, run metadata, or serialized MCP declarations.
- Hosts remain responsible for network exposure, authentication, authorization, tenant policy, and durable storage.

## Non-goals

Core does not provide automatic remote-agent discovery, A2A-to-Driver routing, team role configuration, business workflow planning, production push infrastructure, built-in tenant/auth policy, or durable task persistence. These remain explicit host responsibilities composed above the six core nouns: Agent, Thread, Stream, Event, Result, and Driver.
