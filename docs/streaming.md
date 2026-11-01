# Streaming guide

In v2, `Agent.Send` and `Thread.Send` return one `Stream` for one execution. Observe typed events, wait synchronously with `Result()`, or use both. Each execution ends with authoritative `Done{Result, Err}`. Supplemental input belongs on `Stream.Send`; continuous conversations use `Thread.Standby`.

The complete public types are in the [API reference](./api-reference.md); approvals are described in [run policy](./run-policy.md).

## 1. One execution pipeline, one event stream

```text
Runner.Send(context.Context, string, ...CallOption) Stream
Stream.Send(context.Context, string, ...MessageOption) error
Stream.Events() <-chan Event
Stream.Result() (*Result, error)
Stream.RunID() string
Stream.Cancel()
```

Every execution uses the same option resolution, workspace/runtime/skill/MCP preparation, Thread coordination, Driver dispatch, and finalization. One execution can contain multiple provider turns when messages require continuation.

Provider transport is negotiated from resolved capabilities and schema/policy compatibility. Calling Events or Result does not switch provider protocols. A coarse provider still returns the same Stream interface.

## 2. Minimal consumer

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
	ai := adaptor.New(codex.Driver(codex.Config{}))
	defer ai.Close(context.Background())
	stream := ai.Send(context.Background(), "Write a haiku")
	defer stream.Cancel()

	for ev := range stream.Events() {
		switch e := ev.(type) {
		case adaptor.TextDelta:
			fmt.Print(e.Text)
		case adaptor.ToolCall:
			if e.Phase == adaptor.PhaseStart {
				fmt.Printf("\n[tool %s]\n", e.Name)
			}
		case adaptor.Dropped:
			log.Printf("dropped %d incremental events", e.Count)
		case adaptor.Done:
			if e.Err != nil {
				log.Print(e.Err)
			} else {
				fmt.Printf("\nfinal: %s\n", e.Result.Text)
			}
		}
	}
}
```

Send returns immediately and RunID is immediately available. Startup failures also emit Done. Result is safe to call concurrently and repeatedly; its pair is identical to Done's, including pointer identity. Call `Events()` before ordinary traffic when you need live progress.

A runnable task consumer is in [`examples/streaming`](../examples/streaming/main.go). [`examples/streaming/chat`](../examples/streaming/chat/main.go) streams one prompt on a named Thread; it is not a continuous input loop or Standby example.

## 3. Event vocabulary and ordering

| Event | Purpose |
|---|---|
| `RunStarted` / `RunFinished` | Execution lifecycle; RunFinished is informational. |
| `TextDelta` / `Thinking` | Text/reasoning with PhaseStart, PhaseContent, PhaseEnd. |
| `ToolCall` / `ToolResult` | Tool boundaries, arguments, and results. |
| `MessageUpdate` | Admission, delivery, requeue, or failure of supplemental input. |
| `TurnEnd` | One provider turn's Result/Err and processed MessageIDs; Turn is one-based. |
| `Done` | Final Result/Err after validation, checkpoint handling, and cleanup; always last. |
| `ProcessInfo` | Spawn and raw stdout/stderr chunks. |
| `Notice` | Invocation, runtime, step, transcript, approval, and recovery information. |
| `*ApprovalRequest` | Exactly-once live responder. |
| `Dropped` | Counts and sequence ranges of discarded events. |
| `SubagentUpdate` | Typed optional host-component progress. |

Every event's Meta holds SDK RunID, opaque ThreadKey (empty on direct Agent tasks), strictly increasing Sequence, receive Time, optional TurnID, and provider coordinates in Source. Provider IDs and sequence numbers never override SDK ordering. Standby spans multiple RunIDs; sequence restarts per execution. Persistent replay/broadcast needs a host cursor and host storage.

TurnEnd precedes final execution checks; a successful turn can still be followed by a Done error from persistence or cleanup. Do not treat TurnEnd or RunFinished as execution completion. Per-turn snapshots are intended for replies; final audit/message accounting comes from Done or Result.

Notice selectors relevant to delivery and recovery:

| Selector | Meaning |
|---|---|
| Kind `limit.approaching` | Approaching the turn limit; wrap-up guidance may accompany pending continuation input. |
| Kind `thread.continuity` | Fresh provider session after incompatibility or resume rejection. |
| Kind `thread.persistence` | Storage unavailable; in-process checkpoint remains authoritative during retries. |
| Kind `thread.ownership` | Standby lease release failed. |
| Kind `NoticeLifecycle`, Metadata code `cursor_hook_unavailable` | Tool delivery unavailable; use turn-end resume. |
| Kind `NoticeLifecycle`, Metadata code `claude_hooks_unavailable` | Hook initialization unavailable; use turn-end resume. |
| Kind `NoticeLifecycle`, Data warning `human_decision_retry_unsupported` | Approval retry degrades to abort. |

These codes live in `Metadata["code"]` or `Data["warning"]` as listed, not a Notice.Code field. Preserve unknown notice kinds and extension data. Unobserved approval requests produce an explanatory lifecycle notice without a dedicated code.

## 4. Result, errors, and cancellation

The execution verdict has one Go error path:

- Success: Result and nil error.
- Business failure: nil Result and `*RunError`, carrying the available result in RunError.Result.
- Infrastructure failure: nil Result and wrapped error, preserving errors.Is/As.

Done contains exactly this pair. Raw stdout/stderr, normalized Transcript, final Text, short Summary, observed Services, message outcomes, and schema output remain separate. Across provider turns, Text is the last turn's answer, Usage is summed, and audit streams include all invocations. Raw.Terminal is the last recognized official terminal payload.

Cancel is idempotent. It cancels the execution context, fails pending messages, and unblocks producers and approval waiters. A Driver-claimed message is settled by its acknowledgement. Drain buffered critical events through Done and closure after cancellation. If cancellation causes Driver teardown to return a process/transport error, the final RunError remains ReasonCancelled and matches the context error with errors.Is; the teardown error remains available for diagnostics. A provider or approval failure already observed before cancellation keeps its original classification.

For synchronous consumption, `agent.Send(ctx, prompt).Result()` needs no event-draining goroutine. If Events was never called, Result discards ordinary traffic and retains critical events plus a Dropped summary. Events may be read afterwards, but that is not a full ordinary-event replay. An Events subscriber that disconnects should cancel and drain the remaining critical events.

## 5. Backpressure

Ordinary-event capacity defaults to 1024. Configure it at construction:

```go
ai := adaptor.New(
	codex.Driver(codex.Config{}),
	adaptor.WithEventBuffer(256),
	adaptor.WithBlockingEvents(),
)
```

In default drop mode, only replayable high-frequency content may drop: text/thinking/tool-argument PhaseContent, stdout/stderr chunks, and subagent deltas. Lifecycle boundaries, tool results, transcript notices, approval requests, MessageUpdate, TurnEnd, Done, and drop reports remain reliable during normal consumption.

Dropped includes Count, ByKind, FirstSequence, LastSequence, Reason, Source, and Details. Critical publications enqueue immediately even at capacity. WithBlockingEvents makes ordinary producers wait for space while Events is consumed; cancellation releases waits. Result-only consumption and cancellation can discard ordinary events into a loss summary. Done remains authoritative and last, following any pending drop report.

The channel is single-consumer, not a broadcast bus. Hosts should fan out after one continuous drain.

## 6. Streaming conversations on a Thread

```go
ai := adaptor.New(codex.Driver(codex.Config{}))
thread := ai.Thread("tenant-7/conversation-42")
stream := thread.Send(ctx, "Continue")
for ev := range stream.Events() {
	fmt.Printf("%s %d %T\n", ev.Meta().RunID, ev.Meta().Sequence, ev)
}
result, err := stream.Result()
if err != nil {
	return err
}
fmt.Println(result.Text)
```

Named Threads use private memory by default. Inject a durable WithThreadStore for restart recovery and shared lease coordination. Store and compare host keys verbatim; provider session IDs are checkpoint details.

A task-mode Thread is exclusive. Another Thread.Send fails with ErrThreadBusy rather than joining current work. Send additional guidance on the current Stream. A live Standby reserves the same Thread even while idle (§13).

## 7. Approval requests

Call Events early, or install OnApproval for synchronous use. An Ask request arriving before Events was ever called and without a handler immediately uses the timeout fallback and emits a lifecycle notice.

```go
func decide(ctx context.Context, req *adaptor.ApprovalRequest) error {
	if req.Kind == adaptor.ApprovalQuestion {
		return req.Answer(ctx, "Use the existing database")
	}
	return req.Approve(ctx)
}
```

Subscribe to `*ApprovalRequest` for a UI and retain its live responder in host storage. Approve, Deny, and Answer resolve exactly once, including forwarded copies. Origin identifies an optional member/task; Forward preserves the same responder while attributing a copy. Kind mismatches, expiry, duplicate replies, and zero/unbound requests return stable errors promptly. Policy.Approvals owns timeout, rejection, and retry handling.

Standby's internal event drain does not count as a host approval subscription. Without an external Events subscriber or OnApproval handler, a new Ask request takes the same immediate timeout fallback. Install a handler or use a supported automatic policy for unattended Standby.

## 8. AG-UI bridge

```go
stream := ai.Send(ctx, prompt)
defer stream.Cancel()
for event := range agui.EventsContext(ctx, stream) {
	fmt.Printf("%T\n", event)
}
```

Import `github.com/agent-dance/agent-adaptor/v2/bridges/agui`. EventsContext emits RUN_STARTED first, completes text/thinking/tool boundaries, and derives one RUN_FINISHED/RUN_ERROR from Stream.Result after draining. This is the same authoritative outcome carried by Done. MessageUpdate and TurnEnd become custom `message.update` and `turn.end` events; raw process chunks have no AG-UI projection. Approvals can map to tool-call or custom events.

Cancellation ends downstream sends and cancels the underlying execution. Use EventsContext for request-scoped consumers. `RunAgentInput` extracts the last non-empty user text; `UserTurnEvents` synthesizes a user text lifecycle. Drivers emit assistant text.

### Routing input to an active execution

`agui.ThreadKey(threadID)` maps a frontend thread ID to the same collision-free SDK key used by SessionKey and UserTurnEvents. A zero-value `agui.RunRegistry` is ready for concurrent use:

- `Send(ctx, thread, input, opts...)` starts `Thread.Send` when idle, or calls the registered `Stream.Send` with the last user message ID, human author, and Soon timing. It returns the Stream, a bool indicating supplementation, and an admission error.
- `Active(threadKey)` looks up a Stream without consuming its events. `SendActive(ctx, threadKey, input)` supplements only an existing execution; absent or finishing runs return a MessageError matching ErrRunFinished and do not start replacement work.
- `Forget(threadKey, stream)` removes that completed Stream without erasing a newer registration. The host must call it after Events closes and Result returns, including failures.

Only the caller that creates the Stream consumes Events and Result. Concurrent supplement routes return admission acknowledgements; delivery receipts remain on the original event stream. RunRegistry passes the sender context unchanged to Stream.Send: cancellation can withdraw unclaimed input even after Send returns nil. Once claimed, the Driver acknowledgement settles it. Hosts choosing an HTTP lifetime independent of the request must supply that context explicitly. New-run options do not reconfigure an active Stream. Hosts wanting continuous conversations can retain a Thread.Standby handle instead.

The [CopilotKit example](../examples/web-chat/copilotkit) implements `POST /message` with `{threadId, messageId, text}`. Success is HTTP 202 with `{runId, messageId, state: "accepted"}`; actual delivery is reported over the original SSE stream. The endpoint checks the request context before dispatch, then explicitly passes `context.WithoutCancel(requestCtx)` to the active Stream. Once dispatch begins, disconnecting the supplement request neither withdraws the input nor stops a queue-space wait; Stream cancellation or completion releases that wait. The original /agent execution remains tied to its own SSE request context. No active run or a finished run yields 409, other MessageError admissions 422, context cancellation/deadline 408, and infrastructure/history errors 500. If history storage fails after admission, the 500 response still includes `state: "accepted"`; retry decisions must preserve that fact and the original message ID. Malformed input is 400 and an oversized body is 413.

### Recording and replay

`hosttools/sessionrecorder` stores stable typed Event envelopes with a host-owned, cross-run HostSeq cursor. Done and TurnEnd record only RunID, Turns, Text, Summary and error identity by default, including safe partial results from RunError. Configure `sessionrecorder.WithExposure(sessionrecorder.ExposurePolicy{...})` as an EventOption when constructing an EventRecorder to opt in to `IncludeMetadata`, `IncludeUsage`, `IncludeRawStreams`, `IncludeProviderResult`, `IncludeTranscript`, or `IncludeServices`.

Metadata includes provider/model identity, result metadata and business-error details; Usage preserves observed zero values. Raw streams, terminal payloads, transcripts and service reports replay through `EventRecord.Diagnostics`, not by rebuilding Result's private audit fields. ExposurePolicy only filters these terminal snapshots: the host remains responsible for redacting other event types. A replayed ApprovalRequest has no live responder; answer the retained live request through the host's approval route.


## 9. HTTP SSE bridge

```go
mux := http.NewServeMux()
mux.Handle("/agent", sse.Handler(ai, sse.Options{
	Protocol: sse.AGUI,
	Options:  []adaptor.CallOption{adaptor.WithTimeout(2 * time.Minute)},
}))
```

Import `github.com/agent-dance/agent-adaptor/v2/bridges/sse`. Handler accepts any Runner and starts request-bound Send calls. Options include Protocol (AGUI default or Raw), KeepAlivePing, CORSAllowedOrigin, WriteTimeout (30 seconds by default, reset before each underlying write/flush), and per-execution Options. Request bodies are limited to 4 MiB.

AG-UI mode accepts RunAgentInput:

```json
{"threadId":"conversation-42","runId":"browser-turn-8","messages":[{"id":"m-8","role":"user","content":"Write a haiku"}]}
```

Raw mode accepts:

```json
{"prompt":"Write a haiku","sessionKey":"conversation-42"}
```

An Agent binds incoming session identities to named Threads automatically: AG-UI IDs use collision-free tuple encoding; Raw sessionKey remains verbatim. A host-pinned Thread stays pinned. The handler is a task-mode endpoint; it does not automatically open Standby or accept supplemental messages for a running Stream. Hosts implement those routes and retain the live handle.

Client disconnection cancels request-bound work. Raw message/turn frames are `message.update` and `turn.end`; its final run frame is derived from Result after draining rather than a literal Go Done event. Raw sequence cursors/Last-Event-ID do not provide persistent replay. SSE carries approval information outward; a callback or authenticated companion route must hold and answer the live responder.

Runnable servers and clients are in [`web-chat`](../examples/web-chat), [`aguiclient`](../examples/web-chat/aguiclient), and [`copilotkit`](../examples/web-chat/copilotkit).

## 10. Driver streaming fidelity

`driver.StreamCapability` describes native event granularity, independently of message-delivery capabilities.

| Driver | Native | TokenLevel | Reasoning | ToolCallArgs | HITL |
|---|---:|---:|---:|---:|---:|
| Codex | yes | yes | yes | yes | yes |
| Claude | yes | yes | yes | yes | yes |
| Cursor | no | no | no | no | no |
| CodeBuddy | yes | yes | yes | yes | yes |

A false field still leaves the same public Stream available. Coarse providers may expose process, transcript, lifecycle, and final output without token deltas. Structured-output negotiation can select a batch transport without changing the public handle.

## 11. AG-UI version alignment

The Go dependency is pinned by go.mod, and frontend dependencies by the CopilotKit lockfile. Their version coordinates differ. `go test ./internal/aguiversion/...` checks both pins; deliberate upgrades require updating its expectations and revalidating the bridge fixtures.

## 12. Message delivery capabilities

This matrix comes from Descriptor.Messages, invocation resolvers, and message tests in [`codex`](../codex/messages.go), [`claude`](../claude/messages.go), [`cursor`](../cursor/messages.go), and [`codebuddy`](../codebuddy/driver.go). Turn-end continuation is implied when resume or in-run continuation is supported.

| Driver / transport | Mid-turn boundaries | Interrupt | Continuation | Maximum framed message / unit | Provider-echo confirmation | Declared minimum CLI |
|---|---|---|---|---|---|---|
| Codex app-server | immediate (`turn/steer`) | yes (`turn/interrupt`, then restart turn) | in-run; resume also covers final-boundary races | 0 / bytes: no known limit | no | 0.100.0 |
| Codex exec | none | no | checkpoint resume at turn end | 0 / bytes: no known limit | no | none declared for exec continuation |
| Claude stream-json input | tool, stop | no | in-run; resume fallback | 0 / bytes: no known limit | no | 2.1.110 |
| Claude batch native-schema transport | none | no | checkpoint resume | 0 / bytes: no known limit | no | none declared for this resolved transport |
| Cursor | tool, when relay is available | no | checkpoint resume | **9936 / UTF-16 code units** | yes, `hookAdditionalContexts` | 2026.10.14 |
| CodeBuddy | none | no | checkpoint resume | 0 / bytes: no known limit | no | none declared |

Zero MaxUnits is not a promise of unlimited provider capacity. Core measures the **framed** text, including author guidance/custom Format. Cursor reserves 64 units for its nonce wrapper out of the provider's 10000-unit hook budget, leaving MaxUnits=9936 for core-framed input. Batches that exceed the hook budget requeue excess input for continuation. Cursor retains this per-message limit even when its tool boundary is trimmed.

Built-in environment probes expose the detected CLI `Version` and effective `Messages` capability. Codex, Claude, and Cursor trim in-run boundaries and Interrupt when a known version is below MinVersion, retaining turn-end checkpoint resume and framed-message limits. An execution emits one lifecycle Notice with code `<driver>_version_unsupported` when it falls back. Unknown or unparseable versions produce a probe warning and retain declared capability; they do not certify support. Version detection uses the configured executable and caches by file identity, size, and modification time. Inspect can prime this cache without starting an agent task. Transport, workspace eligibility, hook initialization, and policy can further narrow delivery. Pi/OpenCode and the proposal's ACP presets are not v2 integrations in this tree.

Only Codex declares PersistsDelivered=true. Core preserves bounded delivered guidance for other Drivers and replays it on resume as needed. ConfirmedDelivery refers to a provider echo for boundary injection, not confirmation that the model obeyed it; other deliveries are acknowledged through their concrete transport's acceptance/write path.

### Sending, timing, and outcomes

```go
stream := ai.Send(ctx, "Fix the build")
if err := stream.Send(ctx, "Keep the current dependency versions",
	adaptor.MessageID("review-42"),
	adaptor.From(adaptor.Author{Kind: adaptor.AuthorHuman, ID: "maintainer"}),
	adaptor.When(adaptor.Soon),
); err != nil {
	return err
}
result, err := stream.Result()
if err != nil {
	return err
}
for _, outcome := range result.Messages() {
	fmt.Println(outcome.ID, outcome.State, outcome.Boundary, outcome.Mechanism)
}
```

Soon uses the earliest available boundary, falling back to continuation. AfterTurn waits for a new provider turn within this execution. Interrupt aborts and continues the active turn; unsupported Drivers return ErrMessageUnsupported. Neither Soon nor Interrupt inserts input into tokens already being generated.

Nil means admitted. MessageUpdate emits Accepted, Delivered, or Failed; a boundary-specific retry can emit Accepted again. Boundary is immediate/tool/stop/turn_end; Mechanism reports the actual path (for example `codex.turn_steer`, `claude.hook.post_tool_use`, `cursor.hook.post_tool_use`, or `core.resume`). Failed updates return original Text and a stable Reason. Result.Messages preserves final outcomes in admission order; Undelivered filters failures. Prelaunch sends need no readiness check.

Stream.Send accepts only MessageOption. Admission failure returns MessageError with original ID/Text/Reason and a wrapped cause; errors.Is detects ErrRunFinished, ErrMessageUnsupported, or context cancellation. The send context withdraws only unclaimed input. Once claimed, the acknowledgement wins. Queue capacity defaults to 32 and blocks senders until space or cancellation.

MessageID deduplicates within the Thread; at most 1000 delivered IDs are retained for seven days. Starting-prompt duplicates finish with zero turns and empty Text. Durable deduplication requires a durable store. A crash after provider acceptance and before receipt persistence can replay input. A third-party Driver with in-run continuation but no resume cannot recover input admitted at its final boundary race; the outcome is failed, never silently dropped.

An execution preparation failure before any Driver.Run dispatch returns every already admitted input as failed/provider_rejected with its original text; Standby does not carry it into a later execution with potentially different settings. Policy/schema errors that can be validated synchronously reject admission with MessageError and produce no MessageUpdate. After Driver dispatch, ordinary execution failures continue to use run_failed and the documented Standby carry/retry rules.

Boundary-specific failures such as an RPC timeout, turn-ID mismatch, relay failure, or missing echo requeue input for turn-end continuation. Ambiguous delivery carries a possible-duplicate note. Cancellation, execution failure, unavailable healthy checkpoint, or MaxTurns exhaustion can still fail the message.

### Cursor hook relay

Call the relay helper before other main logic:

```go
package main

import "github.com/agent-dance/agent-adaptor/v2/cursor"

func main() {
	cursor.RunHookRelayIfRequested()
}
```

The real host continues its startup after this call. The helper recognizes the SDK's private relay invocation and exits after serving it. Keep heavy initialization out of init functions: every hook can re-execute the host. Config.HookRelay can supply explicit argv instead.

The resolver enables tool delivery only on non-Windows managed workspaces (git-worktree/cloud-sandbox/driver-managed strategy), with a usable directory and no existing `.cursor/hooks.json`. A plain WithWorkspace or SharedWorkspace does not opt into hook installation; a real manager must supply an appropriately managed lease. Existing hook files, including another run's SDK file, are left untouched.

Relay probe, listener, or installation failure emits `cursor_hook_unavailable` and uses checkpoint resume at turn end. Cursor print mode has no stop-hook delivery. A host binary that omits the helper therefore still supports turn-end input, with later delivery.

Claude registers main-loop tool and Stop callbacks, filtering nested agent/tool loops. Rejected or timed-out hook initialization emits `claude_hooks_unavailable`; pending input continues through resume. Claude Interrupt remains unsupported.

## 13. Standby conversations

Use Thread.Send for a task with one execution result. Use Standby when messages and replies arrive over the lifetime of a chat:

```go
func serveChat(serverCtx context.Context, agent *adaptor.Agent) (adaptor.Standby, error) {
	sb, err := agent.Thread("chat-42").Standby(serverCtx)
	if err != nil {
		return nil, err
	}
	events := sb.Events()
	go func() {
		for event := range events {
			switch e := event.(type) {
			case adaptor.TurnEnd:
				if e.Result != nil {
					fmt.Println(e.MessageIDs, e.Result.Text, e.Err)
				}
			case adaptor.Done:
				if e.Err != nil {
					fmt.Println("execution failed:", e.Err)
				}
			}
		}
	}()
	return sb, nil
}
```

Retain the returned handle in the server. An input handler calls `sb.Send(r.Context(), text, MessageID(id))`; the request context bounds admission, while accepted work uses serverCtx. Message-only input joins active processing; idle input starts an execution. Execution options such as WithModel or WithTimeout queue a separate execution after current work. No input is required to open or subscribe.

Standby exposes Send, Events, Cancel, with no overall Result. Per-turn TurnEnd provides replies; per-execution Done gives the finalized verdict. Before its first Events subscription, events are discarded. After subscribing, keep draining and broadcast from the host if needed.

A live Standby owns the Thread lease even while idle. Task-mode Thread.Send and another process's ownership attempt fail with ErrThreadBusy. Within the same Agent, another Standby call for that key returns the live handle; the first context retains lifetime ownership.

`sb.Cancel()` stops current processing and fails pending input with MessageCancelled, then keeps accepting input. Cancel the lifetime context to close Standby and its Events channel. Agent.Close also shuts down Agent-owned work.

An execution failure leaves Standby alive. Only classified rate-limit, network, and provider-server failures with pending work retry automatically, at most once. A structured retry-after hint is honored; otherwise the fallback delay is 250 ms. Cancellation and context deadlines are not retried.

Undelivered input with reason run_failed carries to the next execution under the same MessageID. It can emit failed, then accepted/delivered. Without an eligible retry, carried input waits for the next host message; it does not start a busy retry loop. The same provider-acceptance/receipt-persistence crash window described above applies to Standby recovery.
