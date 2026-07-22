# CopilotKit, Sessions, And HITL

This is the full interactive-product showcase. A Next.js CopilotKit frontend
uses a Go AG-UI backend for content streaming, host-owned transcript replay, and
human decision cards. It also demonstrates how user turns enter the host recorder
without changing adapter-side output semantics.

## Architecture

```text
Browser <CopilotChat>
  -> Next.js /api/copilotkit -> HttpAgent
  -> Go POST /agent -> SDK Start
  <- AG-UI content + lifecycle events

Browser session panel
  -> GET /session/events?thread_id=...&after=...
  -> GET /decision/pending?thread_id=...
  -> POST /decision/resolve
```

The browser owns `ThreadID`. The backend maps it to the SDK session-key tuple
`("agui", ThreadID)`, while each invocation receives a distinct `RunID`. Replay
cursors use monotonic host sequence numbers, not per-run stream sequence numbers.

## Prerequisites

- Go toolchain
- Node.js 20+ and npm
- An installed and authenticated Codex, Claude Code, or Cursor Agent CLI
- Ports 8080 and 3000 available, or matching environment overrides

## Provider Support

| Capability | Codex | Claude | Cursor |
|---|---:|---:|---:|
| AG-UI run lifecycle | yes | yes | yes |
| Content deltas | yes | yes | no |
| PlanReview / Question `Ask` cards | no | yes | no |
| Permission `Ask` cards | no | no | no |
|| Subagent Activity cards (`activityType="subagent"`) | yes (collab) | yes (Agent/Task) | yes (taskToolCall) |

Claude currently demonstrates the richest HITL path. Unsupported `Ask` modes
are rejected before the provider run starts; the UI must not imply otherwise.

## Subagent Activity Cards (Phase 3)

When the AG-UI backend emits `ACTIVITY_SNAPSHOT` / `ACTIVITY_DELTA` frames with
`activityType="subagent"`, the frontend renders a `SubagentCard` alongside
existing tool-call and HITL decision cards.

### What gets rendered

- **Status badge** — `started | running | completed | failed | cancelled | input_required`
- **Agent name** — `agentName` (falling back to `agentKey`) and kind tag (`native` / `delegated`)
- **Description** — short task description field
- **Streaming text** — cumulative `text` with a live cursor while status is not terminal
- **Internal tool calls** — collapsible list of sub-tools the subagent ran, with expandable args/result
- **Error** — highlighted error block when `status=failed`
- **Footer** — elapsed duration and token-usage summary when available

### Frontend wiring

| File | Role |
|---|---|
| `web/app/lib/subagent-schema.ts` | Zod v4 schema for `ACTIVITY_SNAPSHOT` content |
| `web/app/components/subagent-card.tsx` | `SubagentCard` render component |
| `web/app/components/copilotkit-provider.tsx` | `AppCopilotKitProvider`: client wrapper registering `renderActivityMessages` |
| `web/app/layout.tsx` | Uses `AppCopilotKitProvider` instead of bare `<CopilotKit>` |

The existing `useCopilotAction({ name: "*" })` in `page.tsx` continues to
handle all `ToolCallMessage` frames (generic tool-call cards and HITL decision
cards) without modification — activity and tool-call rendering are orthogonal
channels.

## Setup And Run

```bash
./examples/showcases/web-copilotkit-hitl/start-all.sh claude
```

Open `http://localhost:3000`. For lifecycle and replay without provider HITL,
select `codex` or `cursor` as the first argument.

Manual setup:

```bash
# Terminal 1
./examples/showcases/web-copilotkit-hitl/start.sh claude

# Terminal 2
cd examples/showcases/web-copilotkit-hitl/web
npm ci
npm run dev
```

Backend environment: `AGUI_AGENT`, `AGUI_MODEL`, `ADDR` (default `:8080`),
`CORS_ORIGIN`, `THREAD_STORE_DIR`, and provider-specific `*_COMMAND` / `*_MODEL`
overrides. `NEXT_PUBLIC_AGENT_BACKEND_URL` configures the browser-side direct
AG-UI stream (default `http://localhost:8080/agent`); `AGENT_BACKEND_URL`
configures the optional Next runtime proxy fallback.
`NEXT_PUBLIC_AGENT_BACKEND_BASE` configures the session/decision side panel.

## Expected Evidence

The chat renders user and assistant turns, streaming content when available,
run status, and provider-supported decision cards. Refreshing the page restores
recorded events and pending decisions for the same browser thread. The backend
health endpoint is `http://localhost:8080/health`.

The screenshot below was captured by Playwright from a real, authenticated
Claude run in an isolated workspace/profile. It shows one user turn, the
`WEB_COPILOT_LIVE_OK` assistant response, and the host recorder history after
the run returned to idle; it is not a mockup.

![Authenticated Claude response and recorder history in CopilotKit](./assets/claude-authenticated-run.png)

## Cleanup

Press `Ctrl-C`; `start-all.sh` terminates the Go child. If `THREAD_STORE_DIR` was
set, remove or archive its JSONL records according to host retention policy.
The backend removes its temporary workspace/profile during graceful shutdown.
Remove `web/node_modules` when the dependency cache is no longer needed.

## Security Notes

This demo has no user authentication or authorization and defaults to permissive
CORS. Do not expose it publicly. Production hosts must scope thread and decision
access to an authenticated principal, protect decision resolution against replay,
validate retention paths, add TLS and limits, and audit all HITL outcomes.

## Known Limitations

- The default recorder and pending-decision store are process-local.
- JSONL persistence is an example backend, not a multi-process database.
- Permission `Ask` is unsupported by all current built-in adapters.
- Remote A2A sub-agent events require host-provided delegation wiring; this main
  path starts one local parent CLI. The runnable
  [`team-agent-workflow`](../team-agent-workflow) demonstrates the missing
  `delegate_to_agent` MCP + A2A role composition and produces the same
  `subagent.*` event bus that a CopilotKit host can attach through
  `sse.Options.SubagentBus`.

See [run policy](../../../docs/run-policy.md),
[streaming](../../../docs/streaming.md), and the
[session recorder contract](../../../docs/workstream-session-recorder.md).
