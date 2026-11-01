# Driver Streaming and Message Delivery Contract

This contract is for Driver authors. Hosts use `Runner.Send` and the returned
`Stream`; synchronous `Result()` and asynchronous `Events()` observe that same
execution. The canonical message SPI is [driver/messages.go](../driver/messages.go),
and the public surfaces are frozen by [the root API golden](../testdata/root_api.golden)
and [the Driver API golden](../adaptertest/testdata/driver_api.golden).

## 1. Layer boundaries

The Driver contract remains `Descriptor`, `ValidateConfig`, and `Run`. `Run` is
an extension SPI method, not a consumer execution entry. Core alone merges
options, resolves resources and policy, coordinates Thread ownership, owns the
message queue, dispatches Driver calls, and publishes the final `Done`.

A configured Driver must interpret nil configuration as its captured construction
configuration. Execution, Inspect, schema negotiation, and message capability
resolution must use that same configuration. Public Config types must be real
structs and must not expose internal types.

The Driver parses its official provider protocol and produces semantic events,
Output, Summary, RawStreams, Transcript, Usage, terminal payload, and checkpoint
from the same parse. A process helper may launch/stop, write stdin, capture raw
bytes, publish chunks, and tee to that parser. It must not guess provider fields,
construct semantic transcript entries, or classify checkpoint health. Bridges
consume the public Runner/Stream/Event/Result contract and never dispatch Drivers.
The `driver` package must not import the root, providers, bridges, or internals.

## 2. Provider transport and capabilities

The public Event stream is always available. `StreamSupport.StreamCapability`
describes provider event fidelity: Native, TokenLevel, Reasoning, ToolCallArgs,
and HITL. These fields must be conservative and deterministic. They neither
select a second host entry nor imply message delivery support.

`Request.Streaming` is selected from the resolved invocation, schema, policy,
and provider transport capabilities. Reading `Result()` instead of `Events()`
must not change it. A Driver must honor the selected transport and still supply
a complete Response, including raw output and transcript.

`Descriptor.Messages` separately declares delivery support:

| Field | Required meaning |
|---|---|
| Boundaries | Verified mid-turn `immediate`, `tool`, or `stop` boundaries; `turn_end` is implied by continuation support |
| Interrupt | Can interrupt the active turn and restart it with input in this execution |
| Continue | `ContinueResume` (zero): return after a turn, core resumes; `ContinueInRun`: Driver owns the provider turn loop |
| MaxUnits / Unit | Limit on each **framed** input; zero is no known limit, empty unit means bytes; Unit is the typed `MessageUnitBytes` or `MessageUnitUTF16` |
| ConfirmedDelivery | A delivered Ack requires an official provider echo, rather than successful transport submission |
| PersistsDelivered | Delivered input remains in provider history after later resume |
| MinVersion | Minimum supported CLI version; built-in resolvers gate in-run delivery on a detected version, while third-party Drivers must implement their own truthful resolution |

The zero capability declares no mid-turn delivery and no interrupt. It still
allows core continuation when the Driver supports resume. Without resume or
ContinueInRun, core rejects supplemental input with ErrMessageUnsupported.

A Driver whose support depends on transport or resolved workspace must implement
`MessageCapabilityResolver.ResolveMessageCapability(Request) MessageCapability`.
Core calls it once after final request resolution, including Messages, Session,
profile, runtime, instructions, and schema. Its result must stay within the
static declaration and describe the transport Run actually uses. Prelaunch sends
use the static declaration; core reconciles unclaimed queued input against the
resolved capability before dispatch. An invalidated queued item fails with its
original text. Claimed input remains owned by Ack.

The built-in Codex, Claude, and Cursor resolvers and environment probes detect
the configured CLI version and trim in-run delivery below MinVersion. Environment
reports include Version and the effective Messages capability; execution emits one
`<driver>_version_unsupported` lifecycle Notice and leaves pending input for core
resume. Unknown versions retain the declared capability and report a warning.
The version probe is cached by executable identity, size, and modification time.
Third-party Drivers must implement their own runtime resolution; a MinVersion
string alone does not make core probe an arbitrary executable.

## 3. MessagePort lifetime and delivery windows

`Request.Messages` is nil when `Policy.Messages.Disabled` is true. Otherwise core
supplies a concurrency-safe, run-scoped MessagePort. Its exact method signatures
are:

```text
Window(open bool)
Signal() <-chan struct{}
Claim(b Boundary) []MessageItem
Ack(id string, ack MessageAck)
```

The Driver must not retain this port after Run returns. All readers, hook
handlers, notification dispatchers, and writers must stop using it before then.
This applies equally to resident processes: bind a new port for each call and
unbind it before returning.

`Window(true)` reports a provider-confirmed active turn at a declared boundary.
Process creation or a queued prompt is insufficient. Report `Window(false)` when
the window closes, on cancellation, and during teardown. Push implementations
watch `Signal()` while the provider can accept input; hook implementations claim
only in an eligible provider callback. A window is not a readiness prerequisite
for host admission: core buffers messages sent before launch.

## 4. Claim and Ack ownership

At a real supported boundary, call Claim once and process the returned items in
admission order. Claim atomically transfers all eligible input to the Driver:

| Requested timing | Claim eligibility |
|---|---|
| TimingSoon | Earliest declared boundary, or turn_end |
| TimingAfterTurn | turn_end only |
| TimingInterrupt | immediate only, with Interrupt support |

Every MessageItem contains ID, already-framed Text, Timing, and MaybeDuplicate.
Deliver Text verbatim. Do not strip author authority, add a user role, reframe
it, or remove the duplicate warning. A boundary adapter may combine items into
one provider payload in order, within the provider's total batch limit. An
immediate RPC adapter may submit them individually in order.

**Every claimed item must receive exactly one Ack before Run returns**, including
on parser failure, disconnect, write failure, and cancellation. After Claim,
sender cancellation cannot decide delivery; the Driver owns that decision. A
write already in progress must report its actual outcome. Run cancellation must
still stop I/O and settle all outstanding claims; it must not strand them.

MessageAck is a struct, not an error argument:

| Field | Obligation |
|---|---|
| Delivered | True only when the provider accepted input at the claiming boundary |
| Mechanism | Concrete truthful mechanism, e.g. `codex.turn_steer` or `cursor.hook.post_tool_use` |
| Err | Required on an unsuccessful delivery; ignored on success |
| Requeue | Return an undelivered item to core for continuation instead of terminal failure |
| MaybeDelivered | With Requeue, mark ambiguous submission that may already have reached the model |

With ConfirmedDelivery=true, wait for the official provider echo. A successful
HTTP response write, pipe write, or nonce appearing inside tool output is not a
receipt. With ConfirmedDelivery=false, successful provider RPC acceptance or a
completed protocol write can be the acknowledgement; do not describe it as echo
confirmation or proof that the model followed the guidance.

For a boundary-specific failure such as an ended-turn precondition, RPC timeout,
or absent hook echo, Ack with Err and Requeue=true. Core keeps the message
accepted and makes it eligible **only at turn_end**, avoiding a busy retry loop
at the failed boundary. If submission was ambiguous, also set
MaybeDelivered=true; core supplies MaybeDuplicate and a duplicate warning on
retry. It cannot guarantee exactly-once model effects after ambiguous delivery.
An explicit provider rejection with no possible acceptance need not set that flag.
Cancellation prevents a requeued message from being retried.

Core reports accepted/requeued, delivered, or failed through reliable
MessageUpdate events. Admission rejection is a synchronous MessageError with the
original text and wrapped cause; it never enters the queue. Stream.Send returning
nil means admission, not delivery. The Driver must not create a second receipt
channel or mutate core's queue/accounting.

## 5. Continuation and final-boundary races

With **ContinueResume**, return after the provider turn with a healthy checkpoint.
Core claims pending input at BoundaryTurnEnd, builds a continuation prompt and
SessionContext from that checkpoint, and calls the same Driver dispatch again
inside the same public execution. This works for anonymous Agent.Send as well as
named Threads; no cross-call anonymous history is retained. A declared resume
capability must include a valid SessionCodec and stable configuration fingerprint.

With **ContinueInRun**, complete each provider turn, report TurnCompleted, then
Claim(BoundaryTurnEnd). If items are returned, start another provider turn in
this Driver.Run call and acknowledge each according to actual submission. Return
only after an empty final claim or a failure/limit/cancellation. Core enforces
MaxTurns by refusing additional claims once the turn budget is exhausted.

An empty final Claim does **not** atomically close admission. A message can be
admitted after it and before Run returns. Core then resumes from the healthy
checkpoint even for ContinueInRun Drivers. If a third-party Driver only supports
InRun and has no resume, this race cannot be recovered: core reports the late
input failed/provider_rejected and returns its original text, never silently
loses it. Do not advertise stronger delivery guarantees for that combination.

A failed boundary normally falls back to continuation, not a different consumer
execution. Core uses the last healthy checkpoint only after clean completion;
a failed provider turn must not fabricate a new checkpoint to make fallback
appear successful. Standby can carry run_failed input into its next execution;
that is separate from this turn-end fallback.

## 6. TurnCompleted and public finalization

Emit one `RunEvent{Type: RunEventTurnCompleted, Turn: ...}` at the end of **every**
provider turn, including the final one. TurnCompleted has a zero-based Index
within this Driver.Run call, final assistant Text, observed per-turn Usage (nil
when unavailable), Interrupted, and provider CompletedAt. Interrupted marks a
turn ended by an interrupt message, rather than natural completion. Do not count
replayed history as new usage, emit duplicate completion, or infer missing usage.

Core converts these to one-based execution-wide TurnEnd events. If a Driver
emits no completion event, core can synthesize one from the returned outcome;
this fallback does not satisfy a Driver's per-turn contract for an InRun loop.
Provider completion may precede checkpoint persistence, schema validation, or
resource cleanup. TurnEnd is therefore not an authoritative execution verdict.

Core combines all calls: Result.Text is the last turn's text, Turns counts
provider turns, Usage sums observed accounting once, Raw concatenates complete
stdout/stderr, and Transcript includes each call. Raw.Terminal retains the last
recognized official terminal payload. Message outcomes appear in Messages() and
Undelivered(); core appends actual deliveries as Kind `message` with a
TranscriptMessage, without claiming a provider user role.

Only core publishes `Done{Result, Err}` after validation, persistence handling,
message settlement, and cleanup. It is always last, including startup failures
and cancellation. Its Result pointer and Err are identical to Stream.Result().
RunFinished and TurnEnd remain informational. A Driver must never publish public
Done or close the sink.

## 7. Delivered input and Thread persistence

Set PersistsDelivered=true only with evidence that boundary input survives
provider resume. If false, core replays ephemeral input when resuming and keeps
bounded author-framed EarlierGuidance in the Thread record. RecentTurns keeps the
latest three assistant texts; the two fields occupy at most 8 KiB. Fresh-session
fallback publishes a thread.continuity Notice and uses that bounded preamble.
There is no guarantee of complete chat-history reconstruction.

Core, not the Driver, persists DeliveredMessages with a healthy checkpoint under
the same validated leases and atomic Store.Finalize. Receipts retain at most the
latest 1000 IDs no older than seven days. Already-delivered IDs are deduped before
provider submission, including initial prompts; a duplicate initial prompt can
complete with zero Turns. Fork has its own ID scope.

An execution failure must not advance the previous healthy provider state. Core
may preserve new delivery receipts and ephemeral guidance against an existing
healthy state when ownership is still valid. Store outages retain the newest
checkpoint in process and retry with a Notice; this is not crash-durable queueing.
A crash between provider acceptance and receipt persistence can replay input on
recovery. MessageID dedupe does not close that window.

## 8. Built-in delivery contracts

| Driver / transport | Mid-turn / interrupt | Continuation and acceptance evidence |
|---|---|---|
| Codex app-server, minimum 0.100.0 | immediate; interrupt supported | InRun; `turn/steer` guards expectedTurnId and validates response turnId; interrupt waits for completion then turn/start; persists input, no echo claim |
| Codex exec | None | Resume; resolver trims app-server support |
| Claude stream-json input, minimum 2.1.110 | tool and stop; no interrupt | InRun hooks; Ack after serialized response write, no echo claim; PersistsDelivered=false |
| Claude batch native schema JSON | None | Resume |
| Cursor, minimum 2026.10.14 | tool when relay is eligible; no interrupt | Resume; confirms full hook context through official hookAdditionalContexts; PersistsDelivered=false |
| CodeBuddy | No verified mid-turn boundary or interrupt | Resume; must not consume MessagePort at an undeclared boundary |

Codex native schema requests select app-server even when `Request.Streaming`
is false. The resolved schema is sent on every `turn/start`; message capability
resolution and resident-process eligibility follow that same transport.

Claude registers UserPromptSubmit, PreToolUse, PostToolUse, PostToolUseFailure,
and Stop through an acknowledged SDK initialize request. Only registered
main-agent callbacks can claim. Rejection or no response within ten seconds
permanently disables hooks for that process, emits `claude_hooks_unavailable`,
and allows the ordinary prompt/HITL path to continue. Pending input then uses
core resume. Late responses cannot reopen that process's hooks.

Cursor requires an explicitly managed workspace, an unused regular
`.cursor/hooks.json`, a successful relay probe, and a loopback listener.
Windows relay is unavailable. Failures emit `cursor_hook_unavailable` and leave
input for core resume. The host must call `cursor.RunHookRelayIfRequested()`
first in main, or supply a compatible Config.HookRelay command. Each run uses a
nonce, authenticated relay, conversation/tool ownership checks, and a **complete
context echo**. The total hook payload limit is 10000 UTF-16 units; advertised
MaxUnits subtracts the SDK nonce tag overhead. Batch overflow requeues the
remaining items. See [Claude](../claude/README-streaming.md) and
[Cursor](../cursor/README-streaming.md) for transport details.

Pi and OpenCode integrations are suspended; this matrix does not certify them or
an unimplemented ACP integration as v2-capable.

## 9. EventSink and protocol lifecycle

EventSink.Emit accepts chunks, transcript items, invocation/spawn/runtime/lifecycle
notices, and TurnCompleted. EmitStream accepts normalized provider text, reasoning,
tool, step, run, HITL, and drop payloads. Both feed the one public Event channel.
Wait for every per-call I/O and parser goroutine before returning; resident
processes must detach the call's observers rather than emit into a retired sink.

A provider streaming envelope must open once, close all message/reasoning/tool/
step lifecycles before its terminal frame, and emit no semantic payload after its
terminal. A multi-turn implementation must use unique lifecycle IDs and preserve
provider order. Core folds Driver envelopes into one public execution lifecycle
across continuation calls.

| Payload family | Required contract |
|---|---|
| run.started / finished / error | One opening and terminal envelope; error carries RunFailure; MessageID and ToolCallID empty |
| text.start / content / end | Non-empty MessageID; content has a non-empty Delta, only inside an open lifecycle |
| reasoning.start / content / end | Same lifecycle rules; only with Reasoning=true |
| tool_call.start / args / end / result | Correlated ToolCallID; opening has Name and optional Args; incremental args only with ToolCallArgs=true |
| step.started / finished | Paired Name |
| hitl.requested / resolved | Structured audit envelopes only, with HITL=true |
| stream.dropped | Honest provider loss metadata, not silent omission |

Every Driver payload leaves Role at RoleAssistant, Sequence/Seq and
StreamPayload.Timestamp at zero. RunEvent.Seq is also zero. Core assigns the
SDK EventMeta order/time and preserves provider coordinates only as Source.
Do not allocate sequences across producers.

RunEventChunk.Stream is stdout or stderr. RunEventItem.Item is non-nil and valid;
the emitted item sequence must exactly mirror Response.Transcript. Unknown
formal provider extensions can remain opaque notices; do not guess them into
assistant output, tools, terminal events, or checkpoints.

## 10. Response, Raw and checkpoint safety

Output is final assistant text; Summary is optional bounded host text; neither
contains raw dumps or terminal JSON. RawStreams preserves all untruncated
stdout/stderr and exact official terminal JSON, captured before semantic parsing.
A JSON-RPC reader must tee raw bytes too. Terminal is nil if no official terminal
was observed. Usage is nil when unobserved; non-nil zero means observed zero.
RuntimeServices report observations, never echo input declarations as success.

Structured business failures use Response.Failure, which core turns into a
RunError carrying the available Result. Driver errors remain wrappable Go errors;
do not rely on an accompanying partial Response being exposed as a public Result.
Failure.HumanDecision is non-nil exactly for decision_rejected/decision_timeout.

A new valid checkpoint requires a clean outcome: no Driver error, non-zero exit,
signal, timeout, cancellation, protocol error, missing terminal, or business
failure. The official parser must observe the protocol's explicit session
identity and successful terminal, and the same Driver's SessionCodec must accept
and round-trip a non-empty resume state. Provider-specific protocol tests decide
where that official identity is carried; do not require every provider to put it
in the terminal event (Codex exec uses thread.started). An announcement without
a successful terminal is insufficient. There is no failed-run checkpoint exception.

A resume-capable Driver must expose a stable, non-nil SessionCodec and stable,
cross-process SessionConfigFingerprint covering all provider-visible construction
configuration and codec/version guards. Thread rejects missing contracts before
resource acquisition. Follow codec nil/zero and guard laws from godoc/adaptertest.
Exact ProfilePayload.Fingerprint remains the materialization authority; resume
and resident guards use SessionFingerprint(), which may normalize only
core-owned ephemeral transport allocations.

## 11. HITL, backpressure and cancellation

Block for decisions only through DecisionCapableSink.RequestDecision. Core owns
Policy.Approvals, request identity, exactly-once responders, deadlines, retries,
and fallbacks. Provider HITL broadcasts are Notices, not answerable requests.
Without OnApproval and before Stream.Events has ever been called, core immediately
uses the configured timeout fallback and emits an explanatory Notice. Do not add
a Driver retry loop or a second decision channel to bypass this behavior.
ApprovalRequest.Forward(Origin) attributes a copy while sharing the same responder.

Critical lifecycle, approval, message, turn, notice, drop and Done events are
retained without a subscriber; ordinary events may be summarized as Dropped.
Result-only consumption releases ordinary blocking producers. For normal event
consumption, only permitted high-frequency deltas may drop; tool results and
semantic lifecycle frames remain reliable. WithBlockingEvents can block ordinary
producers, so all process I/O and waits must remain cancellation-safe. Do not
ignore sink errors, close the sink, or form a parser/stdin/publisher deadlock.
A subscriber that abandons Events should cancel and drain the final events.

## 12. Acceptance and release gates

Run the [adaptertest suite](../adaptertest/doc.go) for config, capability,
structured-output, codec, lifecycle, transcript mirror, raw output and response
invariants. The current suite and frozen SPI include the message types, but the
suite does **not** yet have generic MessagePort/MessageCapability/TurnCompleted
clauses. Driver-specific message tests and core contract tests are existing
evidence; generic message conformance remains a release gate, not a claimed pass.

Every transport must cover success, non-zero exit, malformed/missing terminal,
missing checkpoint, cancellation, one completion per turn, declared boundaries,
claim/Ack ownership, ambiguous requeue, size/batch limits, and resume fallback.
InRun tests must exercise an empty-final-claim admission race, and core tests must
cover the no-resume failure outcome. Include Thread receipt recovery, ownership
loss, Standby failure carry and single classified retry, Result/Done identity,
backpressure, and immediate approval fallback. Repeat concurrency cases and run
Linux race; run parser/archive fuzz for the release duration.

Minimum hermetic checks:

```text
go build ./...
go vet ./...
go test -count=1 ./...
```

Live execution requires explicit authorization and its provider gates. Codex
message live conformance requires both `-tags=codex_live` and
`AGENT_ADAPTOR_LIVE_CONFORMANCE=1`, plus an available authenticated CLI. Merely
compiling with the tag is not live verification. Ordinary CI must not pay for
provider calls or infer unverified CLI-version support.
