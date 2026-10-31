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
Thread. The bundled SSE examples opt into fallback for unavailable checkpoints.
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
when observed. Public IDs never become provider resume IDs.

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
This mapping is part of projection version `agui-session/1`; changing it requires
an explicit storage migration. It derives a portable name for recorder backends;
all recorder access must use that mapping. This introduces the usual SHA-256
collision assumption, rather than changing the opaque Thread identity.

Projection version `agui-session/1` records typed Notices with reserved kinds
`agentAdaptor.agui.boundary` (public identity, input protocol version,
projection version, diagnostics) and `agentAdaptor.agui.terminal` (authoritative
code/message). Between them are the input user turn, source events including
approvals, and any Result.Text fallback. Stream.Result is the terminal authority.
Only successfully recorded events are projected. Recording failure cancels and
drains execution, reports `agui.recording_failed` on the live connection, and
leaves incomplete history. Inactive runs without terminal records project
`run.incomplete`, never success. Disconnect cancels this request-scoped run.
The drain owns execution independently of the HTTP subscriber.

`Events(ctx, key, after)` returns ordered `[]ProjectedRecord` with HostSeq per
source record; consume all events in a record before advancing its cursor.
`Messages(ctx, key)` folds that same projection. `Fold` ports the official
@ag-ui/client 1.0.3 `defaultApplyEvents` (`Se`), tool-parent helper `xe`, and
metadata helper `L`. START reuses existing message IDs and CONTENT appends text.
Consequently the POST omits its recorded user triple: echoing it would duplicate
text already present in the client's input messages. History includes the triple.
Reasoning remains role=reasoning, as consumed by assistant-ui
`fromAgUiMessages` under showThinking. Tool failure metadata additionally becomes
ToolMessage.error. Activity snapshots and RFC 6902 deltas are folded.
Projection options, including deterministic redaction hooks, apply to both paths.

`VerifyHistory(ctx, input, runner, key) (string, bool, error)` also fits the generic
SSE handler extension. It compares IDs and roles in order, excluding activities
and the trailing input user turn; content is not compared. Divergence returns
409 `agui.history_diverged`; `ResolveDivergence` may choose a fresh key.
A match plus a readable checkpoint executes only the trailing user prompt.
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
No background execution, database, or follow endpoint is supplied here.

The official live client fold does **not** set ToolMessage.error from result
metadata. History folding adds it for assistant-ui history loading; live UIs
read metadata.agentAdaptor for tool status. Verification compares only IDs and
roles, ignoring both content and error. HttpAgent.prepareRunAgentInput in
@ag-ui/client 1.0.3 clones messages and strips only role=activity: reasoning,
tool results and tool-only assistants remain in the next request.

Session SSE writes use the same deadline writer as the generic handler.
Options.WriteTimeout defaults to 30 seconds per write; KeepAlivePing optionally
sends comment frames. Write failures cancel request-scoped execution and the
single drain finishes recording and cleanup.

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
followed by its recorded user turn and the full run suffix. This satisfies the
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
boundary without reconstructing missing openers. The first POST still suppresses
its submitted user turn because that client already owns the input message.

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
