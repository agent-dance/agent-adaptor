# Host-Owned SSE Server

This showcase wraps one `agent-adaptor` SDK instance in a minimal HTTP server.
It demonstrates where an AG-UI/SSE bridge belongs: above the core SDK and inside
the host that owns serving, authentication, tenancy, and persistence.

## Architecture

```text
browser or curl
  -> POST /v1/chat (JSON request)
  -> host HTTP server + pkg/bridges/sse
  -> SDK Start / StreamEvents
  -> selected local agent CLI
  <- AG-UI events over Server-Sent Events
```

## Prerequisites

- Go toolchain
- An installed and authenticated Codex, Claude Code, or Cursor Agent CLI
- A free local TCP port

## Provider Support

All three providers can execute through the endpoint. Codex and Claude provide
content deltas. Cursor currently completes through the unified result/transcript
path but does not advertise token-level `StreamEvents` content.

## Setup And Run

```bash
go run ./examples/showcases/web-sse -agent=codex -addr=:8080
```

Open `http://localhost:8080/`, or send a request directly:

```bash
curl -N -X POST http://localhost:8080/v1/chat \
  -H 'Content-Type: application/json' \
  -d '{"prompt":"Reply in one sentence","sessionKey":"demo/user-1"}'
```

Use `-agent=claude` or `-agent=cursor` to select another binding. `-command` and
`-model` are optional overrides. `ADDR` overrides `-addr` when set.

## Expected Evidence

The browser or curl stream starts with run lifecycle events, emits available
assistant text deltas, and ends with `RUN_FINISHED` or `RUN_ERROR`. Reusing the
same `sessionKey` resolves the same in-memory business session while the process
is alive.

## Cleanup

Press `Ctrl-C` to stop the server. The in-memory session store is discarded and
the temporary workspace/cloned profile is removed during graceful shutdown.

## Security Notes

This demo deliberately has no authentication and sends a wildcard CORS policy.
Bind it only to a trusted development interface. A production host must add TLS,
authentication, authorization, request limits, tenant isolation, durable session
storage, audit logging, and a restricted CORS policy.

## Known Limitations

- The HTML client renders assistant text only; it is not a complete AG-UI UI.
- The session store is process-local and ephemeral.
- There is no reconnect cursor, backpressure policy, or deployment hardening.
- The temporary workspace is intentionally empty; mount or provision project
  content explicitly in a real host.

See [streaming](../../../docs/streaming.md) and the
[run policy contract](../../../docs/run-policy.md).
