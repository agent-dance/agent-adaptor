# A2A bridge, client, and delegation

`agent-adaptor` keeps A2A at the host integration boundary:

- `bridges/a2a` publishes any `adaptor.Runner` as an A2A agent.
- `clients/a2a` calls remote A2A agents through stable, protocol-shaped DTOs.
- `hosttools/a2adelegation` optionally gives a leader Agent a curated Local/Remote delegation service.

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

## Curated Local/Remote delegation

`hosttools/a2adelegation` is an optional host component for a leader Agent. A `Service` combines:

- a host-curated registry of Local and Remote targets;
- the authenticated per-run `delegate_to_agent` MCP sidecar;
- A2A transport and event mapping;
- ordered delegation events and final result recording;
- runtime-service attachment and teardown.

Local targets execute any `adaptor.Runner` in-process. Remote targets use `clients/a2a`. Both paths pass through the same A2A-shaped mapper and emit the same `DelegationEvent` vocabulary.

```go
team, err := a2adelegation.NewService(a2adelegation.Config{
	Agents: []a2adelegation.AgentRef{
		a2adelegation.LocalNamed("plan", "Codex Planner", planner, a2adelegation.Policy{}),
		a2adelegation.Remote("review", reviewCardURL, a2adelegation.Policy{
			MaxTimeout: 2 * time.Minute,
		}),
	},
	ToolTimeout: 3 * time.Minute,
})
if err != nil {
	return err
}
defer team.Close()

leader := adaptor.New(leaderDriver, team.Option())
stream := leader.Stream(ctx, "coordinate the change")
for event := range stream.Events() {
	switch event := event.(type) {
	case adaptor.TextDelta:
		_ = event.Text
	case adaptor.SubagentUpdate:
		_ = event.Agent
	}
}
result, err := stream.Result()
```

Use `Local` when the registry key is also the only display label needed. Use
`LocalNamed` to keep the model-facing key stable while carrying a separate
human-facing name in `DelegationEvent.AgentName`; visual hosts can use names
such as `Claude Code Implementer` to show the provider base independently from
the workflow role.

`team.Option()` is a `SharedOption`: pass it to `adaptor.New` for every run or to one `Run`/`Stream` call for a single invocation. It is equivalent to the generic extension point:

```go
leader := adaptor.New(leaderDriver, adaptor.WithRunServices(team))
```

`Service` implements `adaptor.RunServiceProvider`. For each run it publishes a typed `ServiceRef.MCP` declaration, carries the bearer token only through `SecretEnv`, injects `SubagentUpdate` into the leader's existing Event channel, and tears the sidecar down during normal run cleanup. There is no separate subagent bus in the Runner API, and MCP declarations are never inferred from stringly metadata.

The leader sees only registry keys, objectives, optional input, and bounded constraints. It never receives endpoint URLs or credentials. Unknown tool fields and attempts to provide `endpoint_url` are rejected.

`Service.Result(runID, key)` returns the latest result for an Agent key. `Service.Results(runID)` is the latest-by-key map. `Service.Delegations(runID)` preserves every delegation in `DelegationStarted` acceptance order, including repeated calls to the same Agent. Recorded results remain readable after per-run sidecar teardown and after `Service.Close`.

For lower-level integrations, `Registry`, `Delegator`, `EventBus`, and `MCPServer` remain independently usable. `StatusPartDecoder` can decode a host-owned A2A Status DataPart schema. The built-in `adapter.stream.v1` decoder remains the lossless path for adaptor events.

## Security boundaries

- Drivers remain unaware of A2A and delegation.
- The bridge and delegation service never bypass `Runner` to dispatch a Driver.
- Remote protocol dumps are not appended to a leader's raw stdout/stderr.
- Remote artifacts are structured references unless host policy explicitly fetches and exposes content.
- Bearer tokens do not enter runtime-service reports, run metadata, or serialized MCP declarations.
- Hosts remain responsible for network exposure, authentication, authorization, tenant policy, and durable storage.

## Non-goals

Core does not provide automatic remote-agent discovery, A2A-to-Driver routing, team role configuration, business workflow planning, production push infrastructure, built-in tenant/auth policy, or durable task persistence. These remain explicit host responsibilities composed above the six core nouns: Agent, Thread, Stream, Event, Result, and Driver.
