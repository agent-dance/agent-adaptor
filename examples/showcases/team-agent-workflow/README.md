# Claude And Codex Team Workflow

This showcase demonstrates a model-led software team built from the SDK's
existing host primitives. A Claude Code leader receives one MCP tool,
`delegate_to_agent`, and uses it to run an ordered workflow:

1. `plan` - Codex inspects the task and returns an implementation plan.
2. `impl` - Claude Code applies that plan in the shared workspace.
3. `review` - Codex reviews the diff and runs the acceptance checks.

The Go host registers the allowed roles and validates the outcome, but it does
not call those roles directly. The leader decides when to make each MCP call and
passes prior structured results to the next stage.

## Architecture

```text
Claude Code leader (read-only parent run)
  -> per-RunID HTTP MCP sidecar: delegate_to_agent
     -> host-curated a2adelegation.Registry
        -> plan Agent Card   -> A2A -> Codex Runner  (read-only)
        -> impl Agent Card   -> A2A -> Claude Runner (workspace write)
        -> review Agent Card -> A2A -> Codex Runner  (read-only)

All four runs share one temporary git repository.
DelegationEvent bus -> ordered plan/impl/review evidence
Host checks         -> go test + git diff --check + changed-file allowlist
```

The MCP sidecar is created by `RuntimeServiceManager.Ensure` after the SDK has
allocated the leader `RunID`. Runtime metadata promotes it into the leader's MCP
configuration, while `SecretEnv` delivers a per-run bearer token only to the
Claude subprocess. The leader subprocess also receives `MCP_TOOL_TIMEOUT` set to
`role-timeout + 30s`, so the MCP client does not cancel a still-valid A2A task.
Each registry entry points to a separate local A2A Agent Card.

## Prerequisites

- Go toolchain and Git
- Installed and authenticated Claude Code CLI
- Installed and authenticated Codex CLI
- Permission to bind ephemeral loopback ports
- Enough provider quota for four sequential runs: one leader and three roles

The default commands are discovered from `PATH`. Override them with
`CLAUDE_COMMAND` / `CODEX_COMMAND` or the command flags below.

## Provider Support

| Workflow role | Provider | Access | Responsibility |
|---|---|---|---|
| leader | Claude Code | parent requested read-only | Call only `delegate_to_agent` and carry structured context forward |
| plan | Codex | read-only | Inspect `TASK.md`, code, and tests; return a bounded plan |
| impl | Claude Code | workspace write | Modify only `slug.go` and run checks |
| review | Codex | read-only | Review the diff plus host-generated test evidence and emit the exact `TEAM_REVIEW_APPROVED` line only after checks pass |

The mapping is intentionally fixed so the example proves mixed-provider
coordination. It is not automatic provider routing in core SDK.

## Setup And Run

From the repository root:

```bash
go run ./examples/showcases/team-agent-workflow
```

Useful overrides:

```bash
go run ./examples/showcases/team-agent-workflow \
  -claude-command=/path/to/claude \
  -codex-command=/path/to/codex \
  -claude-model="$CLAUDE_MODEL" \
  -codex-model="$CODEX_MODEL" \
  -timeout=15m \
  -role-timeout=4m \
  -keep-workspace
```

Model flags are optional; omit them to use the provider configuration already
selected by your environment.

## Web mode (CopilotKit / AG-UI)

By default the example runs the fixed `plan -> impl -> review` workflow once on
the CLI and prints a JSON verdict. Pass `--web-mode` instead to expose the leader
over the AG-UI protocol so a CopilotKit frontend renders the whole run — the
leader's assistant text, reasoning, and `delegate_to_agent` tool calls, plus each
delegated role's progress:

```bash
go run ./examples/showcases/team-agent-workflow --web-mode
# AG-UI server on :8080 (POST /agent). Flags: --web-addr, --web-cors.
```

The backend mounts `sse.Handler(sdk, sse.Options{Protocol: sse.AGUI, SubagentBus: bus})`
at `POST /agent`. In this mode the fixed orchestration protocol is delivered to the
leader as instructions, so the run is driven by whatever you type in the chat (for
example "Implement the task in TASK.md"); the one-shot workspace validation and
sentinel checks are skipped.

Reuse the CopilotKit frontend shipped with the [`web-copilotkit-hitl`](../web-copilotkit-hitl)
showcase — it is a generic AG-UI client whose `AGENT_BACKEND_URL` already defaults to
`http://localhost:8080/agent`:

```bash
cd examples/showcases/web-copilotkit-hitl/web
npm install   # first run only
npm run dev   # http://localhost:3000
```

What renders in the browser:

- **Leader chat**: assistant text and reasoning stream live.
- **Delegation timeline**: each `delegate_to_agent` call renders as a tool-call
  card showing the target role and delegated prompt (args) and the returned
  `DelegationResult` (result) — i.e. the plan -> impl -> review sequence.
- **Subagent detail**: each delegated role's own `subagent.*` progress (including
  its internal tool calls) is emitted on the AG-UI stream as `CUSTOM` events
  (`name` prefixed `subagent.`). CopilotKit does not render `CUSTOM` events by
  default; hosts that want an inline subagent panel register an AG-UI activity /
  custom-event renderer for those names.

The frontend's side "Session" panel is the `web-copilotkit-hitl` host-recovery view;
this minimal team backend serves empty stubs for its `/session/events` and
`/decision/pending` polls (so the panel stays error-free) but does not persist a
session recorder — the live chat is the source of truth here.

## Expected Evidence

The example creates a tiny Go module whose `NormalizeSlug` test initially fails.
During execution stderr streams live progress on one shared, line-synchronized
console: the Claude leader's assistant text and reasoning appear under a
`┌─ leader (claude)` header, its `delegate_to_agent` tool calls appear as
`[leader] tool_call.start delegate_to_agent agent=plan` lines (with argument
deltas streamed under `┌─ leader (claude) · tool args`), and each delegated
role's output streams under a `┌─   ↳ plan` / `impl` / `review` header as it
arrives. Each delegated role's own tool activity is exposed too (the role A2A
servers enable `ExposurePolicy.IncludeToolCalls`), surfacing as
`[team]   ↳ impl tool_call.start <tool>` / `tool_call.result <tool>` lines.
Interleaved with that streaming text, the same console prints delegation
lifecycle lines in this exact order:

```text
[team] plan    subagent.started
[team] plan    subagent.finished
[team] impl    subagent.started
[team] impl    subagent.finished
[team] review  subagent.started
[team] review  subagent.finished
```

Success prints JSON with:

- `status: "passed"` and host-issued sentinel `TEAM_AGENT_WORKFLOW_OK`;
- the leader `RunID` and final output;
- one completed delegation summary for each ordered role;
- the three A2A Agent Card / JSON-RPC endpoints;
- passing `go test ./...` and `git diff --check` evidence;
- exactly one changed file: `slug.go`.

Immediately before review, the host runs `go test ./...`, `git diff --check`, and
the changed-file allowlist, then attaches those results and the full diff to the
A2A review prompt. This avoids depending on provider sandbox access to Go's build
cache and standard-library paths. Codex performs semantic review in read-only
mode. The host also hashes the complete workspace after implementation and after
review, reruns all checks after review, and uses `git status --porcelain`, so
review mutations and untracked files are not ignored. Stage hashes must satisfy
`initial = plan`, `plan != impl`, and `impl = review = final`; this makes the
implementation role the only permitted writer.

Skipping, reordering, repeating, or failing a role makes the process exit
non-zero. The host requires an exact `TEAM_REVIEW_APPROVED` line in the review's
structured MCP `DelegationResult`, reruns the workspace checks, and only then issues the sentinel.
Substring matches such as `NOT TEAM_REVIEW_APPROVED` are rejected. A leader
claim alone is never accepted as completion.

## Cleanup

The temporary repository, four cloned profiles, A2A role server, and per-run MCP
sidecar are removed or stopped automatically. `RuntimeServiceManager.ReleaseByRun`
shuts down the MCP sidecar. Use `-keep-workspace` only for inspection, then remove
the printed temporary root manually.

## Security Notes

- MCP accepts only host-curated registry keys; the model cannot supply an A2A URL.
- The MCP endpoint uses a random per-run bearer token passed through `SecretEnv`.
- A2A and MCP listeners bind only to loopback. The local A2A endpoints are not
  authenticated and must not be exposed outside this demonstration.
- Cloned profiles link available native authentication files and remain sensitive.
- Production hosts still need tenant authorization, durable audit storage,
  credential rotation, network policy, concurrency limits, and artifact policy.

## Known Limitations

- This is a fixed one-pass `plan -> impl -> review` workflow; it does not add a
  repair loop when review rejects the implementation.
- The task fixture is deliberately small and deterministic. Real repositories
  need stronger workspace isolation and conflict handling.
- Role task stores and the delegation event bus are process-local.
- Provider output and tool selection remain probabilistic; host-side validation
  turns deviations into explicit failures.
- The example has no UI. A CopilotKit host can pass the same event bus through
  `sse.Options.SubagentBus` to render `subagent.*` progress.

See the [A2A delegation guide](../../../docs/a2a.md), the standalone
[`a2a-local`](../a2a-local) bridge example, and the
[`web-copilotkit-hitl`](../web-copilotkit-hitl) interactive host.
