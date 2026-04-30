# Examples

[简体中文 / Chinese Version](./README.zh-CN.md)

5 spotlight examples, each answering one host integration decision question. Every example runs against a real native CLI and supports switching between `codex` / `claude` / `cursor`.

## Prerequisites

- Go toolchain installed
- The selected native CLI is installed, logged in, and `--help` runs successfully in the current shell
- Default commands:
  - `codex` -> `codex`
  - `claude` -> `claude`, falls back to `trpc-claudecode` when not found
  - `cursor` -> `agent`, falls back to `cursor-agent` when not found

Common selection patterns:

```bash
go run ./examples/quickstart-cli -agent=claude
go run ./examples/quickstart-cli -agent=cursor -command=/absolute/path/to/agent

AGENT_ADAPTOR_EXAMPLE_AGENT=cursor go run ./examples/web-chat-stream -mode=cli
CODEX_MODEL=gpt-5.4 CLAUDE_MODEL=claude-sonnet-4 CURSOR_MODEL=gpt-5 go run ./examples/quickstart-cli
```

Available environment variables:

| Env | Purpose |
| --- | --- |
| `AGENT_ADAPTOR_EXAMPLE_AGENT` | Default agent: `codex` / `claude` / `cursor` |
| `CODEX_COMMAND` / `CLAUDE_COMMAND` / `CURSOR_COMMAND` | Override the native CLI command |
| `CODEX_MODEL` / `CLAUDE_MODEL` / `CURSOR_MODEL` | Override the default model |
| `AGUI_AGENT` / `AGUI_MODEL` | Agent / model override for AG-UI examples |

## Spotlight Matrix

| spotlight | Host scenarios | Run command | Walkthrough |
| --- | --- | --- | --- |
| [`quickstart-cli`](./quickstart-cli) | deploy-bot / CI step / postcommit hook — feed a prompt, take a chunk of text, and leave | `go run ./examples/quickstart-cli -agent=codex` | [`walkthrough.md`](./quickstart-cli/walkthrough.md) |
| [`web-chat-stream`](./web-chat-stream) | Web IDE / CopilotKit / customer-support seat — tokens land one by one + same-`sessionKey` continuation | `go run ./examples/web-chat-stream -mode=cli -agent=codex` | [`walkthrough.md`](./web-chat-stream/walkthrough.md) |
| [`multi-agent-platform`](./multi-agent-platform) | Internal dev platform / multi-tenant SaaS / team-level AI ops backend | `go run ./examples/multi-agent-platform` | [`walkthrough.md`](./multi-agent-platform/walkthrough.md) |
| [`human-in-the-loop`](./human-in-the-loop) | Compliance approval / PR auto-fix / IT change control | `go run ./examples/human-in-the-loop -agent=claude` | [`walkthrough.md`](./human-in-the-loop/walkthrough.md) |
| [`task-recipes`](./task-recipes) | incident hotfix / scheduled review / data migration / customer-support triage | `go run ./examples/task-recipes -agent=codex` | [`walkthrough.md`](./task-recipes/walkthrough.md) |

The "Walkthrough" link in each row points to the `walkthrough.md` next to that spotlight, all sharing the same five-section skeleton (Host scenarios / One-liner / Terminal artifacts / Filesystem artifacts / Where this lands in your product).

## Each Spotlight in Detail

### 1. `quickstart-cli`

- **Host scenarios**: deploy-bot / CI step / postcommit hook / `git ai-fix` — script-style products that feed a prompt, take a chunk of text, and leave.
- **Question answered**: Can it run in 30 seconds? What do `Output` / `Summary` / `RawStreams` / `Transcript` each look like?
- **Run command**:

  ```bash
  go run ./examples/quickstart-cli -agent=codex
  ```

- **Key artifacts**: four-panel display (Output / Summary / RawStreams / Transcript) + `.spotlight/quickstart-cli/quickstart-cli.json` + [`30-second-recipe.md`](./quickstart-cli/30-second-recipe.md). See [`quickstart-cli/walkthrough.md`](./quickstart-cli/walkthrough.md) §3 / §4 for details.
- **What this spotlight intentionally omits**: streaming / sessions / multi-agent / Admin control plane / HITL / task recipes — each owned by a later spotlight.

### 2. `web-chat-stream`

- **Host scenarios**: Web IDE / Cursor-like chat panel / CopilotKit / customer-support seat assistant / internal review assistant.
- **Question answered**: How much code does it take to expose the SDK as a typing endpoint for a React frontend? How do multi-turn conversations continue?
- **Run command**:

  ```bash
  go run ./examples/web-chat-stream -mode=cli -agent=codex     # smoke runner mode
  go run ./examples/web-chat-stream -mode=server -agent=codex  # browser demo mode
  ```

  `-mode=cli` enters the smoke runner; `-mode=server` is for manual testing / demos and does not run in smoke.

- **Key artifacts**: CLI-mode token-by-token typing transcript + Round 2 `[session reused: ...]` continuation evidence + `.spotlight/web-chat-stream/sse-capture.ndjson`; server mode browser triple test (typing / continuation / close-tab-then-reopen continuation). See [`web-chat-stream/walkthrough.md`](./web-chat-stream/walkthrough.md) §3 for details.
- **What this spotlight intentionally omits**: HITL decisions / multi-driver routing / task recipes / full Admin read-only API — each owned by a neighboring spotlight.

### 3. `multi-agent-platform`

- **Host scenarios**: internal dev platform / multi-tenant SaaS / team-level AI ops backend — multiple drivers in one process, scenario-based routing, every caller has its own identity and its own profile.
- **Question answered**: What does multi-driver routing + multi-tenant identity + control plane look like? What fields can the operations backend see?
- **Run command**:

  ```bash
  go run ./examples/multi-agent-platform \
      -default-agent=codex -review-agent=claude -autopilot-agent=cursor
  ```

  The flag defaults are exactly `codex / claude / cursor`, so passing them is optional; whenever any one is unhealthy, that named agent is automatically SKIPped instead of panicking.

- **Key artifacts**: operations report table (`Agents Overview`) + same-prompt routing comparison cards + clone profile directory tree for the three named agents + `.spotlight/multi-agent-platform/admin-snapshot.json` (combining `Agents` / `CheckEnvironment` / `ListModels` / `GetProfile` / `ConfigSchema` / `GetQuota` / `ListSkills`) + selection isolation evidence. See [`multi-agent-platform/walkthrough.md`](./multi-agent-platform/walkthrough.md) §3 for details.
- **What this spotlight intentionally omits**: streaming / HITL / task recipe details — each owned by a neighboring spotlight.

### 4. `human-in-the-loop`

- **Host scenarios**: finance / healthcare / compliance approval / PR auto-fix bot / IT change control — when an agent wants to run shell / call an external API / write a file, it must ask a real human first.
- **Question answered**: How do you ask the user about dangerous operations? What do reject / approve / timeout each look like? Where is the audit? Which Asks do the three drivers really support?
- **Run command**:

  ```bash
  go run ./examples/human-in-the-loop -agent=claude \
      -decision-timeout=6s -fake-front-end-delay=2s
  ```

  Switching to `-agent=codex` / `-agent=cursor` still runs — the three acts become SKIP, the capability matrix still appears, and the reasons clearly point at driver truth.

- **Key artifacts**: three-act play (`Sync Reject` / `Async Approve` / `Timeout Abort`) + capability matrix (driver truth table for the three) + `.spotlight/human-in-the-loop/audit/session.ndjson` (one line per decision) + [`audit-schema.md`](./human-in-the-loop/audit-schema.md) (audit field schema documentation). See [`human-in-the-loop/walkthrough.md`](./human-in-the-loop/walkthrough.md) §3 for details.
- **What this spotlight intentionally omits**: profile resources / streaming server / multiple named agents — each owned by a neighboring spotlight.

### 5. `task-recipes`

- **Host scenarios**: incident hotfix bot / scheduled code review / data migration workflow / overnight security scan / customer-support triage — products that contain N hard-baked tasks, each one corresponding to a bundle of instructions + skills + agents + hooks + config.
- **Question answered**: How do you declare a "task recipe"? How do default and specific recipes layer on top of each other? Which lines does the host change to write a new recipe?
- **Run command**:

  ```bash
  go run ./examples/task-recipes -agent=codex
  ```

- **Key artifacts**: recipe cards (`+` / `↻` distinguishes additive vs replace) + ProfileSnapshot diff + clone profile before/after directory tree + [`recipes.go`](./task-recipes/recipes.go) (the recipe dictionary you copy directly, importing only the public `agentadaptor` package) + [`recipes-cookbook.md`](./task-recipes/recipes-cookbook.md) (6 recipe paradigms). See [`task-recipes/walkthrough.md`](./task-recipes/walkthrough.md) §3 for details.
- **What this spotlight intentionally omits**: HITL / streaming / multi-agent / full Admin read-only API — each owned by an earlier spotlight.

## Extension reading

The two directories below are frontend-framework integration references, not spotlights:

### `streaming-chat-copilotkit`

CopilotKit + AG-UI demo.

```bash
./examples/streaming-chat-copilotkit/start-all.sh codex
./examples/streaming-chat-copilotkit/start-all.sh claude
./examples/streaming-chat-copilotkit/start-all.sh cursor
```

See [`streaming-chat-copilotkit/README.md`](./streaming-chat-copilotkit/README.md) for details.

### `streaming-chat-aguiclient`

Vite + React + `@ag-ui/client`, the browser talks directly to the Go backend without going through the CopilotKit Runtime.

```bash
./examples/streaming-chat-aguiclient/start-all.sh codex
./examples/streaming-chat-aguiclient/start-all.sh claude
./examples/streaming-chat-aguiclient/start-all.sh cursor
```

See [`streaming-chat-aguiclient/README.md`](./streaming-chat-aguiclient/README.md) for details.

## Internal regression utilities

[`examples/internal/`](./internal) hosts the SDK's own regression utilities and shared helpers (`exampleutil/`, `skills/`, `session-codec-inspect/`); they are not host-facing and do not appear in the spotlight list.

## Smoke Runner

The PowerShell runner first checks `--help` for the selected CLI and skips the whole batch if unhealthy; when healthy, it runs the non-server path of all 5 spotlights against the same agent, corresponding to the lines below in `examples/run_examples.ps1`:

1. `quickstart-cli`
2. `web-chat-stream -mode=cli` (server mode is excluded from smoke: it would hang)
3. `multi-agent-platform`
4. `human-in-the-loop`
5. `task-recipes`

```powershell
powershell -File ./examples/run_examples.ps1 -Agent codex
powershell -File ./examples/run_examples.ps1 -Agent claude
powershell -File ./examples/run_examples.ps1 -Agent cursor -Command "C:\path\to\agent.exe"
```

## Changelog

This refactor (2026-04-29) consolidated the 14 old examples into 5 spotlights + 2 extension-reading entries + `internal/` regression utilities. The table below is a migration cheat sheet; if any internal scripts / IDE tasks / blog links of yours hard-code an old path, update them via this table.

| Old path | New home | Notes |
| --- | --- | --- |
| `examples/codex-basic` | → `examples/quickstart-cli` | renamed + expanded into the four-panel output-layer demo |
| `examples/codex-stream` | → `examples/web-chat-stream` (`-mode=cli`) | merged: streaming CLI sub-mode |
| `examples/streaming-chat` | → `examples/web-chat-stream` (`-mode=cli`) | merged: same as above; dropped the `codex-` prefix and unified the name as `web-chat-stream` |
| `examples/codex-sessions` | → `examples/web-chat-stream` (`-mode=cli`) | merged: carried through with two same-`sessionKey` continuation rounds; semantics preserved in the sessions section of [`docs/usage-guide.md`](../docs/usage-guide.md) |
| `examples/streaming-sse-server` | → `examples/web-chat-stream` (`-mode=server`) | merged: HTTP SSE sub-mode (minimal inline-HTML frontend) |
| `examples/codex-admin-named` | → `examples/multi-agent-platform` | renamed + tightened narrative around "governance plane" (operations report / full Admin read-only / clone profile isolation) |
| `examples/codex-skills-live` | → `examples/task-recipes` | folded in: the `write-proof` skill as the minimum verifiable skill resource |
| `examples/profile-resources` | → `examples/task-recipes` | refactored: from "field-feature platter" to "task recipe" narrative (`recipes.go` + cookbook) |
| `examples/session-codec-inspect` | → `examples/internal/session-codec-inspect` | moved out of spotlights, archived as the SDK's own inspection tool |
| `examples/mock-runtime-admin` | → `examples/internal/mock-runtime-admin` | same as above |
| `examples/mock-adapter-playground` | → `examples/internal/mock-adapter-playground` | same as above |
| `examples/mock-skills-contract` | → `examples/internal/mock-skills-contract` | same as above |
| `examples/streaming-chat-copilotkit` | (kept, downgraded to extension reading) | no longer listed as a spotlight; frontend-framework integration reference |
| `examples/streaming-chat-aguiclient` | (kept, downgraded to extension reading) | same as above |
| **New** `examples/human-in-the-loop` | (new spotlight) | fills the HITL governance narrative gap (permission / plan-review / question, three decision types + capability matrix + audit ndjson) |

`run_examples.ps1` was also consolidated to the 5 lines per the migration above; the old entry points (`basic` / `stream` / `sessions` / `admin-named` / `skills-live` / `profile-resources`) have been removed.
