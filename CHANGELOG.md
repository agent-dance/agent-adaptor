# Changelog

All notable changes to this project are documented in this file.

## [Unreleased]

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
