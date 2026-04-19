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
