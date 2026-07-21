# Local A2A Bridge

This showcase starts an in-process A2A server around a normal `Runner`, discovers
it through an Agent Card, sends a streaming task with the repository A2A client,
then polls the completed task. It proves interoperability without moving serving
or routing responsibilities into core SDK.

## Architecture

```text
A2A client
  -> GET /.well-known/agent-card.json
  -> POST /a2a (JSON-RPC streaming task)
  -> pkg/bridges/a2a -> Runner
  -> selected local agent CLI
  <- task states + assistant artifact + terminal result artifact
```

The A2A `contextId` maps to the SDK session-key tuple
`("a2a-local", contextId)`. The bridge still creates a separate `RunID` for each
execution and stores concrete provider `SessionID` checkpoints behind the SDK.

## Prerequisites

- Go toolchain
- An installed and authenticated Codex, Claude Code, or Cursor Agent CLI
- Permission to bind a localhost TCP port

## Provider Support

Codex, Claude Code, and Cursor Agent use the same A2A surface. Provider content
stream fidelity still follows the selected adapter's stream capability.

## Setup And Run

Run the self-contained server/client proof:

```bash
go run ./examples/showcases/a2a-local -agent=codex
```

Run only the server on a stable port:

```bash
go run ./examples/showcases/a2a-local \
  -agent=claude \
  -serve-only \
  -addr=127.0.0.1:8080 \
  -timeout=0
```

Useful overrides include `-command`, `-model`, `-prompt`, `-expect`, `-context`,
`-workspace`, `-profile`, and `-timeout`.

## Expected Evidence

The normal demo prints JSON containing the Agent Card fingerprint, task and
context IDs, observed task states, artifact counts, final poll state, assistant
output preview, and isolated workspace/profile details. A missing sentinel,
transport failure, failed task, or provider failure exits non-zero.

## Cleanup

The default temporary workspace and cloned profile are removed on exit. Use
`-keep-workspace` only for debugging, then remove the printed directory. Stop
`-serve-only` with `Ctrl-C`.

## Security Notes

The demo server has no authentication and should stay on loopback. The isolated
profile links available native auth files; treat it as sensitive. Production A2A
hosts must add transport authentication, authorization, request limits, task
ownership, durable task/session stores, audit logs, and SSRF-safe discovery.

## Known Limitations

- The task store is bounded, in-memory, and expires entries after 30 minutes.
- The Agent Card advertises one generic skill; no automatic agent routing occurs.
- A2A serving, discovery policy, retries, and multi-tenant persistence remain
  host responsibilities.
- The file is intentionally end-to-end rather than a minimal recipe.

See the [A2A guide](../../../docs/a2a.md).

For a parent Claude Code agent that reaches multiple local A2A roles through an
MCP delegation tool, continue with the [`team-agent-workflow`](../team-agent-workflow)
showcase.
