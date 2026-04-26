# agent-adaptor

[简体中文说明 / Chinese Version](./README.zh-CN.md)

A pure Go SDK for calling local coding agents in one consistent way.

`agent-adaptor` is for teams that want to plug `codex`, `claude`, or `cursor` into their product. It gives you a shared layer for process launch, session reuse, permission injection, dynamic skill injection, runtime service injection, and result collection.

## At A Glance

- Use one interface across multiple local agents.
- Default-agent-first API: bind once, then call `sdk.Run(...)` or `sdk.Start(...)`.
- Stateless by default; sessions are reused only when you inject a `SessionStore`.
- The caller controls sessions, workspaces, skills, permissions, instructions, and runtime services.
- This is a pure SDK, not a server, queue, scheduler, tenant system, or auto-router.

## What It Solves

If you are building a CLI, desktop app, HTTP/gRPC service, or background worker, the hard part usually is not "run one command once." The hard part is keeping agent integration consistent: how defaults are merged, how sessions are reused, how events stream out, how skills are injected, how runtime context is passed in, how results are recorded, and how the environment is checked before a run. `agent-adaptor` folds that into one SDK surface.

The public usage model is intentionally simple: bind a default agent at construction time, call `sdk.Run(...)` or `sdk.Start(...)`, optionally bind more named agents, and inject a `SessionStore` only when you actually need continuity.

## Core Scenarios

- Embed one default local agent into your product with minimal setup.
- Bind extra named agents for flows like "implement with Codex, review with Claude".
- Reuse sessions in service-style workflows without making state mandatory for every caller.
- Let your application control workspaces, skills, permissions, and runtime services instead of hiding those details inside provider-specific integration code.
- Use the same approach for environment checks, model detection, config-field discovery, quota checks, and skill management.

## When To Use It

Use `agent-adaptor` when you do not want your integration layer tied to one agent, but you still want each agent's real capabilities and limits preserved.

Do not use it if you want a built-in HTTP server, queue, scheduler, tenant framework, or automatic agent routing. Those are explicit non-goals and should live above the SDK.

## Install

```bash
go get github.com/agent-dance/agent-adaptor
```

## Quick Start

The fastest path is: bind one default agent and call `sdk.Run(...)`.

```go
package main

import (
	"context"
	"fmt"

	agentadaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/codex"
)

func main() {
	sdk := agentadaptor.New(
		agentadaptor.WithDefaultAgent(codex.New(agentadaptor.CodexConfig{
			Model: "gpt-5.4",
		})),
	)

	result, err := sdk.Run(context.Background(), "fix the failing tests")
	if err != nil {
		panic(err)
	}

	fmt.Println(result.Output)
}
```

`New(...)` fits application and test code and fails fast on invalid config. Use `Build(...)` if you embed the SDK into a library or service and want to handle configuration errors yourself.

## Core Usage Model

1. Bind a default agent with `WithDefaultAgent(...)`.
2. Use `sdk.Run(...)` or `sdk.Start(...)` for the default execution path.
3. For multi-agent flows, bind extra agents with `WithAgent(name, binding)` and fetch them via `sdk.Agent(name)`.
4. Let per-call `RunOption`s override binding defaults.
5. Inject `SessionStore` only when you actually need session continuity.

All execution paths go through the same internal flow: first merge defaults and per-run arguments, then assemble the full config for this run, then handle sessions, run the adapter, and only persist state when a valid session checkpoint is returned.

## Defaults And Overrides

Bind-time defaults establish a stable baseline for your application. Per-run options override only what changed for that specific run.

- Bind-time defaults: `WithDefaultIdentity`, `WithDefaultWorkspace`, `WithDefaultSkills`, `WithDefaultMCP`, `WithDefaultRunPolicy`, `WithDefaultInstructions`, `WithDefaultRuntimeServices`, `WithDefaultStreaming`, `WithDefaultMetadata`, profile options such as `WithDedicatedProfile`, and optional default HITL handlers.
- Per-run overrides: `WithSession`, `WithSessionKey`, `WithContinueSession`, `WithNewSession`, `WithForkSession`, `WithWorkspace`, `WithSkills`, `WithMCP`, `WithRunPolicy`, `WithInstructions`, `WithRuntimeServices`, `WithStreaming`, `WithoutStreaming`, `WithMetadata`, `WithAgentIdentity`, and per-run HITL handlers.

## Common Flows

### Multiple Agents

The default agent uses `sdk.Run(...)`. Named agents are fetched with `sdk.Agent(name)` and run separately.

```go
package main

import (
	"context"

	agentadaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/claude"
	"github.com/agent-dance/agent-adaptor/codex"
)

func main() {
	sdk := agentadaptor.New(
		agentadaptor.WithDefaultAgent(codex.New(agentadaptor.CodexConfig{
			Model: "gpt-5.4",
		})),
		agentadaptor.WithAgent("review", claude.New(agentadaptor.ClaudeConfig{
			Model: "claude-sonnet-4",
		})),
	)

	_, err := sdk.Run(context.Background(), "implement the fix")
	if err != nil {
		panic(err)
	}

	review, err := sdk.Agent("review")
	if err != nil {
		panic(err)
	}

	_, err = review.Run(context.Background(), "review the patch")
	if err != nil {
		panic(err)
	}
}
```

### Session Reuse

Without `WithSessionStore(...)`, runs are stateless. With a store, you can reuse stable logical sessions with `SessionKey`, or choose stricter modes with `continue_only`, `start_new`, and `fork`.

```go
package main

import (
	"context"

	agentadaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/codex"
	"github.com/agent-dance/agent-adaptor/memory"
)

func main() {
	store := memory.NewSessionStore()

	sdk := agentadaptor.New(
		agentadaptor.WithDefaultAgent(codex.New(agentadaptor.CodexConfig{
			Model: "gpt-5.4",
		})),
		agentadaptor.WithSessionStore(store),
	)

	_, err := sdk.Run(
		context.Background(),
		"continue issue-123",
		agentadaptor.WithSessionKey("company-1", "issue-123"),
	)
	if err != nil {
		panic(err)
	}
}
```

Changing `WithSkills(...)` or `WithMCP(...)` does not automatically make an existing session incompatible. Session continuity is still decided by the caller through `SessionMode`.

### MCP Injection

MCP server declarations use the same default/override model as skills: bind a default set with `WithDefaultMCP(...)`, or replace it for one run with `WithMCP(...)`. Built-in adapters materialize the resulting MCP config into the effective provider profile before launching the CLI.

### Streaming

`Start(...)` returns a `RunHandle` with `Events()`, `StreamEvents()`, `DecisionRequests()`, `RunID()`, `Wait(...)`, `Cancel(...)`, and `ResolveDecision(...)`. Your application can use the same execution API for operational events, token-level streaming, and HITL callbacks instead of maintaining separate run paths.

## Capability Surface

| Surface | Description |
| --- | --- |
| Execution | `Run(...)` for synchronous execution, `Start(...)` for streaming handles. |
| Sessions | Stateless by default; supports `continue_or_start`, `continue_only`, `start_new`, and `fork` when a `SessionStore` is present. |
| Skills | Uses one flow for skill resolution, normalization, assembly, and synchronization. |
| MCP | Lets the host declare MCP server specs once and have built-in adapters materialize them into the effective provider profile. |
| Runtime Services | Prepares the runtime services a run needs before execution and releases them during cleanup by `RunID`. |
| Admin API | Provides management APIs for environment checks, model listing and detection, config-field discovery, quota queries, and skill management. |
| Run Results | Separates assistant text (`Output`), raw stdout/stderr (`RawStreams`), semantic entries (`Transcript`), short labels (`Summary`), provider terminal JSON (`Result`), provider/model/cost metadata, runtime-service status, and structured questions/failures. |

## Built-In Packages

The built-in packages return configured `AgentBinding`s, not low-level adapters.

- `github.com/agent-dance/agent-adaptor/codex`
- `github.com/agent-dance/agent-adaptor/claude`
- `github.com/agent-dance/agent-adaptor/cursor`

If you need lower-level extension hooks, each built-in package also exposes `NewAdapter()`.

For built-in adapters, profile options such as `WithNativeProfile()`, `WithDedicatedProfile(dir)`, `WithCloneProfile(dir, opts)`, and `WithCloneProfileFrom(src, dst, opts)` select or initialize the effective provider profile directory without hand-writing provider-specific environment variables.

## Management API

`sdk.Admin()` is for management tasks only. It does not execute prompts.

Use it when your application needs to inspect a bound agent before or around execution:

- `CheckEnvironment(...)` for truthful environment checks.
- `ListModels(...)` and `DetectModel(...)` for model visibility and detection.
- `ConfigSchema(...)` for the field data needed to build settings UIs.
- `GetQuota(...)` for truthful quota or credit windows when supported.
- `ListSkills(...)` and `SetSelectedSkills(...)` for skill inventory and process-local selected-skill overrides.

## Adapter Extensions

Third-party adapters implement `DriverAdapter` and plug into the same public execution surface.

```go
binding := agentadaptor.BindTyped(myAdapter, myConfig)
sdk := agentadaptor.New(agentadaptor.WithDefaultAgent(binding))
```

The shared CLI helper only handles process I/O and raw event transport. Parsing official session checkpoints stays inside each adapter, so CLI protocol differences do not leak into shared infrastructure.

If you are writing your own adapter, the reusable test suite lives in [`adaptertest`](./adaptertest).

## Examples

- [`examples/codex-basic`](./examples/codex-basic): minimal default-agent run.
- [`examples/codex-stream`](./examples/codex-stream): `Start(...)` and event streaming.
- [`examples/codex-sessions`](./examples/codex-sessions): service-style session reuse.
- [`examples/codex-admin-named`](./examples/codex-admin-named): named agents and management APIs.
- [`examples/codex-skills-live`](./examples/codex-skills-live): live skill injection and synchronization.
- [`examples/mock-runtime-admin`](./examples/mock-runtime-admin): runtime services and management output.
- [`examples/session-codec-inspect`](./examples/session-codec-inspect): inspect adapter session parameters safely.
- [`examples/mock-adapter-playground`](./examples/mock-adapter-playground): custom adapter playground.
- [`examples/mock-skills-contract`](./examples/mock-skills-contract): deterministic skills request assembly.
- [`examples/streaming-chat`](./examples/streaming-chat): Go-channel token streaming.
- [`examples/streaming-sse-server`](./examples/streaming-sse-server): minimal HTTP SSE chat endpoint.
- [`examples/streaming-chat-copilotkit`](./examples/streaming-chat-copilotkit): AG-UI + CopilotKit demo with HITL cards.
- [`examples/streaming-chat-aguiclient`](./examples/streaming-chat-aguiclient): Vite + React + `@ag-ui/client` direct AG-UI demo.

## Current Guarantees

- There is only one public execution path: merge defaults, assemble run arguments, coordinate sessions, execute the adapter, persist session checkpoints, and archive the result.
- The main path is default-agent-first, not "pick an agent from a registry each time."
- Stateful runs only persist session state when the adapter returns a valid session checkpoint.
- The management API and execution API share the same default-agent and named-agent model.
- Built-in packages return typed binding objects through `New(...)`, and custom adapters can follow the same pattern with `BindTyped(...)`.

## Non-Goals

- No built-in HTTP/gRPC server.
- No built-in queue, scheduler, tenant framework, or orchestration daemon.
- No automatic agent routing.
- No second execution entrypoint with different semantics.

## Further Reading

Start with the current, user-facing docs:

- [`docs/README.md`](./docs/README.md): current docs map and historical-workstream index.
- [`docs/api-reference.md`](./docs/api-reference.md): public API surface and which package owns each concept.
- [`docs/usage-guide.md`](./docs/usage-guide.md): common host integration patterns.
- [`docs/run-policy.md`](./docs/run-policy.md): `RunPolicy` and HITL contract.
- [`docs/streaming.md`](./docs/streaming.md): token streaming, AG-UI, and SSE usage.
- [`docs/public-errors.md`](./docs/public-errors.md): public error catalogue.
