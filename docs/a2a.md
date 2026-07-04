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
4. The bridge calls `Runner.Start(..., WithStreaming())`.
5. `StreamEvents`, `Wait`, and `Cancel` are translated to A2A status, artifact, and terminal events.

Bridge capability advertisement is strict:

- `AgentCard.Capabilities.Streaming` is a tri-state. The zero value keeps A2A streaming enabled; use `a2a.CapabilityDisabled` to publish `streaming=false`.
- `PushNotifications` is not exposed unless `AgentCard.Capabilities.PushNotifications=true` and `ServerOptions.PushNotifications` provides both an A2A push `ConfigStore` and `Sender`.
- `ExtendedAgentCard` is not exposed unless `AgentCard.Capabilities.ExtendedAgentCard=true` and `ServerOptions.ExtendedAgentCard` provides either a static card or a provider.

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

The terminal result is emitted as a structured artifact named `agent-adaptor-result`. Assistant-facing output remains in the final A2A status message. The default artifact contains only the safe summary; diagnostics such as metadata, usage, provider result payloads, transcript, raw streams, reasoning, tool-call internals, and HITL payloads require explicit `ExposurePolicy` opt-in and are sanitized before they cross the A2A boundary.

Bridge-owned artifact names are part of the package contract for hosts that
compose this bridge with higher-level stream overlays:

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

## Non-Goals

Visual subagent delegation, A2A-to-local adapter routing, bridge-managed durable task persistence, and default push delivery infrastructure are outside this slice. Issue #5 tracks the visual subagent flow above these protocol primitives.
