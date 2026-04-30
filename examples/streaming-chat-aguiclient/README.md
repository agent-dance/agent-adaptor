# streaming-chat-aguiclient

[简体中文 / Chinese Version](./README.zh-CN.md)

A **minimal-middle-layer** AG-UI frontend example: Vite + React + `@ag-ui/client`'s `HttpAgent` wired straight to the Go backend, **without** Next.js, **without** CopilotKit.

Relationship to `streaming-chat-copilotkit/`:

| | streaming-chat-copilotkit | **streaming-chat-aguiclient** |
|---|---|---|
| Frontend stack | Next.js + CopilotKit | Vite + React + `@ag-ui/client` |
| Middle layer | Next.js API route + CopilotRuntime | none |
| Dependency footprint | 900+ npm packages | 80+ npm packages |
| Suited for | Hosts that want CopilotKit's full UI components and ecosystem | Hosts that want the smallest middle layer and a direct AG-UI protocol alignment |

## Architecture

```
Browser (React + @ag-ui/client HttpAgent)
    │
    │  POST /agent   (AG-UI RunAgentInput)
    ▼
Go backend (agent-adaptor + pkg/bridges/sse)
    │
    │  Local codex / claude / cursor CLI (see AGUI_AGENT below)
    ▼
Local agent subprocess (token-level stream)
```

`HttpAgent` is AG-UI's **official browser client**. Its responsibilities:

- Build the `RunAgentInput` request body (`threadId` / `runId` / `messages` / `state` / `tools` etc.)
- Parse the AG-UI SSE response (via the official Zod schema layer + the built-in verifier)
- Expose a `subscribe(...)` event subscription interface for the UI to consume

In other words: **the browser and Go backend speak AG-UI natively**, with no protocol-translating proxy. AG-UI client-side validation rules (the first event must be `RUN_STARTED`, the literal `role: "reasoning"`, lifecycle pairing, etc.) are enforced inside `HttpAgent`; the Go backend's `pkg/bridges/agui` is responsible for keeping the output stream compliant.

## Prerequisites

- Go 1.23+
- Node.js 20+ with npm / pnpm
- One of: **Codex**, **Claude Code**, or **Cursor Agent** local CLI, logged in

## Running it

### One-liner scripts (paths relative to the repo root)

```bash
# Go backend only (Codex by default)
./examples/streaming-chat-aguiclient/start.sh

# Use Claude Code as the backend
./examples/streaming-chat-aguiclient/start.sh claude

# Use Cursor Agent as the backend
./examples/streaming-chat-aguiclient/start.sh cursor

# Same terminal: backend + Vite (Go in the background, npm dev in the foreground)
./examples/streaming-chat-aguiclient/start-all.sh
./examples/streaming-chat-aguiclient/start-all.sh claude
./examples/streaming-chat-aguiclient/start-all.sh cursor
```

### Manual (two terminals)

```bash
# Terminal 1 — Go backend, listening on :8090
go run ./examples/streaming-chat-aguiclient

# Terminal 2 — Vite dev server, listening on :5173
cd examples/streaming-chat-aguiclient/web
npm install
npm run dev
```

The backend driver is controlled by the **`AGUI_AGENT`** environment variable: `codex` (default), `claude`, or `cursor`. The first script argument `codex` / `claude` / `cursor` writes that variable.

Open http://localhost:5173 and send a message. You will see:

- Four kinds of message cards rendered in distinct colors: **User / Assistant / Reasoning / Tool**
- Token-level incremental streaming
- Reasoning (codex's thinking trace) collapsed independently
- Tool calls rendered as args / result cards

## Code map

```
main.go                      # Go backend: pkg/bridges/sse + custom RunAgentInput decoder
web/
├── package.json             # vite + react + @ag-ui/client (only 3 core packages)
├── vite.config.ts           # AGENT_BACKEND_URL env var injection
├── index.html
└── src/
    ├── main.tsx             # React entry
    ├── App.tsx              # HttpAgent + subscribe + handwritten chat UI (~200 lines)
    ├── index.css
    └── env.d.ts
```

The key code lives in `src/App.tsx`:

```tsx
import { HttpAgent } from "@ag-ui/client";

const agent = new HttpAgent({ url: __AGENT_BACKEND_URL__ });

// Subscribe to any AG-UI event
const sub = agent.subscribe({
  onTextMessageContentEvent({ event }) { /* token stream to UI */ },
  onReasoningMessageContentEvent({ event }) { /* thinking trace */ },
  onToolCallStartEvent({ event }) { /* tool card */ },
  onToolCallResultEvent({ event }) { /* tool result */ },
  onRunFinishedEvent() { /* done */ },
});

// Trigger an agent run
agent.addMessage({ id, role: "user", content: text });
await agent.runAgent();
```

`HttpAgent` internally handles all standard AG-UI behaviors (SSE parsing, event ordering, history maintenance, reconnection). The host just subscribes to the events it cares about and renders.

## Environment variables

Backend:

- `AGUI_AGENT`: `codex` (default), `claude`, or `cursor`
- `AGUI_MODEL`: override the AG-UI backend model
- `ADDR`: listen address, default `:8090`
- `CODEX_MODEL`: when using codex, default `gpt-5.4`
- `CLAUDE_MODEL`: when using claude, default `claude-sonnet-4`
- `CURSOR_MODEL`: when using cursor, default `gpt-5`
- `CODEX_COMMAND` / `CLAUDE_COMMAND` / `CURSOR_COMMAND`: override the local CLI command
- `CORS_ORIGIN`: allowed frontend origin, default `http://localhost:5173`

Frontend:
- `AGENT_BACKEND_URL`: backend endpoint, default `http://localhost:8090/agent`

## Why this example exists (design intent)

`streaming-chat-copilotkit` is the canonical "complete AG-UI frontend experience". But it depends on the Next.js + CopilotKit suite (900+ npm packages + a CopilotRuntime middle layer).

This example targets the following use cases:

1. **Embedding into an existing React app**: you only want to add a chat component, not adopt a full Next.js project
2. **Validating agent-adaptor compliance with minimum dependencies**: the fewer middle layers there are, the easier it is to see whether `pkg/bridges/agui`'s output truly conforms to AG-UI
3. **Mobile / embedded frontend**: Vite's bundle output is easy to port to React Native / Electron / Tauri
4. **Learning the AG-UI protocol**: the direct-wire view shows most clearly how the official client consumes the AG-UI stream

Both examples are maintained long-term. Which one to pick depends on the host's specific ecosystem.

## Related docs

- [`docs/streaming.md`](../../docs/streaming.md)
- [`docs/workstream-streaming-chat.md`](../../docs/workstream-streaming-chat.md)
- AG-UI protocol: https://docs.ag-ui.com
- `@ag-ui/client`: https://docs.ag-ui.com/sdk/js/client/overview
