# A2A Bridge And Client

`agent-adaptor` supports A2A through two localized packages:

- `pkg/bridges/a2a` exposes an existing `agentadaptor.Runner` as an A2A-compatible agent.
- `pkg/clients/a2a` provides thin client primitives for calling remote A2A agents.

The core SDK remains protocol-agnostic. There is no `WithA2A`, no remote A2A `AgentBinding`, no built-in HTTP server, and no automatic remote-agent routing in core.

## Dependency Choice

The implementation uses `github.com/a2aproject/a2a-go/v2`.

Reliability: A2A card parsing, JSON-RPC/SSE transport, task events, protocol errors, and request handlers are delegated to the official Go SDK instead of duplicated with hand-rolled wire code.

Maintainability: The dependency is maintained by the A2A project and currently exposes protocol `1.0` through the `a2a.Version` constant. The package is pinned in `go.mod` so protocol updates are explicit.

Localization: Imports from `github.com/a2aproject/a2a-go/v2` are confined to `pkg/bridges/a2a` and `pkg/clients/a2a`. The bridge imports only the core `Runner` contract from this repository and does not import concrete provider adapters.

## Bridge

The bridge maps A2A task execution onto the single SDK execution path:

1. `SendMessage` / `SendStreamingMessage` decode inbound A2A messages.
2. The configured `PromptBuilder` produces a prompt and optional `RunOption`s.
3. The configured `SessionMapper` may bind A2A `contextId` or `taskId` to SDK session options.
4. The bridge calls `Runner.Start(...)` and applies `RunStreaming` as the final
   per-call streaming override (`WithStreaming` by default,
   `WithoutStreaming` when disabled).
5. `StreamEvents`, `Wait`, and `Cancel` are translated to A2A status, artifact, and terminal events. `ServerOptions.StreamWire` controls the intermediate-event wire profile.

Bridge capability advertisement is strict:

- `AgentCard.Capabilities.Streaming` is a tri-state. The zero value keeps A2A streaming enabled; use `a2a.CapabilityDisabled` to publish `streaming=false`.
- `PushNotifications` is not exposed unless `AgentCard.Capabilities.PushNotifications=true` and `ServerOptions.PushNotifications` provides both an A2A push `ConfigStore` and `Sender`.
- `ExtendedAgentCard` is not exposed unless `AgentCard.Capabilities.ExtendedAgentCard=true` and `ServerOptions.ExtendedAgentCard` provides either a static card or a provider.
- `ServerOptions.PromptBuilder` customizes inbound prompt construction. The
  legacy `ServerOptions.Prompt` field remains supported for source
  compatibility, with `PromptBuilder` taking precedence when both are set.
- `ServerOptions.ResultBuilder` can add or replace terminal artifacts and
  override completed status text. It runs only after a successful SDK result;
  streamed artifacts already emitted during the run are not retroactively
  removed.
- `ServerOptions.StreamWire` defaults to `StreamWireLegacyArtifact`. Hosts that
  set `StreamWireStatusData` send intermediate text, reasoning, tool, HITL, and
  dropped-stream events as `TaskStatusUpdateEvent` DataParts using the stable
  `adapter.stream.v1` schema. The server advertises the optional
  `urn:agent-adaptor:stream:v1` Agent Card extension while each DataPart remains
  self-describing, so existing clients do not need an extension negotiation
  header. Terminal result artifacts
  remain unchanged.

```go
runner := sdk.Default()

server := a2a.NewServer(runner, a2a.ServerOptions{
	AgentCard: a2a.AgentCard{
		Name:        "Local Codex",
		Description: "Runs local agent-adaptor tasks",
		Version:     "1.0.0",
		URL:         "https://host.example/a2a",
		Skills: []a2a.Skill{{
			ID:          "chat",
			Name:        "Chat",
			Description: "Run a prompt through the configured default agent",
			Tags:        []string{"agent", "coding"},
		}},
	},
	Session: a2a.SessionByContextID("a2a"),
	StreamWire: a2a.StreamWireStatusData,
	TaskLifecycle: a2a.TaskLifecycleOptions{
		Ephemeral: &a2a.EphemeralTaskStoreOptions{
			MaxTasks: 512,
			TTL:      2 * time.Hour,
		},
	},
})

mux := http.NewServeMux()
mux.Handle("/.well-known/agent-card.json", server.AgentCardHandler())
mux.Handle("/a2a", server.Handler())
```

See [`examples/a2a-local`](../examples/a2a-local) for a runnable local
end-to-end demo that starts this bridge around a real local SDK runner, calls
it with `pkg/clients/a2a`, consumes streaming artifacts, and verifies the final
task with `GetTask`. The example defaults to an isolated temporary workspace
and isolated cloned provider profile seeded from native settings so custom API
key / base URL setups work without writing demo state into a host's active
local agent profile.

The terminal result is emitted as a structured artifact named `agent-adaptor-result`. Assistant-facing output remains in the final A2A status message. The default artifact contains only the safe summary; diagnostics such as metadata, usage, provider result payloads, transcript, raw streams, reasoning, tool-call internals, and HITL payloads require explicit `ExposurePolicy` opt-in and are sanitized before they cross the A2A boundary.

In the default legacy wire mode, bridge-owned artifact names are part of the
package contract for hosts that compose this bridge with higher-level stream overlays:

- `a2a.ArtifactAssistantOutput` (`assistant-output`) carries streamed
  assistant-facing text deltas and closes with `lastChunk=true`.
- `a2a.ArtifactAgentAdaptorResult` (`agent-adaptor-result`) carries the
  terminal summary and any opt-in sanitized diagnostics, and is emitted as a
  single final chunk.

Task retention is explicit. `NewServer` no longer hides the upstream unbounded in-memory task store. If `ServerOptions.TaskLifecycle.Store` is nil, the bridge uses a bounded ephemeral store with a default `MaxTasks=256` and `TTL=1h`. Hosts that need durable retention, custom paging/auth behavior, or cross-process lifecycle ownership must inject their own `a2asrv/taskstore.Store`.

Hosts own serving concerns: route layout, authentication, authorization, TLS, tenancy, durability, task retention, and observability.

## Client

The client package is intentionally protocol-shaped. It does not wrap remote A2A tasks in local `RunResult` semantics and does not expose stdout/stderr concepts.

```go
client := a2a.New(a2a.Options{
	AgentCardURL: "https://remote.example/.well-known/agent-card.json",
	Auth:         a2a.BearerTokenFromEnv("REMOTE_A2A_TOKEN"),
})

card, err := client.AgentCard(ctx)
if err != nil {
	return err
}

task, err := client.Send(ctx, a2a.SendRequest{
	Message: a2a.Message{
		Role: "user",
		Parts: []a2a.Part{{
			Kind:      a2a.PartText,
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

`SendStream` and `Subscribe` return ordered protocol events. The client uses the same execution-final semantics as the official server stack: a `Message`, a terminal task/status, or `TASK_STATE_INPUT_REQUIRED` ends the stream. Duplicate or late events after the first final event are ignored. If a stream fails before a final event and the task ID is known, the client attempts one `GetTask` recovery; a final recovered task is returned with `RecoveredState=true`.

`PartData`, `SendRequest.Metadata`, `Message.Metadata`, and `Part.Metadata` are
normalized before send. Values must encode as non-null JSON, and
integer-valued numbers must stay within the interoperable IEEE-754 safe range
(`-9007199254740991` through `9007199254740991`). Encode larger IDs and counters
as strings. Invalid outbound values are rejected before send; unsafe `PartData`
received from a remote agent is returned as a protocol error instead of being
silently rounded.

Bearer credentials are origin-pinned. With `Auth` configured, the client only sends credentials to the Agent Card origin by default. Agent Card interface URLs on another origin require explicit `TrustedAuthOrigins`, and redirects to untrusted origins have authorization headers stripped. `SubscribeRequest.Since` is rejected when set because A2A 1.0 `SubscribeToTask` has no cursor replay field.

Task lookup and cancellation use request DTOs so hosts can pass A2A tenant and
retention/cancellation metadata without changing the client API later:

```go
task, err := client.GetTask(ctx, a2a.GetTaskRequest{
	TaskID: "task-123",
	Tenant: "tenant-a",
})
if err != nil {
	return err
}

_, err = client.CancelTask(ctx, a2a.CancelTaskRequest{
	TaskID: "task-123",
	Tenant: "tenant-a",
	Metadata: map[string]any{
		"reason": "parent_cancelled",
	},
})
```

## Visual Delegation Host Tools

The optional host-owned visual subagent delegation layer sits above the A2A client layer:

- `pkg/hosttools/a2adelegation` exposes a curated registry, `delegate_to_agent`
  MCP tool server, delegation event bus, A2A event mapper, and `Delegator`.
- `pkg/bridges/subagentstream` overlays delegation events onto an existing
  parent AG-UI stream as `CUSTOM` events named `subagent.*`.
- `pkg/bridges/sse.Options.SubagentBus` enables the overlay in the stock SSE
  handler for AG-UI callers.

The model-facing tool input accepts registry keys only:

```json
{
  "agent": "research",
  "objective": "Find current A2A streaming behavior.",
  "input": {
    "prompt": "Summarize exact APIs and artifact behavior.",
    "context": "We are implementing visual delegation."
  },
  "constraints": {
    "timeout_seconds": 180,
    "stream": true,
    "max_artifacts": 10
  }
}
```

`endpoint_url`, unknown top-level fields, and unknown nested input/constraint
fields are rejected. Host code owns the registry entry (`RemoteAgentSpec`), auth,
tenant, timeout policy, artifact limits, accepted output modes, and trusted
origins. `constraints.max_artifacts`, when supplied, limits only the final
`DelegationResult.Artifacts` returned to the parent model; live `subagent.*`
artifact events remain a UI side channel. The parent model receives only the
final structured `DelegationResult` JSON through the MCP tool result. Live remote
progress is published to the bus and rendered as AG-UI custom events:

```json
{
  "type": "CUSTOM",
  "name": "subagent.text.delta",
  "value": {
    "runId": "run-...",
    "parentToolCallId": "tool-...",
    "delegationId": "del-...",
    "agentKey": "research",
    "remoteProtocol": "a2a",
    "remoteTaskId": "task-...",
    "delta": "Searching official examples..."
  }
}
```

Assistant output uses the ordered `subagent.text.start`,
`subagent.text.delta`, and `subagent.text.end` lifecycle. The mapper accepts both
legacy bridge artifacts and StatusUpdate DataParts. Hosts using
`adapter.stream.v1` receive typed `DelegationEvent` values for each supported
Status DataPart. A host-owned schema can implement `StatusPartDecoder` and pass
it through `WithStatusPartDecoder`; the mapper locks one stream profile per
delegation, deduplicates replayed status data, and reports sequence gaps as
`subagent.stream.dropped`. Legacy tool artifacts continue to emit typed
`subagent.tool_call.start`, `subagent.tool_call.args`,
`subagent.tool_call.result`, and `subagent.tool_call.end` events during rolling
upgrades. For typed tool events, `StreamPayload.ToolCallID` is the remote
tool-call ID; the parent model tool-call ID remains available as
`parentToolCallId` and `Raw["parent_tool_call_id"]`.

`parentToolCallId` is included only when the host can supply the parent provider
tool-call ID, for example by setting `a2adelegation.MCPServerOptions.ParentToolCallID`
or by publishing `DelegationEvent` values with that field. The MCP JSON-RPC
request ID is not treated as the parent model tool-call ID. UI grouping should
tolerate this field being absent and fall back to `runId` + `delegationId`.

Runtime-created MCP sidecars can be injected into a run without asking the model
for run IDs, URLs, or credentials. A `RuntimeServiceManager` returns a
`RuntimeServiceRef` with metadata keys: `agentadaptor.mcp.enabled=true`,
`agentadaptor.mcp.key`, `agentadaptor.mcp.transport`, `agentadaptor.mcp.url`,
`agentadaptor.mcp.command`, `agentadaptor.mcp.args_json`,
`agentadaptor.mcp.env_json`, `agentadaptor.mcp.headers_json`,
`agentadaptor.mcp.bearer_token_env_var`, `agentadaptor.mcp.required`, and
`agentadaptor.mcp.required_reason`. If `agentadaptor.mcp.key` is omitted, the SDK
falls back to the runtime ref `Name`, then `ID`. If transport is omitted, it
defaults to `stdio` when a command is present and `http` otherwise. Non-stdio
servers use `RuntimeServiceRef.URL` when `agentadaptor.mcp.url` is omitted;
stdio servers use `RuntimeServiceRef.Command` when `agentadaptor.mcp.command` is
omitted. The SDK appends those servers after runtime ensure, before profile
materialization, so adapter MCP config and session fingerprints include the
per-run delegation endpoint.

Security boundary: concrete adapters stay unaware of A2A delegation; remote
protocol dumps are not appended to `RunResult.RawStreams`; remote artifacts are
reported as structured references/metadata unless host policy explicitly stores
and exposes content elsewhere.

## Upgrade Notes

- `ServerOptions`, `Delegator`, `DelegationRequest`, `DelegationEvent`,
  `DelegationResult`, and `MCPServerOptions` gained additive fields. Existing
  keyed literals and constructor-based setup continue to compile. External code
  using unkeyed composite literals must migrate to keyed fields.
- Custom `A2AStream` implementations must make `Close` unblock an in-flight
  `Recv`. Implementing the optional `RecvContext(context.Context)` method avoids
  the compatibility goroutine used for legacy streams.
- `DelegationResult.Summary` prefers the final task status message when one is
  present, then falls back to result artifacts and task history.
- Custom MCP tools are configured with `MCPServerOptions.Tools`. Because this is
  a slice, `MCPServerOptions` and `MCPServer` are no longer comparable; code that
  previously used either value with `==` or as a map key must use an explicit
  stable key instead.

## Non-Goals

Automatic A2A-to-local adapter routing, bridge-managed durable task persistence,
default push delivery infrastructure, dynamic remote-agent discovery, built-in
tenant/auth policy, and production per-run MCP sidecar lifecycle remain outside
this slice.

Visual subagent delegation is an optional host-owned layer built from
`pkg/hosttools/a2adelegation`, `pkg/bridges/subagentstream`, and runtime-service
MCP injection. Core SDK execution remains protocol-agnostic and does not route to
remote A2A agents automatically.
