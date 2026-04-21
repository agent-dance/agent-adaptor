# streaming-chat-copilotkit

完整的 AG-UI 前端体验 example：**agent-adaptor（Go） + CopilotKit（React） + codex app-server**。

这份 example 展示了如何用官方 AG-UI 协议和 CopilotKit 组件库，为 `agent-adaptor` 的 streaming 通路搭一个真正的聊天 UI——带 tool-call 可视化、reasoning 折叠、token 级流式。

## 架构

```
Browser
  └─ <CopilotChat /> (React, @copilotkit/react-ui)
         │
         │  POST /api/copilotkit
         ▼
Next.js (web/)
  └─ CopilotRuntime + HttpAgent  (@copilotkit/runtime + @ag-ui/client)
         │
         │  POST /agent  (AG-UI RunAgentInput)
         ▼
Go backend (main.go)
  └─ pkg/bridges/sse + custom DecodeRequest → agent-adaptor SDK
         │
         │  codex app-server --listen stdio://
         ▼
codex subprocess (token-level stream)
```

Runtime 层复用了我们的 `pkg/bridges/sse` Handler；前端通过标准 AG-UI 协议消费，未写任何协议解析代码。

## 先决条件

- Go 1.23+（跑 backend）
- Node.js 20+ 与 npm / pnpm / yarn（跑前端）
- 本机已安装并登录 `codex` CLI（`codex login`）

## 跑起来

```bash
# Terminal 1 — 启 Go backend (监听 :8080)
go run ./examples/streaming-chat-copilotkit

# Terminal 2 — 启 Next.js 前端 (监听 :3000)
cd examples/streaming-chat-copilotkit/web
npm install
npm run dev
```

浏览器打开 http://localhost:3000 ，在聊天框里发消息即可看到 token 级流式响应。

## 关键点说明

### backend 为什么需要自定义 `DecodeRequest`？

默认的 `pkg/bridges/sse.Handler` 接受 `{"prompt": "...", "sessionKey": "..."}`，适合宿主自己直接调用。但 CopilotRuntime 的 `HttpAgent` 按 AG-UI 标准发送的是 `RunAgentInput`：

```json
{
  "threadId": "...",
  "runId": "...",
  "messages": [
    { "id": "...", "role": "user", "content": "hello" }
  ],
  "state": {},
  "tools": [],
  "context": [],
  "forwardedProps": {}
}
```

`main.go` 里的 `decodeRunAgentInput` 把它转换成内部 `sse.Request`：
- 取最后一条 `role=user` 的 message 当作 prompt
- 把 `threadId` 映射为 `sessionKey = "agui/<threadId>"`，让 agent-adaptor 的 session store 跨 turn 复用 codex 线程

如果你需要改协议（支持多模态、tool 回传等），只需扩展 `decodeRunAgentInput`。

### 前端为什么用 Next.js 而不是纯 Vite/CRA？

CopilotKit 依赖一个 **server-side** 的 `CopilotRuntime` 来代理 AG-UI agent 请求。Next.js App Router 的 `/api/copilotkit/route.ts` 是官方首选形态；其它任何 Node 运行时（Express / Fastify / NestJS）用 `copilotRuntimeNode` 入口也行，但 example 选 Next.js 是因为能在一个项目里 serve UI + Runtime，不需要多起一个进程。

### 为什么加了 CORS 头？

Next.js dev server 在 `localhost:3000`，Go backend 在 `localhost:8080`，`/api/copilotkit` 会把请求转给 backend。Runtime 本身就在同 origin，严格说不需要 CORS——但如果你想让浏览器直接 `fetch('http://localhost:8080/agent')`（跳过 Runtime 做调试），CORS 头就派上用场。

## 环境变量

Backend：

- `ADDR`：监听地址，默认 `:8080`
- `CODEX_MODEL`：codex 模型，默认 `gpt-5.4`
- `CORS_ORIGIN`：允许的前端 Origin，默认 `http://localhost:3000`

前端：

- `AGENT_BACKEND_URL`：AG-UI backend 端点，默认 `http://localhost:8080/agent`

## 能看到什么

- **文本流**：token 级打字机效果，用的是 CopilotKit `<CopilotChat>` 默认 UI
- **思考过程**：如果 codex 触发了 reasoning，`REASONING_MESSAGE_*` 事件会被 CopilotChat 渲染为独立卡片
- **工具调用**：`TOOL_CALL_*` 事件渲染为可折叠 tool card，展示参数和结果
- **Usage**：Run 结束后 CopilotChat 会拿到 `RUN_FINISHED.usage`（在开发者面板可查）

## 生产化 checklist

这份 example 为了最小化配置做了些简化，真要上线得注意：

- Session 隔离：所有 thread 都落到同一个 in-memory SessionStore。生产环境用你自己的 `SessionStore` 实现（Redis、Postgres 等）
- 认证：`/agent` 和 `/api/copilotkit` 都应该带 token/cookie 校验，当前 example 全开放
- 沙盒策略：当前写的是 `SandboxReadOnly`，想让 agent 真正改代码时换 `SandboxWorkspaceWrite`
- 并发：每次 chat 启一个 codex app-server 子进程；高并发需要做 worker pool（workstream-streaming-chat.md §11）
- 多 agent：`CopilotRuntime.agents` 支持多个 AG-UI backend，可以同一个 UI 里切换 agent

## 相关文档

- 本仓内：[`docs/streaming.md`](../../docs/streaming.md) / [`docs/workstream-streaming-chat.md`](../../docs/workstream-streaming-chat.md) / [`docs/streaming-adapter-contract.md`](../../docs/streaming-adapter-contract.md)
- CopilotKit：https://docs.copilotkit.ai
- AG-UI 协议：https://docs.ag-ui.com
