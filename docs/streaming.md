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
explicit operations. Recording is best effort: absence of observations is not
proof that no capability ran, and records do not establish authorization, complete
audit coverage or billing completeness. A completed operation proves only its
stated evidence (for example, accepted input or a spawn), not all downstream work.
Todo events are confirmed full ordered snapshots, not PlanReview requests; an
empty snapshot clears its scope and SyntheticID identifies a synthesized ID.
Todo is an additional projection: original ToolCall/ToolResult events and
Transcript entries remain available under their existing contracts. It never
authorizes work or answers an ApprovalRequest. A UI replaces the entire
`(event.Meta().RunID, snapshot.ScopeID)` list for each snapshot; revisions restart
with a new run. After Dropped/cancellation it marks the view incomplete instead
of interpreting missing updates as success. The executable
[offline consumer](../examples/offline/main.go) demonstrates full/empty snapshots,
approval response and scoped recording in one stream.
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

For live capability history, install
[`capabilityrecorder.Recorder.Option()`](./api-reference.md#121-capability-recording).
Query sees successful writes before user backpressure. Supply all exact scope
fields and retain the last Sequence for polling: NextSequence=0 says only that
no later row exists now. Store ownership, context compliance and the existing
observer failure limit remain explicit; no implicit database or second stream
is created.

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
before the Driver completes. Compatible per-turn rich/batch choices do not
independently change Thread identity. A native schema temporary process may
stop the old writer and prewarm its replacement using the same healthy checkpoint;
the next compatible turn must actually reuse that writer. Real configuration,
codec, environment and resource changes remain guarded, and WithSpawn never
registers its temporary process as the next writer.

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

CodeBuddy resident turns complete all stderr callbacks admitted to that turn
before finalizing the parser and publishing Result. Raw-byte capture and callback
ownership share the same handoff boundary. Already observed diagnostics remain in
Raw and Transcript; later idle or next-turn bytes cannot mutate a returned Result.
A healthy turn does not wait for the resident process itself to exit, and this
boundary does not infer ownership of future bytes from another pipe.

For Codex resident turns, a provider write to stderr and a terminal on stdout
do not order the host’s reads across those separate pipes. Already observed
stderr must be preserved byte for byte in Raw, and a returned Result remains
stable through later idle output and another turn. A completed stdout terminal
alone does not establish that bytes from stderr have been received. One-shot
and failed resident paths retain their bounded process/drain completion checks.

Cancellation and shutdown preserve the same audit boundaries. Shared process
stdin completion is safe when writer completion and process exit overlap. Closing
input stops new writes while a healthy writer drains accepted frames in FIFO order;
terminal completion releases blocked writes even after input was closed. Codex
closes its owned transport and interrupts pending RPC waits without closing pending
response channels concurrently with the reader. Its normal process cleanup still
drains captured output and retains the original cancellation or protocol cause.

Without authoritative terminal usage, observed formal messages contribute their
cumulative increments once, with repeated snapshots deduplicated. Valid terminal
zero is authoritative; absent or entirely invalid counts do not invent observed
zero. CodeBuddy requires a formal message ID and preserves the existing
cache_read_input_tokens/cached_input_tokens mapping. Claude can use a formal
anonymous message boundary. No error path promotes an unhealthy checkpoint.
Claude leaves Text empty without final assistant text; CodeBuddy preserves its
formal partial-message fallback, while a terminal's legitimate empty text stays
empty. Neither uses raw stdout as Text or invents Summary.

`ReasonActiveExecutionTimeout`/`ErrActiveExecutionTimeout` report core active
budget exhaustion; its typed error retains Limit. It is distinct from parent
deadline and approval timeout. Ask pauses only its own run before queueing;
the budget seals before atomic persistence, while Finalize remains cancellable
and authoritative for success. See [run policy](./run-policy.md#active-execution-budget).

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

Approval Choices and nested JSON Details are copied independently; live copies
retain one responder. Recorded replay descriptions have no answer authority.
Each Ask pauses active time before enqueue/callback, including event-queue
backpressure; overlapping requests remain paused until the last ends. Parent
deadlines, Close, lease renewal and approval deadlines continue.

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

Subagent Activity snapshots and deltas own their tool lists and nested
Args/Result/Error JSON containers. A delivered `running` tool snapshot stays
`running` after a later completion delta; consumers can retain or serialize it
while translation continues. Consumer edits do not write back to the tracker.
Concrete JSON container types, numeric values and nil/empty distinctions remain
intact; translation and CloseResult keep the existing wire order and terminal
semantics.

The AG-UI input helper `RunAgentInput` extracts the last non-empty user text; `UserTurnEvents` builds the canonical user `TextDelta` triple. Drivers only produce assistant text, and `RoleUser` is synthesized solely by a bridge or a host.

Capability and Todo become CUSTOM `adapter.capability.invocation` and
`adapter.todo.updated`, with `kind`, `meta` and their closed payload. Empty Items
remain an explicit clear. `adapter.tool.parent` precedes tool starts and retains
original tool ID plus scope/parent coordinates. Tool card IDs are `aa1:` followed
by unpadded base64url of compact UTF-8 JSON `["tool",runID,scopeID,ID]` (HTML
escaping disabled). Clients using raw provider IDs must migrate to this tuple
and consume the parent CUSTOM event. Source chains are copied, up to eight
acyclic levels; invalid depth produces a safe explicit degradation notice.

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

Raw SSE emits `capability.invocation` and `todo.updated`, retaining the Meta
envelope, snake_case payload and tool parent/scope fields. Unknown duration is
omitted; observed zero remains duration_ns=0. Approval frames and AG-UI CUSTOM
approval details are independent snapshots. SSE/AG-UI and session JSONL retain
native uint64 and complete typed values; A2A's 64KiB/safe-number limits do not
apply to these local/native consumers.

### Session recording and subagent streams

Session JSONL retains its stable `{host_seq,recorded_at,kind,meta,event}` envelope,
including capability/todo, source chains, parents and empty clears. Mutable
input/history/query/backend values are copied; even memory-recorded approvals
have no responder. Corrupt/invalid JSONL returns ErrJSONLEventLogCorrupt.
Append/write/sync failure cannot silently create an in-memory success or advance
HostSeq. Scope and typed payload validation do not truncate a full snapshot.

`subagentstream.Merge` is now transparent: it never subscribes to the side bus,
injects duplicate events, renumbers or synthesizes terminals. Nil bus returns
the parent unchanged. For a non-nil bus, after draining the parent and reading
its Result it requires the structural `RunEventsBound(runID) bool` proof.
Absent/false proof returns a RunError carrying the complete parent result and
ErrEventInjectionUnsupported, preserving the entire original error graph and
primary Reason. Bare deadline remains ReasonDeadlineExceeded. Cancel cancels
then drains the parent; concurrent Result calls receive one cached outcome.

Install the delegation Service's Option before execution and consume the
original Stream; never feed its UI mirror back into core. Delegation Service.AttachRun installs BindEvents with Events=nil. The bound
publisher enters the core sink and its bounded synchronous observers before the
lossy EventBus. Config.Observe is a UI callback, not the recorder source.
RunEventsBound is exact historical proof of successful binding; detach revokes
publication but keeps proof for transparent Merge after teardown. Attach alone,
ordinary Publish or failed binding cannot establish that proof.
A2A observation projection is explicitly opt-in; see [wire and exposure](./a2a.md).

## Provider observation support

Observation support follows the actual negotiated protocol, independently of
the consumer's Run/Stream choice and the StreamCapability fidelity table.

| Actual provider transport | Skills | MCP | Subagents | Todo/plan |
|---|---|---|---|---|
| Claude stream-json, control and resident | Formal Skill call | Exact resolved catalog | Formal Agent/Task call, scoped parent evidence | Successful formal task results and full snapshots |
| Claude batch JSON | Unavailable | Unavailable | Unavailable | Unavailable |
| CodeBuddy stream-json, control and resident | Formal skill/command | Exact resolved catalog | Formal Task/Agent call; no parent graph | Successful formal task results and full snapshots |
| CodeBuddy batch JSON | Unavailable | Unavailable | Unavailable | Unavailable |
| Codex app-server, one-shot and resident | Typed input accepted | Formal mcpToolCall | Formal spawn plus unique child role/catalog | Full turn/plan/updated snapshot |
| Codex exec JSONL | Unavailable | Unavailable | Unavailable | Unavailable |
| Cursor print stream-json | Unavailable | Formal result and exact catalog | Formal custom-agent result/catalog | Unavailable |

Cursor uses print stream-json for both Request.Streaming values, so both
Observation descriptor branches expose MCP/Subagents. This does not declare a
separate terminal-JSON transport or fine-grained StreamSupport. Cursor Usage
remains nil when its formal protocol has no numeric usage evidence.

Capability keys come from the final resolved catalog. Each known alias must be
valid on its own; an invalid present alias cannot be hidden by another valid
field. Unknown or ambiguous references produce a safe notice without guessed
facts. A result confirms Completed/Failed; a pending call ends Interrupted, or
Cancelled when cancellation is observed. Unobserved duration stays nil.

Claude identifies tool calls by run/scope/call tuple. Complete input is carried
once in start.Args; partial frames carry only actual deltas. Tool description
closure does not prove execution. Parent wrappers and message IDs partition
usage/text; a bare result needs a unique historical scope, even when one call
already completed. Claude's MCP aliases follow its exact encoding, including
Unicode and underscore ambiguity checks.

CodeBuddy preserves its own Unicode/underscore MCP naming. Its user-result
parent_tool_use_id can refer to the call itself and is not a parent edge.
Unproved or malformed partial parents cannot supply arguments or completion
facts to the root, including a later full wrapper. Raw and original deltas remain.
Incremental starts leave an empty protocol input object out of Args and preserve
each actual ArgsDelta. Formal errors close pending capability facts before the
run terminal. Identical full result wrapper replays publish one typed ToolResult;
Raw and Transcript retain both copies. Different parents, IDs or payloads stay
distinct. Declared CodeBuddy SubAgents use exact resolved runtime names in their
native Markdown frontmatter; [materialization](./profile-resource-provider-matrix.md#codebuddy-declared-agents)
by itself does not prove invocation.

Claude and CodeBuddy Todo updates require confirmed successful results. Real
provider task IDs are preserved; namespaced synthetic IDs are explicitly marked
and cannot be matched as real TaskUpdate targets. TodoWrite replaces the full
table, including empty; CodeBuddy uses its formal newTodos field. CodeBuddy
TaskUpdate without a full list requires a matching formal task or the exact
official success rendering for the requested fields. Empty/unknown rawResponse
metadata proves nothing. Invalid snapshots leave the old table unchanged;
replayed snapshots do not increment revision. A new run has no invented task cache.

Codex explicit $skill inputs produce NativeInputAccepted only after turn/start
accepts the typed input. Completed means that input acceptance completed, not
that the skill was read or its work finished. Subagent spawn completion likewise
does not certify the child's final work. Plan steps have synthetic turn/position
IDs, preserve order and text, and include first-empty/clear snapshots. Plan deltas
do not become Todo or PlanReview. Current thread/turn and terminal fences remain;
only formally valid same-thread historical token usage may be retained in Raw
without altering current state. Missing/null/invalid usage fields never become
observed zero; complete explicit zero remains valid.

Resource materialization, native input acceptance and observed invocation are
different evidence. No observed fact does not prove absence of execution or
complete audit coverage. Formal fixture tests are not a current live-provider
certification.

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

## Independent regression validation

`go test -count=1 ./e2e -run TestAlignmentLifecycle` exercises formal fake child
processes through public Agent/Thread Run and Stream. Its independent literals
check complete Raw, Transcript, Services and terminal payloads before comparing
Run and Stream results. Result-only stdin closure, schema/HITL roundtrips,
unhealthy checkpoint rejection and cleanup outcomes share the same pipeline.

`go test -count=1 . -run TestAlignmentProtocol` composes the real parsers, HTTP
bridges, Local/Remote delegation, observers and recorders. Retained AG-UI
snapshots and concurrent serialization must remain independent of subsequent
translation. Both suites use private fake processes and loopback services;
passing them is not a real-provider or native-platform certification.
