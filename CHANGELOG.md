# Changelog

All notable changes to this project are documented in this file.

## [Unreleased]

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
