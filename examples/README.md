# Examples

[简体中文](./README.zh-CN.md)

The examples are organized by how you use them, not by provider:

- `recipes/` are small programs that teach one host contract.
- `showcases/` are end-to-end integrations with product-shaped setup and cleanup.
- `tools/` are inspection utilities, not recommended first integrations.
- `internal/` contains shared live-CLI and deterministic contract support.

## Prerequisites

All examples require the Go toolchain. Entries marked **live CLI** also require the
selected local agent command to be installed and authenticated:

| Agent | Default command | Optional overrides |
|---|---|---|
| Codex | `codex` | `CODEX_COMMAND`, `CODEX_MODEL` |
| Claude Code | `claude` (then `trpc-claudecode`) | `CLAUDE_COMMAND`, `CLAUDE_MODEL` |
| Cursor Agent | `agent` (then `cursor-agent`) | `CURSOR_COMMAND`, `CURSOR_MODEL` |

Most multi-provider examples accept `-agent`, `-command`, `-model`, and `-timeout`.
They use timeouts and temporary workspaces/profiles where they can write files.

## Learning Paths

1. **First run:** `basic-run` -> `result-and-failure` -> `async-events`.
2. **Automation worker:** `session-continuity` -> `structured-output` -> `runtime-service`.
3. **Interactive product:** `content-streaming` -> `hitl-channel` -> `web-agui` or `web-copilotkit-hitl`.
4. **Multi-agent workflow:** `provider-selection` -> `named-agent-review` -> `a2a-local` -> `team-agent-workflow`.
5. **Managed environment:** `admin-preflight` -> `skill-injection` -> `managed-profile` -> `full-profile`.
6. **Adapter author:** `custom-adapter` -> `session-codec-inspect` -> [`adaptertest`](../adaptertest).

## Recipes

| Example | Runtime | Primary concept | Run | Expected result | Production note |
|---|---|---|---|---|---|
| [`recipes/basic-run`](./recipes/basic-run) | live CLI, Codex | Smallest public `sdk.Run` path | `go run ./examples/recipes/basic-run` | Assistant confirmation on stdout | Uses a temporary workspace/cloned profile and avoids internal helpers or explicit model IDs. |
| [`recipes/provider-selection`](./recipes/provider-selection) | live CLI, multi-provider | Explicit provider binding switch and preflight | `go run ./examples/recipes/provider-selection -agent=claude` | Healthy driver and one response | The host still owns routing; the SDK does not select an agent. |
| [`recipes/async-events`](./recipes/async-events) | live CLI, multi-provider | `Start`, operational `Events`, `Wait`, and cancellation | `go run ./examples/recipes/async-events -agent=codex` | Lifecycle/event counts and final output | Slightly over 120 lines because it verifies both success and cancellation branches. |
| [`recipes/content-streaming`](./recipes/content-streaming) | live CLI, capability-dependent | `WithStreaming` and `StreamEvents` | `go run ./examples/recipes/content-streaming -agent=claude` | Text deltas followed by a final result | Drain operational events too; Cursor currently has no token-level stream capability. |
| [`recipes/session-continuity`](./recipes/session-continuity) | live CLI, multi-provider | continue-or-start, continue-only, start-new, and fork | `go run ./examples/recipes/session-continuity -agent=codex` | Created/reused/forked session IDs | The in-memory store demonstrates semantics, not durable production storage. |
| [`recipes/named-agent-review`](./recipes/named-agent-review) | live CLI, Codex + Claude | Host-routed implement/review workflow | `go run ./examples/recipes/named-agent-review` | Separate implementer and reviewer output | Requires both CLIs; there is no automatic routing. |
| [`recipes/admin-preflight`](./recipes/admin-preflight) | local environment | Control-plane discovery without executing a prompt | `go run ./examples/recipes/admin-preflight -agent=cursor` | Environment, models, profile, and schema summary | Unsupported probes report truthful fallback data. |
| [`recipes/result-and-failure`](./recipes/result-and-failure) | offline | `error -> Failure -> success` and output layers | `go run ./examples/recipes/result-and-failure -fail` | Structured business failure; omit `-fail` for success | Uses the examples-only deterministic contract driver. |
| [`recipes/structured-output`](./recipes/structured-output) | live CLI, multi-provider | Typed JSON Schema output and decoding | `go run ./examples/recipes/structured-output -agent=codex` | Validated `ProjectMetadata` | Cursor uses the weaker prompt-plus-local-validation fallback. |
| [`recipes/hitl-handler`](./recipes/hitl-handler) | offline | Synchronous typed plan-review handler | `go run ./examples/recipes/hitl-handler` | Handler sees and approves a plan | Claude is the current built-in adapter with PlanReview/Question Ask support. |
| [`recipes/hitl-channel`](./recipes/hitl-channel) | offline | Async `DecisionRequests` / `ResolveDecision` | `go run ./examples/recipes/hitl-channel` | Request ID is resolved and the run continues | Long-lived hosts must handle expiry, cancellation, and persistence. |
| [`recipes/skill-injection`](./recipes/skill-injection) | live CLI, multi-provider | Skill selection and isolated profile materialization | `go run ./examples/recipes/skill-injection -agent=codex` | A temporary proof file containing `WRITE_PROOF_OK` | Slightly over 120 lines because it isolates and verifies profile/workspace writes. |
| [`recipes/runtime-service`](./recipes/runtime-service) | offline | Ensure, report, and release by `RunID` | `go run ./examples/recipes/runtime-service` | Ensured service report and matching release ID | The host, not core SDK, owns real process/container orchestration. |
| [`recipes/custom-adapter`](./recipes/custom-adapter) | offline | Minimal `DriverAdapter` + `BindTyped` | `go run ./examples/recipes/custom-adapter` | Echo output through the normal `Runner` path | Use `adaptertest` and declare only capabilities actually implemented. |

## Showcases

| Example | Runtime | Product shape | Start | Expected evidence | Production note |
|---|---|---|---|---|---|
| [`showcases/managed-profile`](./showcases/managed-profile) | live CLI, multi-provider | Binding defaults plus per-run profile resources | `go run ./examples/showcases/managed-profile -agent=codex` | Before/after snapshots and two successful runs | Uses a temporary workspace and cloned profile. |
| [`showcases/full-profile`](./showcases/full-profile) | live profile probes; optional model run | Skills, MCP, hooks, instructions, agents, and config together | `go run ./examples/showcases/full-profile -agent=codex -run=false` | Materialized files and local probe evidence | Auth is linked into an isolated profile; use `-run=true` only for a model call. |
| [`showcases/web-sse`](./showcases/web-sse) | live CLI, multi-provider | Host-owned HTTP server with AG-UI SSE | `go run ./examples/showcases/web-sse -agent=codex -addr=:8080` | Browser receives lifecycle and text events | Authentication, TLS, tenancy, and persistence remain host responsibilities. |
| [`showcases/web-agui`](./showcases/web-agui) | live CLI + Node 20 | React client talking directly to the Go AG-UI backend | `./examples/showcases/web-agui/start-all.sh codex` | Streaming messages and stable ThreadID mapping | See the showcase README for setup, cleanup, and limitations. |
| [`showcases/web-copilotkit-hitl`](./showcases/web-copilotkit-hitl) | live CLI + Node 20 | CopilotKit, sessions, replay, and HITL cards | `./examples/showcases/web-copilotkit-hitl/start-all.sh claude` | Plan/question cards resolve and the run continues | Claude currently provides the richest built-in HITL path. |
| [`showcases/a2a-local`](./showcases/a2a-local) | live CLI, multi-provider | Local A2A server plus client and task polling | `go run ./examples/showcases/a2a-local -agent=codex` | Agent Card, streaming artifacts, and final task | Serving, auth, task durability, and routing remain host-owned. |
| [`showcases/team-agent-workflow`](./showcases/team-agent-workflow) | live CLI, Claude Code + Codex | Claude leader delegates plan/impl/review through MCP to curated A2A roles | `go run ./examples/showcases/team-agent-workflow` | Ordered delegation events, passing workspace checks, and `TEAM_AGENT_WORKFLOW_OK` | Uses four sequential model runs in a temporary repository; production orchestration remains host-owned. |

## Tools

| Tool | Runtime | Purpose | Run | Output | Caveat |
|---|---|---|---|---|---|
| [`tools/session-codec-inspect`](./tools/session-codec-inspect) | offline | Inspect an adapter's public session parameter shape | `go run ./examples/tools/session-codec-inspect -agent=cursor` | Session codec JSON | Diagnostic utility; it does not start an agent CLI. |
| [`tools/live-smoke`](./tools/live-smoke) | live CLI, multi-provider | Cross-platform authenticated sentinel smoke | `go run ./examples/tools/live-smoke -agent=codex` | One JSON status: `passed`, `skipped`, `environment_failed`, or `run_failed` | Uses an isolated workspace/profile; exit codes are 0, 0, 2, and 3 respectively. |

## Verification

Deterministic validation:

```bash
go test ./examples/...
go run ./examples/recipes/result-and-failure
go run ./examples/recipes/hitl-handler
go run ./examples/recipes/hitl-channel
go run ./examples/recipes/runtime-service
go run ./examples/recipes/custom-adapter
```

The cross-platform live smoke harness reports `passed`, `skipped`,
`environment_failed`, or `run_failed` instead of treating a missing login as a pass:

```bash
go run ./examples/tools/live-smoke -agent=codex
go run ./examples/tools/live-smoke -agent=claude
go run ./examples/tools/live-smoke -agent=cursor
```

Use `-skip` only when the caller has explicitly disabled live validation. A
missing command, login, or quota is `environment_failed`, not `skipped`.
