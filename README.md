# agent-adaptor

[简体中文](./README.zh-CN.md) | [日本語](./README.ja.md) | [한국어](./README.ko.md) | [Deutsch](./README.de.md)

`agent-adaptor` is an SDK that offers a small, intuitive API for driving different agent flavors — `Codex`, `Claude Code`, `Cursor`, `CodeBuddy` — through one interface, plus a range of capabilities beyond plain invocation.

```go
agent := adaptor.New(codex.Driver(codex.Config{Model: "gpt-5.6-sol"}))
result, err := agent.Run(ctx, "Fix the failing tests")
```

Switching to Claude Code only means swapping the Driver in the constructor; the rest of your code stays untouched.

## Capability overview

- **Unified configuration**: one API controls skills, MCP, system prompts, models, sandboxing, tools, and approvals across agents.
- **Streaming responses**: optional streaming output that distinguishes reasoning, text output, tool calls, and decision requests as the scenario requires.
- **Conversation management**: seamless continuation and forking. Use your own business ID (a ticket number, a user ID) as the conversation key without dealing with the underlying session bookkeeping.
- **Human decisions**: answer questions, intercept dangerous commands, and confirm plans through callbacks or events. A built-in decision write-back mechanism lets decisions be persisted in the cloud rather than only in the local process.

## Advanced features

- **Structured output**: define a Go struct, call `RunAs[T]`, and the agent runs under a constraint that returns a populated object.
- **Multi-protocol decoration**: built-in A2A/AGUI decoration turns an Agent into a standard agent with SSE + AGUI streaming in one line, so a custom front end or client is all you need for a complete agent service (a runnable CopilotKit front end is included).
- **Multi Agent**: cross-Driver team agents — for example Codex as the leader agent autonomously coordinating a Plan Agent (Codex), a Coding Agent (Claude), and a Reviewer Agent (Cursor), with all progress and output aggregated into the leader's event stream (see the examples/showcases/team-agent-workflow showcase).
- **Agent isolation**: copy the machine's agent configuration and login state into a dedicated directory so changes never affect the agent you use locally. Running several Codex/Claude Code instances in parallel for concurrent development or different roles becomes trivial.

## Install

```bash
go get github.com/agent-dance/agent-adaptor
```

Requires Go 1.26.5 or later.

Important: **the matching agent CLI must already be installed and authenticated at run time.**

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

The four built-in Drivers are constructed the same way, each with its own `Config`:

```go
codexAgent := adaptor.New(codex.Driver(codex.Config{}))
claudeAgent := adaptor.New(claude.Driver(claude.Config{}))
cursorAgent := adaptor.New(cursor.Driver(cursor.Config{}))
codeBuddyAgent := adaptor.New(codebuddy.Driver(codebuddy.Config{}))
```

## Streaming execution

`Stream` unfolds one invocation into a single strongly typed event stream and yields a `Result` at the end:

```go
stream := agent.Stream(ctx, "Explain the patch that is about to be committed")
defer stream.Cancel()

for event := range stream.Events() {
	switch event := event.(type) {
	case adaptor.TextDelta:
		fmt.Print(event.Text)
	case adaptor.Thinking:
		fmt.Fprint(os.Stderr, event.Text)
	case adaptor.ToolCall:
		if event.Phase == adaptor.PhaseStart {
			fmt.Printf("\n[tool call: %s]\n", event.Name)
		}
	case *adaptor.ApprovalRequest:
		_ = event.Approve(ctx)
	case adaptor.Dropped:
		log.Printf("backpressure dropped %d incremental events", event.Count)
	}
}

result, err := stream.Result()
```

Text, reasoning, tool calls and their results, process information, lifecycle, subagent progress, and approval requests all travel on this one stream. There is no second channel.

Call `Cancel()` when you stop consuming early; it is idempotent.

## Human approval and sandboxing

Sandbox strength, network and browser tools, and approval modes live in the same `Policy`. The constructor sets defaults, and `Run` / `Stream` can override the whole policy per call:

```go
reviewer := adaptor.New(
	claude.Driver(claude.Config{}),
	adaptor.WithPolicy(adaptor.Policy{
		Sandbox:   adaptor.ReadOnly,    // read-only workspace, suited to review and planning roles
		WebSearch: adaptor.FeatureDeny, // explicitly disable web search
		Browser:   adaptor.FeatureDeny,
		Approvals: adaptor.ApprovalPolicy{
			Permission: adaptor.ApprovalAsk, // hand dangerous commands to a human
			PlanReview: adaptor.ApprovalAsk,
			Question:   adaptor.QuestionAsk, // questions are auto-denied by default
			Timeout:    2 * time.Minute,
			OnTimeout:  adaptor.FallbackAbort,
		},
	}),
)
```

The sandbox has three levels — `ReadOnly`, `WorkspaceWrite`, `Unrestricted` — and presets such as `PolicyReadOnly` are just shortcuts that set `Sandbox` alone. When the selected Driver does not support one of these dimensions, you get an explicit error before the process starts instead of a silent downgrade.

Approvals have two consumption shapes; pick one. Attaching a callback gives you the callback shape, which suits CLIs and unattended runs:

```go
agent := adaptor.New(
	claude.Driver(claude.Config{}),
	adaptor.OnApproval(func(ctx context.Context, req *adaptor.ApprovalRequest) error {
		switch req.Kind {
		case adaptor.ApprovalPermission:
			return req.Approve(ctx)
		case adaptor.ApprovalQuestion:
			return req.Answer(ctx, "Use PostgreSQL")
		default:
			return req.Deny(ctx, "the plan needs human confirmation")
		}
	}),
)
```

For unattended runs, `adaptor.ApproveAll()` and `adaptor.DenyAll(reason)` are ready to use.

Without a callback you get the event shape: the request arrives on the event stream as an `*adaptor.ApprovalRequest` carrying its own responder, so it can be parked and later resolved by any goroutine or by another HTTP request — exactly what web scenarios need:

```go
for event := range stream.Events() {
	switch event := event.(type) {
	case *adaptor.ApprovalRequest:
		pending.Add(threadKey, event) // park the request and push it to the front end
	case adaptor.Notice:
		// The SDK broadcasts every settled decision, including policy auto-approvals
		// and timeout fallbacks, so the host never has to reconcile its pending list.
		if event.Kind == adaptor.NoticeApprovalResolved {
			if id, ok := event.Data["request_id"].(string); ok {
				pending.Remove(threadKey, id)
			}
		}
	}
}
```

`pending` is the host's own storage; once the front end has the request, it resolves the decision in a separate HTTP request:

```go
func (h *host) resolveDecision(w http.ResponseWriter, r *http.Request) {
	req := h.pending.Take(threadKey, requestID)
	if err := req.Approve(r.Context()); err != nil {
		sse.WriteApprovalError(w, err) // already resolved or expired → 410, Kind mismatch → 400
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
```

Responses are exactly-once: duplicate responses, Kind mismatches, and finished runs all return stable errors (`ErrApprovalResolved`, `ErrApprovalKindMismatch`, `ErrApprovalExpired`), and a zero-value request never blocks forever. When nobody answers, `OnTimeout` from `Policy.Approvals` takes over; a rejection follows `OnReject`. Where parked requests live is up to the host and is not limited to process memory.

A complete, runnable web HITL path is in [`web-chat/copilotkit`](./examples/web-chat/copilotkit): two endpoints, `/decision/pending` and `/decision/resolve`, with pending decisions surviving a page refresh.

## Multi-turn conversations

Agents are stateless by default. When you need conversation continuity, inject a store:

```go
agent := adaptor.New(
	claude.Driver(claude.Config{}),
	adaptor.WithThreadStore(memory.NewStore()),
)
defer agent.Close(context.Background())

thread := agent.Thread("tenant-42/issue-123")        // continue the mapped conversation if it exists, otherwise create it
result, err := thread.Run(ctx, "Keep investigating this problem")

only := agent.Thread("tenant-42/issue-123", adaptor.ResumeOnly()) // resume only, never create
branch := thread.Fork("tenant-42/issue-123/plan-b")               // fork from the current progress
```

A few conventions:

- **The conversation key is the host's own string**; the SDK stores and compares it verbatim. Start a brand-new conversation with a new key — the SDK offers no entry point for rebinding an old key to a new conversation.
- **One Thread runs at most one invocation at a time**, guaranteed by a lease, so an expired run never overwrites newer state.
- **Compatibility is checked before resuming**: the Driver, model, resolved workspace, configuration, skills, and MCP all feed the fingerprint, and drift in any one of them prevents an incorrect conversation reuse.
- **Failures do not pollute state**: non-zero exits, protocol errors, and cancellations produce no valid checkpoint, so the previously healthy conversation record stays as it was.
- **Persistent processes are reused by default**: on Windows, macOS, and Linux, Claude, CodeBuddy, and Codex reuse one process across turns of an explicit Thread. Add `adaptor.WithSpawn()` when one turn or every turn needs a fresh process. Cursor and stateless calls always start a new process per turn. Runs after `Close` return `ErrAgentClosed`.

Use `memory.NewStore()` for single-process scenarios; implement `threadstore.Store` when you need durability.

## Structured output

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

The schema is derived from the Go type and prefers each provider's native schema enforcement. When the current transport or policy does not support it, execution falls back to prompt constraints plus local validation automatically; only when neither is available does the run fail before starting. The return values include both the typed value and the full audit `Result`.

For details, see the [`structured-output` example](./examples/structured-output) and the [structured output documentation](./docs/structured-output.md).

## Options and resources

Options share one vocabulary, and their scope is separated by type at compile time:

| Type | Where it applies |
|---|---|
| `Option` | `adaptor.New` only |
| `CallOption` | `Run` / `Stream` only |
| `SharedOption` | Both; the call site overrides the constructor |

There is a single merge rule: the nearer value wins, skills append, and everything else replaces or merges according to its own contract.

The same set of options covers the main configuration surface of every agent:

| What you want to control | What to use |
|---|---|
| Model | `WithModel` |
| System prompt | `WithInstructions` |
| Working directory | `WithWorkspace`, or `WithWorkspaceSpec` for isolated work trees |
| skills | `WithSkills` with `skill.Dir` / `skill.FS` / `skill.Inline` / `skill.Key` / `skill.Require` |
| MCP | `WithMCP` with `mcp.Stdio` / `mcp.HTTP` / `mcp.SSE` |
| Sandbox, network, browser tools, approvals | `WithPolicy`, plus `OnApproval` when interactive |
| Configuration directory and resources | `WithProfile`, `WithProfileResources` |
| Timeout, audit metadata, caller identity | `WithTimeout`, `WithMetadata`, `WithIdentity` |
| Conversation persistence | `WithThreadStore` |

```go
agent := adaptor.New(
	codex.Driver(codex.Config{}),
	adaptor.WithModel("gpt-5.4"),
	adaptor.WithInstructions("You are this repository's reviewer: read the code only, state the conclusion before the evidence."),
	adaptor.WithSkills(skill.Dir("./skills/review")),
	adaptor.WithMCP(mcp.Stdio("repo-tools", "repo-mcp", mcp.Args("serve"))),
	adaptor.WithProfile(profile.Dedicated("./profiles/reviewer")),
	adaptor.WithTimeout(10*time.Minute),
)

result, err := agent.Run(ctx, "Review this change",
	adaptor.WithModel("gpt-5.4-mini"),
	adaptor.WithSkills(skill.Require(skill.Dir("./skills/security"), "this run must pass the security review")), // appends, does not displace the default skills
	adaptor.WithMetadata("request_id", requestID),
)
```

The same configuration with a different Driver is a different Agent; when a Driver does not support one of the capabilities, you get an explicit error before startup rather than a silent omission.

```go
codexReviewer := adaptor.New(codex.Driver(codex.Config{}), reviewerOptions...)
claudeReviewer := adaptor.New(claude.Driver(claude.Config{}), reviewerOptions...)
```

## Host-defined Tools

Extend an Agent with typed Go functions directly, without constructing or maintaining an MCP server yourself:

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

`WithTools` is construction-only and replaces the Tool set as a whole. Schemas are inferred from the handler's Go types by default, and an explicit standard JSON Schema can be supplied instead. `tool.Reject(code, message)` reports a business failure that is safe to show the model; only package-minted rejections pass that boundary, while ordinary errors, lookalikes, and panics are sanitized. Every Tool used by a stateful Thread needs a `tool.Revision` so that changes in handler behavior participate in resume compatibility.

MCP is only the internal delivery mechanism here: existing or remote MCP servers still go through `WithMCP`, and built-in Drivers materialize Tools into an SDK-owned isolated profile, leaving the native profile you configured untouched. Each Agent receives an unpredictable bearer-token environment-variable name, and another MCP server cannot alias that credential carrier. For lifecycle, schema, error, security, and Thread semantics see the [host-defined Tools contract](./docs/tools.md).

## Agent isolation

`WithProfile` decides which provider configuration directory an Agent uses. `profile.CloneNative` clones an independent profile from the machine's native configuration and can optionally bring along settings, MCP, and skills; the login state is shared through a link instead of copying tokens:

```go
worker := adaptor.New(
	claude.Driver(claude.Config{}),
	adaptor.WithProfile(profile.CloneNative("/var/agents/worker-1",
		profile.CopySettings(),
		profile.CopyMCP(),
		profile.CopySkills(),
		profile.LinkAuth(), // share the machine's login state via a symlink, so local re-logins are picked up automatically
	)),
)
```

One CLI can therefore run several instances in parallel, per role or per task, with configuration changes isolated from each other and from the `~/.claude` and `~/.codex` you use locally:

```go
isolated := func(dir string) adaptor.Option {
	return adaptor.WithProfile(profile.CloneNative(dir,
		profile.CopySettings(), profile.LinkAuth()))
}

planner := adaptor.New(codex.Driver(codex.Config{}),
	isolated("/var/agents/planner"),
	adaptor.WithPolicy(adaptor.PolicyReadOnly),
)
implementer := adaptor.New(claude.Driver(claude.Config{}),
	isolated("/var/agents/implementer"),
	adaptor.WithWorkspace("/repo/worktrees/feature-x"),
)
```

Three other choices exist: `profile.Native()` uses the machine's native configuration directly; `profile.Dedicated(dir)` pins a directory you manage yourself; `profile.CloneFrom(src, dst, ...)` derives from a template directory. A profile participates in the conversation fingerprint, so it can only be a construction option and cannot be switched per call.

To see what declared resources actually materialized and whether the Driver truly accepts them, read with `agent.ProfileState(ctx)` and materialize with `agent.SyncProfile(ctx)`; both report only what was actually observed. See the [`profiles` example](./examples/profiles) for a full walkthrough.

## Results and errors

Success returns `*Result, nil`. Failure travels only through Go's `error`: a run that completed but failed on the business level returns a `*RunError` carrying whatever `Result` is available, while infrastructure failures are ordinary wrappable errors.

```go
result, err := agent.Run(ctx, prompt)
if err != nil {
	var runErr *adaptor.RunError
	if errors.As(err, &runErr) {
		log.Printf("run failed: %s; available summary: %s", runErr.Reason, runErr.Result.Summary)
	}
	return err
}
```

Each output layer of `Result` stays free of the others:

| Field | Contents |
|---|---|
| `Text` | The final user-facing answer text |
| `Summary` | A short summary suited to lists, logs, and issue comments |
| `Raw()` | Complete stdout and stderr, plus each provider's official terminal payload |
| `Transcript()` | Normalized entries the Driver parsed from the official protocol |
| `Services()` | Runtime services actually observed during this run |
| `Decode()` | Validated structured output |
| `Usage` / `Model` / `Provider` / `Metadata` | Usage and audit information |

`Text` never mixes in raw stdout, and it never automatically appends the summary or a provider's terminal payload. What you get from `Run` and from `Stream.Result()` is field-for-field equivalent.

## Integrating into applications

**Web front ends**: one line wraps an Agent into an `http.Handler` speaking the AG-UI protocol, so AG-UI compatible clients (such as CopilotKit) can connect directly:

```go
mux.Handle("/agent", sse.Handler(agent, sse.Options{
	Protocol: sse.AGUI,
}))
```

**A2A**: `bridges/a2a` publishes any Runner as an A2A server, leaving routing, authentication, and TLS to the host:

```go
server := bridgea2a.NewServer(agent, bridgea2a.ServerOptions{
	AgentCard: bridgea2a.AgentCard{
		Name:        "Local coding agent",
		Description: "Runs coding tasks through agent-adaptor",
		Version:     "1.0.0",
		URL:         "https://host.example/a2a",
	},
	Session: bridgea2a.ThreadByContextID(), // the remote contextID maps stably to a local Thread key
	Options: []adaptor.CallOption{adaptor.WithPolicy(adaptor.PolicyWorkspaceWrite)},
})

mux.Handle("/.well-known/agent-card.json", server.AgentCardHandler())
mux.Handle("/a2a", server.Handler())
```

Use `clients/a2a` to call a remote A2A agent. It returns A2A tasks, messages, and artifacts, and never pretends that a remote protocol task has a local CLI's stdout or `Result`:

```go
client := clienta2a.New(clienta2a.Options{
	AgentCardURL: "https://remote.example/.well-known/agent-card.json",
	Auth:         clienta2a.BearerTokenFromEnv("REMOTE_A2A_TOKEN"),
})
defer client.Close()

task, err := client.Send(ctx, clienta2a.SendRequest{
	Message: clienta2a.Message{
		Role:  "user",
		Parts: []clienta2a.Part{{Kind: clienta2a.PartText, Text: "Review this change"}},
	},
})
```

Use `SendStream` / `Subscribe` when you need the intermediate steps. Whether reasoning, tool calls, approval events, or diagnostic fields are exposed outward is controlled by `ExposurePolicy`, which defaults to minimal exposure.

## Multi-agent collaboration

`agent-adaptor` supports cross-Driver multi-agent collaboration over the standard A2A protocol (which therefore also covers any remote A2A agent).

The value of cross-Driver collaboration is preserving the fit between a model and its native `Harness`: GPT models perform better on Codex, and Claude models are stronger in Claude Code. `agent-adaptor` is therefore designed to let each model collaborate from the harness that suits it best, rather than settling for one generic harness that supports many models but performs poorly just to enable multi-model collaboration.

The core code looks like this:

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
```

The complete [`team-agent-workflow`](./examples/showcases/team-agent-workflow) adds role-level sandboxes, a structured `PLAN.md` artifact, workspace auditing, and a CopilotKit page with live subagent cards, all started with one command:

```bash
./examples/showcases/team-agent-workflow/start-all.sh claude
```

## Environment probes

`Agent.Inspect()` is a read-only probe used for preflight checks, environment diagnostics, and model selection. Unsupported probes report unsupported explicitly instead of inventing data:

```go
environment, err := agent.Inspect().Environment(ctx) // health status and per-item diagnostics, ready to render
models, err := agent.Inspect().Models(ctx)
quota, err := agent.Inspect().Quota(ctx)
state, err := agent.ProfileState(ctx)                // reports desired versus observed only, changes nothing
synced, err := agent.SyncProfile(ctx)                // explicitly materializes configuration resources
```

## Six nouns

The library's entire public model consists of six nouns:

| Noun | Meaning |
|---|---|
| `Agent` | A fully configured agent, ready to run once constructed |
| `Thread` | A conversation identified by a host key, resumable and forkable |
| `Stream` | One invocation in progress |
| `Event` | One strongly typed occurrence during an invocation |
| `Result` | The final result and audit information of one invocation |
| `Driver` | An integration for one agent CLI, relevant only to extension authors |

The accompanying constraints are: one constructor, one option merge rule, one execution pipeline, one event stream, and one failure verdict.

## Packages

| Package | Purpose |
|---|---|
| [`driver`](./driver) | The Driver SPI, used when integrating a new agent |
| [`codex`](./codex), [`claude`](./claude), [`cursor`](./cursor), [`codebuddy`](./codebuddy) | Built-in Drivers and their Config types |
| [`tool`](./tool), [`skill`](./skill), [`mcp`](./mcp), [`profile`](./profile) | Consumer-facing capability and resource vocabularies |
| [`threadstore`](./threadstore), [`memory`](./memory) | The Thread persistence contract and its in-memory implementation |
| [`bridges`](./bridges) | SSE, AG-UI, A2A, and subagent-stream protocol bridges |
| [`clients/a2a`](./clients/a2a) | The A2A client |
| [`hosttools`](./hosttools) | Optional delegation orchestration and event recording components |
| [`adaptertest`](./adaptertest) | The Driver conformance suite |

To integrate your own agent CLI: implement `driver.Driver`, get `adaptertest` passing, and from then on it has the same higher-level capabilities as the built-in Drivers.

## Examples

- [`quickstart`](./examples/quickstart): construct an Agent and run one prompt.
- [`streaming`](./examples/streaming): event consumption and cancellation.
- [`threads`](./examples/threads): continuation, resume-only, forking, and checkpoint auditing.
- [`structured-output`](./examples/structured-output): typed JSON output.
- [`tools`](./examples/tools): expose a typed Go function to a real local provider without managing MCP yourself.
- [`skills`](./examples/skills) / [`profiles`](./examples/profiles): skill resolution and materialization, configuration resources, and synchronization.
- [`inspect`](./examples/inspect): environment, models, quota, schema, skills, and profile state.
- [`web-chat`](./examples/web-chat): an SSE/AG-UI server with two front ends, [`aguiclient`](./examples/web-chat/aguiclient) and [`copilotkit`](./examples/web-chat/copilotkit).
- [`a2a-server`](./examples/a2a-server): publish and call an Agent through A2A.
- [`showcases/team-agent-workflow`](./examples/showcases/team-agent-workflow): planning, implementation, and review joined into one pipeline.

Examples that make real calls depend on the corresponding CLI and its login state. The repository's ordinary tests never produce paid calls.

## Boundaries

The core library does not provide an HTTP/gRPC server, queue, scheduler, multi-tenancy, authorization, or database, and it does not decide which agent a task should be dispatched to. Protocol serving belongs to bridges and host applications; team roles and workflow policy belong to the host.

## Documentation

- [Documentation map](./docs/README.md)
- [API reference](./docs/api-reference.md)
- [Host-defined Tools](./docs/tools.md)
- [Streaming guide](./docs/streaming.md)
- [Structured output](./docs/structured-output.md)
- [Run policy: sandbox, approvals, timeouts](./docs/run-policy.md)
- [A2A integration](./docs/a2a.md)
- [Public errors](./docs/public-errors.md)

## License

Unless otherwise noted, this repository is licensed under the
[Apache License, Version 2.0](./LICENSE). Third-party material retains its own
license and attribution; see [Third-Party Notices](./THIRD_PARTY_NOTICES.md).

Codex, Claude, Cursor, CodeBuddy, and other product names are trademarks of
their respective owners. They are used only to identify supported integrations;
this project is not affiliated with or endorsed by those owners.
