# agent-adaptor

[简体中文](./README.zh-CN.md) | [日本語](./README.ja.md) | [한국어](./README.ko.md) | [Deutsch](./README.de.md)

`agent-adaptor` v2 is an SDK that offers a small, intuitive API for driving different agent flavors — `Codex`, `Claude Code`, `Cursor`, `CodeBuddy` — through one interface, plus a range of capabilities beyond plain invocation.

```go
agent := adaptor.New(codex.Driver(codex.Config{Model: "gpt-5.4"}))
result, err := agent.Send(ctx, "Fix the failing tests").Result()
```

Switching to Claude Code only means swapping the Driver in the constructor; the rest of your code stays untouched.

## Capability overview

- **Unified configuration**: one API controls skills, MCP, system prompts, models, sandboxing, tools, and approvals across agents.
- **Streaming responses**: optional streaming output that distinguishes reasoning, text output, tool calls, and decision requests as the scenario requires.
- **Conversation management**: seamless continuation and forking. Use your own business ID (a ticket number, a user ID) as the conversation key without dealing with the underlying session bookkeeping.
- **Human decisions**: answer questions, intercept dangerous commands, and confirm plans through callbacks or events. The live responder can be retained by the host and answered from another HTTP request; approval audit storage belongs to the host.

## Advanced features

- **Structured output**: define a Go struct, call `RunAs[T]`, and the agent runs under a constraint that returns a populated object.
- **Multi-protocol decoration**: built-in A2A/AGUI decoration turns an Agent into a standard agent with SSE + AGUI streaming in one line, so a custom front end or client is all you need for a complete agent service (a runnable assistant-ui front end is included).
- **Teams**: a host-selected leader coordinates members on any Driver — for example Codex leading a Plan member (Codex), a Coding member (Claude) and a Reviewer member (Cursor) — or Remote A2A members, with task updates and worker approvals aggregated into the leader's Event stream. See the [team guide](./docs/team.md) and the examples/showcases/team-agent-workflow showcase.
- **Agent isolation**: clone the machine's agent configuration into a dedicated directory and link its login state, keeping configuration changes isolated. Running several Codex/Claude Code instances in parallel for concurrent development or different roles becomes trivial.

## Install

```bash
go get github.com/agent-dance/agent-adaptor/v2
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

	adaptor "github.com/agent-dance/agent-adaptor/v2"
	"github.com/agent-dance/agent-adaptor/v2/codex"
)

func main() {
	agent := adaptor.New(
		codex.Driver(codex.Config{Model: "gpt-5.4"}),
		adaptor.WithWorkspace("/path/to/repository"),
	)

	result, err := agent.Send(context.Background(), "Fix the failing tests").Result()
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

`Send` starts one execution and returns a `Stream`. Read `Events()` for live progress or call `Result()` to wait synchronously:

```go
stream := agent.Send(ctx, "Explain the patch that is about to be committed")
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

Text, reasoning, tool calls and their results, process information, lifecycle, message outcomes, and approval requests all travel on this one stream. Each provider turn emits `TurnEnd`. The last event is `Done{Result, Err}`, published after validation, checkpoint handling, and cleanup; it contains the same result pointer and error as `stream.Result()`. `RunFinished` is informational.

Call `Cancel()` when you stop consuming early; it is idempotent.

## Messages during a run

Send extra guidance to the returned Stream, using a stable host ID for deduplication:

```go
stream := agent.Send(ctx, "Fix the failing tests")
err := stream.Send(ctx, "Keep the public API unchanged",
	adaptor.MessageID("review-comment-42"),
	adaptor.From(adaptor.Author{Kind: adaptor.AuthorHuman, ID: "reviewer"}),
	adaptor.When(adaptor.Soon),
)
```

`Soon` is the default: deliver at the earliest supported boundary, or continue at turn end. `AfterTurn` waits for a continuation turn. `Interrupt` aborts the active turn and continues within the same execution; unsupported Drivers reject it with `ErrMessageUnsupported`. See the [Driver matrix](./docs/streaming.md#12-message-delivery-capabilities).

A nil send error means admitted. `MessageUpdate` reports accepted, delivered, or failed; failure includes the original text. `Result.Messages()` records final outcomes, and `Undelivered()` returns failures. Admission errors are `*MessageError` with ID, text, reason, and a wrapped cause; a finished execution matches `ErrRunFinished`. A Stream send context can withdraw an unclaimed message; after a Driver claims it, the delivery acknowledgement decides its outcome.

`MessageID` deduplicates within the Thread, retaining up to 1000 delivered IDs for seven days. Recovery needs a durable Thread store. A crash after provider acceptance but before receipt persistence can replay a message; deduplication does not close that crash window.

## Standby

Use task mode (`Thread.Send`) when each request needs one bounded execution result. Use Standby for chat or an IM bot that keeps receiving messages and producing replies:

```go
func openChat(serverCtx context.Context, agent *adaptor.Agent) (adaptor.Standby, error) {
	sb, err := agent.Thread("chat-42").Standby(serverCtx)
	if err != nil {
		return nil, err
	}
	events := sb.Events() // subscribe before accepting input
	go func() {
		for event := range events {
			switch e := event.(type) {
			case adaptor.TurnEnd:
				if e.Err == nil && e.Result != nil {
					fmt.Println(e.Result.Text)
				}
			case adaptor.Done:
				if e.Err != nil {
					log.Print(e.Err)
				}
			}
		}
	}()
	return sb, nil
}
```

Open the Standby with a server-lifetime context. An HTTP handler can then call `sb.Send(r.Context(), text, adaptor.MessageID(id))`: that context bounds admission only, and accepted work lives under `serverCtx`. Input joins an active execution, or starts one when idle. Execution options such as `WithModel` queue a separate execution after current work.

Standby has `Send`, `Events`, and `Cancel`, with no overall `Result()`. Each provider turn emits `TurnEnd`; each execution emits `Done`. `sb.Cancel()` stops current processing and fails queued messages with `MessageCancelled`, then keeps accepting messages. Cancel `serverCtx` to end Standby and close its events. Before the first `Events()` call its events are discarded.

A live Standby owns the Thread even when idle; task-mode sends return `ErrThreadBusy`. Reopening the same key on the same Agent returns the existing live handle, whose lifetime still belongs to the first context.

Failed executions leave Standby alive. Only classified rate-limit, network, or provider-server failures retry, at most once. Undelivered `run_failed` messages carry into the next execution under the same IDs: observers can see failed, then delivered. Without a retry, carried input waits for the next host message. See [streaming](./docs/streaming.md) for delivery and recovery limits.

## Human approval and sandboxing

Sandbox strength, search and browser controls, approval modes, and message limits live in the same `Policy`. The constructor sets defaults, and `Send` can override the whole policy per execution:

```go
reviewer := adaptor.New(
	codex.Driver(codex.Config{}),
	adaptor.WithPolicy(adaptor.Policy{
		Sandbox:   adaptor.ReadOnly,
		WebSearch: adaptor.FeatureDeny,
		Approvals: adaptor.ApprovalPolicy{
			Permission: adaptor.ApprovalAutoApprove,
		},
	}),
)
```

The sandbox has three levels — `ReadOnly`, `WorkspaceWrite`, `Unrestricted` — and presets such as `PolicyReadOnly` are just shortcuts that set `Sandbox` alone. When the selected Driver does not support one of these dimensions, you get an explicit error before the process starts instead of a silent downgrade.

Approvals have two consumption shapes; pick one. Attaching a callback gives you the callback shape, which suits CLIs and unattended runs:

```go
agent := adaptor.New(
	claude.Driver(claude.Config{}),
	adaptor.WithPolicy(adaptor.Policy{
		Approvals: adaptor.ApprovalPolicy{Question: adaptor.QuestionAsk},
	}),
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

For callback-based policies, `adaptor.ApproveAll()` and `adaptor.DenyAll(reason)` are ready to use. They handle requests already routed to the callback; they do not enable unsupported approval modes. Claude and CodeBuddy support interactive requests. Codex supports permission approval through app-server, including approval for MCP tools; it does not support plan review or questions requiring input. Cursor does not advertise `Ask`.

Without a callback you get the event shape: the request arrives on the event stream as an `*adaptor.ApprovalRequest` carrying its own responder, so it can be parked and later resolved by any goroutine or by another HTTP request — exactly what web scenarios need:

```go
func consumeApprovals(ctx context.Context, stream adaptor.Stream,
	publish func(*adaptor.ApprovalRequest)) (*adaptor.Result, error) {
	for event := range stream.Events() {
		if req, ok := event.(*adaptor.ApprovalRequest); ok {
			publish(req) // the host retains the live responder for its UI
		}
	}
	return stream.Result()
}
```

The host can later call `req.Approve(r.Context())`, `req.Deny(r.Context(), reason)`, or `req.Answer(r.Context(), text)` from a companion endpoint. `NoticeApprovalResolved` carries `Data["request_id"]` so the UI can retire a pending card. Storing an audit record does not serialize the live responder.

Responses are exactly-once: duplicate responses, Kind mismatches, and finished runs all return stable errors (`ErrApprovalResolved`, `ErrApprovalKindMismatch`, `ErrApprovalExpired`), and a zero-value request never blocks forever. Without an `OnApproval` handler, a request that arrives before `Events()` waits until the caller subscribes or calls `Result()`; a Result-only caller gets the timeout fallback immediately with a lifecycle notice. Otherwise, when nobody answers before the deadline, `OnTimeout` from `Policy.Approvals` takes over; a rejection follows `OnReject`. Where parked requests live is up to the host and is not limited to process memory.

A complete, runnable web HITL path is in [`web-chat/assistant-ui`](./examples/web-chat/assistant-ui): decisions render as Approve/Deny on `dec.*` tool calls, and the host-owned `/decision/pending` and `/decision/resolve` endpoints answer the live request exactly once.

## Multi-turn conversations

Direct `Agent.Send` calls are independent, like sending on a new anonymous Thread each time. Named Threads use an Agent-local in-memory store by default. Inject `WithThreadStore` to share a store or provide durable storage:

```go
agent := adaptor.New(
	claude.Driver(claude.Config{}),
	adaptor.WithThreadStore(memory.NewStore()),
)
defer agent.Close(context.Background())

thread := agent.Thread("tenant-42/issue-123")        // continue the mapped conversation if it exists, otherwise create it
result, err := thread.Send(ctx, "Keep investigating this problem").Result()

only := agent.Thread("tenant-42/issue-123", adaptor.ResumeOnly()) // resume only, never create
branch := thread.Fork("tenant-42/issue-123/plan-b")               // fork from the current progress
```

A few conventions:

- **The conversation key is the host's own string**; the SDK stores and compares it verbatim. Start a brand-new conversation with a new key — the SDK offers no entry point for rebinding an old key to a new conversation.
- **One Thread has one writer**, guaranteed by a lease. Task-mode `thread.Send` fails through its Stream with `ErrThreadBusy` during another execution or a live Standby. Add input to the existing Stream or Standby instead.
- **Compatibility is checked before resuming**: the Driver, model, resolved workspace, configuration, skills, and MCP all feed the fingerprint, and drift in any one of them prevents an incorrect conversation reuse.
- **Failures do not pollute state**: non-zero exits, protocol errors, and cancellations produce no valid checkpoint, so the previously healthy conversation record stays as it was.
- **Persistent processes are reused by default**: on Windows, macOS, and Linux, Claude, CodeBuddy, and Codex reuse one process across turns of an explicit Thread. Add `adaptor.WithSpawn()` when one turn or every turn needs a fresh process. Cursor and stateless calls always start a new process per turn. New sends after `Agent.Close` begins return `ErrAgentClosed`.

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
| `CallOption` | `Agent.Send`, `Thread.Send`, or `Standby.Send` |
| `SharedOption` | Construction and execution defaults; the call site overrides the constructor |
| `MessageOption` | `From`, `MessageID`, `When`; also accepted as `CallOption`, and the only options `Stream.Send` accepts |

There is a single merge rule: the nearer value wins, skills append, and everything else replaces or merges according to its own contract.

The same set of options covers the main configuration surface of every agent:

| What you want to control | What to use |
|---|---|
| Model | `WithModel` |
| System prompt | `WithInstructions` |
| Working directory | `WithWorkspace`, or `WithWorkspaceSpec` for isolated work trees |
| skills | `WithSkills` with `skill.Dir` / `skill.FS` / `skill.Inline` / `skill.Key` / `skill.Require` |
| MCP | `WithMCP` with `mcp.Stdio` / `mcp.HTTP` / `mcp.SSE` |
| Sandbox, search, browser tools, approvals, message limits | `WithPolicy`, plus `OnApproval` when interactive |
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

result, err := agent.Send(ctx, "Review this change",
	adaptor.WithModel("gpt-5.4-mini"),
	adaptor.WithSkills(skill.Require(skill.Dir("./skills/security"), "this run must pass the security review")), // appends, does not displace the default skills
	adaptor.WithMetadata("request_id", "review-42"),
).Result()
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
	"Match files using a repository-relative glob.",
	func(ctx context.Context, in SearchInput) (SearchOutput, error) {
		files, err := filepath.Glob(in.Query)
		return SearchOutput{Files: files}, err
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

To see what declared resources actually materialized and whether the Driver truly accepts them, read with `agent.ProfileState(ctx)` and materialize with `agent.SyncProfile(ctx)`; both distinguish desired resources from observed materialization. See the [`profiles` example](./examples/profiles) for a full walkthrough.

## Results and errors

Success returns `*Result, nil`. Failure travels only through Go's `error`: a run that completed but failed on the business level returns a `*RunError` carrying whatever `Result` is available, while infrastructure failures are ordinary wrappable errors.

```go
result, err := agent.Send(ctx, prompt).Result()
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
| `Text` | The last provider turn’s user-facing answer text |
| `Summary` | A short summary suited to lists, logs, and issue comments |
| `Raw()` | Complete stdout and stderr, plus the last recognized official terminal payload |
| `Transcript()` | Provider-parsed entries across all turns, plus core-recorded supplemental deliveries |
| `Services()` | Runtime services actually observed during this run |
| `Decode()` | Validated structured output |
| `Turns` / `Usage` | Provider turn count and summed observed usage |
| `Messages()` / `Undelivered()` | Final message outcomes in admission order / failed messages with original text |
| `Model` / `Provider` / `Metadata` | Provider and audit information |

`Text` never mixes in raw stdout, and it never automatically appends the summary or a provider's terminal payload. `Done` and `Stream.Result()` expose the same outcome. Business failures put the available result in `RunError.Result`; their direct result value is nil.

## Integrating into applications

[AG-UI 1.0 bridge contract](./docs/agui.md)

**Web front ends**: one line wraps an Agent into an `http.Handler` speaking the AG-UI protocol, so AG-UI compatible clients (such as assistant-ui with `@ag-ui/client`) can connect directly:

```go
mux.Handle("/agent", sse.Handler(agent, sse.Options{
	Protocol: sse.AGUI,
}))
```

For chat UIs, `agui.RunRegistry` starts a Thread when idle and sends new input to its active Stream, preserving native Stream.Send context semantics: cancellation can withdraw unclaimed input. For recorded runs, `aguisession.Session.MessageHandler` serves `POST /message`: it detaches dispatch from the supplement request context, records accepted input as a user message in the run's history before its terminal, and leaves delivery receipts on the original SSE stream; the assistant-ui example uses it for in-run guidance. The optional `sessionrecorder` records safe terminal fields by default; `WithExposure` explicitly enables additional diagnostics. See the [streaming guide](./docs/streaming.md#8-ag-ui-bridge) for routing and recording.

**A2A**: `bridges/a2a` publishes any Runner as an A2A server, leaving routing, authentication, and TLS to the host:

```go
server := bridgea2a.NewServer(agent, bridgea2a.ServerOptions{
	AgentCard: bridgea2a.AgentCard{
		Name:        "Local coding agent",
		Description: "Runs coding tasks through agent-adaptor",
		Version:     "2.0.0",
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

Use `SendStream` / `Subscribe` when you need the intermediate steps. Whether reasoning, tool calls, approval events, or diagnostic fields are exposed outward is controlled by `ExposurePolicy`, which defaults to minimal exposure. See [A2A integration](./docs/a2a.md) for exposure controls, Remote members and protocol limits.

## Multi-agent collaboration

[`hosttools/team`](./hosttools/team) combines a delegation-first leader with
host-selected local Agents, nested Teams or Remote A2A members. Local members
can use different Drivers. The leader gets four fixed tools; workers report
through the leader, and task/member updates and local worker approvals share
one Event stream.

```go
func Coordinate(ctx context.Context, leader, planner, implementer *adaptor.Agent) (*adaptor.Result, error) {
	t, err := team.New(team.Config{
		Leader: leader,
		Members: []team.Member{
			{Key: "plan", Description: "Plan the change", Runner: planner},
			{Key: "impl", Description: "Implement and test", Runner: implementer},
		},
	})
	if err != nil {
		return nil, err
	}
	defer t.Close(context.Background())
	return t.Thread("issue-123").Send(ctx, "Plan and implement TASK.md").Result()
}
```

Import `context`, `adaptor "github.com/agent-dance/agent-adaptor/v2"` and
`"github.com/agent-dance/agent-adaptor/v2/hosttools/team"`. The host retains
ownership of its Agents. Use `Thread(key).Standby(ctx)` for ongoing input;
`Thread.Send` on a busy scope reports `ErrThreadBusy`. The [team guide](./docs/team.md)
covers Remote members, tools, approvals, limits, durable recovery and lazy Fork.

The [live showcase](./examples/showcases/team-agent-workflow) runs a local
plan → implement → review workflow with role sandboxes and the assistant-ui page:

```bash
./examples/showcases/team-agent-workflow/start-all.sh claude
```

## Environment probes

`Agent.Inspect()` is a read-only probe used for preflight checks, environment diagnostics, and model selection. Unsupported probes report unsupported explicitly instead of inventing data:

```go
environment, err := agent.Inspect().Environment(ctx) // health, CLI version, and effective message capability
models, err := agent.Inspect().Models(ctx)
quota, err := agent.Inspect().Quota(ctx)
state, err := agent.ProfileState(ctx)                // reports desired versus observed only, changes nothing
synced, err := agent.SyncProfile(ctx)                // explicitly materializes configuration resources
```

Built-in environment probes report `Version` and `Messages`. When a detected CLI is below its message minimum, in-run delivery is disabled, pending messages continue through checkpoint resume, and the execution emits one Notice. Unknown versions retain the declared capability and rely on runtime fallbacks. See the [Driver matrix](./docs/streaming.md#12-message-delivery-capabilities).

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
- [`streaming`](./examples/streaming): event consumption, optional timed cancellation, and final-text fallback; [`streaming/chat`](./examples/streaming/chat) streams one task on a named Thread.
- [`threads`](./examples/threads): continuation, resume-only, forking, and checkpoint auditing.
- [`structured-output`](./examples/structured-output): typed JSON output.
- [`tools`](./examples/tools): expose a typed Go function to a real local provider without managing MCP yourself.
- [`skills`](./examples/skills): select a skill and verify its proof file; [`profiles`](./examples/profiles): sync profile resources, optionally probe locally or send a task.
- [`inspect`](./examples/inspect): two live calls followed by environment/model probes, selection of a declared reviewer skill, and profile observation.
- [`web-chat`](./examples/web-chat): an SSE/AG-UI server with a built-in page; [`assistant-ui`](./examples/web-chat/assistant-ui) is the web front end, with durable history, failed-tool rendering, in-run guidance, approvals and opt-in active observation (fake by default).
- [`a2a-server`](./examples/a2a-server): publish and call an Agent through A2A.
- [`showcases/team-agent-workflow`](./examples/showcases/team-agent-workflow): planning, implementation, and review joined into one pipeline.

Examples that make real calls depend on the corresponding CLI and its login state. The repository's ordinary tests never produce paid calls.

## Boundaries

The core library does not provide a general HTTP/gRPC server, host task queue, scheduler, multi-tenancy, authorization, or database, and it does not decide which agent a task should be dispatched to. Protocol serving belongs to bridges and host applications; team roles and workflow policy belong to the host.

## Documentation

- [Documentation map](./docs/README.md)
- [API reference](./docs/api-reference.md)
- [Host-defined Tools](./docs/tools.md)
- [Streaming guide](./docs/streaming.md)
- [Structured output](./docs/structured-output.md)
- [Run policy: sandbox, approvals, timeouts](./docs/run-policy.md)
- [A2A integration](./docs/a2a.md)
- [Public errors](./docs/public-errors.md)

## Migrating from v1 to v2

Use the `/v2` module path for every SDK import and dependency.

| v1 | v2 |
|---|---|
| `agent.Run(ctx, prompt, opts...)` | `agent.Send(ctx, prompt, opts...).Result()` |
| `agent.Stream(ctx, prompt, opts...)` | `agent.Send(ctx, prompt, opts...)` |
| `thread.Run` / `thread.Stream` | `thread.Send`, then `Result()` or `Events()` |
| `Runner` with two execution methods | `Runner` with `Send` only |
| Treat `RunFinished` as completion | Read the authoritative final `Done` or call `Result()` |
| Explicit store required for Threads | Agent-local memory by default; inject a durable store for restart recovery |

`RunAs[T]` remains the synchronous typed helper and uses `Send` internally. Subscribe to `Events()` before consuming ordinary progress; a Result-only call retains critical events and a drop summary for later reading. Supplemental input belongs on `Stream.Send`; continuous chat belongs on `Thread.Standby(serverCtx)`. `Agent.Close(ctx)` still releases Agent-owned resources at shutdown.

## License

Unless otherwise noted, this repository is licensed under the
[Apache License, Version 2.0](./LICENSE). Third-party material retains its own
license and attribution; see [Third-Party Notices](./THIRD_PARTY_NOTICES.md).

Codex, Claude, Cursor, CodeBuddy, and other product names are trademarks of
their respective owners. They are used only to identify supported integrations;
this project is not affiliated with or endorsed by those owners.
