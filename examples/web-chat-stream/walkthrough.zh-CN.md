# web-chat-stream · walkthrough

[English Version](./walkthrough.md)

> 这一份是**静态走查**（标准应该长什么样）。每次跑 spotlight 还会生成
> `.spotlight/web-chat-stream/last-run.md` 作为**动态事实**（这次实际看到了什么）。
> PR review 看本文件；事后排错对开两份对照。

## 1. 对位场景

凡是浏览器里要看见 token 一个一个吐、要支持取消、要支持"接着上次那条"的产品：

- **Web IDE / Cursor-like 聊天面板**：右侧 chat 抽屉，token 边到达边渲染，prompt 之间共享上下文
- **CopilotKit 接入**：前端组件直接消费 `text/event-stream`，AG-UI 协议帧零改造
- **客服坐席助手**：每个对话窗口一个 `sessionKey`，agent 在多轮里记住客户身份与历史问题
- **内部 review 助手**：reviewer 一边读，agent 一边出建议；中途取消、重启、续聊都不丢上下文
- **任意"打字机效果 + 多轮续聊"的 SaaS 后台**：1 行 `sse.Handler(sdk, ...)` 把 SDK 直接喂给前端

本 spotlight 一次性回答两个独立问题：**前端打字效果是真的吗？后端把 SDK 暴露成 SSE 端点要写多少代码？**

## 2. 一条命令

CLI 模式（两轮 prompt 共享 sessionKey · 推荐先跑这个）：

```bash
go run ./examples/web-chat-stream -agent=codex -mode=cli -timeout=2m
```

切到 `-agent=claude` / `-agent=cursor` 也能跑。当本机 CLI 未认证或不支持流式时，example 会**优雅降级**——把真实失败模式写进 transcript / stderr 而非 panic。

Server 模式（HTTP SSE 网关 · 浏览器演示用）：

```bash
go run ./examples/web-chat-stream -agent=codex -mode=server -addr=:8080
# 然后浏览器打开 http://localhost:8080/，连续发两条 prompt
```

可选：`-cancel-after=2s` 在 CLI 模式下演示取消行为（仅 Round 1 生效，Round 2 仍按正常流程跑，验证"取消不污染 sessionKey"）。

## 3. 终端产物 + 浏览器交互

CLI 模式跑完终端按这个顺序输出三块独立的可截图区域。

### 3.1 打字效果 + Round 2 [session reused] 证据（stdout + stderr 交错）

**stderr（场景骨架）**：

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

**stdout（用户面前的"打字效果"）**：

```
Agents turn intent into action.
Good agents keep state, context, and clear boundaries.
Useful agent systems optimize reliability before autonomy.
Agents work by turning intent into bounded, reliable action.
```

读法：

- 终端肉眼可辨**逐 token 到达**（不是整段一次 print）。本次 codex 跑出 frames=40 / 25 个 text 增量 / 145 chars，平均每个 delta 约 5 字符
- 前 3 行属于 Round 1（prompt = `"Write three short lines about agents."`）
- 最后 1 行属于 Round 2（prompt = `"Now add a fourth line that summarizes the three you just wrote."`），**显式引用了 Round 1 的"three"** —— 这就是 sessionKey 续聊的物理证据
- stderr 末尾 `[session reused: <id> · turns 2 · age <Δ>]` 是宿主 grep 的锚点，只要这一行出现，session 复用故事就成立
- 失败兜底：当本机 CLI 未认证（claude/cursor 常见），example 不 panic，而是在 transcript 段把 `wait_error = agentadaptor: session checkpoint missing` 直接打出来

### 3.2 Two-round transcript（事后回看每轮真实形状）

```
Two-round transcript
─ Round 1 (run=cbe982b8c48bb57954c87d7e… · session=a7feb790ca6b3de90810228a… · reused=false · created=true)
  frames       = 40 (text deltas 25, 145 chars; reasoning 0; tools 0)
  output_head  = Agents turn intent into action.
─ Round 2 (run=b3ed77f894d3a6af4939a31b… · session=a7feb790ca6b3de90810228a… · reused=true · created=false)
  frames       = 27 (text deltas 11, 60 chars; reasoning 0; tools 0)
  output_head  = Agents work by turning intent into bounded, reliable action.
```

每轮一行 `frames` / `text deltas` / `reasoning` / `tools` 计数 + `output_head`：宿主对照"是否真的流"（`frames > 0` 且 `text deltas > 1`）、"session 是否真的续"（Round 2 `reused=true` 且 session id 与 Round 1 相同）。

### 3.3 Session continuation evidence（控制面真值）

```
Session continuation evidence
  sessionKey       = examples/web-chat-stream
  round 1 session  = a7feb790ca6b3de90810228a… (created=true reused=false)
  round 2 session  = a7feb790ca6b3de90810228a… (created=false reused=true)
  verdict          = continuation OK · same session · turns 2 · age 6.113s
```

三段 banner 收尾：

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

### 3.4 SSE 报文样本（Round 1 · `cat | jq` 直接验证）

`-capture-sse` 默认把 Round 1 的所有 `StreamPayload` dump 进 ndjson，**一行一帧**。可直接 `cat | jq` 给后端工程师验证"我家网关能不能直接转发"。

```bash
$ head -n 3 .spotlight/web-chat-stream/sse-capture.ndjson | jq -c '{kind: .Kind, name: .Name, runId: .RunID[:12], hasRaw: (.Raw != null)}'
{"kind":"","name":"mcpServer/startupStatus/updated","runId":"cbe982b8c48b","hasRaw":true}
{"kind":"","name":"thread/status/changed","runId":"cbe982b8c48b","hasRaw":true}
{"kind":"run.started","name":"","runId":"cbe982b8c48b","hasRaw":false}
```

读法：

- 前 2 帧是 codex app-server 自带的启动元事件（`mcpServer/startupStatus/updated` / `thread/status/changed`）。bridge 把它们透传成 `Raw` 字段，宿主 SSE 网关大概率会直接 forward
- 第 3 帧是 SDK 标准事件 `run.started`（spotlight #2 真正承诺"流"的起点）
- 完整 40 帧里包含 `text.content`（25 个 deltas）+ `tool_call.start`（0 个，本 prompt 不调工具）+ `run.finished`（带 `Usage` 计数）

字段语义参见 `docs/streaming.md` §1（Go channel 直接消费形状）和 §3（HTTP SSE 网关形状）。

### 3.5 浏览器三连测试（server 模式 · 截图占位）

启动 server 后浏览器打开 `http://localhost:8080/`，按下面 3 步操作即可对外 demo：

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

> 真实操作时把上面三块 ASCII 框替换成 PNG 截图。本 spotlight 不在 git 里附二进制截图，只描述应该看到什么。

## 4. 文件系统产物

```
examples/web-chat-stream/
├── main.go              # CLI + server 双模式实现（index.html 内联在常量里）
└── walkthrough.md       # 静态走查（本文件）

.spotlight/web-chat-stream/
├── sse-capture.ndjson   # Round 1 的所有 StreamPayload，一行一帧；cat | jq 直接验证
└── last-run.md          # 本次 run 的真实快照（动态，不入 git，5 段对应 walkthrough）
```

`main.go` 把两件事合并：CLI 模式直接消费 `RunHandle.StreamEvents()`（`docs/streaming.md` §1 的最薄路径），server 模式挂 `pkg/bridges/sse.Handler(sdk, sse.Options{Protocol: sse.AGUI})`（同文件 §3 的 HTTP SSE 路径）。两段加起来 ≤ 350 行 Go + ≤ 80 行内联 HTML。

`sse-capture.ndjson` 的字段直接来自 `agentadaptor.StreamPayload` 公开类型。后端宿主拿到这个文件就可以反算"我家 SSE 网关如果只识别 `Kind` 等于 `text.content` / `tool_call.start` / `run.finished`，能不能复刻 95% 的体验"。

## 5. 落到我家产品的哪里

| 这边的物件 | 对应你家产品的什么 surface |
| --- | --- |
| **stdout 打字效果**（`fmt.Print(ev.Delta)`） | React 前端的 `<ChatMessage>` 实时 append：`useEffect` 里订阅 SSE，`text/event-stream` 解析后把 delta 直接 push 进 message buffer |
| **stderr Round 2 `[session reused: ...]`** | 你家 chat 面板上的"对话连续性指示器"：sessionKey 命中时显示绿点 + age；命中失败显示红点 + 引导 reviewer 排查 |
| **`Two-round transcript` 段** | 后台 conversation analytics：每个 conversation 一行 `frames / text_deltas / reasoning / tools`，可对一段时间内的对话质量做监控 |
| **`Session continuation evidence` 段** | QA 自动化：CI 跑 `web-chat-stream -mode=cli` 后 grep `verdict = continuation OK`，作为"sessionKey 集成回归"门 |
| **`sse-capture.ndjson`** | 后端网关验证：把这份文件喂给你家网关的 SSE 转发逻辑，用静态 fixture 验证字段映射 / CORS / keep-alive 行为 |
| **`pkg/bridges/sse.Handler(sdk, ...)`** | 你家 Go HTTP server 的一行集成：`mux.Handle("/v1/chat", sse.Handler(sdk, sse.Options{Protocol: sse.AGUI, CORSAllowedOrigin: "*"}))`，30 行内上线 |
| **inline `index.html`** | 你家 React/Vue/CopilotKit 工程的最小可运行参考：把 `<script>` 段落复制到自家 `ChatPage.tsx` 即跑通 |

集成模板（前端 React，30 行内打字效果）：

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

集成模板（后端 Go，3 行 SDK 集成）：

```go
sdk := agentadaptor.New(
    agentadaptor.WithDefaultAgent(codex.New(agentadaptor.CodexConfig{Model: "gpt-5.4"})),
    agentadaptor.WithSessionStore(memory.NewSessionStore()), // 生产换 Redis/SQL store
)
mux.Handle("/v1/chat", sse.Handler(sdk, sse.Options{Protocol: sse.AGUI, CORSAllowedOrigin: "*"}))
http.ListenAndServe(":8080", mux)
```

剩下的 token 渲染、续聊、CORS、断流取消，全在 SDK + bridge 里搞定。

---

## 附录 · 不展示什么

为了把"流式 + 续聊"故事讲清楚，spotlight #2 故意**不**演这些（去对应 spotlight 看）：

- 危险操作审批 / HITL 决策审计 → [`../human-in-the-loop`](../human-in-the-loop)
- 任务剧本 / profile resources / skills 注入 → [`../task-recipes`](../task-recipes)
- 多 driver 路由 / 多租户身份 / Admin 控制面 → [`../multi-agent-platform`](../multi-agent-platform)（即将上线）
- 30 秒最短路径 / 输出分层四联屏 → [`../quickstart-cli`](../quickstart-cli)（即将上线）
