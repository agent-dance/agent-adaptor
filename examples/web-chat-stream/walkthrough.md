# web-chat-stream · walkthrough

[简体中文 / Chinese Version](./walkthrough.zh-CN.md)

> This file is the **static walkthrough** (what it should look like). Every spotlight run also produces
> `.spotlight/web-chat-stream/last-run.md` as the **dynamic factual mirror** (what was actually seen this time).
> Read this file during PR review; pair both side by side for after-the-fact troubleshooting.

## 1. Host scenarios

Any product where the browser must visibly stream tokens one by one, must support cancellation, and must support "continue from where the last one left off":

- **Web IDE / Cursor-like chat panel**: a chat drawer on the right, tokens render as they arrive, context is shared between prompts
- **CopilotKit integration**: frontend components consume `text/event-stream` directly, AG-UI protocol frames need zero rework
- **Customer-support seat assistant**: every conversation window has a `sessionKey`, the agent remembers the customer identity and prior questions across turns
- **Internal review assistant**: while the reviewer reads, the agent emits suggestions; cancel mid-flight, restart, continue — none of it loses the context
- **Any "typewriter effect + multi-turn continuation" SaaS backend**: a single line of `sse.Handler(sdk, ...)` wires the SDK straight into the frontend

This spotlight answers two independent questions in one shot: **is the frontend typing effect real? How much code does it take to expose the SDK as an SSE endpoint on the backend?**

## 2. One-liner

CLI mode (two prompts share the same sessionKey · run this first):

```bash
go run ./examples/web-chat-stream -agent=codex -mode=cli -timeout=2m
```

Switching to `-agent=claude` / `-agent=cursor` also runs. When the native CLI is unauthenticated or doesn't support streaming, the example **gracefully degrades** — the real failure mode is written into transcript / stderr instead of panicking.

Server mode (HTTP SSE gateway · for browser demos):

```bash
go run ./examples/web-chat-stream -agent=codex -mode=server -addr=:8080
# then open http://localhost:8080/ in the browser and send two prompts in a row
```

Optional: `-cancel-after=2s` demonstrates cancellation behavior in CLI mode (only takes effect on Round 1; Round 2 still runs the normal flow, verifying that "cancellation does not pollute the sessionKey").

## 3. Terminal artifacts + browser interaction

After the CLI mode run, the terminal prints, in this order, three independent screenshot-friendly regions.

### 3.1 Typing effect + Round 2 [session reused] evidence (interleaved stdout + stderr)

**stderr (scenario skeleton)**:

```
[mode=cli · agent=codex · model=gpt-5.4 · capture=.spotlight/web-chat-stream/sse-capture.ndjson]
━━━ Round 1 (sessionKey: examples/web-chat-stream) ━━━━━━━━━━━━━━━━━
[round 1 run=cbe982b8c48bb57954c87d7e…]
[round 1 usage input=18426 output=41 cached=5504]

━━━ Round 2 (same sessionKey) ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
[round 2 run=b3ed77f894d3a6af4939a31b…]
[round 2 usage input=36054 output=56 cached=9984]
[session reused: a7feb790ca6b3de90810228a54e5eef283c567b8a836070404b87b2d23ad25b0 · turns 2 · age 6.113s]
```

**stdout (the "typing effect" the user sees)**:

```
Agents turn intent into action.
Good agents keep state, context, and clear boundaries.
Useful agent systems optimize reliability before autonomy.
Agents work by turning intent into bounded, reliable action.
```

How to read it:

- The terminal visibly shows **token-by-token arrival** (not the entire chunk printed in one go). This codex run produced frames=40 / 25 text deltas / 145 chars, averaging about 5 characters per delta
- The first 3 lines belong to Round 1 (prompt = `"Write three short lines about agents."`)
- The last line belongs to Round 2 (prompt = `"Now add a fourth line that summarizes the three you just wrote."`), and it **explicitly references the "three" from Round 1** — that is the physical evidence of sessionKey continuation
- The trailing `[session reused: <id> · turns 2 · age <Δ>]` on stderr is the anchor the host greps for; once that line appears, the session-reuse story holds
- Failure fallback: when the native CLI is unauthenticated (common with claude/cursor), the example does not panic; it prints `wait_error = agentadaptor: session checkpoint missing` directly into the transcript section

### 3.2 Two-round transcript (replay each round's real shape after the fact)

```
Two-round transcript
─ Round 1 (run=cbe982b8c48bb57954c87d7e… · session=a7feb790ca6b3de90810228a… · reused=false · created=true)
  frames       = 40 (text deltas 25, 145 chars; reasoning 0; tools 0)
  output_head  = Agents turn intent into action.
─ Round 2 (run=b3ed77f894d3a6af4939a31b… · session=a7feb790ca6b3de90810228a… · reused=true · created=false)
  frames       = 27 (text deltas 11, 60 chars; reasoning 0; tools 0)
  output_head  = Agents work by turning intent into bounded, reliable action.
```

One line per round with `frames` / `text deltas` / `reasoning` / `tools` counts plus `output_head`: the host cross-checks "did it really stream" (`frames > 0` and `text deltas > 1`) and "did the session really continue" (Round 2 `reused=true` with the same session id as Round 1).

### 3.3 Session continuation evidence (control-plane truth)

```
Session continuation evidence
  sessionKey       = examples/web-chat-stream
  round 1 session  = a7feb790ca6b3de90810228a… (created=true reused=false)
  round 2 session  = a7feb790ca6b3de90810228a… (created=false reused=true)
  verdict          = continuation OK · same session · turns 2 · age 6.113s
```

Three-banner footer:

```
━━━ Story ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
Two prompts share one sessionKey: tokens stream live and round 2 picks up where round 1 left off.
对位：Web IDE · Cursor-like 聊天面板 · CopilotKit · 客服坐席助手 · 内部 review 助手

━━━ Artifacts ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
- .spotlight/web-chat-stream/sse-capture.ndjson
- .spotlight/web-chat-stream/last-run.md
- examples/web-chat-stream/main.go
- examples/web-chat-stream/walkthrough.md

━━━ Try next ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
$ go run ./examples/web-chat-stream -mode=server -agent=codex
```

### 3.4 SSE wire-sample (Round 1 · `cat | jq` it directly)

`-capture-sse` by default dumps every Round 1 `StreamPayload` into ndjson, **one frame per line**. Pipe it through `cat | jq` and hand it to the backend engineer to verify "can our gateway forward this as is".

```bash
$ head -n 3 .spotlight/web-chat-stream/sse-capture.ndjson | jq -c '{kind: .Kind, name: .Name, runId: .RunID[:12], hasRaw: (.Raw != null)}'
{"kind":"","name":"mcpServer/startupStatus/updated","runId":"cbe982b8c48b","hasRaw":true}
{"kind":"","name":"thread/status/changed","runId":"cbe982b8c48b","hasRaw":true}
{"kind":"run.started","name":"","runId":"cbe982b8c48b","hasRaw":false}
```

How to read it:

- The first 2 frames are codex app-server's own startup meta-events (`mcpServer/startupStatus/updated` / `thread/status/changed`). The bridge passes them through as the `Raw` field, and a host SSE gateway will most likely forward them straight through
- The 3rd frame is the standard SDK event `run.started` (the actual starting point of the "stream" promise that spotlight #2 makes)
- The full 40 frames include `text.content` (25 deltas) + `tool_call.start` (0; this prompt does not call tools) + `run.finished` (with `Usage` counts)

Field semantics: see `docs/streaming.md` §1 (the shape consumed directly via Go channel) and §3 (the HTTP SSE gateway shape).

### 3.5 Browser triple test (server mode · screenshot placeholders)

After starting the server, open `http://localhost:8080/` in the browser and follow these 3 steps for an external demo:

```
[Test 1 · 第一条 prompt 看到打字效果]
┌──────────────────────────────────────────────────────────────────────────┐
│ agent-adaptor · web-chat-stream                                          │
│ POST /v1/chat ⇢ AG-UI events over SSE · sessionKey = demo/web            │
│ ┌──────────────────────────────────────────────────────────────────────┐ │
│ │ [turn 1] > Write three short lines about agents.                     │ │
│ │ Agents turn intent into action.                                      │ │
│ │ Good agents keep state, context, and clear boundar▮       ← 边发边渲染 │ │
│ └──────────────────────────────────────────────────────────────────────┘ │
└──────────────────────────────────────────────────────────────────────────┘

[Test 2 · 第二条 prompt 看到 context 续上]
┌──────────────────────────────────────────────────────────────────────────┐
│ ┌──────────────────────────────────────────────────────────────────────┐ │
│ │ [turn 1] > Write three short lines about agents.                     │ │
│ │ ... three lines ...                                                  │ │
│ │ [done]                                                               │ │
│ │                                                                      │ │
│ │ [turn 2] > Now add a fourth line that summarizes the three.          │ │
│ │ Agents work by turning intent into bounded, reliable action.   ← 引用 │ │
│ │ [done]                                                               │ │
│ └──────────────────────────────────────────────────────────────────────┘ │
└──────────────────────────────────────────────────────────────────────────┘

[Test 3 · 关闭浏览器再开仍续上]
关闭 tab → 重新打开 http://localhost:8080/ → 输入 "Add a fifth line."
sessionKey 仍是 demo/web；如果 server 进程未重启（内存 SessionStore 仍在），agent 再续一行；
如果 server 进程也重启了，因为是 memory store，session 会从头开始（属生产场景应换 Redis/SQL store）。
```

> In a real demo, replace the three ASCII frames above with PNG screenshots. This spotlight does not commit binary screenshots into git; it only describes what should be seen.

## 4. Filesystem artifacts

```
examples/web-chat-stream/
├── main.go              # CLI + server dual-mode implementation (index.html inlined as a constant)
└── walkthrough.md       # static walkthrough (this file)

.spotlight/web-chat-stream/
├── sse-capture.ndjson   # every Round 1 StreamPayload, one frame per line; cat | jq it directly
└── last-run.md          # this run's actual snapshot (dynamic, not committed to git, 5 sections paralleling walkthrough)
```

`main.go` merges two things: the CLI mode consumes `RunHandle.StreamEvents()` directly (the thinnest path in `docs/streaming.md` §1), the server mode mounts `pkg/bridges/sse.Handler(sdk, sse.Options{Protocol: sse.AGUI})` (the HTTP SSE path in §3 of the same file). Both halves together total ≤ 350 lines of Go + ≤ 80 lines of inline HTML.

The fields in `sse-capture.ndjson` come straight from the public `agentadaptor.StreamPayload` type. With this file in hand, a backend host can reverse-engineer "if our SSE gateway only recognizes `Kind` equal to `text.content` / `tool_call.start` / `run.finished`, can we reproduce 95% of the experience".

## 5. Where this lands in your product

| Artifact here | Which surface of your product it maps to |
| --- | --- |
| **stdout typing effect** (`fmt.Print(ev.Delta)`) | Live append in the React frontend's `<ChatMessage>`: subscribe to SSE in a `useEffect`, parse `text/event-stream`, and push deltas straight into the message buffer |
| **stderr Round 2 `[session reused: ...]`** | The "conversation continuity indicator" in your chat panel: when the sessionKey hits, show a green dot + age; when it misses, show a red dot and guide the reviewer to investigate |
| **`Two-round transcript` section** | Backend conversation analytics: one line per conversation with `frames / text_deltas / reasoning / tools`, lets you monitor conversation quality over a window of time |
| **`Session continuation evidence` section** | QA automation: after CI runs `web-chat-stream -mode=cli`, grep for `verdict = continuation OK` as the gate for "sessionKey integration regression" |
| **`sse-capture.ndjson`** | Backend gateway verification: feed this file into your gateway's SSE forwarding logic and use a static fixture to verify field mapping / CORS / keep-alive behavior |
| **`pkg/bridges/sse.Handler(sdk, ...)`** | A single-line integration into your Go HTTP server: `mux.Handle("/v1/chat", sse.Handler(sdk, sse.Options{Protocol: sse.AGUI, CORSAllowedOrigin: "*"}))`, live in under 30 lines |
| **inline `index.html`** | The minimum runnable reference for your React/Vue/CopilotKit project: copy the `<script>` section into your own `ChatPage.tsx` and it just works |

Integration template (frontend React, typing effect in under 30 lines):

```tsx
async function streamChat(prompt: string, sessionKey: string, onDelta: (s: string) => void) {
  const resp = await fetch('/v1/chat', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ prompt, sessionKey }),
  });
  const reader = resp.body!.getReader();
  const decoder = new TextDecoder();
  let buf = '';
  while (true) {
    const { value, done } = await reader.read();
    if (done) break;
    buf += decoder.decode(value, { stream: true });
    let idx;
    while ((idx = buf.indexOf('\n\n')) >= 0) {
      const frame = buf.slice(0, idx);
      buf = buf.slice(idx + 2);
      const line = frame.split('\n').find((l) => l.startsWith('data: '));
      if (!line) continue;
      const ev = JSON.parse(line.slice(6));
      if (ev.type === 'TEXT_MESSAGE_CONTENT' && ev.delta) onDelta(ev.delta);
    }
  }
}
```

Integration template (backend Go, 3-line SDK integration):

```go
sdk := agentadaptor.New(
    agentadaptor.WithDefaultAgent(codex.New(agentadaptor.CodexConfig{Model: "gpt-5.4"})),
    agentadaptor.WithSessionStore(memory.NewSessionStore()), // swap for Redis/SQL store in production
)
mux.Handle("/v1/chat", sse.Handler(sdk, sse.Options{Protocol: sse.AGUI, CORSAllowedOrigin: "*"}))
http.ListenAndServe(":8080", mux)
```

The remaining token rendering, continuation, CORS, and stream cancellation are all handled inside the SDK + bridge.

---

## Appendix · What this spotlight does not show

To keep the "streaming + continuation" story clean, spotlight #2 deliberately **does not** demo the following (head to the matching spotlight):

- dangerous-operation approval / HITL decision audit → [`../human-in-the-loop`](../human-in-the-loop)
- task recipes / profile resources / skills injection → [`../task-recipes`](../task-recipes)
- multi-driver routing / multi-tenant identity / Admin control plane → [`../multi-agent-platform`](../multi-agent-platform) (coming soon)
- 30-second shortest path / four-panel output layering → [`../quickstart-cli`](../quickstart-cli) (coming soon)
