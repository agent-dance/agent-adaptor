# Changelog

All notable changes to this project are documented in this file.

## [Unreleased]

### Added

- Pi and OpenCode Markdown profile-agent imports through `SourcePath`, with
  immutable capture before profile initialization, durable loading after source
  deletion, settings cloning and invocation overrides. Resolved source changes
  invalidate Thread compatibility even with a caller-supplied fingerprint.
  OpenCode ordered permissions survive JSONC reconciliation/clone and are
  verified before prompting; reordered external rules lose SDK ownership.
  Imports use strict YAML and explicit supported fields; ambiguous combinations
  with inline declarations fail before initialization. GPT 5.6 luna live checks
  proved actual child reads for both cloned and invocation-local imports.

- Verified OpenCode per-turn permission restoration using the native append-only
  session API. Repeated Ask/auto-approve changes retain the original baseline,
  native deny precedence and conversation, including WithSpawn; unchanged
  effective policies do not grow session rules. Saved project grants are checked
  through OpenCode's read-only database command before gated prompts. Nonempty
  grants or unverifiable state fail explicitly instead of bypassing host policy;
  grant mediation remains unsupported and the native entries are not modified.

- Pi profile agents with durable file ownership, fresh-Agent loading, settings
  cloning and invocation replacement/clear. The official extension registers
  `adaptor_subagent`; isolated native children use existing Pi transports and
  parsers. Child events, approvals, transcript, raw terminal data and token usage
  remain in the parent's run, with bounded cancellation/process teardown.
  Instructions, model/thinking and exact built-in tool policies are supported.
  Child skills preload captured selected/installed SKILL.md content through the
  native startup extension, with content-based Thread compatibility. Per-agent
  MCP key selection uses the resolved parent set without rediscovering excluded
  durable servers; missing resource references fail before prompting.
  GPT 5.6 luna proved durable/overridden instructions and child read/question/
  denied-write/cancellation behavior; advanced profile-agent fields remain open.

- OpenCode native child-session approvals, questions and plan review through
  the same core Event/Result pipeline. Verified task/session ownership scopes
  routing; child identifiers, transcript and usage survive parallel/nested
  tasks. Live GPT 5.6 luna checks cover child read, question, denied write and
  cancellation. Resumed children with session-specific allows are rejected
  under explicit Ask/auto-deny until those rules can be safely gated.

- OpenCode profile agents through owned native JSONC and invocation overlays,
  with instructions, model, reasoning variants, tool permissions and selected
  native options. Fresh-Agent delegation and per-call instructions passed
  GPT 5.6 luna tests through the native task tool. Settings clones preserve
  ownership; native overrides and residual merged fields fail before prompting.

- Pi file-managed and OpenCode native JSONC durable MCP synchronization, fresh
  Agent discovery, explicit per-call clear, bearer environment references and
  required-connection checks. All stdio/HTTP/SSE delivery cases passed GPT 5.6
  luna live tests. JSONC ownership/reconciliation uses pinned Tailscale HuJSON
  and preserves external edits, comments and unrelated configuration.

- Pi Coding Agent and OpenCode Drivers with captured package-owned Config,
  native JSON protocol parsing, Run/Stream audit parity, Thread resume/fork,
  cancellation, skills, instructions, profile probes and validated structured output.
- Pi MCP tools through its official extension API and the existing Go MCP SDK;
  OpenCode native per-run MCP configuration. Both support host-defined `WithTools`
  without rewriting shared provider profiles or retaining per-run endpoints.
- Hermetic protocol/checkpoint/transport tests, parser fuzz targets, and explicit
  double-gated live conformance covering native tools, hosted tools and fork isolation.
  Unsupported sandbox and browser policies are rejected before launch.
- OpenCode private authenticated server transport with native text/thinking deltas,
  permission/question replies and correlated `plan_exit` review through core HITL,
  native JSON Schema output, complete server audit data and safe session checkpoints.
  Native tool arguments remain snapshots because OpenCode 1.14.30 does not publish
  argument deltas. Controlled server teardown is distinct from invocation failure.
- OpenCode Thread server reuse with bounded single-writer replacement, isolated
  forks, idle reaping, WithSpawn and Agent.Close. Per-turn instructions, hosted
  tools and native schema output work on the same process; current approval
  policy replaces the previous turn's gates using the native permission baseline.
- Pi tool argument streaming and Permission/PlanReview/Question through an
  official extension with invocation-owned responders. Callback and Event responses share core
  approval policy; denial blocks actual tool execution and cancellation retains
  its Go error identity. Full Claude parity is tracked in docs/claude-parity.md.
- Pi RPC transport and persistent Thread processes with current-turn MCP and
  approval bindings, single-writer replacement, WithSpawn and Close. Native
  select/confirm/input/editor dialogs use Question events, including stateless
  calls with native extensions enabled. RPC checkpoints require an accepted
  prompt, settled agent, idle session state and successful schema validation.

- Pi and OpenCode durable profile skill synchronization, external ownership
  protection, observed snapshots and native-content compatibility guards.
  Fresh-Agent delivery is covered by double-gated GPT 5.6 luna live tests.
- Public `driver.ProfileResourceDriver` and profile snapshot vocabulary, so
  external integrations can implement ProfileState/SyncProfile without internal
  package signatures. The root consumer API is unchanged.

- Pi native additive/replacement and OpenCode native additive instruction
  synchronization for default/user scope, with observed ownership, external-edit
  protection, explicit clear, rollback on filesystem failure, and compatibility
  guards for changed native files. Fresh-Agent delivery passed GPT 5.6 luna tests.
  Invalid instruction bundles fail before profile initialization; file-backed
  invocation instructions are captured once before acquiring resources.
- OpenCode invocation-local replacement instructions through native agent
  prompts, including pure mode and custom default agents. Native settings are
  verified before prompt delivery, persistent execution processes are reused,
  and discovery/execution raw output is retained. GPT 5.6 luna live tests cover
  Run/Stream replacement and reuse without changing shared profile files.

### Fixed

- The Thread lease-renewal regression uses a controlled clock and observes
  early run termination, preventing an infinite test wait after a short lease
  expires under load. Production lease timing and semantics are unchanged.

- OpenCode gates native agent permissions before prompting under explicit Ask
  or auto-deny, retaining deny precedence. Switching gated/ungated policies
  replaces the persistent writer; compatible turns reuse it. Child task failure
  closes descendant lifecycles. Native tool argument snapshots are delivered
  on tool-call start, consistent with the declared absence of argument deltas.

- OpenCode rejects MCP server names that collide under its native sanitizer
  and verifies that higher-priority managed config cannot re-enable startup
  connections. The managed-config live test observes no premature MCP request.

- OpenCode MCP/settings clone selection no longer copies the other family.
  Verified MCP ownership and required policy survive cloning; existing native
  config groups preserve their precedence and failed commits roll back copies.
- OpenCode invocation MCP uses exact native API installation after disabling
  startup slots, preventing old headers/environment values from being inherited
  through deep config merging. Literal template text remains literal, changed
  connections replace persistent writers, and optional failures are observable.


- Explicit MCP declaration tracking in Driver profile payloads: undeclared
  resources survive synchronization, and explicit clear removes only verified
  owned entries, including shared profiles. Legacy ownership without content
  evidence is preserved as external. Driver SPI adds the `MCP` declaration bit.

- Profile and skill inspection no longer initializes or clones profiles in the
  Claude, Codex, Cursor and CodeBuddy Drivers. Explicit synchronization performs
  cloning; all six Drivers now run read-only profile conformance clauses.

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
