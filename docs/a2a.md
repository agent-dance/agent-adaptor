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
})

mux := http.NewServeMux()
mux.Handle("/.well-known/agent-card.json", server.AgentCardHandler())
mux.Handle("/a2a", server.Handler())
```

The terminal result is emitted as a structured artifact named `agent-adaptor-result`. Assistant-facing output remains in the final A2A status message; `raw_streams`, `transcript`, `summary`, `result`, `usage`, and metadata remain separate fields inside the artifact.

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

`SendStream` and `Subscribe` return ordered protocol events. If a stream fails before a terminal event and the task ID is known, the client attempts one `GetTask` recovery; a terminal recovered task is returned with `RecoveredState=true`.

## Non-Goals

Visual subagent delegation, A2A-to-local adapter routing, push notification storage, and durable task persistence are outside this slice. Issue #5 tracks the visual subagent flow above these protocol primitives.
