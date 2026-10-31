# AG-UI bridge (protocol 1.0)

`bridges/agui` projects the public SDK event stream. `bridges/sse.Handler`
provides request-scoped POST/SSE: disconnect cancels execution. It does not
implement SSE resumption; AG-UI ignores SSE `id:` and `event:` fields.

## Input support matrix

The decoder validates known members against the frozen AG-UI 1.0 schema.
Unknown members are captured by JSON Pointer in `RunAgentInput.UnknownMembers`,
stripped from execution, and diagnosed. Arbitrary JSON inside metadata, state,
forwarded properties, activity payloads, and tool parameter schemas stays opaque.
`Message.Content` retains its schema-validated polymorphic JSON representation.

| Member | Support |
| --- | --- |
| `threadId`, `runId` | Public identity, independent of provider checkpoints. Required on versioned requests. |
| `protocolVersion` | Absent selects legacy identity behavior; `1.N` supported; newer minor warns. Malformed or other major rejects. |
| `parentRunId` | Echoed on `RUN_STARTED`. |
| `messages` | All seven 1.0 variants decoded and validated, including tool calls, errors, encrypted values and subagent attribution. Only trailing user text executes. |
| User/tool content parts | Text joined by newline; image, audio, video, document parts skipped with warnings. Unknown part types are removed and retained in `UnknownMembers`. Warnings refer to original part positions, only for the trailing user message or prior text actually rehydrated by `HistoryFallback`. No executable trailing text rejects. |
| Prior messages | Require a usable checkpoint or explicit `HistoryFallback`; never silently discarded on a missing checkpoint. |
| `tools` | Frontend declarations stripped with warning; the bridge never answers or emits calls to these declarations. SDK backend tools still project normally. |
| `state` | No shared-state execution or `STATE_*` projection; non-empty state warns. Absent/null state and empty JSON objects (regardless of whitespace), arrays and strings are treated as empty. |
| `context` | Appended in input order as compact JSON between `<agent-adaptor-context>` and `</agent-adaptor-context>`, separated from prompt text by two newlines. `InputOptions.DisableContext` strips with warning. |
| `forwardedProps` | Original JSON value bytes, including internal whitespace, exposed through `sse.Options.OnAGUIRequest`; never interpreted. |
| `resume` | Empty allowed; non-empty rejects before streaming. |

Trailing assistant, tool, system, developer, reasoning or activity messages reject.
The deprecated `LastUserText` helper retains its historical backwards scan for
source compatibility; it must not be used to choose an execution prompt.

### History policy and host extension

`PrepareInput(input, options, InputState{CheckpointUsable: true})` makes history
policy reusable.
The generic handler reads `Thread.Checkpoint(ctx)` through the public API; no new
root API or internal-engine dependency is introduced. Missing, corrupt, or
codec-incompatible checkpoints default to HTTP 409 `agui.history_unrecoverable`.
Stateless hosts must explicitly enable `InputOptions.HistoryFallback` to accept
complete multi-turn client history, or configure a ThreadStore and resumable
Thread. An Agent without a ThreadStore runs statelessly even when the request supplies
a public threadId. A fixed Thread still requires its host-configured store.
The bundled SSE examples opt into fallback for unavailable checkpoints.
`HistoryFallback` prepends compact JSON of prior user/assistant text and assistant
tool-call summaries between `<agent-adaptor-history>` and
`</agent-adaptor-history>`. This is text rehydration, not faithful provider state;
reasoning, system/developer instructions and tool result bodies are not replayed.

A checkpoint read is **not** a full invocation compatibility check: current
workspace, resources and runtime services are resolved only by the execution
pipeline. For checkpoint-backed history the generic handler uses `ResumeOnly`
when it selects an Agent Thread, preventing a configuration mismatch or rejected
resume from silently starting fresh. Such later failures currently arrive as
`RUN_ERROR`, rather than a pre-stream 409. An already-bound Thread is host-owned;
configure it with `ResumeOnly` when resuming supplied history. Hosts requiring
pre-stream compatibility guarantees supply `VerifyHistory`, which can attest
checkpoint usability, reject with `agui.history_diverged` (409), or choose a new
Thread key. When returning false usability for a previously healthy conversation,
choose a fresh key to avoid resuming and also rehydrating the same history.
A host can use `VerifyHistory` to compare message-ID order with its recorded history.

## Identity and versioning

`RUN_STARTED` always contains `protocolVersion: "1.0"`. The handler echoes
public `threadId` and `runId` on started/finished events and `parentRunId` on
started. The frozen schema does not permit top-level identity fields on
`RUN_ERROR`; its public identity is in `metadata.agentAdaptor.threadId/runId`.
SDK run ID, provider thread ID and bridge key appear as `sdkRunId`,
`providerThreadId`, `threadKey` in the same metadata namespace on boundaries
when observed. Public IDs never become provider resume IDs. Fallback identities also remain fixed after RUN_STARTED.

The default mapping is `bridgekey.Encode("agui", threadId)`.
`ResolveThreadKey` lets the host authorize/map identity before execution. A client
thread ID alone grants no authority. A fixed Thread Runner remains host-bound.
Legacy requests without a version may omit public identity: missing run ID is
generated. A missing thread ID also receives a generated public wire ID while
execution remains stateless; it is never used as a checkpoint key. Missing
identity warns. Other known schema violations are not granted legacy leniency.

Low-level `Events`/`EventTranslator` callers supply
`WithRunIdentity(threadID, runID, parentRunID)` to obtain the same public mapping.
Without it, SDK/provider identity is used, with stable unknown-ID sentinels if
unavailable. Once emitted, run-boundary identity stays fixed.

## Diagnostics and transport errors

Warnings follow `RUN_STARTED` as `CUSTOM` named `agentAdaptor.input.warning`.
Its value is `{code, message, path}`; paths are JSON Pointers (empty means root).

| Code | Meaning |
| --- | --- |
| `agui.unknown_member` | Unrecognised member captured and stripped. |
| `agui.legacy_identity` | Unversioned input omitted thread/run identity. |
| `agui.newer_minor` | Newer 1.x input served as 1.0. |
| `agui.content_part_skipped` | Non-text part not delivered to provider. |
| `agui.tools_unsupported` | Frontend tools ignored. |
| `agui.state_unsupported` | Non-empty shared state ignored. |
| `agui.context_disabled` | Host disabled context projection. |
| `agui.history_rehydrated` | Opt-in text history fallback applied. |

Malformed input returns HTTP 400 JSON `{error: {code, message, path}}` before
`Runner.Stream` or any SSE output. Codes: `agui.invalid_json`,
`agui.invalid_input`, `agui.identity_required`, `agui.unsupported_version`,
`agui.trailing_user_required`, `agui.no_executable_text`,
`agui.resume_unsupported`. History rejection uses HTTP 409. Oversized requests
retain the handler's HTTP 413 response and 4 MiB limit.
Execution-policy errors (including resume and trailing-message rejection) are
checked before host callbacks or checkpoint I/O. Hooks may return an InputError
with a chosen HTTP status, for example 403 for denied access. Other host-hook or
checkpoint-storage failures return 500 `agui.request_failed`, preserving the
original Go error chain. VerifyHistory sees resolver-bound legacy requests and
the actual key of an already-bound Thread; it cannot rebind that fixed Thread.

Decoded metadata preserves opaque JSON numbers as `json.Number`, avoiding
float64 rounding and overflow. The forwardedProps value retains its original
bytes; unknown members are captured by JSON Pointer and stripped only from
schema-defined closed objects, including nested unions and composed messages.

## Tool output and projection hooks

`TOOL_CALL_RESULT.content` remains human-readable. Its
`metadata.agentAdaptor` contains:

| Field | Meaning |
| --- | --- |
| `status` | `succeeded`, `failed`, `cancelled`, or `unknown`. |
| `exitCode` | Observed integral `exitCode` or `exit_code`; accepts int, int64, float64 and json.Number. |
| `error` | Observed string error, when present. |
| `truncated` | True when content exceeded the byte limit. |
| `originalBytes` | Byte size of the formatted content before truncation. |

Explicit failure flags (`is_error`, `isError`, `success:false`), error text or a
nonzero exit code establish failure; cancellation status is preserved unless
contradicted by a nonzero exit/failure flag. Completed/success statuses or zero
exit establish success. No evidence means unknown. The existing output-field
`[exit=N]` suffix is retained for all supported numeric representations.

Default content cap is 64 KiB; `WithToolContentLimit(bytes)` configures it.
Truncation does not split a UTF-8 code point. `TOOL_CALL_START.parentMessageId`
uses the current/last assistant text message when known. Tool events include
`subagentRunId` when the subagent tracker can correlate the tool ID.
`ProcessInfo` is intentionally not projected: raw provider CLI protocol output
is not conversation content and can contain unrelated or sensitive payloads.

`WithProjectionHook` runs over projected content, including synthesized text
and diagnostics. `RUN_STARTED`, `RUN_FINISHED` and `RUN_ERROR` bypass the hook;
a hook that drops every content event still preserves the run boundaries. A host can redact/drop reasoning or
expand events into host-supplied cards/activities. Hooks must preserve valid
lifecycles, must not call back into their translator, and should be deterministic
so the same policy can be reused when projecting recorded history.

Tool attribution is frozen by its opener. A known child tool carries
subagentRunId without claiming a parent-agent message; later tracker discovery
cannot change an open call's owner. Closing an older assistant message does not
replace the most recent assistant parent. Step notices synthesize missing
openers, deduplicate active starts, and close unfinished steps before the run
terminal. Empty CUSTOM notices carry `value: {}`.

Command output keeps `[exit=N]` even when stdout is empty. Decimal and exponent
exit-code numbers are checked exactly; fractional, non-finite and out-of-range
values are not coerced to integer exit codes. Truncation metadata counts the
content bytes before truncation, including any exit suffix.

## What the host still implements

The host supplies identity/authorization, checkpoint and durable history storage,
retention/redaction policy, worker lifetime, and business facts. This handler
provides neither durable UI history nor background execution/observation.

The deterministic handler fixture is `bridges/agui/testdata/handler-agui-1.0.sse`.
Regenerate with `UPDATE_GOLDEN=1` with `go test ./bridges/sse -run TestAGUIHandlerGolden`.

## Recorded execution and history

`bridges/agui/aguisession` is optional. Construct
`New(runner adaptor.Runner, opts Options) *Session` with an injected
`sessionrecorder.EventRecorder`. Mount `Handler()` for POST/SSE,
`EventsHandler()` for JSON records, and `MessagesHandler()` for AG-UI Message[].
The read handlers accept `thread_id`; events additionally accepts `after`.
**Production hosts must install `Options.Resolve`: a client threadId is not
an authorization decision.** This single hook authorizes and maps both starts
and reads. It receives `Access{Op: OpStart|OpRead, ThreadID, Input}`; Input is
set only for starts. Nil permits all access and uses the default mapping.
The default Thread key remains `bridgekey.Encode("agui", threadId)`.
`RecorderKey(key)` is the stable version-1 storage mapping: `"agui-"` plus
64 lowercase hexadecimal characters of SHA-256 over the exact UTF-8 key bytes.
This storage-key mapping remains version 1 independently of projection versions;
changing it requires an explicit storage migration. It derives a portable name for recorder backends;
all recorder access must use that mapping. This introduces the usual SHA-256
collision assumption, rather than changing the opaque Thread identity.

Projection version `agui-session/2` records typed Notices with reserved kinds
`agentAdaptor.agui.boundary` (public identity, input protocol version,
projection version, diagnostics, submitted user message and its ID) and
`agentAdaptor.agui.terminal` (authoritative code/message and SDK run/key metadata).
Older, missing or unknown projection versions produce
`agui.projection_version_unsupported`, never successful replay. Draft version-1
recordings require an explicit host migration; they are not silently reinterpreted.
Source events cannot use either reserved Notice kind: the session cancels/drains
and records `agui.reserved_notice` instead of accepting a forged boundary/terminal. Between them are the input user turn, source events including
approvals, and any Result.Text fallback. Stream.Result is the terminal authority.
Only successfully recorded events are projected. Recording failure cancels and
drains execution, reports `agui.recording_failed` on the live connection, and
leaves incomplete history. That emergency RUN_ERROR is the explicit unrecorded
transport-failure exception; its timestamp uses the last durable record. Failure
to record the boundary returns JSON HTTP 500 before execution. A failed terminal
append never becomes RUN_FINISHED. Cleanup writes use a cancellation-detached
context with a five-second timeout per write, so request cancellation can still
persist its authoritative outcome. Recorder implementations must honor contexts. Inactive runs without terminal records project
`run.incomplete`, never success. Disconnect cancels this request-scoped run.
The drain owns execution independently of the HTTP subscriber.

`Events(ctx, key, after)` returns ordered `[]ProjectedRecord` with HostSeq per
source record; consume all events in a record before advancing its cursor.
`Messages(ctx, key)` folds that same projection. `Fold` ports the official
@ag-ui/client 1.0.3 `defaultApplyEvents` (`Se`), tool-parent helper `xe`, and
metadata helper `L`. Metadata merges by top-level key: a namespace object is
replaced wholesale. Activity replacements retain prior metadata, and tool-result
corrections retain their position and metadata while updating attribution.
START reuses existing message IDs and CONTENT appends text. Consequently the
submitted user triple is recorded for source auditing but projects no text events.
Instead RUN_STARTED.input.messages contains just that recorded user message:
`defaultApplyEvents` adds it only if its ID is absent. A live client already owns
it; a fresh history/observer fold receives its exact name, metadata and content
parts. This avoids duplicate text and preserves representations TextDelta cannot
carry. It does not echo prior history, state, tools or forwarded properties. As part of
RUN_STARTED it follows the boundary's hook bypass; hosts needing submitted-input
redaction apply it before recording.
Live and history use the same event projection, including this input seed.
Reasoning remains role=reasoning, as consumed by assistant-ui
`fromAgUiMessages` under showThinking. Tool failure metadata additionally becomes
ToolMessage.error. Activity snapshots and RFC 6902 deltas are folded.
Projection options, including deterministic redaction hooks, apply to both paths.
Recorded timestamps reach the hook before it runs; emitted timestamps and
unfinished subagent closure times use the current record time. The drain freezes
the typed recorder JSON representation before invoking Observe with the original
live event/responder, keeping memory and JSONL replay equivalent.

Provider IDs are run-local. Projected source message IDs use the collision-free
length-prefixed tuple `bridgekey.Encode("agui-message", publicRunID, sourceID)`;
tool-call IDs use `bridgekey.Encode("agui-tool", publicRunID, sourceID)`. Activity
and correlated child-tool IDs follow the same mapping. The source records keep
original IDs. Submitted user IDs and approval request IDs retain their client/host
identity. This prevents repeated IDs such as Claude's `thinking-0` from combining
later turns with earlier messages. Host-added projection-hook events must also
use stable, conversation-unique IDs.

`VerifyHistory(ctx, input, runner, key) (string, bool, error)` also fits the generic
SSE handler extension. It compares IDs and roles in order, excluding activities
and the trailing input user turn; content is not compared. Divergence returns
409 `agui.history_diverged`; `ResolveDivergence` may choose a fresh key with no
recorded history. The destination is reserved and checked again before releasing
the original reservation. Fixed Threads always reserve their actual host key and
cannot switch keys. Resolver-bound legacy requests use the resolved key; an
unbound legacy request stays stateless and gets a private generated recording key.
Retries without a client thread ID therefore do not name the same conversation.
A match plus a readable checkpoint executes only the trailing user prompt when
the latest recorded run completed successfully. After a failed/incomplete turn,
a previous healthy checkpoint cannot attest that turn's provider context:
stateful continuation returns `agui.history_unrecoverable`, even with fallback.
`ResolveDivergence` can select a fresh key so explicit HistoryFallback rehydrates
without resuming the stale checkpoint. Stateless fallback remains available.
Checkpoint compatibility still has the execution-time limits described above.
Duplicate recorded run IDs return 409 `agui.duplicate_run`; active threads return
409 `agui.run_active` before streaming. `Observe` sees recorded live approvals;
`OnRunEnd` lets hosts clear pending responders after drain completion.

One Session must own admission for its keys. Memory and JSONL backends are
single-process facilities. A multi-worker backend needs atomic per-key HostSeq
allocation and append, durable ordered reads, and an external admission lease
covering duplicate detection and execution. Future follow notifications must be
published after commit; subscribe-before-read with cursor deduplication must
prevent gaps. Storage alone does not provide cross-worker run ownership.
Opt-in observation and unattended lifetime are described below. The host
supplies storage and worker routing.

The official live client fold does **not** set ToolMessage.error from result
metadata. History folding adds it for assistant-ui history loading; live UIs
read metadata.agentAdaptor for tool status. Verification compares only IDs and
roles, ignoring both content and error. HttpAgent.prepareRunAgentInput in
@ag-ui/client 1.0.3 clones messages and strips only role=activity: reasoning,
tool results and tool-only assistants remain in the next request.

Session SSE writes use the same deadline writer as the generic handler.
Options.WriteTimeout defaults to 30 seconds per write; KeepAlivePing optionally
sends comment frames. Write failures cancel request-scoped execution and the
single drain finishes recording and cleanup. Flush-only failures also cancel.
History I/O never holds the admission mutex; reads retry if admission/completion
changes the active boundary during the read. HTTP errors use the same JSON error
envelope; host/storage failures are 500, explicit InputError statuses are retained,
missing identities and invalid cursors are 400, and 405 includes Allow.

## Active observation and unattended lifetime

`aguisession.Options.Unattended` is opt-in. The default remains request-scoped:
POST disconnect cancels execution. Unattended mode requires a host-supplied
`BaseContext`; `MaxConcurrentRuns` defaults to 8. Admission beyond this limit
returns HTTP 429 `agui.capacity` before recording a boundary or starting execution.
The Session owns the single Stream drain; the POST is merely its first observer.
Disconnecting it, a later observer, or a slow/broken observer never cancels that
run. Recorder writes remain fail-closed and independent of observer backpressure.
`Shutdown(ctx)` rejects new starts with HTTP 503 `agui.session_closed`, cancels
unattended runs and waits for drain cleanup within the caller's deadline.
The host must call Shutdown before closing its recorder or other runtime services.
Runners must honor cancellation for bounded shutdown to finish successfully.

`Observe(ctx, key, after, yield)` is a trusted host Go API. `ObserveHandler()`
provides GET SSE at `?thread_id=T&after=N`; `CancelHandler()` provides POST at
`?thread_id=T`. The HTTP paths use the same `Resolve` authorization/mapping hook
with `OpFollow` and `OpCancel`. Direct Go APIs accept already-authorized opaque
keys. `Cancel(ctx, key)` cancels the active unattended run and waits for recording
cleanup; observers receive its authoritative cancelled terminal.

Observers subscribe before reading committed storage. Each observer reconstructs
its projector once from a full catch-up read, discarding projected records through
the requested cursor. It then retains that projector and reads only raw records
after its last raw HostSeq on notifications and idle ticks. Previously consumed
records are neither reread nor reprojected. Notifications are coalesced wakeups,
never the source of event data. HostSeq deduplication prevents overlap between
catch-up and notification reads. A completion re-read prevents the last append
from racing the active-state check. Inactive threads catch up and close. The
observer processes the full catch-up batch, including earlier completed runs,
before stopping at its final run terminal. It polls at 100ms as a completion and
recorder-decorator fallback, also allowing authorization checks during idle runs.

Each observer event carries `metadata.agentAdaptor.seq`, the originating record's
HostSeq. One record can project several events with the same seq. Advance a cursor
only after applying the entire record; the Go API exposes these record boundaries
explicitly. The SSE reader must retain a record's events until the next seq or
clean terminal/connection close establishes completion; a broken connection can
leave the final record partial, so retain the previous fully applied cursor.
Sequence metadata is observational; materialized Message[] remains the same
conversation projection and does not acquire observer cursors.

### Joining an active run

There are two join modes, both application-level reads rather than
`Last-Event-ID` resumption. AG-UI's HTTP POST/SSE binding does not define resumption.

For a refreshed client, use `Snapshot(ctx, key)` or GET `SnapshotHandler()` with
`?thread_id=T`. The JSON response is `{messages, cursor, activeRun}`; the handler
uses `Resolve(OpRead)` and the Go API accepts an already-authorized opaque key.
When a run is active, messages contain only the recorded history strictly before
its boundary. `activeRun` contains its public `threadId`, `runId`, and
`boundaryHostSeq`; `cursor` is `boundaryHostSeq - 1`. During admission the snapshot
waits, cancellation-safely, for boundary publication or release. If admission or
completion changes during the recorder read, it retries without holding the
Session lock. Idle snapshots contain all history, the last recorded HostSeq
(or zero), and `activeRun: null`. Abandoned inactive runs retain their recorded
partial content under the normal incomplete-run diagnosis.

Load snapshot.messages into the fresh client, then connect to ObserveHandler
with `after=snapshot.cursor`. The first event is the active run's real RUN_STARTED,
whose input seed contains its recorded user turn, followed by the full run suffix. This satisfies the
initial RUN_STARTED check in @ag-ui/client 1.0.3's `verifyEvents` and reconstructs
all message/tool openers before their continuations. Do not separately seed that
active user turn: the observation endpoint includes it. If the run finishes
between snapshot and connection, catch-up still supplies that complete sequence
and closes. When idle, observation catches up after the cursor and closes if no
run is active; the host decides when to refresh or reconnect. Route snapshot and
follow to the owning Session in a multi-worker deployment.

`MessagesHandler()` continues returning a bare AG-UI Message[] for simple
`history.load`. It includes the active partial turn and provides no cursor; use
the snapshot endpoint when attaching a fresh client to an active run.
The official client legitimately folds `metadata.agentAdaptor.seq` from observer
events into message and tool-call metadata. Snapshot.Messages and Messages() do
not acquire those transport cursors. Compare conversation state by removing only
that seq field (and empty containers introduced solely to hold it), retaining all
other metadata, IDs, content and errors. Fold and the Message API remain unchanged.
The separately documented live-versus-history ToolMessage.error behavior remains.

For an application that already applied and retained a prefix, an arbitrary
`after` cursor can append only the missing records. Its response may begin inside
a run/message: the consumer must retain protocol state, including RUN_STARTED and
open message/tool lifecycles. It is not a standalone stream for a fresh HttpAgent
verifier. We do not synthesize RUN_STARTED, which would duplicate the lifecycle
boundary without reconstructing missing openers. The first POST projects no user text append because that client already owns
the input message; the idempotent RUN_STARTED input seed is shared with replay.

`Revoke(key)` cancels all currently registered observers for that key without
cancelling execution or inventing a terminal. A callback already in progress may
finish; callbacks must return promptly. New observers undergo fresh authorization.
HTTP follow re-runs `Resolve(OpFollow)` before every delivery batch and every
100ms while idle; denial or a changed key closes cleanly. Hosts should revoke
immediately when grants change and make Resolve reflect the new grants. Revocation
is local to a Session: multi-worker hosts must route or broadcast revocation and
cancellation to the owning worker.

`sessionrecorder.Follower.Subscribe(ctx, key)` is an optional additive recorder
interface, implemented by the built-in recorder. It installs a subscription before
returning and provides coalesced, nonblocking notifications after successful append.
Cancel the subscription when done. Recorder decorators should forward Follower;
without it observers still use committed reads and periodic polling.
`CoordinatedEventBackend` adds atomic `AppendNext` allocation/append plus Follower.
Its notifications must follow durable commit and cover writes from all workers;
Load must return fresh committed records in increasing HostSeq order. The recorder
bypasses its local sequence/cache for this interface. Memory and JSONL behavior
remain single-process; they do not acquire distributed ownership from notifications.
A multi-worker host still supplies shared storage, per-thread admission/leases,
worker routing and consistent authorization. Recording failure in an unattended
run cancels/drains execution; local observers see `agui.recording_failed`, while
reconstructed history without a terminal reports `run.incomplete`.

If a storage failure occurs after an observer has consumed the last durable
record, no new HostSeq can truthfully be allocated. The Go observer returns an
`agui.recording_failed` InputError; the HTTP observer emits an unsequenced
RUN_ERROR as a transport-failure exception and closes. It never advances the
storage cursor or claims the missing terminal was persisted. A replay from an
earlier cursor may instead contain the synthesized failure at the last record;
that synthesis is history diagnosis, not a successful append.

If an observer has already acknowledged the last record of a run that later
becomes inactive without a terminal, it cannot amend that acknowledged cursor.
The incremental projector returns `run.incomplete` as an unsequenced transport
error (or `agui.recording_failed` for a known storage failure), using the same
failure delivery exception described above. Fresh history reads still synthesize
the diagnostic at the last durable record; later boundaries also close abandoned
runs through the shared projector.

The same late-diagnosis rule applies if a later boundary proves an already
acknowledged run was abandoned: the observer reports the unsequenced failure
and closes before delivering a new RUN_STARTED with an unmatched old lifecycle.
A new snapshot can then join the replacement run at its complete boundary.


## Offline protocol fixture check

Run `node bridges/agui/testdata/verify-client.mjs` to validate the golden handler
trace against the frozen schema, `@ag-ui/client` 1.0.3's verifier and HttpAgent
message folding. It uses the existing assistant-ui node_modules read-only and
never opens a socket. `AGUI_NODE_MODULES` can point to another existing install.
The script registers the standard `$anchor` annotation with Ajv and disables its
`strictRequired` schema lint for allOf-inherited properties; instance validation
retains every schema constraint. No package install or frontend change is needed.
The Go handler golden test independently compares the live normalized trace to
the fixture; regenerate only after reviewing protocol changes. The same script
independently checks the hand-derived Node-free reducer table and ten recorded
session turns against real HttpAgent folding/next-turn requests, frozen event and
Message schemas, and assistant-ui history conversion (including failed tool 13).
These fixtures cover repeated provider IDs, reasoning, activities, full user
messages, Result.Text fallback, business errors, cancellation and infrastructure
errors. Expected reducer states are not generated by the Go implementation.
