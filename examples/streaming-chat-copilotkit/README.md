# streaming-chat-copilotkit

[简体中文 / Chinese Version](./README.zh-CN.md)

A complete **AG-UI + CopilotKit** frontend example, demonstrating the three vertical paths through `agent-adaptor`:

1. **Streaming**: text, thinking, and tool_call all stream at the token level
2. **HITL v2**: `dec.plan_review.*` / `dec.question.*` / `dec.permission.*` are rendered as clickable cards in the UI; the host fills the answer back via `POST /decision/resolve`
3. **Recovery**: each browser has a stable `thread_id`; the right-side panel restores history and pending decisions via `/session/events` + `/decision/pending`

The backend only invokes real local CLIs:

| `AGUI_AGENT` | Behavior |
| --- | --- |
| `codex` | Local `codex`, streaming via the codex app-server |
| `claude` | Local `claude` / `trpc-claudecode`, with PlanReview / Question entering HITL interaction |
| `cursor` | Local Cursor Agent CLI (defaults to the `agent` command, also tries `cursor-agent`) |

## Quick Start

```bash
./examples/streaming-chat-copilotkit/start-all.sh codex
./examples/streaming-chat-copilotkit/start-all.sh claude
./examples/streaming-chat-copilotkit/start-all.sh cursor
```

Open http://localhost:3000.

You can also start the backend only:

```bash
./examples/streaming-chat-copilotkit/start.sh claude
```

## Architecture

```
Browser
  ├─ <CopilotChat/>  (React, @copilotkit/react-ui)
  │     │  POST /api/copilotkit
  │     ▼
  │  Next.js CopilotRuntime + HttpAgent
  │     │  POST /agent  (AG-UI RunAgentInput)
  │     ▼
  │  Go backend
  │     ├─ sdk.Start(...)  →  agent-adaptor SDK
  │     ├─ handle.StreamEvents()  →  AG-UI Translator  →  SSE
  │     └─ handle.DecisionRequests()  →  pending store
  │
  └─ Direct fetch (side-channel) — used for HITL & recovery
         GET  /session/events?thread_id=T&after=N
         GET  /decision/pending?thread_id=T
         POST /decision/resolve
```

## Prerequisites

- Go 1.23+
- Node.js 20+ + npm
- The chosen `codex` / `claude` / `cursor` CLI is installed, logged in, and `--help` runs successfully

## Backend HTTP endpoints

| Path | Method | Purpose |
| --- | --- | --- |
| `/agent` | POST | AG-UI RunAgentInput -> SSE stream |
| `/session/events` | GET | History event replay |
| `/decision/pending` | GET | Outstanding decision requests |
| `/decision/resolve` | POST | Host fills back the HITL decision |
| `/health` | GET | Readiness probe |

## Environment variables

| Name | Default | Description |
| --- | --- | --- |
| `AGUI_AGENT` | `codex` | `codex` / `claude` / `cursor` |
| `AGUI_MODEL` | agent default model | Override the AG-UI backend model |
| `CODEX_COMMAND` / `CLAUDE_COMMAND` / `CURSOR_COMMAND` | auto-detected | Override the local CLI command |
| `CODEX_MODEL` / `CLAUDE_MODEL` / `CURSOR_MODEL` | agent default model | Override the model for the corresponding agent |
| `ADDR` | `:8080` | Backend listen address |
| `CORS_ORIGIN` | `*` | Allowed frontend Origin |
| `THREAD_STORE_DIR` | unset | Once set, uses JSONL persistence for the session recorder |

Frontend:

| Name | Default | Description |
| --- | --- | --- |
| `AGENT_BACKEND_URL` | `http://localhost:8080/agent` | The AG-UI endpoint that CopilotRuntime forwards to |
| `NEXT_PUBLIC_AGENT_BACKEND_BASE` | `http://localhost:8080` | Base URL for the browser side-channel requests |

## Related docs

- [`docs/workstream-hitl-v2.md`](../../docs/workstream-hitl-v2.md)
- [`docs/workstream-session-recorder.md`](../../docs/workstream-session-recorder.md)
- [`docs/run-policy.md`](../../docs/run-policy.md)
- [`docs/streaming-adapter-contract.md`](../../docs/streaming-adapter-contract.md)
