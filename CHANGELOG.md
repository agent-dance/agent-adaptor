# Changelog

All notable changes to this project are documented in this file.

## [2.0.0] - Unreleased

### Breaking changes and migration

- The Go module is now `github.com/agent-dance/agent-adaptor/v2`; update root
  and subpackage imports together.
- Replace consumer `agent.Run(ctx, p)` with `agent.Send(ctx, p).Result()`,
  `agent.Stream(ctx, p)` with `agent.Send(ctx, p)`, and Thread execution with
  `th.Send`. Runner now requires Send; Stream wrappers must also forward its
  new message Send method. Driver.Run remains the extension SPI.
- Done is the authoritative last event and carries the same Result pointer and
  error as Stream.Result. RunFinished remains informational. Subscribe to Events
  before execution traffic when ordinary events are needed; Result-only use
  retains critical events and a Dropped summary instead of blocking producers.
- Named Threads use a per-Agent memory store by default. Inject a shared or
  durable WithThreadStore for continuity across Agents or restarts. Custom
  stores must atomically persist delivered-message receipts and bounded recovery
  history with checkpoint state under validated leases.
- Stream and Standby expose Cancel, with no Close. Agent.Close remains the
  idempotent owner-level process and Tool runtime cleanup operation.

### Core and results

- Added ThreadRunner and Thread.Standby while keeping the six-noun public model
  and a single Send execution pipeline. Anonymous Agent sends remain independent.
- Added Stream.Send with From, MessageID, and When(Soon/Interrupt/AfterTurn).
  Message options also apply at execution start; execution options cannot be
  passed to Stream.Send. Policy.Messages controls framing, pending capacity
  (default 32), turn budget (default 20), and delivery enablement.
- Added reliable MessageUpdate, per-provider-turn TurnEnd, final Done,
  Result.Turns, Messages(), Undelivered(), and Subagents(). Multi-turn executions
  retain complete raw streams/transcript, the last turn's text and terminal
  payload, and summed observed usage. Core delivery transcript entries preserve
  author authority without fabricating a provider user role.
- Added MessageError carrying rejected input and a wrapped cause; accepted input
  finishes delivered or failed with original text. MessageID dedupes within an
  execution and against bounded Thread receipts (1000 IDs, seven days).
- Added ApprovalRequest.Origin/Forward with one shared exactly-once responder.
  Requests with no OnApproval handler and no prior Events call immediately use
  the approval timeout fallback with a Notice.

### Standby and Thread recovery

- Standby accepts input until its creation context ends, has no overall Result,
  and forwards each execution's TurnEnd and Done events. Its Send context bounds
  admission only. Cancel stops current processing and queued input while keeping
  the conversation on standby. Execution options queue a separate execution.
- Repeated Standby calls for one key within an Agent return the live handle.
  A renewed ownership lease excludes other writers even while idle; task-mode
  Thread.Send fails with ErrThreadBusy for an active execution or live Standby.
- Failed run_failed input carries into the next execution under the same IDs.
  Only classified rate-limit/network/provider-server failures retry once;
  otherwise input waits for the next host message. A carried item can report
  failed/run_failed and later delivered; it is not silently marked successful.
- Store outages retain the latest checkpoint in process, publish a Notice, and
  retry persistence. Ownership is revalidated before recovery writes. Fresh
  session fallback preserves bounded earlier guidance and three recent turns.

### Message delivery SPI

- Added MessageCapability and per-invocation resolution, MessagePort Window/
  Signal/Claim/Ack, typed boundaries and size units, ContinueResume/InRun, and
  per-turn TurnCompleted events. Core owns ordering and accounting; every Driver
  claim must receive exactly one MessageAck before Run returns.
- Boundary failures requeue for turn-end continuation; ambiguous attempts carry
  a duplicate warning. Core resumes late messages admitted after an InRun
  Driver's last empty claim; without resume those messages explicitly fail.
- Delivery receipts persist atomically with healthy checkpoints. A crash after
  provider acceptance but before receipt persistence can replay input after
  recovery; MessageID dedupe does not guarantee exactly-once across that window.

### Codex

- App-server supports immediate turn/steer guarded by expectedTurnId, interrupt
  followed by turn/start, and multiple turns within one Driver call, for one-shot
  and resident transports. Declares minimum version 0.100.0 and persisted input.
- Native structured output uses app-server `turn/start.outputSchema`, including
  non-streaming Driver requests, with normal Thread process reuse and WithSpawn
  behavior. Every continuation and interrupt restart receives the schema.
- Without an explicit profile, CODEX_HOME or tenant identity, the Driver uses
  the CLI's own home (`~/.codex`), so the user's login and provider
  configuration apply as when running `codex` directly. Previously it fell back
  to an empty managed home unless HOME was bound explicitly, and requests failed
  authentication. Tenants keep per-tenant managed homes.
- Fix MCP invocation overrides so SDK Tool and Team servers start correctly;
  preserve literal server names, native servers, and explicit exact-tool policy.
- Preserve MCP and dynamic tool argument objects as complete ToolCall.Args
  snapshots, including nested values; non-object values keep an explicit wrapper.
- Avoid false Windows termination errors for processes proven already exited.
  Genuine process or transport errors invalidate checkpoints while preserving
  partial audit data.
- Exec resolves to core checkpoint continuation. Per-turn usage and output remain
  separate from execution aggregation. Message live conformance requires both
  the codex_live build tag and AGENT_ADAPTOR_LIVE_CONFORMANCE=1.

### Claude

- Stream-json input registers SDK main-agent prompt/tool/Stop hooks; tool context
  and Stop continuation share a serialized writer and claim ownership.
- Rejected or unanswered hook registration degrades after at most ten seconds
  waiting for a response to core checkpoint resume, with a Notice; ordinary
  prompt and HITL processing continue. Resident processes retain that outcome.
- Declares minimum version 2.1.110, tool/stop boundaries and InRun continuation;
  interrupt, echo confirmation and persistence of hook context remain false.
  Batch native-schema JSON transport uses core resume.

### Cursor

- Added authenticated loopback hook relay, per-run nonce framing, exact ownership
  checks and official hookAdditionalContexts echo confirmation. Hosts call
  RunHookRelayIfRequested first in main or configure a compatible HookRelay.
- Tool delivery requires an explicitly managed workspace and an unused hooks.json;
  setup/probe failure, user workspaces and Windows use core resume with a Notice.
  The total hook context is limited to 10000 UTF-16 units, with tag overhead
  reserved at admission. Declares minimum version 2026.10.14, no interrupt and
  no persistence of hook input across resume.

### CodeBuddy

- Added exactly one TurnCompleted for headless, control and resident turns,
  preserving existing Raw/Transcript/checkpoint behavior and propagating sink
  failures. Declares no verified mid-turn or interrupt capability; supplemental
  messages continue through core resume.

Built-in Drivers probe the installed CLI version and report the effective
message capability through Inspect.Environment. Unsupported or unknown versions
fall back to available core resume with a Notice.
Pi and OpenCode remain outside the supported v2 Driver set.

### Teams and A2A

- Replace `hosttools/a2adelegation` with `hosttools/team`: a delegation-first
  leader, four fixed leader tools, two local worker tools, host-selected local
  or Remote A2A members, one loopback MCP listener and execution-scoped tokens.
- Add asynchronous task handles, PerTask/PerScope continuity, queued concurrency
  limits, wake coalescing, leader relays, and worker approvals forwarded with
  `Origin{Member, Task}` under each member's own policy.
- Add named team Threads and Standby. Task-mode `Thread.Send` reports
  `ErrThreadBusy` for active work or live Standby; supplements use Stream.Send
  and Standby.Send. Team.Close releases host resources without closing Agents.
- Add TaskStore fencing, atomic task/wake commits, inbox claims, original-ID
  resubmission, interrupted-task recovery and `teamstoretest`. Recovered messages
  needing execution options remain held until the host restores those options.
- Add idle-scope Fork with durable lineage, copied terminal history/artifacts,
  and lazy leader/local PerScope conversation branching. PerTask conversation
  links and inboxes are not copied; PerScope Remote members reject Fork.
- Compose leader audit layers with `ComposeResults`, rebase message turn
  numbers, expose task summaries through `Subagents()`, and retain full member
  results/Remote snapshots in TaskStore. `CapMessageTurns` enforces remaining
  leader budget; aborted work at a team limit reports `ReasonLimitExceeded`.
- Replace the delegation-bus subagent bridge with typed `SubagentUpdate`
  projection and optional external-source Merge; Teams already include workers.
- Remote members retain origin-restricted authentication and policy, same-task
  messages (`Soon` only), observed context continuity, and a two-minute
  disconnected observation deadline. No approval-response extension,
  conversation Fork or output-schema negotiation is available; input-required
  is handled by remote policy. The A2A bridge routes active-task input to the
  existing Stream and projects v2 turn, message, subagent and completion events;
  a new task targeting a busy context is explicitly rejected.

### Integration hardening

- Preserve cancellation identity across provider process teardown, approval
  shutdown and lease renewal, while retaining diagnostics and provider failures
  observed before cancellation. Preflight failures return message receipts and
  cannot replay rejected input through Standby recovery.
- Preserve reliable terminal and message events during cancellation and paused
  subscriptions. Carried Standby input no longer prevents fresh host input from
  being admitted; internal event drains do not suppress approval fallback.
- Enforce framed message limits on combined continuation batches and turn-limit
  reminders, including UTF-16 overhead, without starting a turn for a reminder.
- Codex submits one ordered steer RPC per claimed batch and acknowledges confirmed
  RPC success before a later cancel. Late and replayed usage notifications are
  attributed to their own turns without double-counting resumed history.
- Keep SDK-declared Codex MCP servers enabled under read-only isolation. Derived
  approval defaults map AutoApprove to approve and Ask/AutoDeny to prompt,
  retaining exact per-tool overrides. App-server MCP consent reaches host
  approvals; blocked replies retire the transport within bounded cleanup.
- Bound Claude hook writer settlement, retire unusable resident processes and
  report ambiguous delivery truthfully. Hook degradation restores ordinary stdin
  lifecycle; Stop delivery preserves admission order and per-turn usage.
- Give a healthy Claude terminal up to 100ms for its hook writer to finish
  bookkeeping before retiring the resident. Cancellation and failed runs still
  interrupt writes immediately; blocked writes retain bounded cleanup.
- Fix team rejected-message replay, retry continuity, final task snapshots on
  every completion path, unread notes and Remote approval-state recovery.
  Add Result.WithSubagents for detached task-snapshot replacement without
  repeating usage accounting. Fork retains healthy local PerScope lineage.
- Keep team Standby contexts scoped to admission and drain reliable events on
  shutdown. AG-UI Activity restores member display names, worker text and
  validated structured attachments, excluding raw and private runtime data.
- Add AG-UI RunRegistry routing and a CopilotKit running-message input using
  the existing event stream for delivery receipts. Record safe Done fields by
  default; sessionrecorder.WithExposure explicitly enables diagnostic layers.
- Add adaptertest MSG-01–MSG-10 and transport fixtures for all four built-in
  Drivers. Stabilize event-loop and store-expiry tests across race instrumentation
  and Windows clock resolution. Snapshot lease timing before asynchronous store
  calls so abandoned hooks cannot read a later configuration lifetime.
  Synchronize v2 guides and all README languages.
- Use Go 1.26.10 for security fixes and upgrade the example frontend dependencies
  without lowering production audit thresholds.

### Added

- Optional `bridges/agui/aguisession` recorded execution with one Stream drain,
  durable typed source recording, ordered AG-UI events and materialized Message[]
  history, plus cursor-bounded snapshots for completed history and active runs.
- Opt-in unattended sessions with bounded concurrency, host-owned lifetime,
  shutdown, catch-up/follow observation, cancellation and observer revocation.
  Recorder subscriptions and coordinated-backend contracts support atomic
  sequence allocation and notifications after durable commit.
- A direct assistant-ui example with persistent Thread checkpoints and JSONL
  history, deterministic fake mode (13 commands, one failure), opt-in real CLI
  profiles, reload/observation/cancel UI, and official-client integration tests.
- AG-UI 1.0 validation and conformance fixtures, structured transport errors,
  input diagnostics, context projection, history fallback, configurable tool
  result limits, and deterministic redaction/extension hooks.

### Changed

- Run boundaries echo public request identities and advertise protocol 1.0;
  observed SDK/provider identities use namespaced metadata. Versioned requests
  require public IDs, while legacy requests retain identity-only leniency.
- Execution requires a trailing user message and rejects interrupt resume.
  Prior history needs a usable checkpoint or explicit text rehydration. Hosts
  receiving complete client history must inject a ThreadStore or enable
  `sse.Options.AGUIInput.HistoryFallback`; otherwise later turns return
  `agui.history_unrecoverable`. Bundled stateless examples enable fallback.
- History verification compares required message IDs/roles in order, excluding
  activities and optional reasoning. Text/reasoning lifecycles open only on
  non-empty content, accommodating assistant-ui reloads with thinking on or off.
- Notice projection uses a protocol allowlist for security: invocation/runtime/
  transcript/provider data can expose local paths, environment names and provider
  internals. Exact additional kinds require `WithNoticePassthrough`; protocol
  warnings, steps, approvals, drops and subagent activity remain supported.
- The assistant-ui real-driver example explicitly binds the operator CLI profile
  (HOME and Codex CODEX_HOME) for existing login/custom-provider configuration.
  Driver defaults are unchanged; changing bindings requires a compatible Thread.
- Direct clients pin `@ag-ui/client` 1.0.3. The CopilotKit example uses
  CopilotKit 1.83.0's bundled `@ag-ui/client` 1.0.2. Session projection format `agui-session/2` uses
  run-scoped provider IDs and an idempotent submitted-user seed; older recording
  formats require migration. Recorder-key encoding remains version 1.

### Fixed

- Codex app-server and batch output collection join process exit and both raw readers
  before returning a stopped process's stdout/stderr. Reaping no longer closes
  unread output pipes; malformed protocol tails remain available in Raw, and
  process exit independently wakes app-server turns and starts a five-second
  drain bound even without cancellation or EOF from inherited writers on any OS.
  Healthy persistent turns retain their live writer and return the bytes observed
  during the turn.
- App-server and batch pipe cleanup closes each owning file once, including
  stdin on start failure and persistent shutdown. Closure regressions check
  `os.ErrClosed` through `Read` on Windows and Unix; Windows `Stat` does not
  check the file's closed state.
- Codex one-shot/persistent app-server and batch execution classify cancellation
  as cancelled rather than agent failure, including explicit process shutdown.
  They retain available raw output and never persist a cancelled checkpoint.
  Session-initiated cancellation
  records `run.cancelled` even when a Driver reports an error from the killed
  process; independent failures keep their codes and diagnostics remain available.
- Recorded execution fails closed on storage errors and forged control Notices.
  Duplicate/active runs and diverged or unrecoverable histories are rejected;
  failed/incomplete turns cannot be attested by an older healthy checkpoint.
- Snapshot/observation reads pair committed storage with stable admission state,
  preserve suffixes across runs, and avoid false incomplete-run outcomes. Follow
  retains its projector and reads only new records after catch-up.
- Cancellation reaches pending admission; cleanup and SSE writes are bounded,
  write/flush failures cancel request-scoped runs, shutdown can be retried, and
  revocation detaches observers. Recorder Close releases subscriptions and rejects
  further operations. Fixed Thread execution/read/cancel use the actual host key.
- Live/history output preserves Result.Text fallback, tool failure/exit status,
  provider-neutral parent/subagent attribution, activity updates and opaque
  metadata. Tool output truncation respects UTF-8 and retains integral exit codes.
- Unknown identity metadata is omitted; event queries encode empty lists as [].
  The assistant-ui page suppresses unknown data blocks, uses mode-appropriate copy,
  and displays cancelled status live and after reload. The direct aguiclient
  handles content-array tool results with the official text conversion helper.
- Preserve recorded approval retry attempts and latency in AG-UI live/history
  projections. Share exact integer conversion with A2A approval/drop consumers,
  preserve decoded A2A payload numbers, and accept JSON numbers in task metadata.
- Reject colliding submitted AG-UI message IDs before recording or execution
  with HTTP 409 `agui.duplicate_message`, preserving idempotent history seeds.
- Preserve opaque JSON numbers through typed recorder payloads, live/replayed
  events and Message[]; reject fractional exit-code coercion after persistence.
- Enforce the frozen non-null `state` constraint on versioned and legacy input.
- Make assistant-ui checkpoint directory durability platform-aware on Windows;
  add scoped authenticated restart and visible activity parity fixtures, and
  enforce the example's build, Node suite and input fuzz target in CI.
- Upgraded the CopilotKit example to CopilotKit 1.83.0, `@ag-ui/client` 1.0.2
  and stable Next.js 16.4.3, with `zod` 4.6.7, `undici` 6.29.1 and
  `graphql-scalars` 2.0.1 overrides for advisories that have no upstream fix in
  the required ranges, so `npm audit --omit=dev --audit-level=high` passes
  again. The remaining moderate `@hono/node-server` and low `katex` findings are
  documented in the example README.

## [1.1.2] - 2026-08-07

### Added

- Licensed the project under the Apache License, Version 2.0, with a NOTICE,
  third-party attribution for schema-derived material, contribution terms, and
  matching license notices across all README translations.

### Fixed

- Enabled the declared persistent-process behavior for Claude, CodeBuddy, and
  Codex Threads on Windows, including shared `.cmd`/`.ps1` command preparation,
  process-tree cancellation, single-writer handoff, and native Windows
  lifecycle regression coverage.
- Updated the CopilotKit example's `fast-uri` override to 3.1.5 so production
  dependency audits no longer include GHSA-7p8r-x3mc-p8w7 at high severity.

## [1.1.1] - 2026-07-31

### Added

- `tool.AsRejection` for recognizing wrapped errors created by `tool.Reject`
  without exposing or trusting an application-implementable rejection type.
- A separate `ProfilePayload.SessionCompatibilityFingerprint` and
  `SessionFingerprint()` Driver SPI contract, keeping exact profile
  materialization distinct from resumable-session compatibility.

### Fixed

- Moved hosted Tool profile resolution and allocation inside Agent lifecycle
  admission so `Agent.Close(ctx)` remains bounded and retry cleanup cannot miss
  a concurrently created private profile.
- Prevented application errors from forging model-visible Tool rejections by
  implementing the former public method shape.
- Replaced the process-wide hosted Tool credential env name with an
  unpredictable per-Agent name, and reject explicit or runtime-published MCP
  servers that attempt to alias it.
- Preserved the concrete `ProfilePayload.Fingerprint` across Thread execution;
  only the separate session compatibility guard now normalizes Agent-owned
  ephemeral endpoint and credential-carrier allocations.
- Normalized already-closed listener errors across both Tool gateway admission
  fencing and HTTP shutdown so retry cleanup remains idempotent on Linux.

## [1.1.0] - 2026-07-31

### Added

- Provider-neutral host-defined Tools through typed Go handlers,
  construction-only `WithTools`, inferred or explicit JSON Schemas, safe
  model-visible rejection, tri-state behavioral annotations, deterministic
  Thread compatibility, and an
  Agent-owned authenticated loopback runtime backed internally by the official
  MCP Go SDK.
- Hermetic end-to-end coverage that launches a real provider fixture, reads its
  materialized MCP profile, initializes/lists/calls the hosted Tool, verifies
  unauthorized access, resumes a Thread across Agent reconstruction and a
  forced endpoint change, and proves `Agent.Close` removes both the endpoint
  and its isolated execution profile without modifying the source profile.

### Changed

- An explicit `WithProfile` selection now takes precedence over a provider
  `CommonConfig.Env` profile-directory binding, matching the construction
  option's nearer-scope semantics and enabling safe internal execution clones.
- `Agent.Close` now fences and cancels active runs, uses bounded provider-close
  and drain phases, safely cleans Tool execution profiles, and leaves a timed
  out close retryable. The frozen root `With*` surface
  intentionally increases from 25 to 26 with `WithTools`.
- The README and core usage documentation are now complete in English,
  Simplified Chinese, German, Japanese, and Korean.

### Fixed

- Legacy closed-listener failures are now classified consistently during Tool
  runtime shutdown.

### Removed

- Deprecated implementation files, migration plans, and stale pre-v1
  references that no longer describe the v1 SDK contract.

## [1.0.0] - 2026-07-30

This release is a clean v1 cutover from the `v0.12.0` public baseline.

### Added

- A six-noun public model: Agent, Thread, Stream, Event, Result, and Driver.
- One constructor for configured agents and one `Runner` contract shared by stateless Agents and stateful Threads.
- Host-keyed Threads with continue-or-start, resume-only, fork, checkpoint, lease, fingerprint, and atomic-finalization semantics.
- One typed Event stream carrying text, thinking, tools, process details, lifecycle notices, drop reports, subagent updates, and approval requests.
- Self-resolving approval requests with exactly-once `Approve`, `Deny`, and `Answer` operations.
- Structured output through `RunAs[T]`, `WithSchema[T]`, `WithSchemaJSON`, and `Result.Decode`.
- Read-only inspection plus explicit profile state, synchronization, and skill selection.
- Public `skill`, `mcp`, `profile`, `threadstore`, and `memory` vocabularies.
- Top-level SSE, AG-UI, A2A, and subagent-stream bridges; an A2A client; optional delegation and event-recorder host tools.
- A Driver conformance suite for built-in and third-party integrations.
- Persistent provider processes for Claude, CodeBuddy, and Codex Threads, plus a guarded real-CLI Godog BDD suite.

### Changed

- The minimum Go toolchain is 1.26.5 so consumers receive the required standard-library security fixes.
- Built-in providers now expose their own `Config` type and a `Driver(Config)` constructor.
- Batch and live execution share one invocation pipeline; batch execution is the live pipeline drained to its final Result.
- Construction defaults and per-call overrides use one option vocabulary with compile-time scopes. Skills append; other values follow their documented replacement or merge rules.
- Conversation identity is host-owned through one opaque Thread key. Provider resume identifiers remain checkpoint details.
- Business failures use the Go error path through `*RunError`, which retains the available Result.
- Result output is split into assistant text, short summary, complete raw streams and terminal payload, normalized transcript, observed service reports, usage, metadata, and structured output.
- Driver protocol parsers are solely responsible for transcript, output, terminal payload, and checkpoint validity.
- Runtime-service MCP publication uses typed fields rather than string metadata conventions.
- Bridges and host tools consume only the public Runner, Stream, Event, and Result contracts.
- Stateful Threads use persistent provider processes by default where declared; `WithSpawn()` opts an Agent or one call into a fresh process, and `Agent.Close` reaps owned processes.
- Structured output now has one automatic behavior: provider-native schema enforcement is preferred and Prompt plus local validation is used as the fallback.

### Removed

- The central execution object, built-in named-agent registry, default-agent binding model, and string-based agent lookup.
- The parallel asynchronous execution entry and its split operational, semantic, and decision channels.
- Binding wrappers and provider constructor sugar that created a second construction model.
- The control-plane façade; read-only probes and explicit profile operations now live on Agent.
- Same-key start-new session rebinding; an unrelated conversation now requires a new host-owned Thread key.
- Consumer and Driver SPI structured-output mode selectors; capability negotiation is owned entirely by core.
- Legacy forwarding package paths, provider-only compatibility packages, stringly runtime metadata parsing, and migration-only aliases.

### Reliability

- Prevented failed and non-zero-exit runs from overwriting healthy Thread checkpoints.
- Added collision-free composite key encoding, complete configuration fingerprints, fork compatibility checks, lease ownership tokens, bounded release, and atomic rebind semantics.
- Made Event backpressure and cancellation safe for blocked publishers and approval waiters while preserving critical and terminal events.
- Preserved complete raw stdout, stderr, transcript, and official provider terminal payloads, including Codex app-server runs.
- Hardened Driver lifecycle, sequence ownership, transcript mirroring, capability truthfulness, checkpoint codec, and approval contracts in the conformance suite.
- Closed protocol-fidelity gaps across SSE, AG-UI, A2A, and subagent-stream bridges.
- Added single-writer handoff, pre-delivery-only fallback, idle reaping, idempotent Close, and cross-turn app-server/control-channel reuse tests.

### Migration

- The final public API and option scopes are documented in the [API reference](./docs/api-reference.md).
- Runnable final-shape integrations are available under [examples](./examples).
