# A2A bridge, client, and delegation

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

`a2a.NewServer` accepts the same `adaptor.Runner` implemented by `*adaptor.Agent`, `*adaptor.Thread`, and host decorators. The bridge always executes through `Runner.Stream`, consumes the one typed `Event` channel, and obtains the terminal `Result` from `Stream.Result`. It does not dispatch a Driver directly or reproduce option merging, Thread coordination, error policy, or result construction.

```go
agent := adaptor.New(
	configuredDriver,
	adaptor.WithThreadStore(memory.NewStore()),
)

server := bridgea2a.NewServer(agent, bridgea2a.ServerOptions{
	AgentCard: bridgea2a.AgentCard{
		Name:        "Local coding agent",
		Description: "Runs coding tasks through agent-adaptor",
		Version:     "1.0.0",
		URL:         "https://host.example/a2a",
		Skills: []bridgea2a.Skill{{
			ID:          "coding",
			Name:        "Coding",
			Description: "Implement and review repository changes",
			Tags:        []string{"coding", "repository"},
		}},
	},
	Session: bridgea2a.ThreadByContextID(),
	Options: []adaptor.CallOption{
		adaptor.WithPolicy(nonInteractivePolicy),
	},
	TaskLifecycle: bridgea2a.TaskLifecycleOptions{
		Ephemeral: &bridgea2a.EphemeralTaskStoreOptions{
			MaxTasks: 512,
			TTL:      2 * time.Hour,
		},
	},
})

mux := http.NewServeMux()
mux.Handle("/.well-known/agent-card.json", server.AgentCardHandler())
mux.Handle("/a2a", server.Handler())
```

Hosts own route layout, authentication, authorization, TLS, tenancy, rate limiting, observability, and durable task storage. `Server` only supplies mountable handlers.

### Session binding

`ServerOptions.Session` has two final modes:

- `a2a.Stateless()` executes each request on the configured Runner without cross-request memory. It is the default.
- `a2a.ThreadByContextID()` maps a non-empty A2A `contextID` to a collision-free adaptor Thread key. Follow-up requests with the same `contextID` continue the same conversation.

`ThreadByContextID` requires the configured Runner to be an `*adaptor.Agent` with `adaptor.WithThreadStore(...)`; only an Agent can create Threads. A missing `contextID` runs stateless. Passing an already selected `*adaptor.Thread` is valid with `Stateless`, because the bridge simply invokes that Runner.

### Prompt and call options

The default prompt is the last non-blank text part of the inbound A2A message. Set `ServerOptions.Prompt` to a `PromptBuilder` when the host needs domain-specific projection of data, files, or multiple message parts. `ServerOptions.Options` supplies the shared call options; the builder returns the prompt plus request-specific `[]adaptor.CallOption`, which are applied afterward.

Both sources remain call-scoped. They do not create a second set of Agent defaults.

### Event mapping and the intentional wire schema

The bridge maps adaptor events to A2A task updates in one order:

1. submitted and working task status;
2. one working `TaskStatusUpdateEvent` per exposed adaptor `Event`;
3. exactly one completed, failed, or canceled terminal outcome;
4. terminal result artifacts when allowed by the exposure policy.

Intermediate typed events use DataParts with schema `adapter.stream.v1`. The exported names `AdapterStreamSchemaV1`, `AdapterStreamEnvelopeV1`, `AdapterStreamEventV1`, and `DecodeAdapterEventV1` intentionally keep the `V1` suffix: they version an interoperable wire format, not a temporary Go API. Encoding remains an internal bridge responsibility. The server advertises `urn:agent-adaptor:stream:v1` in its Agent Card, and each DataPart is self-describing.

The wire envelope preserves adaptor `EventMeta` plus provider source coordinates, complete tool snapshots, approval fields, and detailed dropped-event markers. Unknown DataPart schemas remain protocol data; the bridge does not guess provider semantics.

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
client := clienta2a.New(clienta2a.Options{
	AgentCardURL: "https://remote.example/.well-known/agent-card.json",
	Auth:         clienta2a.BearerTokenFromEnv("REMOTE_A2A_TOKEN"),
})
defer client.Close()

card, err := client.AgentCard(ctx)
if err != nil {
	return err
}

task, err := client.Send(ctx, clienta2a.SendRequest{
	Message: clienta2a.Message{
		Role: "user",
		Parts: []clienta2a.Part{{
			Kind:      clienta2a.PartText,
			Text:      "Review this change",
			MediaType: "text/plain",
		}},
	},
	AcceptedOutputModes: card.DefaultOutputModes,
})
if err != nil {
	return err
}
_ = task
```

`SendStream` and `Subscribe` return ordered protocol events. A message, final task/status, or `TASK_STATE_INPUT_REQUIRED` completes the stream. Duplicate or late events after the first terminal event are ignored. If transport fails after a task ID is known, the client attempts one `GetTask` recovery and marks a recovered terminal event with `RecoveredState`.

`GetTaskRequest` and `CancelTaskRequest` carry tenant and operation metadata:

```go
task, err := client.GetTask(ctx, clienta2a.GetTaskRequest{
	TaskID: "task-123",
	Tenant: "tenant-a",
})

_, err = client.CancelTask(ctx, clienta2a.CancelTaskRequest{
	TaskID: "task-123",
	Tenant: "tenant-a",
	Metadata: map[string]any{
		"reason": "parent_cancelled",
	},
})
```

Outbound DataPart and metadata values must encode as non-null JSON. Integer-valued numbers must remain within the interoperable IEEE-754 safe range; encode larger IDs and counters as strings. Invalid values fail before sending, and unsafe inbound DataParts return protocol errors rather than silently rounding.

Credentials are origin-pinned. Auth is sent to the Agent Card origin by default; cross-origin interfaces require `TrustedAuthOrigins`, and redirects to untrusted origins lose authorization headers. `SubscribeRequest.Since` is rejected because A2A 1.0 has no cursor replay field.

See [`examples/a2a-server`](../examples/a2a-server) for an end-to-end server and client using the final API.

## Curated Local/Remote teams

`hosttools/team` implements `adaptor.Runner` and owns one authenticated loopback
MCP listener. A leader gets four tools (`team_delegate`, `team_send`,
`team_status`, `team_cancel`); workers get messaging and status tools. The
leader wakes when a task finishes or a member asks a question. See [Teams](team.md)
for durability, limits, recovery, Fork and protocol limitations.

```go
workers, err := team.New(team.Config{
    Leader: leader,
    Members: []team.Member{
        {Key: "plan", Runner: planner},
        {Key: "review", Remote: &team.RemoteSpec{
            AgentCardURL: reviewCardURL,
            Auth: a2aclient.BearerTokenFromEnv("REVIEW_TOKEN"),
            Policy: team.RemotePolicy{MaxTimeout: 2 * time.Minute},
        }},
    },
})
if err != nil { return err }
defer workers.Close(ctx)
stream := workers.Thread("issue-123").Send(ctx, "coordinate the change")
for event := range stream.Events() { /* leader and typed SubagentUpdate */ }
result, err := stream.Result()
```

Remote members use `clients/a2a`, including its credential origin restrictions.
`team_send` sends another A2A message to the same task ID. Rejections return a
clear tool error. Disconnected tasks become failed with `unknown_state` after
two minutes without successful observation; the original request is never
replayed. The client negotiates the remote A2A protocol binding and wire method
names; hosts do not hard-code them.

The portable A2A contract exposes input-required state but no actionable
approval response schema. Such requests remain governed by the remote's own
policy, and the local task reports `waiting_approval`. Lifecycle-only HITL
payloads are not fabricated into locally answerable ApprovalRequests.

## Security boundaries

- Drivers remain unaware of A2A and delegation.
- The bridge and delegation service never bypass `Runner` to dispatch a Driver.
- Remote protocol dumps are not appended to a leader's raw stdout/stderr.
- Remote artifacts are structured references unless host policy explicitly fetches and exposes content.
- Bearer tokens do not enter runtime-service reports, run metadata, or serialized MCP declarations.
- Hosts remain responsible for network exposure, authentication, authorization, tenant policy, and durable storage.

## Non-goals

Core does not provide automatic remote-agent discovery, A2A-to-Driver routing, team role configuration, business workflow planning, production push infrastructure, built-in tenant/auth policy, or durable task persistence. These remain explicit host responsibilities composed above the six core nouns: Agent, Thread, Stream, Event, Result, and Driver.
