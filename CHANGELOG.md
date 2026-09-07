# Changelog

All notable changes to this project are documented in this file.

## [Unreleased]

### Added

- `RunError.Cause`, `ReasonInfrastructure` and `ReasonDeadlineExceeded` preserve
  original error chains and available results after Driver entry.
- Driver SPI per-kind `NativeHITL` / `PromptValidateHITL` matrices; nil keeps
  legacy explicit-Ask compatibility, while precise matrices include effective
  inherited Ask requirements.
- Persistent Dedicated profiles with hosted Tools retain local provider session
  files in an identity-partitioned namespace. Profile-owned errors report
  contention, unsafe ownership, dirty generations and unsupported filesystems.
- Typed capability invocation and todo snapshot events, parent/scope coordinates,
  and optional per-run observers, publishers and observation demand.
- Opt-in live delegation artifact Parts and Append/LastChunk flags, preserving
  individual updates and complete final recovery.

- Capability-gated `WithAppendSystemPrompt`, its exact-byte SharedOption and
  Driver capability/request/error contract, independent of Prompt/Instructions.
- Ask-aware `Policy.ActiveExecutionTimeout` and typed local timeout diagnostics.
- Explicit-Store `hosttools/capabilityrecorder` with exact scoped live queries,
  bounded pagination, atomic idempotence and host-owned shared Store lifetime.
- Opt-in A2A capability/todo DataParts and parent/source fidelity, plus typed
  SSE, AG-UI and session-recording projections.

### Fixed

- Finish Claude result-only bidirectional one-shot runs by closing stdin once;
  nested subagent completion cannot close the root control channel.
- Deliver bounded safe `invalid_input` Tool corrections without argument values
  or raw validator errors, including through the hosted MCP transport.
- Await live A2A continuation outcomes instead of replayed Task terminal states;
  recover complete artifact extensions after disconnection and explicitly report
  incompatible recovery views without payload diagnostics.
- Preserve partial Result and cause on cancellation, protocol/store/cleanup
  failures, and preserve audit data across safe resume fallback. A2A outcome
  mapping prioritizes RunError's primary Reason over joined context causes.
- Normalize schema and validate transport/source candidates once before resource
  acquisition. Late observation demand selects only among those candidates and
  cannot discard applicable Ask requirements or repeat resource resolution.
- Keep profile cleanup retryable and never modify a successor generation after
  OS unlock. Dirty active generations require proven offline recovery.
- Preserve Claude and CodeBuddy resident partial output, formal transcripts,
  stdout/stderr tails, observed message usage and original process Wait errors,
  including early prompt-write failures; never replay delivered input or promote
  an unhealthy checkpoint. Claude decision abort now stops and drains its writer.
- Keep absent/invalid Claude usage distinct from observed zero, and aggregate
  per-message cumulative usage without counting repeated snapshots twice.
- Reconcile every admitted run's sole public terminal with the final Result and
  cleanup outcome. Preserve all numbered but undelivered events in the final
  drop report and unblock run publishers during teardown.
- Isolate failed/timed-out observers and snapshot resolved hosted-profile resources
  after the unique skill resolver, including proven managed reconciliation,
  actual file modes and unknown settings; different profile directories can run
  concurrently. Preserve existing MCP JSON/TOML file permissions during writes;
  reject non-regular targets and inspection errors without widening permissions.
- Keep compatible rich/schema/batch transitions on the same Thread checkpoint:
  per-turn Streaming no longer forces a rebind or discards a valid prewarm, while
  real configuration/codec/environment and private process signature guards remain.
- Deep-copy delegation artifacts across subscribers, replay and result accessors;
  enforce cumulative artifact byte limits before publication and report invalid
  content and compact-result count truncation with safe diagnostics.

- Preserve the first confirmed terminal cause and distinguish active exhaustion,
  parent deadlines and approval timeouts while retaining partial audit results.
- Reject memory Finalize writes canceled while waiting for its mutex; retain
  legitimately completed healthy commits after later cancellation.
- Isolate approval descriptive Choices/JSON Details while retaining one live
  responder; historical replay carries no responder. Preserve entire wrapped
  parent error graphs in subagent-stream configuration failures.
- Validate new A2A facts without breaking old args/raw/time compatibility;
  reject duplicate schema, same-scope self-parent and unsafe encodings, preserve
  opaque Thread keys, and normalize decoded occurrence times to UTC.

### Changed

- Execution errors after Driver entry now use `nil, *RunError` even for
  infrastructure failures; callers can continue matching original causes with
  `errors.Is/As`. Pre-invocation errors retain their existing wrapping.
- Hosted Tool calls now share `Definition.Invoke` validation. Schema defaults
  are descriptive and no longer implicitly inserted by the MCP wrapper;
  omitted fields follow the Go input type, as with direct calls.
- Clean Close retains Dedicated+Tools session files; other profile selections
  continue to remove temporary clones. Previously deleted transcripts cannot
  be recovered from resume identifiers alone.
- Claude native schema supports Question and PlanReview Ask; Permission Ask uses
  prompt validation. Effective inherited Ask makes zero-policy schema calls use
  prompt validation, while existing raw-policy interactive activation is unchanged.
- Default artifact events now expose an omission marker instead of original
  protocol Raw. Full Parts and artifact Raw require IncludeRemoteArtifacts;
  byte accounting now includes metadata and protocol Raw.
- Consumers of admitted runs now receive a core RunStarted/RunFinished envelope
  even without run services. Static pre-admission refusals retain empty closed
  Events and an error from Result.

- Active budget is settled and sealed before the sole atomic Thread persistence
  call. Finalize/return delay and cleanup do not spend active time; original
  context and Finalize errors still decide success. Whole Policy replacement
  includes the budget; append changes contribute to existing compatibility.
- `subagentstream.Merge` requires a completed-run binding proof for non-nil
  buses and transparently preserves the parent sequence/terminal. Migrate to
  a run-service attachment and the original Stream.
- AG-UI tool card IDs use a run/scope/tool tuple; graph consumers must handle
  `adapter.tool.parent`. A2A capability/todo exposure remains separately opt-in.
- Root With-function count increases from 26 to 27 for the sole native append
  option; the six nouns and Run/Stream execution model are unchanged.


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
