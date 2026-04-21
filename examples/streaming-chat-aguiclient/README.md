# streaming-chat-aguiclient

**最小中间层**的 AG-UI 前端 example：Vite + React + `@ag-ui/client` 的 `HttpAgent` 直连 Go backend，**不经过** Next.js、**不使用** CopilotKit。

和 `streaming-chat-copilotkit/` 的关系：

| | streaming-chat-copilotkit | **streaming-chat-aguiclient** |
|---|---|---|
| 前端栈 | Next.js + CopilotKit | Vite + React + `@ag-ui/client` |
| 中间层 | Next.js API route + CopilotRuntime | 无 |
| 依赖规模 | 900+ npm packages | 80+ npm packages |
| 适合 | 希望获得 CopilotKit 完整 UI 组件和生态 | 希望最少中间层、直接对齐 AG-UI 协议 |

## 架构

```
Browser (React + @ag-ui/client HttpAgent)
    │
    │  POST /agent   (AG-UI RunAgentInput)
    ▼
Go backend (agent-adaptor + pkg/bridges/sse)
    │
    │  codex app-server --listen stdio://
    ▼
codex subprocess (token-level stream)
```

`HttpAgent` 是 AG-UI 的**官方浏览器客户端**。它负责：

- 构造 `RunAgentInput` 请求体（`threadId` / `runId` / `messages` / `state` / `tools` 等）
- 解析 AG-UI SSE 响应（通过官方 Zod schema 层 + 内置 verifier）
- 提供 `subscribe(...)` 事件订阅接口供 UI 消费

换句话说：**浏览器和 Go backend 之间走的是 AG-UI 协议原生链路**，没有任何协议转换代理。AG-UI 客户端侧的校验规则（首事件必须 `RUN_STARTED`、`role: "reasoning"` 字面量、生命周期配对等）都会在 HttpAgent 层被执行；Go backend 的 `pkg/bridges/agui` 负责保证输出流合规。

## 先决条件

- Go 1.23+
- Node.js 20+ 与 npm / pnpm
- 本机 `codex` CLI 已登录（`codex login`）

## 跑起来

```bash
# Terminal 1 — Go backend，监听 :8090
go run ./examples/streaming-chat-aguiclient

# Terminal 2 — Vite dev server，监听 :5173
cd examples/streaming-chat-aguiclient/web
npm install
npm run dev
```

浏览器打开 http://localhost:5173 ，发消息即可看到：

- **User / Assistant / Reasoning / Tool** 四种消息卡片分色渲染
- Token 级增量流
- Reasoning（codex 思考过程）独立折叠
- Tool call 以 args / result 卡片呈现

## 代码地图

```
main.go                      # Go backend：pkg/bridges/sse + 自定义 RunAgentInput decoder
web/
├── package.json             # vite + react + @ag-ui/client（核心仅 3 个包）
├── vite.config.ts           # AGENT_BACKEND_URL 环境变量注入
├── index.html
└── src/
    ├── main.tsx             # React 入口
    ├── App.tsx              # HttpAgent + subscribe + 自写 chat UI（~200 行）
    ├── index.css
    └── env.d.ts
```

关键代码在 `src/App.tsx`：

```tsx
import { HttpAgent } from "@ag-ui/client";

const agent = new HttpAgent({ url: __AGENT_BACKEND_URL__ });

// 订阅任何 AG-UI 事件
const sub = agent.subscribe({
  onTextMessageContentEvent({ event }) { /* token 流到 UI */ },
  onReasoningMessageContentEvent({ event }) { /* 思考过程 */ },
  onToolCallStartEvent({ event }) { /* 工具卡片 */ },
  onToolCallResultEvent({ event }) { /* 工具结果 */ },
  onRunFinishedEvent() { /* done */ },
});

// 触发 agent run
agent.addMessage({ id, role: "user", content: text });
await agent.runAgent();
```

`HttpAgent` 内部处理了 AG-UI 的所有标准行为（SSE 解析、事件排序、历史消息维护、断线重连）。宿主只需订阅感兴趣的事件并渲染。

## 环境变量

Backend：
- `ADDR`：监听地址，默认 `:8090`
- `CODEX_MODEL`：codex 模型，默认 `gpt-5.4`
- `CORS_ORIGIN`：允许的前端 origin，默认 `http://localhost:5173`

前端：
- `AGENT_BACKEND_URL`：backend 端点，默认 `http://localhost:8090/agent`

## 为什么要有这个 example（设计意图）

`streaming-chat-copilotkit` 是"完整 AG-UI 前端体验"的标准样板。但它依赖 Next.js + CopilotKit 套件（900+ npm 包 + CopilotRuntime 中间层）。

本 example 对应的使用场景：

1. **嵌入现有 React 应用**：只想加一个聊天组件，不想引入整个 Next.js 工程
2. **最小依赖验证 agent-adaptor 合规性**：中间层越少越能看出 `pkg/bridges/agui` 的输出是否真的符合 AG-UI 规范
3. **移动 / 嵌入式前端**：Vite 输出的 bundle 容易 port 到 React Native / Electron / Tauri
4. **学习 AG-UI 协议**：直连视图最能展示官方 client 是如何消费 AG-UI 流的

两个 example 都长期维护。选哪个取决于宿主的具体生态。

## 相关文档

- [`docs/streaming.md`](../../docs/streaming.md)
- [`docs/workstream-streaming-chat.md`](../../docs/workstream-streaming-chat.md)
- AG-UI 协议：https://docs.ag-ui.com
- `@ag-ui/client`：https://docs.ag-ui.com/sdk/js/client/overview
