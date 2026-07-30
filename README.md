# agent-adaptor

[简体中文](./README.zh-CN.md)

`agent-adaptor` is a production-oriented Go library for embedding local coding agents in CLIs, desktop applications, services, workers, and workflows.

Its public model has six nouns:

| Noun | Meaning |
|---|---|
| `Agent` | A configured agent that is ready to run. |
| `Thread` | A durable conversation identified by an opaque host-owned key. |
| `Stream` | One running invocation. |
| `Event` | One typed occurrence during an invocation. |
| `Result` | The final output and audit record. |
| `Driver` | A provider integration such as Codex, Claude, Cursor, or CodeBuddy. |

There is one constructor, one execution pipeline, one typed event stream, and one error verdict. Multiple agents are simply multiple Go variables.

## Install

```bash
go get github.com/agent-dance/agent-adaptor
```

The module requires Go 1.26.5 or later.

## Quick start

```go
package main

import (
	"context"
	"fmt"
	"log"

	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/codex"
)

func main() {
	agent := adaptor.New(
		codex.Driver(codex.Config{Model: "gpt-5.4"}),
		adaptor.WithWorkspace("/path/to/repository"),
	)

	result, err := agent.Run(context.Background(), "Fix the failing tests")
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(result.Text)
}
```

Built-in integrations use the same construction shape:

```go
codexAgent := adaptor.New(codex.Driver(codex.Config{}))
claudeAgent := adaptor.New(claude.Driver(claude.Config{}))
cursorAgent := adaptor.New(cursor.Driver(cursor.Config{}))
codeBuddyAgent := adaptor.New(codebuddy.Driver(codebuddy.Config{}))
```

## Run and Stream

`Run` is the batch form. `Stream` exposes the same invocation as one typed event channel and a final `Result`:

```go
stream := agent.Stream(ctx, "Explain the proposed patch")
defer stream.Cancel()

for event := range stream.Events() {
	switch event := event.(type) {
	case adaptor.TextDelta:
		fmt.Print(event.Text)
	case adaptor.ToolCall:
		fmt.Printf("\n[tool: %s]\n", event.Name)
	case *adaptor.ApprovalRequest:
		_ = event.Deny(ctx, "interactive approval is disabled")
	case adaptor.Dropped:
		log.Printf("dropped %d incremental events", event.Count)
	}
}

result, err := stream.Result()
```

`Run` is implemented through the same stream pipeline. A caller that stops consuming events must call `Cancel`; approval, lifecycle, terminal, transcript, and drop-report events are never silently discarded.

## Stateful Threads

Agents are stateless by default. Add a `threadstore.Store` when a host needs conversation continuity:

```go
agent := adaptor.New(
	claude.Driver(claude.Config{}),
	adaptor.WithThreadStore(memory.NewStore()),
)

thread := agent.Thread("tenant-42/issue-123")
result, err := thread.Run(ctx, "Continue the investigation")

resumeOnly := agent.Thread("tenant-42/issue-123", adaptor.ResumeOnly())
branch := thread.Fork("tenant-42/issue-123/alternative")
```

Thread keys are opaque strings owned by the host. A new unrelated conversation gets a new host key; the SDK does not rebind an existing key on request. Driver resume identifiers remain checkpoint details. Durable hosts can implement `threadstore.Store`; `memory.NewStore()` is intended for one process.

On POSIX platforms, Claude, CodeBuddy, and Codex reuse a provider process by default for successive turns of an explicit Thread; Windows truthfully reports one-shot process capability. Use `WithSpawn()` at Agent construction or on one call to force fresh one-shot processes. Close the Agent to reap its process pool:

```go
defer agent.Close(context.Background())

_, _ = thread.Run(ctx, "reuse the persistent writer")
_, _ = thread.Run(ctx, "use a fresh process", adaptor.WithSpawn())
```

Cursor and stateless Agent calls always spawn per invocation. Once `Close` starts, new Agent and Thread runs fail with `ErrAgentClosed`.

## Options and resources

Options use one vocabulary with compile-time scopes:

- `Option` applies only while constructing an Agent.
- `CallOption` applies only to `Run` and `Stream`.
- `SharedOption` is accepted in both places; a call-site value overrides the Agent default.

Skills append. Other resource families replace or merge according to their documented contract.

```go
agent := adaptor.New(
	codex.Driver(codex.Config{}),
	adaptor.WithModel("gpt-5.4"),
	adaptor.WithSkills(skill.Dir("./skills/review")),
	adaptor.WithMCP(mcp.Stdio("repo-tools", "repo-mcp", mcp.Args("serve"))),
	adaptor.WithProfile(profile.Dedicated("./profiles/reviewer")),
)

result, err := agent.Run(ctx, "Review this change",
	adaptor.WithModel("gpt-5.4-mini"),
	adaptor.WithMetadata("request_id", requestID),
)
```

Workspace managers, runtime service managers, skill providers, skill materializers, profile resources, and run-scoped host services are optional construction or invocation extensions. None is required for a basic run.

## Host-defined Tools

Applications can extend an Agent with typed Go functions without constructing
or managing an MCP server:

```go
type SearchInput struct {
	Query string `json:"query" jsonschema:"required"`
}

type SearchOutput struct {
	Files []string `json:"files"`
}

searchRepo := tool.Define(
	"search_repo",
	"Search files in the current repository.",
	func(ctx context.Context, in SearchInput) (SearchOutput, error) {
		return search(ctx, in.Query)
	},
	tool.ReadOnly(),
	tool.Idempotent(),
	tool.Revision("search_repo/v1"),
)

agent := adaptor.New(
	codex.Driver(codex.Config{}),
	adaptor.WithTools(searchRepo),
)
defer agent.Close(context.Background())
```

`WithTools` is construction-only and replaces the complete Tool set. Schemas
are inferred from the handler's Go types; explicit standard JSON Schema
overrides are available. `tool.Reject(code, message)` returns a safe,
model-visible business failure, while ordinary errors and panics are sanitized.
Use `tool.Revision` for every Tool used by a stateful Thread so handler behavior
can participate in resume compatibility.

MCP is an internal delivery mechanism for this API. Existing or remote MCP
servers still use `WithMCP`. Built-in Drivers materialize Tools in an
SDK-owned isolated execution profile, leaving the configured/native profile
unchanged. See the [host-defined Tools contract](./docs/tools.md)
for lifecycle, schema, error, security, and Thread semantics.

## Structured output example

Structured output is a per-run contract. `RunAs[T]` derives JSON Schema from a
Go type, prefers provider-native enforcement, automatically falls back to
prompting plus local validation when needed, and returns both the typed value
and the ordinary audit `Result`:

```go
type ReleasePlan struct {
	Filename  string `json:"filename"`
	MediaType string `json:"media_type"`
	Summary   string `json:"summary"`
	Content   string `json:"content"`
}

plan, result, err := adaptor.RunAs[ReleasePlan](ctx, agent,
	"Produce the release plan as a Markdown file artifact.")
if err != nil {
	return err
}
fmt.Printf("%s (%s)\n%s\n", plan.Filename, result.RunID, plan.Content)
```

See the runnable [`structured-output`](./examples/structured-output) example
and the [structured-output contract](./docs/structured-output.md).

## Team agent example

Team shape and routing remain host policy. Configure ordinary Runner values as
local or remote delegation targets, attach one `a2adelegation.Service` to the
leader, and consume every role update from the leader's single Event stream:

```go
team, err := a2adelegation.NewService(a2adelegation.Config{
	Agents: []a2adelegation.AgentRef{
		a2adelegation.LocalNamed("plan", "Codex Planner", planner, a2adelegation.Policy{}),
		a2adelegation.LocalNamed("impl", "Claude Code Implementer", implementer, a2adelegation.Policy{}),
		a2adelegation.LocalNamed("review", "Codex Reviewer", reviewer, a2adelegation.Policy{}),
	},
})
if err != nil {
	return err
}
defer team.Close()

leader := adaptor.New(leaderDriver, team.Option())
stream := leader.Stream(ctx, "Plan, implement, and review TASK.md")
for event := range stream.Events() {
	if update, ok := event.(adaptor.SubagentUpdate); ok {
		fmt.Printf("[%s] %s: %s\n", update.Agent, update.Kind, update.Delta)
	}
}
result, err := stream.Result()
```

The complete [`team-agent-workflow`](./examples/showcases/team-agent-workflow)
showcase adds role-specific sandboxes, a structured `PLAN.md` artifact,
workspace auditing, and a CopilotKit UI with live subagent cards. Start it with:

```bash
./examples/showcases/team-agent-workflow/start-all.sh claude
```

## Results and errors

A successful invocation returns `*Result, nil`. A completed business failure returns a typed `*RunError` that carries the available `Result`; infrastructure failures remain ordinary wrapped Go errors.

```go
result, err := agent.Run(ctx, prompt)
if err != nil {
	var runErr *adaptor.RunError
	if errors.As(err, &runErr) {
		log.Printf("run failed: %s; partial summary: %s", runErr.Reason, runErr.Result.Summary)
	}
	return err
}
```

`Result` keeps each output layer independent:

- `Text` is final assistant-facing text.
- `Summary` is a short host-facing label.
- `Raw()` contains complete stdout, stderr, and the official provider terminal payload when observed.
- `Transcript()` contains normalized protocol entries parsed by the Driver.
- `Services()` reports runtime services actually observed for the run.
- `Decode()` reads validated structured output.

Use `RunAs[T]` for structured output; advanced schema customization stays in
the dedicated contract document.

## Inspect and profiles

`Agent.Inspect()` provides read-only environment, model, quota, schema, and skill probes. Unsupported probes report that honestly. Profile operations are explicit:

```go
environment, err := agent.Inspect().Environment(ctx)
models, err := agent.Inspect().Models(ctx)
state, err := agent.ProfileState(ctx)
synced, err := agent.SyncProfile(ctx)
```

## Packages

| Package | Purpose |
|---|---|
| [`driver`](./driver) | Driver SPI and provider-facing contracts. |
| [`codex`](./codex), [`claude`](./claude), [`cursor`](./cursor), [`codebuddy`](./codebuddy) | Built-in Drivers and their Config types. |
| [`tool`](./tool), [`skill`](./skill), [`mcp`](./mcp), [`profile`](./profile) | Consumer capability and resource vocabularies. |
| [`threadstore`](./threadstore), [`memory`](./memory) | Thread persistence contract and in-memory implementation. |
| [`bridges`](./bridges) | SSE, AG-UI, A2A, and subagent-stream protocol bridges. |
| [`clients/a2a`](./clients/a2a) | Host-oriented A2A client. |
| [`hosttools`](./hosttools) | Optional delegation and event-recording components. |
| [`adaptertest`](./adaptertest) | Driver conformance suite. |

## Examples

- [`quickstart`](./examples/quickstart): construct an Agent and run one prompt.
- [`inspect`](./examples/inspect): environment, models, quota, schema, skills, and profile state.
- [`threads`](./examples/threads): continue, resume-only, fork, and checkpoint inspection.
- [`tools`](./examples/tools): expose a typed Go function to a real local provider without managing MCP.
- [`skills`](./examples/skills): live skill resolution and materialization.
- [`profiles`](./examples/profiles): provider profile resources and synchronization.
- [`streaming`](./examples/streaming): typed Event consumption and cancellation.
- [`structured-output`](./examples/structured-output): typed JSON output.
- [`web-chat`](./examples/web-chat): SSE/AG-UI server, with [`aguiclient`](./examples/web-chat/aguiclient) and [`copilotkit`](./examples/web-chat/copilotkit) front ends.
- [`a2a-server`](./examples/a2a-server): publish an Agent through A2A and call it with the A2A client.
- [`showcases/team-agent-workflow`](./examples/showcases/team-agent-workflow): leader → plan → implementation → review, with a one-command CopilotKit verifier.

Examples that invoke a provider require its local CLI and authentication. They do not make paid calls during ordinary repository tests.

## Boundaries

The core library does not provide an HTTP or gRPC server, queue, scheduler, tenant system, authorization system, database, or automatic agent router. Protocol serving belongs in bridges and host applications; team roles and workflow policy remain host concerns.

## Documentation

- [Documentation map](./docs/README.md)
- [API reference](./docs/api-reference.md)
- [Streaming guide](./docs/streaming.md)
- [Host-defined Tools](./docs/tools.md)
- [Structured output](./docs/structured-output.md)
- [A2A integration](./docs/a2a.md)
- [Public errors](./docs/public-errors.md)
