# Changelog

All notable changes to this project are documented in this file.

## [Unreleased]

### Added

- AG-UI snapshots with completed-history messages, an active-run boundary and
  a join cursor for fresh clients attaching to an unattended run.

- Opt-in unattended AG-UI sessions with bounded concurrency, host-owned lifetime,
  shutdown, cursor catch-up/follow, cancellation and observer revocation.
- Additive recorder change subscriptions and coordinated-backend contracts for
  atomic sequence allocation and post-commit multi-worker notifications.

- Optional `bridges/agui/aguisession` recorded execution, deterministic history
  projection, Message folding, history verification and HTTP helpers.

- AG-UI 1.0 input validation, structured transport errors, input diagnostics,
  context projection, history fallback and host verification/request hooks.
- Configurable tool-result truncation and live projection redaction/extension hooks.

### Changed

- AG-UI observers retain incremental projection state after one catch-up read,
  avoiding full-thread rereads and reprojection on follow notifications.

- Recorded AG-UI sessions use one operation-aware authorization/key resolver,
  shared request preparation and bounded SSE writes with optional keep-alives.

- AG-UI run boundaries echo public identities and advertise protocol 1.0.
  SDK/provider identities are namespaced metadata. Versioned requests require
  public identities; legacy requests retain identity-only leniency.
- AG-UI execution requires a trailing user message and rejects interrupt resume;
  missing prior checkpoints reject unless history rehydration is enabled.
  Migration: stateless hosts receiving full client history must configure a
  ThreadStore with resumable Threads or explicitly enable
  `sse.Options.AGUIInput.HistoryFallback`; otherwise subsequent turns return
  HTTP 409 `agui.history_unrecoverable`. The bundled SSE examples enable fallback.

### Fixed

- AG-UI observation pairs recorder reads with stable admission/completion cuts,
  preserving suffixes across consecutive runs and avoiding false incomplete-run
  terminals. Snapshot boundary waits are notification-driven and validated.
- Fixed-Thread AG-UI sessions use their actual host key for default history,
  observation and cancellation, matching execution admission.
- Unattended cancellation reaches pending admission; shutdown interrupts
  preparation and shares bounded cleanup waiting across repeated callers.
- Recorder Close releases change subscriptions and rejects coordinated reads and
  writes. Observer cursor stamping preserves opaque metadata; revoked observers
  close cleanly through error delivery, and established SSE errors stay SSE.

- Recorded AG-UI history now matches the official client reducer's metadata,
  activity replacement and corrected tool-result rules, including JSON patches.
- Recorded sessions reserve actual resolved Thread keys, recheck fresh branch
  destinations, reject forged control Notices and never report terminal-write
  failure or an empty-reason business error as successful completion. A checkpoint
  predating a failed recorded turn cannot silently attest that turn's history.
- Session projection version 2 scopes provider IDs by run, preserves complete
  submitted user messages through an idempotent input seed, and stamps events
  before redaction hooks. Draft version-1 recordings require explicit migration;
  the SHA-256 recorder-key mapping remains unchanged.
- SSE flush errors cancel recorded execution; cancellation still records cleanup.
  CopilotKit second turns explicitly rehydrate history, and its recovery panel
  consumes typed recorder envelopes and live approval fields.

- AG-UI run identities stay fixed after the opening boundary; step lifecycles
  close before terminal events and empty custom notices include their payload.
- AG-UI child tool ownership stays consistent with its opener, and subagent
  activity patches no longer change after subsequent tracker updates.
- Stateless AG-UI Agents accept public thread IDs without requiring a store;
  opt-in history fallback now executes on those Agents. Host/storage preparation
  failures report HTTP 500 instead of being misclassified as malformed input.
- Opaque AG-UI metadata preserves JSON number precision and large values.
  Fractional exit codes are not rounded into integers, and empty command output
  retains its exit-code suffix.
- Tool result failure/exit status survives recorded numeric representations,
  with explicit metadata and assistant-message parent attribution.

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
