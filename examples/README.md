# Examples

This directory contains runnable examples for the current `agent-adaptor` API.
Each example is an independent `main.go` with minimal self-checks. A successful
run exits with code `0`. A failed assertion exits non-zero.

## Prerequisites

- Go toolchain installed
- The repository checked out locally
- For the real Codex examples:
  - `codex` CLI installed and available on `PATH`
  - Codex already authenticated and usable from this shell

## Example Matrix

### `codex-basic`

Purpose:
- Validate the shortest default-agent path

Run:

```powershell
go run ./examples/codex-basic
```

Passes when:
- `sdk.Run(...)` succeeds
- `DriverType == "codex"`
- `ExitCode == 0`

### `codex-stream`

Purpose:
- Validate async execution, event consumption, and optional cancellation

Run:

```powershell
go run ./examples/codex-stream
go run ./examples/codex-stream -- -cancel-after=2s
```

Passes when:
- The success path emits at least one event and completes cleanly
- The cancel path returns a cancellation-shaped error

### `codex-sessions`

Purpose:
- Validate service-style session creation, reuse, continue, restart, and fork

Run:

```powershell
go run ./examples/codex-sessions
```

Passes when:
- `WithSessionKey(...)` creates then reuses a session
- `WithContinueSession(...)` reuses an exact session ID
- `WithNewSession(...)` returns a new session with `PreviousID`
- `WithForkSession(...)` returns a distinct session under a new logical key

### `codex-admin-named`

Purpose:
- Validate named agents plus Admin control-plane usage

Run:

```powershell
go run ./examples/codex-admin-named
```

Passes when:
- The default and named `review` agents both execute successfully
- `Admin().Agents()` reports both bindings
- `CheckEnvironment`, `ListModels`, `ListSkills`, and `SyncSkills` all return expected shapes

### `codex-skills-live`

Purpose:
- Validate the real skills usage path for Codex

Run:

```powershell
go run ./examples/codex-skills-live
```

Passes when:
- The prompt explicitly invokes the `write-proof` skill
- The Codex adapter injects `write-proof` into the effective `CODEX_HOME/skills`
- A proof file is created in the temporary workspace
- The file content matches the expected sentinel text
- `ListSkills` and `SyncSkills` return the expected control-plane state

Important:
- By default this example first probes the discovered `codex.ps1` command from `PATH`.
- If that external Codex command is healthy, the example uses it.
- If that probe fails, the example falls back to a bundled codex-compatible verifier command.
- The verifier still exercises the real Codex adapter path, including runtime skill materialization and `CODEX_HOME/skills` injection.
- If you want to target an external Codex binary instead, pass `-- -command=/absolute/path/to/codex.exe`.

### `streaming-chat`

Purpose:
- Validate the Go-channel streaming consumption path end-to-end against codex app-server

Run:

```bash
go run ./examples/streaming-chat "Write a haiku about streaming"
```

Passes when:
- `handle.StreamEvents()` yields at least one `StreamTextContent`
- `handle.Wait()` returns a non-empty `RunResult.Output`
- `result.Session.ID` is populated

### `streaming-sse-server`

Purpose:
- Validate the AG-UI SSE HTTP handler with an inline HTML page

Run:

```bash
go run ./examples/streaming-sse-server
# open http://localhost:8080
```

Passes when:
- `curl -N http://localhost:8080/v1/chat -d '{"prompt":"hi"}'` streams AG-UI events

### `streaming-chat-copilotkit`

Purpose:
- End-to-end AG-UI demo with the official CopilotKit React UI (tool rendering,
  reasoning, token streaming) backed by codex app-server

Run (two terminals):

```bash
# Terminal 1 — Go backend (port 8080)；Claude：`./examples/streaming-chat-copilotkit/start.sh claude`
go run ./examples/streaming-chat-copilotkit

# Terminal 2 — Next.js frontend (port 3000)
cd examples/streaming-chat-copilotkit/web
npm install && npm run dev
```

Passes when:
- http://localhost:3000 renders the CopilotKit chat UI
- Messages stream token-by-token via AG-UI over SSE

See [`examples/streaming-chat-copilotkit/README.md`](./streaming-chat-copilotkit/README.md).

### `streaming-chat-aguiclient`

Purpose:
- Minimal-middleware AG-UI demo: Vite + React + `@ag-ui/client` HttpAgent
  direct to Go backend, no CopilotKit, no Next.js

Run (two terminals):

```bash
# Terminal 1 — Go backend (port 8090)；Claude：`./examples/streaming-chat-aguiclient/start.sh claude`
go run ./examples/streaming-chat-aguiclient

# Terminal 2 — Vite dev server (port 5173)
cd examples/streaming-chat-aguiclient/web
npm install && npm run dev
```

Passes when:
- http://localhost:5173 renders a chat UI with user / assistant /
  reasoning / tool message cards
- The AG-UI events are consumed directly by the browser via
  `@ag-ui/client`'s `HttpAgent`; no Node.js runtime proxy in between

See [`examples/streaming-chat-aguiclient/README.md`](./streaming-chat-aguiclient/README.md).

### `mock-adapter-playground`

Purpose:
- Validate normalized request shape, typed binding, and per-call overrides without relying on a live CLI

Run:

```powershell
go run ./examples/mock-adapter-playground
```

Passes when:
- The typed config round-trips correctly
- `RunOption` values override binding defaults
- Binding metadata that is not overridden is preserved

### `mock-skills-contract`

Purpose:
- Validate deterministic skills payload assembly without relying on live Codex behavior

Run:

```powershell
go run ./examples/mock-skills-contract
```

Passes when:
- Binding default skills appear in the first captured `DriverRunRequest.Skills`
- Per-call `WithSkills(...)` overrides appear in the second captured payload
- `Requested`, `Resolved`, `Mode`, and `Fingerprint` all match expectations

## Smoke Runner

A PowerShell smoke runner is included:

```powershell
powershell -File ./examples/run_examples.ps1
```

Notes:
- Mock examples always run first.
- Real Codex examples run only if the `codex` CLI is available and passes a basic `codex --help` health probe in the current shell environment.
- `codex-skills-live` now runs by default because it can fall back to the bundled verifier when the external Codex command is unhealthy.
