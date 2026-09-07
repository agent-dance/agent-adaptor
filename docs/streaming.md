# Streaming guide

This guide is for hosts that need to render text, thinking, tool calls, run status, or approval requests in real time. The final API exposes a single typed `Event` stream: both `Agent` and `Thread` return a `Stream` from `Runner.Stream`, and the authoritative outcome is read from `Stream.Result()` after the run ends.

The complete public types are listed in the [API reference](./api-reference.md), and the approval policy is described in [run policy](./run-policy.md).

## 1. One execution pipeline, one event stream

```go
type Runner interface {
	Run(ctx context.Context, prompt string, opts ...CallOption) (*Result, error)
	Stream(ctx context.Context, prompt string, opts ...CallOption) Stream
}

type Stream interface {
	Events() <-chan Event
	Result() (*Result, error)
	RunID() string
	Cancel()
}
```

`Run` is strictly equivalent to calling `Stream`, draining `Events()` to completion, and then calling `Result()`. Both share the same option merging, resource resolution, Driver invocation, Thread coordination, and result archiving logic.

Calling `Stream` only states that the host wants to observe live events; it does not force any particular provider protocol. Core selects the provider transport from the resolved invocation, the Driver's `StreamCapability`, and structured-output compatibility; the host has no additional streaming switch.

## 2. Minimal consumer

```go
package main

import (
	"context"
	"errors"
	"fmt"

	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/codex"
)

func main() {
	ai := adaptor.New(codex.Driver(codex.Config{Model: "gpt-5.4"}))
	stream := ai.Stream(context.Background(), "Write a haiku")
	defer stream.Cancel()

	for ev := range stream.Events() {
		switch e := ev.(type) {
		case adaptor.TextDelta:
			if e.Phase == adaptor.PhaseContent {
				fmt.Print(e.Text)
			}
		case adaptor.Thinking:
			// Render reasoning when needed; simply ignore it otherwise.
		case adaptor.ToolCall:
			if e.Phase == adaptor.PhaseStart {
				fmt.Printf("\n[tool %s]\n", e.Name)
			}
		case adaptor.Dropped:
			fmt.Printf("\n[dropped %d events]\n", e.Count)
		}
	}

	result, err := stream.Result()
	if err != nil {
		var runErr *adaptor.RunError
		if errors.As(err, &runErr) {
			fmt.Printf("run failed: %s: %s\n", runErr.Reason, runErr.Message)
			return
		}
		panic(err)
	}
	fmt.Printf("\nresult: %s\n", result.Text)
}
```

Key semantics:

- `Stream` returns immediately, and `RunID()` is available right away.
- Pre-start errors use the same shape: `Events()` is closed and `Result()` returns the error.
- A normal run that is not cancelled closes `Events()` after every accepted terminal event has been delivered and the provider, runtime services, and workspace have been released. A closed channel means no further events will arrive, and `Result()` is already available at that point.
- `Result()` may be called concurrently and repeatedly, and returns a consistent outcome.
- Event types you do not care about can be omitted from the type switch, but the channel must still be drained continuously.

A complete runnable example is in [`examples/streaming`](../examples/streaming/main.go); an interactive example with a Thread is in [`examples/streaming/chat`](../examples/streaming/chat/main.go).

## 3. Event vocabulary and ordering

Every semantic and operational signal of a run arrives on the same `<-chan adaptor.Event`:

| Event | Purpose |
|---|---|
| `RunStarted` / `RunFinished` | Run lifecycle; the final success or failure is still decided by `Stream.Result()` |
| `TextDelta` | Assistant text, with `PhaseStart` / `PhaseContent` / `PhaseEnd` |
| `Thinking` | Reasoning lifecycle |
| `ToolCall` / `ToolResult` | Tool call arguments, boundaries, and results |
| `ProcessInfo` | Subprocess spawn and raw stdout/stderr chunks |
| `Notice` | Invocation, runtime, step, transcript item, and approval audit information |
| `*ApprovalRequest` | Host approval request with an exactly-once responder |
| `Dropped` | Aggregated loss report, including numbered undelivered events during cancellation or revocation |
| `SubagentUpdate` | Live delegation progress |
| `CapabilityInvocation` | Validated capability invocation facts from explicit evidence |
| `TodoUpdated` | Confirmed full ordered todo snapshot within a scope |

The `Meta()` of every event returns the authoritative SDK envelope:

- `RunID`: the ID of this execution.
- `ThreadKey`: the opaque Thread key supplied by the host.
- `Sequence`: assigned by the SDK in the receive order of the unified sink; strictly increasing within a single run.
- `Time`: the time at which the SDK received the event.
- `Source`: optional raw provider/Driver coordinates; it never overrides the authoritative SDK fields.

Concurrent producers are serialized by the same broker, so the channel receive order matches `EventMeta.Sequence`. A bridge may use it as a wire cursor within a single run, and should not promote provider sequence numbers into a second authoritative ordering. A recorder that needs to persist and resume across multiple runs must allocate its own host-scoped cursor (`sessionrecorder.HostSeq`), because `EventMeta.Sequence` restarts for every new run.

Capability facts use canonical catalog keys, explicit operation/phase/evidence,
UTC occurrence time and optional reliable duration. `Duration=nil` means unknown;
a pointer to zero means observed zero. Skill activation and subagent spawn are
explicit operations. Absence of observations is not proof that no capability ran.
Todo events are confirmed full ordered snapshots, not PlanReview requests; an
empty snapshot clears its scope and SyntheticID identifies a synthesized ID.
ToolCall, ToolResult and TranscriptItem retain ScopeID, ParentScopeID and
ParentToolCallID. Source Upstream accepts at most eight acyclic levels. Mutable
payloads are independently copied at publication, observation and result access.

Run-service attachments can install a [RunEventObserver](./api-reference.md#12-workspace-and-runtime-services)
for Capability/Todo only. Observers run in receive/registration order before user
backpressure, including before delivery of an earlier Dropped marker. Each call
has a 100ms wall limit, shortened by remaining cleanup time. The first error,
panic or timeout disables that observer for this run and emits one safe
observation_disabled Notice; the error text is withheld and Result/HITL/checkpoint
are unchanged. A late nil return cannot reactivate it. Other observers continue.

Callbacks must honor context before external writes and cannot synchronously
wait on the same run, call its publisher, drain Events, call Result or Agent.Close.
The SDK cannot undo a host store's late write or kill arbitrary Go callbacks;
a timed-out observer receives no new calls. Shared stores remain host-owned.
Observation demand is separate from installing an observer and selects only
already feasible transport candidates. Current provider support remains whatever
the configured Driver descriptor declares; the new event types alone promise no
provider observation coverage.

## 4. Result, errors, and cancellation

Events are in-flight observations; `Stream.Result()` is the terminal authority:

- Success: returns `*Result, nil`.
- After Driver entry, every failure returns `nil, *RunError`, with the non-nil partial or complete result in `RunError.Result` and original/secondary errors in `Cause`.
- Before Driver entry, failure returns `nil, error`, preserving the `errors.Is/As` chain.

`Result.Text`, `Summary`, `Raw()`, `Transcript()`, and `Services()` are independent layers. Do not rebuild the final result from a `RunFinished` event, and do not concatenate live deltas yourself into an audit-grade Raw or transcript.

`Cancel()` is idempotent and unblocks pending event publication, approval waits, and the run context. Buffered events may still be readable after cancellation; keep ranging until `Events()` closes, then call `Result()` to obtain the final cancellation error.

A consumer that plans to stop reading events early must call `Cancel()` first. It is not enough to stop ranging and then wait on `Result()`: reliable events or blocking mode may be waiting for channel space.

For an entered execution, cancellation retains the available Text, Raw,
Transcript, Usage and Services through `RunError.Result`. `Reason` identifies
the primary outcome even if `Cause` also matches cancellation or cleanup.
After Events closes, repeated concurrent `Result()` calls observe the same
immutable result/error pair.

Claude one-shot bidirectional transport closes stdin once on a formal
`type:result`, even without a preceding terminal `message_stop`. Root assistant
completion may also release input; `tool_use` and nested subagent completion
keep it available for control responses. Resident Thread turns release only
the turn handle. Output draining and process/checkpoint validation still finish
before the Driver completes.

Every admitted invocation receives exactly one core `RunStarted` and
`RunFinished`, whether or not it has run services. Core determines the terminal
Failed/Reason only after final Result formation, source flush, observer shutdown,
lease and resource release. A provider success followed by Detach failure thus
ends with an infrastructure terminal and `RunError` retaining the provider's
Raw.Terminal and Transcript. Events close after that terminal. Static refusals
before admission (such as invalid configuration/schema/policy or a closed Agent)
retain empty closed Events and a Result error, without acquiring resources,
calling the Driver or inventing a started event/RunID.

Claude and CodeBuddy resident failures retain available protocol observations
through `RunError.Result`. Short or partial prompt writes still stop and drain
stdout/stderr, including an unterminated stderr tail, and never replay accepted
input. EOF and an observed `*exec.ExitError` remain reachable in Cause; a natural
positive exit code is agent_error unless a more specific outcome already exists.
Internal process cleanup does not fabricate caller cancellation. Claude decision
sink errors also stop and drain the resident writer without requiring the caller
to cancel it, and a buffered terminal cannot make that failed writer reusable.

Without authoritative terminal usage, observed formal messages contribute their
cumulative increments once, with repeated snapshots deduplicated. Valid terminal
zero is authoritative; absent or entirely invalid counts do not invent observed
zero. CodeBuddy requires a formal message ID and preserves the existing
cache_read_input_tokens/cached_input_tokens mapping. Claude can use a formal
anonymous message boundary. No error path promotes an unhealthy checkpoint.
Claude leaves Text empty without final assistant text; CodeBuddy preserves its
formal partial-message fallback, while a terminal's legitimate empty text stays
empty. Neither uses raw stdout as Text or invents Summary.

## 5. Backpressure

The default buffer for ordinary events is 1024 and can be adjusted when constructing the Agent; the SDK reserves separate capacity for terminal events, which does not count against that number:

```go
ai := adaptor.New(
	codex.Driver(codex.Config{}),
	adaptor.WithEventBuffer(256),
)
```

The default policy only allows dropping replayable or high-frequency deltas:

- `PhaseContent` of `TextDelta`, `Thinking`, and `ToolCall`
- stdout/stderr `ProcessInfo`
- `SubagentUpdate{Kind: SubagentDelta}`

Lifecycle boundaries, approvals, tool results, transcript items, CapabilityInvocation,
TodoUpdated and Dropped are reliable during normal backpressure. An aggregated
`Dropped` reports Count, ByKind, FirstSequence, LastSequence, Reason and Source.
Cancellation or publisher revocation releases blocking sends; every numbered
but undelivered event is included in the final loss report, including an earlier
Dropped marker that itself never reached the channel. Two independent reserved
slots deliver that final Dropped and then RunFinished without requiring a consumer
to make space. Count measures undelivered typed events, not business operations.

For example, with buffer=1: RunStarted seq1 fills the buffer, delta seq2 is dropped,
and pending Dropped seq3 precedes Todo seq4. The observer sees the Todo before
any user-send wait. Cancellation without draining yields final Dropped seq5 with
Count=3, ByKind={text.content:1,dropped:1,todo.updated:1}, FirstSequence=2 and
LastSequence=4, followed by RunFinished seq6. A delivered earlier marker is not
counted again. Consumers should mark projections incomplete after loss.

Use the construction-scope option when lossless events are required:

```go
ai := adaptor.New(
	codex.Driver(codex.Config{}),
	adaptor.WithEventBuffer(256),
	adaptor.WithBlockingEvents(),
)
```

Without cancellation, blocking mode never drops events, but a slow consumer applies backpressure to the Driver; cancellation releases those blocks and enters teardown. Whichever policy is used, production hosts should drain continuously and call `Cancel()` immediately when they disconnect or abandon consumption.

## 6. Streaming conversations on a Thread

`Thread` and `Agent` implement the same `Runner`. A stateful conversation only adds a Thread coordination layer; it does not create a second stream:

```go
import "github.com/agent-dance/agent-adaptor/memory"

ai := adaptor.New(
	codex.Driver(codex.Config{Model: "gpt-5.4"}),
	adaptor.WithThreadStore(memory.NewStore()),
)

stream := ai.Thread("tenant-7/conversation-42").Stream(ctx, "Continue")
defer stream.Cancel()
for ev := range stream.Events() {
	// The same adaptor.Event vocabulary.
}
result, err := stream.Result()
```

The Thread key is an opaque string supplied by the host. Hosts should hold it verbatim; do not assemble provider session IDs yourself, and do not derive consumer identity from `Source.ThreadID` in events.

## 7. Approval requests

Without an installed `OnApproval` callback, requests that need a human answer appear directly on the same event stream:

```go
for ev := range stream.Events() {
	switch req := ev.(type) {
	case *adaptor.ApprovalRequest:
		switch req.Kind {
		case adaptor.ApprovalQuestion:
			_ = req.Answer(ctx, "yes")
		default:
			_ = req.Approve(ctx)
		}
	}
}
```

`Approve`, `Deny`, and `Answer` are exactly-once; a duplicate, expired, kind-mismatched, or responder-less request returns a stable error immediately. Timeout, rejection, and retry behaviour is decided solely by `Policy.Approvals`; bridges do not build a second policy.

Hosts that cannot interact inside the event loop can install a callback with `adaptor.OnApproval`. Both consumption styles share the same request and outcome contract.

## 8. AG-UI bridge

A host that already has an AG-UI transport layer can translate a `Stream` directly:

```go
import "github.com/agent-dance/agent-adaptor/bridges/agui"

stream := ai.Stream(ctx, prompt)
defer stream.Cancel()

for ev := range agui.EventsContext(ctx, stream) {
	// ev is an AG-UI events.Event; write it to SSE, a WebSocket, or a recorder.
}
```

`agui.EventsContext`:

- Guarantees that `RUN_STARTED` is the first item of the output.
- Completes and deduplicates text, thinking, and tool-call lifecycle boundaries.
- Produces exactly one `RUN_FINISHED` or `RUN_ERROR` from `Stream.Result()` after all open lifecycles are closed.
- Cancels the underlying `Stream` when the context ends, and keeps every downstream send cancellable.
- Maps approvals to configurable tool-call or custom events.

Local programs without a request-scoped context can use `agui.Events(stream)`; HTTP/WebSocket handlers should prefer `EventsContext` to avoid leaking a fan-out goroutine after the client disconnects.

The AG-UI input helper `RunAgentInput` extracts the last non-empty user text; `UserTurnEvents` builds the canonical user `TextDelta` triple. Drivers only produce assistant text, and `RoleUser` is synthesized solely by a bridge or a host.

## 9. HTTP SSE bridge

`sse.Handler` accepts any `adaptor.Runner`:

```go
import (
	"net/http"
	"time"

	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/bridges/sse"
)

mux := http.NewServeMux()
mux.Handle("/v1/chat", sse.Handler(ai, sse.Options{
	Protocol:          sse.AGUI,
	CORSAllowedOrigin: "*",
	Options: []adaptor.CallOption{
		adaptor.WithTimeout(2 * time.Minute),
	},
}))
```

The final `Options` fields:

- `Protocol`: `sse.AGUI` (the zero value) or `sse.Raw`.
- `KeepAlivePing`: when non-zero, writes an SSE comment to keep proxy connections alive.
- `CORSAllowedOrigin`: when non-empty, writes CORS headers.
- `WriteTimeout`: resets the write deadline before each underlying Write/Flush, defaulting to 30 seconds; it is not a budget for a whole frame or a whole run. A `ResponseWriter` without deadline support falls back transparently.
- `Options`: `[]adaptor.CallOption` appended to every `Runner.Stream`.

AG-UI mode accepts the standard `RunAgentInput`:

```json
{
  "threadId": "conversation-42",
  "runId": "browser-turn-8",
  "messages": [
    {"id": "m-8", "role": "user", "content": "Write a haiku"}
  ]
}
```

Raw mode accepts:

```json
{"prompt":"Write a haiku","sessionKey":"conversation-42"}
```

When the handler receives a session identity and the `Runner` is an `*adaptor.Agent` with a Thread Store, it binds to `Agent.Thread`. If the caller passed an `*adaptor.Thread` in the first place, the host-pinned Thread is kept. The AG-UI `threadId` uses a collision-free tuple encoding; the Raw `sessionKey` is preserved verbatim.

A client disconnect cancels the request context and the underlying `Stream`. Raw frames use `EventMeta.Sequence` as the SSE `id` and support reading `Last-Event-ID` as a fallback cursor; persistent replay remains the host's responsibility.

SSE is a one-way transport, so approval requests can only be sent as informational frames. Interactive approval should use `OnApproval`, or the host should hold the live responder and expose an authenticated companion endpoint.

A complete server example is in [`examples/web-chat`](../examples/web-chat/main.go), an AG-UI client in [`examples/web-chat/aguiclient`](../examples/web-chat/aguiclient/main.go), and a CopilotKit integration in [`examples/web-chat/copilotkit`](../examples/web-chat/copilotkit/server.go).

## 10. Driver streaming fidelity

Every `Runner` has `Stream`; `driver.StreamCapability` only describes the granularity of native provider events. It is neither another execution capability nor an A2A transport capability.

The declarations of the current built-in Drivers are:

| Driver | Native | TokenLevel | Reasoning | ToolCallArgs | HITL |
|---|---:|---:|---:|---:|---:|
| Codex | ✓ | ✓ | ✓ | ✓ | — |
| Claude | ✓ | ✓ | ✓ | ✓ | ✓ |
| Cursor | — | — | — | — | — |
| CodeBuddy | ✓ | ✓ | ✓ | ✓ | ✓ |

When a capability is false, the host still uses the same `Stream`. The events may only carry coarser lifecycle, process, transcript, and final result information; a host should not create another call path because fidelity is lower.

When a structured-output schema is incompatible with a rich-event provider transport, core may select a compatible batch provider transport while still exposing the run through the unified `Event` channel. This decision does not change the public semantics of `Run` and `Stream`.

## 11. AG-UI version alignment

The Go side pins `github.com/ag-ui-protocol/ag-ui/sdks/community/go` through `go.mod`; the CopilotKit example pins `@ag-ui/core` through [`examples/web-chat/copilotkit/web/package-lock.json`](../examples/web-chat/copilotkit/web/package-lock.json). The two version coordinates differ, so upgrading either side requires revalidating the event lifecycle and schema.

`go test ./internal/aguiversion/...` verifies both pins. When upgrading deliberately, update the expected versions in `internal/aguiversion/align_test.go` and review the `bridges/agui` fixtures.
